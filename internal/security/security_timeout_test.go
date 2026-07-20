package security

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/storage"
)

// expiredContext returns a context whose deadline is already in the past, so any
// safefs operation returns ErrTimeout at entry without a real blocking mount.
func expiredContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancel)
	return ctx
}

// TestVerifyDirectoriesSkipsOnTimeout simulates a dead/stale mount: every stat
// times out, so verifyDirectories must warn and skip each path without erroring
// and without creating anything.
func TestVerifyDirectoriesSkipsOnTimeout(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{
		BaseDir:    baseDir,
		BackupPath: filepath.Join(baseDir, "backup"),
		LogPath:    filepath.Join(baseDir, "log"),
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.verifyDirectories(expiredContext(t))

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("expected no errors on timeout, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a timeout warning, got %+v", checker.result.Issues)
	}
	if _, err := os.Stat(cfg.BackupPath); !os.IsNotExist(err) {
		t.Fatalf("backup dir must not be created on timeout; stat err = %v", err)
	}
}

// TestVerifyDirectoriesDryRunSkipsCreate verifies a dry-run never materializes a
// missing directory.
func TestVerifyDirectoriesDryRunSkipsCreate(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{
		BaseDir:    baseDir,
		BackupPath: filepath.Join(baseDir, "backup"),
		LogPath:    filepath.Join(baseDir, "log"),
		DryRun:     true,
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.verifyDirectories(context.Background())

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("dry-run should not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	for _, p := range []string{cfg.BackupPath, cfg.LogPath, filepath.Join(baseDir, "identity")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("dry-run must not create %s; stat err = %v", p, err)
		}
	}
}

// TestShouldSkipPOSIXDirectoryChecksOnDetectionTimeout verifies a filesystem
// detection timeout is treated as "skip the path" (warning), not "proceed".
func TestShouldSkipPOSIXDirectoryChecksOnDetectionTimeout(t *testing.T) {
	cfg := &config.Config{BaseDir: t.TempDir()}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second
	checker.filesystemInfoLookup = func(context.Context, string) (*storage.FilesystemInfo, error) {
		return nil, &safefs.TimeoutError{Op: "statfs", Path: "/mnt/dead", Timeout: 30 * time.Second}
	}

	if !checker.shouldSkipPOSIXDirectoryChecks(context.Background(), "/mnt/dead") {
		t.Fatal("expected shouldSkipPOSIXDirectoryChecks to return true on detection timeout")
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a timeout warning, got %+v", checker.result.Issues)
	}
}

// TestEnsureOwnershipAndPermDryRunIsReadOnly verifies that, in dry-run, the
// preflight does not chmod an existing file with wrong permissions (it only
// reports what it would do).
func TestEnsureOwnershipAndPermDryRunIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg := &config.Config{
		BaseDir:            dir,
		AutoFixPermissions: true,
		DryRun:             true,
	}
	checker := newChecker(t, cfg)
	checker.fsTimeout = 30 * time.Second

	checker.ensureOwnershipAndPerm(context.Background(), file, nil, 0o600, "test file")

	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("dry-run must not chmod existing file; perm = %o, want 644", info.Mode().Perm())
	}
}

