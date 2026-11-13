package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// The install and upgrade footers said "permissions and ownership normalized correctly
// (including backup paths)" whatever the backup-path pass did: it returns no error on
// any skip. The footer now names what was skipped, in the line, since the TUI does not
// show the pass's WARNINGs.
func TestPermissionsFooterNamesTheSkippedBackupPaths(t *testing.T) {
	usr, grp := currentUserGroup(t)
	for _, tc := range []struct {
		name, user, wantStatus, wantMessage string
	}{
		{"unknown user", "proxsave-no-such-user-cm6", "warning", "permissions and ownership normalized; backup paths skipped: BACKUP_USER/BACKUP_GROUP cannot be resolved"},
		{"empty user", "", "warning", "permissions and ownership normalized; backup paths skipped: BACKUP_USER/BACKUP_GROUP are empty"},
		{"resolvable user", usr, "ok", "permissions and ownership normalized correctly (including backup paths)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			for _, d := range []string{"backup", "log", "configs"} {
				if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfgPath := filepath.Join(base, "configs", "backup.env")
			env := "BASE_DIR=" + base + "\nBACKUP_PATH=" + filepath.Join(base, "backup") + "\nLOG_PATH=" + filepath.Join(base, "log") +
				"\nSET_BACKUP_PERMISSIONS=true\nBACKUP_USER=" + tc.user + "\nBACKUP_GROUP=" + grp + "\nSECONDARY_ENABLED=false\n"
			if err := os.WriteFile(cfgPath, []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			status, message := fixPermissionsAfterInstall(context.Background(), cfgPath, base, logging.NewBootstrapLogger(), io.Discard)
			if status != tc.wantStatus || message != tc.wantMessage {
				t.Fatalf("status=%q message=%q, want %q %q", status, message, tc.wantStatus, tc.wantMessage)
			}
		})
	}
}

func TestBackupPermissionsOutcomeMarksADocumentedSkip(t *testing.T) {
	usr, grp := currentUserGroup(t)
	cfg := &config.Config{BackupUser: usr, BackupGroup: grp, BackupPath: filepath.Join(t.TempDir(), "missing"), FsIoTimeoutSeconds: 30}
	logger, _ := bufLogger()
	outcome := applyBackupPermissionsOutcome(context.Background(), cfg, logger, false)
	if !outcome.documentedSkip || len(outcome.skipped) != 0 {
		t.Fatalf("missing BACKUP_PATH: outcome %+v, want a documented skip and nothing skipped", outcome)
	}
}

// A backup path whose walk left an entry unchanged is a skipped path: the footer is a
// warning that names it, never "normalized correctly (including backup paths)".
func TestPermissionsFooterNamesABackupPathWithAFailedEntry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a 0000 file, so its chmod does not fail")
	}
	usr, grp := currentUserGroup(t)
	base := t.TempDir()
	for _, d := range []string{"backup", "log", "configs"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The chmod of a backup set file opens it first; a 0000 file cannot be opened by
	// its non-root owner, so that entry is the one the walk cannot change.
	if err := os.WriteFile(filepath.Join(base, "backup", "pve1-backup-20261001-010101.tar.xz"), []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(base, "configs", "backup.env")
	env := "BASE_DIR=" + base + "\nBACKUP_PATH=" + filepath.Join(base, "backup") + "\nLOG_PATH=" + filepath.Join(base, "log") +
		"\nSET_BACKUP_PERMISSIONS=true\nBACKUP_USER=" + usr + "\nBACKUP_GROUP=" + grp + "\nSECONDARY_ENABLED=false\n"
	if err := os.WriteFile(cfgPath, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	status, message := fixPermissionsAfterInstall(context.Background(), cfgPath, base, logging.NewBootstrapLogger(), io.Discard)
	wantMessage := "permissions and ownership normalized; backup paths skipped: " + filepath.Join(base, "backup") + " could not be changed"
	if status != "warning" || message != wantMessage {
		t.Fatalf("status=%q message=%q, want %q %q", status, message, "warning", wantMessage)
	}
}
