package termtheme

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestReadRejectsMissingAndJunk(t *testing.T) {
	dir := t.TempDir()
	if _, ok := Read(""); ok {
		t.Fatal("empty data dir must not report a scheme")
	}
	if _, ok := Read(dir); ok {
		t.Fatal("missing file must not report a scheme")
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("system\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := Read(dir); ok {
		t.Fatal("unrecognized scheme must not report a scheme")
	}
}

func TestWriteConcurrentIsAtomicAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	const writers = 64
	errors := make(chan error, writers)
	var group sync.WaitGroup
	for i := 0; i < writers; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			scheme := SchemeDark
			if i%2 == 0 {
				scheme = SchemeLight
			}
			if err := Write(dir, scheme); err != nil {
				errors <- err
			}
		}(i)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("concurrent write: %v", err)
	}
	if _, ok := Read(dir); !ok {
		t.Fatal("concurrent writes left an invalid terminal theme")
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("stat terminal theme: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("terminal theme permissions = %#o, want 0600", got)
	}
	temporary, err := filepath.Glob(filepath.Join(dir, "."+FileName+".*.tmp"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(temporary) != 0 {
		t.Fatalf("temporary files remain: %v", temporary)
	}
}

func TestApplySetsHintsFromFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"KEEP": "yes"}
	Apply(env, dir)
	if env[EnvTheme] != "light" {
		t.Fatalf("TERM_THEME = %q, want light", env[EnvTheme])
	}
	if env[EnvColorFgBg] != "0;15" {
		t.Fatalf("COLORFGBG = %q, want 0;15", env[EnvColorFgBg])
	}
	if env["KEEP"] != "yes" {
		t.Fatalf("KEEP = %q, want yes", env["KEEP"])
	}
}

func TestApplyPreservesExplicitOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("light\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{EnvTheme: "dark", EnvColorFgBg: "15;0"}
	Apply(env, dir)
	if env[EnvTheme] != "dark" {
		t.Fatalf("TERM_THEME = %q, want caller dark", env[EnvTheme])
	}
	if env[EnvColorFgBg] != "15;0" {
		t.Fatalf("COLORFGBG = %q, want caller 15;0", env[EnvColorFgBg])
	}
}

func TestApplyDarkHint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("DARK"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{}
	Apply(env, dir)
	if env[EnvTheme] != "dark" || env[EnvColorFgBg] != "15;0" {
		t.Fatalf("env = %#v, want dark / 15;0", env)
	}
}
