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

// A staged acl.cfg with a line that is not an acl: entry was found only after
// user.cfg, domains.cfg and token.cfg had been written: a WARNING, the ACL left out,
// and the restored users without their permissions. It now stops the PBS access
// control restore before the first write.
func TestPBSAccessControlStopsOnAStagedACLNotInLineFormatBeforeAnyWrite(t *testing.T) {
	const stageRoot = "/stage"
	host := map[string]string{
		pbsDomainsCfgPath: "pam: pam\n\tcomment hostpam\n",
		pbsUserCfgPath:    "user: root@pam\n\tenable 1\n\ntoken: root@pam!host\n\tcomment host token\n",
		pbsTokenCfgPath:   "token: root@pam!host\n\tcomment host token\n",
		pbsACLCfgPath:     "acl:1:/datastore/ds1:root@pam!host:DatastoreBackup\n",
	}
	stage := map[string]string{
		stageRoot + "/etc/proxmox-backup/domains.cfg": "pam: pam\n\tcomment backuppam\n",
		stageRoot + "/etc/proxmox-backup/user.cfg":    "user: alice@pbs\n\tenable 1\n",
		stageRoot + "/etc/proxmox-backup/token.cfg":   "token: alice@pbs!tok\n\tcomment alice token\n",
		stageRoot + "/etc/proxmox-backup/acl.cfg":     "acl:1:/:alice@pbs:Admin\n\nacl: /datastore\n\tusers alice@pbs\n",
	}
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	hostAccessControlFixture(t, fakeFS, host, stage)
	restoreFS = fakeFS

	err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot)
	const want = "parse staged acl.cfg: line 3 is not an acl: entry"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	assertHostFilesUnchanged(t, fakeFS, host)
}

// PBS leaves acl.cfg at 0 bytes when no ACL is set (measured on pve-test). A backup of
// such a host restored onto one whose root@pam tokens have ACL entries removed this
// host's acl.cfg: the retained tokens authenticated and were denied everywhere. The
// host's root@pam token entries are kept, with root@pam's Admin on "/", as for a
// staged acl.cfg with entries.
func TestPBSAccessControlKeepsHostRootTokenACLWhenTheStagedACLIsEmpty(t *testing.T) {
	const stageRoot = "/stage"
	host := map[string]string{
		pbsUserCfgPath: "user: root@pam\n\tenable 1\n\ntoken: root@pam!host\n\tcomment host token\n",
		pbsACLCfgPath:  "acl:1:/datastore/ds1:root@pam!host,alice@pbs:DatastoreBackup\n",
	}
	stage := map[string]string{
		stageRoot + "/etc/proxmox-backup/user.cfg": "user: alice@pbs\n\tenable 1\n",
		stageRoot + "/etc/proxmox-backup/acl.cfg":  "",
	}
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	hostAccessControlFixture(t, fakeFS, host, stage)
	restoreFS = fakeFS

	if err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := fakeFS.ReadFile(pbsACLCfgPath)
	if err != nil {
		t.Fatalf("acl.cfg after restoring an empty one: %v", err)
	}
	const want = "acl:1:/datastore/ds1:root@pam!host:DatastoreBackup\nacl:1:/:root@pam:Admin\n"
	if string(got) != want {
		t.Fatalf("acl.cfg = %q, want %q", got, want)
	}
}
