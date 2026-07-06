package health

import (
	"testing"
	"time"
)

// TestSensorLevelTruthTable pins the shared per-record helper: nil->neutral, fresh+ok->ok,
// stale->warn, no_url->warn, transmit-fail->warn, and staleAfter<=0 disables staleness.
func TestSensorLevelTruthTable(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := int64(9_990) // 10s ago
	old := int64(1_000)   // ~2.5h ago
	stale := 5 * time.Minute

	cases := []struct {
		name      string
		rec       *PingRecord
		staleAft  time.Duration
		wantLevel SensorLevel
		wantState string
		wantAge   bool
	}{
		{"nil neutral", nil, stale, SensorNeutral, "no data", false},
		{"fresh ok", &PingRecord{TS: fresh, OK: true}, stale, SensorOk, "ok", true},
		{"stale", &PingRecord{TS: old, OK: true}, stale, SensorWarn, "stale", true},
		{"no url", &PingRecord{TS: fresh, OK: false, Reason: ReasonNoURL}, stale, SensorWarn, "not provisioned", true},
		{"transmit fail", &PingRecord{TS: fresh, OK: false, Err: "HTTP 500"}, stale, SensorWarn, "transmit failed", true},
		{"never-stale keeps ok", &PingRecord{TS: old, OK: true}, 0, SensorOk, "ok", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lvl, state, age := sensorLevel(tc.rec, tc.staleAft, now)
			if lvl != tc.wantLevel || state != tc.wantState {
				t.Fatalf("got (%v,%q), want (%v,%q)", lvl, state, tc.wantLevel, tc.wantState)
			}
			if (age != "") != tc.wantAge {
				t.Fatalf("age presence = %q, want non-empty=%v", age, tc.wantAge)
			}
		})
	}
}

// TestSensorRowsMapping: three healthy sensors with an AVAILABLE update -> the updates row
// goes RED ("update available") even though its ping transmitted, proving Available is
// orthogonal to PingRecord.OK.
func TestSensorRowsMapping(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := int64(9_990)
	interval := 5 * time.Minute

	st := Status{
		Heartbeat:   &PingRecord{TS: fresh, OK: true},
		RunFinished: &PingRecord{TS: fresh, OK: true},
		Update:      &UpdateRecord{Ping: PingRecord{TS: fresh, OK: true}, Available: true, Latest: "v2"},
	}
	rows := SensorRows(st, interval, now)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	byName := map[string]SensorRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	if r := byName[SensorAlive]; r.Level != SensorOk {
		t.Fatalf("alive level = %v, want Ok", r.Level)
	}
	if r := byName[SensorBackup]; r.Level != SensorOk {
		t.Fatalf("backup level = %v, want Ok", r.Level)
	}
	if r := byName[SensorUpdates]; r.Level != SensorError || r.State != "update available" {
		t.Fatalf("updates = %+v, want Error/'update available'", r)
	}
}

// TestSensorRowsUpToDateAndEmpty: a fresh, transmitted "up to date" update reads green
// ("up to date"), and an empty status yields all-neutral "no data" rows with no age.
func TestSensorRowsUpToDateAndEmpty(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := int64(9_990)
	interval := 5 * time.Minute

	st := Status{Update: &UpdateRecord{Ping: PingRecord{TS: fresh, OK: true}, Available: false}}
	var up SensorRow
	for _, r := range SensorRows(st, interval, now) {
		if r.Name == SensorUpdates {
			up = r
		}
	}
	if up.Level != SensorOk || up.State != "up to date" {
		t.Fatalf("updates up-to-date = %+v, want Ok/'up to date'", up)
	}

	for _, r := range SensorRows(Status{}, interval, now) {
		if r.Level != SensorNeutral || r.State != "no data" || r.Age != "" {
			t.Fatalf("empty status row %+v, want neutral/no data/no age", r)
		}
	}
}
