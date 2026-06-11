package backup

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

func TestEnsureSystemPathAddsDefaults(t *testing.T) {
	t.Setenv("PATH", "")

	ensureSystemPath()

	got := os.Getenv("PATH")
	if got == "" {
		t.Fatal("PATH should not remain empty")
	}
	for _, required := range []string{"/usr/local/sbin", "/usr/sbin", "/sbin"} {
		if !strings.Contains(got, required) {
			t.Fatalf("PATH %q should contain %s", got, required)
		}
	}
}

func TestCollectSystemKernelModulesRuntimeBestEffort(t *testing.T) {
	var log bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&log)

	tempDir := t.TempDir()
	config := GetDefaultCollectorConfig()
	calls := 0
	collector := NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, CollectorDeps{
		LookPath: func(name string) (string, error) {
			if name == "lsmod" {
				return "/usr/sbin/lsmod", nil
			}
			return "", os.ErrNotExist
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "lsmod" {
				t.Fatalf("unexpected command %s", name)
			}
			calls++
			return []byte("lsmod failed"), errors.New("lsmod failed")
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})

	commandsDir := filepath.Join(tempDir, "commands")
	if err := collector.collectSystemKernelModulesRuntime(context.Background(), commandsDir); err != nil {
		t.Fatalf("collectSystemKernelModulesRuntime returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("lsmod calls=%d; want 1", calls)
	}
	if logger.WarningCount() != 0 {
		t.Fatalf("expected lsmod failure to stay below warning level, warnings=%d log=%s", logger.WarningCount(), log.String())
	}
	if _, err := os.Stat(filepath.Join(commandsDir, "lsmod.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected no lsmod output file on failure, stat err: %v", err)
	}
}

func TestCollectSystemKernelModulesRuntimePropagatesCommandCancellation(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	tempDir := t.TempDir()
	config := GetDefaultCollectorConfig()
	collector := NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, CollectorDeps{
		LookPath: func(name string) (string, error) {
			if name == "lsmod" {
				return "/usr/sbin/lsmod", nil
			}
			return "", os.ErrNotExist
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "lsmod" {
				t.Fatalf("unexpected command %s", name)
			}
			return nil, context.Canceled
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})

	err := collector.collectSystemKernelModulesRuntime(context.Background(), filepath.Join(tempDir, "commands"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectSystemKernelModulesRuntime error=%v; want %v", err, context.Canceled)
	}
}

func TestCollectSystemKernelModulesRuntimePropagatesCommandDeadline(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	tempDir := t.TempDir()
	config := GetDefaultCollectorConfig()
	collector := NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, CollectorDeps{
		LookPath: func(name string) (string, error) {
			if name == "lsmod" {
				return "/usr/sbin/lsmod", nil
			}
			return "", os.ErrNotExist
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "lsmod" {
				t.Fatalf("unexpected command %s", name)
			}
			return nil, context.DeadlineExceeded
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})

	err := collector.collectSystemKernelModulesRuntime(context.Background(), filepath.Join(tempDir, "commands"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("collectSystemKernelModulesRuntime error=%v; want %v", err, context.DeadlineExceeded)
	}
}

func TestCollectBestEffortProbePropagatesCanceledContext(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{})

	collector := NewCollector(logger, GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := collector.collectBestEffortProbe(ctx, commandSpec("lsusb"), filepath.Join(t.TempDir(), "lsusb.txt"), "USB devices", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("collectBestEffortProbe error=%v; want %v", err, context.Canceled)
	}
}

func TestCollectHardwareInfoSmartctlScanBestEffort(t *testing.T) {
	var log bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&log)

	tempDir := t.TempDir()
	smartctlMarker := filepath.Join(tempDir, "smartctl")
	if err := os.WriteFile(smartctlMarker, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write smartctl marker: %v", err)
	}
	smartctlInfo, err := os.Stat(smartctlMarker)
	if err != nil {
		t.Fatalf("stat smartctl marker: %v", err)
	}
	config := GetDefaultCollectorConfig()
	calls := 0
	collector := NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, CollectorDeps{
		LookPath: func(name string) (string, error) {
			if name == "smartctl" {
				return "/usr/sbin/smartctl", nil
			}
			return "", os.ErrNotExist
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "smartctl" || len(args) != 1 || args[0] != "--scan" {
				t.Fatalf("unexpected command %s %v", name, args)
			}
			calls++
			return []byte("smartctl failed"), errors.New("smartctl failed")
		},
		Stat: func(path string) (os.FileInfo, error) {
			if strings.HasSuffix(path, "/usr/sbin/smartctl") {
				return smartctlInfo, nil
			}
			return nil, os.ErrNotExist
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})

	if err := collector.collectHardwareInfo(context.Background()); err != nil {
		t.Fatalf("collectHardwareInfo returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("smartctl calls=%d; want 1", calls)
	}
	if logger.WarningCount() != 0 {
		t.Fatalf("expected smartctl failure to stay below warning level, warnings=%d log=%s", logger.WarningCount(), log.String())
	}
	output := filepath.Join(collector.proxsaveCommandsDir("system"), "smartctl_scan.txt")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no smartctl output file on failure, stat err: %v", err)
	}
}

func TestEnsureSystemPathDeduplicates(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/usr/bin:/usr/sbin:/usr/sbin")

	ensureSystemPath()

	got := os.Getenv("PATH")
	segments := strings.Split(got, string(os.PathListSeparator))
	counts := make(map[string]int)
	for _, seg := range segments {
		counts[seg]++
		if counts[seg] > 1 {
			t.Fatalf("segment %s appears %d times in PATH %q", seg, counts[seg], got)
		}
	}
}

func TestEnsureSystemPathPreservesCustomPrefix(t *testing.T) {
	custom := "/my/custom/bin"
	t.Setenv("PATH", custom+string(os.PathListSeparator)+"/usr/bin")

	ensureSystemPath()

	got := os.Getenv("PATH")
	if !strings.HasPrefix(got, custom) {
		t.Fatalf("expected PATH %q to start with %s", got, custom)
	}
}

func TestCollectCustomPathsIgnoresEmptyEntries(t *testing.T) {
	collector := newTestCollector(t)
	collector.config.CustomBackupPaths = []string{"", "", ""}

	if err := collector.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths returned error for empty paths: %v", err)
	}
}

func TestCollectCustomPathsCopiesContent(t *testing.T) {
	collector := newTestCollector(t)
	tempDir := t.TempDir()

	customDir := filepath.Join(tempDir, "custom")
	if err := os.MkdirAll(customDir, 0o755); err != nil {
		t.Fatalf("failed to create custom dir: %v", err)
	}
	wantPath := filepath.Join(customDir, "data.txt")
	if err := os.WriteFile(wantPath, []byte("custom data"), 0o644); err != nil {
		t.Fatalf("failed to write custom file: %v", err)
	}
	collector.config.CustomBackupPaths = []string{customDir}

	if err := collector.collectCustomPaths(context.Background()); err != nil {
		t.Fatalf("collectCustomPaths failed: %v", err)
	}

	dest := filepath.Join(collector.tempDir, strings.TrimPrefix(customDir, "/"), "data.txt")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("expected copied file at %s: %v", dest, err)
	}
	if string(data) != "custom data" {
		t.Fatalf("copied file contents mismatch: %q", data)
	}
}

