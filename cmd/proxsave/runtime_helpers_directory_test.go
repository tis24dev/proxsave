package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/pkg/utils"
)

func newBufferedTestLogger() (*logging.Logger, *bytes.Buffer) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

func TestEnsureDirectoryExistsAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	logger, buf := newBufferedTestLogger()

	ensureDirectoryExists(logger, "Backup directory", dir, time.Second)

	output := buf.String()
	expected := "Backup directory exists: " + dir
	if !strings.Contains(output, expected) {
		t.Fatalf("expected log to contain %q, got %s", expected, output)
	}
	if strings.Contains(output, "created:") {
		t.Fatalf("did not expect creation log for existing directory; output=%s", output)
	}
}

func TestEnsureDirectoryExistsCreatesMissingDir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "cloud", "logs")
	logger, buf := newBufferedTestLogger()

	ensureDirectoryExists(logger, "Cloud log directory", target, time.Second)

	if !utils.DirExists(target) {
		t.Fatalf("expected directory %s to be created", target)
	}

	output := buf.String()
	if !strings.Contains(output, "Cloud log directory created: "+target) {
		t.Fatalf("expected creation log for %s, got %s", target, output)
	}
	if !strings.Contains(output, "Cloud log directory not found: "+target) {
		t.Fatalf("expected missing warning for %s, got %s", target, output)
	}
}

func TestEnsureDirectoryExistsLogsFailure(t *testing.T) {
	root := t.TempDir()
	conflict := filepath.Join(root, "conflict")
	if err := os.WriteFile(conflict, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to prepare conflict file: %v", err)
	}

	logger, buf := newBufferedTestLogger()
	ensureDirectoryExists(logger, "Cloud log directory", conflict, time.Second)

	output := buf.String()
	if !strings.Contains(output, "Cloud log directory not found: "+conflict) {
		t.Fatalf("expected missing warning for %s, got %s", conflict, output)
	}
	if !strings.Contains(output, "Failed to create "+conflict) {
		t.Fatalf("expected failure log for %s, got %s", conflict, output)
	}
	if utils.DirExists(conflict) {
		t.Fatalf("conflict path %s unexpectedly became a directory", conflict)
	}
}

// Live test case 7: a dry run does not create a missing SECONDARY_LOG_PATH while
// verifying the directory structure; it says what a real run would create. A real run
// still creates it, and LOG_PATH and the lock directory are created in a dry run too.
func TestVerifyBackupDirectoriesDryRunLeavesTheSecondaryLogDirectory(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		root := t.TempDir()
		secondaryLog := filepath.Join(root, "secondary-log")
		cfg := &config.Config{DryRun: dryRun, BackupPath: filepath.Join(root, "backup"), LogPath: filepath.Join(root, "log"),
			LockPath: filepath.Join(root, "lock"), SecondaryEnabled: true, SecondaryLogPath: secondaryLog}
		logger := logging.New(types.LogLevelInfo, false)
		buf := &bytes.Buffer{}
		logger.SetOutput(buf)
		prev := logging.GetDefaultLogger()
		logging.SetDefaultLogger(logger)
		verifyBackupDirectories(cfg, logger)
		logging.SetDefaultLogger(prev)

		out := buf.String()
		notFound := strings.Index(out, "WARNING  ✗ Secondary log directory not found: "+secondaryLog+"\n")
		wouldCreate := strings.Index(out, "INFO     [DRY RUN] Would create directory: "+secondaryLog+"\n")
		exists := utils.DirExists(secondaryLog)
		if dryRun && (notFound < 0 || wouldCreate < notFound || exists || strings.Contains(out, "Secondary log directory created")) {
			t.Fatalf("dry run: want not found, then Would create, and no directory (exists=%v):\n%s", exists, out)
		}
		if !dryRun && (!exists || wouldCreate >= 0) {
			t.Fatalf("real run: the directory must be created (exists=%v):\n%s", exists, out)
		}
		for _, dir := range []string{cfg.LogPath, cfg.LockPath} {
			if !utils.DirExists(dir) {
				t.Fatalf("dryRun=%v: %s must still be created", dryRun, dir)
			}
		}
	}
}

