package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

// The recreated datastore lock is an empty file, 0600 before the chown to the
// datastore owner, never created over an existing one, never in an unsafe directory.
func TestCreatePBSDatastoreLockFile(t *testing.T) {
	lock := filepath.Join(t.TempDir(), ".lock")
	if err := createPBSDatastoreLockFile(lock); err != nil {
		t.Fatalf("createPBSDatastoreLockFile: %v", err)
	}
	info, err := os.Stat(lock)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock = %v size %d, want an empty 0600 file", info.Mode(), info.Size())
	}
	if err := createPBSDatastoreLockFile(lock); err == nil {
		t.Fatalf("a second create over the existing lock must fail")
	}
	if err := createPBSDatastoreLockFile("/etc/proxsave-test/.lock"); err == nil {
		t.Fatalf("a lock in a system-critical directory must be refused")
	}
}
