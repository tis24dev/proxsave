package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// outputLine returns the single output line containing needle, failing when there is
// none or more than one.
func outputLine(t *testing.T, out, needle string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, needle) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 line containing %q, got %d:\n%s", needle, len(found), out)
	}
	return found[0]
}

func requireExists(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s must still exist after a dry run: %v", what, err)
	}
}

func requireGone(t *testing.T, path, what string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s must be removed by a real run, got err=%v", what, err)
	}
}

// tmpProfileCount counts the profiles the cleanup globs under the shared
// /tmp/proxsave, which is not redirected by workspaceRoot.
func tmpProfileCount(t *testing.T) int {
	t.Helper()
	n := 0
	for _, pattern := range []string{"cpu-*.pprof", "heap-*.pprof"} {
		matches, err := filepath.Glob(filepath.Join("/tmp", "proxsave", pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		n += len(matches)
	}
	return n
}

// A dry run must remove nothing that a previous execution left (stats report, legacy
// and /tmp/proxsave profiles, orphaned workspace), and must announce exactly the
// counts the real cleanup then reports for the same state.
func TestCleanupPreviousExecutionArtifactsDryRunRemovesNothing(t *testing.T) {
	origRoot := workspaceRoot
	workspaceRoot = t.TempDir()
	t.Cleanup(func() { workspaceRoot = origRoot })

	logDir := t.TempDir()
	statsFile := filepath.Join(logDir, "backup-stats-20261001-120000.json")
	legacyCPU := filepath.Join(logDir, "cpu-legacy.pprof")
	for _, p := range []string{statsFile, legacyCPU} {
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	profDir := filepath.Join("/tmp", "proxsave")
	if err := os.MkdirAll(profDir, 0o755); err != nil {
		t.Fatalf("prepare %s: %v", profDir, err)
	}
	heapFile := filepath.Join(profDir, fmt.Sprintf("heap-%d.pprof", time.Now().UnixNano()))
	if err := os.WriteFile(heapFile, []byte("heap"), 0o640); err != nil {
		t.Fatalf("write heap profile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(heapFile) })

	registryPath := filepath.Join(t.TempDir(), "registry.json")
	reg, err := NewTempDirRegistry(logging.New(types.LogLevelError, false), registryPath)
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}
	orphanDir := filepath.Join(workspaceRoot, "proxsave-orphan")
	if err := os.MkdirAll(orphanDir, 0o700); err != nil {
		t.Fatalf("create orphan dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(orphanDir, workspaceMarker), []byte("m"), 0o600); err != nil {
		t.Fatalf("write orphan marker: %v", err)
	}
	if err := reg.Register(orphanDir); err != nil {
		t.Fatalf("register orphan dir: %v", err)
	}
	makeRegistryEntriesStale(t, registryPath)

	wantFiles := 2 + tmpProfileCount(t)

	var dryOut bytes.Buffer
	dryLogger := logging.New(types.LogLevelInfo, false)
	dryLogger.SetOutput(&dryOut)
	dry := &Orchestrator{logger: dryLogger, dryRun: true, logPath: logDir, tempRegistry: reg}
	dry.cleanupPreviousExecutionArtifacts(context.Background())

	requireExists(t, statsFile, "previous stats report")
	requireExists(t, legacyCPU, "legacy CPU profile")
	requireExists(t, heapFile, "/tmp/proxsave heap profile")
	requireExists(t, filepath.Join(orphanDir, workspaceMarker), "orphaned workspace")
	entries, err := reg.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != orphanDir {
		t.Fatalf("a dry run must keep the orphan's registry entry, got %+v", entries)
	}

	wantDry := fmt.Sprintf("[DRY RUN] Would remove %d item(s) from previous executions (%d file(s), 1 dir(s))", wantFiles+1, wantFiles)
	if line := outputLine(t, dryOut.String(), wantDry); !strings.Contains(line, "INFO") {
		t.Fatalf("dry-run cleanup line must be INFO, got %q", line)
	}
	if strings.Contains(dryOut.String(), "Cleanup of previous execution files") {
		t.Fatalf("a dry run must not claim a cleanup:\n%s", dryOut.String())
	}

	// The real cleanup of the same state removes what the dry run announced, and
	// still says so with its own line.
	var realOut bytes.Buffer
	realLogger := logging.New(types.LogLevelInfo, false)
	realLogger.SetOutput(&realOut)
	real := &Orchestrator{logger: realLogger, logPath: logDir, tempRegistry: reg}
	real.cleanupPreviousExecutionArtifacts(context.Background())

	wantReal := fmt.Sprintf("Cleanup of previous execution files completed successfully (%d item(s) removed: %d file(s), 1 dir(s))", wantFiles+1, wantFiles)
	if line := outputLine(t, realOut.String(), wantReal); !strings.Contains(line, "INFO") {
		t.Fatalf("real cleanup line must be INFO, got %q", line)
	}
	if strings.Contains(realOut.String(), "[DRY RUN]") {
		t.Fatalf("a real run must not print dry-run lines:\n%s", realOut.String())
	}
	requireGone(t, statsFile, "previous stats report")
	requireGone(t, legacyCPU, "legacy CPU profile")
	requireGone(t, heapFile, "/tmp/proxsave heap profile")
	requireGone(t, orphanDir, "orphaned workspace")
}

// With nothing to remove, the dry run prints what a real run prints: no INFO line.
func TestCleanupPreviousExecutionArtifactsDryRunNothingToRemove(t *testing.T) {
	if n := tmpProfileCount(t); n > 0 {
		t.Skipf("/tmp/proxsave holds %d profile(s) on this machine; the empty case cannot be built", n)
	}
	origRoot := workspaceRoot
	workspaceRoot = t.TempDir()
	t.Cleanup(func() { workspaceRoot = origRoot })

	for _, dryRun := range []bool{true, false} {
		reg, err := NewTempDirRegistry(logging.New(types.LogLevelError, false), filepath.Join(t.TempDir(), "registry.json"))
		if err != nil {
			t.Fatalf("NewTempDirRegistry: %v", err)
		}
		var out bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&out)
		o := &Orchestrator{logger: logger, dryRun: dryRun, logPath: t.TempDir(), tempRegistry: reg}
		o.cleanupPreviousExecutionArtifacts(context.Background())
		if out.Len() != 0 {
			t.Fatalf("dryRun=%v: nothing to remove must print nothing at INFO, got:\n%s", dryRun, out.String())
		}
	}
}

// CountOrphaned counts what CleanupOrphaned removes and removes none of it; an
// untrusted entry is dropped from the registry exactly as in a real sweep.
func TestTempDirRegistryCountOrphanedRemovesNothing(t *testing.T) {
	origRoot := workspaceRoot
	workspaceRoot = t.TempDir()
	t.Cleanup(func() { workspaceRoot = origRoot })

	var out bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&out)
	registry, err := NewTempDirRegistry(logger, filepath.Join(t.TempDir(), "temp-dirs.json"))
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}

	staleDir := filepath.Join(workspaceRoot, "proxsave-stale")
	if err := os.MkdirAll(staleDir, 0o700); err != nil {
		t.Fatalf("mkdir stale dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, workspaceMarker), []byte("m"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatalf("mkdir outside dir: %v", err)
	}
	for _, dir := range []string{staleDir, outsideDir} {
		if err := registry.Register(dir); err != nil {
			t.Fatalf("register %s: %v", dir, err)
		}
	}
	entries, err := registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	for i := range entries {
		entries[i].CreatedAt = time.Now().Add(-48 * time.Hour)
		entries[i].PID = -1
	}
	if err := registry.saveEntries(entries); err != nil {
		t.Fatalf("save entries: %v", err)
	}

	counted, err := registry.CountOrphaned(time.Hour)
	if err != nil {
		t.Fatalf("CountOrphaned: %v", err)
	}
	if counted != 1 {
		t.Fatalf("CountOrphaned = %d, want 1 (the contained workspace)", counted)
	}
	requireExists(t, filepath.Join(staleDir, workspaceMarker), "orphaned workspace")
	requireExists(t, outsideDir, "out-of-root dir")
	entries, err = registry.loadEntries()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != staleDir {
		t.Fatalf("CountOrphaned must keep the workspace entry and drop only the untrusted one, got %+v", entries)
	}
	if !strings.Contains(out.String(), "Refusing to remove registry entry "+outsideDir) {
		t.Fatalf("the untrusted entry must get the same warning as a real sweep:\n%s", out.String())
	}

	cleaned, err := registry.CleanupOrphaned(time.Hour)
	if err != nil {
		t.Fatalf("CleanupOrphaned: %v", err)
	}
	if cleaned != counted {
		t.Fatalf("CleanupOrphaned removed %d, CountOrphaned announced %d", cleaned, counted)
	}
	requireGone(t, staleDir, "orphaned workspace")
}

func runDryRunBackup(t *testing.T, level types.LogLevel, bundle bool) (*BackupStats, string) {
	t.Helper()
	logger := logging.New(level, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	orch := New(logger, true)
	orch.SetBackupConfig(t.TempDir(), t.TempDir(), types.CompressionNone, 0, 0, "standard", nil)
	orch.SetConfig(&config.Config{
		BundleAssociatedFiles: bundle,
		SystemRootPrefix:      t.TempDir(), // isolate collection from the real host
	})
	orch.RegisterStorageTarget(&testStorageTarget{})

	stats, err := orch.RunGoBackup(context.Background(), &environment.EnvironmentInfo{Type: types.ProxmoxUnknown, Version: "unknown"}, "dry-host")
	if err != nil {
		t.Fatalf("RunGoBackup dry run failed: %v", err)
	}
	return stats, buf.String()
}

// F4 (a), (b), (c): the dry run names the final archive once, under step [3]; step
// [5] says it is skipped like [4] and [6] (or, with bundling off, prints the same SKIP
// line as a real run); the workspace is reported like a real run.
func TestRunGoBackupDryRunStepLines(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping RunGoBackup dry-run test in short mode")
	}

	for _, bundle := range []bool{true, false} {
		stats, out := runDryRunBackup(t, types.LogLevelInfo, bundle)

		// (a) one archive line, final name, between [3] and [4].
		archiveLine := outputLine(t, out, "[DRY RUN] Would create archive:")
		if !strings.HasSuffix(archiveLine, "[DRY RUN] Would create archive: "+stats.ArchivePath) {
			t.Fatalf("archive line must name the final archive %q, got %q", stats.ArchivePath, archiveLine)
		}
		if strings.Contains(out, ".partial") {
			t.Fatalf("the internal partial name must not appear in a dry run:\n%s", out)
		}
		step3 := strings.Index(out, "[3] Creation of compressed archive")
		archiveAt := strings.Index(out, "[DRY RUN] Would create archive:")
		step4 := strings.Index(out, "[4] Verification skipped (dry run mode)")
		bundleLine := "[5] Bundling skipped (dry run mode)"
		if !bundle {
			bundleLine = "Bundling disabled"
		}
		step5 := strings.Index(out, bundleLine)
		step6 := strings.Index(out, "[6] Storage dispatch skipped (dry run mode)")
		if step3 < 0 || step4 < 0 || step5 < 0 || step6 < 0 {
			t.Fatalf("bundle=%v: missing a step line ([3]=%d [4]=%d [5]=%d [6]=%d):\n%s", bundle, step3, step4, step5, step6, out)
		}
		if step3 >= archiveAt || archiveAt >= step4 || step4 >= step5 || step5 >= step6 {
			t.Fatalf("bundle=%v: wrong order ([3]=%d archive=%d [4]=%d [5]=%d [6]=%d):\n%s", bundle, step3, archiveAt, step4, step5, step6, out)
		}

		// (b) step [5] is emitted exactly like [4] and [6]: a STEP line. With bundling
		// off there is no step [5], only the SKIP line a real run prints.
		steps := []string{"[4] Verification skipped (dry run mode)", "[6] Storage dispatch skipped (dry run mode)"}
		if bundle {
			steps = append(steps, "[5] Bundling skipped (dry run mode)")
		} else {
			if line := outputLine(t, out, "Bundling disabled"); !strings.Contains(line, "SKIP") {
				t.Fatalf("bundling off: want the real run's SKIP line, got %q", line)
			}
			if strings.Contains(out, "[5]") {
				t.Fatalf("bundling off: no step [5] may be printed:\n%s", out)
			}
		}
		for _, step := range steps {
			if line := outputLine(t, out, step); !strings.Contains(line, "STEP") {
				t.Fatalf("%q must be a STEP line, got %q", step, line)
			}
		}

		// (c) no INFO line about the workspace.
		if strings.Contains(out, "Temporary directory would be") || strings.Contains(out, "Using temporary directory") {
			t.Fatalf("the workspace must not be reported at INFO:\n%s", out)
		}
	}

	// (c) at DEBUG, the line a real run prints.
	_, debugOut := runDryRunBackup(t, types.LogLevelDebug, true)
	if line := outputLine(t, debugOut, "Using temporary directory: "+workspaceRoot+"/proxsave-dry-host-"); !strings.Contains(line, "DEBUG") {
		t.Fatalf("workspace line must be DEBUG, got %q", line)
	}
	if strings.Contains(debugOut, "Temporary directory would be") {
		t.Fatalf("the dry-run-only workspace line must be gone:\n%s", debugOut)
	}
}

// N1: the dry run writes no report, so it must not hand the caller a report path
// (the caller prints "Statistics report saved" only when one is set).
func TestSaveStatsReportDryRunLeavesNoReportPath(t *testing.T) {
	var out bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&out)
	logDir := t.TempDir()
	stats := &BackupStats{Timestamp: time.Date(2026, 10, 2, 11, 33, 6, 0, time.UTC)}
	reportPath := filepath.Join(logDir, "backup-stats-20261002-113306.json")

	dry := New(logger, true)
	dry.SetBackupConfig(t.TempDir(), logDir, types.CompressionNone, 0, 0, "standard", nil)
	if err := dry.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport (dry run): %v", err)
	}
	if stats.ReportPath != "" {
		t.Fatalf("dry run must leave ReportPath empty, got %q", stats.ReportPath)
	}
	outputLine(t, out.String(), "[DRY RUN] Would write stats report: "+reportPath)
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("dry run must not write the report, stat err=%v", err)
	}

	real := New(logger, false)
	real.SetBackupConfig(t.TempDir(), logDir, types.CompressionNone, 0, 0, "standard", nil)
	if err := real.SaveStatsReport(stats); err != nil {
		t.Fatalf("SaveStatsReport (real): %v", err)
	}
	if stats.ReportPath != reportPath {
		t.Fatalf("real run ReportPath = %q, want %q", stats.ReportPath, reportPath)
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("real run must write the report: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("report is not JSON: %s", data)
	}
}

// N4: the current run's own profiles (passed from bootstrap) are not "previous
// execution" files: a real cleanup keeps them and removes the others, a dry run
// leaves everything and does not count them.
func TestCleanupPreviousExecutionArtifactsKeepsRunProfiles(t *testing.T) {
	origRoot := workspaceRoot
	workspaceRoot = t.TempDir()
	t.Cleanup(func() { workspaceRoot = origRoot })

	profDir := filepath.Join("/tmp", "proxsave")
	if err := os.MkdirAll(profDir, 0o755); err != nil {
		t.Fatalf("prepare %s: %v", profDir, err)
	}
	stamp := time.Now().UnixNano()
	ownCPU := filepath.Join(profDir, fmt.Sprintf("cpu-n4own-%d.pprof", stamp))
	ownHeap := filepath.Join(profDir, fmt.Sprintf("heap-n4own-%d.pprof", stamp))
	prevCPU := filepath.Join(profDir, fmt.Sprintf("cpu-n4prev-%d.pprof", stamp))
	prevHeap := filepath.Join(profDir, fmt.Sprintf("heap-n4prev-%d.pprof", stamp))
	for _, p := range []string{ownCPU, ownHeap, prevCPU, prevHeap} {
		if err := os.WriteFile(p, []byte("pprof"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		p := p
		t.Cleanup(func() { _ = os.Remove(p) })
	}

	newOrch := func(dryRun bool, out *bytes.Buffer) *Orchestrator {
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(out)
		reg, err := NewTempDirRegistry(logging.New(types.LogLevelError, false), filepath.Join(t.TempDir(), "registry.json"))
		if err != nil {
			t.Fatalf("NewTempDirRegistry: %v", err)
		}
		o := &Orchestrator{logger: logger, dryRun: dryRun, logPath: t.TempDir(), tempRegistry: reg}
		o.SetRunProfilePaths(ownCPU, "", ownHeap)
		return o
	}

	wantFiles := tmpProfileCount(t) - 2
	var dryOut bytes.Buffer
	newOrch(true, &dryOut).cleanupPreviousExecutionArtifacts(context.Background())
	outputLine(t, dryOut.String(), fmt.Sprintf("[DRY RUN] Would remove %d item(s) from previous executions (%d file(s), 0 dir(s))", wantFiles, wantFiles))
	for _, p := range []string{ownCPU, ownHeap, prevCPU, prevHeap} {
		requireExists(t, p, p)
	}

	var realOut bytes.Buffer
	newOrch(false, &realOut).cleanupPreviousExecutionArtifacts(context.Background())
	outputLine(t, realOut.String(), fmt.Sprintf("Cleanup of previous execution files completed successfully (%d item(s) removed: %d file(s), 0 dir(s))", wantFiles, wantFiles))
	if _, err := os.Stat(ownCPU); err != nil {
		t.Fatalf("this run's CPU profile must survive the cleanup: %v", err)
	}
	if _, err := os.Stat(ownHeap); err != nil {
		t.Fatalf("this run's heap profile path must survive the cleanup: %v", err)
	}
	requireGone(t, prevCPU, "previous CPU profile")
	requireGone(t, prevHeap, "previous heap profile")
}
