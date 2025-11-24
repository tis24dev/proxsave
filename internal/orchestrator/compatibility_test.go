package orchestrator

import (
	"os"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/backup"
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
