package orchestrator

import (
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/notify"
)

func TestClassifyTelegramSetupResult(t *testing.T) {
	cases := []struct {
		name         string
		res          notify.TelegramRegistrationResult
		wantCode     string
		wantMessage  string
		wantVerified bool
		wantPartial  bool
		wantFatal    bool
	}{
		{
			name:         "200-confirmed",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}, Provision: notify.TelegramProvisionConfirmed},
			wantCode:     "linked_confirmed",
			wantMessage:  "Linked successfully.",
			wantVerified: true,
		},
		{
			name:         "200-no-token",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}, Provision: notify.TelegramProvisionNoToken},
			wantCode:     "linked_confirmed",
			wantMessage:  "Linked successfully.",
			wantVerified: true,
		},
		{
			name:         "200-zero-value-provision",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}},
			wantCode:     "linked_confirmed",
			wantMessage:  "Linked successfully.",
			wantVerified: true,
		},
		{
			name:         "200-clean",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}, Provision: notify.TelegramProvisionClean},
			wantCode:     "linked_confirmed",
			wantMessage:  "Linked successfully.",
			wantVerified: true,
		},
		{
			name:         "200-persist-failed",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}, Provision: notify.TelegramProvisionPersistFailed},
			wantCode:     "linked_token_unsaved",
			wantMessage:  "Linked, but the relay token could not be saved locally. It will be reissued on the next backup.",
			wantVerified: true,
			wantPartial:  true,
		},
		{
			name:         "200-confirm-failed",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200}, Provision: notify.TelegramProvisionConfirmFailed},
			wantCode:     "linked_confirm_pending",
			wantMessage:  "Linked, but the relay-token confirmation did not complete. It will finish automatically on the next backup.",
			wantVerified: true,
			wantPartial:  true,
		},
		{
			name:        "403",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 403}},
			wantCode:    "bot_not_started",
			wantMessage: "Start the bot and send the Server ID, then press Check again.",
		},
		{
			name:        "409",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 409}},
			wantCode:    "not_associated",
			wantMessage: "Registration not associated yet. Send the Server ID to the bot, then press Check again.",
		},
		{
			name:        "422-fatal",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 422}},
			wantCode:    "invalid_server_id",
			wantMessage: "Invalid Server ID. Re-run the installer or regenerate the identity file.",
			wantFatal:   true,
		},
		{
			name:        "426-fatal",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 426}},
			wantCode:    "upgrade_required",
			wantMessage: "Upgrade ProxSave to v0.28.0 or later to complete pairing.",
			wantFatal:   true,
		},
		{
			name:        "code-0-connection-error",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 0, Error: errors.New("conn")}},
			wantCode:    "connection_error",
			wantMessage: "Could not reach the pairing server. Check connectivity and press Check again.",
		},
		{
			name:        "missing-server-id-identity",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: notify.StatusCodeMissingServerID, Error: errors.New("server ID missing")}},
			wantCode:    "missing_identity",
			wantMessage: "Server identity not found. Re-run the installer or regenerate the identity file.",
			wantFatal:   true,
		},
		{
			name:        "unexpected-500",
			res:         notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 500, Message: "x"}},
			wantCode:    "unexpected_response",
			wantMessage: "x",
		},
		{
			name:         "200-relay-only-chatless",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200, LinkState: notify.TelegramLinkStateRelayOnly}},
			wantCode:     "centralized_no_telegram",
			wantMessage:  "Centralized monitoring is active, but no Telegram chat is linked yet. Start the bot and send the Server ID to link a chat, then press Check again.",
			wantVerified: false,
		},
		{
			name:         "200-linked-explicit",
			res:          notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 200, LinkState: notify.TelegramLinkStateLinked}, Provision: notify.TelegramProvisionConfirmed},
			wantCode:     "linked_confirmed",
			wantMessage:  "Linked successfully.",
			wantVerified: true,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			st := ClassifyTelegramSetupResult(tt.res)
			if st.Code != tt.wantCode {
				t.Fatalf("Code=%q, want %q", st.Code, tt.wantCode)
			}
			if st.Message != tt.wantMessage {
				t.Fatalf("Message=%q, want %q", st.Message, tt.wantMessage)
			}
			if st.Verified != tt.wantVerified {
				t.Fatalf("Verified=%v, want %v", st.Verified, tt.wantVerified)
			}
			if st.Partial != tt.wantPartial {
				t.Fatalf("Partial=%v, want %v", st.Partial, tt.wantPartial)
			}
			if st.Fatal != tt.wantFatal {
				t.Fatalf("Fatal=%v, want %v", st.Fatal, tt.wantFatal)
			}
		})
	}
}

