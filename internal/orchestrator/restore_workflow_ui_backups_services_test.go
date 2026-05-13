package orchestrator

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
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
