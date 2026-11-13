package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// SET_BACKUP_PERMISSIONS walked SECONDARY_PATH and SECONDARY_LOG_PATH with the secondary
// disabled: the loader keeps both paths, and the pass chowned and re-moded a directory
// the backup no longer uses. A disabled secondary is now left alone.
func TestBackupPermissionsLeaveADisabledSecondaryAlone(t *testing.T) {
	usr, grp := currentUserGroup(t)
	root := t.TempDir()
	backupDir := filepath.Join(root, "backup")
	secondaryDir := filepath.Join(root, "secondary")
	for _, d := range []string{backupDir, secondaryDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(secondaryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: backupDir,
		SecondaryEnabled: false, SecondaryPath: secondaryDir, SecondaryLogPath: secondaryDir, FsIoTimeoutSeconds: 30}
	logger, _ := bufLogger()
	if err := applyBackupPermissions(context.Background(), cfg, logger, false); err != nil {
		t.Fatalf("applyBackupPermissions: %v", err)
	}
	if st, err := os.Stat(secondaryDir); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("disabled secondary mode = %v (%v), want 0755 untouched", st.Mode().Perm(), err)
	}
	if st, err := os.Stat(backupDir); err != nil || st.Mode().Perm() != 0o750 {
		t.Fatalf("backup dir mode = %v (%v), want 0750 (the pass still runs there)", st.Mode().Perm(), err)
	}
}