func TestCollectCustomPathsHonorsContext(t *testing.T) {
	collector := newTestCollector(t)
	collector.config.CustomBackupPaths = []string{"/tmp"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := collector.collectCustomPaths(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestCollectCriticalFilesExcludesFilesystemAndStorageStackFiles(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "fstab"), []byte("/dev/sda1 / ext4 defaults 0 1\n"), 0o644); err != nil {
		t.Fatalf("write fstab: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "crypttab"), []byte("crypt1 UUID=deadbeef none luks\n"), 0o600); err != nil {
		t.Fatalf("write crypttab: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "passwd"), []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}

	if err := collector.collectCriticalFiles(context.Background()); err != nil {
		t.Fatalf("collectCriticalFiles error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "crypttab")); !os.IsNotExist(err) {
		t.Fatalf("expected crypttab to be excluded from collectCriticalFiles, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "fstab")); !os.IsNotExist(err) {
		t.Fatalf("expected fstab to be excluded from collectCriticalFiles, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "etc", "passwd")); err != nil {
		t.Fatalf("expected passwd copied, got %v", err)
	}
}

func TestCollectSystemDirectoriesCopiesCommonStorageStack(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	for _, dir := range []string{
		filepath.Join(root, "etc"),
		filepath.Join(root, "etc", "keys"),
		filepath.Join(root, "etc", "iscsi", "nodes", "iqn.2026-01.test:target1", "127.0.0.1,3260,1"),
		filepath.Join(root, "etc", "multipath"),
		filepath.Join(root, "etc", "mdadm"),
		filepath.Join(root, "etc", "lvm", "backup"),
		filepath.Join(root, "etc", "lvm", "archive"),
		filepath.Join(root, "etc", "systemd", "system"),
		filepath.Join(root, "etc", "auto.master.d"),
		filepath.Join(root, "root", ".ssh"),
		filepath.Join(root, "var", "lib", "iscsi"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	writeFile := func(rel, content string, mode os.FileMode) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	writeFile("etc/fstab", "//server/share /mnt/cifs cifs credentials=/etc/cifs-creds 0 0\nsshfs#example:/ /mnt/ssh fuse.sshfs defaults,_netdev,IdentityFile=/root/.ssh/id_rsa 0 0\n", 0o644)
	writeFile("etc/crypttab", "crypt1 UUID=deadbeef /etc/keys/crypt1.key luks\n", 0o600)
	writeFile("etc/keys/crypt1.key", "keydata\n", 0o600)
	writeFile("etc/cifs-creds", "username=alice\npassword=secret\n", 0o600)
	writeFile("root/.ssh/id_rsa", "PRIVATEKEY\n", 0o600)
	writeFile("etc/iscsi/nodes/iqn.2026-01.test:target1/127.0.0.1,3260,1/default", "node.session.auth.password = secret\n", 0o600)
	writeFile("var/lib/iscsi/example.txt", "state\n", 0o600)
	writeFile("etc/multipath.conf", "defaults {}\n", 0o644)
	writeFile("etc/multipath/bindings", "mpatha 3600...\n", 0o600)
	writeFile("etc/mdadm/mdadm.conf", "ARRAY /dev/md0 UUID=deadbeef\n", 0o644)
	writeFile("etc/lvm/backup/vg0", "backup\n", 0o600)
	writeFile("etc/lvm/archive/vg0_00001.vg", "archive\n", 0o600)
	writeFile("etc/systemd/system/mnt-storage.mount", "[Mount]\nWhat=server:/export\nWhere=/mnt/storage\nType=nfs\n", 0o644)
	writeFile("etc/auto.master", "/- /etc/auto.pbs\n", 0o644)
	writeFile("etc/autofs.conf", "TIMEOUT=60\n", 0o644)
	writeFile("etc/auto.pbs", "/mnt/autofs -fstype=nfs4 server:/export\n", 0o644)

	runSelectedBricksForTest(t, context.Background(), collector, newSystemRecipe(), nil,
		brickCommonFilesystemFstab,
		brickCommonStorageStackCrypttab,
		brickCommonStorageStackISCSISnapshot,
		brickCommonStorageStackMultipathSnapshot,
		brickCommonStorageStackMDADMSnapshot,
		brickCommonStorageStackLVMSnapshot,
		brickCommonStorageStackMountUnitsSnapshot,
		brickCommonStorageStackAutofsSnapshot,
		brickCommonStorageStackReferencedFiles,
	)

	for _, rel := range []string{
		"etc/fstab",
		"etc/crypttab",
		"etc/keys/crypt1.key",
		"etc/cifs-creds",
		"root/.ssh/id_rsa",
		"etc/iscsi/nodes/iqn.2026-01.test:target1/127.0.0.1,3260,1/default",
		"var/lib/iscsi/example.txt",
		"etc/multipath.conf",
		"etc/multipath/bindings",
		"etc/mdadm/mdadm.conf",
		"etc/lvm/backup/vg0",
		"etc/lvm/archive/vg0_00001.vg",
		"etc/systemd/system/mnt-storage.mount",
		"etc/auto.master",
		"etc/autofs.conf",
		"etc/auto.pbs",
	} {
		if _, err := os.Stat(filepath.Join(collector.tempDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected copied file %s: %v", rel, err)
		}
	}
}

func TestCollectSSHKeysCopiesEtcSSH(t *testing.T) {
	collector := newTestCollector(t)

	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	srcDir := filepath.Join(root, "etc", "ssh")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("failed to create fake /etc/ssh: %v", err)
	}
	configPath := filepath.Join(srcDir, "sshd_config")
	if err := os.WriteFile(configPath, []byte("Port 22\n"), 0o600); err != nil {
		t.Fatalf("failed to write sshd_config: %v", err)
	}

	if err := collector.collectSSHKeys(context.Background()); err != nil {
		t.Fatalf("collectSSHKeys failed: %v", err)
	}

	destPath := filepath.Join(collector.tempDir, "etc", "ssh", "sshd_config")
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("expected sshd_config copied, got error: %v", err)
	}
	if string(got) != "Port 22\n" {
		t.Fatalf("copied sshd_config mismatch: %q", string(got))
	}
}

func TestCollectRootHomeSkipsSSHKeysWhenDisabled(t *testing.T) {
	collector := newTestCollector(t)

	root := t.TempDir()
	collector.config.SystemRootPrefix = root
	collector.config.BackupSSHKeys = false

	sshDir := filepath.Join(root, "root", ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir /root/.ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("key"), 0o600); err != nil {
		t.Fatalf("write id_rsa: %v", err)
	}

	if err := collector.collectRootHome(context.Background()); err != nil {
		t.Fatalf("collectRootHome failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "root", ".ssh")); err == nil {
		t.Fatalf("expected /root/.ssh excluded when BACKUP_SSH_KEYS=false")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat /root/.ssh: %v", err)
	}
}

func TestCollectUserHomesSkipsSSHKeysWhenDisabled(t *testing.T) {
	collector := newTestCollector(t)

	root := t.TempDir()
	collector.config.SystemRootPrefix = root
	collector.config.BackupSSHKeys = false

	userHome := filepath.Join(root, "home", "alice")
	if err := os.MkdirAll(filepath.Join(userHome, ".ssh"), 0o755); err != nil {
		t.Fatalf("mkdir alice .ssh: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userHome, ".ssh", "id_rsa"), []byte("key"), 0o600); err != nil {
		t.Fatalf("write alice id_rsa: %v", err)
	}
	if err := os.WriteFile(filepath.Join(userHome, "note.txt"), []byte("note"), 0o644); err != nil {
		t.Fatalf("write note.txt: %v", err)
	}

	if err := collector.collectUserHomes(context.Background()); err != nil {
		t.Fatalf("collectUserHomes failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(collector.tempDir, "home", "alice", "note.txt")); err != nil {
		t.Fatalf("expected note.txt copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(collector.tempDir, "home", "alice", ".ssh")); err == nil {
		t.Fatalf("expected alice .ssh excluded when BACKUP_SSH_KEYS=false")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat alice .ssh: %v", err)
	}
}

func TestWriteReportFileCreatesDirectories(t *testing.T) {
	collector := newTestCollector(t)
	report := filepath.Join(collector.tempDir, "reports", "test", "report.txt")

	content := []byte("hello report\nsecond line\n")
	if err := collector.writeReportFile(report, content); err != nil {
		t.Fatalf("writeReportFile failed: %v", err)
	}

	got, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("failed to read report file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("report content mismatch: got %q want %q", got, content)
	}
}

func TestWriteReportFileIncrementsFilesFailedOnEnsureDirError(t *testing.T) {
	collector := newTestCollector(t)

	// Block directory creation by placing a regular file where a directory is expected.
	blocker := filepath.Join(collector.tempDir, "reports")
	if err := os.WriteFile(blocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write blocker file: %v", err)
	}

	report := filepath.Join(blocker, "nested", "report.txt")
	if err := collector.writeReportFile(report, []byte("data")); err == nil {
		t.Fatalf("expected writeReportFile to fail when parent path is a file")
	}

	if _, err := os.Stat(report); err == nil {
		t.Fatalf("expected no report file to be created")
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

func TestWriteReportFileIncrementsFilesFailedOnWriteError(t *testing.T) {
	collector := newTestCollector(t)

	parent := filepath.Join(collector.tempDir, "reports", "test")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	// Force os.WriteFile to fail deterministically by making the report path a directory.
	report := filepath.Join(parent, "report.txt")
	if err := os.MkdirAll(report, 0o755); err != nil {
		t.Fatalf("mkdir report dir: %v", err)
	}

	if err := collector.writeReportFile(report, []byte("data")); err == nil {
		t.Fatalf("expected writeReportFile to fail when report path is a directory")
	}

	stats := collector.GetStats()
	if stats.FilesFailed != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", stats.FilesFailed)
	}
}

// TestWriteReportFileRejectsPathEscapingRoot exercises the gosec G703
// path-traversal containment added to writeReportFile: report paths that
// resolve outside the collector staging root (c.tempDir) must be refused, both
// for a lexical ".." escape (reportRelPath) and for a symlinked component that
// only escapes once resolved (os.Root). In both cases no file may be written
// outside the root and FilesFailed must be incremented.
func TestWriteReportFileRejectsPathEscapingRoot(t *testing.T) {
	t.Run("lexical parent-dir escape is rejected", func(t *testing.T) {
		collector := newTestCollector(t)

		escape := filepath.Join(collector.tempDir, "..", "escaped-report.txt")
		if err := collector.writeReportFile(escape, []byte("payload")); err == nil {
			t.Fatalf("expected writeReportFile to reject path escaping collector root")
		}
		if _, err := os.Stat(escape); !os.IsNotExist(err) {
			t.Fatalf("escaping report must not be created, stat err=%v", err)
		}
		if stats := collector.GetStats(); stats.FilesFailed != 1 {
			t.Fatalf("expected FilesFailed=1 after escape rejection, got %d", stats.FilesFailed)
		}
	})

	t.Run("symlinked component escape is rejected", func(t *testing.T) {
		collector := newTestCollector(t)

		// A directory outside the collector root, reached through a symlink that
		// lives inside the root. reportRelPath sees a clean in-root path, so the
		// os.Root write is what must refuse to follow the link out of the root.
		outside := t.TempDir()
		link := filepath.Join(collector.tempDir, "evil")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("create escaping symlink: %v", err)
		}

		report := filepath.Join(link, "report.txt")
		if err := collector.writeReportFile(report, []byte("payload")); err == nil {
			t.Fatalf("expected writeReportFile to reject write through escaping symlink")
		}
		if _, err := os.Stat(filepath.Join(outside, "report.txt")); !os.IsNotExist(err) {
			t.Fatalf("report must not be written outside the root, stat err=%v", err)
		}
	})
}

func TestWriteReportFileDryRun(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	collector := NewCollector(logger, config, tempDir, types.ProxmoxUnknown, true)

	report := filepath.Join(tempDir, "report.txt")
	if err := collector.writeReportFile(report, []byte("dry run")); err != nil {
		t.Fatalf("writeReportFile dry-run failed: %v", err)
	}
	if _, err := os.Stat(report); !os.IsNotExist(err) {
		t.Fatalf("expected no file created in dry-run, got err=%v", err)
	}
}

func TestSanitizeFilename(t *testing.T) {
	testCases := []struct {
		name     string
		expected string
	}{
		{"normal_file.txt", "normal_file.txt"},
		{"file with spaces.txt", "file with spaces.txt"},
		{"user@domain.com", "user_domain.com"},
		{"path/to/file", "path_to_file"},
		{"special:chars*here?", "special_chars*here?"},
		{"", "entry"},
	}

	for _, tc := range testCases {
		if got := sanitizeFilename(tc.name); got != tc.expected {
			t.Fatalf("sanitizeFilename(%q) = %q, want %q", tc.name, got, tc.expected)
		}
	}
}

func TestCollectSystemDirectoriesCopiesAltNetConfigsAndLeases(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	// Alternate network configs
	netplanDir := filepath.Join(root, "etc", "netplan")
	systemdNetDir := filepath.Join(root, "etc", "systemd", "network")
	nmDir := filepath.Join(root, "etc", "NetworkManager", "system-connections")
	for _, dir := range []string{netplanDir, systemdNetDir, nmDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(netplanDir, "01-netcfg.yaml"), []byte("network: {}\n"), 0o644); err != nil {
		t.Fatalf("failed to write netplan file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(systemdNetDir, "10-eth0.network"), []byte("[Match]\nName=eth0\n"), 0o644); err != nil {
		t.Fatalf("failed to write systemd-networkd file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nmDir, "conn.nmconnection"), []byte("[connection]\nid=test\n"), 0o600); err != nil {
		t.Fatalf("failed to write NetworkManager file: %v", err)
	}

	// DHCP leases
	dhcpDirs := []string{
		filepath.Join(root, "var", "lib", "dhcp"),
		filepath.Join(root, "var", "lib", "NetworkManager"),
		filepath.Join(root, "run", "systemd", "netif", "leases"),
	}
	for _, dir := range dhcpDirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("failed to create lease dir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "lease.test"), []byte("lease"), 0o644); err != nil {
			t.Fatalf("failed to write lease in %s: %v", dir, err)
		}
	}

	runSelectedBricksForTest(t, context.Background(), collector, newSystemRecipe(), nil,
		brickSystemNetworkStatic,
		brickSystemRuntimeLeases,
	)

	paths := []string{
		filepath.Join(collector.tempDir, "etc", "netplan", "01-netcfg.yaml"),
		filepath.Join(collector.tempDir, "etc", "systemd", "network", "10-eth0.network"),
		filepath.Join(collector.tempDir, "etc", "NetworkManager", "system-connections", "conn.nmconnection"),
		filepath.Join(collector.tempDir, "var", "lib", "proxsave-info", "runtime", "var", "lib", "dhcp", "lease.test"),
		filepath.Join(collector.tempDir, "var", "lib", "proxsave-info", "runtime", "var", "lib", "NetworkManager", "lease.test"),
		filepath.Join(collector.tempDir, "var", "lib", "proxsave-info", "runtime", "run", "systemd", "netif", "leases", "lease.test"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected copied file %s: %v", p, err)
		}
	}
}

func TestBuildNetworkReportAggregatesOutputs(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	// Config files
	netDir := filepath.Join(root, "etc", "network")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("failed to create %s: %v", netDir, err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "interfaces"), []byte("auto lo\niface lo inet loopback\n"), 0o644); err != nil {
		t.Fatalf("failed to write interfaces: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("failed to create /etc in root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "resolv.conf"), []byte("nameserver 1.1.1.1\n"), 0o644); err != nil {
		t.Fatalf("failed to write resolv.conf: %v", err)
	}

	commandsDir := filepath.Join(collector.tempDir, "var/lib/proxsave-info", "commands", "system")
	if err := os.MkdirAll(commandsDir, 0o755); err != nil {
		t.Fatalf("failed to create dir %s: %v", commandsDir, err)
	}

	writeCmd := func(name, content string) {
		if err := os.WriteFile(filepath.Join(commandsDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	writeCmd("ip_addr.txt", "1: lo: <LOOPBACK>\n")
	writeCmd("ip_route.txt", "default via 192.0.2.1 dev eth0\n")
	writeCmd("ip_rule.txt", "0:	from all lookup local\n")
	writeCmd("ip_route_all_v4.txt", "local 127.0.0.0/8 dev lo\n")
	writeCmd("iptables_nat.txt", "PREROUTING\n")
	writeCmd("iptables.txt", "*nat\nCOMMIT\n")
	writeCmd("nftables.txt", "table inet filter {}\n")
	writeCmd("ufw_status.txt", "Status: inactive\n")
	writeCmd("bridge_link.txt", "2: br0: <BROADCAST>\n")
	if err := os.WriteFile(filepath.Join(commandsDir, "bonding_eth0.txt"), []byte("Bonding Mode: active-backup\n"), 0o644); err != nil {
		t.Fatalf("failed to write bonding status: %v", err)
	}

	if err := collector.buildNetworkReport(context.Background(), commandsDir); err != nil {
		t.Fatalf("buildNetworkReport failed: %v", err)
	}

	reportPath := filepath.Join(commandsDir, "network_report.txt")
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("expected network_report.txt: %v", err)
	}
	text := string(report)
	for _, want := range []string{"Proxsave Network Report", "ip_addr", "default via", "nameserver 1.1.1.1", "Bonding Mode"} {
		if !strings.Contains(text, want) {
			t.Fatalf("network report missing %q in:\n%s", want, text)
		}
	}

	// Report is written only to the primary directory (no secondary mirror).
}

func TestSystemProbeAvailabilityBranches(t *testing.T) {
	t.Run("systemctl runtime directory", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		if err := os.MkdirAll(filepath.Join(root, "run", "systemd", "system"), 0o755); err != nil {
			t.Fatalf("mkdir systemd runtime: %v", err)
		}
		if ok, reason := collector.systemctlProbeAvailable(); !ok || reason != "" {
			t.Fatalf("systemctlProbeAvailable() = %t, %q; want true, empty reason", ok, reason)
		}
	})

	t.Run("systemctl proc comm", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		if err := os.MkdirAll(filepath.Join(root, "proc", "1"), 0o755); err != nil {
			t.Fatalf("mkdir proc/1: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "proc", "1", "comm"), []byte("systemd\n"), 0o644); err != nil {
			t.Fatalf("write comm: %v", err)
		}
		if ok, reason := collector.systemctlProbeAvailable(); !ok || reason != "" {
			t.Fatalf("systemctlProbeAvailable() = %t, %q; want true, empty reason", ok, reason)
		}
	})

	t.Run("systemctl container reason", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			DetectUnprivilegedContainer: func() (bool, string) { return true, "lxc test" },
		})
		collector.config.SystemRootPrefix = t.TempDir()
		ok, reason := collector.systemctlProbeAvailable()
		if ok || !strings.Contains(reason, "lxc test") {
			t.Fatalf("systemctlProbeAvailable() = %t, %q; want false with container detail", ok, reason)
		}
	})

	t.Run("systemctl generic unavailable", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
		})
		collector.config.SystemRootPrefix = t.TempDir()
		ok, reason := collector.systemctlProbeAvailable()
		if ok || reason != "systemd runtime not detected" {
			t.Fatalf("systemctlProbeAvailable() = %t, %q; want generic unavailable", ok, reason)
		}
	})

	t.Run("sensors availability", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		if ok, reason := collector.sensorsProbeAvailable(); ok || reason == "" {
			t.Fatalf("sensorsProbeAvailable() = %t, %q; want false with reason", ok, reason)
		}
		if err := os.MkdirAll(filepath.Join(root, "sys", "class", "hwmon"), 0o755); err != nil {
			t.Fatalf("mkdir hwmon: %v", err)
		}
		if ok, reason := collector.sensorsProbeAvailable(); !ok || reason != "" {
			t.Fatalf("sensorsProbeAvailable() = %t, %q; want true", ok, reason)
		}
	})

	t.Run("dmidecode availability", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root

		if os.Geteuid() != 0 {
			ok, reason := collector.dmidecodeProbeAvailable()
			if ok || !strings.Contains(reason, "requires root") {
				t.Fatalf("dmidecodeProbeAvailable() = %t, %q; want non-root reason", ok, reason)
			}
			return
		}

		if ok, reason := collector.dmidecodeProbeAvailable(); ok || reason != "DMI tables not accessible" {
			t.Fatalf("dmidecodeProbeAvailable() = %t, %q; want inaccessible", ok, reason)
		}
		if err := os.MkdirAll(filepath.Join(root, "sys", "firmware", "dmi", "tables"), 0o755); err != nil {
			t.Fatalf("mkdir dmi tables: %v", err)
		}
		if ok, reason := collector.dmidecodeProbeAvailable(); !ok || reason != "" {
			t.Fatalf("dmidecodeProbeAvailable() tables = %t, %q; want true", ok, reason)
		}

		collector = newTestCollector(t)
		root = t.TempDir()
		collector.config.SystemRootPrefix = root
		if err := os.MkdirAll(filepath.Join(root, "dev"), 0o755); err != nil {
			t.Fatalf("mkdir dev: %v", err)
		}
		if err := os.WriteFile(filepath.Join(root, "dev", "mem"), []byte("mem"), 0o600); err != nil {
			t.Fatalf("write dev mem: %v", err)
		}
		if ok, reason := collector.dmidecodeProbeAvailable(); !ok || reason != "" {
			t.Fatalf("dmidecodeProbeAvailable() devmem = %t, %q; want true", ok, reason)
		}
	})
}

func TestDetectZFSUsageIndicators(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	writeRootFile(t, root, "proc/mounts", "tank/ds /tank zfs rw 0 0\n", 0o644)
	writeRootFile(t, root, "etc/zfs/zpool.cache", "cache", 0o600)
	writeRootFile(t, root, "etc/fstab", "# tank /old zfs defaults 0 0\ntank/home /home zfs defaults 0 0\n", 0o644)
	writeRootFile(t, root, "etc/pve/storage.cfg", "zfspool: fast\n\tpool tank\n", 0o644)

	ok, indicators := collector.detectZFSUsage()
	if !ok {
		t.Fatalf("detectZFSUsage() ok=false, indicators=%q", indicators)
	}
	for _, want := range []string{"mounted_zfs", "zpool_cache", "fstab_zfs", "pve_storage_zfspool"} {
		if !strings.Contains(indicators, want) {
			t.Fatalf("detectZFSUsage indicators %q missing %s", indicators, want)
		}
	}

	collector = newTestCollector(t)
	collector.config.SystemRootPrefix = t.TempDir()
	ok, indicators = collector.detectZFSUsage()
	if ok || indicators != "none" {
		t.Fatalf("detectZFSUsage() = %t, %q; want false, none", ok, indicators)
	}
}

func TestCollectBestEffortProbeBranches(t *testing.T) {
	t.Run("missing command skips", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		})
		err := collector.collectBestEffortProbe(context.Background(), commandSpec("missing"), filepath.Join(t.TempDir(), "out.txt"), "missing", nil)
		if err != nil {
			t.Fatalf("collectBestEffortProbe missing command = %v", err)
		}
	})

	t.Run("lookpath cancellation wins", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) {
				cancel()
				return "", os.ErrNotExist
			},
		})
		err := collector.collectBestEffortProbe(ctx, commandSpec("missing"), filepath.Join(t.TempDir(), "out.txt"), "missing", nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("collectBestEffortProbe error=%v; want context.Canceled", err)
		}
	})

	t.Run("availability false skips command", func(t *testing.T) {
		calls := 0
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) { return "/bin/tool", nil },
			RunCommand: func(context.Context, string, ...string) ([]byte, error) {
				calls++
				return []byte("unexpected"), nil
			},
		})
		err := collector.collectBestEffortProbe(context.Background(), commandSpec("tool"), filepath.Join(t.TempDir(), "out.txt"), "tool", func() (bool, string) {
			return false, ""
		})
		if err != nil {
			t.Fatalf("collectBestEffortProbe unavailable = %v", err)
		}
		if calls != 0 {
			t.Fatalf("RunCommand calls=%d; want 0", calls)
		}
	})

	t.Run("availability cancellation wins", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) { return "/bin/tool", nil },
		})
		err := collector.collectBestEffortProbe(ctx, commandSpec("tool"), filepath.Join(t.TempDir(), "out.txt"), "tool", func() (bool, string) {
			cancel()
			return true, ""
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("collectBestEffortProbe error=%v; want context.Canceled", err)
		}
	})

	t.Run("write failure is best effort", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath:   func(string) (string, error) { return "/bin/tool", nil },
			RunCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte("ok"), nil },
		})
		blocker := filepath.Join(t.TempDir(), "blocked")
		if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
			t.Fatalf("write blocker: %v", err)
		}
		err := collector.collectBestEffortProbe(context.Background(), commandSpec("tool"), filepath.Join(blocker, "out.txt"), "tool", nil)
		if err != nil {
			t.Fatalf("collectBestEffortProbe write failure should be swallowed, got %v", err)
		}
	})
}

