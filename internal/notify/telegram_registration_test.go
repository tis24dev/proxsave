package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/version"
)

func TestCheckTelegramRegistrationMissingServerID(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	status := CheckTelegramRegistration(context.Background(), "https://central.test", "", logger)

	if status.Code != StatusCodeMissingServerID || status.Error == nil {
		t.Fatalf("expected missing server ID sentinel, got %+v", status)
	}
}

func TestCheckTelegramRegistrationResponses(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	cases := []struct {
		name        string
		statusCode  int
		expectCode  int
		expectError bool
	}{
		{"200-ok", http.StatusOK, 200, false},
		{"403-first-comm", http.StatusForbidden, 403, true},
		{"409-missing-reg", http.StatusConflict, 409, true},
		{"422-invalid", http.StatusUnprocessableEntity, 422, true},
		{"426-upgrade", http.StatusUpgradeRequired, 426, true},
		{"500-unexpected", http.StatusInternalServerError, 500, true},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.name))
			}))
			defer server.Close()

			status := CheckTelegramRegistration(context.Background(), server.URL, "server-123", logger)
			if status.Code != tt.expectCode {
				t.Fatalf("Code=%d, want %d", status.Code, tt.expectCode)
			}
			if tt.expectError && status.Error == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.expectError && status.Error != nil {
				t.Fatalf("unexpected error: %v", status.Error)
			}
		})
	}
}

// TestCheckTelegramRegistrationSendsVersionHeader verifies the GET get-chat-id
// request from CheckTelegramRegistration carries X-Proxsave-Version.
func TestCheckTelegramRegistrationSendsVersionHeader(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	var captured, capturedProvision string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = r.Header.Get("X-Proxsave-Version")
		capturedProvision = r.Header.Get("X-Proxsave-Provision")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	CheckTelegramRegistration(context.Background(), server.URL, "server-123", logger)

	if captured == "" {
		t.Fatalf("X-Proxsave-Version header was not set")
	}
	if captured != version.String() {
		t.Fatalf("X-Proxsave-Version = %q, want %q", captured, version.String())
	}
	// The bare status probe must never carry provision-intent.
	if capturedProvision != "" {
		t.Fatalf("X-Proxsave-Provision = %q, want empty on the public status path", capturedProvision)
	}
}

// TestCheckTelegramRegistrationLinkStateDiscriminator locks the linked-vs-relay-only
// signal interpretation on a 200: Code stays 200 for BOTH so the provisioning gates
// keep firing; LinkState and Message distinguish chat-linked from chat-less
// (Option A). It covers all four contract paths: link_state present ("linked" /
// "relay_only") takes precedence, and link_state absent falls back to chat_id. It
// also proves notify_secret is NOT a discriminator (present in the relay-only case
// yet LinkState stays RelayOnly, and present in a linked case yet LinkState stays
// Linked).
func TestCheckTelegramRegistrationLinkStateDiscriminator(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)

	const linkedMsg = "200 - Registration active"
	const relayMsg = "200 - Relay provisioned (no Telegram chat)"

	cases := []struct {
		name        string
		body        string
		wantState   TelegramLinkState
		wantMessage string
	}{
		{
			// Chat-linked: chat_id present, no link_state. notify_secret also present
			// (linked hosts get one on provision) yet the host is still Linked -> proves
			// notify_secret is not the discriminator.
			name:        "linked-chat-id-no-link-state",
			body:        `{"chat_id":"123456","notify_secret":"` + provisionTestSecret + `"}`,
			wantState:   TelegramLinkStateLinked,
			wantMessage: linkedMsg,
		},
		{
			// Chat-less (Option A): notify_secret issued, NO chat_id, no link_state.
			name:        "relay-only-secret-no-chat-id",
			body:        `{"notify_secret":"` + provisionTestSecret + `"}`,
			wantState:   TelegramLinkStateRelayOnly,
			wantMessage: relayMsg,
		},
		{
			// link_state="relay_only" wins even though a chat_id is present (precedence
			// of the explicit server field over the chat_id fallback).
			name:        "link-state-relay-only-overrides-chat-id",
			body:        `{"chat_id":"123456","notify_secret":"` + provisionTestSecret + `","link_state":"relay_only"}`,
			wantState:   TelegramLinkStateRelayOnly,
			wantMessage: relayMsg,
		},
		{
			// link_state="linked" wins even with NO chat_id (precedence, linked direction).
			name:        "link-state-linked-overrides-missing-chat-id",
			body:        `{"notify_secret":"` + provisionTestSecret + `","link_state":"linked"}`,
			wantState:   TelegramLinkStateLinked,
			wantMessage: linkedMsg,
		},
		{
			// link_state absent, no chat_id -> fallback yields relay-only.
			name:        "no-link-state-no-chat-id-falls-back-relay-only",
			body:        `{}`,
			wantState:   TelegramLinkStateRelayOnly,
			wantMessage: relayMsg,
		},
		{
			// link_state absent, chat_id present -> fallback yields linked.
			name:        "no-link-state-with-chat-id-falls-back-linked",
			body:        `{"chat_id":"987654"}`,
			wantState:   TelegramLinkStateLinked,
			wantMessage: linkedMsg,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			status := CheckTelegramRegistration(context.Background(), server.URL, "server-123", logger)

			// Code MUST stay 200 for BOTH meanings so provisioning gates keep firing.
			if status.Code != 200 {
				t.Fatalf("Code = %d, want 200", status.Code)
			}
			if status.Error != nil {
				t.Fatalf("unexpected error: %v", status.Error)
			}
			if status.LinkState != tt.wantState {
				t.Fatalf("LinkState = %v, want %v (body=%s)", status.LinkState, tt.wantState, tt.body)
			}
			// Linked copy must stay byte-identical; relay-only copy is the distinct line.
			if status.Message != tt.wantMessage {
				t.Fatalf("Message = %q, want %q (body=%s)", status.Message, tt.wantMessage, tt.body)
			}
		})
	}
}

