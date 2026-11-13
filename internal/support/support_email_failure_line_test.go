package support

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// A failed support email showed only the adapter's "❌ Email: notification failed":
// its CRITICAL branch waited for an error the adapter never returns. The failure now
// gets its own WARNING, naming the support email and its recipient.
func TestSupportEmailFailureHasItsOwnWarning(t *testing.T) {
	logger, output := captureSupportEmailFlow(t)
	delivery := &supportDeliveryStub{fakeNotifier{enabled: true, result: &notify.NotificationResult{Success: false, Method: "email-sendmail", Error: errors.New("sendmail rejected message: exit status 75")}}}
	newEmailNotifier = func(notify.EmailConfig, types.ProxmoxType, *logging.Logger) (notify.Notifier, error) {
		return delivery, nil
	}
	SendEmail(context.Background(), nil, logger, types.ProxmoxVE, &orchestrator.BackupStats{Hostname: "pve-test"}, Meta{}, "")
	if !strings.Contains(output.String(), "WARNING  Support mode: FAILED to send support email to maintainer@example.invalid\n") {
		t.Fatalf("no support failure line; log:\n%s", output.String())
	}
}
