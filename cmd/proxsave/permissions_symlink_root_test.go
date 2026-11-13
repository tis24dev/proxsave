package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// The bounded walk Lstats its root and never follows a symlink, so a BACKUP_PATH that
// is a symlink to a directory was walked as a leaf: the link was chowned, the directory
// behind it kept its modes, and the footer still said "normalized correctly". The
// configured root is now resolved before the walk; symlinks inside stay unfollowed.
func TestApplyBackupPermissionsFollowsASymlinkedRoot(t *testing.T) {
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	sub := filepath.Join(target, "sub")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{target, sub, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(target, "pve1-backup-20261001-010101.tar.xz")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "inner-link")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "backup")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: link, FsIoTimeoutSeconds: 30}
	logger, buf := bufLogger()
	outcome := applyBackupPermissionsOutcome(context.Background(), cfg, logger, false)
	if len(outcome.skipped) != 0 || outcome.documentedSkip {
		t.Fatalf("outcome %+v, want nothing skipped; log:\n%s", outcome, buf.String())
	}
	for p, want := range map[string]os.FileMode{target: 0o750, sub: 0o750, file: 0o640, outside: 0o700} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s: mode %o, want %o", p, info.Mode().Perm(), want)
		}
	}
}
