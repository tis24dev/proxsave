package environment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// writeFile is a small fixture helper for building a host tree under a prefix.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDetectWithPrefixPVE is the issue #255 detection regression: a plain
// container root with only a mounted host tree under the prefix must detect the
// host as Proxmox VE, where the historical container-root detection returned
// ProxmoxUnknown.
func TestDetectWithPrefixPVE(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")
	if err := os.MkdirAll(filepath.Join(root, "etc/pve"), 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if info.PVEVersion != "8.2.2" {
		t.Fatalf("PVEVersion = %q, want 8.2.2", info.PVEVersion)
	}
}

func TestDetectWithPrefixPBS(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/proxmox-backup/version"), "3.2.1\n")
	if err := os.MkdirAll(filepath.Join(root, "etc/proxmox-backup"), 0o755); err != nil {
		t.Fatal(err)
	}

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxBS {
		t.Fatalf("Type = %v, want ProxmoxBS", info.Type)
	}
}

func TestDetectWithPrefixDual(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")
	writeFile(t, filepath.Join(root, "etc/proxmox-backup/version"), "3.2.1\n")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxDual {
		t.Fatalf("Type = %v, want ProxmoxDual", info.Type)
	}
}

// TestDetectWithPrefixSkipsCommandProbes proves that under a prefix the host
// command probes are not consulted: pveversion/proxmox-backup-manager run inside
// the container answer for the container, not the mounted host, so detection must
// rely on the re-anchored files only.
func TestDetectWithPrefixSkipsCommandProbes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")

	setValue(t, &lookPathFunc, func(string) (string, error) {
		t.Fatal("lookPathFunc must not be called under a root prefix")
		return "", nil
	})
	setValue(t, &runCommandFunc, func(string, ...string) (string, error) {
		t.Fatal("runCommandFunc must not be called under a root prefix")
		return "", nil
	})

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
}

// TestDetectWithPrefixClusterDBAloneIsResidue: config.db is created by pmxcfs, owned
// by no package and kept by pve-cluster's postrm even on purge, so on its own it proves
// PVE was once here, not that it is installed. Measured on PVE 9.2.2 with every PVE
// package purged: config.db was the only PVE file left. The mp1-missing host-backup
// shape does not reach this rung: mp0 carries /usr and the dpkg status, which decide
// first (TestDetectWithPrefixPVEViaClusterDBAndBinary).
func TestDetectWithPrefixClusterDBAloneIsResidue(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/pve-cluster/config.db"), "SQLite format 3\x00")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err == nil {
		t.Fatalf("config.db alone must not detect Proxmox, got Type = %v", info.Type)
	}
	want := "cluster-db (" + filepath.Join(root, "var/lib/pve-cluster/config.db") + ")"
	if info.PVEResidual != want {
		t.Fatalf("PVEResidual = %q, want %q", info.PVEResidual, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the residue %q", err, want)
	}
}

// TestDetectWithPrefixPBSHostWithClusterDB is the shape a FULL restore of a PVE+PBS
// archive leaves on a PBS-only host: config.db written, no PVE package. It is a PBS
// host with a PVE leftover, not a dual host.
func TestDetectWithPrefixPBSHostWithClusterDB(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "usr/sbin/proxmox-backup-manager"), "\x7fELF")
	writeFile(t, filepath.Join(root, "var/lib/pve-cluster/config.db"), "SQLite format 3\x00")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxBS {
		t.Fatalf("Type = %v, want ProxmoxBS", info.Type)
	}
	want := "cluster-db (" + filepath.Join(root, "var/lib/pve-cluster/config.db") + ")"
	if info.PVEResidual != want {
		t.Fatalf("PVEResidual = %q, want %q", info.PVEResidual, want)
	}
}

// TestDetectWithPrefixPVEViaClusterDBAndBinary: a real PVE whose dpkg status cannot
// be read still has the binaries its packages ship, so demoting config.db costs it
// nothing. The mp1-missing host-backup mount (mp0 = / without the /etc/pve bind)
// carries /usr, so this is its shape.
func TestDetectWithPrefixPVEViaClusterDBAndBinary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "usr/bin/pmxcfs"), "\x7fELF")
	writeFile(t, filepath.Join(root, "var/lib/pve-cluster/config.db"), "SQLite format 3\x00")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if info.PVEResidual != "" {
		t.Fatalf("PVEResidual = %q, want empty on an installed PVE", info.PVEResidual)
	}
}