// TestVerifyBinaryIntegrityDryRunDoesNotRegenerateHash verifies that a dry-run
// does not rewrite the .md5 hash file on mismatch (the "Regenerated hash file"
// write from issue #242).
func TestVerifyBinaryIntegrityDryRunDoesNotRegenerateHash(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("real content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	hashPath := execPath + ".md5"
	if err := os.WriteFile(hashPath, []byte("stale-hash"), 0o600); err != nil {
		t.Fatalf("write stale hash: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity(context.Background())

	data, err := os.ReadFile(hashPath)
	if err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if string(data) != "stale-hash" {
		t.Fatalf("dry-run must not regenerate hash file; got %q, want %q", string(data), "stale-hash")
	}
}

// TestVerifyBinaryIntegrityDryRunDoesNotCreateHash verifies that a dry-run does
// not create a missing .md5 hash file.
func TestVerifyBinaryIntegrityDryRunDoesNotCreateHash(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity(context.Background())

	if _, err := os.Stat(execPath + ".md5"); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not create hash file; stat err = %v", err)
	}
}

// TestVerifyBinaryIntegrityFromFDDryRunDoesNotChmod verifies that a dry-run does not
// fchmod the executable (ensureExecutableOwnerWriteOnly) even when it is genuinely
// group/other-writable — the one permission state the guard would otherwise correct.
func TestVerifyBinaryIntegrityFromFDDryRunDoesNotChmod(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	if err := os.Chmod(execPath, 0o777); err != nil { // group/other-writable: the guard would fix it
		t.Fatalf("chmod: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoFixPermissions: true, AutoUpdateHashes: false, DryRun: true}, execPath)
	checker.verifyBinaryIntegrity(context.Background())

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("dry-run must not fchmod the executable; perm = %o, want 777", info.Mode().Perm())
	}
}

// TestVerifyBinaryIntegrityFixesGroupOtherWritable verifies that with AUTO_FIX on the
// guard clears only the group/other write bits of a writable executable (0o777 ->
// 0o755, i.e. perm &^ 0o022) rather than forcing an exact mode.
func TestVerifyBinaryIntegrityFixesGroupOtherWritable(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o755); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	if err := os.Chmod(execPath, 0o777); err != nil { // group/other-writable
		t.Fatalf("chmod: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoFixPermissions: true, AutoUpdateHashes: false}, execPath)
	checker.verifyBinaryIntegrity(context.Background())

	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("guard must clear only the group/other write bits; perm = %o, want 755", info.Mode().Perm())
	}
}

// TestVerifyBinaryIntegrityWarnsGroupOtherWritable verifies that with AUTO_FIX off a
// group/other-writable executable is warned about and left untouched — and that a
// conventional 0o755 binary raises no such warning.
func TestVerifyBinaryIntegrityWarnsGroupOtherWritable(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o755); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	if err := os.Chmod(execPath, 0o777); err != nil { // group/other-writable
		t.Fatalf("chmod: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoFixPermissions: false, AutoUpdateHashes: false}, execPath)
	checker.verifyBinaryIntegrity(context.Background())

	if !containsIssue(checker.result, "must not be writable by group or other") {
		t.Fatalf("expected group/other-writable warning, got %+v", checker.result.Issues)
	}
	info, err := os.Stat(execPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("warn-only must not chmod the executable; perm = %o, want 777", info.Mode().Perm())
	}
}

// TestVerifyBinaryIntegrityExecTimeoutErrors: a wedged mount holding the
// executable must FAIL the integrity check (fail-closed), not warn-and-skip.
// expiredContext times out the exec Lstat, which now routes through addError so
// ErrorCount>0 -- the same classification as a non-timeout stat error (F03-01).
func TestVerifyBinaryIntegrityExecTimeoutErrors(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true}, execPath)
	checker.fsTimeout = 30 * time.Second

	checker.verifyBinaryIntegrity(expiredContext(t))

	if checker.result.ErrorCount() == 0 {
		t.Fatalf("exec-integrity timeout must fail closed (error), got %d errors: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected the timeout message, got %+v", checker.result.Issues)
	}
	if _, err := os.Stat(execPath + ".md5"); !os.IsNotExist(err) {
		t.Fatalf("must not create hash file on timeout; stat err = %v", err)
	}
}

// TestDetectPrivateAgeKeysSkipsOnTimeout simulates a dead/stale mount under the
// identity directory: the bounded stat times out, so the private-key scan warns and
// skips without erroring and without scanning/flagging the planted key.
func TestDetectPrivateAgeKeysSkipsOnTimeout(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, "identity")
	if err := os.MkdirAll(identityDir, 0o700); err != nil {
		t.Fatalf("mkdir identity: %v", err)
	}
	if err := os.WriteFile(filepath.Join(identityDir, "key"), []byte("AGE-SECRET-KEY-1EXAMPLE"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	checker := newChecker(t, &config.Config{BaseDir: baseDir})
	checker.fsTimeout = 30 * time.Second

	checker.detectPrivateAgeKeys(expiredContext(t))

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("timeout must not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a timeout warning, got %+v", checker.result.Issues)
	}
	if containsIssue(checker.result, "Possible private AGE/SSH key") {
		t.Fatalf("must not scan/flag keys on timeout; got %+v", checker.result.Issues)
	}
}

// runVerifyBinaryIntegrityGuarded runs verifyBinaryIntegrity in a goroutine and
// fails the test if it does not return within 5s. A reverted bound on any .md5
// op manifests as a hang on a wedged FIFO / blocked stat seam, so this watchdog
// is the mutation property: drop a bound -> the op blocks forever -> Fatal.
func runVerifyBinaryIntegrityGuarded(t *testing.T, checker *Checker, ctx context.Context) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		checker.verifyBinaryIntegrity(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("verifyBinaryIntegrity hung: a .md5 op is not timeout-bounded")
	}
}

// TestVerifyBinaryIntegrityHashStatTimeoutSkips proves the existence-stat bound
// on the .md5 path AND the explicit safefs.ErrTimeout branch (the control-flow
// trap): a wedged stat must warn+skip, never fall through to the read.
// Mutation: revert safefs.Stat -> os.Stat (the seam swaps safefs's osStat, not
// the package's os.Stat, so a raw os.Stat returns ErrNotExist instantly and the
// "timed out" warning never appears); OR delete the ErrTimeout branch (falls
// through to the bounded read and emits a different message) -> this test fails.
func TestVerifyBinaryIntegrityHashStatTimeoutSkips(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("real content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	// A real, readable .md5 with stale content: if the stat were NOT bounded the
	// code would fall through, read this file, and warn "Executable hash mismatch"
	// (never "timed out").
	if err := os.WriteFile(execPath+".md5", []byte("stale-hash"), 0o600); err != nil {
		t.Fatalf("write hash: %v", err)
	}

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: false}, execPath)
	checker.fsTimeout = 300 * time.Millisecond

	// Install the blocking osStat seam AFTER setup (setup used raw os.WriteFile,
	// not safefs.Stat). The lone safefs.Stat call in verifyBinaryIntegrity is the
	// hashFile existence check; the executable path uses osLstat/osOpen and the
	// FD-based ownership/checksum helpers, none of which route through osStat.
	park := make(chan struct{})
	t.Cleanup(func() { close(park) }) // release the abandoned osStat worker
	restore := safefs.SetOsStatForTest(func(string) (os.FileInfo, error) {
		<-park
		return nil, errors.New("released")
	})
	t.Cleanup(restore)

	runVerifyBinaryIntegrityGuarded(t, checker, context.Background())

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("stat timeout must not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "stat of hash file") || !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a stat-of-hash-file timeout warning, got %+v", checker.result.Issues)
	}
	if containsIssue(checker.result, "Unable to read hash file") || containsIssue(checker.result, "Executable hash mismatch") {
		t.Fatalf("stat timeout must not fall through to read/compare, got %+v", checker.result.Issues)
	}
}

