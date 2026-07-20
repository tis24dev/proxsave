package orchestrator

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/types"
)

// notifyScopeFailingNotifier is an enabled, non-critical notifier whose Send always
// fails, to exercise the adapter's send-error path.
type notifyScopeFailingNotifier struct{}

func (notifyScopeFailingNotifier) Name() string     { return "FakeChannel" }
func (notifyScopeFailingNotifier) IsEnabled() bool  { return true }
func (notifyScopeFailingNotifier) IsCritical() bool { return false }
func (notifyScopeFailingNotifier) Send(ctx context.Context, data *notify.NotificationData) (*notify.NotificationResult, error) {
	return nil, errors.New("connection refused")
}

// A channel Send failure during notification dispatch must be recorded as a NOTIFY-ERR
// (notifyCount) and NOT as a backup error (errorCount): dispatchNotifications brackets
// the whole (shared-logger) dispatch in a notify-error scope.
func TestDispatchNotifications_SendErrorIsNotifyError(t *testing.T) {
	base := logging.New(types.LogLevelDebug, false)
	base.SetOutput(io.Discard)

	orch := New(base, false)
	orch.RegisterNotificationChannel(NewNotificationAdapter(notifyScopeFailingNotifier{}, base))

	orch.dispatchNotifications(context.Background(), &BackupStats{ExitCode: types.ExitSuccess.Int()})

	if base.NotifyCount() == 0 {
		t.Fatalf("a channel Send failure during dispatch must be a NOTIFY-ERR (notifyCount>0), got %d", base.NotifyCount())
	}
	if base.ErrorCount() != 0 {
		t.Fatalf("a channel Send failure must NOT count as a backup error, got errorCount=%d", base.ErrorCount())
	}
}
