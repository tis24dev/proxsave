package storage

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// TestMain points the open-log check at an empty process table: the tests run without
// root, and the real one then cannot be read whole, which the check answers by
// deleting no log at all.
func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "proxsave-proc-")
	if err != nil {
		panic(err)
	}
	restore := safefs.SetProcRootForTest(root)
	code := m.Run()
	restore()
	_ = os.RemoveAll(root)
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
