package orchestrator

import (
	"context"
	"os"
	"testing"
)

// PBS writes acl.cfg only as acl:<propagate>:<path>:<userlist>:<roles> lines. A host
// acl.cfg with any other line gave no root@pam token entries to keep, and the
// restore's acl.cfg was written without them: the host's retained tokens lost their
// permissions. Such a file now stops the restore before anything is written.
func TestPBSAccessControlStopsOnAHostACLNotInLineFormat(t *testing.T) {
	const stageRoot = "/stage"
	host := map[string]string{
		pbsDomainsCfgPath: "pam: pam\n\tcomment hostpam\n",
		pbsUserCfgPath:    "user: root@pam\n\tenable 1\n\ntoken: root@pam!host\n\tcomment host token\n",
		pbsTokenCfgPath:   "token: root@pam!host\n\tcomment host token\n",
		pbsACLCfgPath:     "# host acl\nacl:1:/:root@pam:Admin\nacl: /datastore/ds1\n\tusers root@pam!host\n\troles DatastoreBackup\n",
	}
	stage := map[string]string{
		stageRoot + "/etc/proxmox-backup/domains.cfg": "pam: pam\n\tcomment backuppam\n",
		stageRoot + "/etc/proxmox-backup/user.cfg":    "user: alice@pbs\n\tenable 1\n",
		stageRoot + "/etc/proxmox-backup/token.cfg":   "token: alice@pbs!tok\n\tcomment alice token\n",
		stageRoot + "/etc/proxmox-backup/acl.cfg":     "acl:1:/:alice@pbs:Admin\n",
	}
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	hostAccessControlFixture(t, fakeFS, host, stage)
	restoreFS = fakeFS

	err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot)
	const want = "read current /etc/proxmox-backup/acl.cfg: line 3 is not an acl: entry"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	assertHostFilesUnchanged(t, fakeFS, host)
}
