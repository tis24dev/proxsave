package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// `dir: local` exists on every node and in every backup, so the create-only
// apply failed on it on every live restore ("create storage failed: storage ID
// 'local' already defined", probed 2026-09-02) and existing definitions were
// never updated (fable-check bug 4). The jobs apply twenty lines away has had
// the create-then-set fallback all along; storage.cfg now gets the same one.
func TestApplyStorageCfgFallsBackToSetOnAnExistingStorage(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pvesh := newSchemaAwarePvesh("local")
	restoreCmd = pvesh

	cfg := strings.Join([]string{
		"dir: local",
		"\tpath /var/lib/vz",
		"\tcontent iso,vztmpl,backup",
		"",
		"nfs: backup_ext",
		"\tserver 10.0.0.1",
		"\texport /srv/backups",
		"",
	}, "\n")
	if err := fakeFS.AddFile("/stage/etc/pve/storage.cfg", []byte(cfg)); err != nil {
		t.Fatal(err)
	}

	logger := logging.New(types.LogLevelDebug, false)
	applied, failed, err := applyStorageCfg(context.Background(), "/stage/etc/pve/storage.cfg", logger)
	if err != nil {
		t.Fatalf("applyStorageCfg: %v", err)
	}
	if applied != 2 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 2/0: the existing 'local' must fall back to set", applied, failed)
	}

	calls := strings.Join(pvesh.calls, "\n")
	if !strings.Contains(calls, "pvesh set /storage/local") {
		t.Fatalf("no set fallback on the existing storage; calls:\n%s", calls)
	}
	if strings.Contains(calls, "set /storage/local --storage=") || strings.Contains(calls, "set /storage/local --type=") {
		t.Fatalf("the set fallback re-sends create-only keys (--storage/--type); calls:\n%s", calls)
	}
	if !strings.Contains(calls, "pvesh create /storage --storage=backup_ext") {
		t.Fatalf("the new storage must still go through create; calls:\n%s", calls)
	}
}

func TestApplyStorageCfgStillFailsWhenCreateAndSetBothFail(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	// The fake refuses set on a storage it does not know; forcing the create to
	// also fail needs an exact-key runner layered on top - simplest: an id the
	// fake treats as existing for create but unknown for set cannot exist, so use
	// a FakeCommandRunner that fails both calls explicitly.
	restoreCmd = &FakeCommandRunner{Errors: map[string]error{
		"pvesh create /storage --storage=local --type=dir --path=/var/lib/vz": errString("storage ID 'local' already defined"),
		"pvesh set /storage/local --path=/var/lib/vz":                         errString("update storage failed: permission denied"),
	}}

	cfg := "dir: local\n\tpath /var/lib/vz\n"
	if err := fakeFS.AddFile("/stage/etc/pve/storage.cfg", []byte(cfg)); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	applied, failed, err := applyStorageCfg(context.Background(), "/stage/etc/pve/storage.cfg", logger)
	if err != nil {
		t.Fatalf("applyStorageCfg: %v", err)
	}
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1 when both create and set fail", applied, failed)
	}
}
