package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/storage"
)

// The storage backends resolve BACKUP_USER/BACKUP_GROUP with their own copy of the
// lookup (storage.ResolveBackupOwner); the current run's files and the startup pass
// must land on the same owner, so the two must answer alike for every input.
func TestResolveBackupOwnerMatchesResolveUserGroupIDs(t *testing.T) {
	usr, grp := currentUserGroup(t)
	inputs := [][2]string{
		{usr, grp},
		{"root", "root"},
		{"proxsave-no-such-user-f3", grp},
		{usr, "proxsave-no-such-group-f3"},
		{"", ""},
	}
	for _, in := range inputs {
		uid1, gid1, err1 := resolveUserGroupIDs(in[0], in[1])
		uid2, gid2, err2 := storage.ResolveBackupOwner(in[0], in[1])
		if uid1 != uid2 || gid1 != gid2 || fmt.Sprint(err1) != fmt.Sprint(err2) {
			t.Errorf("%q:%q: resolveUserGroupIDs=(%d,%d,%v) ResolveBackupOwner=(%d,%d,%v)",
				in[0], in[1], uid1, gid1, err1, uid2, gid2, err2)
		}
	}
}

// The startup pass opens the files earlier runs left to the backup group: every file
// of a backup set on BACKUP_PATH and SECONDARY_PATH becomes 0640, while logs, temp
// and partial files, and a symlink's target keep their mode.
func TestApplyBackupPermissionsOpensPreviousBackupFilesToGroup(t *testing.T) {
	usr, grp := currentUserGroup(t)
	backupDir := t.TempDir()
	secondaryDir := t.TempDir()
	logDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.tar.xz")

	write := func(path string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(path, []byte("x"), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}
	shared := []string{}
	for _, dir := range []string{backupDir, secondaryDir} {
		raw := filepath.Join(dir, "pve1-backup-20261001-010101.tar.xz")
		for _, p := range []string{raw, raw + ".sha256", raw + ".metadata", raw + ".manifest.json",
			filepath.Join(dir, "pve1-backup-20261001-020202.tar.xz.bundle.tar"),
			filepath.Join(dir, "proxmox-backup-pve1-20200101.tar.gz")} {
			write(p, 0o600)
			shared = append(shared, p)
		}
	}
	untouched := map[string]os.FileMode{
		filepath.Join(backupDir, "pve1-backup-20261002-030303.tar.xz.partial"):          0o600,
		filepath.Join(backupDir, "pve1-backup-20261002-030303.tar.xz.bundle.tar.tmp-1"): 0o600,
		filepath.Join(secondaryDir, ".tmp-pve1-backup-20261002-030303.tar.xz-2"):        0o600,
		filepath.Join(backupDir, "notes.txt"):                                           0o600,
		filepath.Join(logDir, "backup-pve1-20261001-010101.log"):                        0o600,
		filepath.Join(logDir, "backup-stats-20261001-010101.json"):                      0o640,
		outside: 0o600,
	}
	for p, m := range untouched {
		write(p, m)
	}
	if err := os.Symlink(outside, filepath.Join(backupDir, "pve1-backup-20261001-040404.tar.xz")); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: backupDir,
		SecondaryPath: secondaryDir, LogPath: logDir, FsIoTimeoutSeconds: 30}
	logger, _ := bufLogger()
	if err := applyBackupPermissions(context.Background(), cfg, logger, false); err != nil {
		t.Fatalf("applyBackupPermissions: %v", err)
	}

	for _, p := range shared {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != backup.SharedArtifactFilePerm {
			t.Errorf("%s: mode %o, want %o", filepath.Base(p), info.Mode().Perm(), backup.SharedArtifactFilePerm)
		}
	}
	for p, m := range untouched {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != m {
			t.Errorf("%s: mode %o, want it untouched at %o", filepath.Base(p), info.Mode().Perm(), m)
		}
	}
}

func TestApplyBackupPermissionsDryRunLineNamesBackupFileMode(t *testing.T) {
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	file := filepath.Join(base, "pve1-backup-20261001-010101.tar.xz")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: base, FsIoTimeoutSeconds: 30}
	logger, buf := bufLogger()
	if err := applyBackupPermissions(context.Background(), cfg, logger, true); err != nil {
		t.Fatalf("applyBackupPermissions: %v", err)
	}
	want := fmt.Sprintf("DRY RUN: would recursively set ownership %d:%d, 0750 directory perms and 0640 backup file perms on %s",
		os.Getuid(), os.Getgid(), base)
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("expected %q in:\n%s", want, buf.String())
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("dry-run must not chmod a backup file; got %o", info.Mode().Perm())
	}
}
