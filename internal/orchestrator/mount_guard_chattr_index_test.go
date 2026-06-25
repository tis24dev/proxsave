package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// withTempGuardBaseDir redirects mountGuardBaseDir to an isolated temp dir for the
// duration of a test and restores it afterward. Mirrors the cleanup*/mountGuard*
// save/restore pattern used throughout this package and keeps the chattr index off
// the real /var/lib/proxsave path.
func withTempGuardBaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := mountGuardBaseDir
	mountGuardBaseDir = dir
	t.Cleanup(func() { mountGuardBaseDir = orig })
	return dir
}

// readGuardIndexLines returns the recorded immutable-target lines (trimmed, no
// blank lines). A missing index yields nil.
func readGuardIndexLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(mountGuardChattrTargetsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read guard index: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// --- record (apply-time) ---------------------------------------------------

// Kills: removing the dedup loop (would yield 2 identical lines) or any mutation
// that drops/reorders the second distinct target.
func TestRecordImmutableGuardTarget_DedupAndOrder(t *testing.T) {
	withTempGuardBaseDir(t)
	logger := newTestLogger()

	recordImmutableGuardTarget(logger, "/mnt/pve/store-a")
	recordImmutableGuardTarget(logger, "/mnt/pve/store-a") // duplicate
	if got := readGuardIndexLines(t); len(got) != 1 || got[0] != "/mnt/pve/store-a" {
		t.Fatalf("after duplicate record, index=%#v want [/mnt/pve/store-a]", got)
	}

	recordImmutableGuardTarget(logger, "/media/usb-b")
	got := readGuardIndexLines(t)
	if len(got) != 2 || got[0] != "/mnt/pve/store-a" || got[1] != "/media/usb-b" {
		t.Fatalf("index=%#v want [/mnt/pve/store-a, /media/usb-b] in order", got)
	}

	// The index records mount-root paths, not secrets, but should not be world-readable.
	info, err := os.Stat(mountGuardChattrTargetsPath())
	if err != nil {
		t.Fatalf("stat index: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("index mode = %o, want 0600", perm)
	}
}

// Kills: dropping the newline/control-char guard (a newline target would inject an
// extra index line), the isValidGuardTarget guard ("/"/"" rejected), or the
// isConfirmableDatastoreMountRoot guard (a non-datastore path would be recorded).
func TestRecordImmutableGuardTarget_RejectsUnsafe(t *testing.T) {
	withTempGuardBaseDir(t)
	logger := newTestLogger()

	for _, bad := range []string{
		"", "   ", ".", "/",
		"/etc/cron.d",          // not a datastore root
		"/var/lib/x",           // not a datastore root
		"/mnt/ok\n/etc/passwd", // newline injection
		"/mnt/with\ttab",       // control char
	} {
		recordImmutableGuardTarget(logger, bad)
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("index should be empty after only-invalid records, got %#v", got)
	}

	// A legitimate one still records, proving the rejections above were selective.
	recordImmutableGuardTarget(logger, "/mnt/pve/legit")
	if got := readGuardIndexLines(t); len(got) != 1 || got[0] != "/mnt/pve/legit" {
		t.Fatalf("index=%#v want [/mnt/pve/legit]", got)
	}
}

// Kills: removing tmp.Chmod(perm) in writeImmutableGuardIndex. os.CreateTemp
// defaults to 0600, so a non-default perm is required to pin the Chmod call.
func TestWriteImmutableGuardIndex_AppliesPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idx")
	if err := writeImmutableGuardIndex(path, []byte("/mnt/x\n"), 0o640); err != nil {
		t.Fatalf("writeImmutableGuardIndex: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("index perm = %o, want 0640 (Chmod not applied?)", perm)
	}
}

// Kills: parser regressions on blank/duplicate/CRLF/non-datastore-not-filtered
// (note: parse only validates isValidGuardTarget; datastore filtering is the clear
// loop's job, so /etc survives parse here and is dropped at clear time — see the
// dedicated cleanup test).
func TestParseImmutableGuardTargets_MalformedTolerant(t *testing.T) {
	raw := strings.Join([]string{
		"", "  /mnt/a  ", "/mnt/a", "/mnt/b\r", "   ", "/", ".", "/media/c", "",
	}, "\n")
	got := parseImmutableGuardTargets([]byte(raw))
	want := []string{"/mnt/a", "/mnt/b", "/media/c"} // first-seen order, deduped, root/empty dropped
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parse=%v want %v", got, want)
	}
}

// Kills: removing the maxChattrIndexBytes size cap.
func TestParseImmutableGuardTargets_OversizedTreatedEmpty(t *testing.T) {
	big := make([]byte, maxChattrIndexBytes+1)
	if got := parseImmutableGuardTargets(big); got != nil {
		t.Fatalf("oversized index should parse as empty, got %d entries", len(got))
	}
}

// Kills: re-introducing the chattr +i fallback at the PBS bind-failure site. The
// fallback is now warn-only: it must run no command, record nothing in the index,
// and not mark the target protected.
func TestPBSBindFailWarnsNoChattr(t *testing.T) {
	withTempGuardBaseDir(t)

	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	fake := &FakeCommandRunner{}
	restoreCmd = fake

	a := &pbsMountGuardApply{
		ctx:       context.Background(),
		logger:    newTestLogger(),
		protected: map[string]struct{}{},
	}

	a.warnOfflineTargetUnguarded("/mnt/pbs-ok", errors.New("bind failed"))

	if calls := fake.CallsList(); len(calls) != 0 {
		t.Fatalf("warn-only fallback must run no commands (no chattr); got %v", calls)
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("warn-only fallback must not record an immutable flag; got %#v", got)
	}
	if _, ok := a.protected["/mnt/pbs-ok"]; ok {
		t.Fatalf("warn-only fallback must not mark the target protected")
	}
}

// Kills: re-introducing the chattr +i fallback at the PVE bind-failure site. The
// fallback is now warn-only. Drives the PVE guard path entirely through seams so it
// ALWAYS runs (the pre-existing _EarlyAndFallback test is skip-gated on a writable
// /mnt/pve + root).
func TestPVEBindFailWarnsNoChattr(t *testing.T) {
	ctx := context.Background()
	logger := newTestLogger()
	withTempGuardBaseDir(t)

	origFS := restoreFS
	origCmd := restoreCmd
	origGeteuid := mountGuardGeteuid
	origMkdir := mountGuardMkdirAll
	origRootFS := mountGuardIsPathOnRootFilesystem
	origMount := mountGuardSysMount
	origUnmount := mountGuardSysUnmount
	origRead := mountGuardReadFile
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
		mountGuardGeteuid = origGeteuid
		mountGuardMkdirAll = origMkdir
		mountGuardIsPathOnRootFilesystem = origRootFS
		mountGuardSysMount = origMount
		mountGuardSysUnmount = origUnmount
		mountGuardReadFile = origRead
	})

	restoreFS = osFS{}
	mountGuardGeteuid = func() int { return 0 }
	mountGuardMkdirAll = func(string, os.FileMode) error { return nil }
	mountGuardIsPathOnRootFilesystem = func(p string) (bool, string, error) { return true, p, nil }               // force offline guard
	mountGuardSysMount = func(string, string, string, uintptr, string) error { return errors.New("bind denied") } // force chattr fallback
	mountGuardSysUnmount = func(string, int) error { return nil }
	mountGuardReadFile = func(string) ([]byte, error) { return []byte(""), nil } // /proc empty -> not mounted

	stageRoot := t.TempDir()
	stageCfgPath := filepath.Join(stageRoot, "etc/pve/storage.cfg")
	if err := os.MkdirAll(filepath.Dir(stageCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir stage cfg dir: %v", err)
	}
	id := uniquePveMountTestStorageID(t, "chattr-record")
	if err := os.WriteFile(stageCfgPath, []byte("nfs: "+id+"\n"), 0o644); err != nil {
		t.Fatalf("write staged storage.cfg: %v", err)
	}
	target := pveMountTargetForStorageID(id)

	fakeCmd := &FakeCommandRunner{
		Errors: map[string]error{
			"which pvesm":     errors.New("missing"),
			"mount " + target: errors.New("offline"),
		},
	}
	restoreCmd = fakeCmd

	if err := maybeApplyPVEStorageMountGuardsFromStage(ctx, logger, pvePlan(false, "storage_pve"), stageRoot, "/"); err != nil {
		t.Fatalf("guard fallback should be non-fatal, got %v", err)
	}
	// The offline mount attempt still runs, but the bind failure is now warn-only:
	// no chattr +i, nothing recorded in the index.
	if !strings.Contains(strings.Join(fakeCmd.CallsList(), "\n"), "mount "+target) {
		t.Fatalf("expected the offline mount attempt on %q; calls=%v", target, fakeCmd.CallsList())
	}
	if strings.Contains(strings.Join(fakeCmd.CallsList(), "\n"), "chattr +i") {
		t.Fatalf("bind failure must be warn-only (no chattr +i); calls=%v", fakeCmd.CallsList())
	}
	if got := readGuardIndexLines(t); len(got) != 0 {
		t.Fatalf("warn-only fallback must not record anything; got %#v", got)
	}
}

