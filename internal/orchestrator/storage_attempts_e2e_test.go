package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// fakeRcloneOnPath puts an executable "rclone" running script first on PATH, so the
// real CloudStorage runs it through safeexec. The retry backoff still runs between the
// attempts, with each wait recorded and returned at once (storage.SetCloudRetryWaitForTest)
// instead of sleeping 2s, 4s, ...
func fakeRcloneOnPath(t *testing.T, script string) *[]time.Duration {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var mu sync.Mutex
	waits := &[]time.Duration{}
	restore := storage.SetCloudRetryWaitForTest(func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		*waits = append(*waits, d)
		mu.Unlock()
		return ctx.Err()
	})
	t.Cleanup(restore)
	return waits
}

// visibleLines are the lines an operator sees (no DEBUG), without timestamps.
func visibleLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if _, rest, ok := strings.Cut(line, "] "); ok {
			line = rest
		}
		if line == "" || strings.HasPrefix(line, "DEBUG") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// syncRealBackend runs one adapter Sync over a real backend and returns the visible
// lines from the block's STEP up to and including its store outcome (the first
// WARNING, or the first line starting with "✓" after "Storing backup...").
func syncRealBackend(t *testing.T, cfg *config.Config, build func(*logging.Logger) storage.Storage, fsInfo *storage.FilesystemInfo, archive string) []string {
	t.Helper()
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	adapter := NewStorageAdapter(build(logger), logger, cfg)
	adapter.SetFilesystemInfo(fsInfo)
	stats := sampleAdapterStats()
	stats.ArchivePath = archive
	if err := adapter.Sync(context.Background(), stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	lines := visibleLines(buf.String())
	for i, line := range lines {
		if strings.HasPrefix(line, "WARNING") || strings.Contains(line, "✓") {
			return lines[:i+1]
		}
	}
	t.Fatalf("no store outcome in:\n%s", buf.String())
	return nil
}

func writeArchive(t *testing.T, sidecars ...string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "host-backup-20261003-100000.tar.zst")
	for _, f := range append([]string{archive}, sidecars...) {
		if f != archive {
			f = archive + f
		}
		if err := os.WriteFile(f, []byte("archive"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	return archive
}

func requireExactLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

var cloudFS = &storage.FilesystemInfo{Type: "rclone-remote", IsNetworkFS: true, MountPoint: "remote:backup", Device: "cloud"}

func newRealCloud(t *testing.T, cfg *config.Config) func(*logging.Logger) storage.Storage {
	return func(logger *logging.Logger) storage.Storage {
		cs, err := storage.NewCloudStorage(cfg, logger, "")
		if err != nil {
			t.Fatalf("NewCloudStorage: %v", err)
		}
		return cs
	}
}

const failingCopytoScript = "#!/bin/sh\n" +
	"case \"$1\" in\n" +
	"copyto) echo '2026/10/03 10:00:00 ERROR : a: Failed to copy: read-only file system' >&2\n" +
	"  echo '2026/10/03 10:00:00 Failed to copyto: read-only file system' >&2; exit 1;;\n" +
	"esac\nexit 0\n"

// RCLONE_RETRIES > 1 and every attempt failed: the attempt lines, then the outcome,
// with no "  Upload failed" line repeating the cause.
func TestCloudBlockEveryAttemptFailed(t *testing.T) {
	waits := fakeRcloneOnPath(t, failingCopytoScript)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Attempt 1/2 failed: Failed to copyto: read-only file system",
		"INFO       Attempt 2/2 failed: Failed to copyto: read-only file system",
		"WARNING  ✗ Cloud Storage (rclone): backup not saved",
	)
	// The backoff ran between the two attempts, with the real schedule's first step.
	if len(*waits) != 1 || (*waits)[0] != 2*time.Second {
		t.Fatalf("backoff waits = %v, want [2s]", *waits)
	}
}

// The copy went through and its verification failed: "  Verification failed" with the
// error, not "  Upload failed"; the outcome is unchanged.
func TestCloudBlockVerificationFailed(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"lsl) case \"$2\" in *.tar.zst) echo '        5 2026-10-03 10:00:00.000000000 host-backup-20261003-100000.tar.zst';; esac;;\n"+
		"esac\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Verification failed: size mismatch: local=7 remote=5",
		"WARNING  ✗ Cloud Storage (rclone): backup not saved",
	)
}

// RCLONE_RETRIES = 1: one "  Upload failed" fact with the cause, no attempt line.
func TestCloudBlockSingleAttemptFailed(t *testing.T) {
	fakeRcloneOnPath(t, failingCopytoScript)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 1}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Upload failed: Failed to copyto: read-only file system",
		"WARNING  ✗ Cloud Storage (rclone): backup not saved",
	)
}

