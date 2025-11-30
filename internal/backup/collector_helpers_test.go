package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectorIsClusteredPVE(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()

	collector := NewCollector(logger, config, tempDir, types.ProxmoxVE, false)

	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"status\" ]; then\n  echo \"Cluster information\"\n  exit 0\nfi\nexit 1\n"
	pvecmPath := filepath.Join(binDir, "pvecm")
	if err := os.WriteFile(pvecmPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write stub pvecm: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	ctx := context.Background()
	clustered, err := collector.isClusteredPVE(ctx)
	if err != nil {
		t.Fatalf("isClusteredPVE returned error: %v", err)
	}
	if !clustered {
		t.Fatal("expected clustered=true from stub pvecm output")
	}
}

func TestCollectorGetDatastoreList(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	collector := NewCollector(logger, config, tempDir, types.ProxmoxBS, false)

	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"datastore\" ] && [ \"$2\" = \"list\" ]; then\n  echo '[{\"name\":\"primary\",\"path\":\"/data/primary\",\"comment\":\"main\"},{\"name\":\"secondary\",\"path\":\"/data/secondary\"}]'\n  exit 0\nfi\nexit 1\n"
	managerPath := filepath.Join(binDir, "proxmox-backup-manager")
	if err := os.WriteFile(managerPath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write stub proxmox-backup-manager: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	ctx := context.Background()
	datastores, err := collector.getDatastoreList(ctx)
	if err != nil {
		t.Fatalf("getDatastoreList returned error: %v", err)
	}
	if len(datastores) != 2 {
		t.Fatalf("unexpected datastore list: %#v", datastores)
	}
	if datastores[0].Name != "primary" || datastores[0].Path != "/data/primary" {
		t.Fatalf("unexpected primary datastore: %#v", datastores[0])
	}
	if datastores[1].Name != "secondary" || datastores[1].Path != "/data/secondary" {
		t.Fatalf("unexpected secondary datastore: %#v", datastores[1])
	}
}

func TestCollectorHasTapeSupport(t *testing.T) {
	logger := logging.New(types.LogLevelInfo, false)
	config := GetDefaultCollectorConfig()
	tempDir := t.TempDir()
	collector := NewCollector(logger, config, tempDir, types.ProxmoxBS, false)

	binDir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = \"drive\" ] && [ \"$2\" = \"list\" ]; then\n  echo 'drive1'\n  exit 0\nfi\nexit 1\n"
	tapePath := filepath.Join(binDir, "proxmox-tape")
	if err := os.WriteFile(tapePath, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write stub proxmox-tape: %v", err)
	}

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)

	ctx := context.Background()
	hasTape, err := collector.hasTapeSupport(ctx)
	if err != nil {
		t.Fatalf("hasTapeSupport returned error: %v", err)
	}
	if !hasTape {
		t.Fatal("expected hasTapeSupport to return true with stub output")
	}
}
