package orchestrator

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
)

func TestPBSServicesNotRestartedDuringFileBasedStagedApply(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to exercise staged apply code paths")
	}

	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	restoreFS = osFS{}
	fakeCmd := &FakeCommandRunner{}
	restoreCmd = fakeCmd

	stageRoot := t.TempDir()
	logger := newTestLogger()
	plan := &RestorePlan{
		SystemType: SystemTypePBS,
		StagedCategories: []Category{
			{ID: "pbs_host", Type: CategoryTypePBS},
			{ID: "pbs_notifications", Type: CategoryTypePBS},
		},
		PBSRestoreBehavior: PBSRestoreBehaviorMerge,
	}

	if err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyPBSConfigsFromStage error: %v", err)
	}
	if len(fakeCmd.Calls) != 0 {
		t.Fatalf("expected no commands during file-based staged apply, got %v", fakeCmd.Calls)
	}

	if err := maybeApplyNotificationsFromStage(context.Background(), logger, plan, stageRoot, false); err != nil {
		t.Fatalf("maybeApplyNotificationsFromStage error: %v", err)
	}
	if len(fakeCmd.Calls) != 0 {
		t.Fatalf("expected no commands during notifications staged apply on PBS, got %v", fakeCmd.Calls)
	}

	// Allow the temporary stop at the end of API apply to complete quickly.
	if fakeCmd.Outputs == nil {
		fakeCmd.Outputs = make(map[string][]byte)
	}
	if fakeCmd.Errors == nil {
		fakeCmd.Errors = make(map[string]error)
	}
	for _, svc := range []string{"proxmox-backup-proxy", "proxmox-backup"} {
		key := "systemctl is-active " + svc
		fakeCmd.Outputs[key] = []byte("inactive\n")
		fakeCmd.Errors[key] = errors.New("exit status 3")
	}

	if err := maybeApplyPBSConfigsViaAPIFromStage(context.Background(), logger, plan, stageRoot, false, true); err != nil {
		t.Fatalf("maybeApplyPBSConfigsViaAPIFromStage error: %v", err)
	}

	if !slices.Contains(fakeCmd.Calls, "systemctl start proxmox-backup") {
		t.Fatalf("expected PBS service start during API phase, calls=%v", fakeCmd.Calls)
	}
	if !slices.Contains(fakeCmd.Calls, "systemctl stop --no-block proxmox-backup-proxy") {
		t.Fatalf("expected PBS service stop after API phase, calls=%v", fakeCmd.Calls)
	}
}