// A failed attempt followed by a successful one: the attempt line, then the
// unchanged success outcome.
func TestCloudBlockAttemptFailedThenSaved(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "failed-once")
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"copyto) if [ ! -e "+marker+" ]; then : > "+marker+"; echo '2026/10/03 10:00:00 Failed to copyto: connection reset by peer' >&2; exit 1; fi; exit 0;;\n"+
		"lsl) case \"$2\" in *.tar.zst) echo '        7 2026-10-03 10:00:00.000000000 host-backup-20261003-100000.tar.zst';; esac; exit 0;;\n"+
		"esac\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Attempt 1/2 failed: Failed to copyto: connection reset by peer",
		"INFO     ✓ Cloud Storage (rclone): backup saved",
	)
}

// What a failing SIDECAR upload prints with RCLONE_RETRIES=3, every visible line of
// the block up to its outcome. Round 3 leaves it as it is (the maintainer decides):
// the attempt lines do not name the sidecar, and the "  Sidecar failed" fact repeats
// the cause after them.
func TestCloudBlockSidecarFailedWithThreeAttempts(t *testing.T) {
	waits := fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"copyto) case \"$2\" in *.sha256) echo '2026/10/03 10:00:00 Failed to copyto: permission denied' >&2; exit 1;; esac; exit 0;;\n"+
		"lsl) case \"$2\" in *.tar.zst) echo '        7 2026-10-03 10:00:00.000000000 host-backup-20261003-100000.tar.zst';; esac; exit 0;;\n"+
		"esac\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t, ".sha256"))
	if len(*waits) != 2 || (*waits)[0] != 2*time.Second || (*waits)[1] != 4*time.Second {
		t.Fatalf("backoff waits = %v, want [2s 4s]", *waits)
	}
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Attempt 1/3 failed: Failed to copyto: permission denied",
		"INFO       Attempt 2/3 failed: Failed to copyto: permission denied",
		"INFO       Attempt 3/3 failed: Failed to copyto: permission denied",
		"INFO       Sidecar failed: host-backup-20261003-100000.tar.zst.sha256: Failed to copyto: permission denied",
		"WARNING  ⚠ Cloud Storage (rclone): backup saved, sidecar file not saved",
	)
}

// A secondary directory that cannot be created at step [6]: the same fact as at
// storage init and in the step [8] log copy, then "✗ ... backup not saved".
func TestSecondaryBlockDirectoryNotCreated(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: filepath.Join(blocker, "sub")}
	build := func(logger *logging.Logger) storage.Storage {
		s, err := storage.NewSecondaryStorage(cfg, logger, "")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		return s
	}
	got := syncRealBackend(t, cfg, build, &storage.FilesystemInfo{Type: storage.FilesystemExt4, SupportsOwnership: true}, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Secondary Storage",
		"INFO     Storing backup...",
		"INFO       Directory not created: not a directory",
		"WARNING  ✗ Secondary Storage: backup not saved",
	)
}

// Step [8], cloud log copy: with RCLONE_RETRIES > 1 all failed, the attempt lines
// then the outcome (no "  Copy failed"); with one attempt, "  Copy failed" carries
// the cause.
func TestDispatchLogFileCloudAttempts(t *testing.T) {
	fakeRcloneOnPath(t, failingCopytoScript)
	for _, tc := range []struct {
		retries int
		facts   []string
	}{
		{2, []string{
			"INFO       Attempt 1/2 failed: Failed to copyto: read-only file system",
			"INFO       Attempt 2/2 failed: Failed to copyto: read-only file system",
		}},
		{1, []string{"INFO       Copy failed: Failed to copyto: read-only file system"}},
	} {
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)
		cfg := &config.Config{CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", RcloneRetries: tc.retries, FsIoTimeoutSeconds: 30}
		o := &Orchestrator{logger: logger, cfg: cfg}
		src := writeSrcLog(t)
		if err := o.dispatchLogFile(context.Background(), src); err != nil {
			t.Fatalf("dispatchLogFile: %v", err)
		}
		want := append([]string{
			"INFO     Dispatching log file: " + filepath.Base(src),
			"INFO     Cloud: remote:/logs/" + filepath.Base(src),
		}, tc.facts...)
		want = append(want, "WARNING  ⚠ Log not copied to cloud")
		requireExactLines(t, visibleLines(buf.String()), want...)
	}
}