// TestVerifyBinaryIntegrityHashReadTimeoutSkips proves the ReadFile bound. The
// .md5 is a FIFO with no writer: os.Stat(FIFO) succeeds (exists) so the code
// falls through to the bounded read, which blocks on the writer-less FIFO until
// the fs timeout fires. Mutation: revert safefs.Run -> raw os.ReadFile -> the
// read blocks forever -> the watchdog Fatals.
func TestVerifyBinaryIntegrityHashReadTimeoutSkips(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	hashPath := execPath + ".md5"
	if err := syscall.Mkfifo(hashPath, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		// Unblock the abandoned FIFO-read worker by opening the write end. O_NONBLOCK
		// so a regression where the read was never attempted (no reader) returns ENXIO
		// immediately (e != nil -> skip) instead of wedging the cleanup forever.
		if w, e := os.OpenFile(hashPath, os.O_WRONLY|syscall.O_NONBLOCK, 0); e == nil {
			_ = w.Close()
		}
	})

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: false}, execPath)
	checker.fsTimeout = 300 * time.Millisecond

	runVerifyBinaryIntegrityGuarded(t, checker, context.Background())

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("read timeout must not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "reading hash file") || !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected a reading-hash-file timeout warning, got %+v", checker.result.Issues)
	}
}

