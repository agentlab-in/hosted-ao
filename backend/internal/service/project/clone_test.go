package project

// Internal tests for the clone flow's rejection paths. They exercise
// cloneRepository, parseCloneURL, sanitizeDirComponent, classifyCloneError, and
// sanitizeOriginURL directly, which the external project_test package cannot
// reach. End-to-end coverage through Service.Add lives in service_test.go.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// wantCloneCode asserts err is an *apierr.Error with the given code and that it
// never carries raw git output.
func wantCloneCode(t *testing.T, err error, code string) *apierr.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", code)
	}
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *apierr.Error", err, err)
	}
	if apiErr.Code != code {
		t.Fatalf("code = %q, want %q (message %q)", apiErr.Code, code, apiErr.Message)
	}
	if strings.Contains(apiErr.Message, "fatal:") || strings.Contains(apiErr.Message, "error:") {
		t.Fatalf("message leaked raw git output: %q", apiErr.Message)
	}
	return apiErr
}

// gitSource creates a real git repository at <base>/<owner>/<repo> so a
// file:// clone URL yields a predictable <owner>-<repo> destination.
func gitSource(t *testing.T, owner, repo string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), owner, repo)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	git("init", "-b", "main", dir)
	git("-C", dir, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "-m", "init")
	return dir
}

func TestCloneRepository_NotConfigured(t *testing.T) {
	// An empty reposRoot means the daemon was started without a clone
	// destination, so cloning by URL is unavailable rather than invalid.
	for _, reposRoot := range []string{"", "   "} {
		m := &Service{reposRoot: reposRoot}
		_, err := m.cloneRepository(context.Background(), "https://github.com/o/r.git")
		wantCloneCode(t, err, "CLONE_NOT_CONFIGURED")
	}
}

func TestCloneRepository_DestUnavailable(t *testing.T) {
	// reposRoot below a regular file: MkdirAll cannot create it (ENOTDIR).
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Service{reposRoot: filepath.Join(file, "repos")}
	_, err := m.cloneRepository(context.Background(), "https://github.com/o/r.git")
	wantCloneCode(t, err, "CLONE_DEST_UNAVAILABLE")
}

func TestCloneRepository_DestStatUnavailable(t *testing.T) {
	// This branch needs a directory whose permissions make Stat fail, which
	// only Unix modes can express: Windows Chmod toggles a read-only bit that
	// directories ignore, and root bypasses the check entirely. Without the
	// guards the clone would proceed and reach for the network.
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions are not enforced this way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the stat cannot be made to fail")
	}
	// reposRoot exists but is not searchable, so MkdirAll succeeds (the
	// directory is already there) and the destination Stat fails with EACCES,
	// which is neither success nor os.ErrNotExist.
	reposRoot := filepath.Join(t.TempDir(), "repos")
	if err := os.Mkdir(reposRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(reposRoot, 0o700) })

	m := &Service{reposRoot: reposRoot}
	_, err := m.cloneRepository(context.Background(), "https://github.com/o/r.git")
	wantCloneCode(t, err, "CLONE_DEST_UNAVAILABLE")
}

func TestCloneRepository_Timeout(t *testing.T) {
	// A loopback server that never answers, so git blocks until the clone's own
	// deadline fires. Shrinking cloneTimeout keeps the test fast.
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	t.Cleanup(func() {
		close(blocked)
		srv.Close()
	})

	originalTimeout, originalWaitDelay := cloneTimeout, cloneWaitDelay
	cloneTimeout, cloneWaitDelay = 500*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { cloneTimeout, cloneWaitDelay = originalTimeout, originalWaitDelay })

	reposRoot := t.TempDir()
	m := &Service{reposRoot: reposRoot}
	_, err := m.cloneRepository(context.Background(), srv.URL+"/acme/widgets.git")
	apiErr := wantCloneCode(t, err, "CLONE_TIMEOUT")
	if !strings.Contains(apiErr.Message, cloneTimeout.String()) {
		t.Fatalf("message %q does not mention the timeout %s", apiErr.Message, cloneTimeout)
	}
	if _, statErr := os.Stat(filepath.Join(reposRoot, "acme-widgets")); !os.IsNotExist(statErr) {
		t.Fatalf("expected the timed-out clone destination to be cleaned up, stat err = %v", statErr)
	}
}