// Step [8] with CLOUD_REMOTE a local directory: the log goes INSIDE it
// (CLOUD_LOG_PATH under CLOUD_REMOTE, CLOUD_REMOTE_PATH not involved), the "Cloud:"
// line names that path, and rclone receives it without a "<name>:" prefix and
// without the directory joined twice.
func TestDispatchLogFileCloudLocalDirectory(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	// copyto does what rclone's local backend does: parents 0755, the file 0644.
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"echo \"$*\" >> "+record+"\n"+
		"case \"$1\" in\n"+
		"copyto) mkdir -p -m 0755 \"$(dirname \"$3\")\" && cp \"$2\" \"$3\" && chmod 0644 \"$3\";;\n"+
		"lsl) echo \"        7 2026-10-03 10:00:00.000000000 $(basename \"$2\")\";;\n"+
		"esac\n")
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	root := filepath.Join(t.TempDir(), "cloud")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: root, CloudRemotePath: "host1", CloudLogPath: "/proxsave/log", RcloneRetries: 1, FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}
	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	dest := root + "/proxsave/log/" + filepath.Base(src)
	requireExactLines(t, visibleLines(buf.String()),
		"INFO     Dispatching log file: "+filepath.Base(src),
		"INFO     Cloud: "+dest,
		"INFO     ✓ Log copied to cloud",
	)
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read the fake rclone record: %v", err)
	}
	requireExactLines(t, strings.Split(strings.TrimSpace(string(data)), "\n"),
		"copyto "+src+" "+dest,
		"lsl "+dest,
	)
	// The log gets the mode the Secondary log copy creates (0640), not rclone's 0644.
	if st, err := os.Stat(dest); err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("log mode = %v (%v), want 0640 like the Secondary log copy", st.Mode().Perm(), err)
	}
}

// Step [8], CLOUD_REMOTE a local directory and a CLOUD_LOG_PATH that resolves outside
// it: refused before rclone runs. The "Cloud:" line names the resolved destination,
// the fact names the cleaned CLOUD_REMOTE directory.
func TestDispatchLogFileCloudLogPathOutside(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	fakeRcloneOnPath(t, "#!/bin/sh\necho \"$*\" >> "+record+"\nexit 0\n")
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "/mnt/cloud/", CloudLogPath: "../logs", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}
	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	requireExactLines(t, visibleLines(buf.String()),
		"INFO     Dispatching log file: "+filepath.Base(src),
		"INFO     Cloud: /mnt/logs/"+filepath.Base(src),
		"INFO       CLOUD_LOG_PATH: outside /mnt/cloud",
		"WARNING  ⚠ Log not copied to cloud",
	)
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("rclone must not run for a refused destination (record err=%v)", err)
	}
}

// Step [8] cloud log copy, RCLONE_RETRIES = 1: an operation timeout is
// "  Copy failed: timed out after <N>s", and a verification that failed after a good
// copy is "  Verification failed: <error>"; the outcome stays "⚠ Log not copied".
func TestDispatchLogFileCloudTimeoutAndVerification(t *testing.T) {
	for _, tc := range []struct {
		name, script, fact string
		timeout            int
	}{
		{"timeout", "#!/bin/sh\ncase \"$1\" in\ncopyto) exec sleep 10;;\nesac\nexit 0\n",
			"INFO       Copy failed: timed out after 1s", 1},
		{"verification", "#!/bin/sh\ncase \"$1\" in\nlsl) echo \"        5 2026-10-03 10:00:00.000000000 $(basename \"$2\")\";;\nesac\nexit 0\n",
			"INFO       Verification failed: size mismatch: local=7 remote=5", 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeRcloneOnPath(t, tc.script)
			var buf bytes.Buffer
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(&buf)
			cfg := &config.Config{CloudEnabled: true, CloudLogPath: "/logs", CloudRemote: "remote", RcloneRetries: 1,
				RcloneTimeoutOperation: tc.timeout, FsIoTimeoutSeconds: 30}
			o := &Orchestrator{logger: logger, cfg: cfg}
			src := writeSrcLog(t)
			if err := o.dispatchLogFile(context.Background(), src); err != nil {
				t.Fatalf("dispatchLogFile: %v", err)
			}
			requireExactLines(t, visibleLines(buf.String()),
				"INFO     Dispatching log file: "+filepath.Base(src),
				"INFO     Cloud: remote:/logs/"+filepath.Base(src),
				tc.fact,
				"WARNING  ⚠ Log not copied to cloud",
			)
		})
	}
}