// TestResolveTelegramLinkState exercises the parser directly, independent of HTTP
// handling, to lock the fail-safe and case-insensitivity guarantees: an
// unparseable body must never read as a false Linked, and only the explicit
// "linked" token (any casing) resolves to Linked.
func TestResolveTelegramLinkState(t *testing.T) {
	cases := []struct {
		name string
		body string
		want TelegramLinkState
	}{
		{"non-json-body-falls-back-relay-only", "not-json", TelegramLinkStateRelayOnly},
		{"truncated-json-falls-back-relay-only", "{", TelegramLinkStateRelayOnly},
		{"empty-body-falls-back-relay-only", "", TelegramLinkStateRelayOnly},
		{"link-state-uppercase-linked", `{"link_state":"LINKED"}`, TelegramLinkStateLinked},
		{"link-state-mixed-case-linked", `{"link_state":"Linked"}`, TelegramLinkStateLinked},
		{"link-state-unknown-value-relay-only", `{"link_state":"something-else","chat_id":"123"}`, TelegramLinkStateRelayOnly},
		{"link-state-linked-empty-chat-id", `{"link_state":"linked"}`, TelegramLinkStateLinked},
		{"no-link-state-chat-id-linked", `{"chat_id":"123"}`, TelegramLinkStateLinked},
		{"no-link-state-no-chat-id-relay-only", `{}`, TelegramLinkStateRelayOnly},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveTelegramLinkState([]byte(tt.body)); got != tt.want {
				t.Fatalf("resolveTelegramLinkState(%q) = %v, want %v", tt.body, got, tt.want)
			}
		})
	}
}

// TestLinkStateFromFields pins the shared precedence helper that both
// resolveTelegramLinkState and the get-chat-id diagnostic in telegram.go consume,
// covering the two edge cases where the old inline diagnostic diverged: an explicit
// "linked" with empty chat_id (must be Linked), and an unrecognized link_state with
// a chat_id present (must be RelayOnly).
func TestLinkStateFromFields(t *testing.T) {
	cases := []struct {
		name      string
		linkState string
		chatID    string
		want      TelegramLinkState
	}{
		{"linked-token-empty-chat-id", "linked", "", TelegramLinkStateLinked},
		{"linked-token-case-insensitive", "LiNkEd", "", TelegramLinkStateLinked},
		{"unknown-link-state-with-chat-id", "foo", "123", TelegramLinkStateRelayOnly},
		{"relay-only-token-overrides-chat-id", "relay_only", "123", TelegramLinkStateRelayOnly},
		{"no-link-state-chat-id-linked", "", "123", TelegramLinkStateLinked},
		{"no-link-state-no-chat-id-relay-only", "", "", TelegramLinkStateRelayOnly},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := linkStateFromFields(tt.linkState, tt.chatID); got != tt.want {
				t.Fatalf("linkStateFromFields(%q,%q) = %v, want %v", tt.linkState, tt.chatID, got, tt.want)
			}
		})
	}
}
