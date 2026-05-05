package orchestrator

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestEstimatedBackupSizeGBMinimumAndScaling(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  float64
	}{
		{name: "zero uses minimum", bytes: 0, want: 0.001},
		{name: "below minimum uses minimum", bytes: 512, want: 0.001},
		{name: "one gibibyte", bytes: 1024 * 1024 * 1024, want: 1},
		{name: "two and a half gibibytes", bytes: 5 * 1024 * 1024 * 1024 / 2, want: 2.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estimatedBackupSizeGB(tt.bytes); math.Abs(got-tt.want) > 0.0000001 {
				t.Fatalf("estimatedBackupSizeGB(%d)=%f want %f", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestBackupDiskValidationErrorWrapsDiskError(t *testing.T) {
	diskErr := errors.New("need 3.5 GB free")
	err := backupDiskValidationError("", diskErr)

	var backupErr *BackupError
	if !errors.As(err, &backupErr) {
		t.Fatalf("expected BackupError, got %T", err)
	}
	if backupErr.Phase != "disk" || backupErr.Code != types.ExitDiskSpaceError {
		t.Fatalf("unexpected BackupError fields: phase=%q code=%v", backupErr.Phase, backupErr.Code)
	}
	if !errors.Is(err, diskErr) {
		t.Fatalf("expected disk error to be wrapped, got %v", err)
	}
}

func TestBackupDiskValidationErrorUsesDefaultMessage(t *testing.T) {
	err := backupDiskValidationError("", nil)

	var backupErr *BackupError
	if !errors.As(err, &backupErr) {
		t.Fatalf("expected BackupError, got %T", err)
	}
	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Fatalf("expected default disk space message, got %q", err.Error())
	}
}

func TestBackupMetricsExitCode(t *testing.T) {
	if got := backupMetricsExitCode(&BackupStats{}, nil); got != types.ExitSuccess.Int() {
		t.Fatalf("success exit code=%d want %d", got, types.ExitSuccess.Int())
	}
	if got := backupMetricsExitCode(&BackupStats{ExitCode: 77}, nil); got != 77 {
		t.Fatalf("stats exit code=%d want 77", got)
	}

	runErr := &BackupError{Phase: "disk", Err: errors.New("full"), Code: types.ExitDiskSpaceError}
	if got := backupMetricsExitCode(&BackupStats{}, runErr); got != types.ExitDiskSpaceError.Int() {
		t.Fatalf("backup error exit code=%d want %d", got, types.ExitDiskSpaceError.Int())
	}
	if got := backupMetricsExitCode(&BackupStats{}, errors.New("boom")); got != types.ExitGenericError.Int() {
		t.Fatalf("generic error exit code=%d want %d", got, types.ExitGenericError.Int())
	}
}

func TestEnsureBackupStatsTimingFillsEndAndDuration(t *testing.T) {
	now := time.Date(2026, 5, 5, 10, 30, 0, 0, time.UTC)
	orch := New(newTestLogger(), false)
	orch.clock = &FakeTime{Current: now}

	stats := &BackupStats{StartTime: now.Add(-90 * time.Second)}
	orch.ensureBackupStatsTiming(stats)

	if !stats.EndTime.Equal(now) {
		t.Fatalf("EndTime=%v want %v", stats.EndTime, now)
	}
	if stats.Duration != 90*time.Second {
		t.Fatalf("Duration=%v want %v", stats.Duration, 90*time.Second)
	}
}

func TestBuildBackupCollectorConfigMergesRuntimeExcludesAndBlacklist(t *testing.T) {
	orch := New(newTestLogger(), false)
	orch.SetBackupConfig("/backup", "/logs", types.CompressionZstd, 3, 2, "fast", []string{"runtime/**"})
	orch.SetConfig(&config.Config{
		BackupBlacklist:   []string{"/secret", "/tmp/cache"},
		CustomBackupPaths: []string{"/srv/app"},
		BaseDir:           "/opt/proxsave",
		ConfigPath:        "/etc/proxsave/backup.env",
	})

	cfg := orch.buildBackupCollectorConfig()
	for _, want := range []string{"runtime/**", "/secret", "/tmp/cache"} {
		if !containsString(cfg.ExcludePatterns, want) {
			t.Fatalf("ExcludePatterns missing %q: %#v", want, cfg.ExcludePatterns)
		}
	}
	if len(cfg.BackupBlacklist) != 2 || cfg.BackupBlacklist[0] != "/secret" || cfg.BackupBlacklist[1] != "/tmp/cache" {
		t.Fatalf("BackupBlacklist not copied: %#v", cfg.BackupBlacklist)
	}
	if len(cfg.CustomBackupPaths) != 1 || cfg.CustomBackupPaths[0] != "/srv/app" {
		t.Fatalf("CustomBackupPaths not copied: %#v", cfg.CustomBackupPaths)
	}
	if cfg.ScriptRepositoryPath != "/opt/proxsave" || cfg.ConfigFilePath != "/etc/proxsave/backup.env" {
		t.Fatalf("paths not copied: script=%q config=%q", cfg.ScriptRepositoryPath, cfg.ConfigFilePath)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