// CLOUD_LOG_PATH outside the local CLOUD_REMOTE AND the local log unreadable: on
// screen only the refusal, but the source probe still runs and its result is DEBUG.
func TestDispatchLogFileCloudLogPathOutsideKeepsTheProbe(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "/mnt/cloud", CloudLogPath: "../logs", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg, fs: logStatDeniedFS{}}
	o.copyLogToCloudFn = func(context.Context, string, string) error {
		t.Fatal("the upload must not run for a refused destination")
		return nil
	}
	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	requireExactLines(t, visibleLines(buf.String()),
		"INFO     Dispatching log file: "+filepath.Base(src),
		"INFO     Cloud: /mnt/logs/"+filepath.Base(src),
		"INFO       CLOUD_LOG_PATH: outside /mnt/cloud",
		"WARNING  ⚠ Log not copied to cloud",
	)
	if !strings.Contains(buf.String(), "source log "+src+" not accessible either: stat "+src+": permission denied") {
		t.Fatalf("the probe result must be in DEBUG:\n%s", buf.String())
	}
}

// Step [6], CLOUD_REMOTE a local directory: owner and mode are set like the
// Secondary's, and a chown that fails (unprivileged run) closes the block with the
// Secondary's facts and the cloud outcome "backup saved, permissions not set".
func TestCloudBlockLocalDirectoryPermissions(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"copyto) cp \"$2\" \"$3\" && chmod 0644 \"$3\";;\n"+
		"lsl) if [ -f \"$2\" ]; then echo \"        7 2026-10-03 10:00:00.000000000 $(basename \"$2\")\"; fi;;\n"+
		"esac\n")
	root := filepath.Join(t.TempDir(), "cloud")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: root, RcloneRetries: 1}
	archive := writeArchive(t)
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), nil, archive)
	name := filepath.Base(archive)
	if detected, err := storage.NewFilesystemDetector(logging.New(types.LogLevelInfo, false)).DetectFilesystem(context.Background(), root); err != nil || !detected.SupportsOwnership {
		t.Skipf("the temporary directory's filesystem takes no ownership here (%v): nothing is set, like on the Secondary", err)
	}
	if st, err := os.Stat(filepath.Join(root, name)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("archive mode = %v (%v), want 0600 like the Secondary", st.Mode().Perm(), err)
	}
	if os.Geteuid() == 0 {
		if last := got[len(got)-1]; last != "INFO     ✓ Cloud Storage (rclone): backup saved" {
			t.Fatalf("root: the block closes on the saved outcome, got:\n%s", strings.Join(got, "\n"))
		}
		return
	}
	tail := got[len(got)-2:]
	requireExactLines(t, tail,
		"INFO       Owner failed: "+name+": operation not permitted",
		"WARNING  ⚠ Cloud Storage (rclone): backup saved, permissions not set",
	)
}

// Step [6], CLOUD_REMOTE a local directory that cannot be (re)created before the copy:
// the Secondary's fact, then "✗ ... backup not saved".
func TestCloudBlockLocalDirectoryNotCreated(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\nexit 0\n")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg := &config.Config{CloudEnabled: true, CloudRemote: filepath.Join(blocker, "cloud"), RcloneRetries: 1}
	localFS := &storage.FilesystemInfo{Type: storage.FilesystemExt4, SupportsOwnership: true}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), localFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Directory not created: not a directory",
		"WARNING  ✗ Cloud Storage (rclone): backup not saved",
	)
}

