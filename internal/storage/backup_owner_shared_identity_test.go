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

// sharedIdentityAt is one hour of January 2025, the only dates these fixtures need.
func sharedIdentityAt(day, hour int) time.Time {
	return time.Date(2025, 1, day, hour, 0, 0, 0, time.UTC)
}

// sharedIdentityEntry is one listed archive as a retention pass sees it after
// attribution: the name its manifest records, the identity it carries and its date.
func sharedIdentityEntry(host string, when time.Time, serverID string) *types.BackupMetadata {
	return &types.BackupMetadata{
		BackupFile: fmt.Sprintf("%s-backup-%s.tar.zst", host, when.Format("20060102-150405")),
		Hostname:   host,
		ServerID:   serverID,
		Timestamp:  when,
	}
}

// lineIndex returns the position of the first recorded line at level containing
// needle, or -1.
func (l *levelRecordingLogger) lineIndex(level, needle string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, line := range l.lines {
		if line.level == level && strings.Contains(line.message, needle) {
			return i
		}
	}
	return -1
}

// TestASecondWriterCarryingOurIdentityIsStillWritingHere is the clone: a second
// machine kept this host's server identity (a copied disk, a restored container) and
// writes to the same location under its own name. Its archives are adopted, as the
// rule says, and one of them is newer than this host's previous own backup, which a
// rename can never produce: the old name stops before the new one starts.
func TestASecondWriterCarryingOurIdentityIsStillWritingHere(t *testing.T) {
	thisRun := sharedIdentityEntry("pve", sharedIdentityAt(2, 10), ourServerID)
	backups := []*types.BackupMetadata{
		thisRun,
		sharedIdentityEntry("pve2", sharedIdentityAt(1, 12), ourServerID),
		sharedIdentityEntry("pve3", sharedIdentityAt(1, 11), ourServerID),
		sharedIdentityEntry("pve", sharedIdentityAt(1, 10), ourServerID),
	}
	id := hostWithIdentity("pve", ourServerID)
	id.thisRunArchive = filepath.Base(thisRun.BackupFile)

	logger := &levelRecordingLogger{}
	scope, err := applyRetentionHostScope("Local storage", id, backups, logger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}

	if len(scope.owned) != 4 {
		t.Fatalf("scoped %d of 4 entries; the rotation must not change, every archive here carries this host's identity", len(scope.owned))
	}
	if got := strings.Join(scope.sharedWith, ","); got != "pve2,pve3" {
		t.Fatalf("sharedWith = %q, want \"pve2,pve3\": both names wrote after this host's previous own backup", got)
	}

	adopted := logger.lineIndex("INFO", "  Adopted: 1 backups named pve2, same server identity")
	evidence := logger.lineIndex("DEBUG", "Local storage: retention shared identity: name=pve2 newest=2025-01-01T12:00:00Z previous_own=2025-01-01T10:00:00Z")
	fact := logger.lineIndex("INFO", "  Still writing here: pve2 and pve3")
	if adopted < 0 || evidence < 0 || fact < 0 {
		t.Fatalf("adopted=%d evidence=%d fact=%d, want all present. Lines: %+v", adopted, evidence, fact, logger.lines)
	}
	if adopted >= evidence || evidence >= fact {
		t.Errorf("order adopted=%d evidence=%d fact=%d, want the Adopted fact, then the DEBUG evidence, then the Still writing here fact", adopted, evidence, fact)
	}
	if n := logger.countAtLevel("WARNING"); n != 0 {
		t.Errorf("%d WARNING line(s) from the scope: the outcome line is the caller's. %q", n, logger.messagesAtLevel("WARNING"))
	}
}