func TestCollectSystemStaticCopiesEnabledFiles(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	for _, item := range []struct {
		rel     string
		content string
		mode    os.FileMode
	}{
		{"etc/network/interfaces", "auto lo\n", 0o644},
		{"etc/network/interfaces.d/vmbr0", "iface vmbr0 inet static\n", 0o644},
		{"etc/cloud/cloud.cfg.d/99-disable-network-config.cfg", "network: {config: disabled}\n", 0o644},
		{"etc/dnsmasq.d/lxc-vmbr1.conf", "dhcp-range=1.2.3.4\n", 0o644},
		{"etc/netplan/01-netcfg.yaml", "network: {}\n", 0o644},
		{"etc/systemd/network/10-eth0.network", "[Match]\nName=eth0\n", 0o644},
		{"etc/NetworkManager/system-connections/test.nmconnection", "[connection]\n", 0o600},
		{"etc/hostname", "node1\n", 0o644},
		{"etc/hosts", "127.0.0.1 localhost\n", 0o644},
		{"etc/resolv.conf", "nameserver 1.1.1.1\n", 0o644},
		{"etc/timezone", "UTC\n", 0o644},
		{"etc/apt/sources.list", "deb http://example stable main\n", 0o644},
		{"etc/apt/sources.list.d/vendor.list", "deb http://vendor stable main\n", 0o644},
		{"etc/apt/preferences", "Package: *\n", 0o644},
		{"etc/apt/preferences.d/pin", "Pin: release *\n", 0o644},
		{"etc/apt/trusted.gpg.d/vendor.gpg", "key", 0o644},
		{"etc/apt/apt.conf.d/99local", "APT::Get::Assume-Yes false;\n", 0o644},
		{"etc/apt/auth.conf.d/private.conf", "machine example\n", 0o600},
		{"etc/apt/keyrings/vendor.gpg", "key", 0o644},
		{"etc/apt/listchanges.conf", "[apt]\n", 0o644},
		{"etc/apt/listchanges.conf.d/local.conf", "[local]\n", 0o644},
		{"etc/crontab", "* * * * * root true\n", 0o644},
		{"etc/cron.d/job", "* * * * * root true\n", 0o644},
		{"etc/cron.daily/job", "#!/bin/sh\n", 0o755},
		{"etc/cron.hourly/job", "#!/bin/sh\n", 0o755},
		{"etc/cron.monthly/job", "#!/bin/sh\n", 0o755},
		{"etc/cron.weekly/job", "#!/bin/sh\n", 0o755},
		{"var/spool/cron/crontabs/root", "* * * * * true\n", 0o600},
		{"etc/systemd/system/example.service", "[Service]\n", 0o644},
		{"etc/logrotate.d/app", "/var/log/app.log {}\n", 0o644},
		{"etc/ssl/certs/cert.pem", "cert", 0o644},
		{"etc/ssl/private/key.pem", "key", 0o600},
		{"etc/ssl/openssl.cnf", "openssl_conf = default\n", 0o644},
		{"etc/sysctl.conf", "net.ipv4.ip_forward=0\n", 0o644},
		{"etc/sysctl.d/99.conf", "vm.swappiness=1\n", 0o644},
		{"etc/modules", "loop\n", 0o644},
		{"etc/modprobe.d/local.conf", "options loop max_loop=8\n", 0o644},
		{"etc/zfs/zpool.cache", "cache", 0o600},
		{"etc/hostid", "hostid", 0o644},
		{"etc/iptables/rules.v4", "*filter\n", 0o644},
		{"etc/nftables.d/filter.nft", "table inet filter {}\n", 0o644},
		{"etc/nftables.conf", "include \"/etc/nftables.d/*.nft\"\n", 0o644},
	} {
		writeRootFile(t, root, item.rel, item.content, item.mode)
	}

	for name, fn := range map[string]func(context.Context) error{
		"network":        collector.collectSystemNetworkStatic,
		"identity":       collector.collectSystemIdentityStatic,
		"apt":            collector.collectSystemAptStatic,
		"cron":           collector.collectSystemCronStatic,
		"services":       collector.collectSystemServicesStatic,
		"logging":        collector.collectSystemLoggingStatic,
		"ssl":            collector.collectSystemSSLStatic,
		"sysctl":         collector.collectSystemSysctlStatic,
		"kernel_modules": collector.collectSystemKernelModuleStatic,
		"zfs":            collector.collectSystemZFSStatic,
		"firewall":       collector.collectSystemFirewallStatic,
	} {
		if err := fn(context.Background()); err != nil {
			t.Fatalf("%s static collection failed: %v", name, err)
		}
	}

	for _, rel := range []string{
		"etc/network/interfaces",
		"etc/network/interfaces.d/vmbr0",
		"etc/cloud/cloud.cfg.d/99-disable-network-config.cfg",
		"etc/dnsmasq.d/lxc-vmbr1.conf",
		"etc/netplan/01-netcfg.yaml",
		"etc/systemd/network/10-eth0.network",
		"etc/NetworkManager/system-connections/test.nmconnection",
		"etc/hostname",
		"etc/hosts",
		"etc/resolv.conf",
		"etc/timezone",
		"etc/apt/sources.list",
		"etc/apt/sources.list.d/vendor.list",
		"etc/apt/preferences",
		"etc/apt/preferences.d/pin",
		"etc/apt/trusted.gpg.d/vendor.gpg",
		"etc/apt/apt.conf.d/99local",
		"etc/apt/auth.conf.d/private.conf",
		"etc/apt/keyrings/vendor.gpg",
		"etc/apt/listchanges.conf",
		"etc/apt/listchanges.conf.d/local.conf",
		"etc/crontab",
		"etc/cron.d/job",
		"etc/cron.daily/job",
		"etc/cron.hourly/job",
		"etc/cron.monthly/job",
		"etc/cron.weekly/job",
		"var/spool/cron/crontabs/root",
		"etc/systemd/system/example.service",
		"etc/logrotate.d/app",
		"etc/ssl/certs/cert.pem",
		"etc/ssl/private/key.pem",
		"etc/ssl/openssl.cnf",
		"etc/sysctl.conf",
		"etc/sysctl.d/99.conf",
		"etc/modules",
		"etc/modprobe.d/local.conf",
		"etc/zfs/zpool.cache",
		"etc/hostid",
		"etc/iptables/rules.v4",
		"etc/nftables.d/filter.nft",
		"etc/nftables.conf",
	} {
		assertFileExists(t, filepath.Join(collector.tempDir, filepath.FromSlash(rel)))
	}
}