// Step [8], local directory: a log whose mode cannot be set is there anyway:
// "  Permissions failed: <log>: <cause>" and "⚠ Log copied to cloud, permissions not set".
func TestDispatchLogFileCloudLocalLogPermissionsFailed(t *testing.T) {
	// copyto creates the directory but leaves no file, so the chmod after it fails.
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"copyto) mkdir -p -m 0755 \"$(dirname \"$3\")\";;\n"+
		"lsl) echo \"        7 2026-10-03 10:00:00.000000000 $(basename \"$2\")\";;\n"+
		"esac\n")
	root := filepath.Join(t.TempDir(), "cloud")
	if detected, err := storage.NewFilesystemDetector(logging.New(types.LogLevelInfo, false)).DetectFilesystem(context.Background(), filepath.Dir(root)); err != nil || !detected.SupportsOwnership {
		t.Skipf("the temporary directory's filesystem takes no ownership here (%v): the mode is not set at all", err)
	}
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: root, CloudLogPath: "/proxsave/log", RcloneRetries: 1, FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg}
	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	dest := root + "/proxsave/log/" + filepath.Base(src)
	requireExactLines(t, visibleLines(buf.String()),
		"INFO     Dispatching log file: "+filepath.Base(src),
		"INFO     Cloud: "+dest,
		"INFO       Permissions failed: "+filepath.Base(src)+": no such file or directory",
		"WARNING  ⚠ Log copied to cloud, permissions not set",
	)
	// The log directory rclone created keeps its 0755, like the Secondary log directory.
	if st, err := os.Stat(filepath.Dir(dest)); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("log directory mode = %v (%v), want 0755", st.Mode().Perm(), err)
	}
}

// Dry run, step [8]: the log stays in LOG_PATH. Each enabled destination names where
// the copy would go, then SKIP; nothing is written there (no SECONDARY_LOG_PATH mkdir,
// no rclone), checked on real temp dirs and on the rclone argv.
func TestDispatchLogFileDryRunCopiesNothing(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	fakeRcloneOnPath(t, "#!/bin/sh\necho \"$*\" >> "+record+"\nexit 0\n")
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	base := t.TempDir()
	secondaryLog := filepath.Join(base, "secondary-log")
	cloudRoot := filepath.Join(base, "cloud")
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: secondaryLog,
		CloudEnabled: true, CloudRemote: cloudRoot, CloudLogPath: "../outside", FsIoTimeoutSeconds: 30}
	o := &Orchestrator{logger: logger, cfg: cfg, dryRun: true}
	src := writeSrcLog(t)
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	name := filepath.Base(src)
	requireExactLines(t, visibleLines(buf.String()),
		"INFO     Dispatching log file: "+name,
		"INFO     Secondary: "+filepath.Join(secondaryLog, name),
		"SKIP     Log copy: dry run mode",
		"INFO     Cloud: "+filepath.Join(base, "outside", name),
		"SKIP     Log copy: dry run mode",
	)
	if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
		t.Fatalf("a dry run writes nothing on the destinations, found %v (%v)", entries, err)
	}
	if _, err := os.Stat(record); !os.IsNotExist(err) {
		t.Fatalf("rclone must not run in a dry run")
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("the log stays in LOG_PATH: %v", err)
	}

	// Only the enabled destinations.
	buf.Reset()
	o.cfg = &config.Config{SecondaryEnabled: false, CloudEnabled: false}
	if err := o.dispatchLogFile(context.Background(), src); err != nil {
		t.Fatalf("dispatchLogFile: %v", err)
	}
	requireExactLines(t, visibleLines(buf.String()), "INFO     Dispatching log file: "+name)
}

// The whole dry-run block of step [8], through FinalizeAfterRun as the backup run
// calls it.
func TestFinalizeAfterRunDryRunLogBlock(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	logPath := filepath.Join(t.TempDir(), "backup-node-20261003-100000.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	secondaryLog := filepath.Join(t.TempDir(), "secondary-log")
	cfg := &config.Config{SecondaryEnabled: true, SecondaryLogPath: secondaryLog, CloudEnabled: true, CloudRemote: "remote", CloudLogPath: "/logs", FsIoTimeoutSeconds: 30}
	o := New(logger, true)
	o.SetConfig(cfg)
	o.copyLogToCloudFn = func(context.Context, string, string) error {
		t.Fatal("a dry run copies no log to the cloud")
		return nil
	}
	o.FinalizeAfterRun(context.Background(), &BackupStats{})
	name := filepath.Base(logPath)
	requireExactLines(t, visibleLines(buf.String()),
		"STEP     [8] Log file management",
		"INFO     Closing log file: "+logPath,
		"INFO     Dispatching log file: "+name,
		"INFO     Secondary: "+filepath.Join(secondaryLog, name),
		"SKIP     Log copy: dry run mode",
		"INFO     Cloud: remote:/logs/"+name,
		"SKIP     Log copy: dry run mode",
	)
	if _, err := os.Stat(secondaryLog); !os.IsNotExist(err) {
		t.Fatalf("no SECONDARY_LOG_PATH mkdir in a dry run (stat err=%v)", err)
	}
}
