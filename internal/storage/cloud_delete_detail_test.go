package storage

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A SIGKILLed rclone (cmd.WaitDelay in defaultExecCommand kills the process on
// context cancellation) returns whatever it printed so far - typically a NOTICE -
// plus an exec error of "signal: killed". The delete warning used to print only the
// captured output, so the NOTICE was presented as the failure cause and the kill
// appeared on no line at any level.
func TestKilledRcloneDeleteNamesTheKill(t *testing.T) {
	l := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	l.SetOutput(buf)

	// remoteFiles left nil: the snapshot is not ready, so every candidate is attempted.
	c := &CloudStorage{
		config: &config.Config{}, logger: l,
		remote: "gdrive", remotePrefix: "proxsave/backup",
	}
	c.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("2026/09/02 01:00:00 NOTICE: Waiting for transfers to finish"), errors.New("signal: killed")
	}

	_ = c.Delete(context.Background(), "gdrive:proxsave/backup/pve01-backup-20260825-020000.tar.zst")

	var warned string
	for _, ln := range strings.Split(buf.String(), "\n") {
		if strings.Contains(ln, "failed to delete") && strings.Contains(ln, "WARNING") {
			warned = ln
			break
		}
	}
	if warned == "" {
		t.Fatalf("no delete warning at all:\n%s", buf.String())
	}
	if !strings.Contains(warned, "signal: killed") {
		t.Fatalf("the kill is on no line: the NOTICE is presented as the cause:\n%s", warned)
	}
	if !strings.Contains(warned, "NOTICE") {
		t.Fatalf("rclone's own output was dropped from the line:\n%s", warned)
	}
	// The console line already carries the timestamp in its own column; rclone's
	// leading "2026/09/02 01:00:00" inside the message says it twice.
	if strings.Contains(warned, "01:00:00") {
		t.Fatalf("rclone's own timestamp survived inside the message body:\n%s", warned)
	}
}

// The common failure keeps its clean single-cause shape: rclone's stderr names the
// reason and the exec error beside it says only "exit status N", which adds nothing.
func TestExitStatusDeleteKeepsTheSingleCause(t *testing.T) {
	l := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	l.SetOutput(buf)

	c := &CloudStorage{
		config: &config.Config{}, logger: l,
		remote: "gdrive", remotePrefix: "proxsave/backup",
	}
	c.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("2026/09/02 01:00:00 ERROR : insufficientFilePermissions"), errors.New("exit status 3")
	}

	_ = c.Delete(context.Background(), "gdrive:proxsave/backup/pve01-backup-20260825-020000.tar.zst")

	for _, ln := range strings.Split(buf.String(), "\n") {
		if strings.Contains(ln, "failed to delete") && strings.Contains(ln, "WARNING") {
			if strings.Contains(ln, "exit status") {
				t.Fatalf("exit status N carries no information and came back:\n%s", ln)
			}
			return
		}
	}
	t.Fatalf("no delete warning at all:\n%s", buf.String())
}
