package orchestrator

import (
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

func TestAnalyzeRestoreArchive_UsesInternalMetadataWhenCategoriesAreCommonOnly(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	restoreFS = osFS{}

	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"etc/hosts": "127.0.0.1 localhost\n",
		"var/lib/proxsave-info/backup_metadata.txt": "# ProxSave Metadata\nBACKUP_TYPE=pbs\nHOSTNAME=pbs-node\nPVE_CLUSTER_MODE=cluster\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	categories, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if err != nil {
		t.Fatalf("AnalyzeRestoreArchive() error: %v", err)
	}
	if backupType, ambiguous := detectBackupTypeFromCategories(categories); backupType != SystemTypeUnknown || ambiguous {
		t.Fatalf("detectBackupTypeFromCategories() = (%s, %v); want (%s, false)", backupType, ambiguous, SystemTypeUnknown)
	}
	if decision == nil {
		t.Fatalf("decision info is nil")
	}
	if decision.BackupType != SystemTypePBS {
		t.Fatalf("BackupType=%s; want %s", decision.BackupType, SystemTypePBS)
	}
	if decision.Source != RestoreDecisionSourceInternalMetadata {
		t.Fatalf("Source=%s; want %s", decision.Source, RestoreDecisionSourceInternalMetadata)
	}
	if decision.BackupHostname != "pbs-node" {
		t.Fatalf("BackupHostname=%q; want %q", decision.BackupHostname, "pbs-node")
	}
	if decision.ClusterPayload {
		t.Fatalf("ClusterPayload should stay false without pve_cluster payload")
	}
}

func TestAnalyzeRestoreArchive_ClusterPayloadUsesArchiveContents(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	restoreFS = osFS{}

	archivePath := filepath.Join(t.TempDir(), "backup.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"var/lib/pve-cluster/config.db":             "db\n",
		"var/lib/proxsave-info/backup_metadata.txt": "BACKUP_TYPE=pve\nPVE_CLUSTER_MODE=standalone\nHOSTNAME=node1\n",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	_, decision, err := AnalyzeRestoreArchive(archivePath, logger)
	if err != nil {
		t.Fatalf("AnalyzeRestoreArchive() error: %v", err)
	}
	if decision == nil {
		t.Fatalf("decision info is nil")
	}
	if !decision.ClusterPayload {
		t.Fatalf("ClusterPayload should be true when pve_cluster payload exists")
	}
	if decision.BackupType != SystemTypePVE {
		t.Fatalf("BackupType=%s; want %s", decision.BackupType, SystemTypePVE)
	}
}
