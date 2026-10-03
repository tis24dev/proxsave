package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// fakeRcloneOnPath puts an executable "rclone" running script first on PATH, so the
// real CloudStorage runs it through safeexec. The retry backoff is the real one
// (2s, 4s, ...), so these cases keep the attempts few.
func fakeRcloneOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
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
	fakeRcloneOnPath(t, failingCopytoScript)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 2}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t))
	requireExactLines(t, got,
		"STEP     Cloud Storage (rclone)",
		"INFO     Storing backup...",
		"INFO       Attempt 1/2 failed: Failed to copyto: read-only file system",
		"INFO       Attempt 2/2 failed: Failed to copyto: read-only file system",
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
	fakeRcloneOnPath(t, "#!/bin/sh\n"+
		"case \"$1\" in\n"+
		"copyto) case \"$2\" in *.sha256) echo '2026/10/03 10:00:00 Failed to copyto: permission denied' >&2; exit 1;; esac; exit 0;;\n"+
		"lsl) case \"$2\" in *.tar.zst) echo '        7 2026-10-03 10:00:00.000000000 host-backup-20261003-100000.tar.zst';; esac; exit 0;;\n"+
		"esac\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 3}
	got := syncRealBackend(t, cfg, newRealCloud(t, cfg), cloudFS, writeArchive(t, ".sha256"))
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