// A secondary whose backup or log directory is the local one, by path or through a
// link, is reported and switched off for the run, without failing it; a separate one
// is left alone.
func TestVerifyBackupDirectoriesDisablesASecondaryThatIsTheLocalOne(t *testing.T) {
	cases := map[string]struct {
		secondary, secondaryLog func(root, backup, log string) string
		wantLines               []string
	}{
		"backup directory by path": {
			secondary:    func(root, backup, log string) string { return backup + "/" },
			secondaryLog: func(root, backup, log string) string { return filepath.Join(root, "secondary-log") },
			wantLines:    []string{"WARNING  ✗ Secondary directory same as Backup directory (secondary storage disabled)"},
		},
		"log directory through a link": {
			secondary: func(root, backup, log string) string { return filepath.Join(root, "secondary") },
			secondaryLog: func(root, backup, log string) string {
				link := filepath.Join(root, "log-link")
				if err := os.Symlink(log, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
			wantLines: []string{"WARNING  ✗ Secondary log directory same as Log directory (secondary storage disabled)"},
		},
		"separate directories": {
			secondary:    func(root, backup, log string) string { return filepath.Join(root, "secondary") },
			secondaryLog: func(root, backup, log string) string { return filepath.Join(root, "secondary-log") },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			backup, logDir := filepath.Join(root, "backup"), filepath.Join(root, "log")
			for _, dir := range []string{backup, logDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{BackupPath: backup, LogPath: logDir, LockPath: filepath.Join(root, "lock"),
				SecondaryEnabled: true, SecondaryPath: tc.secondary(root, backup, logDir), SecondaryLogPath: tc.secondaryLog(root, backup, logDir)}
			logger := logging.New(types.LogLevelInfo, false)
			buf := &bytes.Buffer{}
			logger.SetOutput(buf)
			prev := logging.GetDefaultLogger()
			logging.SetDefaultLogger(logger)
			verifyBackupDirectories(cfg, logger)
			logging.SetDefaultLogger(prev)

			out := buf.String()
			for _, line := range tc.wantLines {
				if !strings.Contains(out, line) {
					t.Fatalf("missing %q in:\n%s", line, out)
				}
			}
			disabled := len(tc.wantLines) > 0
			if cfg.SecondaryEnabled == disabled || cfg.SecondaryStartupFailed {
				t.Fatalf("SecondaryEnabled=%v SecondaryStartupFailed=%v, want disabled=%v and not failed:\n%s",
					cfg.SecondaryEnabled, cfg.SecondaryStartupFailed, disabled, out)
			}
			if disabled && strings.Contains(out, "Secondary log directory exists") {
				t.Fatalf("a disabled secondary still had its log directory checked:\n%s", out)
			}
		})
	}
}

// A cloud that is a local directory gets the same check: its backup or log directory
// may be neither the local one nor the secondary's while that is on. It is switched off,
// not failed; a cloud on a remote is never compared.
func TestVerifyBackupDirectoriesDisablesACloudThatIsALocalOne(t *testing.T) {
	cases := map[string]struct {
		cloudRemote, cloudLog func(root string) string
		secondary             bool
		wantLines             []string
	}{
		"cloud directory is the backup directory": {
			cloudRemote: func(root string) string { return filepath.Join(root, "backup") },
			cloudLog:    func(root string) string { return "/cloud-log" },
			wantLines:   []string{"WARNING  ✗ Cloud directory same as Backup directory (cloud storage disabled)"},
		},
		"cloud log directory is the log directory": {
			cloudRemote: func(root string) string { return filepath.Join(root, "cloud") },
			cloudLog:    func(root string) string { return "../log" },
			wantLines:   []string{"WARNING  ✗ Cloud log directory same as Log directory (cloud storage disabled)"},
		},
		"cloud log directory is the secondary log directory": {
			cloudRemote: func(root string) string { return filepath.Join(root, "cloud") },
			cloudLog:    func(root string) string { return "../secondary-log" },
			secondary:   true,
			wantLines:   []string{"WARNING  ✗ Cloud log directory same as Secondary log directory (cloud storage disabled)"},
		},
		"separate directories": {
			cloudRemote: func(root string) string { return filepath.Join(root, "cloud") },
			cloudLog:    func(root string) string { return "/cloud-log" },
			secondary:   true,
		},
		"cloud on a remote": {
			cloudRemote: func(root string) string { return "gdrive" },
			cloudLog:    func(root string) string { return "/log" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, dir := range []string{"backup", "log", "secondary", "secondary-log", "cloud"} {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &config.Config{BackupPath: filepath.Join(root, "backup"), LogPath: filepath.Join(root, "log"),
				LockPath: filepath.Join(root, "lock"), CloudEnabled: true,
				CloudRemote: tc.cloudRemote(root), CloudLogPath: tc.cloudLog(root)}
			if tc.secondary {
				cfg.SecondaryEnabled = true
				cfg.SecondaryPath = filepath.Join(root, "secondary")
				cfg.SecondaryLogPath = filepath.Join(root, "secondary-log")
			}
			logger := logging.New(types.LogLevelInfo, false)
			buf := &bytes.Buffer{}
			logger.SetOutput(buf)
			prev := logging.GetDefaultLogger()
			logging.SetDefaultLogger(logger)
			verifyBackupDirectories(cfg, logger)
			logging.SetDefaultLogger(prev)

			out := buf.String()
			for _, line := range tc.wantLines {
				if !strings.Contains(out, line) {
					t.Fatalf("missing %q in:\n%s", line, out)
				}
			}
			disabled := len(tc.wantLines) > 0
			if cfg.CloudEnabled == disabled || cfg.CloudStartupFailed {
				t.Fatalf("CloudEnabled=%v CloudStartupFailed=%v, want disabled=%v and not failed:\n%s",
					cfg.CloudEnabled, cfg.CloudStartupFailed, disabled, out)
			}
			if !disabled && strings.Contains(out, "Cloud directory same") {
				t.Fatalf("a separate cloud was reported:\n%s", out)
			}
		})
	}
}

// A directory that does not answer within FS_IO_TIMEOUT (a dead NFS mount) is
// reported as not checked and nothing else is tried on it: an unbounded stat there
// blocked the run before the lock.
func TestDirectoryChecksReportADirectoryThatDoesNotAnswer(t *testing.T) {
	origStat, origMkdir := dirCheckStat, dirCheckMkdirAll
	t.Cleanup(func() { dirCheckStat, dirCheckMkdirAll = origStat, origMkdir })
	dirCheckStat = func(_ context.Context, path string, timeout time.Duration) (fs.FileInfo, error) {
		return nil, &safefs.TimeoutError{Op: "stat", Path: path, Timeout: timeout}
	}
	mkdirs := 0
	dirCheckMkdirAll = func(context.Context, string, os.FileMode, time.Duration) error {
		mkdirs++
		return nil
	}
	logger, buf := newBufferedTestLogger()

	ensureDirectoryExists(logger, "Secondary log directory", "/mnt/nas/log", 30*time.Second)
	reportDirectoryDryRun(logger, "Secondary log directory", "/mnt/nas/log", 30*time.Second)

	want := "WARNING  ✗ Secondary log directory not checked: /mnt/nas/log did not answer within 30s (dead/stale mount?)"
	if got := strings.Count(buf.String(), want); got != 2 {
		t.Fatalf("want the line twice, got %d:\n%s", got, buf.String())
	}
	if mkdirs != 0 || strings.Contains(buf.String(), "not found") {
		t.Fatalf("a directory that did not answer must not be reported missing or created (mkdirs=%d):\n%s", mkdirs, buf.String())
	}
}
