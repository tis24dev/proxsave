package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestScheduleAckConfirmsOnlyTheSentFrequencyApplied(t *testing.T) {
	cases := []struct {
		name string
		ack  *ScheduleAck
		want bool
	}{
		{"nil", nil, false},
		{"applied", &ScheduleAck{Requested: "weekly", Applied: true}, true},
		{"not applied", &ScheduleAck{Requested: "weekly", Applied: false}, false},
		{"other frequency", &ScheduleAck{Requested: "daily", Applied: true}, false},
	}
	for _, tc := range cases {
		if got := tc.ack.Confirms("weekly"); got != tc.want {
			t.Errorf("%s: Confirms(weekly) = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// The poll sends frequency next to the notify contract, decodes the schedule ack, and returns
// the HTTP status even when the answer is an error.
func TestFetchCentralizedConfigPollSendsFrequencyAndReturnsTheStatus(t *testing.T) {
	var gotFreq, gotContract string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotFreq, gotContract = r.URL.Query().Get("frequency"), r.URL.Query().Get("hc_contract")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"alive_ping_url":"a","backup_ping_url":"b","schedule":{"requested":"weekly","applied":true,"timeout":604800,"grace":3600}}`))
	}))
	defer srv.Close()

	cfg, st, err := FetchCentralizedConfigPoll(context.Background(), nil, srv.URL, "1", "s", []string{"email"}, "warning", "weekly", PollLog{})
	if err != nil || st != http.StatusOK || gotFreq != "weekly" || gotContract != "1" {
		t.Fatalf("poll = %v, status %d, frequency=%q hc_contract=%q", err, st, gotFreq, gotContract)
	}
	if cfg.Schedule == nil || !cfg.Schedule.Confirms("weekly") || cfg.Schedule.Timeout == nil || *cfg.Schedule.Timeout != 604800 {
		t.Fatalf("schedule ack = %+v", cfg.Schedule)
	}

	status = http.StatusServiceUnavailable
	if _, st, err := FetchCentralizedConfigPoll(context.Background(), nil, srv.URL, "1", "s", nil, "warning", "weekly", PollLog{}); err == nil || st != http.StatusServiceUnavailable {
		t.Fatalf("503 poll = %v, status %d; want an error and 503", err, st)
	}
}

func TestScheduleStateRoundTripIsRootOnly(t *testing.T) {
	base := t.TempDir()
	if _, found, err := ReadScheduleState(base); found || err != nil {
		t.Fatalf("missing state = found %t, %v; want none, no error", found, err)
	}
	want := ScheduleState{LastConfirmed: "monthly", ConfirmedTS: 7, Configured: &ScheduleCadence{Frequency: "monthly", Weekday: "mon", MonthDay: 15, Time: "02:00"}, ConfiguredTS: 5}
	if err := WriteScheduleState(base, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(ScheduleStatePath(base))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file = %v, %v; want 0600", info, err)
	}
	if dir, err := os.Stat(DaemonStateDir(base)); err != nil || dir.Mode().Perm() != DaemonStateDirPerm {
		t.Fatalf("daemon_state = %v, %v; want %o", dir, err, DaemonStateDirPerm)
	}
	got, found, err := ReadScheduleState(base)
	if err != nil || !found || got.SchemaVersion != ScheduleStateSchemaVersion || got.LastConfirmed != "monthly" || *got.Configured != *want.Configured {
		t.Fatalf("read = %+v, %t, %v", got, found, err)
	}
	if err := os.WriteFile(ScheduleStatePath(base), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadScheduleState(base); err == nil {
		t.Fatal("a corrupt state read without error")
	}
}
