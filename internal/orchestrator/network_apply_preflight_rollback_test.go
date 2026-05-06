package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyNetworkWithRollbackWithUI_RollsBackFilesOnPreflightFailure(t *testing.T) {
	fake := setupNetworkPreflightRollbackTest(t)
	err := runNetworkPreflightRollbackFailure(t)
	if err == nil || !strings.Contains(err.Error(), "network preflight validation failed") {
		t.Fatalf("expected preflight error, got %v", err)
	}
	assertNetworkPreflightRollbackCalls(t, fake.CallsList())
}

func setupNetworkPreflightRollbackTest(t *testing.T) *FakeCommandRunner {
	t.Helper()
	origFS := restoreFS
	origCmd := restoreCmd
	origTime := restoreTime
	origSeq := networkDiagnosticsSequence
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		restoreTime = origTime
		networkDiagnosticsSequence = origSeq
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 18, 13, 47, 6, 0, time.UTC)}
	networkDiagnosticsSequence = 0

	installNetworkPreflightRollbackTools(t)
	fake := newNetworkPreflightRollbackRunner()
	restoreCmd = fake
	return fake
}

func installNetworkPreflightRollbackTools(t *testing.T) {
	t.Helper()
	pathDir := t.TempDir()
	writeExecutableTestTool(t, pathDir, "ifquery")
	writeExecutableTestTool(t, pathDir, "ifup")
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeExecutableTestTool(t *testing.T, pathDir, name string) {
	t.Helper()
	toolPath := filepath.Join(pathDir, name)
	if err := os.WriteFile(toolPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func newNetworkPreflightRollbackRunner() *FakeCommandRunner {
	return &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip route show default": []byte("default via 192.168.1.1 dev nic1\n"),
			"ifquery --check -a":    []byte("ifquery check output\n"),
			"ifup -n -a":            []byte("error: invalid config\n"),
		},
		Errors: map[string]error{
			"ifup -n -a": fmt.Errorf("exit 1"),
		},
	}
}

func runNetworkPreflightRollbackFailure(t *testing.T) error {
	t.Helper()
	logger := newTestLogger()
	rollbackBackup := "/tmp/proxsave/network_rollback_backup_20260118_134651.tar.gz"

	ui := &fakeRestoreWorkflowUI{confirmAction: true}
	return applyNetworkWithRollbackWithUI(
		context.Background(),
		ui,
		logger,
		networkRollbackUIApplyRequest{
			rollbackBackupPath:  rollbackBackup,
			networkRollbackPath: rollbackBackup,
			timeout:             defaultNetworkRollbackTimeout,
			systemType:          SystemTypePBS,
		},
	)
}

func assertNetworkPreflightRollbackCalls(t *testing.T, calls []string) {
	t.Helper()
	foundIfupPreflight := false
	foundRollbackSh := false
	for _, call := range calls {
		if call == "ifup -n -a" {
			foundIfupPreflight = true
		}
		if strings.HasPrefix(call, "sh ") && strings.Contains(call, "network_rollback_now_") {
			foundRollbackSh = true
		}
	}
	if !foundIfupPreflight {
		t.Fatalf("expected ifup preflight to run; calls=%#v", calls)
	}
	if !foundRollbackSh {
		t.Fatalf("expected rollback script to be invoked via sh; calls=%#v", calls)
	}
}
