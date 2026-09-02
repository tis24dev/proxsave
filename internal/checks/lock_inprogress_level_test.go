package checks

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The in-progress lock fact reaches the operator once, through the caller's
// "✗ Lock File (BACKUP_IN_PROGRESS): ..." line (logResult in the orchestrator).
// CheckLockFile used to ALSO log the bare message at ERROR right before returning
// it, so one benign concurrency skip recapped errors=2 with two red lines saying
// the same thing. The bare pre-log stays for debugging, at Debug.
func TestCheckLockFileInProgressPreLogIsDebugNotError(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)

	lockDir := t.TempDir()
	lockPath := filepath.Join(lockDir, ".backup.lock")
	host, _ := os.Hostname()
	content := fmt.Sprintf("pid=%d\nhost=%s\ntime=2026-09-02T17:00:00+02:00\n", os.Getpid(), host)
	if err := os.WriteFile(lockPath, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}

	c := NewChecker(logger, &CheckerConfig{
		BackupPath:   lockDir,
		LogPath:      lockDir,
		LockDirPath:  lockDir,
		LockFilePath: lockPath,
		SafetyFactor: 1.0,
		MaxLockAge:   time.Hour,
	})
	result := c.CheckLockFile()

	if result.Passed || result.Code != CheckCodeBackupInProgress {
		t.Fatalf("expected the in-progress arm (Passed=false, Code=%s), got Passed=%v Code=%q", CheckCodeBackupInProgress, result.Passed, result.Code)
	}

	var debugSeen bool
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "Another backup is in progress") {
			continue
		}
		_, rest, found := strings.Cut(line, "] ")
		if !found {
			continue
		}
		switch strings.Fields(rest)[0] {
		case "ERROR":
			t.Fatalf("the bare in-progress message still renders at ERROR, duplicating the caller's ✗ line:\n%s", line)
		case "DEBUG":
			debugSeen = true
		}
	}
	if !debugSeen {
		t.Fatalf("the bare in-progress message vanished entirely; it must stay at Debug:\n%s", buf.String())
	}
}
