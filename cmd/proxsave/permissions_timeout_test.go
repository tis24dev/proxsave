package main

import (
	"bytes"
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func currentUserGroup(t *testing.T) (string, string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Fatalf("LookupGroupId: %v", err)
	}
	return u.Username, g.Name
}

func bufLogger() (*logging.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	l := logging.New(types.LogLevelDebug, false)
	l.SetOutput(buf)
	return l, buf
}

func expiredCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	t.Cleanup(cancel)
	return ctx
}

// TestApplyBackupPermissionsHealthyAppliesChmod confirms the bounded walk still
// applies 0750 to directories on a responsive filesystem.
func TestApplyBackupPermissionsHealthyAppliesChmod(t *testing.T) {
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: base, FsIoTimeoutSeconds: 30}
	logger, _ := bufLogger()

	if err := applyBackupPermissions(context.Background(), cfg, logger, false); err != nil {
		t.Fatalf("applyBackupPermissions: %v", err)
	}

	info, err := os.Stat(sub)
	if err != nil {
		t.Fatalf("stat sub: %v", err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("healthy walk should chmod dir to 0750; got %o", info.Mode().Perm())
	}
}

// TestApplyBackupPermissionsDryRunNoMutation confirms a dry-run mutates nothing.
func TestApplyBackupPermissionsDryRunNoMutation(t *testing.T) {
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: base, FsIoTimeoutSeconds: 30}
	logger, buf := bufLogger()

	if err := applyBackupPermissions(context.Background(), cfg, logger, true); err != nil {
		t.Fatalf("applyBackupPermissions: %v", err)
	}

	info, err := os.Stat(sub)
	if err != nil {
		t.Fatalf("stat sub: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dry-run must not chmod; got %o, want 700", info.Mode().Perm())
	}
	if !strings.Contains(buf.String(), "DRY RUN: would recursively set ownership") {
		t.Fatalf("expected dry-run log, got:\n%s", buf.String())
	}
}

// TestApplyBackupPermissionsTimeoutIsBestEffort confirms a timed-out (dead/stale)
// path is skipped with a warning, without hanging or erroring.
func TestApplyBackupPermissionsTimeoutIsBestEffort(t *testing.T) {
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: base, FsIoTimeoutSeconds: 30}
	logger, buf := bufLogger()

	if err := applyBackupPermissions(expiredCtx(t), cfg, logger, false); err != nil {
		t.Fatalf("applyBackupPermissions must be best-effort (nil), got: %v", err)
	}
	if !strings.Contains(buf.String(), "timed out") {
		t.Fatalf("expected a timeout warning, got:\n%s", buf.String())
	}
}
