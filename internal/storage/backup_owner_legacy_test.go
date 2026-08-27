package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// recordedLogLine is one line the retention scope reporting emitted, together with
// the level it went out at. The level is the point of this fake: it is what decides
// whether the run's exit code moves. ParseLogCounts counts WARNING lines and
// applyIssueExitCode promotes an otherwise clean run to exit 1 on any of them, so a
// test that matched only the text would happily pass on a report that turns every
// affected run red for ever.
type recordedLogLine struct {
	level   string
	message string
}

// levelRecordingLogger is a retentionScopeLogger that keeps every line and its
// level. A real logging.Logger cannot serve here: it renders the level into the
// output stream, so a text scan for "WARNING" would also match a message that
// merely contains the word.
type levelRecordingLogger struct {
	mu    sync.Mutex
	lines []recordedLogLine
}

func (l *levelRecordingLogger) record(level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, recordedLogLine{level: level, message: fmt.Sprintf(format, args...)})
}

func (l *levelRecordingLogger) Warning(format string, args ...interface{}) {
	l.record("WARNING", format, args...)
}

func (l *levelRecordingLogger) Info(format string, args ...interface{}) {
	l.record("INFO", format, args...)
}

func (l *levelRecordingLogger) Debug(format string, args ...interface{}) {
	l.record("DEBUG", format, args...)
}

// levelOf returns the level of the first recorded line containing needle, or "" when
// no recorded line contains it.
func (l *levelRecordingLogger) levelOf(needle string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line.message, needle) {
			return line.level
		}
	}
	return ""
}

// countAtLevel counts the recorded lines emitted at the given level.
func (l *levelRecordingLogger) countAtLevel(level string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, line := range l.lines {
		if line.level == level {
			count++
		}
	}
	return count
}

// messagesAtLevel renders the recorded messages of one level, for failure output.
func (l *levelRecordingLogger) messagesAtLevel(level string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, line := range l.lines {
		if line.level == level {
			out = append(out, line.message)
		}
	}
	return out
}

// writeRetentionFixtureFile writes one file of a retention fixture into dir and
// returns its full path. It is named for retention rather than carrying a bare
// "write" name so it cannot collide with a future helper of a more general name
// inside package storage.
func writeRetentionFixtureFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// TestUnattributableLegacyArchiveIsClaimedByNoHost states the rule as an
// impossibility rather than as one host's answer: a pre-Go "proxmox-backup-*" name
// carries no host token (its leading label is the product name), so when its
// manifest names no host either, nothing anywhere can say which machine wrote it and
// no machine may claim it.
//
// It is asserted for TWO different hosts, one of which answers to an FQDN, because
// the harm is not "host A is wrong", it is "every host says yes", which on a shared
// directory or remote prefix is each machine deleting the others' archives.
//
// backupBelongsToHost is handed neither the location nor the rest of the listing, so
// no rule that reintroduces a location-dependent or listing-dependent claim can pass
// this test without changing the signature. That is deliberate.
func TestUnattributableLegacyArchiveIsClaimedByNoHost(t *testing.T) {
	// The path is the shape discussion #292 reports: a BACKUP_PATH on a networked
	// drive, which is a "local" location that several machines can share.
	entry := &types.BackupMetadata{BackupFile: "/mnt/synopbs/backup/proxmox-backup-20250102-100000.tar.gz"}

	if backupBelongsToHost(entry, hostOnly("hostA")) {
		t.Error("hostA claimed a pre-Go archive that names no host. Nothing in the archive says hostA wrote it, and on a shared location claiming it means deleting another machine's backup (discussion #292)")
	}
	if backupBelongsToHost(entry, hostOnly("pve", "pve.home.arpa")) {
		t.Error("a second, differently named host claimed the same pre-Go archive. Both hosts saying yes to the same archive is the cross-host deletion channel this rule exists to close")
	}
}

