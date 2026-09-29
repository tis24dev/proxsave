package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// writeDefaultsFixture plants the boot and service-default files the system recipe
// reads, under a fake system root, including the /dev/null udev mask a stock PVE 9 ships.
func writeDefaultsFixture(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"etc/default/grub":                         "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet intel_iommu=on\"\n",
		"etc/default/grub.d/proxmox-ve.cfg":        "GRUB_DISABLE_OS_PROBER=true\n",
		"etc/default/zfs":                          "ZFS_MOUNT=yes\n",
		"etc/default/pve-ha-manager":               "#WATCHDOG_MODULE=ipmi_watchdog\n",
		"etc/udev/rules.d/70-persistent-net.rules": "SUBSYSTEM==\"net\", NAME=\"lan0\"\n",
		"etc/kernel/cmdline":                       "root=ZFS=rpool/ROOT/pve-1 boot=zfs\n",
		"etc/kernel/proxmox-boot-uuids":            "1234-ABCD\n",
	}
	for rel, content := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "etc/udev/rules.d/60-bridge-network-interface.rules")); err != nil {
		t.Fatal(err)
	}
}

// collectDefaults runs the real system recipe over root with every command stubbed
// (present, empty output), so what reaches tempDir is what the two variables that
// govern these files let through: BACKUP_SYSTEMD_SERVICES for /etc/default and the
// udev rules, BACKUP_KERNEL_MODULES for /etc/kernel.
func collectDefaults(t *testing.T, root string, services, kernelModules bool, customPaths ...string) (*Collector, string) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cfg.BackupSystemdServices = services
	cfg.BackupKernelModules = kernelModules
	cfg.CustomBackupPaths = customPaths
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, tempDir, types.ProxmoxVE, false, CollectorDeps{
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		RunCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte{}, nil },
		RunCommandWithEnv: func(context.Context, []string, string, ...string) ([]byte, error) {
			return []byte{}, nil
		},
		RunCommandCaptured: func(context.Context, []string, string, ...string) ([]byte, []byte, error) {
			return []byte{}, nil, nil
		},
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})
	if err := c.CollectSystemInfo(context.Background()); err != nil {
		t.Fatalf("CollectSystemInfo: %v", err)
	}
	return c, tempDir
}

func assertStaged(t *testing.T, tempDir, rel string, want bool) {
	t.Helper()
	_, err := os.Lstat(filepath.Join(tempDir, filepath.FromSlash(rel)))
	if got := err == nil; got != want {
		t.Errorf("%s staged=%v, want %v (err=%v)", rel, got, want, err)
	}
}

// The services restore category writes ./etc/default/ straight to the live system,
// so the GRUB files must never sit there: they are kept under proxsave-info, which
// every restore mode treats as export-only.
func TestSystemRecipeRoutesBootFilesToProxsaveInfo(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	_, tempDir := collectDefaults(t, root, true, true)

	assertStaged(t, tempDir, "etc/default/zfs", true)
	assertStaged(t, tempDir, "etc/default/pve-ha-manager", true)
	assertStaged(t, tempDir, "etc/udev/rules.d/70-persistent-net.rules", true)

	assertStaged(t, tempDir, "etc/default/grub", false)
	assertStaged(t, tempDir, "etc/default/grub.d", false)
	assertStaged(t, tempDir, "etc/kernel/cmdline", false)
	assertStaged(t, tempDir, "etc/kernel/proxmox-boot-uuids", false)

	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/default/grub", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/default/grub.d/proxmox-ve.cfg", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/kernel/cmdline", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/kernel/proxmox-boot-uuids", true)

	mask := filepath.Join(tempDir, "etc/udev/rules.d/60-bridge-network-interface.rules")
	if target, err := os.Readlink(mask); err != nil || target != "/dev/null" {
		t.Errorf("udev mask should stay a symlink to /dev/null, got target=%q err=%v", target, err)
	}
}

// BACKUP_SYSTEMD_SERVICES governs /etc/default (GRUB reference copy included) and the
// udev rules; the /etc/kernel files follow BACKUP_KERNEL_MODULES.
func TestBackupSystemdServicesGatesDefaultsAndUdevRules(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	_, tempDir := collectDefaults(t, root, false, true)

	for _, rel := range []string{"etc/default", "etc/udev", "var/lib/proxsave-info/boot/etc/default"} {
		assertStaged(t, tempDir, rel, false)
	}
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/kernel/cmdline", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/kernel/proxmox-boot-uuids", true)
}

// BACKUP_KERNEL_MODULES governs /etc/kernel/cmdline and proxmox-boot-uuids, next to
// /etc/modules and /etc/modprobe.d.
func TestBackupKernelModulesGatesKernelBootFiles(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	_, tempDir := collectDefaults(t, root, true, false)

	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/kernel", false)
	assertStaged(t, tempDir, "etc/kernel", false)
	assertStaged(t, tempDir, "etc/default/zfs", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/default/grub", true)
}

// A GRUB host on LVM has no /etc/kernel at all (measured on pve-test), and a
// minimal system may have no udev rules: neither is a warning.
func TestMissingBootSourcesAreQuiet(t *testing.T) {
	root := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, t.TempDir(), types.ProxmoxVE, false, CollectorDeps{
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})
	for _, collect := range []func(context.Context) error{c.collectSystemServicesStatic, c.collectSystemKernelModuleStatic} {
		if err := collect(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := c.logger.WarningCount(); n != 0 {
		t.Fatalf("missing sources produced %d warning(s)", n)
	}
}

// With CUSTOM_BACKUP_PATHS naming /etc/default the custom-path brick, which runs after
// the static one, also collects /etc/default/grub at its natural path. The restore
// never writes that copy to the system (orchestrator bootNeverLivePaths); it exports
// it with proxsave_info.
func TestCollectSystemInfoCustomBackupPathsKeepsGrubInPlace(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	_, tempDir := collectDefaults(t, root, true, true, "/etc/default")

	assertStaged(t, tempDir, "etc/default/grub", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/default/grub", true)
}
