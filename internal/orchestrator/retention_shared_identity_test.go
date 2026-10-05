package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

const sharedIdentityServerID = "0123456789012345"

// sharedIdentitySeed is one archive on disk: its owner and identity live in the
// .metadata beside it, its date in both the manifest and the mtime.
type sharedIdentitySeed struct {
	host     string
	serverID string
	when     time.Time
}

func (s sharedIdentitySeed) name() string {
	return fmt.Sprintf("%s-backup-%s.tar.zst", s.host, s.when.Format("20060102-150405"))
}

// seedSharedIdentityDir writes the seeds into dir and returns their paths in order.
// Every archive carries a .sha256 (an unverified entry is inert for retention) and a
// parseable manifest (a missing one raises an unrelated WARNING).
func seedSharedIdentityDir(t *testing.T, dir string, seeds []sharedIdentitySeed) []string {
	t.Helper()
	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		paths[i] = filepath.Join(dir, seed.name())
		manifest := fmt.Sprintf(`{"hostname":%q,"server_id":%q,"created_at":%q}`, seed.host, seed.serverID, seed.when.Format(time.RFC3339))
		for suffix, content := range map[string]string{"": "archive", ".sha256": "h  archive\n", ".metadata": manifest} {
			if err := os.WriteFile(paths[i]+suffix, []byte(content), 0o600); err != nil {
				t.Fatalf("seed %s%s: %v", seed.name(), suffix, err)
			}
		}
		if err := os.Chtimes(paths[i], seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name(), err)
		}
	}
	return paths
}

// syncRealSecondary runs the Secondary block of a run over a real SecondaryStorage at
// dir: Store copies this run's archive in from its own directory (with its sidecars),
// then retention runs over everything there. It returns what an operator reads and the
// exit code the run would end with. The owner is the user running the test: the
// default root:root cannot be set unprivileged, and the WARNING that follows would
// decide the exit code instead of retention.
func syncRealSecondary(t *testing.T, dir, runHostname string, thisRun sharedIdentitySeed) (string, int) {
	t.Helper()
	source := seedSharedIdentityDir(t, t.TempDir(), []sharedIdentitySeed{thisRun})[0]
	u, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("current group: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "run.log")
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	cfg := &config.Config{
		SecondaryEnabled:       true,
		SecondaryPath:          dir,
		ServerID:               sharedIdentityServerID,
		SecondaryRetentionDays: 10,
		SetBackupPermissions:   true,
		BackupUser:             u.Username,
		BackupGroup:            g.Name,
	}
	secondary, err := storage.NewSecondaryStorage(cfg, logger, runHostname)
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	stats := sampleAdapterStats()
	stats.ArchivePath = source
	stats.StartTime = thisRun.when
	if err := NewStorageAdapter(secondary, logger, cfg).Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile: %v", err)
	}
	categories, errorCount, warningCount, notifyCount := ParseLogCounts(logPath, 10)
	run := &BackupStats{ExitCode: types.ExitSuccess.Int(), ErrorCount: errorCount, WarningCount: warningCount, NotifyCount: notifyCount, LogCategories: categories}
	applyIssueExitCode(run)
	return buf.String(), run.ExitCode
}

// TestASecondWriterWithOurIdentityWarnsTheRun is the clone end to end: a second machine
// kept this host's server identity and still writes to the same location under its own
// name. Its archives are adopted and rotated as before; the pass now states the second
// writer and its outcome is a WARNING, so the run ends at exit 1.
func TestASecondWriterWithOurIdentityWarnsTheRun(t *testing.T) {
	const runHostname = "hosta.example.test"
	dir := t.TempDir()
	seedSharedIdentityDir(t, dir, []sharedIdentitySeed{
		{host: "hostb.example.test", serverID: sharedIdentityServerID, when: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)},
		{host: runHostname, serverID: sharedIdentityServerID, when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	})
	thisRun := sharedIdentitySeed{host: runHostname, serverID: sharedIdentityServerID, when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)}

	out, exitCode := syncRealSecondary(t, dir, runHostname, thisRun)
	requireLines(t, out,
		"INFO     Applying retention policy...",
		"INFO       Adopted: 1 backups named hostb.example.test, same server identity",
		"INFO       Still writing here: hostb.example.test",
		"INFO     ✓ Nothing to delete",
		"WARNING  ⚠ Server identity shared with hostb.example.test",
	)
	if n := strings.Count(out, "WARNING "); n != 1 {
		t.Errorf("%d WARNING lines, want only the retention outcome:\n%s", n, out)
	}
	if exitCode == types.ExitSuccess.Int() {
		t.Errorf("exit code = %d: a WARNING outcome must not leave the run clean:\n%s", exitCode, out)
	}
}

// TestARenamedHostDoesNotWarnTheRun is the same host under a new name: every archive
// under the old name predates the new name's first, so nothing is stated and the run
// stays clean.
func TestARenamedHostDoesNotWarnTheRun(t *testing.T) {
	const runHostname = "hosta.example.test"
	dir := t.TempDir()
	seedSharedIdentityDir(t, dir, []sharedIdentitySeed{
		{host: runHostname, serverID: sharedIdentityServerID, when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)},
		{host: "hostold.example.test", serverID: sharedIdentityServerID, when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)},
	})
	thisRun := sharedIdentitySeed{host: runHostname, serverID: sharedIdentityServerID, when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC)}

	out, exitCode := syncRealSecondary(t, dir, runHostname, thisRun)
	requireLines(t, out,
		"INFO       Adopted: 1 backups named hostold.example.test, same server identity",
		"INFO     ✓ Nothing to delete",
	)
	if strings.Contains(out, "Still writing here") || strings.Contains(out, "Server identity shared") {
		t.Errorf("a rename was reported as a second writer:\n%s", out)
	}
	if exitCode != types.ExitSuccess.Int() {
		t.Errorf("exit code = %d, want %d for a renamed host:\n%s", exitCode, types.ExitSuccess.Int(), out)
	}
}

// TestTheFirstOwnBackupDoesNotWarnTheRun is the first run under this name: with no
// earlier own backup here a clone cannot be told from a rename, so nothing is stated.
func TestTheFirstOwnBackupDoesNotWarnTheRun(t *testing.T) {
	const runHostname = "hosta.example.test"
	dir := t.TempDir()
	seedSharedIdentityDir(t, dir, []sharedIdentitySeed{
		{host: "hostb.example.test", serverID: sharedIdentityServerID, when: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)},
	})
	thisRun := sharedIdentitySeed{host: runHostname, serverID: sharedIdentityServerID, when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)}

	out, exitCode := syncRealSecondary(t, dir, runHostname, thisRun)
	requireLines(t, out, "INFO       Adopted: 1 backups named hostb.example.test, same server identity")
	if strings.Contains(out, "Still writing here") || strings.Contains(out, "Server identity shared") {
		t.Errorf("the first own backup here was reported as a second writer:\n%s", out)
	}
	if exitCode != types.ExitSuccess.Int() {
		t.Errorf("exit code = %d, want %d on the first own backup here:\n%s", exitCode, types.ExitSuccess.Int(), out)
	}
}
