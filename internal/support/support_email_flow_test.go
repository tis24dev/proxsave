package support

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// Keep the real notification adapter and replace only the delivery boundary.
// The Email name also exercises the adapter's recording of EmailStatus.
type supportDeliveryStub struct{ fakeNotifier }

func (*supportDeliveryStub) Name() string { return "Email" }

// These tests modify package globals and must not run in parallel.
func captureSupportEmailFlow(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	originalFactory, originalRecipient := newEmailNotifier, supportEmail
	originalLogger := logging.GetDefaultLogger()
	t.Cleanup(func() {
		newEmailNotifier, supportEmail = originalFactory, originalRecipient
		logging.SetDefaultLogger(originalLogger)
	})
	output := new(bytes.Buffer)
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(output)
	logging.SetDefaultLogger(logger)
	supportEmail = "maintainer@example.invalid"
	return logger, output
}

func TestSendEmailDeliveryOutcome(t *testing.T) {
	transportError := errors.New("sendmail rejected message: exit status 75")
	for _, tc := range []struct {
		name        string
		enabled     bool
		result      *notify.NotificationResult
		err         error
		exitCode    int
		wantSent    int
		wantStatus  string
		wantLog     string
		wantHandoff bool
	}{
		{"accepted", true, &notify.NotificationResult{Success: true, Method: "email-sendmail"}, nil, 0, 1, "ok", "", true},
		{"accepted_after_failed_run", true, &notify.NotificationResult{Success: true, Method: "email-sendmail"}, nil, 2, 1, "ok", "", true},
		{"unsuccessful_result", true, &notify.NotificationResult{Success: false, Method: "email-sendmail", Error: transportError}, nil, 0, 1, "error", transportError.Error(), false},
		{"unsuccessful_result_without_detail", true, &notify.NotificationResult{Success: false, Method: "email-sendmail"}, nil, 0, 1, "error", "notification failed", false},
		{"transport_error", true, nil, transportError, 0, 1, "error", transportError.Error(), false},
		{"disabled", false, nil, nil, 0, 0, "", "disabled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, output := captureSupportEmailFlow(t)
			delivery := &supportDeliveryStub{fakeNotifier{enabled: tc.enabled, result: tc.result, err: tc.err}}
			newEmailNotifier = func(notify.EmailConfig, types.ProxmoxType, *logging.Logger) (notify.Notifier, error) {
				return delivery, nil
			}
			stats := &orchestrator.BackupStats{ExitCode: tc.exitCode, Hostname: "pve-test"}
			SendEmail(context.Background(), nil, logger, types.ProxmoxVE, stats, Meta{}, "")
			if delivery.sent != tc.wantSent {
				t.Fatalf("delivery calls = %d; want %d", delivery.sent, tc.wantSent)
			}
			if stats.EmailStatus != tc.wantStatus {
				t.Errorf("EmailStatus = %q; want %q", stats.EmailStatus, tc.wantStatus)
			}
			if stats.ExitCode != tc.exitCode {
				t.Errorf("email outcome changed the run exit code: %d", stats.ExitCode)
			}
			if tc.wantLog != "" && !strings.Contains(output.String(), tc.wantLog) {
				t.Errorf("missing delivery diagnostic %q in log:\n%s", tc.wantLog, output)
			}
			// A nil adapter error alone is not evidence that the MTA accepted mail.
			handoff := strings.Contains(output.String(), "support email handed off to local MTA")
			if handoff != tc.wantHandoff {
				t.Errorf("support handoff announcement = %v; want %v (EmailStatus=%q)\n%s", handoff, tc.wantHandoff, stats.EmailStatus, output)
			}
		})
	}
}

func TestSendEmailPreflightStops(t *testing.T) {
	for _, tc := range []struct {
		name        string
		recipient   string
		nilStats    bool
		factoryErr  error
		wantFactory int
		wantLog     string
	}{
		{"missing_stats", "maintainer@example.invalid", true, nil, 0, "stats are nil"},
		{"missing_recipient", "", false, nil, 0, "recipient is not configured"},
		{"blank_recipient", " \t\n", false, nil, 0, "recipient is not configured"},
		{"initialization_error", "maintainer@example.invalid", false, errors.New("notifier initialization failed"), 1, "notifier initialization failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, output := captureSupportEmailFlow(t)
			supportEmail = tc.recipient
			factoryCalls := 0
			delivery := &supportDeliveryStub{fakeNotifier{enabled: true}}
			newEmailNotifier = func(notify.EmailConfig, types.ProxmoxType, *logging.Logger) (notify.Notifier, error) {
				factoryCalls++
				return delivery, tc.factoryErr
			}
			stats := &orchestrator.BackupStats{ExitCode: 2}
			if tc.nilStats {
				stats = nil
			}
			SendEmail(context.Background(), nil, logger, types.ProxmoxVE, stats, Meta{}, "")
			if factoryCalls != tc.wantFactory || delivery.sent != 0 {
				t.Errorf("factory calls = %d, delivery calls = %d; want %d, 0", factoryCalls, delivery.sent, tc.wantFactory)
			}
			if !strings.Contains(output.String(), tc.wantLog) {
				t.Errorf("missing preflight diagnostic %q in log:\n%s", tc.wantLog, output)
			}
			if strings.Contains(output.String(), "support email handed off") {
				t.Errorf("preflight failure announced a handoff:\n%s", output)
			}
		})
	}
}

func TestSendEmailSupportPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		meta    Meta
		build   string
		cfg     *config.Config
		subject string
	}{
		{"no_metadata", Meta{}, "", nil, "SUPPORT REQUEST"},
		{"nickname_only", Meta{GitHubUser: " alice "}, "", nil, "SUPPORT REQUEST - Nickname: alice"},
		{"issue_only", Meta{IssueID: " #42 "}, "", nil, "SUPPORT REQUEST - Issue: #42"},
		{"build_only", Meta{}, " test-build ", nil, "SUPPORT REQUEST - Build: test-build"},
		{"blank_metadata", Meta{GitHubUser: " \t", IssueID: " "}, " ", nil, "SUPPORT REQUEST"},
		{"complete", Meta{GitHubUser: " alice ", IssueID: " #42 "}, " test-build ", &config.Config{EmailFrom: "host@example.invalid", EmailRecipient: "operator@example.invalid"}, "SUPPORT REQUEST - Nickname: alice - Issue: #42 - Build: test-build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, _ := captureSupportEmailFlow(t)
			supportEmail = " maintainer@example.invalid "
			logPath := filepath.Join(t.TempDir(), "support.log")
			if err := logger.OpenLogFile(logPath); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = logger.CloseLogFile() })
			logger.Debug("support diagnostic marker")
			start := time.Unix(1700000000, 0)
			stats := BuildSupportStats(logger, "pbs-test", types.ProxmoxBS, "4.0", "test-build", start, start.Add(5*time.Second), 2, "restore")
			var captured notify.EmailConfig
			delivery := &supportDeliveryStub{fakeNotifier{enabled: true}}
			newEmailNotifier = func(cfg notify.EmailConfig, platform types.ProxmoxType, gotLogger *logging.Logger) (notify.Notifier, error) {
				captured = cfg
				if platform != types.ProxmoxBS || gotLogger != logger {
					t.Error("support delivery lost the platform or logger")
				}
				return delivery, nil
			}
			SendEmail(context.Background(), tc.cfg, logger, types.ProxmoxBS, stats, tc.meta, tc.build)
			if !captured.Enabled || !captured.AttachLogFile || captured.FallbackSendmail || captured.DeliveryMethod != notify.EmailDeliverySendmail {
				t.Errorf("support must use enabled local sendmail with the log attached and no fallback: %+v", captured)
			}
			if captured.Recipient != "maintainer@example.invalid" || captured.SubjectOverride != tc.subject {
				t.Errorf("recipient = %q, subject = %q; want maintainer recipient and %q", captured.Recipient, captured.SubjectOverride, tc.subject)
			}
			wantFrom := ""
			if tc.cfg != nil {
				wantFrom = tc.cfg.EmailFrom
			}
			if captured.From != wantFrom {
				t.Errorf("From = %q; want %q", captured.From, wantFrom)
			}
			if delivery.sent != 1 || delivery.last == nil {
				t.Fatal("support payload was not sent exactly once")
			}
			data := delivery.last
			if data.LogFilePath != logPath || data.Hostname != "pbs-test" || data.ProxmoxType != types.ProxmoxBS {
				t.Errorf("support payload lost log path or host identity: %+v", data)
			}
			if data.ExitCode != 2 || data.Status != notify.StatusFailure || data.BackupDuration != 5*time.Second || !data.BackupDate.Equal(start) {
				t.Errorf("support payload lost the failed run outcome or timing: %+v", data)
			}
		})
	}
}

func TestSendEmailRealNotifierRejectsInvalidRecipient(t *testing.T) {
	logger, output := captureSupportEmailFlow(t)
	// Exercise SendEmail -> adapter -> EmailNotifier.Send without a delivery stub.
	// This invalid, nonempty address reaches the real notifier's validation and
	// is rejected before any mail process or network request can start.
	supportEmail = "invalid-recipient"
	stats := &orchestrator.BackupStats{Hostname: "pve-test", ExitCode: 2}
	SendEmail(context.Background(), nil, logger, types.ProxmoxVE, stats, Meta{}, "test-build")
	if stats.EmailStatus != "error" || !strings.Contains(output.String(), "invalid email recipient format") {
		t.Fatalf("real notifier must reject the malformed recipient; EmailStatus = %q\n%s", stats.EmailStatus, output)
	}
	if strings.Contains(output.String(), "support email handed off to local MTA") {
		t.Errorf("rejected recipient falsely announced a support email handoff:\n%s", output)
	}
}

func TestSendEmailDoesNotReusePreviousOutcome(t *testing.T) {
	for _, tc := range []struct {
		name        string
		previous    string
		enabled     bool
		success     bool
		wantStatus  string
		wantHandoff bool
	}{
		{"disabled_after_success", "ok", false, false, "ok", false},
		{"failure_after_success", "ok", true, false, "error", false},
		{"success_after_failure", "error", true, true, "ok", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger, output := captureSupportEmailFlow(t)
			delivery := &supportDeliveryStub{fakeNotifier{
				enabled: tc.enabled,
				result:  &notify.NotificationResult{Success: tc.success, Method: "email-sendmail"},
			}}
			newEmailNotifier = func(notify.EmailConfig, types.ProxmoxType, *logging.Logger) (notify.Notifier, error) {
				return delivery, nil
			}
			stats := &orchestrator.BackupStats{
				Hostname: "pve-test", EmailStatus: tc.previous,
				NotifyResults: map[string]string{"Email": tc.previous},
			}
			SendEmail(context.Background(), nil, logger, types.ProxmoxVE, stats, Meta{}, "")
			if stats.EmailStatus != tc.wantStatus || stats.NotifyResults["Email"] != tc.wantStatus {
				t.Errorf("recorded email outcome = %q, %q; want %q", stats.EmailStatus, stats.NotifyResults["Email"], tc.wantStatus)
			}
			handoff := strings.Contains(output.String(), "support email handed off to local MTA")
			if handoff != tc.wantHandoff {
				t.Errorf("support handoff announcement = %v; want %v after previous outcome %q\n%s", handoff, tc.wantHandoff, tc.previous, output)
			}
		})
	}
}
