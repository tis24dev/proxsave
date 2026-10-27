package orchestrator

import (
	"os"
	"strings"
	"testing"
)

// root@pam's tokens are kept from this host, so their ACL entries come from this
// host's acl.cfg; the backup's entries for root tokens are dropped.
func TestPBSACLKeepsThisHostsRootTokenEntries(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile(pbsACLCfgPath, []byte("acl:1:/datastore/ds1:root@pam!pve,alice@pbs:DatastoreBackup\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := applyPBSACLLineFormat("acl:1:/datastore/ds1:root@pam!old,bob@pbs:DatastoreBackup\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := fakeFS.ReadFile(pbsACLCfgPath)
	want := "acl:1:/datastore/ds1:bob@pbs:DatastoreBackup\nacl:1:/datastore/ds1:root@pam!pve:DatastoreBackup\nacl:1:/:root@pam:Admin\n"
	if string(got) != want {
		t.Fatalf("acl.cfg\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(string(got), "root@pam!old") {
		t.Fatal("the backup's root token entry must not be restored")
	}
}