// TestDetectWithPrefixPVEViaBinaryOnly covers a /usr-only mount: no /etc or /var,
// only the pmxcfs binary. It must detect PVE.
func TestDetectWithPrefixPVEViaBinaryOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "usr/bin/pmxcfs"), "\x7fELF")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
}

// TestDetectWithPrefixPVEViaDpkgStatus proves the dpkg database recovers the real
// offline version when the pmxcfs version files are absent.
func TestDetectWithPrefixPVEViaDpkgStatus(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"),
		"Package: bash\nStatus: install ok installed\nVersion: 5.2\n\n"+
			"Package: pve-manager\nStatus: install ok installed\nVersion: 8.2.2-1\n")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxVE {
		t.Fatalf("Type = %v, want ProxmoxVE", info.Type)
	}
	if info.PVEVersion != "8.2.2-1" {
		t.Fatalf("PVEVersion = %q, want 8.2.2-1", info.PVEVersion)
	}
}

// TestDetectWithPrefixDpkgResidualNotInstalled locks the residual-config guard: a
// pve-manager entry left as "deinstall ok config-files" is NOT installed and must
// not detect PVE.
func TestDetectWithPrefixDpkgResidualNotInstalled(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"),
		"Package: pve-manager\nStatus: deinstall ok config-files\nVersion: 8.2.2-1\n")

	if _, err := DetectWith(DetectOptions{RootPrefix: root}); err == nil {
		t.Fatal("residual config-files entry must not detect Proxmox")
	}
}

// TestDetectWithPrefixDpkgDependsMentionOnly locks stanza anchoring: pve-manager
// appearing only inside another package's Depends: line must not detect PVE.
func TestDetectWithPrefixDpkgDependsMentionOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"),
		"Package: some-tool\nStatus: install ok installed\nVersion: 1.0\nDepends: pve-manager\n")

	if _, err := DetectWith(DetectOptions{RootPrefix: root}); err == nil {
		t.Fatal("a Depends: pve-manager mention must not detect Proxmox")
	}
}

// TestDetectWithPrefixPBSViaBinaryOnly covers a /usr-only PBS mount.
func TestDetectWithPrefixPBSViaBinaryOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "usr/sbin/proxmox-backup-proxy"), "\x7fELF")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxBS {
		t.Fatalf("Type = %v, want ProxmoxBS", info.Type)
	}
}

// TestDetectWithPrefixDualViaDpkg: both server packages installed -> Dual.
func TestDetectWithPrefixDualViaDpkg(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"),
		"Package: pve-manager\nStatus: install ok installed\nVersion: 8.2.2-1\n\n"+
			"Package: proxmox-backup-server\nStatus: install ok installed\nVersion: 3.2.1\n")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err != nil {
		t.Fatalf("DetectWith: %v", err)
	}
	if info.Type != types.ProxmoxDual {
		t.Fatalf("Type = %v, want ProxmoxDual", info.Type)
	}
}

// TestDetectWithPrefixPlainDebianNoFalsePositive is the no-spam contract: a generic
// Debian tree with no Proxmox markers under the prefix must stay Unknown so the
// caller can fail closed without a false PVE/PBS label. The pbsBinaryCandidates
// client entry is intentionally excluded upstream so a proxmox-backup-client-only
// host does not read as PBS.
func TestDetectWithPrefixPlainDebianNoFalsePositive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/os-release"), "ID=debian\n")
	writeFile(t, filepath.Join(root, "var/lib/dpkg/status"),
		"Package: bash\nStatus: install ok installed\nVersion: 5.2\n\n"+
			"Package: coreutils\nStatus: install ok installed\nVersion: 9.1\n")

	info, err := DetectWith(DetectOptions{RootPrefix: root})
	if err == nil {
		t.Fatalf("plain Debian tree must not detect Proxmox, got Type=%v", info.Type)
	}
	if info.Type != types.ProxmoxUnknown {
		t.Fatalf("Type = %v, want ProxmoxUnknown", info.Type)
	}
}

// TestDetectWithEmptyPrefixUnchanged guards backward compatibility: with no prefix
// DetectWith is the historical Detect() and still probes commands.
func TestDetectWithEmptyPrefixUnchanged(t *testing.T) {
	probed := false
	setValue(t, &lookPathFunc, func(string) (string, error) {
		probed = true
		return "", os.ErrNotExist
	})
	// A tree that would only be seen if the prefix were honored must be ignored.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")

	_, _ = DetectWith(DetectOptions{})
	if !probed {
		t.Fatal("command probes must run when no prefix is set")
	}
}