// TestARenameIsNeverReportedAsASecondWriter is the same host under a new name: every
// archive under the old name predates the first one under the new name, so none is
// newer than this host's previous own backup and nothing is reported. They are still
// adopted and rotated.
func TestARenameIsNeverReportedAsASecondWriter(t *testing.T) {
	thisRun := sharedIdentityEntry("pve-new", sharedIdentityAt(4, 10), ourServerID)
	backups := []*types.BackupMetadata{
		thisRun,
		sharedIdentityEntry("pve-new", sharedIdentityAt(3, 10), ourServerID),
		sharedIdentityEntry("pve-old", sharedIdentityAt(2, 10), ourServerID),
		sharedIdentityEntry("pve-old", sharedIdentityAt(1, 10), ourServerID),
	}
	id := hostWithIdentity("pve-new", ourServerID)
	id.thisRunArchive = filepath.Base(thisRun.BackupFile)

	logger := &levelRecordingLogger{}
	scope, err := applyRetentionHostScope("Local storage", id, backups, logger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if len(scope.owned) != 4 {
		t.Fatalf("scoped %d of 4 entries, want 4: the old name's archives are adopted and rotate", len(scope.owned))
	}
	if len(scope.sharedWith) != 0 {
		t.Errorf("sharedWith = %v, want none for a rename", scope.sharedWith)
	}
	if level := logger.levelOf("Still writing here"); level != "" {
		t.Errorf("a rename printed the Still writing here fact at %s", level)
	}
	if level := logger.levelOf("retention shared identity: name=pve-old newest=2025-01-02T10:00:00Z previous_own=2025-01-03T10:00:00Z"); level != "DEBUG" {
		t.Errorf("the DEBUG evidence for the rename is at %q, want DEBUG. Lines: %+v", level, logger.lines)
	}
}

// TestTheFirstOwnBackupHereReportsNothing is the first run after a rename or a clone:
// this host has no earlier backup here, so the two cannot be told apart yet and nothing
// is reported, even when the other name's archive is the newest one listed.
func TestTheFirstOwnBackupHereReportsNothing(t *testing.T) {
	thisRun := sharedIdentityEntry("pve", sharedIdentityAt(2, 10), ourServerID)
	backups := []*types.BackupMetadata{
		sharedIdentityEntry("pve2", sharedIdentityAt(2, 11), ourServerID),
		thisRun,
		sharedIdentityEntry("pve2", sharedIdentityAt(1, 10), ourServerID),
	}
	id := hostWithIdentity("pve", ourServerID)
	id.thisRunArchive = filepath.Base(thisRun.BackupFile)

	logger := &levelRecordingLogger{}
	scope, err := applyRetentionHostScope("Local storage", id, backups, logger)
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if len(scope.sharedWith) != 0 {
		t.Errorf("sharedWith = %v, want none: with no previous own backup here a clone cannot be told from a rename", scope.sharedWith)
	}
	if level := logger.levelOf("Still writing here"); level != "" {
		t.Errorf("the first own backup here printed the Still writing here fact at %s", level)
	}
	if level := logger.levelOf("retention shared identity: previous_own=none"); level != "DEBUG" {
		t.Errorf("no DEBUG line says there is no previous own backup (level %q). Lines: %+v", level, logger.lines)
	}
}

// TestThisRunsBundleIsNotThePreviousOwnBackup pins the match between the archive Store
// was handed and the entry the listing returns for it: a destination with bundling on
// lists "<archive>.bundle.tar" for it. Were the two not matched, this run's bundle would
// be taken for the previous own backup and the clone, older than it, would go unreported.
func TestThisRunsBundleIsNotThePreviousOwnBackup(t *testing.T) {
	thisRun := sharedIdentityEntry("pve", sharedIdentityAt(3, 10), ourServerID)
	storedName := filepath.Base(thisRun.BackupFile)
	thisRun.BackupFile += bundleSuffix
	backups := []*types.BackupMetadata{
		thisRun,
		sharedIdentityEntry("pve2", sharedIdentityAt(2, 10), ourServerID),
		sharedIdentityEntry("pve", sharedIdentityAt(1, 10), ourServerID),
	}
	id := hostWithIdentity("pve", ourServerID)
	id.thisRunArchive = storedName

	scope, err := applyRetentionHostScope("Secondary storage", id, backups, &levelRecordingLogger{})
	if err != nil {
		t.Fatalf("applyRetentionHostScope: %v", err)
	}
	if got := strings.Join(scope.sharedWith, ","); got != "pve2" {
		t.Errorf("sharedWith = %q, want \"pve2\": this run's bundle must not count as the previous own backup", got)
	}
}

// TestStoreRecordsThisRunsArchive pins the seam the rule reads on every backend: Store
// records the base name it was handed even when it fails, because the adapter carries on
// with retention after a failed store, and this run's archive is then simply absent.
func TestStoreRecordsThisRunsArchive(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "pve-backup-20250102-100000.tar.zst")
	meta := &types.BackupMetadata{BackupFile: missing}

	local, err := NewLocalStorage(&config.Config{BackupPath: t.TempDir()}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	secondary, err := NewSecondaryStorage(&config.Config{SecondaryEnabled: true, SecondaryPath: t.TempDir()}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	cloud, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "gdrive"}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}

	_ = local.Store(context.Background(), missing, meta)
	_ = secondary.Store(context.Background(), missing, meta)
	_ = cloud.Store(context.Background(), missing, meta)

	want := filepath.Base(missing)
	for name, got := range map[string]string{"local": local.thisRunArchive, "secondary": secondary.thisRunArchive, "cloud": cloud.thisRunArchive} {
		if got != want {
			t.Errorf("%s: thisRunArchive = %q, want %q", name, got, want)
		}
	}
}

