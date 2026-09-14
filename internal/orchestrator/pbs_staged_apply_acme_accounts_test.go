package orchestrator

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const stagedAcmeAccountsDir = "/stage/etc/proxmox-backup/acme/accounts"

func withFakeRestoreFS(t *testing.T) *FakeFS {
	t.Helper()
	orig := restoreFS
	fakeFS := NewFakeFS()
	restoreFS = fakeFS
	t.Cleanup(func() {
		restoreFS = orig
		_ = os.RemoveAll(fakeFS.Root)
	})
	return fakeFS
}

// The staged apply mirrors the backup: accounts the backup carries are written, accounts
// only the system has are removed. Anything less would leave the node holding a
// registration the restored node.cfg does not reference.
func TestApplyPBSAcmeAccountsFromStage_MirrorsStagedDirectory(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.WriteFile(stagedAcmeAccountsDir+"/le", []byte(`{"account":{"status":"valid"}}`), 0o600); err != nil {
		t.Fatalf("write staged account: %v", err)
	}
	if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{"status":"stale"}}`), 0o600); err != nil {
		t.Fatalf("write live account: %v", err)
	}
	if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/le-staging", []byte(`{"account":{}}`), 0o600); err != nil {
		t.Fatalf("write live extra account: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyPBSAcmeAccountsFromStage: %v", err)
	}

	data, err := fakeFS.ReadFile("/etc/proxmox-backup/acme/accounts/le")
	if err != nil {
		t.Fatalf("read applied account: %v", err)
	}
	if string(data) != `{"account":{"status":"valid"}}` {
		t.Fatalf("account not replaced by the staged copy: %s", data)
	}
	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le-staging"); err == nil {
		t.Fatal("expected an account absent from the backup to be removed")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat removed account: %v", err)
	}

	info, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts")
	if err != nil {
		t.Fatalf("stat accounts dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("expected the account directory at 0700 (PBS default), got %o", perm)
	}
	if fileInfo, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le"); err != nil {
		t.Fatalf("stat applied account: %v", err)
	} else if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected the account file at 0600, got %o", perm)
	}
}

// An archive that carries no accounts directory says nothing about accounts (it predates
// the fix, or the toggle was off at backup time), so the live registrations must survive.
func TestApplyPBSAcmeAccountsFromStage_AbsentStageLeavesSystemUntouched(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{}}`), 0o600); err != nil {
		t.Fatalf("write live account: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyPBSAcmeAccountsFromStage: %v", err)
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le"); err != nil {
		t.Fatalf("expected the live account untouched when the stage has no accounts directory: %v", err)
	}
}

// A staged directory that exists and is empty is a backup taken with zero accounts, which
// under mirror semantics means the system must end with zero accounts.
func TestApplyPBSAcmeAccountsFromStage_EmptyStagedDirectoryRemovesAll(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.MkdirAll(stagedAcmeAccountsDir, 0o700); err != nil {
		t.Fatalf("mkdir staged accounts: %v", err)
	}
	if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/le", []byte(`{"account":{}}`), 0o600); err != nil {
		t.Fatalf("write live account: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err != nil {
		t.Fatalf("applyPBSAcmeAccountsFromStage: %v", err)
	}

	entries, err := fakeFS.ReadDir("/etc/proxmox-backup/acme/accounts")
	if err != nil {
		t.Fatalf("read accounts dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected every account removed, got %d entries", len(entries))
	}
}

// A staged entry that is not a regular account file makes the whole apply refuse. Applying
// the rest and skipping that one is what the mirror cannot survive: the skipped name would
// be missing from the keep-set and the live account of that name would be deleted.
func TestApplyPBSAcmeAccountsFromStage_RejectsNonRegularStagedEntries(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.WriteFile(stagedAcmeAccountsDir+"/le", []byte(`{"account":{}}`), 0o600); err != nil {
		t.Fatalf("write staged account: %v", err)
	}
	if err := fakeFS.MkdirAll(stagedAcmeAccountsDir+"/nested", 0o700); err != nil {
		t.Fatalf("mkdir staged nested: %v", err)
	}

	err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage")
	if err == nil {
		t.Fatal("expected an error when a staged entry is not a regular account file")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Fatalf("expected the error to name the rejected entry, got: %v", err)
	}

	// Nothing is applied from a refused stage, not even the entries that were valid.
	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts/le"); err == nil {
		t.Fatal("expected no account applied from a refused stage")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat le: %v", err)
	}
}

