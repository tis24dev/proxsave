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