// TestClassifyTelegramSetupResult_RelayOnlyNotVerified pins the Option A fix: a
// chat-less 200 (centralized monitoring active, notify_secret issued, but NO
// Telegram chat) classifies as a DISTINCT, non-Verified, non-Fatal, retryable
// state, so the pairing wizard never shows green "Linked" for a host that will
// never receive a Telegram message. The LinkState discriminator is consulted
// BEFORE the provision switch, so it holds even when a relay secret was confirmed.
func TestClassifyTelegramSetupResult_RelayOnlyNotVerified(t *testing.T) {
	for _, prov := range []notify.TelegramProvisionOutcome{
		notify.TelegramProvisionNotApplicable,
		notify.TelegramProvisionNoToken,
		notify.TelegramProvisionConfirmed,
		notify.TelegramProvisionPersistFailed,
		notify.TelegramProvisionConfirmFailed,
		notify.TelegramProvisionClean,
	} {
		st := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{
			Status:    notify.TelegramRegistrationStatus{Code: 200, LinkState: notify.TelegramLinkStateRelayOnly},
			Provision: prov,
		})
		if st.Verified {
			t.Fatalf("relay-only 200 (provision=%d) must NOT be Verified", prov)
		}
		if st.Fatal {
			t.Fatalf("relay-only 200 (provision=%d) must NOT be Fatal (a later Check can link a chat)", prov)
		}
		if st.Partial {
			t.Fatalf("relay-only 200 (provision=%d) must NOT be Partial", prov)
		}
		if st.Code != "centralized_no_telegram" {
			t.Fatalf("relay-only 200 (provision=%d) Code=%q, want centralized_no_telegram", prov, st.Code)
		}
		if st.Severity != TelegramSeverityAction {
			t.Fatalf("relay-only 200 (provision=%d) Severity=%d, want Action(%d)", prov, st.Severity, TelegramSeverityAction)
		}
		if st.Label == "" {
			t.Fatalf("relay-only 200 (provision=%d) has an empty Label", prov)
		}
	}

	// A relay-only state must be DISTINCT from the linked verdict.
	relay := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{
		Status: notify.TelegramRegistrationStatus{Code: 200, LinkState: notify.TelegramLinkStateRelayOnly},
	})
	linked := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{
		Status:    notify.TelegramRegistrationStatus{Code: 200, LinkState: notify.TelegramLinkStateLinked},
		Provision: notify.TelegramProvisionConfirmed,
	})
	if relay.Code == linked.Code {
		t.Fatalf("relay-only and linked 200 must classify distinctly, both got %q", relay.Code)
	}
	if !linked.Verified || linked.Code != "linked_confirmed" || linked.Message != "Linked successfully." {
		t.Fatalf("linked 200 must stay Verified linked_confirmed \"Linked successfully.\", got Verified=%v Code=%q Message=%q", linked.Verified, linked.Code, linked.Message)
	}

	// Backward compat: a 200 with no LinkState (old server -> Unknown zero value)
	// keeps the legacy Verified behavior (chat_id fallback resolves linked upstream).
	unknown := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{
		Status: notify.TelegramRegistrationStatus{Code: 200},
	})
	if !unknown.Verified || unknown.Code != "linked_confirmed" {
		t.Fatalf("200 with Unknown LinkState must stay Verified linked_confirmed, got Verified=%v Code=%q", unknown.Verified, unknown.Code)
	}
}

