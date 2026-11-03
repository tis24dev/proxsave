package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// Ctrl+C on the TUI Healthchecks step came back as "check skipped" (nil) or as the
// spinner's context.Canceled, so the install went on into finalization on a closed
// session and ended "Installation failed" (exit 2) without restoring backup.env. A
// closed session after the step is now the abort the other steps already report.
func TestClosedSessionAfterAStepAbortsTheInstall(t *testing.T) {
	closed := make(chan struct{})
	close(closed)
	open := make(chan struct{})
	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)

	for _, stepErr := range []error{nil, context.Canceled} {
		err := abortInstallOnOptionalStep(context.Background(), bootstrap, "Healthcheck setup", closedSessionIsAbort(closed, stepErr, bootstrap))
		if !isInstallAbortedError(err) {
			t.Fatalf("closed session, step err %v: got %v, want the aborted-install error", stepErr, err)
		}
	}
	if err := abortInstallOnOptionalStep(context.Background(), bootstrap, "Healthcheck setup", closedSessionIsAbort(open, nil, bootstrap)); err != nil {
		t.Fatalf("open session, skipped step: got %v, want the install to continue", err)
	}
	if err := closedSessionIsAbort(open, errors.New("relay unreachable"), bootstrap); err == nil || err.Error() != "relay unreachable" {
		t.Fatalf("open session keeps the step error, got %v", err)
	}
}
