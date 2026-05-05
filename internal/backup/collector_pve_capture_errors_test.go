package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPVECollectorWithSuccessfulCommands(t *testing.T, calls *[]string) *Collector {
	t.Helper()
	return newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(name string) (string, error) {
			return "/bin/true", nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			*calls = append(*calls, commandSpec(name, args...).String())
			return []byte("[]"), nil
		},
	})
}

func TestCollectPVEBackupJobHistoryReturnsFirstCaptureError(t *testing.T) {
	var calls []string
	collector := newPVECollectorWithSuccessfulCommands(t, &calls)

	jobsDir := collector.pveJobsDir()
	if err := os.MkdirAll(filepath.Join(jobsDir, "node-a_backup_history.json"), 0o755); err != nil {
		t.Fatalf("create output collision: %v", err)
	}

	err := collector.collectPVEBackupJobHistory(context.Background(), []string{"node-a", "node-b"})
	if err == nil {
		t.Fatalf("expected capture error")
	}
	if !strings.Contains(err.Error(), "failed to write report") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected collection to continue after first capture error, calls=%#v", calls)
	}
}

func TestCollectPVEReplicationStatusReturnsFirstCaptureError(t *testing.T) {
	var calls []string
	collector := newPVECollectorWithSuccessfulCommands(t, &calls)

	repDir := collector.pveReplicationDir()
	if err := os.MkdirAll(filepath.Join(repDir, "node-a_replication_status.json"), 0o755); err != nil {
		t.Fatalf("create output collision: %v", err)
	}

	err := collector.collectPVEReplicationStatus(context.Background(), []string{"node-a", "node-b"})
	if err == nil {
		t.Fatalf("expected capture error")
	}
	if !strings.Contains(err.Error(), "failed to write report") {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected collection to continue after first capture error, calls=%#v", calls)
	}
}

func TestCollectPVEScheduleCrontabReturnsCaptureError(t *testing.T) {
	var calls []string
	collector := newPVECollectorWithSuccessfulCommands(t, &calls)

	schedulesDir := collector.pveSchedulesDir()
	if err := os.MkdirAll(filepath.Join(schedulesDir, "root_crontab.txt"), 0o755); err != nil {
		t.Fatalf("create output collision: %v", err)
	}

	err := collector.collectPVEScheduleCrontab(context.Background())
	if err == nil {
		t.Fatalf("expected capture error")
	}
	if !strings.Contains(err.Error(), "collectPVEScheduleCrontab:") {
		t.Fatalf("expected function context in error, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one command call, calls=%#v", calls)
	}
}

func TestCollectPVEScheduleTimersReturnsCaptureError(t *testing.T) {
	var calls []string
	collector := newPVECollectorWithSuccessfulCommands(t, &calls)

	schedulesDir := collector.pveSchedulesDir()
	if err := os.MkdirAll(filepath.Join(schedulesDir, "systemd_timers.txt"), 0o755); err != nil {
		t.Fatalf("create output collision: %v", err)
	}

	err := collector.collectPVEScheduleTimers(context.Background())
	if err == nil {
		t.Fatalf("expected capture error")
	}
	if !strings.Contains(err.Error(), "collectPVEScheduleTimers:") {
		t.Fatalf("expected function context in error, got %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("expected one command call, calls=%#v", calls)
	}
}