// TestClassifyTelegramSetupResult_ConnectionDistinctFromUnexpected pins that a
// Code 0 (genuine connection failure) maps to a DISTINCT user-facing state than an
// unexpected non-2xx response, so the user is told to check connectivity rather
// than shown a raw upstream body. A missing server identity is a separate sentinel
// (StatusCodeMissingServerID), classified as missing_identity, not connection_error.
func TestClassifyTelegramSetupResult_ConnectionDistinctFromUnexpected(t *testing.T) {
	conn := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 0, Error: errors.New("conn")}})
	other := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 500, Message: "x"}})

	if conn.Code == other.Code {
		t.Fatalf("connection error and unexpected response must classify distinctly, both got %q", conn.Code)
	}
	if conn.Code != "connection_error" {
		t.Fatalf("Code 0 classified as %q, want connection_error", conn.Code)
	}
	if other.Code != "unexpected_response" {
		t.Fatalf("Code 500 classified as %q, want unexpected_response", other.Code)
	}
}

// TestClassifyTelegramSetupResult_Severities locks the per-state severities so the
// UI can render distinct labels/colors: "server unreachable" must NOT look like
// "not paired yet" (the user's requirement).
func TestClassifyTelegramSetupResult_Severities(t *testing.T) {
	sev := func(code int) TelegramSetupSeverity {
		return ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: code}}).Severity
	}
	if sev(0) == sev(409) {
		t.Fatal("connection-error and not-associated must be DISTINCT severities")
	}
	for _, tc := range []struct {
		code int
		want TelegramSetupSeverity
	}{
		{200, TelegramSeveritySuccess},
		{403, TelegramSeverityAction},
		{409, TelegramSeverityAction},
		{0, TelegramSeverityUnreachable},
		{500, TelegramSeverityUnreachable},
		{422, TelegramSeverityFatal},
		{426, TelegramSeverityFatal},
	} {
		if got := sev(tc.code); got != tc.want {
			t.Errorf("code %d severity = %d, want %d", tc.code, got, tc.want)
		}
	}
	// Every non-neutral classified state must carry a display label.
	for _, code := range []int{200, 403, 409, 0, 500, 422, 426} {
		st := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: code}})
		if st.Label == "" {
			t.Errorf("code %d has an empty Label", code)
		}
	}
}

// TestClassifyTelegramSetupResult_TruncatesUnexpectedMessage verifies the raw,
// untrusted upstream message in the unexpected_response branch is truncated to the
// shared bound before it reaches either UI.
func TestClassifyTelegramSetupResult_TruncatesUnexpectedMessage(t *testing.T) {
	long := strings.Repeat("x", 320)
	st := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 599, Message: long}})

	if st.Code != "unexpected_response" {
		t.Fatalf("Code=%q, want unexpected_response", st.Code)
	}
	if !strings.HasSuffix(st.Message, "...(truncated)") {
		t.Fatalf("expected truncated message, got %q", st.Message)
	}
	if runes := []rune(st.Message); len(runes) > TelegramSetupStatusMessageMaxRunes {
		t.Fatalf("message length=%d runes, want <= %d", len(runes), TelegramSetupStatusMessageMaxRunes)
	}
}

// TestClassifyTelegramSetupResult_SanitizesUnexpectedBody pins that the shared
// classifier strips terminal/control sequences from untrusted upstream text, so
// the TUI (which only tview.Escapes) cannot be garbled or injected by a hostile
// relay response.
func TestClassifyTelegramSetupResult_SanitizesUnexpectedBody(t *testing.T) {
	raw := "weird\x1b[31m body\x07\twith\x00controls\x9b2K"
	st := ClassifyTelegramSetupResult(notify.TelegramRegistrationResult{Status: notify.TelegramRegistrationStatus{Code: 500, Message: raw}})

	if st.Code != "unexpected_response" {
		t.Fatalf("Code=%q, want unexpected_response", st.Code)
	}
	if st.Message == "" {
		t.Fatalf("expected a non-empty sanitized message")
	}
	for _, r := range st.Message {
		if r == 0x1b || r == 0x9b || r == 0x07 || r == 0x00 || r == '\t' {
			t.Fatalf("classifier left a control/escape byte (%#x) in Message: %q", r, st.Message)
		}
	}
}
