package orchestrator

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestApplyIssueExitCode(t *testing.T) {
	applyIssueExitCode(nil)

	tests := []struct {
		name       string
		stats      BackupStats
		wantExit   int
		wantErrors int
		wantWarns  int
	}{
		{
			name:     "no issues keeps success",
			stats:    BackupStats{ExitCode: types.ExitSuccess.Int()},
			wantExit: types.ExitSuccess.Int(),
		},
		{
			name:      "warnings promote success to generic error",
			stats:     BackupStats{WarningCount: 1, ExitCode: types.ExitSuccess.Int()},
			wantExit:  types.ExitGenericError.Int(),
			wantWarns: 1,
		},
		{
			name:      "warnings preserve specific failure",
			stats:     BackupStats{WarningCount: 1, ExitCode: types.ExitStorageError.Int()},
			wantExit:  types.ExitStorageError.Int(),
			wantWarns: 1,
		},
		{
			name:       "errors promote success to backup error",
			stats:      BackupStats{ErrorCount: 1, ExitCode: types.ExitSuccess.Int()},
			wantExit:   types.ExitBackupError.Int(),
			wantErrors: 1,
		},
		{
			name:       "errors promote generic error to backup error",
			stats:      BackupStats{ErrorCount: 1, ExitCode: types.ExitGenericError.Int()},
			wantExit:   types.ExitBackupError.Int(),
			wantErrors: 1,
		},
		{
			name:       "errors preserve specific failure",
			stats:      BackupStats{ErrorCount: 1, ExitCode: types.ExitStorageError.Int()},
			wantExit:   types.ExitStorageError.Int(),
			wantErrors: 1,
		},
		{
			name:       "errors take precedence over warnings",
			stats:      BackupStats{ErrorCount: 1, WarningCount: 1, ExitCode: types.ExitSuccess.Int()},
			wantExit:   types.ExitBackupError.Int(),
			wantErrors: 1,
			wantWarns:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stats := tt.stats
			applyIssueExitCode(&stats)

			if stats.ExitCode != tt.wantExit {
				t.Fatalf("ExitCode=%d; want %d", stats.ExitCode, tt.wantExit)
			}
			if stats.ErrorCount != tt.wantErrors || stats.WarningCount != tt.wantWarns {
				t.Fatalf("issue counts changed to errors=%d warnings=%d; want errors=%d warnings=%d",
					stats.ErrorCount,
					stats.WarningCount,
					tt.wantErrors,
					tt.wantWarns,
				)
			}
		})
	}
}

func TestDispatchEarlyErrorNotification_ReturnsNilWhenNoError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	orch := &Orchestrator{logger: logger}

	if got := orch.DispatchEarlyErrorNotification(context.Background(), nil); got != nil {
		t.Fatalf("expected nil stats for nil early error, got %+v", got)
	}

	early := &EarlyErrorState{
		Phase:     "config",
		Error:     nil,
		ExitCode:  types.ExitConfigError,
		Timestamp: time.Unix(1700000000, 0),
	}
	if got := orch.DispatchEarlyErrorNotification(context.Background(), early); got != nil {
		t.Fatalf("expected nil stats for early error without Error, got %+v", got)
	}
}

func TestDispatchEarlyErrorNotification_PopulatesMinimalStats(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)

	ts := time.Unix(1700000000, 0)
	orch := &Orchestrator{
		logger:         logger,
		version:        "1.2.3",
		proxmoxVersion: "8.1",
		serverID:       " 1234567890123456 ",
		serverMAC:      " bc:24:11:41:0d:18 ",
	}
	early := &EarlyErrorState{
		Phase:     "config",
		Error:     errors.New("boom"),
		ExitCode:  types.ExitConfigError,
		Timestamp: ts,
	}

	stats := orch.DispatchEarlyErrorNotification(context.Background(), early)
	if stats == nil {
		t.Fatalf("expected stats, got nil")
	}
	if stats.ExitCode != types.ExitConfigError.Int() {
		t.Fatalf("ExitCode=%d; want %d", stats.ExitCode, types.ExitConfigError.Int())
	}
	if stats.ErrorCount != 1 || stats.LocalStatus != "error" {
		t.Fatalf("ErrorCount/LocalStatus=%d/%q; want 1/%q", stats.ErrorCount, stats.LocalStatus, "error")
	}
	if stats.StartTime != ts || stats.EndTime != ts || stats.Timestamp != ts {
		t.Fatalf("timestamps not propagated: start=%v end=%v ts=%v", stats.StartTime, stats.EndTime, stats.Timestamp)
	}
	if stats.Version != "1.2.3" || stats.ScriptVersion != "1.2.3" || stats.ProxmoxVersion != "8.1" {
		t.Fatalf("version fields=%q/%q/%q; want %q/%q/%q", stats.Version, stats.ScriptVersion, stats.ProxmoxVersion, "1.2.3", "1.2.3", "8.1")
	}
	if stats.ServerID != "1234567890123456" || stats.ServerMAC != "bc:24:11:41:0d:18" {
		t.Fatalf("identity fields=%q/%q; want server ID and MAC", stats.ServerID, stats.ServerMAC)
	}
	if stats.LocalStatusSummary == "" || !strings.Contains(stats.LocalStatusSummary, "boom") {
		t.Fatalf("LocalStatusSummary=%q; want to contain error text", stats.LocalStatusSummary)
	}
	if stats.Hostname == "" {
		t.Fatalf("Hostname is empty")
	}
}

func TestDispatchEarlyErrorNotificationPreservesSyntheticIssueWithLogFile(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	logPath := filepath.Join(t.TempDir(), "early.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() {
		_ = logger.CloseLogFile()
	})

	orch := &Orchestrator{logger: logger}
	early := &EarlyErrorState{
		Phase:     "config",
		Error:     errors.New("boom"),
		ExitCode:  types.ExitConfigError,
		Timestamp: time.Unix(1700000000, 0),
	}

	stats := orch.DispatchEarlyErrorNotification(context.Background(), early)
	if stats == nil {
		t.Fatalf("expected stats, got nil")
	}
	if stats.ExitCode != types.ExitConfigError.Int() {
		t.Fatalf("ExitCode=%d; want %d", stats.ExitCode, types.ExitConfigError.Int())
	}
	if stats.ErrorCount != 1 {
		t.Fatalf("ErrorCount=%d; want synthetic early error count to be preserved", stats.ErrorCount)
	}
	if stats.LogFilePath != logPath {
		t.Fatalf("LogFilePath=%q; want %q", stats.LogFilePath, logPath)
	}
}