func TestCollectSystemStaticDisabledAndCanceledBranches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(*Collector)
		fn        func(*Collector, context.Context) error
		wantRel   string
	}{
		{
			name:      "network",
			configure: func(c *Collector) { c.config.BackupNetworkConfigs = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemNetworkStatic(ctx) },
			wantRel:   "etc/network/interfaces",
		},
		{
			name:      "apt",
			configure: func(c *Collector) { c.config.BackupAptSources = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemAptStatic(ctx) },
			wantRel:   "etc/apt/sources.list",
		},
		{
			name:      "cron",
			configure: func(c *Collector) { c.config.BackupCronJobs = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemCronStatic(ctx) },
			wantRel:   "etc/crontab",
		},
		{
			name:      "services",
			configure: func(c *Collector) { c.config.BackupSystemdServices = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemServicesStatic(ctx) },
			wantRel:   "etc/systemd/system/example.service",
		},
		{
			name:      "ssl",
			configure: func(c *Collector) { c.config.BackupSSLCerts = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemSSLStatic(ctx) },
			wantRel:   "etc/ssl/openssl.cnf",
		},
		{
			name:      "sysctl",
			configure: func(c *Collector) { c.config.BackupSysctlConfig = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemSysctlStatic(ctx) },
			wantRel:   "etc/sysctl.conf",
		},
		{
			name:      "kernel modules",
			configure: func(c *Collector) { c.config.BackupKernelModules = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemKernelModuleStatic(ctx) },
			wantRel:   "etc/modules",
		},
		{
			name:      "zfs",
			configure: func(c *Collector) { c.config.BackupZFSConfig = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemZFSStatic(ctx) },
			wantRel:   "etc/hostid",
		},
		{
			name:      "firewall",
			configure: func(c *Collector) { c.config.BackupFirewallRules = false },
			fn:        func(c *Collector, ctx context.Context) error { return c.collectSystemFirewallStatic(ctx) },
			wantRel:   "etc/nftables.conf",
		},
	} {
		t.Run(tc.name+"_disabled", func(t *testing.T) {
			collector := newTestCollector(t)
			root := t.TempDir()
			collector.config.SystemRootPrefix = root
			writeRootFile(t, root, tc.wantRel, "data\n", 0o644)
			tc.configure(collector)
			if err := tc.fn(collector, context.Background()); err != nil {
				t.Fatalf("disabled branch returned error: %v", err)
			}
			if _, err := os.Stat(filepath.Join(collector.tempDir, filepath.FromSlash(tc.wantRel))); !os.IsNotExist(err) {
				t.Fatalf("expected disabled branch not to copy %s, stat err=%v", tc.wantRel, err)
			}
		})

		t.Run(tc.name+"_canceled", func(t *testing.T) {
			collector := newTestCollector(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := tc.fn(collector, ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled branch error=%v; want context.Canceled", err)
			}
		})
	}
}

func TestCollectSystemStaticMissingSourceBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*Collector, context.Context) error
	}{
		{name: "network", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemNetworkStatic(ctx) }},
		{name: "identity", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemIdentityStatic(ctx) }},
		{name: "apt", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemAptStatic(ctx) }},
		{name: "cron", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemCronStatic(ctx) }},
		{name: "services", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemServicesStatic(ctx) }},
		{name: "logging", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemLoggingStatic(ctx) }},
		{name: "ssl", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemSSLStatic(ctx) }},
		{name: "sysctl", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemSysctlStatic(ctx) }},
		{name: "kernel modules", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemKernelModuleStatic(ctx) }},
		{name: "zfs", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemZFSStatic(ctx) }},
		{name: "firewall", fn: func(c *Collector, ctx context.Context) error { return c.collectSystemFirewallStatic(ctx) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTestCollector(t)
			collector.config.SystemRootPrefix = t.TempDir()
			if err := tc.fn(collector, context.Background()); err != nil {
				t.Fatalf("missing static sources should be tolerated: %v", err)
			}
		})
	}
}

func TestCollectSystemStaticCopyErrorBranches(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	for _, path := range []struct {
		rel string
		dir bool
	}{
		{rel: "etc/network/interfaces"},
		{rel: "etc/network/interfaces.d", dir: true},
		{rel: "etc/cloud/cloud.cfg.d/99-disable-network-config.cfg"},
		{rel: "etc/dnsmasq.d/lxc-vmbr1.conf"},
		{rel: "etc/netplan", dir: true},
		{rel: "etc/systemd/network", dir: true},
		{rel: "etc/NetworkManager/system-connections", dir: true},
		{rel: "etc/hostname"},
		{rel: "etc/hosts"},
		{rel: "etc/resolv.conf"},
		{rel: "etc/timezone"},
		{rel: "etc/apt/sources.list"},
		{rel: "etc/apt/sources.list.d", dir: true},
		{rel: "etc/apt/preferences"},
		{rel: "etc/apt/preferences.d", dir: true},
		{rel: "etc/apt/trusted.gpg.d", dir: true},
		{rel: "etc/apt/apt.conf.d", dir: true},
		{rel: "etc/apt/auth.conf.d", dir: true},
		{rel: "etc/apt/keyrings", dir: true},
		{rel: "etc/apt/listchanges.conf"},
		{rel: "etc/apt/listchanges.conf.d", dir: true},
		{rel: "etc/crontab"},
		{rel: "etc/cron.d", dir: true},
		{rel: "etc/cron.daily", dir: true},
		{rel: "etc/cron.hourly", dir: true},
		{rel: "etc/cron.monthly", dir: true},
		{rel: "etc/cron.weekly", dir: true},
		{rel: "var/spool/cron/crontabs", dir: true},
		{rel: "etc/systemd/system", dir: true},
		{rel: "etc/logrotate.d", dir: true},
		{rel: "etc/ssl/certs", dir: true},
		{rel: "etc/ssl/private", dir: true},
		{rel: "etc/ssl/openssl.cnf"},
		{rel: "etc/sysctl.conf"},
		{rel: "etc/sysctl.d", dir: true},
		{rel: "etc/modules"},
		{rel: "etc/modprobe.d", dir: true},
		{rel: "etc/zfs", dir: true},
		{rel: "etc/hostid"},
		{rel: "etc/iptables", dir: true},
		{rel: "etc/nftables.d", dir: true},
		{rel: "etc/nftables.conf"},
	} {
		prepareStaticCopyErrorPath(t, collector, root, path.rel, path.dir)
	}

	for name, fn := range map[string]func(context.Context) error{
		"network":        collector.collectSystemNetworkStatic,
		"identity":       collector.collectSystemIdentityStatic,
		"apt":            collector.collectSystemAptStatic,
		"cron":           collector.collectSystemCronStatic,
		"services":       collector.collectSystemServicesStatic,
		"logging":        collector.collectSystemLoggingStatic,
		"ssl":            collector.collectSystemSSLStatic,
		"sysctl":         collector.collectSystemSysctlStatic,
		"kernel_modules": collector.collectSystemKernelModuleStatic,
		"zfs":            collector.collectSystemZFSStatic,
		"firewall":       collector.collectSystemFirewallStatic,
	} {
		if err := fn(context.Background()); err != nil {
			t.Fatalf("%s static copy error branches should be tolerated: %v", name, err)
		}
	}
}

func TestCollectSystemRuntimeBranchCoverage(t *testing.T) {
	t.Run("zfs disabled", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.BackupZFSConfig = false
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemZFSRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemZFSRuntime disabled: %v", err)
		}
		if _, err := os.Stat(filepath.Join(commandsDir, "zfs")); !os.IsNotExist(err) {
			t.Fatalf("expected zfs dir absent when disabled, stat err=%v", err)
		}
	})

	t.Run("zfs detected and command outputs", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "zpool", "zfs":
					return "/usr/sbin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + " " + strings.Join(args, " ") + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/fstab", "tank/home /home zfs defaults 0 0\n", 0o644)
		commandsDir := scratchUnderRoot(t, collector)

		if err := collector.collectSystemZFSRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemZFSRuntime detected: %v", err)
		}
		for _, name := range []string{"zpool_status.txt", "zpool_list.txt", "zfs_list.txt", "zfs_get_all.txt"} {
			assertFileExists(t, filepath.Join(commandsDir, "zfs", name))
		}
	})

	t.Run("zfs info dir blocked", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/fstab", "tank/home /home zfs defaults 0 0\n", 0o644)
		commandsDir := scratchUnderRoot(t, collector)
		if err := os.WriteFile(filepath.Join(commandsDir, "zfs"), []byte("blocker"), 0o644); err != nil {
			t.Fatalf("write zfs blocker: %v", err)
		}
		if err := collector.collectSystemZFSRuntime(context.Background(), commandsDir); err == nil {
			t.Fatalf("expected zfs dir blocker error")
		}
	})

	t.Run("lvm command matrix", func(t *testing.T) {
		calls := make(map[string]int)
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "pvs", "vgs", "lvs":
					return "/usr/sbin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls[name]++
				return []byte(name + "\n"), nil
			},
		})
		commandsDir := collector.proxsaveCommandsDir("system")
		if err := collector.collectSystemLVMRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemLVMRuntime: %v", err)
		}
		for _, name := range []string{"pvs", "vgs", "lvs"} {
			if calls[name] != 1 {
				t.Fatalf("%s calls=%d; want 1", name, calls[name])
			}
		}
		for _, name := range []string{"lvm_pvs.txt", "lvm_vgs.txt", "lvm_lvs.txt"} {
			assertFileExists(t, filepath.Join(commandsDir, name))
		}
	})

	t.Run("lvm missing commands", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		})
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemLVMRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemLVMRuntime missing commands: %v", err)
		}
		if entries, err := os.ReadDir(commandsDir); err != nil {
			t.Fatalf("read commands dir: %v", err)
		} else if len(entries) != 0 {
			t.Fatalf("expected no LVM outputs when commands missing, got %d entries", len(entries))
		}
	})
}

func TestCollectConfigFileBranches(t *testing.T) {
	t.Run("empty path skips", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.ConfigFilePath = ""
		if err := collector.collectConfigFile(context.Background()); err != nil {
			t.Fatalf("collectConfigFile empty: %v", err)
		}
	})

	t.Run("absolute path uses system root", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.ConfigFilePath = "/etc/proxsave/backup.env"
		writeRootFile(t, root, "etc/proxsave/backup.env", "BACKUP=true\n", 0o600)
		if err := collector.collectConfigFile(context.Background()); err != nil {
			t.Fatalf("collectConfigFile absolute: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.tempDir, "etc/proxsave/backup.env"))
	})

	t.Run("missing file tolerated", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.SystemRootPrefix = t.TempDir()
		collector.config.ConfigFilePath = "/etc/proxsave/missing.env"
		if err := collector.collectConfigFile(context.Background()); err != nil {
			t.Fatalf("collectConfigFile missing should be tolerated: %v", err)
		}
	})

	t.Run("copy error returned", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.ConfigFilePath = "/etc/proxsave/backup.env"
		writeRootFile(t, root, "etc/proxsave/backup.env", "BACKUP=true\n", 0o600)
		if err := os.WriteFile(filepath.Join(collector.tempDir, "etc"), []byte("dest blocker"), 0o644); err != nil {
			t.Fatalf("write destination blocker: %v", err)
		}
		if err := collector.collectConfigFile(context.Background()); err == nil {
			t.Fatalf("expected collectConfigFile to return destination copy error")
		}
	})

	t.Run("canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectConfigFile(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectConfigFile canceled=%v; want context.Canceled", err)
		}
	})
}

