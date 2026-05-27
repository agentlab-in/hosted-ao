package gitworktree

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestCommandArgs(t *testing.T) {
	repo := "/repo"
	path := "/managed/proj/sess"
	branch := "feature/test"

	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"check ref", checkRefFormatBranchArgs(repo, branch), []string{"-C", repo, "check-ref-format", "--branch", branch}},
		{"rev parse", revParseVerifyArgs(repo, "origin/main"), []string{"-C", repo, "rev-parse", "--verify", "--quiet", "origin/main"}},
		{"add existing", chooseWorktreeAddArgs(repo, path, branch, "", true), []string{"-C", repo, "worktree", "add", path, branch}},
		{"add new", chooseWorktreeAddArgs(repo, path, branch, "origin/main", false), []string{"-C", repo, "worktree", "add", "-b", branch, path, "origin/main"}},
		{"remove", worktreeRemoveForceArgs(repo, path), []string{"-C", repo, "worktree", "remove", "--force", path}},
		{"prune", worktreePruneArgs(repo), []string{"-C", repo, "worktree", "prune"}},
		{"list", worktreeListPorcelainArgs(repo), []string{"-C", repo, "worktree", "list", "--porcelain"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, tc.want) {
				t.Fatalf("args = %#v, want %#v", tc.got, tc.want)
			}
		})
	}
}

func TestBaseRefCandidates(t *testing.T) {
	got := baseRefCandidates("feature/test", "main")
	want := []string{"origin/feature/test", "origin/main", "feature/test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates = %#v, want %#v", got, want)
	}
}

func TestParseWorktreePorcelain(t *testing.T) {
	input := strings.Join([]string{
		"worktree /repo",
		"HEAD abc123",
		"branch refs/heads/main",
		"",
		"worktree /managed/proj/sess1",
		"HEAD def456",
		"branch refs/heads/feature/test",
		"",
		"worktree /managed/proj/sess2",
		"HEAD 789abc",
		"detached",
		"",
		"worktree /bare",
		"bare",
		"",
	}, "\n")

	recs, err := parseWorktreePorcelain(input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 4 {
		t.Fatalf("len = %d, want 4: %#v", len(recs), recs)
	}
	if recs[1].Path != "/managed/proj/sess1" || recs[1].Branch != "feature/test" {
		t.Fatalf("normal record = %#v", recs[1])
	}
	if !recs[2].Detached || recs[2].Branch != "" {
		t.Fatalf("detached record = %#v", recs[2])
	}
	if !recs[3].Bare {
		t.Fatalf("bare record = %#v", recs[3])
	}
}

func TestFilterProjectWorktrees(t *testing.T) {
	root := filepath.Clean("/managed/proj")
	recs := []worktreeRecord{
		{Path: "/repo", Branch: "main"},
		{Path: "/managed/proj/s1", Branch: "feature/one"},
		{Path: "/managed/proj/s2", Branch: ""},
		{Path: "/managed/other/s3", Branch: "feature/three"},
	}
	got := filterProjectWorktrees(recs, root, domain.ProjectID("proj"))
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %#v", len(got), got)
	}
	if got[0].SessionID != "s1" || got[0].Branch != "feature/one" || got[0].ProjectID != "proj" {
		t.Fatalf("first = %#v", got[0])
	}
	if got[1].SessionID != "s2" || got[1].Branch != "" {
		t.Fatalf("second = %#v", got[1])
	}
}

func TestManagedPathSafety(t *testing.T) {
	root := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": root}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path, err := ws.managedPath("proj", "sess")
	if err != nil {
		t.Fatalf("managed path: %v", err)
	}
	if want := filepath.Join(ws.managedRoot, "proj", "sess"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := ws.validateManagedPath(filepath.Join(root, "..", "outside")); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("outside error = %v, want ErrUnsafePath", err)
	}
	if _, err := ws.validateManagedPath("relative/path"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("relative error = %v, want ErrUnsafePath", err)
	}
}

func TestRestoreRefusesNonEmptyUnregisteredPath(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ws.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("worktree " + repo + "\nbranch refs/heads/main\n"), nil
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	_, err = ws.Restore(context.Background(), ports.WorkspaceConfig{ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err == nil || !strings.Contains(err.Error(), "path exists and is not a registered worktree") {
		t.Fatalf("restore error = %v", err)
	}
}

func TestDestroyRefusesStillRegisteredPathAndPreservesDirectory(t *testing.T) {
	root := t.TempDir()
	repo := t.TempDir()
	ws, err := New(Options{ManagedRoot: root, RepoResolver: StaticRepoResolver{"proj": repo}})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	path := filepath.Join(ws.managedRoot, "proj", "sess")
	if err := mkdirFile(path, "keep.txt"); err != nil {
		t.Fatalf("seed path: %v", err)
	}
	ws.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "worktree remove"):
			return []byte("locked"), errors.New("remove failed")
		case strings.Contains(joined, "worktree prune"):
			return nil, nil
		case strings.Contains(joined, "worktree list --porcelain"):
			return []byte("worktree " + path + "\nbranch refs/heads/feature/one\n"), nil
		default:
			return nil, nil
		}
	}
	err = ws.Destroy(context.Background(), ports.WorkspaceInfo{Path: path, ProjectID: "proj", SessionID: "sess", Branch: "feature/one"})
	if err == nil || !strings.Contains(err.Error(), "still registered") {
		t.Fatalf("destroy error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "keep.txt")); statErr != nil {
		t.Fatalf("expected directory to be preserved: %v", statErr)
	}
}

func mkdirFile(dir, name string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644)
}
