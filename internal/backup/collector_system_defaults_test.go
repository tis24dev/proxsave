package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// writeDefaultsFixture plants the files collectSystemDefaultsStatic reads, under a
// fake system root, including the /dev/null udev mask a stock PVE 9 ships.
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

func newDefaultsCollector(t *testing.T, root string, enabled bool) (*Collector, string) {
	t.Helper()
	tempDir := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = root
	cfg.BackupSystemDefaults = enabled
	c := NewCollectorWithDeps(logging.New(types.LogLevelError, false), cfg, tempDir, types.ProxmoxVE, false, CollectorDeps{
		DetectUnprivilegedContainer: func() (bool, string) { return false, "" },
	})
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
func TestCollectSystemDefaultsStaticRoutesBootFilesToProxsaveInfo(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	c, tempDir := newDefaultsCollector(t, root, true)

	if err := c.collectSystemDefaultsStatic(context.Background()); err != nil {
		t.Fatalf("collectSystemDefaultsStatic: %v", err)
	}

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

func TestCollectSystemDefaultsStaticDisabledCollectsNothing(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	c, tempDir := newDefaultsCollector(t, root, false)

	if err := c.collectSystemDefaultsStatic(context.Background()); err != nil {
		t.Fatalf("collectSystemDefaultsStatic: %v", err)
	}

	for _, rel := range []string{"etc/default", "etc/udev", "etc/kernel", "var/lib/proxsave-info/boot"} {
		assertStaged(t, tempDir, rel, false)
	}
}

// A GRUB host on LVM has no /etc/kernel at all (measured on pve-test), and a
// minimal system may have no udev rules: neither is a warning.
func TestCollectSystemDefaultsStaticMissingSourcesAreQuiet(t *testing.T) {
	root := t.TempDir()
	c, _ := newDefaultsCollector(t, root, true)

	if err := c.collectSystemDefaultsStatic(context.Background()); err != nil {
		t.Fatalf("collectSystemDefaultsStatic: %v", err)
	}
	if n := c.logger.WarningCount(); n != 0 {
		t.Fatalf("missing sources produced %d warning(s)", n)
	}
}

// With CUSTOM_BACKUP_PATHS naming /etc/default the operator already chose to have
// /etc/default/grub restored in place; the custom-path brick runs after the static
// one, so that choice survives.
func TestCollectSystemInfoCustomBackupPathsKeepsGrubInPlace(t *testing.T) {
	root := t.TempDir()
	writeDefaultsFixture(t, root)
	c, tempDir := newDefaultsCollector(t, root, true)
	c.config.CustomBackupPaths = []string{"/etc/default"}
	c.deps.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	c.deps.RunCommand = func(context.Context, string, ...string) ([]byte, error) { return []byte{}, nil }
	c.deps.RunCommandWithEnv = func(context.Context, []string, string, ...string) ([]byte, error) { return []byte{}, nil }
	c.deps.RunCommandCaptured = func(context.Context, []string, string, ...string) ([]byte, []byte, error) {
		return []byte{}, nil, nil
	}

	if err := c.CollectSystemInfo(context.Background()); err != nil {
		t.Fatalf("CollectSystemInfo: %v", err)
	}

	assertStaged(t, tempDir, "etc/default/grub", true)
	assertStaged(t, tempDir, "var/lib/proxsave-info/boot/etc/default/grub", true)
}