// TestLegacyArchiveNamingItsHostStillRotatesOnThatHost is the other direction, and
// it is what keeps the rule from being a blanket refusal: a pre-Go archive whose
// bash-era sidecar names its host in full is attributed by that name, so it keeps
// rotating on that machine and stays untouchable everywhere else.
//
// It runs through LocalStorage.List rather than constructing the metadata by hand,
// because the attribution it pins is the one backup.LoadManifest reaches by falling
// back to the KEY=VALUE parser, and a hand-built entry would skip exactly that step.
func TestLegacyArchiveNamingItsHostStillRotatesOnThatHost(t *testing.T) {
	dir := t.TempDir()
	const archive = "proxmox-backup-20250102-100000.tar.gz"

	path := writeRetentionFixtureFile(t, dir, archive, "archive")
	writeRetentionFixtureFile(t, dir, archive+".sha256", "h  archive\n")
	// The pre-Go pipeline wrote KEY=VALUE sidecars, never JSON. This one names its
	// host, which is the whole difference between this test and the one above.
	writeRetentionFixtureFile(t, dir, archive+".metadata", "COMPRESSION_TYPE=gzip\nHOSTNAME=hostB\n")

	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "hostA")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	listed, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d entries, want 1: %+v", len(listed), listed)
	}

	if owner := backupOwnerHost(listed[0]); owner != "hostB" {
		t.Fatalf("backupOwnerHost = %q, want hostB. A pre-Go archive whose sidecar names its host must be attributed by that name, or dropping the legacy claim rule would stop it rotating anywhere", owner)
	}
	if backupBelongsToHost(listed[0], hostOnly("hostA")) {
		t.Error("hostA claimed an archive whose sidecar names hostB. That is one machine deleting another machine's backup (discussion #292)")
	}
	if !backupBelongsToHost(listed[0], hostOnly("hostB")) {
		t.Error("hostB did not claim an archive its own sidecar names it in. It would then never rotate on any host and the location would grow without bound")
	}

	// The secondary backend never opens a manifest during List, so it attributes
	// through this helper instead. Pinned here so the two paths cannot drift.
	if host, _ := manifestOwnerFromLocalArchive(context.Background(), path, 5*time.Second); host != "hostB" {
		t.Fatalf("manifestOwnerFromLocalArchive = %q, want hostB: the secondary location must attribute the same archive the same way", host)
	}
}

// TestUnclaimedLegacyArchivesAreReportedWithoutRaisingTheRunSeverity pins the
// reporting half of the rule at its own seam. Three assertions on three different
// seams:
//
//  1. the scoped set is exactly the archive this host can name (classification),
//  2. a line naming the case exists, so the operator can find out why rotation
//     stopped (reporting),
//  3. that line is INFO and NOTHING was emitted at WARNING (the exit code).
//
// The third is the load-bearing one. An archive nobody can name is a fixed backlog
// fact that no future run will change: counting it as a run issue would promote
// every affected run to exit 1 for ever through applyIssueExitCode, which is the
// symptom of discussion #292 rather than a report of it. Asserting ZERO warnings
// rather than merely checking the new line's level is what catches the half-fix that
// downgrades the new line while leaving the pre-existing generic warning counting
// the same entries.
func TestUnclaimedLegacyArchivesAreReportedWithoutRaisingTheRunSeverity(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "hostA-backup-20250103-100000.tar.zst", Hostname: "hostA"},
		{BackupFile: "proxmox-backup-20250102-100000.tar.gz"},
	}

	logger := &levelRecordingLogger{}
	scoped, _ := applyRetentionHostScope("Local storage", hostOnly("hostA"), backups, logger)

	if len(scoped) != 1 || scoped[0] != backups[0] {
		t.Fatalf("scoped %d entries (%+v), want exactly the archive this host can name", len(scoped), scoped)
	}
	if level := logger.levelOf("no host will ever delete them"); level == "" {
		t.Error("nothing named the unclaimed archives. Retention silently stops rotating them, and the operator's only way to find out is disk usage")
	} else if level != "INFO" {
		t.Errorf("the unclaimed-archive line was emitted at %s, want INFO. A WARNING line is counted by ParseLogCounts and promotes the run to exit 1, and since these archives are never pruned it would do so on every run for ever", level)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) emitted for a location where nothing is contended: %q. Every one of them promotes an otherwise clean run to exit 1, permanently", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestForeignHostArchivesStillRaiseTheRunSeverity is the other half of the split,
