package wizard

import (
	"context"
	"testing"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestRunPostInstallAuditWizard_PassesContextToRunner(t *testing.T) {
	origRunner := postInstallAuditWizardRunner
	t.Cleanup(func() {
		postInstallAuditWizardRunner = origRunner
	})

	ctx := t.Context()
	postInstallAuditWizardRunner = func(gotCtx context.Context, app *tui.App, root, focus tview.Primitive) error {
		if gotCtx != ctx {
			t.Fatalf("got context %p, want %p", gotCtx, ctx)
		}
		return nil
	}

	result, err := RunPostInstallAuditWizard(ctx, "/tmp/proxsave", "/tmp/backup.env", "sig")
	if err != nil {
		t.Fatalf("RunPostInstallAuditWizard error: %v", err)
	}
	if result.Ran {
		t.Fatalf("expected Ran=false when runner exits without selecting an action")
	}
}
