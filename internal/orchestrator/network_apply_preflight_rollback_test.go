package orchestrator

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyNetworkWithRollbackCLI_RollsBackFilesOnPreflightFailure(t *testing.T) {
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

	restoreFS = NewFakeFS()
	restoreTime = &FakeTime{Current: time.Date(2026, 1, 18, 13, 47, 6, 0, time.UTC)}
	networkDiagnosticsSequence = 0

	pathDir := t.TempDir()
	ifqueryPath := filepath.Join(pathDir, "ifquery")
	if err := os.WriteFile(ifqueryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write ifquery: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip route show default": []byte("default via 192.168.1.1 dev nic1\n"),
			"ifquery --check -a":    []byte("error: interface enp4s4 not found\n"),
		},
		Errors: map[string]error{
			"ifquery --check -a": fmt.Errorf("exit 1"),
		},
	}
	restoreCmd = fake

	reader := bufio.NewReader(strings.NewReader("\n"))
	logger := newTestLogger()
	rollbackBackup := "/tmp/proxsave/network_rollback_backup_20260118_134651.tar.gz"

	err := applyNetworkWithRollbackCLI(
		context.Background(),
		reader,
		logger,
		rollbackBackup,
		rollbackBackup,
		"",
		"",
		90*time.Second,
		SystemTypePBS,
	)
	if err == nil || !strings.Contains(err.Error(), "network preflight validation failed") {
		t.Fatalf("expected preflight error, got %v", err)
	}

	foundIfquery := false
	foundRollbackSh := false
	for _, call := range fake.CallsList() {
		if call == "ifquery --check -a" {
			foundIfquery = true
		}
		if strings.HasPrefix(call, "sh ") && strings.Contains(call, "network_rollback_now_") {
			foundRollbackSh = true
		}
	}
	if !foundIfquery {
		t.Fatalf("expected ifquery preflight to run; calls=%#v", fake.CallsList())
	}
	if !foundRollbackSh {
		t.Fatalf("expected rollback script to be invoked via sh; calls=%#v", fake.CallsList())
	}
}
