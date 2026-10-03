package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// fakeRcloneOnPath puts an executable "rclone" running script first on PATH, so
// the real CloudStorage (defaultExecCommand, safeexec) runs it.
func fakeRcloneOnPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rclone"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rclone: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// captureStorageInit runs fn with the default logger writing to a buffer and returns
// the visible lines (no DEBUG), without their timestamps.
func captureStorageInit(t *testing.T, fn func(logger *logging.Logger)) []string {
	t.Helper()
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)
	fn(logger)
	var lines []string
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if _, rest, ok := strings.Cut(line, "] "); ok {
			line = rest
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func requireBlock(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("block =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A reachable cloud block carries no "  Filesystem:" line: a remote has no filesystem
// of its own to show, and what the backend reports stays in DEBUG.
func TestCloudInitializedBlockHasNoFilesystemLine(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", CloudRetentionDays: 4}
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializeCloudStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path Cloud: remote",
		"INFO       Retention policy: simple (keep 4 newest)",
		"INFO     Checking cloud remote accessibility...",
		"INFO       Accessible",
		"INFO       Backups: 0",
		"INFO     ✓ Cloud storage: initialized",
	)
}

// The same holds for a remote reachable by the write test only.
func TestCloudWriteTestOnlyBlockHasNoFilesystemLine(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\ncase \"$1\" in\nlsf|lsl) echo 'ERROR : remote: permission denied' >&2; exit 1;;\nesac\nexit 0\n")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", CloudRetentionDays: 4, RcloneTimeoutConnection: 30}
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializeCloudStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path Cloud: remote",
		"INFO       Retention policy: simple (keep 4 newest)",
		"INFO     Checking cloud remote accessibility...",
		"INFO       Accessible by write test only, listing not permitted",
		"INFO       Backups: unknown, statistics unavailable",
		"WARNING  ⚠ Cloud storage: initialized, statistics unavailable",
	)
}

// A cloud backend that cannot be created prints the cause indented under the path,
// without a subject and with its first letter upper-cased, then the outcome and the
// SKIP. No filesystem line: nothing was checked.
func TestCloudBackendNotCreatedBlock(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "a/b:backups", CloudLogPath: "/logs", CloudRetentionDays: 4}
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializeCloudStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, nil, nil)
	})
	requireBlock(t, got,
		"INFO     Path Cloud: a/b:backups",
		"INFO       Retention policy: simple (keep 4 newest)",
		"INFO       Invalid CLOUD_REMOTE: rclone remote name contains a path separator or colon",
		"WARNING  ✗ Cloud storage: not initialized",
		"SKIP     Path Cloud: disabled",
	)
	if cfg.CloudEnabled || cfg.CloudLogPath != "" {
		t.Fatalf("the cloud must be off for the run: enabled=%v logPath=%q", cfg.CloudEnabled, cfg.CloudLogPath)
	}
}

// CLOUD_REMOTE as an absolute local directory: the backend accepts it, normalized
// (the "Path Cloud:" line keeps the value as configured), creates the backup
// directory with rclone mkdir, and runs rclone on plain paths (its local backend), with
// no "<name>:" prefix. The block is the reachable cloud block, and the storage summary
// shows the directory's real filesystem type.
func TestCloudLocalDirectoryIsInitialized(t *testing.T) {
	record := filepath.Join(t.TempDir(), "argv")
	// mkdir does what rclone's local backend does: it creates the directory, parents
	// included, so the filesystem detection finds it.
	fakeRcloneOnPath(t, "#!/bin/sh\necho \"$*\" >> "+record+"\n"+
		"if [ \"$1\" = mkdir ]; then mkdir -p \"$2\" || exit 1; fi\nexit 0\n")
	root := filepath.Join(t.TempDir(), "cloud")
	configured := root + "//"
	cfg := &config.Config{CloudEnabled: true, CloudRemote: configured, CloudRemotePath: "host1", CloudRetentionDays: 4}
	var cloudFS *storage.FilesystemInfo
	got := captureStorageInit(t, func(logger *logging.Logger) {
		cloudFS = initializeCloudStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, orchestrator.New(logger, false), nil)
	})
	requireBlock(t, got,
		"INFO     Path Cloud: "+configured,
		"INFO       Retention policy: simple (keep 4 newest)",
		"INFO     Checking cloud remote accessibility...",
		"INFO       Accessible",
		"INFO       Backups: 0",
		"INFO     ✓ Cloud storage: initialized",
	)
	if !cfg.CloudEnabled {
		t.Fatalf("the cloud must stay enabled")
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("read the fake rclone record: %v", err)
	}
	argv := strings.Split(strings.TrimSpace(string(data)), "\n")
	base := root + "/host1"
	want := []string{"mkdir " + base, "lsf " + root + " --max-depth 1", "lsf " + base + " --max-depth 1"}
	if len(argv) < len(want) || strings.Join(argv[:len(want)], "\n") != strings.Join(want, "\n") {
		t.Fatalf("rclone argv =\n%s\nwant it to open with\n%s", strings.Join(argv, "\n"), strings.Join(want, "\n"))
	}
	for _, line := range argv {
		if strings.Contains(line, ":") {
			t.Fatalf("a local directory must reach rclone without a remote prefix: %q", line)
		}
	}
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		t.Fatalf("the backup directory %s was not created: %v", base, err)
	}
	// The storage summary shows the type the Primary and the Secondary would see.
	detected, err := storage.NewFilesystemDetector(logging.New(types.LogLevelInfo, false)).DetectFilesystem(context.Background(), base)
	if err != nil {
		t.Fatalf("detect %s: %v", base, err)
	}
	if label, want := formatStorageLabel(cfg.CloudRemote, cloudFS), configured+" ["+string(detected.Type)+"]"; label != want || strings.Contains(label, "rclone") {
		t.Fatalf("storage label = %q, want %q", label, want)
	}
}

// A local CLOUD_REMOTE directory that cannot be created: the Secondary's fact under
// the check, then the outcome and the SKIP, and the cloud is off for the run.
func TestCloudLocalDirectoryNotCreatedBlock(t *testing.T) {
	fakeRcloneOnPath(t, "#!/bin/sh\nif [ \"$1\" = mkdir ]; then echo '2026/10/03 10:00:00 Failed to mkdir: mkdir /mnt/cloud: permission denied' >&2; exit 1; fi\nexit 0\n")
	t.Cleanup(storage.SetCloudRetryWaitForTest(func(ctx context.Context, _ time.Duration) error { return ctx.Err() }))
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "/mnt/cloud", CloudLogPath: "/logs", CloudRetentionDays: 4}
	got := captureStorageInit(t, func(logger *logging.Logger) {
		initializeCloudStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, nil, nil)
	})
	requireBlock(t, got,
		"INFO     Path Cloud: /mnt/cloud",
		"INFO       Retention policy: simple (keep 4 newest)",
		"INFO     Checking cloud remote accessibility...",
		"INFO       Directory not created: Failed to mkdir: mkdir /mnt/cloud: permission denied",
		"WARNING  ✗ Cloud storage: not initialized",
		"SKIP     Path Cloud: disabled",
	)
	if cfg.CloudEnabled || cfg.CloudLogPath != "" {
		t.Fatalf("the cloud must be off for the run: enabled=%v logPath=%q", cfg.CloudEnabled, cfg.CloudLogPath)
	}
}
