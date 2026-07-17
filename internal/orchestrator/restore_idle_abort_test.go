package orchestrator

import (
	"bufio"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestSelectRestoreModeAbortsWhenIdle proves the first restore-mode menu no longer
// hangs forever when the operator walks away. The idle read fires ErrIdleTimeout
// (which wraps input.ErrInputAborted) and the menu's local abort boundary in
// selective.go remaps it to the graceful ErrRestoreAborted sentinel, so an
// unattended restore aborts with zero mutation.
//
// We assert ErrRestoreAborted (not input.ErrInputAborted): the surrounding error
// handling is left unchanged, and that local boundary deliberately swallows the
// ErrInputAborted identity when it returns the fresh ErrRestoreAborted sentinel.
func TestSelectRestoreModeAbortsWhenIdle(t *testing.T) {
	orig := cliIdleTimeout
	cliIdleTimeout = time.Millisecond
	t.Cleanup(func() { cliIdleTimeout = orig })

	pr, pw := io.Pipe()
	defer pw.Close() // never deliver a line -> idle fires
	logger := logging.New(types.LogLevelError, false)

	_, err := ShowRestoreModeMenuWithReader(context.Background(), bufio.NewReader(pr), logger, SystemTypePVE)
	if !errors.Is(err, ErrRestoreAborted) {
		t.Fatalf("idle read must map to a graceful restore abort (ErrRestoreAborted); got %v", err)
	}
}

// TestPromptClusterRestoreModeAbortsWhenIdle proves a delegated restore helper with
// no local abort boundary (it returns the raw read error, which the restore master
// normalizer later maps to ErrRestoreAborted) is now idle-bounded. The idle read
// returns ErrIdleTimeout, which rides the input.ErrInputAborted boundary.
func TestPromptClusterRestoreModeAbortsWhenIdle(t *testing.T) {
	orig := cliIdleTimeout
	cliIdleTimeout = time.Millisecond
	t.Cleanup(func() { cliIdleTimeout = orig })

	pr, pw := io.Pipe()
	defer pw.Close() // never deliver a line -> idle fires

	_, err := promptClusterRestoreMode(context.Background(), bufio.NewReader(pr))
	if !errors.Is(err, input.ErrInputAborted) {
		t.Fatalf("idle read must map to a graceful abort (ErrInputAborted); got %v", err)
	}
	if !errors.Is(err, input.ErrIdleTimeout) {
		t.Fatalf("idle read should carry ErrIdleTimeout identity; got %v", err)
	}
}
