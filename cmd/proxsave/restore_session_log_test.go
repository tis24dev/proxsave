package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// The restore's session log is the only record of what a restore did, and the run
// closes by recommending a reboot that empties a tmpfs /tmp. It goes into the
// restore's own directory, the one the workflow writes its safety backup, rollback
// archives and detailed logs to, not under /tmp.
func TestRestoreSessionLogLivesOutsideTmp(t *testing.T) {
	// Same directory as the workflow's safety backup, rollback archives and logs:
	// orchestrator.RestoreRunDir, fixed on its first call for the whole process.
	if got, want := restoreRunDir(), orchestrator.RestoreRunDir(); got != want {
		t.Fatalf("restoreRunDir() = %s, want orchestrator.RestoreRunDir() = %s", got, want)
	}
	if got := restoreRunDir(); !strings.HasPrefix(got, "/var/lib/proxsave/restore/") {
		t.Fatalf("restoreRunDir() = %s, want a /var/lib/proxsave/restore/<ts> directory", got)
	}

	runDir := filepath.Join(t.TempDir(), "restore", "20260926_100000")
	origRunDir := restoreRunDir
	restoreRunDir = func() string { return runDir }
	t.Cleanup(func() { restoreRunDir = origRunDir })
	t.Setenv("LOG_FILE", "")
	fallback := logging.New(types.LogLevelInfo, false)
	fallback.SetOutput(io.Discard)
	rt := &appRuntime{bootstrap: logging.NewBootstrapLogger(), cfg: &config.Config{}, logLevel: types.LogLevelInfo}

	logger := initializeRestoreSessionLogger(rt, fallback)
	if rt.sessionLogCloser != nil {
		t.Cleanup(rt.sessionLogCloser)
	}
	path := os.Getenv("LOG_FILE")
	if logger == fallback || path == "" {
		t.Fatalf("restore session log not started (LOG_FILE=%q)", path)
	}
	if filepath.Dir(path) != runDir {
		t.Errorf("restore session log %s is not in the restore run directory %s", path, runDir)
	}
	if info, err := os.Stat(runDir); err != nil {
		t.Errorf("stat %s: %v", runDir, err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("restore run directory mode = %o, want 700", got)
	}
	if info, err := os.Stat(path); err != nil {
		t.Errorf("stat %s: %v", path, err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("restore session log mode = %o, want 600", got)
	}
}
