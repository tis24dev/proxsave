package install

import (
	"context"
	"fmt"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
)

// A dry-run stopped by the operator showed the green "✓ NO UNUSED COMPONENTS". It now
// shows NOT CHECKED with the reason; a failed or timed-out dry-run keeps CHECK FAILED.
func TestRunPostInstallAuditCancelledDryRunIsNotChecked(t *testing.T) {
	d := newDriver(t)
	origCollect := auditCollect
	auditCollect = func(ctx context.Context, execPath, cfgPath string) ([]installer.PostInstallAuditSuggestion, error) {
		return nil, fmt.Errorf("post-install audit dry-run failed: %w", context.Canceled)
	}
	t.Cleanup(func() { auditCollect = origCollect })

	done := make(chan installer.PostInstallAuditResult, 1)
	go func() {
		res, _ := RunPostInstallAudit(t.Context(), d.session, "/fake/proxsave", "/fake/backup.env", false)
		done <- res
	}()
	d.waitScreen("Post-install check")
	d.keys("enter")
	d.waitText("NOT CHECKED")
	d.waitText("The dry-run was cancelled before it finished.")
	d.keys("enter")
	if res := <-done; res.CollectErr == nil {
		t.Fatalf("the cancelled dry-run must be recorded on CollectErr, got %+v", res)
	}
}