// --- cleanup (clear-time) --------------------------------------------------

// installChattrCleanupSeams wires the cleanup seams for the chattr-clear path.
// index: bytes returned by the index read seam (nil => missing/ErrNotExist).
// mountinfo: what isMounted sees via mountGuardReadFile("/proc/self/mountinfo").
// chattrErr: per-command (commandKey) errors for the chattr runner.
// Returns a pointer to the list of commands the runner received.
func installChattrCleanupSeams(t *testing.T, index []byte, mountinfo string, chattrErr map[string]error) *[]string {
	t.Helper()
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origUnmount := cleanupSysUnmount
	origChattrRead := cleanupChattrReadFile
	origResolve := resolveGuardTarget
	origRunCmd := cleanupRunCmd
	origMGRead := mountGuardReadFile
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		cleanupSysUnmount = origUnmount
		cleanupChattrReadFile = origChattrRead
		resolveGuardTarget = origResolve
		cleanupRunCmd = origRunCmd
		mountGuardReadFile = origMGRead
	})

	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }
	cleanupReadFile = func(string) ([]byte, error) { return []byte(""), nil } // empty /proc -> no bind guards
	cleanupRemoveAll = func(string) error { return nil }
	cleanupSysUnmount = func(string, int) error { return nil }
	resolveGuardTarget = func(p string) (string, error) { return p, nil } // identity: no symlink resolution in unit tests
	cleanupChattrReadFile = func(path string) ([]byte, error) {
		if path != mountGuardChattrTargetsPath() {
			t.Fatalf("unexpected index read path %q", path)
		}
		if index == nil {
			return nil, os.ErrNotExist
		}
		return index, nil
	}
	mountGuardReadFile = func(path string) ([]byte, error) {
		if path == "/proc/self/mountinfo" {
			return []byte(mountinfo), nil
		}
		return []byte(""), nil
	}

	var ran []string
	cleanupRunCmd = func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := commandKey(name, args)
		ran = append(ran, key)
		if chattrErr != nil {
			if err, ok := chattrErr[key]; ok {
				return nil, err
			}
		}
		return nil, nil
	}
	return &ran
}