func TestCollectScriptRepositoryCopiesAndSkipsRuntimeDirs(t *testing.T) {
	collector := newTestCollector(t)
	repo := t.TempDir()
	collector.config.ScriptRepositoryPath = repo

	writeFileAt(t, filepath.Join(repo, "keep.sh"), "#!/bin/sh\n")
	writeFileAt(t, filepath.Join(repo, "nested", "config.env"), "A=1\n")
	writeFileAt(t, filepath.Join(repo, "backup", "skip.tar"), "backup\n")
	writeFileAt(t, filepath.Join(repo, "log", "skip.log"), "log\n")

	if err := collector.collectScriptRepository(context.Background()); err != nil {
		t.Fatalf("collectScriptRepository: %v", err)
	}

	target := collector.proxsaveInfoDir("script-repository", filepath.Base(repo))
	assertFileExists(t, filepath.Join(target, "keep.sh"))
	assertFileExists(t, filepath.Join(target, "nested", "config.env"))
	if _, err := os.Stat(filepath.Join(target, "backup", "skip.tar")); !os.IsNotExist(err) {
		t.Fatalf("expected backup dir skipped, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(target, "log", "skip.log")); !os.IsNotExist(err) {
		t.Fatalf("expected log dir skipped, stat err=%v", err)
	}
}

func TestCollectScriptRepositorySkipAndCancelBranches(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "missing", path: filepath.Join(t.TempDir(), "missing")},
		{name: "file", path: filepath.Join(t.TempDir(), "repo-file")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := newTestCollector(t)
			if tc.name == "file" {
				writeFileAt(t, tc.path, "not a dir\n")
			}
			collector.config.ScriptRepositoryPath = tc.path
			if err := collector.collectScriptRepository(context.Background()); err != nil {
				t.Fatalf("collectScriptRepository %s: %v", tc.name, err)
			}
		})
	}

	t.Run("canceled during walk", func(t *testing.T) {
		collector := newTestCollector(t)
		repo := t.TempDir()
		collector.config.ScriptRepositoryPath = repo
		writeFileAt(t, filepath.Join(repo, "file.txt"), "data\n")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectScriptRepository(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectScriptRepository canceled=%v; want context.Canceled", err)
		}
	})
}

func TestCollectSSHKeysCopiesRootAndUsers(t *testing.T) {
	collector := newTestCollector(t)
	root := t.TempDir()
	collector.config.SystemRootPrefix = root

	writeRootFile(t, root, "etc/ssh/sshd_config", "Port 22\n", 0o600)
	writeRootFile(t, root, "root/.ssh/id_rsa", "root-key\n", 0o600)
	writeRootFile(t, root, "home/alice/.ssh/id_ed25519", "alice-key\n", 0o600)
	writeRootFile(t, root, "home/bob/note.txt", "not ssh\n", 0o644)
	writeRootFile(t, root, "home/README", "top-level file\n", 0o644)

	if err := collector.collectSSHKeys(context.Background()); err != nil {
		t.Fatalf("collectSSHKeys: %v", err)
	}

	assertFileExists(t, filepath.Join(collector.tempDir, "etc/ssh/sshd_config"))
	assertFileExists(t, filepath.Join(collector.tempDir, "root/.ssh/id_rsa"))
	assertFileExists(t, filepath.Join(collector.tempDir, "home/alice/.ssh/id_ed25519"))
	if _, err := os.Stat(filepath.Join(collector.tempDir, "home/bob/.ssh")); !os.IsNotExist(err) {
		t.Fatalf("expected bob .ssh absent, stat err=%v", err)
	}
}

func TestCollectSSHKeysMissingAndCanceledBranches(t *testing.T) {
	t.Run("canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectSSHKeys(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSSHKeys canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("missing sources are tolerated", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.SystemRootPrefix = t.TempDir()
		if err := collector.collectSSHKeys(context.Background()); err != nil {
			t.Fatalf("collectSSHKeys missing sources: %v", err)
		}
		if _, err := os.Stat(filepath.Join(collector.tempDir, "etc/ssh")); !os.IsNotExist(err) {
			t.Fatalf("expected missing etc ssh skipped, stat err=%v", err)
		}
	})
}

func TestCollectRootAndUserHomeAdditionalBranches(t *testing.T) {
	t.Run("root missing skips", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.SystemRootPrefix = t.TempDir()
		if err := collector.collectRootHome(context.Background()); err != nil {
			t.Fatalf("collectRootHome missing root: %v", err)
		}
		if _, err := os.Stat(filepath.Join(collector.tempDir, "root")); !os.IsNotExist(err) {
			t.Fatalf("expected no root output when root is missing, stat err=%v", err)
		}
	})

	t.Run("root canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectRootHome(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectRootHome canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("root target blocker returns error", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "root/.bashrc", "alias ll='ls -l'\n", 0o644)
		if err := os.WriteFile(filepath.Join(collector.tempDir, "root"), []byte("blocker"), 0o644); err != nil {
			t.Fatalf("write root blocker: %v", err)
		}
		if err := collector.collectRootHome(context.Background()); err == nil {
			t.Fatalf("expected root target blocker error")
		}
	})

	t.Run("root copies profiles histories config and ssh", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.BackupSSHKeys = true

		writeRootFile(t, root, "root/.bashrc", "alias ll='ls -l'\n", 0o644)
		writeRootFile(t, root, "root/.bash_history", "ls\n", 0o600)
		writeRootFile(t, root, "root/.bash_history-1", "cd /\n", 0o600)
		writeRootFile(t, root, "root/.ssh/id_rsa", "key\n", 0o600)
		writeRootFile(t, root, "root/.config/tool/config.json", "{}\n", 0o600)
		writeRootFile(t, root, "root/.config/.wrangler/logs/wrangler.log", "log\n", 0o600)

		if err := collector.collectRootHome(context.Background()); err != nil {
			t.Fatalf("collectRootHome: %v", err)
		}
		for _, rel := range []string{
			"root/.bashrc",
			"root/.bash_history",
			"root/.bash_history-1",
			"root/.ssh/id_rsa",
			"root/.config/tool/config.json",
			"root/.config/.wrangler/logs/wrangler.log",
		} {
			assertFileExists(t, filepath.Join(collector.tempDir, filepath.FromSlash(rel)))
		}
	})

	t.Run("root skips ssh when disabled", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.BackupSSHKeys = false
		writeRootFile(t, root, "root/.ssh/id_rsa", "key\n", 0o600)

		if err := collector.collectRootHome(context.Background()); err != nil {
			t.Fatalf("collectRootHome: %v", err)
		}
		if _, err := os.Stat(filepath.Join(collector.tempDir, "root/.ssh")); !os.IsNotExist(err) {
			t.Fatalf("expected root .ssh skipped when disabled, stat err=%v", err)
		}
	})

	t.Run("user homes missing skips", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.SystemRootPrefix = t.TempDir()
		if err := collector.collectUserHomes(context.Background()); err != nil {
			t.Fatalf("collectUserHomes missing home: %v", err)
		}
	})

	t.Run("user homes canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectUserHomes(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectUserHomes canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("user homes unreadable path returns error", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "home", "not a directory\n", 0o644)
		if err := collector.collectUserHomes(context.Background()); err == nil {
			t.Fatalf("expected /home file to return read error")
		}
	})

	t.Run("user homes copy file entry and ssh when enabled", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.BackupSSHKeys = true

		writeRootFile(t, root, "home/alice/.ssh/id_ed25519", "key\n", 0o600)
		writeRootFile(t, root, "home/alice/note.txt", "note\n", 0o644)
		writeRootFile(t, root, "home/README", "top-level file\n", 0o644)

		if err := collector.collectUserHomes(context.Background()); err != nil {
			t.Fatalf("collectUserHomes: %v", err)
		}
		for _, rel := range []string{
			"home/alice/.ssh/id_ed25519",
			"home/alice/note.txt",
			"home/README",
		} {
			assertFileExists(t, filepath.Join(collector.tempDir, filepath.FromSlash(rel)))
		}
	})

	t.Run("user homes exclude ssh when disabled", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.BackupSSHKeys = false
		writeRootFile(t, root, "home/alice/.ssh/id_ed25519", "key\n", 0o600)
		writeRootFile(t, root, "home/alice/note.txt", "note\n", 0o644)

		if err := collector.collectUserHomes(context.Background()); err != nil {
			t.Fatalf("collectUserHomes: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.tempDir, "home/alice/note.txt"))
		if _, err := os.Stat(filepath.Join(collector.tempDir, "home/alice/.ssh")); !os.IsNotExist(err) {
			t.Fatalf("expected alice .ssh skipped when disabled, stat err=%v", err)
		}
	})
}

