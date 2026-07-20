package environment

import (
	"os"
	"path/filepath"
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
