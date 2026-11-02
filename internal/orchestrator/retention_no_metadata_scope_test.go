package orchestrator

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// Archives without .metadata that this host does not rotate (another host's, a pre-Go
// one nothing names) are never deleted here. Named under "No metadata, name used" and
// counted into "Backups deleted: 0, N without metadata", they kept the WARNING and exit
// 1 on every run. Only this host's own archives are named and counted.
func TestForeignArchivesWithoutMetadataLeaveACleanRunAtExitZero(t *testing.T) {
	const runHostname = "hosta.example.test"

	backupDir := t.TempDir()
	seeds := []struct {
		name     string
		when     time.Time
		metadata string
	}{
		{name: "proxmox-backup-20240101-000000.tar.zst", when: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		{name: "hostb.example.test-backup-20250101-100000.tar.zst", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
		{name: runHostname + "-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), metadata: "HOSTNAME=" + runHostname + "\n"},
	}
	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		paths[i] = filepath.Join(backupDir, seed.name)
		files := map[string]string{"": "archive", ".sha256": "h  archive\n"}
		if seed.metadata != "" {
			files[".metadata"] = seed.metadata
		}
		for suffix, content := range files {
			if err := os.WriteFile(paths[i]+suffix, []byte(content), 0o600); err != nil {
				t.Fatalf("seed %s%s: %v", seed.name, suffix, err)
			}
		}
		if err := os.Chtimes(paths[i], seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}

	logPath := filepath.Join(t.TempDir(), "run.log")
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(io.Discard)
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	cfg := &config.Config{BackupPath: backupDir}
	local, err := storage.NewLocalStorage(cfg, logger, runHostname)
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	deleted, err := local.ApplyRetention(context.Background(), storage.RetentionConfig{Policy: "simple", MaxBackups: 7})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	summary := local.LastRetentionSummary()
	NewStorageAdapter(local, logger, cfg).logRetentionOutcome(summary, deleted)
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile: %v", err)
	}

	categories, errorCount, warningCount, notifyCount := ParseLogCounts(logPath, 10)
	stats := &BackupStats{
		ExitCode:      types.ExitSuccess.Int(),
		ErrorCount:    errorCount,
		WarningCount:  warningCount,
		NotifyCount:   notifyCount,
		LogCategories: categories,
	}
	applyIssueExitCode(stats)
	runLog, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}

	if summary.NoMetadata != 0 {
		t.Errorf("NoMetadata = %d, want 0: neither archive without metadata is this host's", summary.NoMetadata)
	}
	if warningCount != 0 || stats.ExitCode != types.ExitSuccess.Int() {
		t.Errorf("warnings=%d exit=%d, want 0 and %d. Log:\n%s", warningCount, stats.ExitCode, types.ExitSuccess.Int(), runLog)
	}
	if strings.Contains(string(runLog), "No metadata, name used") {
		t.Errorf("the run log names an archive this host does not rotate. Log:\n%s", runLog)
	}
	for _, p := range paths[:2] {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("retention deleted %s, which is not this host's: %v", filepath.Base(p), err)
		}
	}
}
