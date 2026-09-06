package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise Load, the production daemon entry point, alongside the narrow HAO
// resolver. Neither resolver should create a directory or touch account data.
func TestLoadSharesStateRootWithCompanionTools(t *testing.T) {
	base := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defaultRoot, err := DefaultStateDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, data, run, root string }{
		{name: "default", root: defaultRoot},
		{name: "data", data: filepath.Join(base, "isolated", "data"), root: filepath.Join(base, "isolated")},
		{name: "run", run: filepath.Join(base, "discovery", "running.json"), root: filepath.Join(base, "discovery")},
		{name: "both", data: filepath.Join(base, "isolated", "data"), run: filepath.Join(base, "discovery", "running.json"), root: filepath.Join(base, "discovery")},
		{name: "trailing separator", data: filepath.Join(base, "isolated", "data") + string(filepath.Separator), root: filepath.Join(base, "isolated")},
		{name: "surrounding whitespace", data: "  " + filepath.Join(base, "isolated", "data") + "  ", root: filepath.Join(base, "isolated")},
		{name: "whitespace unset", data: "  ", run: "  ", root: defaultRoot},
		{name: "relative", data: "relative-data", root: cwd},
		{name: "relative run precedence", data: "relative-data", run: filepath.Join("relative-root", "running.json"), root: filepath.Join(cwd, "relative-root")},
		{name: "shell text is literal", data: filepath.Join(base, "$(touch forbidden);token=private", "data"), root: filepath.Join(base, "$(touch forbidden);token=private")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", tc.data)
			t.Setenv("AO_RUN_FILE", tc.run)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			root, err := ResolveStateRoot()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.StateDir != tc.root || root != tc.root {
				t.Fatalf("daemon root=%q, companion root=%q, want %q", cfg.StateDir, root, tc.root)
			}
			run, err := ResolveRunFilePath()
			if err != nil {
				t.Fatal(err)
			}
			if run != cfg.RunFilePath {
				t.Fatalf("discovery mismatch %q != %q", run, cfg.RunFilePath)
			}
			if strings.TrimSpace(tc.data) == "" && cfg.DataDir != filepath.Join(tc.root, "data") {
				t.Fatalf("default data %q is outside root %q", cfg.DataDir, tc.root)
			}
			if strings.TrimSpace(tc.run) == "" && cfg.RunFilePath != filepath.Join(tc.root, "running.json") {
				t.Fatalf("default run file %q is outside root %q", cfg.RunFilePath, tc.root)
			}
			if strings.TrimSpace(tc.data) != "" {
				want, _ := filepath.Abs(strings.TrimSpace(tc.data))
				if cfg.DataDir != want {
					t.Fatalf("explicit data changed: %q != %q", cfg.DataDir, want)
				}
			}
			if strings.TrimSpace(tc.run) != "" {
				want, _ := filepath.Abs(strings.TrimSpace(tc.run))
				if cfg.RunFilePath != want {
					t.Fatalf("explicit run file changed: %q != %q", cfg.RunFilePath, want)
				}
			}
		})
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("path resolution performed writes: %v", entries)
	}
}

func TestStateRootRejectsInvalidOverridesBeforeUse(t *testing.T) {
	for _, name := range []string{"AO_DATA_DIR", "AO_RUN_FILE"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", "")
			t.Setenv("AO_RUN_FILE", "")
			// Environment APIs reject NUL themselves, so exercise the shared path
			// validator directly. No filesystem operation is necessary for rejection.
			if _, err := absOverride(name, "private\x00path"); err == nil {
				t.Fatal("accepted NUL path")
			}
		})
	}
}
