package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// Exit-code rule of a backup run on a clean host. The characterization goldens
// (backup_characterization_test.go) run as ProxmoxUnknown, whose baseline WARNINGs make
// even a clean run exit 1, so there the exit code cannot tell "all ok" from "a secondary
// or cloud copy failed". Here the run is a Proxmox VE host with zero baseline warnings,
// so the exit code alone tells them apart. It reuses the characterization fakes (storage
// backends behind the real StorageAdapter, registered local, secondary, cloud, and the
// notification channel) and asserts only the exit code, the warning count and the
// per-destination statuses: no golden file.

// backupExitFakeEmpty answers with nothing and succeeds.
const backupExitFakeEmpty = "#!/bin/sh\nexit 0\n"

// backupExitFakeSystemctl answers every query the way systemctl answers for an inactive
// unit (exit 3), so the PVE collector sees corosync and Ceph down whatever the machine
// running the test has installed.
const backupExitFakeSystemctl = "#!/bin/sh\nexit 3\n"

// backupExitEmptyFakes are the commands the system recipe can reach. PATH is set to the
// fakes' directory alone, but the collector appends /usr/local/sbin, /usr/sbin and /sbin
// to it (ensureSystemPath), so each one is shadowed here to keep the machine's own
// binaries, and the warnings they can raise, out of the run. Every name must be one
// safeexec allows.
var backupExitEmptyFakes = []string{
	"uname", "hostname", "ip", "bridge", "df", "mount", "lsblk", "blkid", "free", "lscpu",
	"lspci", "lsusb", "pvs", "vgs", "lvs", "dmidecode", "sensors", "smartctl",
}

// backupExitRealTools are linked from the machine running the test: cat reads the
// fixtures, tar lists the archive to verify it.
var backupExitRealTools = []string{"cat", "tar"}

// backupExitFixtures is the fake host under SYSTEM_ROOT_PREFIX. The etc/pve directory is
// what the PVE collector checks before collecting; under the prefix it skips the PVE
// runtime commands (pvesh, pveversion, pvecm), which would describe this machine. Each
// file is one the system recipe reads with cat and warns about when it is missing.
var backupExitFixtures = map[string]string{
	"etc/pve/":        "",
	"etc/os-release":  "PRETTY_NAME=\"Exit code test\"\nID=debian\n",
	"proc/cmdline":    "BOOT_IMAGE=/vmlinuz root=/dev/sda1\n",
	"proc/version":    "Linux version 0.0.0-exit-code-test\n",
	"etc/resolv.conf": "nameserver 192.0.2.53\n",
}

