package health

import (
	"testing"
	"time"
)

func TestDiagnose(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	const interval = 5 * time.Minute // -> stale after 10m
	ts := func(age time.Duration) int64 { return now.Add(-age).Unix() }

	cases := []struct {
		name       string
		st         Status
		wantState  TxState
		wantUp     bool
		wantOutcom bool
	}{
		{
			name:      "no heartbeat -> daemon down",
			st:        Status{},
			wantState: TxNoHeartbeat, wantUp: false,
		},
		{
			name:      "stale heartbeat -> down/stuck",
			st:        Status{Heartbeat: &PingRecord{TS: ts(time.Hour), OK: true}},
			wantState: TxStale, wantUp: false,
		},
		{
			name:      "fresh no_url -> not provisioned (daemon up)",
			st:        Status{Heartbeat: &PingRecord{TS: ts(30 * time.Second), OK: false, Reason: ReasonNoURL}},
			wantState: TxNotProvisioned, wantUp: true,
		},
		{
			name:      "fresh real error -> unreachable (daemon up)",
			st:        Status{Heartbeat: &PingRecord{TS: ts(30 * time.Second), OK: false, Err: "healthcheck alive: connection refused"}},
			wantState: TxUnreachable, wantUp: true,
		},
		{
			name: "fresh ok + failed outcome -> transmit-failed",
			st: Status{
				Heartbeat:   &PingRecord{TS: ts(30 * time.Second), OK: true},
				RunFinished: &PingRecord{TS: ts(2 * time.Minute), OK: false, Err: "healthcheck finish: HTTP 500"},
			},
			wantState: TxTransmitFailed, wantUp: true, wantOutcom: true,
		},
		{
			name: "fresh ok + ok outcome -> transmitting",
			st: Status{
				Heartbeat:   &PingRecord{TS: ts(30 * time.Second), OK: true},
				RunFinished: &PingRecord{TS: ts(2 * time.Minute), OK: true},
			},
			wantState: TxTransmitting, wantUp: true, wantOutcom: true,
		},
		{
			name:      "fresh ok + no outcome -> transmitting",
			st:        Status{Heartbeat: &PingRecord{TS: ts(30 * time.Second), OK: true}},
			wantState: TxTransmitting, wantUp: true, wantOutcom: false,
		},
		{
			name: "newer ok outcome supersedes older failure -> transmitting",
			st: Status{
				Heartbeat:   &PingRecord{TS: ts(30 * time.Second), OK: true},
				RunFinished: &PingRecord{TS: ts(20 * time.Minute), OK: false, Err: "old"},
				RunHang:     &PingRecord{TS: ts(2 * time.Minute), OK: true},
			},
			wantState: TxTransmitting, wantUp: true, wantOutcom: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Diagnose(tc.st, interval, now)
			if d.State != tc.wantState {
				t.Fatalf("State=%q want %q", d.State, tc.wantState)
			}
			if d.DaemonUp != tc.wantUp {
				t.Fatalf("DaemonUp=%t want %t", d.DaemonUp, tc.wantUp)
			}
			if d.HasOutcome != tc.wantOutcom {
				t.Fatalf("HasOutcome=%t want %t", d.HasOutcome, tc.wantOutcom)
			}
		})
	}
}

func TestDiagnoseStaleThreshold(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// explicit 1m interval -> stale after 2m
	fresh := Status{Heartbeat: &PingRecord{TS: now.Add(-90 * time.Second).Unix(), OK: true}}
	if d := Diagnose(fresh, time.Minute, now); !d.DaemonUp || d.State != TxTransmitting {
		t.Fatalf("90s < 2x1m should be up+transmitting, got up=%t state=%q", d.DaemonUp, d.State)
	}
	stale := Status{Heartbeat: &PingRecord{TS: now.Add(-150 * time.Second).Unix(), OK: true}}
	if d := Diagnose(stale, time.Minute, now); d.DaemonUp || d.State != TxStale {
		t.Fatalf("150s > 2x1m should be down+stale, got up=%t state=%q", d.DaemonUp, d.State)
	}
}

func TestHumanizeAge(t *testing.T) {
	cases := map[time.Duration]string{
		500 * time.Millisecond: "just now",
		30 * time.Second:       "30s ago",
		5 * time.Minute:        "5m ago",
		3 * time.Hour:          "3h ago",
		50 * time.Hour:         "2d ago",
	}
	for d, want := range cases {
		if got := HumanizeAge(d); got != want {
			t.Errorf("HumanizeAge(%s)=%q want %q", d, got, want)
		}
	}
}

func TestRefineWithPresence(t *testing.T) {
	down := Diagnosis{State: TxNoHeartbeat}
	stale := Diagnosis{State: TxStale, HbAge: time.Hour}
	fresh := Diagnosis{State: TxTransmitting, DaemonUp: true}

	cases := []struct {
		name      string
		in        Diagnosis
		p         DaemonPresence
		wantState TxState
		wantUp    bool
	}{
		// Not probed -> unchanged (graceful fallback).
		{"unprobed keeps input", down, DaemonPresence{Probed: false}, TxNoHeartbeat, false},
		// Not installed dominates any heartbeat.
		{"not installed", fresh, DaemonPresence{Probed: true, Installed: false}, TxNotInstalled, false},
		// Installed but inactive -> truly stopped.
		{"installed not active", fresh, DaemonPresence{Probed: true, Installed: true, Active: false}, TxNotActive, false},
		// Active but no beat -> running but not reporting (the key fix).
		{"active no beat", down, DaemonPresence{Probed: true, Installed: true, Active: true}, TxRunningNoReport, true},
		{"active stale beat", stale, DaemonPresence{Probed: true, Installed: true, Active: true}, TxRunningNoReport, true},
		// Active + fresh beat -> keep the transmit state.
		{"active transmitting", fresh, DaemonPresence{Probed: true, Installed: true, Active: true}, TxTransmitting, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RefineWithPresence(tc.in, tc.p)
			if got.State != tc.wantState || got.DaemonUp != tc.wantUp {
				t.Fatalf("state=%q up=%v, want %q/%v", got.State, got.DaemonUp, tc.wantState, tc.wantUp)
			}
		})
	}
}
