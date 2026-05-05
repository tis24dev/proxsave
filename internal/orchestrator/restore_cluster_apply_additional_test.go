package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunSafeClusterApplyWithUI_SkipsStorageDatacenterWhenStoragePVEStaged(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	pveshPath := filepath.Join(pathDir, "pvesh")
	if err := os.WriteFile(pveshPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	pveDir := filepath.Join(exportRoot, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatalf("mkdir pve dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pveDir, "storage.cfg"), []byte("storage: local\n    type dir\n"), 0o640); err != nil {
		t.Fatalf("write storage.cfg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pveDir, "datacenter.cfg"), []byte("keyboard: it\n"), 0o640); err != nil {
		t.Fatalf("write datacenter.cfg: %v", err)
	}

	plan := &RestorePlan{
		SystemType:       SystemTypePVE,
		StagedCategories: []Category{{ID: "storage_pve", Type: CategoryTypePVE}},
	}
	ui := &fakeRestoreWorkflowUI{
		applyStorageCfg:    true,
		applyDatacenterCfg: true,
	}

	if err := runSafeClusterApplyWithUI(context.Background(), ui, exportRoot, newTestLogger(), plan); err != nil {
		t.Fatalf("runSafeClusterApplyWithUI error: %v", err)
	}

	for _, call := range runner.calls {
		if strings.Contains(call, "/cluster/storage") || strings.Contains(call, "/cluster/config") {
			t.Fatalf("storage/datacenter apply should be skipped for storage_pve staged restore; calls=%#v", runner.calls)
		}
	}
}