func TestCloneRepository_TimeoutSurvivesCallerCancellation(t *testing.T) {
	// The clone deliberately runs on a context.WithoutCancel of the request
	// context, so a cancelled caller must not turn into a spurious CLONE_TIMEOUT
	// before git has even been given its own deadline.
	src := gitSource(t, "acme", "widgets")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reposRoot := t.TempDir()
	m := &Service{reposRoot: reposRoot}
	dest, err := m.cloneRepository(ctx, "file://"+filepath.ToSlash(src))
	if err != nil {
		t.Fatalf("cloneRepository with a cancelled caller context: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dest, ".git")); statErr != nil {
		t.Fatalf("cloned repo missing .git: %v", statErr)
	}
}

func TestCloneRepository_RepoNotFound(t *testing.T) {
	// 404 on the smart-http discovery request is exactly what a missing or
	// inaccessible repository returns.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	m := &Service{reposRoot: t.TempDir()}
	_, err := m.cloneRepository(context.Background(), srv.URL+"/acme/widgets.git")
	apiErr := wantCloneCode(t, err, "CLONE_REPO_NOT_FOUND")
	if strings.Contains(apiErr.Message, srv.URL) {
		t.Fatalf("message leaked the clone URL: %q", apiErr.Message)
	}
}

func TestCloneRepository_UnclassifiedFailureIsGeneric(t *testing.T) {
	// A directory that exists but is not a git repository: git fails with
	// "does not exist", which matches no classifier branch.
	notARepo := filepath.Join(t.TempDir(), "acme", "widgets")
	if err := os.MkdirAll(notARepo, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Service{reposRoot: t.TempDir()}
	_, err := m.cloneRepository(context.Background(), "file://"+filepath.ToSlash(notARepo))
	wantCloneCode(t, err, "CLONE_FAILED")
}

func TestClassifyCloneError(t *testing.T) {
	// Representative real git output for each branch.
	for _, tc := range []struct {
		name     string
		output   string
		wantCode string
	}{
		{
			name:     "https no credentials",
			output:   "fatal: could not read Username for 'https://github.com': terminal prompts disabled",
			wantCode: "CLONE_AUTH_FAILED",
		},
		{
			name:     "https bad credentials",
			output:   "remote: Invalid username or password.\nfatal: Authentication failed for 'https://github.com/o/r.git/'",
			wantCode: "CLONE_AUTH_FAILED",
		},
		{
			name:     "ssh no key",
			output:   "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
			wantCode: "CLONE_AUTH_FAILED",
		},
		{
			name:     "dns failure",
			output:   "fatal: unable to access 'https://githu.com/o/r.git/': Could not resolve host: githu.com",
			wantCode: "CLONE_HOST_NOT_FOUND",
		},
		{
			name:     "dns failure glibc wording",
			output:   "ssh: Could not resolve hostname host: Temporary failure in name resolution",
			wantCode: "CLONE_HOST_NOT_FOUND",
		},
		{
			name:     "unknown host key",
			output:   "Host key verification failed.\nfatal: Could not read from remote repository.",
			wantCode: "CLONE_HOST_KEY_UNVERIFIED",
		},
		{
			name:     "repo missing",
			output:   "remote: Repository not found.\nfatal: repository 'https://github.com/o/r.git/' not found",
			wantCode: "CLONE_REPO_NOT_FOUND",
		},
		{
			name:     "http 404",
			output:   "fatal: repository 'http://127.0.0.1:1/o/r.git/' not found",
			wantCode: "CLONE_REPO_NOT_FOUND",
		},
		{
			name:     "anything else",
			output:   "fatal: early EOF\nfatal: index-pack failed",
			wantCode: "CLONE_FAILED",
		},
		{
			name:     "empty output",
			output:   "",
			wantCode: "CLONE_FAILED",
		},
		{
			name:     "mixed case is still classified",
			output:   "FATAL: AUTHENTICATION FAILED for 'https://github.com/o/r.git/'",
			wantCode: "CLONE_AUTH_FAILED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := classifyCloneError(tc.output)
			if err.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", err.Code, tc.wantCode)
			}
			// No branch, including the default, may echo git's own text.
			if err.Message == "" {
				t.Fatal("message is empty, callers need remediation text")
			}
			for _, line := range strings.Split(tc.output, "\n") {
				if line = strings.TrimSpace(line); line != "" && strings.Contains(err.Message, line) {
					t.Fatalf("message %q leaked raw git output %q", err.Message, line)
				}
			}
			if err.Details != nil {
				t.Fatalf("details = %v, want nil", err.Details)
			}
		})
	}
}