// TestLocalRetentionReportsASecondWriterAndRotatesUnchanged is the clone on the real
// local backend: Store, then the retention pass, through the manifests on disk. The
// rotation is the one adoption already produces, against this host's limit; only the
// fact and the summary field are new.
func TestLocalRetentionReportsASecondWriterAndRotatesUnchanged(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	paths := seedServerIDFixture(t, dir, []serverIDSeed{
		{name: "pve-backup-20250102-100000.tar.zst", when: sharedIdentityAt(2, 10), manifestHost: "pve", manifestID: ourServerID},
		{name: "pve2-backup-20250101-120000.tar.zst", when: sharedIdentityAt(1, 12), manifestHost: "pve2", manifestID: ourServerID},
		{name: "pve-backup-20250101-100000.tar.zst", when: sharedIdentityAt(1, 10), manifestHost: "pve", manifestID: ourServerID},
	})

	logger, buf := newRecordingRetentionLogger()
	l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	_ = l.Store(context.Background(), paths[0], &types.BackupMetadata{BackupFile: paths[0], Timestamp: sharedIdentityAt(2, 10)})

	deleted, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 2})
	if err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1: the clone's archive counts against this host's limit, as adoption already does", deleted)
	}
	if _, err := os.Stat(paths[2]); !os.IsNotExist(err) {
		t.Errorf("the oldest archive survived (stat err=%v)", err)
	}
	for _, path := range paths[:2] {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("retention deleted %s: %v", filepath.Base(path), err)
		}
	}

	summary := l.LastRetentionSummary()
	if summary.SharedIdentityNames != "pve2" {
		t.Errorf("SharedIdentityNames = %q, want \"pve2\"", summary.SharedIdentityNames)
	}
	if !strings.Contains(buf.String(), "  Still writing here: pve2\n") {
		t.Errorf("the Still writing here fact is missing. Log:\n%s", buf.String())
	}
}