func TestCollectKernelAndHardwareInfoBranches(t *testing.T) {
	t.Run("kernel command failure is non critical", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "cat" {
					return "/bin/cat", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if len(args) == 1 && strings.HasSuffix(args[0], "/proc/version") {
					return []byte("broken version"), errors.New("version failed")
				}
				return []byte("BOOT_IMAGE=/vmlinuz\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/cmdline", "BOOT_IMAGE=/vmlinuz\n", 0o644)
		writeRootFile(t, root, "proc/version", "Linux version\n", 0o644)
		if err := collector.collectKernelInfo(context.Background()); err != nil {
			t.Fatalf("non-critical kernel version command failure should be swallowed: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.proxsaveCommandsDir("system"), "kernel_cmdline.txt"))
		if _, err := os.Stat(filepath.Join(collector.proxsaveCommandsDir("system"), "kernel_version.txt")); !os.IsNotExist(err) {
			t.Fatalf("expected no kernel_version output after command failure, stat err=%v", err)
		}
	})

	t.Run("kernel success and write failure", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "cat" {
					return "/bin/cat", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(filepath.Base(args[0]) + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/cmdline", "BOOT_IMAGE=/vmlinuz\n", 0o644)
		writeRootFile(t, root, "proc/version", "Linux version\n", 0o644)

		if err := collector.collectKernelInfo(context.Background()); err != nil {
			t.Fatalf("collectKernelInfo success: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.proxsaveCommandsDir("system"), "kernel_cmdline.txt"))
		assertFileExists(t, filepath.Join(collector.proxsaveCommandsDir("system"), "kernel_version.txt"))

		collector = newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "cat" {
					return "/bin/cat", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(filepath.Base(args[0]) + "\n"), nil
			},
		})
		collector.config.SystemRootPrefix = root
		commandsDir := collector.proxsaveCommandsDir("system")
		if err := os.MkdirAll(commandsDir, 0o755); err != nil {
			t.Fatalf("mkdir commands dir: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(commandsDir, "kernel_version.txt"), 0o755); err != nil {
			t.Fatalf("mkdir kernel_version blocker: %v", err)
		}
		if err := collector.collectKernelInfo(context.Background()); err == nil {
			t.Fatalf("expected kernel version write failure")
		}
	})

	t.Run("hardware probes success where available", func(t *testing.T) {
		calls := make(map[string]int)
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "dmidecode", "sensors", "smartctl":
					return "/usr/sbin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls[name]++
				return []byte(name + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "sys/class/hwmon/hwmon0/name", "coretemp\n", 0o644)
		writeRootFile(t, root, "usr/sbin/smartctl", "#!/bin/sh\n", 0o755)
		if os.Geteuid() == 0 {
			if err := os.MkdirAll(filepath.Join(root, "sys", "firmware", "dmi", "tables"), 0o755); err != nil {
				t.Fatalf("mkdir dmi tables: %v", err)
			}
		}

		if err := collector.collectHardwareInfo(context.Background()); err != nil {
			t.Fatalf("collectHardwareInfo: %v", err)
		}
		if os.Geteuid() == 0 && calls["dmidecode"] != 1 {
			t.Fatalf("dmidecode calls=%d; want 1 when root", calls["dmidecode"])
		}
		if calls["sensors"] != 1 {
			t.Fatalf("sensors calls=%d; want 1", calls["sensors"])
		}
		if calls["smartctl"] != 1 {
			t.Fatalf("smartctl calls=%d; want 1", calls["smartctl"])
		}
		assertFileExists(t, filepath.Join(collector.proxsaveCommandsDir("system"), "sensors.txt"))
		assertFileExists(t, filepath.Join(collector.proxsaveCommandsDir("system"), "smartctl_scan.txt"))
	})
}

func TestCollectSystemAdditionalRuntimeBranches(t *testing.T) {
	t.Run("runtime leases canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectSystemRuntimeLeases(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemRuntimeLeases canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("runtime leases copies available directories", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "var/lib/dhcp/lease", "lease\n", 0o644)
		writeRootFile(t, root, "var/lib/NetworkManager/lease", "lease\n", 0o644)
		writeRootFile(t, root, "run/systemd/netif/leases/2", "lease\n", 0o644)

		if err := collector.collectSystemRuntimeLeases(context.Background()); err != nil {
			t.Fatalf("collectSystemRuntimeLeases: %v", err)
		}
		for _, path := range []string{
			filepath.Join(collector.proxsaveRuntimeDir("var/lib/dhcp"), "lease"),
			filepath.Join(collector.proxsaveRuntimeDir("var/lib/NetworkManager"), "lease"),
			filepath.Join(collector.proxsaveRuntimeDir("run/systemd/netif/leases"), "2"),
		} {
			assertFileExists(t, path)
		}
	})

	t.Run("core runtime critical command missing", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		})
		if err := collector.collectSystemCoreRuntime(context.Background(), t.TempDir()); err == nil {
			t.Fatalf("expected critical os-release command failure")
		}
	})

	t.Run("core runtime hostname missing is tolerated", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "cat", "uname":
					return "/bin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + " " + strings.Join(args, " ") + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/os-release", "ID=debian\n", 0o644)
		commandsDir := scratchUnderRoot(t, collector)

		if err := collector.collectSystemCoreRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemCoreRuntime: %v", err)
		}
		assertFileExists(t, filepath.Join(commandsDir, "os_release.txt"))
		assertFileExists(t, filepath.Join(commandsDir, "uname.txt"))
		if _, err := os.Stat(filepath.Join(commandsDir, "hostname.txt")); !os.IsNotExist(err) {
			t.Fatalf("expected missing hostname command to produce no file, stat err=%v", err)
		}
	})

	t.Run("network primary write failures", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "ip" {
					return "/usr/sbin/ip", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(strings.Join(args, " ") + "\n"), nil
			},
		})
		for _, tc := range []struct {
			name    string
			blocker string
			fn      func(*Collector, context.Context, string) error
		}{
			{
				name:    "addr",
				blocker: "ip_addr.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemNetworkAddrRuntime(ctx, dir)
				},
			},
			{
				name:    "rules",
				blocker: "ip_rule.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemNetworkRulesRuntime(ctx, dir)
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				commandsDir := scratchUnderRoot(t, collector)
				if err := os.MkdirAll(filepath.Join(commandsDir, tc.blocker), 0o755); err != nil {
					t.Fatalf("mkdir blocker: %v", err)
				}
				if err := tc.fn(collector, context.Background(), commandsDir); err == nil {
					t.Fatalf("expected %s write failure", tc.name)
				}
			})
		}
	})

	t.Run("network neighbors ipv6 write failure", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "ip" {
					return "/usr/sbin/ip", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(strings.Join(args, " ") + "\n"), nil
			},
		})
		commandsDir := scratchUnderRoot(t, collector)
		if err := os.MkdirAll(filepath.Join(commandsDir, "ip6_neigh.txt"), 0o755); err != nil {
			t.Fatalf("mkdir blocker: %v", err)
		}
		if err := collector.collectSystemNetworkNeighborsRuntime(context.Background(), commandsDir); err == nil {
			t.Fatalf("expected IPv6 neighbor write failure")
		}
		assertFileExists(t, filepath.Join(commandsDir, "ip_neigh.txt"))
	})

	t.Run("network inventory write failure is logged and swallowed", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		if err := os.MkdirAll(filepath.Join(root, "sys/class/net/eth0"), 0o755); err != nil {
			t.Fatalf("mkdir sys net: %v", err)
		}
		writeRootFile(t, root, "sys/class/net/eth0/address", "02:00:00:00:00:01\n", 0o644)
		commandsDir := filepath.Join(t.TempDir(), "commands-file")
		if err := os.WriteFile(commandsDir, []byte("blocker"), 0o644); err != nil {
			t.Fatalf("write commands blocker: %v", err)
		}
		if err := collector.collectSystemNetworkInventoryRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("inventory wrapper should swallow write error: %v", err)
		}
	})

	t.Run("network bonding canceled and copies files", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectSystemNetworkBondingRuntime(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemNetworkBondingRuntime canceled=%v; want context.Canceled", err)
		}

		collector = newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/net/bonding/bond0", "bonding\n", 0o644)
		if err := os.MkdirAll(filepath.Join(root, "proc/net/bonding/subdir"), 0o755); err != nil {
			t.Fatalf("mkdir bonding subdir: %v", err)
		}
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemNetworkBondingRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemNetworkBondingRuntime: %v", err)
		}
		assertFileExists(t, filepath.Join(commandsDir, "bonding_bond0.txt"))
		if _, err := os.Stat(filepath.Join(commandsDir, "bonding_subdir.txt")); !os.IsNotExist(err) {
			t.Fatalf("expected bonding subdir skipped, stat err=%v", err)
		}
	})

	t.Run("storage and compute write failures", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "df", "mount", "free", "lscpu", "lspci", "lsusb", "lsblk":
					return "/usr/bin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + "\n"), nil
			},
		})
		for _, tc := range []struct {
			name    string
			blocker string
			fn      func(*Collector, context.Context, string) error
		}{
			{
				name:    "mounts second command",
				blocker: "mount.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemStorageMountsRuntime(ctx, dir)
				},
			},
			{
				name:    "block devices primary",
				blocker: "lsblk.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemStorageBlockDevicesRuntime(ctx, dir)
				},
			},
			{
				name:    "memory cpu second command",
				blocker: "lscpu.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemComputeMemoryCPURuntime(ctx, dir)
				},
			},
			{
				name:    "bus inventory primary",
				blocker: "lspci.txt",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemComputeBusInventoryRuntime(ctx, dir)
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				commandsDir := scratchUnderRoot(t, collector)
				if err := os.MkdirAll(filepath.Join(commandsDir, tc.blocker), 0o755); err != nil {
					t.Fatalf("mkdir blocker: %v", err)
				}
				if err := tc.fn(collector, context.Background(), commandsDir); err == nil {
					t.Fatalf("expected %s write failure", tc.name)
				}
			})
		}
	})

	t.Run("services runtime disabled unavailable and available", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.BackupSystemdServices = false
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemServicesRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemServicesRuntime disabled: %v", err)
		}
		if entries, err := os.ReadDir(commandsDir); err != nil {
			t.Fatalf("read commands dir: %v", err)
		} else if len(entries) != 0 {
			t.Fatalf("expected disabled services runtime to write no files, got %d", len(entries))
		}

		calls := 0
		collector = newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "systemctl" {
					return "/usr/bin/systemctl", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls++
				return []byte("unexpected\n"), nil
			},
			DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
		})
		collector.config.SystemRootPrefix = t.TempDir()
		commandsDir = scratchUnderRoot(t, collector)
		if err := collector.collectSystemServicesRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemServicesRuntime unavailable: %v", err)
		}
		if calls != 0 {
			t.Fatalf("systemctl calls=%d; want 0 when runtime unavailable", calls)
		}

		collector = newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "systemctl" {
					return "/usr/bin/systemctl", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(strings.Join(args, " ") + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/1/comm", "systemd\n", 0o644)
		commandsDir = scratchUnderRoot(t, collector)
		if err := collector.collectSystemServicesRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemServicesRuntime available: %v", err)
		}
		assertFileExists(t, filepath.Join(commandsDir, "systemctl_services.txt"))
		assertFileExists(t, filepath.Join(commandsDir, "systemctl_service_files.txt"))
	})

	t.Run("packages runtime disabled blocked and success", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.BackupInstalledPackages = false
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemPackagesInstalledRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemPackagesInstalledRuntime disabled: %v", err)
		}
		if _, err := os.Stat(filepath.Join(commandsDir, "packages")); !os.IsNotExist(err) {
			t.Fatalf("expected no packages dir when disabled, stat err=%v", err)
		}

		collector = newTestCollector(t)
		collector.config.BackupInstalledPackages = true
		commandsDir = scratchUnderRoot(t, collector)
		if err := os.WriteFile(filepath.Join(commandsDir, "packages"), []byte("blocker"), 0o644); err != nil {
			t.Fatalf("write packages blocker: %v", err)
		}
		if err := collector.collectSystemPackagesInstalledRuntime(context.Background(), commandsDir); err == nil {
			t.Fatalf("expected packages directory creation failure")
		}

		collector = newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "dpkg" {
					return "/usr/bin/dpkg", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("ii proxsave\n"), nil
			},
		})
		collector.config.BackupInstalledPackages = true
		commandsDir = collector.proxsaveCommandsDir("system")
		if err := collector.collectSystemPackagesInstalledRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemPackagesInstalledRuntime success: %v", err)
		}
		assertFileExists(t, filepath.Join(commandsDir, "packages/dpkg_list.txt"))
	})

	t.Run("sysctl runtime disabled success and write failure", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.BackupSysctlConfig = false
		commandsDir := scratchUnderRoot(t, collector)
		if err := collector.collectSystemSysctlRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemSysctlRuntime disabled: %v", err)
		}
		if _, err := os.Stat(filepath.Join(commandsDir, "sysctl.txt")); !os.IsNotExist(err) {
			t.Fatalf("expected no sysctl output when disabled, stat err=%v", err)
		}

		collector = newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "sysctl" {
					return "/usr/sbin/sysctl", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte("kernel.hostname = test\n"), nil
			},
		})
		commandsDir = collector.proxsaveCommandsDir("system")
		if err := collector.collectSystemSysctlRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("collectSystemSysctlRuntime success: %v", err)
		}
		assertFileExists(t, filepath.Join(commandsDir, "sysctl.txt"))

		commandsDir = scratchUnderRoot(t, collector)
		if err := os.MkdirAll(filepath.Join(commandsDir, "sysctl.txt"), 0o755); err != nil {
			t.Fatalf("mkdir sysctl blocker: %v", err)
		}
		if err := collector.collectSystemSysctlRuntime(context.Background(), commandsDir); err == nil {
			t.Fatalf("expected sysctl write failure")
		}
	})

	t.Run("lvm write failures after earlier commands", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				switch name {
				case "pvs", "vgs", "lvs":
					return "/usr/sbin/" + name, nil
				default:
					return "", os.ErrNotExist
				}
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + "\n"), nil
			},
		})
		for _, tc := range []struct {
			name    string
			blocker string
			before  []string
		}{
			{name: "pvs", blocker: "lvm_pvs.txt"},
			{name: "vgs", blocker: "lvm_vgs.txt", before: []string{"lvm_pvs.txt"}},
			{name: "lvs", blocker: "lvm_lvs.txt", before: []string{"lvm_pvs.txt", "lvm_vgs.txt"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				commandsDir := scratchUnderRoot(t, collector)
				if err := os.MkdirAll(filepath.Join(commandsDir, tc.blocker), 0o755); err != nil {
					t.Fatalf("mkdir blocker: %v", err)
				}
				if err := collector.collectSystemLVMRuntime(context.Background(), commandsDir); err == nil {
					t.Fatalf("expected %s write failure", tc.name)
				}
				for _, rel := range tc.before {
					assertFileExists(t, filepath.Join(commandsDir, rel))
				}
			})
		}
	})
}

func TestCollectBestEffortProbeLateCancellationBranches(t *testing.T) {
	t.Run("write error observes canceled context", func(t *testing.T) {
		var cancel context.CancelFunc
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				cancel()
				return []byte("ok\n"), nil
			},
		})
		ctx, cancelFn := context.WithCancel(context.Background())
		cancel = cancelFn
		output := filepath.Join(scratchUnderRoot(t, collector), "probe.txt")
		if err := os.MkdirAll(output, 0o755); err != nil {
			t.Fatalf("mkdir output blocker: %v", err)
		}
		if err := collector.collectBestEffortProbe(ctx, commandSpec("probe"), output, "probe", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectBestEffortProbe error=%v; want context.Canceled", err)
		}
	})

	t.Run("successful command observes canceled context before returning", func(t *testing.T) {
		var cancel context.CancelFunc
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				cancel()
				return []byte("ok\n"), nil
			},
		})
		ctx, cancelFn := context.WithCancel(context.Background())
		cancel = cancelFn
		output := filepath.Join(scratchUnderRoot(t, collector), "probe.txt")
		if err := collector.collectBestEffortProbe(ctx, commandSpec("probe"), output, "probe", nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectBestEffortProbe error=%v; want context.Canceled", err)
		}
		assertFileExists(t, output)
	})
}

