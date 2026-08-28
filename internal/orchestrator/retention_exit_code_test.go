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

// TestUnclaimedLegacyArchivesLeaveACleanRunAtExitZero closes the loop between the two
// halves of one contract, which live in packages that cannot see each other. Package
// storage decides the SEVERITY of a retention report; package orchestrator derives
// the run's EXIT CODE from those severities. Orchestrator imports storage, so this is
// the only side the whole chain can be observed from.
//
// The contract: a location whose only out-of-scope entries are archives nothing can
// attribute keeps a clean run. Those archives are never pruned by anyone, so a
// WARNING about them would repeat on every run for ever and applyIssueExitCode would
// hold the run at exit 1 permanently, which is the symptom discussion #292 reported
// (retention prunes nothing, the run exits 1) rather than a report of it.
//
// Neither package can pin this alone: a storage-side test proves the line is INFO but
// not that INFO is harmless, and TestApplyIssueExitCode proves a WARNING promotes the
// run but not that retention avoids emitting one here.
//
// Every archive in the fixture carries a parseable .metadata on purpose. A missing
// one raises the pre-existing and unrelated "Missing .metadata for ... using filename
// metadata" WARNING from LocalStorage.loadMetadata, which would make this test pass
// or fail for the wrong reason.
func TestUnclaimedLegacyArchivesLeaveACleanRunAtExitZero(t *testing.T) {
	// The run's own name reaches retention through the storage constructor, which is
	// how a machine claims its own archives without this test depending on what the
	// kernel calls the host it runs on.
	const runHostname = "hosta.example.test"

	backupDir := t.TempDir()
	seeds := []struct {
		name     string
		when     time.Time
		metadata string
	}{
		// Sidecar parses, names no host: nothing can say which machine wrote it.
		{name: "proxmox-backup-20250101-100000.tar.gz", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), metadata: "COMPRESSION_TYPE=gzip\n"},
		{name: runHostname + "-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), metadata: "HOSTNAME=" + runHostname + "\n"},
		{name: runHostname + "-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), metadata: "HOSTNAME=" + runHostname + "\n"},
	}

	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		paths[i] = filepath.Join(backupDir, seed.name)
		// The .sha256 is what makes an entry eligible for retention at all
		// (backupHasCompletionSidecar does not accept .metadata), so without it the
		// archives would be inert and the run would be clean for the wrong reason.
		for suffix, content := range map[string]string{
			"":          "archive",
			".sha256":   "h  archive\n",
			".metadata": seed.metadata,
		} {
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

	local, err := storage.NewLocalStorage(&config.Config{BackupPath: backupDir}, logger, runHostname)
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if _, err := local.ApplyRetention(context.Background(), storage.RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
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

	runLog, err := os.ReadFile(logPath) //nolint:gosec // the path is this test's own t.TempDir()
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}

	if warningCount != 0 {
		t.Errorf("the run logged %d warning(s) over a location where nothing is contended: %+v. These archives are never pruned by anyone, so the warning and the exit code it forces would repeat on every run for ever", warningCount, categories)
	}
	if stats.ExitCode != types.ExitSuccess.Int() {
		t.Errorf("exit code = %d, want %d. A single host on its own backup path, holding archives nothing can attribute, must keep a clean run: a permanent exit 1 is what discussion #292 reported, not a fix for it", stats.ExitCode, types.ExitSuccess.Int())
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("retention deleted the pre-Go archive nothing attributes to this machine: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Errorf("this host's own surplus archive survived; a clean exit must not come from retention being switched off (stat err=%v)", err)
	}
	if !strings.Contains(string(runLog), "no host will ever delete them") {
		t.Errorf("the run log never named the archives retention left alone, so an operator has no way to learn why they stopped rotating. Log:\n%s", runLog)
	}
}
