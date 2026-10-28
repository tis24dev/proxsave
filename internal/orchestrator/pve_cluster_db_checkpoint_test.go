package orchestrator

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

const pveClusterDBCheckpointCmd = "sqlite3 /var/lib/pve-cluster/config.db PRAGMA wal_checkpoint(TRUNCATE);"

func pveClusterDBFixture(t *testing.T) *FakeFS {
	t.Helper()
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	for _, p := range append([]string{pveClusterDB}, pveClusterDBSidecars...) {
		if err := fakeFS.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return fakeFS
}

func fakeCheckpointRunner(t *testing.T, output string, err error) *FakeCommandRunner {
	t.Helper()
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	cmd := &FakeCommandRunner{Outputs: map[string][]byte{pveClusterDBCheckpointCmd: []byte(output)}, Errors: map[string]error{}}
	if err != nil {
		cmd.Errors[pveClusterDBCheckpointCmd] = err
	}
	restoreCmd = cmd
	return cmd
}

// pmxcfs is SIGKILLed when its stop times out, so its last commits live only in the
// WAL: the WAL is folded into config.db before the sidecars are removed.
func TestCheckpointPVEClusterDB(t *testing.T) {
	pveClusterDBFixture(t)
	cmd := fakeCheckpointRunner(t, "0|12|12\n", nil)
	if err := checkpointPVEClusterDB(context.Background(), newTestLogger()); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if !slices.Contains(cmd.Calls, pveClusterDBCheckpointCmd) {
		t.Fatalf("no checkpoint run: %v", cmd.Calls)
	}

	for _, tc := range []struct {
		output string
		err    error
		want   string
	}{
		{"1|12|5\n", nil, "failed to checkpoint the cluster database (/var/lib/pve-cluster/config.db): 1|12|5"},
		{"sqlite3 said no\n", errors.New("exit status 1"), "failed to checkpoint the cluster database (/var/lib/pve-cluster/config.db): sqlite3 said no"},
		{"", errors.New("exit status 1"), "failed to checkpoint the cluster database (/var/lib/pve-cluster/config.db): exit status 1"},
	} {
		fakeCheckpointRunner(t, tc.output, tc.err)
		if err := checkpointPVEClusterDB(context.Background(), newTestLogger()); err == nil || err.Error() != tc.want {
			t.Fatalf("output %q err %v: got %v, want %q", tc.output, tc.err, err, tc.want)
		}
	}
}

// No config.db: nothing to fold, no sqlite3 run.
func TestCheckpointPVEClusterDBWithoutADatabase(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	cmd := fakeCheckpointRunner(t, "", nil)
	if err := checkpointPVEClusterDB(context.Background(), newTestLogger()); err != nil {
		t.Fatalf("checkpoint without config.db: %v", err)
	}
	if len(cmd.Calls) != 0 {
		t.Fatalf("sqlite3 run without config.db: %v", cmd.Calls)
	}
}

// A checkpoint that fails stops the restore before anything is touched: the WAL
// stays, and the stopped services are started again.
func TestPreparePVEClusterRestoreStopsWhenTheCheckpointFails(t *testing.T) {
	fakeFS := pveClusterDBFixture(t)
	cmd := fakeCheckpointRunner(t, "sqlite3 said no\n", errors.New("exit status 1"))
	cmd.Outputs["umount /etc/pve"] = []byte("not mounted\n")
	cmd.Errors["umount /etc/pve"] = errors.New("not mounted")
	cmd.Errors["which systemctl"] = errors.New("missing")
	for _, svc := range []string{"pve-ha-lrm", "pve-ha-crm", "pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	w := &restoreUIWorkflowRun{
		ctx:    context.Background(),
		cfg:    &config.Config{},
		logger: newTestLogger(),
		ui:     &fakeRestoreWorkflowUI{},
		plan:   &RestorePlan{NeedsClusterRestore: true},
	}
	cleanup, err := w.prepareRestoreServices()
	if err == nil || !strings.Contains(err.Error(), "failed to checkpoint the cluster database") {
		t.Fatalf("err = %v, want the checkpoint failure", err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup on prepare error")
	}
	if !slices.Contains(cmd.Calls, "systemctl start pve-cluster") {
		t.Fatalf("pve-cluster not started again: %v", cmd.Calls)
	}
	for _, p := range pveClusterDBSidecars {
		if _, err := fakeFS.Stat(p); err != nil {
			t.Fatalf("%s removed although the checkpoint failed: %v", p, err)
		}
	}
}

// An old WAL or SHM that cannot be removed (any error but "does not exist") stops the
// restore before the archive's config.db is written beside it, and the stopped
// services are started again. A non-empty directory in place of config.db-wal is a
// removal that fails with ENOTEMPTY.
func TestPreparePVEClusterRestoreStopsWhenAnOldDatabaseFileCannotBeRemoved(t *testing.T) {
	fakeFS := pveClusterDBFixture(t)
	wal := pveClusterDBSidecars[0]
	if err := fakeFS.Remove(wal); err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile(wal+"/held", []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := fakeCheckpointRunner(t, "0|0|0\n", nil)
	cmd.Outputs["umount /etc/pve"] = []byte("not mounted\n")
	cmd.Errors["umount /etc/pve"] = errors.New("not mounted")
	cmd.Errors["which systemctl"] = errors.New("missing")
	for _, svc := range []string{"pve-ha-lrm", "pve-ha-crm", "pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	w := &restoreUIWorkflowRun{
		ctx:    context.Background(),
		cfg:    &config.Config{},
		logger: newTestLogger(),
		ui:     &fakeRestoreWorkflowUI{},
		plan:   &RestorePlan{NeedsClusterRestore: true},
	}
	cleanup, err := w.prepareRestoreServices()
	want := "failed to remove the old cluster database file (" + wal + "): "
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Fatalf("err = %v, want prefix %q", err, want)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup on prepare error")
	}
	if !slices.Contains(cmd.Calls, "systemctl start pve-cluster") {
		t.Fatalf("pve-cluster not started again: %v", cmd.Calls)
	}
}
