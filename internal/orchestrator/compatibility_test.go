package orchestrator

import (
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
)

func TestValidateCompatibility_Mismatch(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	compatFS = fake
	if err := os.MkdirAll(fake.onDisk("/etc/pve"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	manifest := &backup.Manifest{ProxmoxType: "pbs"}
	if err := ValidateCompatibility(manifest); err == nil {
		t.Fatalf("expected incompatibility error")
	}
}

func TestDetectCurrentSystem_Unknown(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()
	compatFS = NewFakeFS()

	if got := DetectCurrentSystem(); got != SystemTypeUnknown {
		t.Fatalf("expected unknown system, got %s", got)
	}
}

func TestGetSystemInfoDetectsPVE(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	compatFS = fake
	if err := fake.AddDir("/etc/pve"); err != nil {
		t.Fatalf("add dir: %v", err)
	}
	if err := fake.WriteFile("/etc/pve-release", []byte("Proxmox VE 8.1\n"), 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	if err := fake.WriteFile("/etc/hostname", []byte("pve-node\n"), 0o644); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	info := GetSystemInfo()
	if info["type"] != string(SystemTypePVE) {
		t.Fatalf("unexpected type: %s", info["type"])
	}
	if info["type_name"] != GetSystemTypeString(SystemTypePVE) {
		t.Fatalf("unexpected type name: %s", info["type_name"])
	}
	if info["version"] != "Proxmox VE 8.1" {
		t.Fatalf("unexpected version: %s", info["version"])
	}
	if info["hostname"] != "pve-node" {
		t.Fatalf("unexpected hostname: %s", info["hostname"])
	}
}

func TestGetSystemInfoDetectsPBS(t *testing.T) {
	orig := compatFS
	defer func() { compatFS = orig }()

	fake := NewFakeFS()
	compatFS = fake
	if err := fake.AddDir("/etc/proxmox-backup"); err != nil {
		t.Fatalf("add dir: %v", err)
	}
	if err := fake.WriteFile("/etc/proxmox-backup-release", []byte("Proxmox Backup Server 3.0\n"), 0o644); err != nil {
		t.Fatalf("write release: %v", err)
	}
	if err := fake.WriteFile("/etc/hostname", []byte("pbs-node\n"), 0o644); err != nil {
		t.Fatalf("write hostname: %v", err)
	}

	info := GetSystemInfo()
	if info["type"] != string(SystemTypePBS) {
		t.Fatalf("unexpected type: %s", info["type"])
	}
	if info["version"] != "Proxmox Backup Server 3.0" {
		t.Fatalf("unexpected version: %s", info["version"])
	}
	if info["hostname"] != "pbs-node" {
		t.Fatalf("unexpected hostname: %s", info["hostname"])
	}
}