func TestParseCloneURL(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		wantOwner string
		wantRepo  string
	}{
		{name: "https with .git", raw: "https://github.com/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "https without .git", raw: "https://github.com/acme/widgets", wantOwner: "acme", wantRepo: "widgets"},
		{name: "https with trailing slash", raw: "https://github.com/acme/widgets/", wantOwner: "acme", wantRepo: "widgets"},
		{name: "https with credentials", raw: "https://x-access-token:ghp_TOKEN@github.com/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "surrounding whitespace", raw: "  https://github.com/acme/widgets.git\n", wantOwner: "acme", wantRepo: "widgets"},
		{name: "scp-like shorthand", raw: "git@github.com:acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "scp-like without .git", raw: "git@github.com:acme/widgets", wantOwner: "acme", wantRepo: "widgets"},
		{name: "scp-like with a port-looking host", raw: "git@my-host.example.com:acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "ssh scheme", raw: "ssh://git@github.com/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "ssh scheme with port", raw: "ssh://git@github.com:2222/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "git scheme", raw: "git://github.com/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "file scheme", raw: "file:///srv/git/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		// Only the last two segments are used, which is what keeps a deep or
		// hostile path from reaching the destination name.
		{name: "self-hosted subgroups use the last two segments", raw: "https://gitlab.example.com/group/subgroup/acme/widgets.git", wantOwner: "acme", wantRepo: "widgets"},
		{name: "dots inside a segment are allowed", raw: "https://github.com/acme/widgets.js.git", wantOwner: "acme", wantRepo: "widgets.js"},
		{name: "double dot inside a segment is allowed", raw: "https://github.com/ac..me/wid..gets.git", wantOwner: "ac..me", wantRepo: "wid..gets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseCloneURL(tc.raw)
			if err != nil {
				t.Fatalf("parseCloneURL(%q): %v", tc.raw, err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Fatalf("parseCloneURL(%q) = %q, %q, want %q, %q", tc.raw, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

func TestParseCloneURL_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "empty", raw: ""},
		{name: "whitespace only", raw: "   \t\n"},
		{name: "not a url", raw: "not-a-git-url"},
		{name: "bare host", raw: "https://github.com"},
		{name: "one segment", raw: "https://github.com/widgets.git"},
		{name: "scp-like with one segment", raw: "git@github.com:widgets.git"},
		{name: "scheme relative", raw: "//github.com/acme/widgets.git"},
		{name: "plain relative path", raw: "acme/widgets.git"},
		{name: "absolute path", raw: "/srv/git/acme/widgets.git"},

		// Traversal segments that reach sanitizeDirComponent verbatim.
		{name: "traversal in the repo segment", raw: "https://host/acme/...git"},
		{name: "dot repo segment", raw: "https://host/acme/."},
		{name: "dotdot repo segment", raw: "https://host/acme/.."},
		{name: "dot owner segment", raw: "https://host/./widgets.git"},
		{name: "dotdot owner segment", raw: "https://host/../widgets.git"},
		{name: "backslash traversal", raw: `https://host/acme/..\..\windows.git`},
		{name: "tilde expansion", raw: "https://host/acme/~root.git"},

		// Injection-shaped segments. safeDirComponent admits none of these.
		{name: "space in the repo segment", raw: "https://host/acme/wid gets.git"},
		{name: "shell metacharacters", raw: "https://host/acme/widgets;rm -rf x.git"},
		{name: "command substitution", raw: "https://host/acme/$(whoami).git"},
		{name: "newline in the repo segment", raw: "https://host/acme/widgets\nx.git"},
		{name: "null byte", raw: "https://host/acme/widgets\x00.git"},
		{name: "colon in the repo segment", raw: "https://host/acme/wid:gets.git"},
		{name: "non-ascii", raw: "https://host/acme/wídgets.git"},
		{name: "empty repo segment after stripping .git", raw: "https://host/acme/.git"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseCloneURL(tc.raw)
			if err == nil {
				t.Fatalf("parseCloneURL(%q) = %q, %q, want an error", tc.raw, owner, repo)
			}
		})
	}
}

// TestParseCloneURL_HostileInputsStayInsideReposRoot locks in the traversal
// defence. A hostile URL is not always rejected: because only the last two path
// segments are ever used, something like https://host/../../etc/x.git reduces to
// the inert pair ("etc", "x"). That is the property that matters, so assert it
// directly: whatever parseCloneURL accepts must join into a direct child of
// reposRoot.
func TestParseCloneURL_HostileInputsStayInsideReposRoot(t *testing.T) {
	const reposRoot = "/srv/ao/repos"
	for _, tc := range []struct {
		name string
		raw  string
		want string // the <owner>-<repo> destination name, "" when rejected
	}{
		{name: "ordinary https", raw: "https://github.com/acme/widgets.git", want: "acme-widgets"},
		{name: "deep path uses the last two segments", raw: "https://gitlab.example.com/group/subgroup/acme/widgets.git", want: "acme-widgets"},
		{name: "scp-like", raw: "git@github.com:acme/widgets.git", want: "acme-widgets"},
		{name: "ssh with a port and dots", raw: "ssh://git@github.com:2222/ac..me/wid..gets.git", want: "ac..me-wid..gets"},
		{name: "credentialed https", raw: "https://x-access-token:ghp_TOKEN@github.com/acme/widgets.git", want: "acme-widgets"},
		{name: "file url", raw: "file:///srv/git/acme/widgets.git", want: "acme-widgets"},

		// Traversal attempts. These reduce, they do not escape.
		{name: "parent traversal", raw: "https://host/../../etc/x.git", want: "etc-x"},
		{name: "leading double slash", raw: "https://host//etc/passwd", want: "etc-passwd"},
		{name: "percent-encoded traversal", raw: "https://host/acme/%2e%2e%2fetc%2fpasswd.git", want: "etc-passwd"},
		{name: "percent-encoded slash", raw: "https://host/acme/wid%2Fgets.git", want: "wid-gets"},
		{name: "scp-like traversal", raw: "git@host:../../etc/x.git", want: "etc-x"},
		{name: "scp-like absolute path", raw: "git@host:/etc/passwd.git", want: "etc-passwd"},
		{name: "deep traversal", raw: "https://host/a/../../../../../../etc/shadow", want: "etc-shadow"},
		{name: "traversal that reduces to nothing", raw: "https://host/../..", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseCloneURL(tc.raw)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("parseCloneURL(%q) = %q, %q, want an error", tc.raw, owner, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCloneURL(%q): %v", tc.raw, err)
			}
			if got := owner + "-" + repo; got != tc.want {
				t.Fatalf("parseCloneURL(%q) destination name = %q, want %q", tc.raw, got, tc.want)
			}
			dest := filepath.Join(reposRoot, owner+"-"+repo)
			if filepath.Dir(dest) != filepath.FromSlash(reposRoot) {
				t.Fatalf("parseCloneURL(%q) escaped reposRoot: dest = %q", tc.raw, dest)
			}
			if filepath.Clean(dest) != dest {
				t.Fatalf("parseCloneURL(%q) produced an uncleaned dest = %q", tc.raw, dest)
			}
		})
	}
}

func TestSanitizeDirComponent(t *testing.T) {
	for _, ok := range []string{"widgets", "widgets.js", "ac..me", "a", "_", "-", ".hidden", "A1-b_2.c", "..a"} {
		if got, err := sanitizeDirComponent(ok); err != nil || got != ok {
			t.Fatalf("sanitizeDirComponent(%q) = %q, %v, want %q, nil", ok, got, err, ok)
		}
	}
	// Leading and trailing whitespace is trimmed, not rejected.
	if got, err := sanitizeDirComponent("  widgets  "); err != nil || got != "widgets" {
		t.Fatalf(`sanitizeDirComponent("  widgets  ") = %q, %v, want "widgets", nil`, got, err)
	}
	for _, bad := range []string{
		"", "   ", ".", "..", "a/b", `a\b`, "a b", "a;b", "a|b", "a&b", "$a", "a$", "`a`", "a'b", `a"b`,
		"a*b", "a?b", "a:b", "a\nb", "a\x00b", "~", "~root", "a%2Fb", "wídgets", "..\\..", "../..",
	} {
		if got, err := sanitizeDirComponent(bad); err == nil {
			t.Fatalf("sanitizeDirComponent(%q) = %q, want an error", bad, got)
		}
	}
}

func TestSanitizeOriginURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "https token as username and password",
			raw:  "https://x-access-token:ghp_SECRET@github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "https token as username only",
			raw:  "https://ghp_SECRET@github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "https user and password",
			raw:  "https://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},
		{
			name: "https percent-encoded password",
			raw:  "https://alice:p%40ss%3Aword@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},
		{
			name: "http is treated the same as https",
			raw:  "http://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "http://git.example.com/acme/widgets.git",
		},
		{
			name: "port and query are preserved",
			raw:  "https://alice:s3cr3t@git.example.com:8443/acme/widgets.git",
			want: "https://git.example.com:8443/acme/widgets.git",
		},
		{
			name: "uppercase scheme",
			raw:  "HTTPS://alice:s3cr3t@git.example.com/acme/widgets.git",
			want: "https://git.example.com/acme/widgets.git",
		},

		// Everything below must survive untouched.
		{
			name: "plain https is unchanged",
			raw:  "https://github.com/acme/widgets.git",
			want: "https://github.com/acme/widgets.git",
		},
		{
			name: "scp-like shorthand keeps its ssh user",
			raw:  "git@github.com:acme/widgets.git",
			want: "git@github.com:acme/widgets.git",
		},
		{
			name: "ssh scheme keeps its login name",
			raw:  "ssh://git@github.com/acme/widgets.git",
			want: "ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "ssh scheme with a port keeps its login name",
			raw:  "ssh://git@github.com:2222/acme/widgets.git",
			want: "ssh://git@github.com:2222/acme/widgets.git",
		},
		{
			name: "ssh scheme drops a password but keeps the login name",
			raw:  "ssh://git:s3cr3t@github.com/acme/widgets.git",
			want: "ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "git+ssh keeps its login name",
			raw:  "git+ssh://git@github.com/acme/widgets.git",
			want: "git+ssh://git@github.com/acme/widgets.git",
		},
		{
			name: "local path is unchanged",
			raw:  "/srv/git/acme/widgets.git",
			want: "/srv/git/acme/widgets.git",
		},
		{
			name: "file url is unchanged",
			raw:  "file:///srv/git/acme/widgets.git",
			want: "file:///srv/git/acme/widgets.git",
		},
		{
			name: "empty is unchanged",
			raw:  "",
			want: "",
		},
		{
			name: "an at sign in the path is not userinfo",
			raw:  "https://git.example.com/acme/wid@gets.git",
			want: "https://git.example.com/acme/wid@gets.git",
		},
		{
			name: "unparseable input is returned verbatim",
			raw:  "git@host:acme/wid\x7fgets.git",
			want: "git@host:acme/wid\x7fgets.git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeOriginURL(tc.raw); got != tc.want {
				t.Fatalf("sanitizeOriginURL(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSanitizeOriginURL_LeaksNoSecret(t *testing.T) {
	const secret = "ghp_SUPERSECRET"
	for _, raw := range []string{
		"https://x-access-token:" + secret + "@github.com/acme/widgets.git",
		"https://" + secret + "@github.com/acme/widgets.git",
		"https://alice:" + secret + "@git.example.com:8443/acme/widgets.git",
		"ssh://git:" + secret + "@github.com/acme/widgets.git",
	} {
		if got := sanitizeOriginURL(raw); strings.Contains(got, secret) {
			t.Fatalf("sanitizeOriginURL(%q) still contains the secret: %q", raw, got)
		}
	}
}
