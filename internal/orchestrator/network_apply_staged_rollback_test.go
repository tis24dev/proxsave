package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyNetworkWithRollbackWithUI_RollsBackOnStagedApplyFailure(t *testing.T) {
	fake := setupNetworkPreflightRollbackTest(t) // installs FakeFS as restoreFS, fake cmd, fake time
	stageRoot := "/tmp/proxsave/stage"
	// Fail the copy of the first staged network dir so applyNetworkFilesFromStage errors.
	fakeFS := restoreFS.(*FakeFS)
	fakeFS.StatErrors[filepath.Clean(stageRoot+"/etc/network")] = fmt.Errorf("injected staged copy failure")

	rollbackBackup := "/tmp/proxsave/network_rollback_backup_20260118_134651.tar.gz"
	ui := &fakeRestoreWorkflowUI{confirmAction: true}
	err := applyNetworkWithRollbackWithUI(context.Background(), ui, newTestLogger(), networkRollbackUIApplyRequest{
		rollbackBackupPath:  rollbackBackup,
		networkRollbackPath: rollbackBackup,
		stageRoot:           stageRoot,
		timeout:             defaultNetworkRollbackTimeout,
		systemType:          SystemTypePBS,
	})
	if err == nil {
		t.Fatal("expected the staged apply failure to propagate")
	}
	// The rollback script must have run, and preflight (ifup) must NOT have (we failed before it).
	foundRollback := false
	for _, c := range fake.CallsList() {
		if strings.HasPrefix(c, "sh ") && strings.Contains(c, "network_rollback_now_") {
			foundRollback = true
		}
	}
	if !foundRollback {
		t.Fatalf("staged apply failure must trigger the network rollback; calls=%#v", fake.CallsList())
	}
}

func TestApplyNetworkWithRollbackWithUI_StagedApplyFailureNoRollbackBackup(t *testing.T) {
	fake := setupNetworkPreflightRollbackTest(t)
	stageRoot := "/tmp/proxsave/stage"
	fakeFS := restoreFS.(*FakeFS)
	fakeFS.StatErrors[filepath.Clean(stageRoot+"/etc/network")] = fmt.Errorf("injected staged copy failure")

	ui := &fakeRestoreWorkflowUI{confirmAction: true}
	err := applyNetworkWithRollbackWithUI(context.Background(), ui, newTestLogger(), networkRollbackUIApplyRequest{
		rollbackBackupPath:  "/tmp/proxsave/full_backup.tar.gz", // full backup present so run() proceeds
		networkRollbackPath: "",                                 // no dedicated network rollback
		stageRoot:           stageRoot,
		timeout:             defaultNetworkRollbackTimeout,
		systemType:          SystemTypePBS,
	})
	if err == nil || !strings.Contains(err.Error(), "injected staged copy failure") {
		t.Fatalf("expected the raw copy error to propagate honestly, got %v", err)
	}
	for _, c := range fake.CallsList() {
		if strings.HasPrefix(c, "sh ") && strings.Contains(c, "network_rollback_now_") {
			t.Fatalf("must NOT run a network rollback with an empty rollback path; calls=%#v", fake.CallsList())
		}
	}
}