// setupBackupExitCleanPVEHost writes the fixtures under the system root and the fakes in
// the bin directory, then makes the bin directory the whole PATH.
func setupBackupExitCleanPVEHost(t *testing.T, dirs backupCharDirs) {
	t.Helper()
	for rel, content := range backupExitFixtures {
		p := filepath.Join(dirs.sysroot, rel)
		if strings.HasSuffix(rel, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatalf("mkdir fixture %s: %v", rel, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir fixture %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", rel, err)
		}
	}
	scripts := map[string]string{"systemctl": backupExitFakeSystemctl}
	for _, name := range backupExitEmptyFakes {
		scripts[name] = backupExitFakeEmpty
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(dirs.bin, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	for _, name := range backupExitRealTools {
		target, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("look up %s: %v", name, err)
		}
		if err := os.Symlink(target, filepath.Join(dirs.bin, name)); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dirs.bin)
}

type backupExitCodeCase struct {
	name       string
	failLocal  bool
	failSecond bool
	failCloud  bool
	// wantPhase is the phase of the BackupError RunGoBackup must return; empty means it
	// must return no error.
	wantPhase    string
	wantExit     int
	wantWarnings int
	// Statuses of the destinations in the returned stats; empty means not asserted.
	wantLocal     string
	wantSecondary string
	wantCloud     string
}

type backupExitCodeResult struct {
	stats   *BackupStats
	err     error
	logPath string
}

// runBackupExitCodeCase drives Orchestrator.RunGoBackup the way runBackupCharacterization
// does, with the same configuration and fakes, on the clean PVE host instead of an
// unknown one.
func runBackupExitCodeCase(t *testing.T, tc backupExitCodeCase) backupExitCodeResult {
	t.Helper()

	origUmask := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(origUmask) })

	dirs := newBackupCharDirs(t)
	origRoot := workspaceRoot
	workspaceRoot = dirs.workspace
	t.Cleanup(func() { workspaceRoot = origRoot })

	t.Setenv(health.EnvRunID, "")
	t.Setenv("LC_ALL", "C")
	setupBackupExitCleanPVEHost(t, dirs)

	clock := &backupCharClock{now: backupCharStart}
	rec := &backupCharRecorder{}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	logPath := filepath.Join(dirs.log, fmt.Sprintf("backup-%s-%s.log", backupCharHost, backupCharStart.Format("20060102-150405")))
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() { _ = logger.CloseLogFile() })

	cfg := &config.Config{
		BackupPath:             dirs.backup,
		LogPath:                dirs.log,
		BundleAssociatedFiles:  true,
		RetentionPolicy:        "simple",
		LocalRetentionDays:     7,
		SecondaryRetentionDays: 14,
		CloudRetentionDays:     30,
		SecondaryEnabled:       true,
		SecondaryPath:          dirs.secondary,
		SecondaryLogPath:       dirs.secondaryLog,
		CloudEnabled:           true,
		CloudRemote:            backupCharRemote,
		CloudLogPath:           dirs.cloudLog,
		FsIoTimeoutSeconds:     30,
		SystemRootPrefix:       dirs.sysroot,
	}

	registry, err := NewTempDirRegistry(logger, filepath.Join(dirs.registry, "temp-dirs.json"))
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}

	orch := New(logger, false)
	orch.clock = clock
	orch.SetBackupConfig(dirs.backup, dirs.log, types.CompressionNone, 0, 0, "standard", nil)
	orch.SetConfig(cfg)
	orch.SetTempDirRegistry(registry)
	orch.copyLogToCloudFn = func(ctx context.Context, sourcePath, destPath string) error {
		rec.add("cloud log upload dest=%s", destPath)
		return copyBackupCharFile(sourcePath, strings.TrimPrefix(destPath, backupCharRemote+":"))
	}

	// Registered in the order cmd/proxsave/backup_storage.go registers them: local,
	// secondary, cloud, with the filesystem info and initial stats of the characterization.
	backends := []*backupCharBackend{
		{
			label: "local", name: "Local Storage", location: storage.LocationPrimary, critical: true,
			failStore: tc.failLocal,
			fsInfo:    storage.FilesystemInfo{Path: dirs.backup, Type: storage.FilesystemExt4, SupportsOwnership: true, MountPoint: "/"},
			stats: storage.StorageStats{TotalBackups: 8, TotalSize: 8 << 20,
				AvailableSpace: 100 << 30, UsedSpace: 50 << 30, TotalSpace: 150 << 30},
			deleted: 1,
			summary: storage.RetentionSummary{BackupsDeleted: 1, LogsDeleted: 1, ScopeValid: true, Owned: 7, PassCompleted: true},
		},
		{
			label: "secondary", name: "Secondary Storage", location: storage.LocationSecondary,
			destDir: dirs.secondary, failStore: tc.failSecond,
			fsInfo: storage.FilesystemInfo{Path: dirs.secondary, Type: storage.FilesystemNFS4, IsNetworkFS: true, MountPoint: dirs.secondary},
			stats: storage.StorageStats{TotalBackups: 5, TotalSize: 5 << 20,
				AvailableSpace: 200 << 30, UsedSpace: 300 << 30, TotalSpace: 500 << 30},
			summary: storage.RetentionSummary{ScopeValid: true, Owned: 3, PassCompleted: true},
		},
		{
			label: "cloud", name: "Cloud Storage (rclone)", location: storage.LocationCloud,
			destDir: dirs.cloud, failStore: tc.failCloud,
			fsInfo: storage.FilesystemInfo{Path: backupCharRemote + ":", Type: storage.FilesystemType("rclone-" + backupCharRemote),
				IsNetworkFS: true, MountPoint: backupCharRemote + ":", Device: "cloud"},
			stats:   storage.StorageStats{TotalBackups: 4, TotalSize: 4 << 20},
			deleted: 2,
			summary: storage.RetentionSummary{BackupsDeleted: 2, PassCompleted: true},
		},
	}
	for _, b := range backends {
		b.rec = rec
		b.clock = clock
		info := b.fsInfo
		initial := b.stats
		adapter := NewStorageAdapter(b, logger, cfg)
		adapter.SetFilesystemInfo(&info)
		adapter.SetInitialStats(&initial)
		orch.RegisterStorageTarget(adapter)
	}
	orch.RegisterNotificationChannel(&backupCharChannel{rec: rec})

	res := backupExitCodeResult{logPath: logPath}
	captureBackupCharOutput(t, logger, func() {
		res.stats, res.err = orch.RunGoBackup(context.Background(),
			&environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "unknown"}, backupCharHost)
	})
	return res
}