// Kills: removing the clearImmutableGuards call, or any mutation that skips the
// chattr -i for a valid not-mounted datastore-root target.
func TestCleanupMountGuards_ClearsImmutableWhenNotMounted(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n/media/usb\n"), "", nil)

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	want := map[string]bool{"chattr -i /mnt/pve/offline": true, "chattr -i /media/usb": true}
	if len(*ran) != 2 || !want[(*ran)[0]] || !want[(*ran)[1]] {
		t.Fatalf("runner calls=%#v want both chattr -i targets", *ran)
	}
}

// Kills: removing the isMounted skip (a re-mounted target would be wrongly cleared,
// touching the live mount instead of the shadowed dir).
func TestCleanupMountGuards_SkipsImmutableWhenMounted(t *testing.T) {
	withTempGuardBaseDir(t)
	mountinfo := "36 35 0:30 / /mnt/pve/offline rw - nfs server:/export rw\n"
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n"), mountinfo, nil)

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("chattr -i must not run for a mounted (shadowed) target, calls=%#v", *ran)
	}
}

// Kills: a mutation that ignores dryRun and clears anyway.
func TestCleanupMountGuards_DryRunNoChattr(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n"), "", nil)

	if err := CleanupMountGuards(context.Background(), newTestLogger(), true); err != nil {
		t.Fatalf("CleanupMountGuards dry-run: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("dry-run must not run chattr -i, calls=%#v", *ran)
	}
}

// Kills: removing the isConfirmableDatastoreMountRoot guard in the clear loop (an
// arbitrary path like /etc would otherwise get chattr -i).
func TestCleanupMountGuards_RejectsNonDatastoreIndexEntry(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/etc/passwd-dir\n/var/lib/x\n/mnt/pve/legit\n"), "", nil)

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 1 || (*ran)[0] != "chattr -i /mnt/pve/legit" {
		t.Fatalf("only the datastore-root entry may be cleared, calls=%#v", *ran)
	}
}

// Kills: making a chattr -i failure abort cleanup (return non-nil) or break out of
// the loop (the second target would never be processed).
func TestCleanupMountGuards_ChattrFailureNonFatalContinues(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/first\n/mnt/pve/second\n"), "",
		map[string]error{"chattr -i /mnt/pve/first": errors.New("operation not permitted")})

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("per-target chattr failure must be non-fatal, got %v", err)
	}
	if len(*ran) != 2 || (*ran)[0] != "chattr -i /mnt/pve/first" || (*ran)[1] != "chattr -i /mnt/pve/second" {
		t.Fatalf("both targets must be attempted in order despite failure, calls=%#v", *ran)
	}
}

