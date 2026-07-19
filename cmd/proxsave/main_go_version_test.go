package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// The declared runtime minimum must track the go.mod `go` directive, so a
// toolchain bump cannot silently leave the version-check floor stale.
func TestGoRuntimeMinVersionMatchesGoMod(t *testing.T) {
	root := repoRootForVersionTest(t)
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)`).FindSubmatch(data)
	if m == nil {
		t.Fatalf("go.mod has no `go X.Y.Z` directive:\n%s", data)
	}
	want := string(m[1])
	if goRuntimeMinVersion != want {
		t.Fatalf("goRuntimeMinVersion = %q, want %q (go.mod directive)", goRuntimeMinVersion, want)
	}
}

// repoRootForVersionTest walks up from the package dir until it finds go.mod.
func repoRootForVersionTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s (runtime %s)", dir, runtime.Version())
		}
		dir = parent
	}
}