// TestVerifyBinaryIntegrityHashWriteTimeoutWarns proves the WriteFile bound via
// the regenerate path (which shares c.writeHashFile with the create path, so a
// single revert point covers both writes). A feed goroutine writes a wrong hash
// to the FIFO then closes it, so the bounded read returns a mismatch; the
// subsequent regenerate write opens the now reader/writer-less FIFO O_WRONLY and
// blocks until the fs timeout fires. Mutation: revert writeHashFile's safefs.Run
// -> raw os.WriteFile -> the write blocks forever -> the watchdog Fatals.
func TestVerifyBinaryIntegrityHashWriteTimeoutWarns(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "binary")
	if err := os.WriteFile(execPath, []byte("content"), 0o700); err != nil {
		t.Fatalf("write exec: %v", err)
	}
	hashPath := execPath + ".md5"
	if err := syscall.Mkfifo(hashPath, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}

	// Feed a wrong hash then EOF so the bounded read yields a mismatch and the
	// code proceeds to the regenerate write.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		w, e := os.OpenFile(hashPath, os.O_WRONLY, 0)
		if e != nil {
			return
		}
		_, _ = w.Write([]byte("stale-hash\n"))
		_ = w.Close()
	}()
	t.Cleanup(func() {
		// Drain the read end FIRST (O_NONBLOCK open never wedges): providing a reader
		// rendezvouses and releases both the feed goroutine's O_WRONLY open and the
		// abandoned regenerate-write worker's O_WRONLY open, even on a regression where
		// verifyBinaryIntegrity never touched the FIFO. io.Copy terminates because both
		// writers close once unblocked.
		if r, e := os.OpenFile(hashPath, os.O_RDONLY|syscall.O_NONBLOCK, 0); e == nil {
			_, _ = io.Copy(io.Discard, r)
			_ = r.Close()
		}
		// Then bound the join so a stuck feed goroutine cannot wedge the suite.
		select {
		case <-writerDone:
		case <-time.After(time.Second):
			t.Error("timed out waiting for the FIFO writer goroutine to finish")
		}
	})

	checker := newCheckerWithExec(t, &config.Config{AutoUpdateHashes: true, DryRun: false}, execPath)
	checker.fsTimeout = 300 * time.Millisecond

	runVerifyBinaryIntegrityGuarded(t, checker, context.Background())

	if checker.result.ErrorCount() != 0 {
		t.Fatalf("write timeout must not error, got %d: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "Failed to update hash file") {
		t.Fatalf("expected a failed-to-update-hash-file warning, got %+v", checker.result.Issues)
	}
}

// TestVerifyConfigFileStatTimeoutErrors: a wedged mount holding the config file
// must FAIL the config check (fail-closed), matching the non-timeout stat error
// path at security.go:513 (F03-01).
func TestVerifyConfigFileStatTimeoutErrors(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(configPath, []byte("x=1\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	checker := newChecker(t, &config.Config{})
	checker.configPath = configPath
	checker.fsTimeout = 30 * time.Second

	checker.verifyConfigFile(expiredContext(t))

	if checker.result.ErrorCount() == 0 {
		t.Fatalf("config stat timeout must fail closed (error), got %d errors: %+v", checker.result.ErrorCount(), checker.result.Issues)
	}
	if !containsIssue(checker.result, "timed out") {
		t.Fatalf("expected the timeout message, got %+v", checker.result.Issues)
	}
}