func TestCollectSystemRemainingRuntimeErrorBranches(t *testing.T) {
	t.Run("identity and logging canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		collector := newTestCollector(t)
		if err := collector.collectSystemIdentityStatic(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemIdentityStatic canceled=%v; want context.Canceled", err)
		}
		if err := collector.collectSystemLoggingStatic(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemLoggingStatic canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("runtime lease copy errors are logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "var/lib/dhcp/lease", "lease\n", 0o644)
		writeRootFile(t, root, "var/lib/NetworkManager/lease", "lease\n", 0o644)
		writeRootFile(t, root, "run/systemd/netif/leases/2", "lease\n", 0o644)
		for _, dest := range []string{
			collector.proxsaveRuntimeDir("var/lib/dhcp"),
			collector.proxsaveRuntimeDir("var/lib/NetworkManager"),
			collector.proxsaveRuntimeDir("run/systemd/netif/leases"),
		} {
			writeFileAt(t, dest, "blocker\n")
		}
		if err := collector.collectSystemRuntimeLeases(context.Background()); err != nil {
			t.Fatalf("collectSystemRuntimeLeases should log copy errors: %v", err)
		}
	})

	t.Run("core runtime uname critical failure", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "cat" {
					return "/bin/cat", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/os-release", "ID=debian\n", 0o644)
		if err := collector.collectSystemCoreRuntime(context.Background(), t.TempDir()); err == nil {
			t.Fatalf("expected critical uname failure")
		}
	})

	t.Run("core runtime hostname cancellation propagates", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if name == "hostname" {
					return nil, context.Canceled
				}
				return []byte(name + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/os-release", "ID=debian\n", 0o644)
		if err := collector.collectSystemCoreRuntime(context.Background(), collector.proxsaveCommandsDir("system")); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemCoreRuntime hostname cancellation=%v; want context.Canceled", err)
		}
	})

	t.Run("remaining command write failures", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(name + "\n"), nil
			},
		})
		collector.config.BackupFirewallRules = true
		collector.config.BackupInstalledPackages = true
		for _, tc := range []struct {
			name    string
			blocker string
			fn      func(context.Context, string) error
		}{
			{name: "routes", blocker: "ip_route.txt", fn: collector.collectSystemNetworkRoutesRuntime},
			{name: "neighbors first command", blocker: "ip_neigh.txt", fn: collector.collectSystemNetworkNeighborsRuntime},
			{name: "mounts first command", blocker: "df.txt", fn: collector.collectSystemStorageMountsRuntime},
			{name: "compute first command", blocker: "free.txt", fn: collector.collectSystemComputeMemoryCPURuntime},
			{name: "packages dpkg", blocker: "packages/dpkg_list.txt", fn: collector.collectSystemPackagesInstalledRuntime},
			{name: "iptables", blocker: "iptables.txt", fn: collector.collectSystemFirewallIPTablesRuntime},
			{name: "ip6tables", blocker: "ip6tables.txt", fn: collector.collectSystemFirewallIP6TablesRuntime},
		} {
			t.Run(tc.name, func(t *testing.T) {
				commandsDir := scratchUnderRoot(t, collector)
				if err := os.MkdirAll(filepath.Join(commandsDir, tc.blocker), 0o755); err != nil {
					t.Fatalf("mkdir blocker: %v", err)
				}
				if err := tc.fn(context.Background(), commandsDir); err == nil {
					t.Fatalf("expected %s write failure", tc.name)
				}
			})
		}
	})

	t.Run("bonding copy error is logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/net/bonding/bond0", "bonding\n", 0o644)
		commandsDir := scratchUnderRoot(t, collector)
		if err := os.MkdirAll(filepath.Join(commandsDir, "bonding_bond0.txt"), 0o755); err != nil {
			t.Fatalf("mkdir bonding blocker: %v", err)
		}
		if err := collector.collectSystemNetworkBondingRuntime(context.Background(), commandsDir); err != nil {
			t.Fatalf("bonding copy errors should be logged: %v", err)
		}
	})

	t.Run("best effort runtime cancellations propagate", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			setup func(*Collector)
			fn    func(*Collector, context.Context, string) error
		}{
			{
				name: "bus lsusb",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemComputeBusInventoryRuntime(ctx, dir)
				},
			},
			{
				name:  "services first",
				setup: func(c *Collector) { writeRootFile(t, c.config.SystemRootPrefix, "proc/1/comm", "systemd\n", 0o644) },
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemServicesRuntime(ctx, dir)
				},
			},
			{
				name:  "ufw systemctl",
				setup: func(c *Collector) { writeRootFile(t, c.config.SystemRootPrefix, "proc/1/comm", "systemd\n", 0o644) },
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemFirewallUFWRuntime(ctx, dir)
				},
			},
			{
				name:  "firewalld systemctl",
				setup: func(c *Collector) { writeRootFile(t, c.config.SystemRootPrefix, "proc/1/comm", "systemd\n", 0o644) },
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemFirewallFirewalldRuntime(ctx, dir)
				},
			},
			{
				name: "kernel modules lsmod",
				fn: func(c *Collector, ctx context.Context, dir string) error {
					return c.collectSystemKernelModulesRuntime(ctx, dir)
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				collector := newTestCollectorWithDeps(t, CollectorDeps{
					LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
					RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
						switch {
						case tc.name == "bus lsusb" && name == "lsusb":
							return nil, context.Canceled
						case tc.name == "services first" && name == "systemctl":
							return nil, context.Canceled
						case tc.name == "ufw systemctl" && name == "systemctl":
							return nil, context.Canceled
						case tc.name == "firewalld systemctl" && name == "systemctl":
							return nil, context.Canceled
						case tc.name == "kernel modules lsmod" && name == "lsmod":
							return nil, context.Canceled
						default:
							return []byte(name + "\n"), nil
						}
					},
				})
				collector.config.SystemRootPrefix = t.TempDir()
				collector.config.BackupFirewallRules = true
				collector.config.BackupKernelModules = true
				collector.config.BackupSystemdServices = true
				if tc.setup != nil {
					tc.setup(collector)
				}
				if err := tc.fn(collector, context.Background(), scratchUnderRoot(t, collector)); !errors.Is(err, context.Canceled) {
					t.Fatalf("%s error=%v; want context.Canceled", tc.name, err)
				}
			})
		}
	})

	t.Run("services second command cancellation propagates", func(t *testing.T) {
		calls := 0
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if calls == 2 {
					return nil, context.Canceled
				}
				return []byte("ok\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/1/comm", "systemd\n", 0o644)
		if err := collector.collectSystemServicesRuntime(context.Background(), t.TempDir()); !errors.Is(err, context.Canceled) {
			t.Fatalf("services second command error=%v; want context.Canceled", err)
		}
	})

	t.Run("kernel modules disabled and lvm canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.BackupKernelModules = false
		if err := collector.collectSystemKernelModulesRuntime(context.Background(), t.TempDir()); err != nil {
			t.Fatalf("collectSystemKernelModulesRuntime disabled: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectSystemLVMRuntime(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectSystemLVMRuntime canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("network report canceled and write failure", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.buildNetworkReport(ctx, t.TempDir()); !errors.Is(err, context.Canceled) {
			t.Fatalf("buildNetworkReport canceled=%v; want context.Canceled", err)
		}

		commandsDir := scratchUnderRoot(t, collector)
		if err := os.MkdirAll(filepath.Join(commandsDir, "network_report.txt"), 0o755); err != nil {
			t.Fatalf("mkdir network report blocker: %v", err)
		}
		if err := collector.buildNetworkReport(context.Background(), commandsDir); err == nil {
			t.Fatalf("expected network report write failure")
		}
	})

	t.Run("ensure system path skips blanks and duplicates", func(t *testing.T) {
		t.Setenv("PATH", strings.Join([]string{"/usr/bin", "", "/usr/bin"}, string(os.PathListSeparator)))
		ensureSystemPath()
		got := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))
		if got[0] != "/usr/bin" {
			t.Fatalf("PATH first entry=%q; want /usr/bin", got[0])
		}
		for _, seg := range got {
			if seg == "" {
				t.Fatalf("PATH should not contain blank entries: %q", os.Getenv("PATH"))
			}
		}
	})
}