// TestLocalRetentionStatesTheSecondWriterBesideAnOutcomeThatTakesPrecedence pins that
// the fact is printed whatever outcome line the pass ends on: here an archive under
// another spelling of this host's name, with another identity, is not rotated, and that
// outcome takes the line. The fact and the summary field are still there.
func TestLocalRetentionStatesTheSecondWriterBesideAnOutcomeThatTakesPrecedence(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	paths := seedServerIDFixture(t, dir, []serverIDSeed{
		{name: "pve-backup-20250102-100000.tar.zst", when: sharedIdentityAt(2, 10), manifestHost: "pve", manifestID: ourServerID},
		{name: "pve2-backup-20250101-120000.tar.zst", when: sharedIdentityAt(1, 12), manifestHost: "pve2", manifestID: ourServerID},
		{name: "pve-backup-20250101-100000.tar.zst", when: sharedIdentityAt(1, 10), manifestHost: "pve", manifestID: ourServerID},
		{name: "pve.lan-backup-20250101-090000.tar.zst", when: sharedIdentityAt(1, 9), manifestHost: "pve.lan", manifestID: anotherServerID},
	})

	logger, buf := newRecordingRetentionLogger()
	l, err := NewLocalStorage(&config.Config{BackupPath: dir, ServerID: ourServerID}, logger, "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	_ = l.Store(context.Background(), paths[0], &types.BackupMetadata{BackupFile: paths[0], Timestamp: sharedIdentityAt(2, 10)})

	if _, err := l.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	summary := l.LastRetentionSummary()
	if summary.NotRotated != 1 || summary.SharedIdentityNames != "pve2" {
		t.Errorf("NotRotated = %d, SharedIdentityNames = %q; want 1 and \"pve2\"", summary.NotRotated, summary.SharedIdentityNames)
	}
	if !strings.Contains(buf.String(), "  Still writing here: pve2\n") {
		t.Errorf("the Still writing here fact is missing beside a not-rotated outcome. Log:\n%s", buf.String())
	}
}

// TestSecondaryRetentionReportsASecondWriter is the clone on the real secondary backend,
// the documented shared NAS layout, with this run's archive listed as a bundle.
func TestSecondaryRetentionReportsASecondWriter(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	dir := t.TempDir()
	seedSecondaryServerIDFixture(t, dir, []secondaryServerIDSeed{
		{name: "pve-backup-20250102-100000.tar.zst", when: sharedIdentityAt(2, 10), bundled: true, manifestHost: "pve", manifestID: ourServerID},
		{name: "pve2-backup-20250101-120000.tar.zst", when: sharedIdentityAt(1, 12), manifestHost: "pve2", manifestID: ourServerID},
		{name: "pve-backup-20250101-100000.tar.zst", when: sharedIdentityAt(1, 10), manifestHost: "pve", manifestID: ourServerID},
	})

	logger, buf := newRecordingRetentionLogger()
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: dir, ServerID: ourServerID, BundleAssociatedFiles: true}
	s, err := NewSecondaryStorage(cfg, logger, "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	// Store would copy the archive in from the Primary; the fixture already holds it,
	// so only the name Store records is set.
	s.thisRunArchive = "pve-backup-20250102-100000.tar.zst"

	if _, err := s.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if got := s.LastRetentionSummary().SharedIdentityNames; got != "pve2" {
		t.Errorf("SharedIdentityNames = %q, want \"pve2\". Log:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "  Still writing here: pve2\n") {
		t.Errorf("the Still writing here fact is missing. Log:\n%s", buf.String())
	}
}

// TestCloudRetentionReportsASecondWriter is the clone on the cloud backend, whose
// owners arrive from the remote manifests (bundling off, as in
// TestCloudRetentionAdoptsArchivesWrittenUnderALostFQDN).
func TestCloudRetentionReportsASecondWriter(t *testing.T) {
	original := retentionHostname
	retentionHostname = func() (string, error) { return "pve", nil }
	defer func() { retentionHostname = original }()

	const thisRun = "pve-backup-20250102-100000.tar.zst"
	const clone = "pve2-backup-20250101-120000.tar.zst"
	const previous = "pve-backup-20250101-100000.tar.zst"
	listing := "" +
		"      100 2025-01-02 10:00:00.000000000 " + thisRun + "\n" +
		"       10 2025-01-02 10:00:00.000000000 " + thisRun + ".sha256\n" +
		"      100 2025-01-01 12:00:00.000000000 " + clone + "\n" +
		"       10 2025-01-01 12:00:00.000000000 " + clone + ".sha256\n" +
		"      100 2025-01-01 10:00:00.000000000 " + previous + "\n" +
		"       10 2025-01-01 10:00:00.000000000 " + previous + ".sha256\n"

	logger, buf := newRecordingRetentionLogger()
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "gdrive", ServerID: ourServerID}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	var mu sync.Mutex
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range args {
			if a == "lsl" {
				return []byte(listing), nil
			}
		}
		remotePath := args[len(args)-1]
		if args[0] == "cat" {
			if !strings.HasSuffix(remotePath, ".metadata") {
				return nil, fmt.Errorf("object not found: %s", remotePath)
			}
			host := "pve"
			if strings.Contains(remotePath, "pve2-") {
				host = "pve2"
			}
			return []byte(fmt.Sprintf(`{"hostname":%q,"server_id":%q}`, host, ourServerID)), nil
		}
		return nil, nil
	}
	cs.thisRunArchive = thisRun

	if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 10}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if got := cs.LastRetentionSummary().SharedIdentityNames; got != "pve2" {
		t.Errorf("SharedIdentityNames = %q, want \"pve2\". Log:\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "  Still writing here: pve2\n") {
		t.Errorf("the Still writing here fact is missing. Log:\n%s", buf.String())
	}
}
