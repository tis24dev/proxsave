package backup

import (
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
	logger := logging.New(types.LogLevelDebug, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	return NewCollectorWithDeps(logger, config, tempDir, types.ProxmoxUnknown, false, deps)
}
