package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// testProcRootEnv carries the fake process table to re-executions of this test binary.
const testProcRootEnv = "PROXSAVE_TEST_PROC_ROOT"

// TestMain points the open-log check at an empty process table: the tests run without
// root, and the real one then cannot be read whole, which the check answers by
// deleting no log at all.
//
// A re-execution of this test binary (a helper subprocess) inherits the table through
// testProcRootEnv instead of making its own: it exits through os.Exit inside a test, so
// a table of its own would never be removed.
func TestMain(m *testing.M) {
	root, owned := os.Getenv(testProcRootEnv), false
	if root == "" {
		var err error
		if root, err = os.MkdirTemp("", "proxsave-proc-"); err != nil {
			panic(err)
		}
		owned = true
		if err := os.Setenv(testProcRootEnv, root); err != nil {
			panic(err)
		}
	}
	restore := safefs.SetProcRootForTest(root)
	code := m.Run()
	restore()
	if owned {
		_ = os.RemoveAll(root)
	}
	os.Exit(code)
}

// holdOpenForTest makes a process of the fake table hold each of paths open for the
// rest of the test.
func holdOpenForTest(t *testing.T, paths ...string) {
	t.Helper()
	root := t.TempDir()
	fdDir := filepath.Join(root, "4242", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		if err := os.Symlink(path, filepath.Join(fdDir, strconv.Itoa(i+3))); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(safefs.SetProcRootForTest(root))
}
