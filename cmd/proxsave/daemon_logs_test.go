package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRunLogLocation(t *testing.T) {
	cfg := &config.Config{LogPath: "/srv/logs", DaemonLogPath: "/srv/logs/daemon"}
	cases := []struct {
		name       string
		args       *cli.Args
		dir, label string
	}{
		{"backup run", &cli.Args{}, "/srv/logs", "backup"},
		{"daemon", &cli.Args{Daemon: true}, "/srv/logs/daemon", "daemon"},
		{"daemon-setup", &cli.Args{DaemonSetup: true}, "/srv/logs/daemon", "daemon"},
		{"daemon-remove", &cli.Args{DaemonRemove: true}, "/srv/logs/daemon", "daemon"},
		{"daemon-status", &cli.Args{DaemonStatus: true}, "/srv/logs/daemon", "daemon"},
	}
	for _, tc := range cases {
		dir, prefix := runLogLocation(tc.args, cfg)
		if dir != tc.dir || prefix != tc.label {
			t.Errorf("%s: runLogLocation = %q, %q; want %q, %q", tc.name, dir, prefix, tc.dir, tc.label)
		}
	}
}

func TestDaemonLogKey(t *testing.T) {
	if host, ts, ok := daemonLogKey("daemon-pve1.lan-20261012-115520.log"); !ok || host != "pve1.lan" || ts != "20261012-115520" {
		t.Fatalf("daemonLogKey = %q, %q, %v", host, ts, ok)
	}
	for _, name := range []string{"backup-pve1-20261012-115520.log", "daemon-pve1-2026101-115520.log", "daemon-20261012-115520.log"} {
		if _, _, ok := daemonLogKey(name); ok {
			t.Errorf("daemonLogKey(%q) accepted", name)
		}
	}
}

func newDaemonLogTestLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	return logger, &buf
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// A daemon start keeps its own log and the two before it, of this host only.
func TestRotateDaemonLogsKeepsTheNewestThreeOfThisHost(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"daemon-node-20260720-204416.log",
		"daemon-node-20260720-204418.log",
		"daemon-node-20260729-085438.log",
		"daemon-node-20260730-183132.log",
		"daemon-node-20261007-090309.log", // this start
		"daemon-other-20260101-000000.log",
		"backup-node-20260101-000000.log",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("log"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	logger, buf := newDaemonLogTestLogger()
	rotateDaemonLogs(context.Background(), logger, filepath.Join(dir, "daemon-node-20261007-090309.log"), time.Second)

	want := []string{
		"backup-node-20260101-000000.log",
		"daemon-node-20260729-085438.log",
		"daemon-node-20260730-183132.log",
		"daemon-node-20261007-090309.log",
		"daemon-other-20260101-000000.log",
	}
	if got := listDir(t, dir); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("left %v, want %v", got, want)
	}
	out := buf.String()
	for _, line := range []string{
		"daemon log rotation: listed=5 delete=2",
		"daemon log rotation: deleted daemon-node-20260720-204416.log",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
	if strings.Contains(out, "INFO") {
		t.Errorf("a clean rotation prints no INFO line:\n%s", out)
	}
}

// The log this start opened is never deleted, even when its name sorts older.
func TestRotateDaemonLogsNeverDeletesTheCurrentLog(t *testing.T) {
	dir := t.TempDir()
	current := "daemon-node-20250101-000000.log" // clock set back
	for _, name := range []string{current, "daemon-node-20261001-000000.log", "daemon-node-20261002-000000.log", "daemon-node-20261003-000000.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("log"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	logger, _ := newDaemonLogTestLogger()
	rotateDaemonLogs(context.Background(), logger, filepath.Join(dir, current), time.Second)
	if _, err := os.Stat(filepath.Join(dir, current)); err != nil {
		t.Fatalf("the current log went: %v", err)
	}
}

// A log it cannot delete: the fact line, then the outcome, both INFO.
func TestRotateDaemonLogsReportsALogNotDeleted(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files from a read-only directory")
	}
	dir := t.TempDir()
	for _, name := range []string{"daemon-node-20261001-000000.log", "daemon-node-20261002-000000.log", "daemon-node-20261003-000000.log", "daemon-node-20261004-000000.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("log"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	logger, buf := newDaemonLogTestLogger()
	rotateDaemonLogs(context.Background(), logger, filepath.Join(dir, "daemon-node-20261004-000000.log"), time.Second)
	out := buf.String()
	for _, line := range []string{
		"INFO       Log not deleted: daemon-node-20261001-000000.log: permission denied",
		"INFO     ⚠ Daemon logs: rotation not applied",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("missing %q in:\n%s", line, out)
		}
	}
	if strings.Index(out, "Log not deleted") > strings.Index(out, "rotation not applied") {
		t.Errorf("the outcome must close the block, after the fact:\n%s", out)
	}
}

func TestRotateDaemonLogsWithoutALogFile(t *testing.T) {
	logger, buf := newDaemonLogTestLogger()
	rotateDaemonLogs(context.Background(), logger, "", time.Second)
	if !strings.Contains(buf.String(), "daemon log rotation: skipped reason=no_log_file") {
		t.Fatalf("missing the skip evidence:\n%s", buf.String())
	}
}
