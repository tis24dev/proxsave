package health

import (
	"reflect"
	"strings"
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
		Records: map[string]*PingRecord{
			KindHeartbeat:   {TS: fresh, OK: true},
			KindRunFinished: {TS: fresh, OK: true},
		},
		Update: &UpdateRecord{Ping: PingRecord{TS: fresh, OK: true}, Available: true, Latest: "v2"},
	}
	rows := SensorRows(st, interval, interval, now)
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
	for _, r := range SensorRows(st, interval, interval, now) {
		if r.Name == SensorUpdates {
			up = r
		}
	}
	if up.Level != SensorOk || up.State != "up to date" {
		t.Fatalf("updates up-to-date = %+v, want Ok/'up to date'", up)
	}

	for _, r := range SensorRows(Status{}, interval, interval, now) {
		if r.Level != SensorNeutral || r.State != "no data" || r.Age != "" {
			t.Fatalf("empty status row %+v, want neutral/no data/no age", r)
		}
	}
}

// TestSensorRowsUpdatesAgeAgainstUpdateInterval (L1): the updates sensor ages against the
// update-check cadence, not the heartbeat. A freshly-transmitted updates ping older than 2x
// heartbeat but well within 2x the (longer) update interval must read fresh ("up to date"),
// NOT "stale", while the SAME-age heartbeat IS stale against its own shorter window.
func TestSensorRowsUpdatesAgeAgainstUpdateInterval(t *testing.T) {
	now := time.Unix(100_000, 0)
	heartbeat := 5 * time.Minute // alive stale window = 10m
	updateItv := 6 * time.Hour   // updates stale window = 12h
	pingTS := now.Add(-30 * time.Minute).Unix()

	st := Status{
		Records: map[string]*PingRecord{
			KindHeartbeat: {TS: pingTS, OK: true},
		},
		Update: &UpdateRecord{Ping: PingRecord{TS: pingTS, OK: true}, Available: false},
	}
	byName := map[string]SensorRow{}
	for _, r := range SensorRows(st, heartbeat, updateItv, now) {
		byName[r.Name] = r
	}
	// Updates: fresh against the 12h window -> "up to date", NOT "stale".
	if up := byName[SensorUpdates]; up.Level != SensorOk || up.State != "up to date" {
		t.Fatalf("updates must age on the update interval (fresh) = %+v, want Ok/'up to date'", up)
	}
	// Alive: the same 30m-old ping IS stale against the 10m heartbeat window (sanity: the two
	// sensors deliberately use different stale windows).
	if al := byName[SensorAlive]; al.Level != SensorWarn || al.State != "stale" {
		t.Fatalf("alive must be stale against the heartbeat window = %+v, want Warn/'stale'", al)
	}
}

// TestSensorRowsNotify: each notify-<channel> record in Records produces a
// "proxsave-notify-<channel>" row AFTER the three fixed rows. A fresh transmitted send is
// SensorOk "sent"; a fresh transmitted /1 (Down) is SensorError "send failed"; a
// non-transmitted ping is SensorWarn; an absent channel yields no row; and multiple
// channels are emitted in sorted order for a deterministic screen.
func TestSensorRowsNotify(t *testing.T) {
	now := time.Unix(10_000, 0)
	fresh := int64(9_990)
	interval := 5 * time.Minute

	find := func(rows []SensorRow, name string) (SensorRow, bool) {
		for _, r := range rows {
			if r.Name == name {
				return r, true
			}
		}
		return SensorRow{}, false
	}

	emailKey := CheckKeyNotify("email")         // "notify-email"
	emailRow := SensorProxsavePrefix + emailKey // "proxsave-notify-email"

	// OK + !Down -> a healthy transmitted send is green "sent".
	st := Status{Records: map[string]*PingRecord{emailKey: {TS: fresh, OK: true}}}
	if r, ok := find(SensorRows(st, interval, interval, now), emailRow); !ok || r.Level != SensorOk || r.State != "sent" {
		t.Fatalf("OK+!Down notify = %+v ok=%v, want SensorOk 'sent'", r, ok)
	}

	// OK + Down -> a healthy transmitted /1 (send failed) is red "send failed".
	st = Status{Records: map[string]*PingRecord{emailKey: {TS: fresh, OK: true, Down: true}}}
	if r, ok := find(SensorRows(st, interval, interval, now), emailRow); !ok || r.Level != SensorError || r.State != "send failed" {
		t.Fatalf("OK+Down notify = %+v ok=%v, want SensorError 'send failed'", r, ok)
	}

	// !OK -> the ping never transmitted, so the shared helper yields SensorWarn.
	st = Status{Records: map[string]*PingRecord{emailKey: {TS: fresh, OK: false, Err: "HTTP 500"}}}
	if r, ok := find(SensorRows(st, interval, interval, now), emailRow); !ok || r.Level != SensorWarn {
		t.Fatalf("!OK notify = %+v ok=%v, want SensorWarn", r, ok)
	}

	// Absent channel -> no row (only the three fixed sensors when no notify keys exist).
	rows := SensorRows(Status{}, interval, interval, now)
	if _, ok := find(rows, emailRow); ok {
		t.Fatalf("absent notify channel must not produce a row")
	}
	if len(rows) != 3 {
		t.Fatalf("no notify keys should leave only the 3 fixed rows, got %d", len(rows))
	}

	// Multiple channels -> emitted in sorted order, after the fixed rows.
	telKey := CheckKeyNotify("telegram")
	st = Status{Records: map[string]*PingRecord{
		telKey:   {TS: fresh, OK: true},
		emailKey: {TS: fresh, OK: true},
	}}
	var notifyNames []string
	for _, r := range SensorRows(st, interval, interval, now) {
		if strings.HasPrefix(r.Name, SensorProxsavePrefix+CheckKeyNotifyPrefix) {
			notifyNames = append(notifyNames, r.Name)
		}
	}
	want := []string{SensorProxsavePrefix + emailKey, SensorProxsavePrefix + telKey}
	if !reflect.DeepEqual(notifyNames, want) {
		t.Fatalf("notify rows = %v, want sorted %v", notifyNames, want)
	}
}
