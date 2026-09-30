package health

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// relayDeliveryBody is a schema_version 1 answer in the shape proxsave_server
// hc_delivery_status.py builds: a ready project whose two checks each have one verified DOWN
// route, with the warning policy applied on the Telegram notify check.
const relayDeliveryBody = `{
 "schema_version": 1, "mode": "centralized", "project_code": "proj1",
 "capabilities": {"delivery_evidence": true, "event_driven_notify_checks": true},
 "notify_policy": {"contract_version": 1, "requested": "warning", "applied": true, "mode": "event_driven",
   "revision": 3, "channels": ["telegram"]},
 "evaluated_at": "2026-09-29T10:00:00Z", "age_seconds": 12, "valid_for_seconds": 120,
 "state": "ready", "reason_codes": [], "alert_worker": "running",
 "checks": {
   "alive": {"present": true, "armed": true, "status": "up", "configured_down_routes": 1, "verified_down_routes": 1},
   "backup": {"present": true, "armed": true, "status": "up", "configured_down_routes": 1, "verified_down_routes": 1}
 },
 "integrations": []
}`

func TestFetchDeliveryStatusParsesTheRelayAnswer(t *testing.T) {
	var gotAuth, gotSID, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Server-Auth")
		gotSID = r.URL.Query().Get("server_id")
		gotPath = r.URL.Path
		gotMethod = r.Method
		_, _ = io.WriteString(w, relayDeliveryBody)
	}))
	defer srv.Close()

	st, err := FetchDeliveryStatus(context.Background(), srv.Client(), srv.URL, "123456789012", "sekret-token", nil, "")
	if err != nil {
		t.Fatalf("FetchDeliveryStatus: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/healthcheck/delivery-status" {
		t.Fatalf("request was %s %s, want GET /api/healthcheck/delivery-status", gotMethod, gotPath)
	}
	if gotAuth != "sekret-token" || gotSID != "123456789012" {
		t.Fatalf("auth=%q server_id=%q; want the per-server auth of the config poll", gotAuth, gotSID)
	}
	if st.State != "ready" || st.ProjectCode != "proj1" || st.AgeSeconds != 12 || st.ValidForSeconds != 120 {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.Checks.Alive.VerifiedDownRoutes != 1 || st.Checks.Backup.ConfiguredDownRoutes != 1 || !st.Checks.Backup.Armed {
		t.Fatalf("unexpected checks: %+v", st.Checks)
	}
	if !st.PolicyConfirmed("warning", []string{"telegram"}) {
		t.Fatalf("the relay applied warning on telegram, PolicyConfirmed must say so: %+v", st.NotifyPolicy)
	}
}

// Anything but a valid schema_version 1 answer is ErrDeliveryUnavailable, which the run reads
// as "not confirmed" and answers by notifying every outcome.
func TestFetchDeliveryStatusRejectsWhatIsNotAUsableAnswer(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"relay without the route", http.StatusNotFound, `{"error":"not found"}`},
		{"feature off or reader down", http.StatusServiceUnavailable, `{"error":"HC_DELIVERY_DISABLED"}`},
		{"auth rejected", http.StatusForbidden, ``},
		{"bad JSON", http.StatusOK, `{"schema_version": 1, "state": `},
		{"newer schema", http.StatusOK, `{"schema_version": 2, "state": "ready", "valid_for_seconds": 120}`},
		{"missing schema", http.StatusOK, `{"state": "ready", "valid_for_seconds": 120}`},
		{"state outside the contract", http.StatusOK, `{"schema_version": 1, "state": "project_missing", "valid_for_seconds": 120}`},
		{"empty state", http.StatusOK, `{"schema_version": 1, "state": "", "valid_for_seconds": 120}`},
		// The relay's own validity decides too: an evaluation it no longer vouches for is not an answer.
		{"expired evaluation", http.StatusOK, `{"schema_version": 1, "state": "ready", "age_seconds": 120, "valid_for_seconds": 120}`},
		{"no validity", http.StatusOK, `{"schema_version": 1, "state": "ready"}`},
		{"negative age", http.StatusOK, `{"schema_version": 1, "state": "ready", "age_seconds": -1, "valid_for_seconds": 120}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			if _, err := FetchDeliveryStatus(context.Background(), srv.Client(), srv.URL, "1", "s", nil, ""); !errors.Is(err, ErrDeliveryUnavailable) {
				t.Fatalf("err = %v; want ErrDeliveryUnavailable", err)
			}
		})
	}

	t.Run("relay unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		if _, err := FetchDeliveryStatus(context.Background(), nil, url, "1", "s", nil, ""); !errors.Is(err, ErrDeliveryUnavailable) {
			t.Fatalf("err = %v; want ErrDeliveryUnavailable", err)
		}
	})
}

// Each of the five states the relay may answer is accepted as it is.
func TestFetchDeliveryStatusAcceptsEveryContractState(t *testing.T) {
	for state := range DeliveryStates {
		t.Run(state, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, `{"schema_version": 1, "state": "`+state+`", "valid_for_seconds": 120}`)
			}))
			defer srv.Close()
			st, err := FetchDeliveryStatus(context.Background(), srv.Client(), srv.URL, "1", "s", nil, "")
			if err != nil || st.State != state {
				t.Fatalf("state %q: got %+v, %v", state, st, err)
			}
		})
	}
}

// The ack confirms only the exact request: the same threshold on the same channel set, applied,
// under contract 1. Channel order is not part of the request.
func TestNotifyPolicyAckConfirmsOnlyTheExactRequest(t *testing.T) {
	applied := func(mut func(a *NotifyPolicyAck)) *NotifyPolicyAck {
		a := &NotifyPolicyAck{ContractVersion: 1, Requested: "warning", Applied: true, Channels: []string{"email", "telegram"}}
		if mut != nil {
			mut(a)
		}
		return a
	}
	cases := []struct {
		name     string
		ack      *NotifyPolicyAck
		channels []string
		want     bool
	}{
		{"exact", applied(nil), []string{"email", "telegram"}, true},
		{"channel order", applied(nil), []string{"telegram", "email"}, true},
		{"no ack (legacy relay)", nil, []string{"email", "telegram"}, false},
		{"not applied", applied(func(a *NotifyPolicyAck) { a.Applied = false }), []string{"email", "telegram"}, false},
		{"other contract", applied(func(a *NotifyPolicyAck) { a.ContractVersion = 2 }), []string{"email", "telegram"}, false},
		{"other threshold", applied(func(a *NotifyPolicyAck) { a.Requested = "failure" }), []string{"email", "telegram"}, false},
		{"channel added", applied(nil), []string{"email", "gotify", "telegram"}, false},
		{"channel removed", applied(nil), []string{"telegram"}, false},
		{"no channels on both sides", applied(func(a *NotifyPolicyAck) { a.Channels = nil }), []string{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ack.Confirms("warning", tc.channels); got != tc.want {
				t.Fatalf("Confirms = %v; want %v", got, tc.want)
			}
			if got := (DeliveryStatus{NotifyPolicy: tc.ack}).PolicyConfirmed("warning", tc.channels); got != tc.want {
				t.Fatalf("PolicyConfirmed = %v; want %v", got, tc.want)
			}
		})
	}
}

// The contract-1 config poll carries the threshold and the channel set, and hands back the ack.
func TestFetchCentralizedConfigWithPolicySendsTheContract(t *testing.T) {
	var gotContract, gotNotifyOn, gotChannels string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotContract, gotNotifyOn, gotChannels = q.Get("hc_contract"), q.Get("notify_on"), q.Get("channels")
		_, _ = io.WriteString(w, `{"mode":"centralized","alive_ping_url":"https://x/ping/a","backup_ping_url":"https://x/ping/b",
			"notify_policy":{"contract_version":1,"requested":"failure","applied":true,"channels":["email","telegram"]}}`)
	}))
	defer srv.Close()

	cfg, err := FetchCentralizedConfigWithPolicy(context.Background(), srv.Client(), srv.URL, "1", "s", []string{"email", "telegram"}, "failure")
	if err != nil {
		t.Fatalf("FetchCentralizedConfigWithPolicy: %v", err)
	}
	if gotContract != "1" || gotNotifyOn != "failure" || gotChannels != "email,telegram" {
		t.Fatalf("query hc_contract=%q notify_on=%q channels=%q", gotContract, gotNotifyOn, gotChannels)
	}
	if !cfg.NotifyPolicy.Confirms("failure", []string{"email", "telegram"}) {
		t.Fatalf("ack not parsed: %+v", cfg.NotifyPolicy)
	}

	// No channel enabled is sent as the explicit "none", never as an empty value a proxy could drop.
	if _, err := FetchCentralizedConfigWithPolicy(context.Background(), srv.Client(), srv.URL, "1", "s", []string{}, "warning"); err != nil {
		t.Fatalf("FetchCentralizedConfigWithPolicy: %v", err)
	}
	if gotChannels != "none" || gotNotifyOn != "warning" {
		t.Fatalf("empty channel set sent as channels=%q notify_on=%q; want none, warning", gotChannels, gotNotifyOn)
	}
}

// Remaining is what is left of the relay's own validity, never more than the caller's limit.
func TestDeliveryStatusRemaining(t *testing.T) {
	cases := []struct {
		age, validFor int
		want          time.Duration
	}{
		{0, 120, 120 * time.Second},
		{100, 120, 20 * time.Second},
		{0, 21600, 120 * time.Second},
		{120, 120, 0},
		{0, 0, 0},
	}
	for _, tc := range cases {
		st := DeliveryStatus{AgeSeconds: tc.age, ValidForSeconds: tc.validFor}
		if got := st.Remaining(120 * time.Second); got != tc.want {
			t.Errorf("age %d valid_for %d: Remaining = %s; want %s", tc.age, tc.validFor, got, tc.want)
		}
	}
}
