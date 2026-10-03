package security

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// BASE_DIR/daemon_state is checked like identity/: created root-only when missing.
func TestVerifyDirectoriesCreatesDaemonStateRootOnly(t *testing.T) {
	baseDir := t.TempDir()
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)
	checker := &Checker{logger: logger, cfg: &config.Config{BaseDir: baseDir}, result: &Result{}}

	checker.verifyDirectories(context.Background())

	dir := filepath.Join(baseDir, "daemon_state")
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("daemon_state not created: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("daemon_state mode = %o, want 0700", got)
	}
	if !strings.Contains(buf.String(), "Created missing directory: "+dir) {
		t.Fatalf("missing the existing creation line for %s\n%s", dir, buf.String())
	}
}

// A daemon_state left 0755 is reported, and fixed to 0700 under AUTO_FIX_PERMISSIONS.
func TestVerifyDirectoriesFixesDaemonStateMode(t *testing.T) {
	for _, autoFix := range []bool{false, true} {
		name := "report"
		if autoFix {
			name = "auto-fix"
		}
		t.Run(name, func(t *testing.T) {
			baseDir := t.TempDir()
			dir := filepath.Join(baseDir, "daemon_state")
			if err := os.Mkdir(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			checker := &Checker{
				logger: newSecurityTestLogger(),
				cfg:    &config.Config{BaseDir: baseDir, AutoFixPermissions: autoFix},
				result: &Result{},
			}

			checker.verifyDirectories(context.Background())

			info, err := os.Stat(dir)
			if err != nil {
				t.Fatalf("stat daemon_state: %v", err)
			}
			if autoFix {
				if got := info.Mode().Perm(); got != 0o700 {
					t.Fatalf("daemon_state mode after auto-fix = %o, want 0700", got)
				}
				return
			}
			if got := info.Mode().Perm(); got != 0o755 {
				t.Fatalf("daemon_state mode changed without auto-fix: %o", got)
			}
			if !containsIssue(checker.result, "Directory "+dir+" should have permissions 700 (current 755)") {
				t.Fatalf("missing the permission warning for %s: %+v", dir, checker.result.Issues)
			}
		})
	}
}