// The reason the apply refuses: a skipped entry used to take the live account of the same
// name down with it, losing a working registration and its private key while the staged
// replacement was never written either.
func TestApplyPBSAcmeAccountsFromStage_NonRegularStagedEntryKeepsSameNamedLiveAccount(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	live := []byte(`{"account":"live"}`)
	if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/le", live, 0o600); err != nil {
		t.Fatalf("write live account: %v", err)
	}
	if err := fakeFS.MkdirAll(stagedAcmeAccountsDir+"/le", 0o700); err != nil {
		t.Fatalf("mkdir staged le: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err == nil {
		t.Fatal("expected an error when the staged entry is not a regular account file")
	}

	got, err := fakeFS.ReadFile("/etc/proxmox-backup/acme/accounts/le")
	if err != nil {
		t.Fatalf("expected the live account left in place: %v", err)
	}
	if string(got) != string(live) {
		t.Fatalf("expected the live account untouched, got %q", string(got))
	}
}

// A refused stage removes nothing and overwrites nothing, for every account on the node.
func TestApplyPBSAcmeAccountsFromStage_RejectedStageLeavesOtherLiveAccountsIntact(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	live := []byte(`{"account":"live"}`)
	for _, name := range []string{"le", "le-staging"} {
		if err := fakeFS.WriteFile("/etc/proxmox-backup/acme/accounts/"+name, live, 0o600); err != nil {
			t.Fatalf("write live account %s: %v", name, err)
		}
	}
	if err := fakeFS.WriteFile(stagedAcmeAccountsDir+"/le", []byte(`{"account":"staged"}`), 0o600); err != nil {
		t.Fatalf("write staged account: %v", err)
	}
	if err := fakeFS.MkdirAll(stagedAcmeAccountsDir+"/nested", 0o700); err != nil {
		t.Fatalf("mkdir staged nested: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err == nil {
		t.Fatal("expected an error when a staged entry is not a regular account file")
	}

	for _, name := range []string{"le", "le-staging"} {
		got, err := fakeFS.ReadFile("/etc/proxmox-backup/acme/accounts/" + name)
		if err != nil {
			t.Fatalf("expected the live account %s left in place: %v", name, err)
		}
		if string(got) != string(live) {
			t.Fatalf("expected the live account %s untouched, got %q", name, string(got))
		}
	}
}

// The refusal comes before the destination is touched, so a node that never had the
// directory does not end up with an empty one created for an archive that was rejected.
func TestApplyPBSAcmeAccountsFromStage_RejectedStageDoesNotCreateDestinationDir(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.MkdirAll(stagedAcmeAccountsDir+"/nested", 0o700); err != nil {
		t.Fatalf("mkdir staged nested: %v", err)
	}

	if err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage"); err == nil {
		t.Fatal("expected an error when a staged entry is not a regular account file")
	}

	if _, err := fakeFS.Stat("/etc/proxmox-backup/acme/accounts"); err == nil {
		t.Fatal("expected the destination directory not to be created for a refused stage")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat destination: %v", err)
	}
}

// The staged path is a directory on every PBS release; a stage holding a file there is a
// corrupt or hand-made archive and must surface as an error rather than be applied.
func TestApplyPBSAcmeAccountsFromStage_RejectsFileWhereDirectoryExpected(t *testing.T) {
	fakeFS := withFakeRestoreFS(t)

	if err := fakeFS.WriteFile(stagedAcmeAccountsDir, []byte("account: a1\n"), 0o600); err != nil {
		t.Fatalf("write staged file: %v", err)
	}

	err := applyPBSAcmeAccountsFromStage(newTestLogger(), "/stage")
	if err == nil {
		t.Fatal("expected an error when the staged accounts path is not a directory")
	}
}
