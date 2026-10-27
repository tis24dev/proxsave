package orchestrator

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

// readFailFS fails ReadFile of one path with an I/O error, as a failing disk would;
// every other call goes to the FakeFS.
type readFailFS struct {
	*FakeFS
	failPath string
}

func (f *readFailFS) ReadFile(path string) ([]byte, error) {
	if path == f.failPath {
		return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EIO}
	}
	return f.FakeFS.ReadFile(path)
}

// hostAccessControlFixture writes this host's files and the staged ones, and returns
// the host's, to check that nothing was rewritten.
func hostAccessControlFixture(t *testing.T, fakeFS *FakeFS, host, stage map[string]string) {
	t.Helper()
	for _, files := range []map[string]string{host, stage} {
		for path, content := range files {
			if err := fakeFS.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
	}
}

func assertHostFilesUnchanged(t *testing.T, fakeFS *FakeFS, host map[string]string) {
	t.Helper()
	for path, content := range host {
		got, err := fakeFS.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("%s was rewritten:\n%s", path, got)
		}
	}
}

// An access control restore merges this host's copies in: root@pam, its tokens and
// their ACL entries, its TFA, the realms it needs. Only an absent copy reads as
// empty. One that cannot be read fails the restore before anything is written:
// merged as empty, it dropped this host's root@pam tokens and TFA from what was
// written next, while the log said root@pam was preserved.
func TestPVEAccessControlFailsOnAnUnreadableHostFile(t *testing.T) {
	const stageRoot = "/stage"
	host := map[string]string{
		pveDomainsCfgPath: "pam: pam\n\tcomment hostpam\n",
		pveUserCfgPath: "user:root@pam:1:0:::root@host.example:HostRoot::\n" +
			"token:root@pam!host:0:1:host token:\n\n" +
			"acl:1:/:root@pam!host:PVEAuditor:\n",
		pveTokenCfgPath: "root@pam!host 11111111-1111-1111-1111-111111111111\n",
		pveTFACfgPath:   `{"users":{"root@pam":{"totp":[{"id":"host"}]}}}`,
	}
	stage := map[string]string{
		stageRoot + "/etc/pve/domains.cfg": "pam: pam\n\tcomment backuppam\n",
		stageRoot + "/etc/pve/user.cfg": "user:alice@pve:1:0:::a@x.y:Alice:x:\n" +
			"user:root@pam:0:0:::old@x.y:OldRoot::\n" +
			"token:root@pam!old:0:1:old token:\n",
		stageRoot + "/etc/pve/priv/token.cfg": "root@pam!old 44444444-4444-4444-4444-444444444444\n",
		stageRoot + "/etc/pve/priv/tfa.cfg":   `{"users":{"root@pam":{"totp":[{"id":"old"}]}}}`,
	}
	for _, failPath := range []string{pveUserCfgPath, pveTokenCfgPath, pveTFACfgPath, pveDomainsCfgPath} {
		t.Run(failPath, func(t *testing.T) {
			origFS := restoreFS
			t.Cleanup(func() { restoreFS = origFS })
			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			hostAccessControlFixture(t, fakeFS, host, stage)
			restoreFS = &readFailFS{FakeFS: fakeFS, failPath: failPath}

			err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot)
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("err = %v, want the read failure of %s", err, failPath)
			}
			assertHostFilesUnchanged(t, fakeFS, host)
		})
	}

	// A fresh node has no token.cfg and no tfa.cfg: absent reads as empty.
	t.Run("fresh node", func(t *testing.T) {
		origFS := restoreFS
		t.Cleanup(func() { restoreFS = origFS })
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		hostAccessControlFixture(t, fakeFS, map[string]string{
			pveDomainsCfgPath: host[pveDomainsCfgPath],
			pveUserCfgPath:    "user:root@pam:1:0::::::\n",
		}, stage)
		restoreFS = fakeFS
		if err := applyPVEAccessControlFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
			t.Fatalf("err = %v, want nil on a node without token.cfg and tfa.cfg", err)
		}
	})
}

// The same on PBS: user.cfg, domains.cfg, token.cfg and acl.cfg are read before the
// first write, and one that cannot be read stops the restore.
func TestPBSAccessControlFailsOnAnUnreadableHostFile(t *testing.T) {
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
		stageRoot + "/etc/proxmox-backup/acl.cfg":     "acl:1:/:alice@pbs:Admin\n",
	}
	for _, failPath := range []string{pbsUserCfgPath, pbsDomainsCfgPath, pbsTokenCfgPath, pbsACLCfgPath} {
		t.Run(failPath, func(t *testing.T) {
			origFS := restoreFS
			t.Cleanup(func() { restoreFS = origFS })
			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			hostAccessControlFixture(t, fakeFS, host, stage)
			restoreFS = &readFailFS{FakeFS: fakeFS, failPath: failPath}

			err := applyPBSAccessControlFromStage(context.Background(), newTestLogger(), stageRoot)
			if !errors.Is(err, syscall.EIO) {
				t.Fatalf("err = %v, want the read failure of %s", err, failPath)
			}
			assertHostFilesUnchanged(t, fakeFS, host)
		})
	}
}
