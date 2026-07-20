package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestPrepareRestoreServicesCleansUpPreviousServicesOnLaterError(t *testing.T) {
	origRestoreCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origRestoreCmd })

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"umount /etc/pve": []byte("not mounted\n"),
		},
		Errors: map[string]error{
			"umount /etc/pve": errors.New("not mounted"),
			"which systemctl": errors.New("missing"),
		},
	}
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	restoreCmd = cmd

	w := &restoreUIWorkflowRun{
		ctx:    context.Background(),
		cfg:    &config.Config{},
		logger: newTestLogger(),
		ui:     &fakeRestoreWorkflowUI{},
		plan: &RestorePlan{
			NeedsClusterRestore: true,
			NeedsPBSServices:    true,
		},
	}

	cleanup, err := w.prepareRestoreServices()
	if !errors.Is(err, ErrRestoreAborted) {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
	if cleanup != nil {
		t.Fatalf("expected nil cleanup on prepare error")
	}

	for _, want := range []string{
		"systemctl start pve-cluster",
		"systemctl start pvedaemon",
		"systemctl start pveproxy",
		"systemctl start pvestatd",
	} {
		if !slices.Contains(cmd.Calls, want) {
			t.Fatalf("missing cleanup command %q; calls=%v", want, cmd.Calls)
		}
	}
}

// newSafetyBackupTestRun builds a restoreUIWorkflowRun wired to a sandboxed
// safetyFS/safetyNow and a logger that writes to buf, so createSafetyBackup
// can be exercised without touching the real filesystem.
func newSafetyBackupTestRun(t *testing.T, buf *bytes.Buffer) *restoreUIWorkflowRun {
	t.Helper()

	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	origFS := safetyFS
	safetyFS = fake
	t.Cleanup(func() { safetyFS = origFS })

	origNow := safetyNow
	safetyNow = func() time.Time { return time.Date(2024, time.March, 1, 15, 4, 5, 0, time.UTC) }
	t.Cleanup(func() { safetyNow = origNow })

	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(buf)

	return &restoreUIWorkflowRun{
		ctx:      context.Background(),
		cfg:      &config.Config{},
		logger:   logger,
		ui:       &fakeRestoreWorkflowUI{},
		destRoot: "/restore-target",
	}
}

func TestCreateSafetyBackupLogsAccountsRollbackHintWhenAccountsCategoryPresent(t *testing.T) {
	var buf bytes.Buffer
	w := newSafetyBackupTestRun(t, &buf)

	categories := []Category{{ID: "accounts", Paths: []string{"./etc/passwd"}}}
	if err := w.createSafetyBackup(categories); err != nil {
		t.Fatalf("createSafetyBackup failed: %v", err)
	}

	if !strings.Contains(buf.String(), "System accounts rollback") {
		t.Fatalf("expected accounts rollback hint in log output, got: %q", buf.String())
	}
}

func TestCreateSafetyBackupOmitsAccountsRollbackHintWhenAccountsCategoryAbsent(t *testing.T) {
	var buf bytes.Buffer
	w := newSafetyBackupTestRun(t, &buf)

	categories := []Category{{ID: "etc", Paths: []string{"./etc/hosts"}}}
	if err := w.createSafetyBackup(categories); err != nil {
		t.Fatalf("createSafetyBackup failed: %v", err)
	}

	if strings.Contains(buf.String(), "System accounts rollback") {
		t.Fatalf("did not expect accounts rollback hint in log output, got: %q", buf.String())
	}
}
