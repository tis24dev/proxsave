package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
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