// backupExitCodeAsCmd is the exit code cmd/proxsave/backup_execution.go turns the run
// into: the stats' code when RunGoBackup succeeds, the BackupError's code when it fails,
// ExitBackupError for any other error.
func backupExitCodeAsCmd(stats *BackupStats, err error) int {
	if err == nil {
		return stats.ExitCode
	}
	var be *BackupError
	if errors.As(err, &be) {
		return be.Code.Int()
	}
	return types.ExitBackupError.Int()
}

// backupExitIssueLines lists the run log lines the log parser counts as issues
// (classifyLogLine, behind WarningCount and ErrorCount), so a failure names a stray
// warning instead of only counting it.
func backupExitIssueLines(logPath string) string {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Sprintf("  (run log unreadable: %v)", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if kind, msg := classifyLogLine(line); kind != "" && msg != "" {
			lines = append(lines, "  "+strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		return "  (none)"
	}
	return strings.Join(lines, "\n")
}

func TestBackupExitCodeOnCleanPVEHost(t *testing.T) {
	cases := []backupExitCodeCase{
		{
			name:     "all_ok",
			wantExit: types.ExitSuccess.Int(), wantWarnings: 0,
			wantLocal: "ok", wantSecondary: "ok", wantCloud: "ok",
		},
		// One WARNING per failed copy: the "✗ <Name>: backup not saved" outcome. The
		// store error and the closing "operations completed with errors" line used to
		// repeat it twice more; the exit code is the same either way.
		{
			name: "secondary_failed", failSecond: true,
			wantExit: types.ExitGenericError.Int(), wantWarnings: 1,
			wantLocal: "ok", wantSecondary: "error", wantCloud: "ok",
		},
		{
			name: "cloud_failed", failCloud: true,
			wantExit: types.ExitGenericError.Int(), wantWarnings: 1,
			wantLocal: "ok", wantSecondary: "ok", wantCloud: "error",
		},
		{
			name: "local_failed", failLocal: true,
			wantPhase: "storage", wantExit: types.ExitStorageError.Int(), wantWarnings: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := runBackupExitCodeCase(t, tc)
			defer func() {
				if t.Failed() {
					t.Logf("run log lines counted as issues:\n%s", backupExitIssueLines(res.logPath))
				}
			}()

			if tc.wantPhase == "" {
				if res.err != nil {
					t.Fatalf("RunGoBackup returned %v, want no error", res.err)
				}
			} else {
				var be *BackupError
				if !errors.As(res.err, &be) {
					t.Fatalf("RunGoBackup returned %v, want a BackupError in phase %q", res.err, tc.wantPhase)
				}
				if be.Phase != tc.wantPhase {
					t.Errorf("BackupError phase = %q, want %q", be.Phase, tc.wantPhase)
				}
			}
			if res.stats == nil {
				t.Fatalf("RunGoBackup returned nil stats")
			}

			if got := backupExitCodeAsCmd(res.stats, res.err); got != tc.wantExit {
				t.Errorf("exit code (as cmd/proxsave computes it) = %d, want %d", got, tc.wantExit)
			}
			if res.stats.WarningCount != tc.wantWarnings {
				t.Errorf("WarningCount = %d, want %d", res.stats.WarningCount, tc.wantWarnings)
			}
			for _, s := range []struct{ field, got, want string }{
				{"LocalStatus", res.stats.LocalStatus, tc.wantLocal},
				{"SecondaryStatus", res.stats.SecondaryStatus, tc.wantSecondary},
				{"CloudStatus", res.stats.CloudStatus, tc.wantCloud},
			} {
				if s.want != "" && s.got != s.want {
					t.Errorf("%s = %q, want %q", s.field, s.got, s.want)
				}
			}
		})
	}
}