// Kills: a mutation that calls chattr (or errors) when the index is absent.
func TestCleanupMountGuards_MissingIndexNoOp(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, nil, "", nil)

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("missing index must be a no-op, calls=%#v", *ran)
	}
}

// Kills: a mutation that clears (rather than skips) when isMounted cannot be
// determined.
func TestCleanupMountGuards_IsMountedErrorSkips(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n"), "", nil)
	// Force isMounted to error: both /proc reads fail.
	mountGuardReadFile = func(string) ([]byte, error) { return nil, errors.New("procfs unavailable") }

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("chattr -i must not run when mount status is inconclusive, calls=%#v", *ran)
	}
}

// Kills: removing the symlink-resolution + post-resolve allowlist re-check (a
// target whose parent symlink resolves outside /mnt|/media|/run/media must NOT be
// chattr'd).
func TestCleanupMountGuards_SymlinkEscapeRefused(t *testing.T) {
	withTempGuardBaseDir(t)
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/evil\n"), "", nil)
	// /mnt/pve/evil passes the string allowlist but resolves outside it.
	resolveGuardTarget = func(string) (string, error) { return "/etc/evil", nil }

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("a symlink-escaping target must not be cleared, calls=%#v", *ran)
	}
}

// Kills: removing the pending-gate (a chattr target skipped because it is mounted
// must NOT cause the guard dir/index to be removed, or its record is lost forever).
func TestCleanupMountGuards_PendingKeepsIndexDir(t *testing.T) {
	withTempGuardBaseDir(t)
	mountinfo := "36 35 0:30 / /mnt/pve/offline rw - nfs server:/export rw\n"
	ran := installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n"), mountinfo, nil)

	removed := false
	cleanupRemoveAll = func(string) error { removed = true; return nil }

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(*ran) != 0 {
		t.Fatalf("mounted target must be skipped, calls=%#v", *ran)
	}
	if removed {
		t.Fatalf("guard dir/index must be kept while an immutable target is still pending")
	}
}

// End-to-end: a real recorded index is read back and cleared by CleanupMountGuards
// (proves record and clear agree on the on-disk format and path).
func TestCleanupMountGuards_RoundTripFromRecord(t *testing.T) {
	withTempGuardBaseDir(t)
	recordImmutableGuardTarget(newTestLogger(), "/mnt/pve/roundtrip")

	// Stub the dangerous/root-only seams but leave cleanupChattrReadFile at its
	// default (os.ReadFile) so it reads the file record actually wrote.
	origGeteuid := cleanupGeteuid
	origStat := cleanupStat
	origReadFile := cleanupReadFile
	origRemoveAll := cleanupRemoveAll
	origResolve := resolveGuardTarget
	origRunCmd := cleanupRunCmd
	origMGRead := mountGuardReadFile
	t.Cleanup(func() {
		cleanupGeteuid = origGeteuid
		cleanupStat = origStat
		cleanupReadFile = origReadFile
		cleanupRemoveAll = origRemoveAll
		resolveGuardTarget = origResolve
		cleanupRunCmd = origRunCmd
		mountGuardReadFile = origMGRead
	})
	cleanupGeteuid = func() int { return 0 }
	cleanupStat = func(string) (os.FileInfo, error) { return nil, nil }
	cleanupReadFile = func(string) ([]byte, error) { return []byte(""), nil }
	cleanupRemoveAll = func(string) error { return nil }
	resolveGuardTarget = func(p string) (string, error) { return p, nil }        // identity (path is fake)
	mountGuardReadFile = func(string) ([]byte, error) { return []byte(""), nil } // not mounted
	var ran []string
	cleanupRunCmd = func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, commandKey(name, args))
		return nil, nil
	}

	if err := CleanupMountGuards(context.Background(), newTestLogger(), false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	if len(ran) != 1 || ran[0] != "chattr -i /mnt/pve/roundtrip" {
		t.Fatalf("round-trip: runner calls=%#v want [chattr -i /mnt/pve/roundtrip]", ran)
	}
}

