package security

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// The preflight created and re-owned SECONDARY_PATH and SECONDARY_LOG_PATH with the
// secondary disabled: the loader keeps both paths, so a missing mount point became a
// directory on the root filesystem. A disabled secondary is now neither created nor
// checked.
func TestVerifyDirectoriesLeavesADisabledSecondaryAlone(t *testing.T) {
	root := t.TempDir()
	secondary := filepath.Join(root, "usb", "proxsave")
	secondaryLog := filepath.Join(root, "usb", "proxsave-log")
	cfg := &config.Config{
		BackupPath:       filepath.Join(root, "backup"),
		LogPath:          filepath.Join(root, "log"),
		SecondaryEnabled: false,
		SecondaryPath:    secondary,
		SecondaryLogPath: secondaryLog,
	}
	c := &Checker{logger: newSecurityTestLogger(), cfg: cfg, result: &Result{}, fsTimeout: 30 * time.Second}
	c.verifyDirectories(context.Background())

	for _, p := range []string{secondary, secondaryLog, filepath.Dir(secondary)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s exists after the preflight (err=%v): a disabled secondary was created", p, err)
		}
	}
	if _, err := os.Stat(cfg.BackupPath); err != nil {
		t.Fatalf("BACKUP_PATH not created: %v", err)
	}
}
