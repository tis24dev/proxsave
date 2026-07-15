package main

import (
	"context"
	"io"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// TestSkipOptionalInstallStepOnAbort_CancelledCtxAborts pins F10-01: when the run
// context was cancelled (Ctrl+C / SIGINT), a prompt error from an OPTIONAL install
// step must abort the install so runInstall stops before the cron/scheduler
// finalization and the footer shows "Installation aborted". Continuing here would
// run the finalization on a dead context, install NO scheduler, yet report a
// false-green "Installation completed".
func TestSkipOptionalInstallStepOnAbort_CancelledCtxAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := skipOptionalInstallStepOnAbort(ctx, logging.NewBootstrapLogger(), "Telegram setup", io.EOF)
	if err == nil {
		t.Fatal("cancelled context must abort the install, got nil (false-green)")
	}
	if !isInstallAbortedError(err) {
		t.Fatalf("returned error must be recognized as an install abort, got %v", err)
	}
	title, level := installBanner(err)
	if level != installBannerAborted || title != "Installation aborted" {
		t.Fatalf("banner = (%q, %v), want (%q, aborted)", title, level, "Installation aborted")
	}
}

// TestSkipOptionalInstallStepOnAbort_LiveCtxSkips pins the benign half of the
// contract: a plain input abort (Ctrl-D/EOF) with the run context still live is
// non-blocking (the config is already written and the step is accessory), so the
// install continues to finalization and the caller gets nil.
func TestSkipOptionalInstallStepOnAbort_LiveCtxSkips(t *testing.T) {
	err := skipOptionalInstallStepOnAbort(context.Background(), logging.NewBootstrapLogger(), "Telegram setup", io.EOF)
	if err != nil {
		t.Fatalf("benign EOF with a live context must be non-blocking, got %v", err)
	}
}