// and what stops the test above from being a blanket mute. Another machine writing
// into this location is live information about what retention will and will not
// prune, it can change from run to run, and it reaches the run status today. It must
// keep doing so.
func TestForeignHostArchivesStillRaiseTheRunSeverity(t *testing.T) {
	backups := []*types.BackupMetadata{
		{BackupFile: "hostA-backup-20250103-100000.tar.zst", Hostname: "hostA"},
		{BackupFile: "hostB-backup-20250102-100000.tar.zst", Hostname: "hostB"},
	}

	logger := &levelRecordingLogger{}
	scoped, _ := applyRetentionHostScope("Local storage", hostOnly("hostA"), backups, logger)

	if len(scoped) != 1 || scoped[0] != backups[0] {
		t.Fatalf("scoped %d entries (%+v), want exactly this host's own archive", len(scoped), scoped)
	}
	if level := logger.levelOf("do not belong to hostA"); level != "WARNING" {
		t.Errorf("an archive attributed to another host was reported at %q, want WARNING. It changes what retention prunes here and has to reach the run status and the exit code, exactly as it does today. Lines: %+v", level, logger.lines)
	}
}

// TestLocalRetentionLeavesUnattributableLegacyArchivesAlone is the networked
// BACKUP_PATH shape end to end, with t.TempDir() standing in for the shared mount.
// Discussion #292 reports a BACKUP_PATH of /mnt/synopbs/backup with the note that
// the backup target is a networked drive, so "local" here means "reached through the
// filesystem", never "this machine's own disk".
//
// Both directions are asserted: the archive this host cannot name survives, AND this
// host's own surplus is still pruned. Without the second half the test would pass on
// a change that simply turned retention off.
func TestLocalRetentionLeavesUnattributableLegacyArchivesAlone(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "hostA", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	seeds := []struct {
		name     string
		when     time.Time
		metadata string
	}{
		// The pre-Go archive: its sidecar parses but carries no HOSTNAME line, so
		// nothing anywhere names the machine that wrote it. It is the oldest, so a
		// rule that claimed it would delete it first.
		{name: "proxmox-backup-20250101-100000.tar.gz", when: time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC), metadata: "COMPRESSION_TYPE=gzip\n"},
		{name: "hostA-backup-20250102-100000.tar.zst", when: time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC), metadata: "HOSTNAME=hostA\n"},
		{name: "hostA-backup-20250103-100000.tar.zst", when: time.Date(2025, 1, 3, 10, 0, 0, 0, time.UTC), metadata: "HOSTNAME=hostA\n"},
	}

	paths := make([]string, len(seeds))
	for i, seed := range seeds {
		// Every archive carries a .sha256 because backupHasCompletionSidecar accepts
		// .manifest.json, .sha256 or a bundle suffix but NOT .metadata, and an
		// unverified entry is inert for retention: without it this test would pass
		// for the wrong reason.
		paths[i] = writeRetentionFixtureFile(t, dir, seed.name, "archive")
		writeRetentionFixtureFile(t, dir, seed.name+".sha256", "h  archive\n")
		writeRetentionFixtureFile(t, dir, seed.name+".metadata", seed.metadata)
		if err := os.Chtimes(paths[i], seed.when, seed.when); err != nil {
			t.Fatalf("chtimes %s: %v", seed.name, err)
		}
	}

	l, err := NewLocalStorage(&config.Config{BackupPath: dir}, newTestLogger(), "hostA")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}

	if _, err := os.Stat(paths[0]); err != nil {
		t.Errorf("retention deleted %s: nothing names the machine that wrote it, and this location can be a mount several hosts share, so claiming it means deleting another machine's backup (stat err=%v)", filepath.Base(paths[0]), err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Errorf("retention spared this host's own surplus archive %s: scoping must narrow what retention prunes, not switch it off (stat err=%v)", filepath.Base(paths[1]), err)
	}
	if _, err := os.Stat(paths[2]); err != nil {
		t.Errorf("retention deleted the archive it was told to keep: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1 (this host's own surplus, and nothing else); the count feeds the run summary and the retention report", deleted)
	}
}