func TestCollectSystemAdditionalFileBranches(t *testing.T) {
	t.Run("critical files canceled and copies existing", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectCriticalFiles(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectCriticalFiles canceled=%v; want context.Canceled", err)
		}

		collector = newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n", 0o644)
		writeRootFile(t, root, "etc/group", "root:x:0:\n", 0o644)
		if err := collector.collectCriticalFiles(context.Background()); err != nil {
			t.Fatalf("collectCriticalFiles: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.tempDir, "etc/passwd"))
		assertFileExists(t, filepath.Join(collector.tempDir, "etc/group"))
		if _, err := os.Stat(filepath.Join(collector.tempDir, "etc/shadow")); !os.IsNotExist(err) {
			t.Fatalf("expected missing shadow skipped, stat err=%v", err)
		}
	})

	t.Run("custom paths trim deduplicate relative and absolute entries", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "var/lib/app/config.yml", "enabled: true\n", 0o644)
		writeRootFile(t, root, "opt/app/bin/run", "#!/bin/sh\n", 0o755)
		collector.config.CustomBackupPaths = []string{
			" ",
			"var/lib/app/config.yml",
			"/var/lib/app/config.yml",
			"/opt/app",
			"/missing",
		}

		if err := collector.collectCustomPaths(context.Background()); err != nil {
			t.Fatalf("collectCustomPaths: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.tempDir, "var/lib/app/config.yml"))
		assertFileExists(t, filepath.Join(collector.tempDir, "opt/app/bin/run"))
		if _, err := os.Stat(filepath.Join(collector.tempDir, "missing")); !os.IsNotExist(err) {
			t.Fatalf("expected missing custom path skipped, stat err=%v", err)
		}
	})

	t.Run("custom paths canceled", func(t *testing.T) {
		collector := newTestCollector(t)
		collector.config.CustomBackupPaths = []string{"/etc/passwd"}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectCustomPaths(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectCustomPaths canceled=%v; want context.Canceled", err)
		}
	})

	t.Run("script directories canceled copies and skips missing", func(t *testing.T) {
		collector := newTestCollector(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := collector.collectScriptDirectories(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectScriptDirectories canceled=%v; want context.Canceled", err)
		}

		collector = newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "usr/local/bin/tool", "#!/bin/sh\n", 0o755)
		if err := collector.collectScriptDirectories(context.Background()); err != nil {
			t.Fatalf("collectScriptDirectories: %v", err)
		}
		assertFileExists(t, filepath.Join(collector.tempDir, "usr/local/bin/tool"))
		if _, err := os.Stat(filepath.Join(collector.tempDir, "usr/local/sbin")); !os.IsNotExist(err) {
			t.Fatalf("expected missing /usr/local/sbin skipped, stat err=%v", err)
		}
	})
}

func TestCollectSystemRemainingFileErrorBranches(t *testing.T) {
	t.Run("kernel first command write failure", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(name string) (string, error) {
				if name == "cat" {
					return "/bin/cat", nil
				}
				return "", os.ErrNotExist
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return []byte(filepath.Base(args[0]) + "\n"), nil
			},
		})
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "proc/cmdline", "BOOT_IMAGE=/vmlinuz\n", 0o644)
		writeRootFile(t, root, "proc/version", "Linux version\n", 0o644)
		if err := os.MkdirAll(filepath.Join(collector.proxsaveCommandsDir("system"), "kernel_cmdline.txt"), 0o755); err != nil {
			t.Fatalf("mkdir kernel cmdline blocker: %v", err)
		}
		if err := collector.collectKernelInfo(context.Background()); err == nil {
			t.Fatalf("expected kernel cmdline write failure")
		}
	})

	t.Run("hardware cancellation branches", func(t *testing.T) {
		for _, tc := range []string{"dmidecode", "sensors", "smartctl"} {
			t.Run(tc, func(t *testing.T) {
				if tc == "dmidecode" && os.Geteuid() != 0 {
					t.Skip("dmidecode availability requires root")
				}
				collector := newTestCollectorWithDeps(t, CollectorDeps{
					LookPath: func(name string) (string, error) {
						switch name {
						case "dmidecode", "sensors", "smartctl":
							return "/usr/sbin/" + name, nil
						default:
							return "", os.ErrNotExist
						}
					},
					RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
						if name == tc {
							return nil, context.Canceled
						}
						return []byte(name + "\n"), nil
					},
				})
				root := t.TempDir()
				collector.config.SystemRootPrefix = root
				writeRootFile(t, root, "sys/class/hwmon/hwmon0/name", "coretemp\n", 0o644)
				writeRootFile(t, root, "usr/sbin/smartctl", "#!/bin/sh\n", 0o755)
				if os.Geteuid() == 0 {
					if err := os.MkdirAll(filepath.Join(root, "sys/firmware/dmi/tables"), 0o755); err != nil {
						t.Fatalf("mkdir dmi tables: %v", err)
					}
				}
				if err := collector.collectHardwareInfo(context.Background()); !errors.Is(err, context.Canceled) {
					t.Fatalf("collectHardwareInfo %s cancellation=%v; want context.Canceled", tc, err)
				}
			})
		}
	})

	t.Run("critical files copy error is logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n", 0o644)
		if err := os.MkdirAll(filepath.Join(collector.tempDir, "etc/passwd"), 0o755); err != nil {
			t.Fatalf("mkdir passwd blocker: %v", err)
		}
		if err := collector.collectCriticalFiles(context.Background()); err != nil {
			t.Fatalf("collectCriticalFiles should log copy errors: %v", err)
		}
	})

	t.Run("custom path copy errors are logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "opt/app/config.yml", "enabled: true\n", 0o644)
		writeRootFile(t, root, "opt/appdir/file.txt", "data\n", 0o644)
		collector.config.CustomBackupPaths = []string{"/opt/app/config.yml", "/opt/appdir"}
		if err := os.MkdirAll(filepath.Join(collector.tempDir, "opt/app/config.yml"), 0o755); err != nil {
			t.Fatalf("mkdir custom file blocker: %v", err)
		}
		writeFileAt(t, filepath.Join(collector.tempDir, "opt/appdir"), "dir blocker\n")
		if err := collector.collectCustomPaths(context.Background()); err != nil {
			t.Fatalf("collectCustomPaths should log copy errors: %v", err)
		}
	})

	t.Run("script directory copy error is logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "usr/local/bin/tool", "#!/bin/sh\n", 0o755)
		writeFileAt(t, filepath.Join(collector.tempDir, "usr/local/bin"), "bin blocker\n")
		if err := collector.collectScriptDirectories(context.Background()); err != nil {
			t.Fatalf("collectScriptDirectories should log copy errors: %v", err)
		}
	})

	t.Run("ssh key copy errors are logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/ssh/sshd_config", "Port 22\n", 0o600)
		writeRootFile(t, root, "root/.ssh/id_rsa", "root-key\n", 0o600)
		writeRootFile(t, root, "home/alice/.ssh/id_ed25519", "alice-key\n", 0o600)
		writeFileAt(t, filepath.Join(collector.tempDir, "etc/ssh"), "etc ssh blocker\n")
		writeFileAt(t, filepath.Join(collector.tempDir, "root/.ssh"), "root ssh blocker\n")
		writeFileAt(t, filepath.Join(collector.tempDir, "home/alice/.ssh"), "alice ssh blocker\n")
		if err := collector.collectSSHKeys(context.Background()); err != nil {
			t.Fatalf("collectSSHKeys should log copy errors: %v", err)
		}
	})

	t.Run("script repository directory destination error", func(t *testing.T) {
		collector := newTestCollector(t)
		repo := t.TempDir()
		collector.config.ScriptRepositoryPath = repo
		if err := os.MkdirAll(filepath.Join(repo, "nested"), 0o755); err != nil {
			t.Fatalf("mkdir repo nested: %v", err)
		}
		writeFileAt(t, filepath.Join(repo, "nested", "file.txt"), "data\n")
		target := collector.proxsaveInfoDir("script-repository", filepath.Base(repo))
		writeFileAt(t, filepath.Join(target, "nested"), "nested blocker\n")
		if err := collector.collectScriptRepository(context.Background()); err == nil {
			t.Fatalf("expected script repository destination error")
		}
	})

	t.Run("root home copy errors are logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		collector.config.BackupSSHKeys = true
		writeRootFile(t, root, "root/.bashrc", "alias ll='ls -l'\n", 0o644)
		writeRootFile(t, root, "root/.bash_history", "history\n", 0o600)
		writeRootFile(t, root, "root/.ssh/id_rsa", "key\n", 0o600)
		writeRootFile(t, root, "root/.config/tool/config.json", "{}\n", 0o600)
		writeRootFile(t, root, "root/.config/.wrangler/logs/wrangler.log", "log\n", 0o600)
		if err := os.MkdirAll(filepath.Join(collector.tempDir, "root/.bashrc"), 0o755); err != nil {
			t.Fatalf("mkdir root file blocker: %v", err)
		}
		if err := os.MkdirAll(filepath.Join(collector.tempDir, "root/.bash_history"), 0o755); err != nil {
			t.Fatalf("mkdir root history blocker: %v", err)
		}
		writeFileAt(t, filepath.Join(collector.tempDir, "root/.ssh"), "ssh blocker\n")
		writeFileAt(t, filepath.Join(collector.tempDir, "root/.config"), "config blocker\n")
		if err := collector.collectRootHome(context.Background()); err != nil {
			t.Fatalf("collectRootHome should log copy errors: %v", err)
		}
	})

	t.Run("user home copy errors are logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "home/alice/note.txt", "note\n", 0o644)
		writeRootFile(t, root, "home/README", "top-level file\n", 0o644)
		writeFileAt(t, filepath.Join(collector.tempDir, "home/alice"), "alice blocker\n")
		if err := os.MkdirAll(filepath.Join(collector.tempDir, "home/README"), 0o755); err != nil {
			t.Fatalf("mkdir readme blocker: %v", err)
		}
		if err := collector.collectUserHomes(context.Background()); err != nil {
			t.Fatalf("collectUserHomes should log copy errors: %v", err)
		}
	})

	t.Run("custom inaccessible path is logged", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "blocked", "not a directory\n", 0o644)
		collector.config.CustomBackupPaths = []string{"/blocked/child"}
		if err := collector.collectCustomPaths(context.Background()); err != nil {
			t.Fatalf("collectCustomPaths should log inaccessible custom path: %v", err)
		}
	})

	t.Run("network report includes globbed config files", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "etc/network/interfaces.d/vmbr0", "auto vmbr0\n", 0o644)
		// Report files are written inside the collector staging root (tempDir), as
		// in production (proxsaveCommandsDir is under tempDir); a stray dir would be
		// rejected by writeReportFile's os.Root containment.
		commandsDir := collector.proxsaveCommandsDir("system")
		if err := collector.buildNetworkReport(context.Background(), commandsDir); err != nil {
			t.Fatalf("buildNetworkReport with globbed config: %v", err)
		}
	})

	t.Run("script repository skips backup and log files", func(t *testing.T) {
		collector := newTestCollector(t)
		repo := t.TempDir()
		collector.config.ScriptRepositoryPath = repo
		writeFileAt(t, filepath.Join(repo, "backup"), "backup file\n")
		writeFileAt(t, filepath.Join(repo, "log"), "log file\n")
		if err := collector.collectScriptRepository(context.Background()); err != nil {
			t.Fatalf("collectScriptRepository backup/log files: %v", err)
		}
		target := collector.proxsaveInfoDir("script-repository", filepath.Base(repo))
		if _, err := os.Stat(filepath.Join(target, "backup")); !os.IsNotExist(err) {
			t.Fatalf("expected backup file skipped, stat err=%v", err)
		}
		if _, err := os.Stat(filepath.Join(target, "log")); !os.IsNotExist(err) {
			t.Fatalf("expected log file skipped, stat err=%v", err)
		}
	})

	t.Run("script repository mid walk cancellation", func(t *testing.T) {
		collector := newTestCollector(t)
		repo := t.TempDir()
		collector.config.ScriptRepositoryPath = repo
		writeFileAt(t, filepath.Join(repo, "file.txt"), "data\n")
		ctx := &errAfterContext{Context: context.Background(), after: 2}
		if err := collector.collectScriptRepository(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectScriptRepository mid-walk cancellation=%v; want context.Canceled", err)
		}
	})

	t.Run("root home bad history glob is skipped", func(t *testing.T) {
		collector := newTestCollector(t)
		parent := t.TempDir()
		root := filepath.Join(parent, "[bad")
		if err := os.MkdirAll(filepath.Join(root, "root"), 0o755); err != nil {
			t.Fatalf("mkdir root with glob metachar: %v", err)
		}
		collector.config.SystemRootPrefix = root
		if err := collector.collectRootHome(context.Background()); err != nil {
			t.Fatalf("collectRootHome bad history glob: %v", err)
		}
	})

	t.Run("user homes mid-loop cancellation", func(t *testing.T) {
		collector := newTestCollector(t)
		root := t.TempDir()
		collector.config.SystemRootPrefix = root
		writeRootFile(t, root, "home/alice/note.txt", "note\n", 0o644)
		ctx := &errAfterContext{Context: context.Background(), after: 1}
		if err := collector.collectUserHomes(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("collectUserHomes mid-loop cancellation=%v; want context.Canceled", err)
		}
	})
}

func writeRootFile(t *testing.T, root, rel, content string, mode os.FileMode) {
	t.Helper()
	writeFileAt(t, filepath.Join(root, filepath.FromSlash(rel)), content)
	if err := os.Chmod(filepath.Join(root, filepath.FromSlash(rel)), mode); err != nil {
		t.Fatalf("chmod %s: %v", rel, err)
	}
}

func writeFileAt(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file %s: %v", path, err)
	}
}

func prepareStaticCopyErrorPath(t *testing.T, collector *Collector, root, rel string, dir bool) {
	t.Helper()

	src := filepath.Join(root, filepath.FromSlash(rel))
	dest := filepath.Join(collector.tempDir, filepath.FromSlash(rel))
	if dir {
		writeFileAt(t, filepath.Join(src, "entry"), "data\n")
		writeFileAt(t, dest, "dest blocker\n")
		return
	}

	writeRootFile(t, root, rel, "data\n", 0o644)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir destination blocker %s: %v", dest, err)
	}
}

type errAfterContext struct {
	context.Context
	calls int
	after int
}

func (c *errAfterContext) Err() error {
	c.calls++
	if c.calls > c.after {
		return context.Canceled
	}
	return nil
}

func newTestCollector(t *testing.T) *Collector {
	t.Helper()
	return newTestCollectorWithDeps(t, CollectorDeps{})
}

func newTestCollectorWithDeps(t *testing.T, override CollectorDeps) *Collector {
	t.Helper()
	deps := defaultCollectorDeps()
	if override.LookPath != nil {
		deps.LookPath = override.LookPath
	}
	if override.RunCommand != nil {
		deps.RunCommand = override.RunCommand
	}
	if override.RunCommandWithEnv != nil {
		deps.RunCommandWithEnv = override.RunCommandWithEnv
	}
	if override.Stat != nil {
		deps.Stat = override.Stat
	}
	if override.DetectUnprivilegedContainer != nil {
		deps.DetectUnprivilegedContainer = override.DetectUnprivilegedContainer
	}
	logger := logging.New(types.LogLevelDebug, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	return NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, deps)
}

// TestSystemManifestRecordsTargetsNotNestedFiles verifies system collection
// populates systemManifest at collection-target granularity (issue #59): a direct
// file copy and a directory copy each yield one entry, a missing source is
// recorded as not_found, and files nested inside a copied directory are NOT
// recorded individually.
func TestSystemManifestRecordsTargetsNotNestedFiles(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "hostname"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "netdir", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "netdir", "a.conf"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "netdir", "sub", "b.conf"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	collector := newTestCollectorWithDeps(t, CollectorDeps{})
	collector.systemManifest = make(map[string]ManifestEntry)
	collector.recordSystemManifest = true

	ctx := context.Background()
	if err := collector.safeCopyFile(ctx, filepath.Join(src, "hostname"), filepath.Join(collector.tempDir, "etc/hostname"), "Hostname"); err != nil {
		t.Fatalf("safeCopyFile hostname: %v", err)
	}
	if err := collector.safeCopyFile(ctx, filepath.Join(src, "missing"), filepath.Join(collector.tempDir, "etc/missing"), "Missing"); err != nil {
		t.Fatalf("safeCopyFile missing: %v", err)
	}
	if err := collector.safeCopyDir(ctx, filepath.Join(src, "netdir"), filepath.Join(collector.tempDir, "etc/netdir"), "Net dir"); err != nil {
		t.Fatalf("safeCopyDir netdir: %v", err)
	}

	m := collector.systemManifest
	if got := m["etc/hostname"]; got.Status != StatusCollected {
		t.Fatalf("etc/hostname: want collected, got %+v", got)
	}
	if got := m["etc/missing"]; got.Status != StatusNotFound {
		t.Fatalf("etc/missing: want not_found, got %+v", got)
	}
	if got := m["etc/netdir"]; got.Status != StatusCollected {
		t.Fatalf("etc/netdir: want collected dir target, got %+v", got)
	}
	for k := range m {
		if strings.HasPrefix(k, "etc/netdir/") {
			t.Fatalf("nested file %q must not be recorded (only the dir target)", k)
		}
	}
	if len(m) != 3 {
		t.Fatalf("expected 3 system manifest entries (hostname, missing, netdir), got %d: %+v", len(m), m)
	}
}

// TestSafeCopyDirSkipsStagingWorkspace verifies a broad source does not copy the
// staging workspace into itself (issue #56): the staging subtree under the source
// must be pruned while the real content is still collected.
func TestSafeCopyDirSkipsStagingWorkspace(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{})

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "real.conf"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The staging workspace lives under the source (the self-recursion case).
	staging := filepath.Join(srcDir, "proxsave-staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "ARCHIVE_DATA"), []byte("must not be copied"), 0o600); err != nil {
		t.Fatal(err)
	}
	collector.tempDir = staging
	collector.collectingCustomPaths = true // the prune is scoped to custom-path collection

	dest := filepath.Join(staging, "etc", "custom")
	if err := collector.safeCopyDir(context.Background(), srcDir, dest, "custom"); err != nil {
		t.Fatalf("safeCopyDir: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dest, "real.conf")); err != nil {
		t.Fatalf("expected real.conf to be collected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "proxsave-staging")); !os.IsNotExist(err) {
		t.Fatalf("staging workspace must not be copied into itself (#56), stat err=%v", err)
	}
}