// Kills: a reader that drops/duplicates recorded targets, or one that does not
// return the recorded entries to the restore-start legacy warning.
func TestRecordedImmutableGuardTargets(t *testing.T) {
	withTempGuardBaseDir(t)
	if got := recordedImmutableGuardTargets(); len(got) != 0 {
		t.Fatalf("no index -> empty; got %#v", got)
	}
	recordImmutableGuardTarget(newTestLogger(), "/mnt/ds1")
	recordImmutableGuardTarget(newTestLogger(), "/media/ds2")
	got := recordedImmutableGuardTargets()
	if len(got) != 2 || got[0] != "/mnt/ds1" || got[1] != "/media/ds2" {
		t.Fatalf("recorded targets = %#v", got)
	}
}

// Kills: dropping the restore-start legacy warning (it must fire when persistent
// chattr +i flags are recorded and stay silent otherwise).
func TestWarnLegacyImmutableGuards(t *testing.T) {
	withTempGuardBaseDir(t)
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	warnLegacyImmutableGuards(logger)
	if buf.Len() != 0 {
		t.Fatalf("empty index must be silent; got %q", buf.String())
	}

	recordImmutableGuardTarget(newTestLogger(), "/mnt/ds1")
	warnLegacyImmutableGuards(logger)
	out := buf.String()
	if !strings.Contains(out, "persistent immutable guard flag") || !strings.Contains(out, "/mnt/ds1") {
		t.Fatalf("expected legacy warning naming the target; got %q", out)
	}
}

// Kills: dropping the end-of-cleanup summary, or mis-reporting cleared/removed.
func TestCleanupMountGuards_SummaryReported(t *testing.T) {
	withTempGuardBaseDir(t)
	installChattrCleanupSeams(t, []byte("/mnt/pve/a\n/media/b\n"), "", nil)

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Guard cleanup summary:") {
		t.Fatalf("missing summary line; out=%q", out)
	}
	if !strings.Contains(out, "immutable-cleared=2") || !strings.Contains(out, "immutable-pending=0") {
		t.Fatalf("summary counts wrong; out=%q", out)
	}
	if !strings.Contains(out, "guard-dir=removed") {
		t.Fatalf("expected guard-dir=removed; out=%q", out)
	}
}

// Kills: dropping the pending accounting/warning when an immutable target is left
// (e.g. shadowed by a real mount), or removing the guard dir while pending > 0.
func TestCleanupMountGuards_SummaryPending(t *testing.T) {
	withTempGuardBaseDir(t)
	mountinfo := "36 35 0:30 / /mnt/pve/offline rw - nfs server:/export rw\n"
	installChattrCleanupSeams(t, []byte("/mnt/pve/offline\n"), mountinfo, nil)

	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	if err := CleanupMountGuards(context.Background(), logger, false); err != nil {
		t.Fatalf("CleanupMountGuards: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "immutable-cleared=0") || !strings.Contains(out, "immutable-pending=1") {
		t.Fatalf("summary counts wrong; out=%q", out)
	}
	if !strings.Contains(out, "still pending") {
		t.Fatalf("expected pending warning; out=%q", out)
	}
	if !strings.Contains(out, "guard-dir=kept") {
		t.Fatalf("expected guard-dir=kept while pending; out=%q", out)
	}
}
