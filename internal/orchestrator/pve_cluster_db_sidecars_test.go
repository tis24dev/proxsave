package orchestrator

import (
	"os"
	"testing"
)

// The old cluster database's WAL files are removed before the restore writes
// config.db: SQLite would replay them onto the restored database.
func TestRemovePVEClusterDBSidecars(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	for _, p := range append([]string{"/var/lib/pve-cluster/config.db"}, pveClusterDBSidecars...) {
		if err := fakeFS.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removePVEClusterDBSidecars(newTestLogger()); err != nil {
		t.Fatalf("remove: %v", err)
	}
	for _, p := range pveClusterDBSidecars {
		if _, err := fakeFS.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still there (err %v)", p, err)
		}
	}
	if _, err := fakeFS.Stat("/var/lib/pve-cluster/config.db"); err != nil {
		t.Fatalf("config.db must stay: %v", err)
	}
}
