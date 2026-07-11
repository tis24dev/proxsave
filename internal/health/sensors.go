package health

import (
	"sort"
	"strings"
	"time"
)

// Sensor display names as they appear on the healthchecks panel and in the install
// check screen. They are the three Fase-1 sensors' identities; the CentralizedConfig
// Checks map key that carries the updates ping URL is CheckKeyUpdates ("updates"),
// deliberately distinct from the SensorUpdates display name.
const (
	SensorAlive   = "proxsave-alive"
	SensorBackup  = "proxsave-backup"
	SensorUpdates = "proxsave-updates"
)

// SensorLevel is the display severity of one monitored sensor in the install check
// screen. It is a health-local enum so this package stays a leaf (it must not import
// the UI/orchestrator); the front-end maps it to its own color/severity.
type SensorLevel int

const (
	// SensorNeutral: nothing recorded yet (nil record) - rendered yellow, no symbol.
	SensorNeutral SensorLevel = iota
	// SensorOk: a fresh, transmitted ping (updates: also up to date) - green.
	SensorOk
	// SensorWarn: stale, no ping URL resolved, or a transmit failure - yellow.
	SensorWarn
	// SensorError: an update is available (the updates sensor's /1 DOWN state) - red.
	SensorError
)

// SensorRow is one sensor's rendered state for the check screen: its display name, a
// level, a short state word, and a humanized "last ping" age ("" when nothing recorded).
type SensorRow struct {
	Name  string
	Level SensorLevel
	State string
	Age   string
}

// sensorLevel maps a single PingRecord to a display level, a short state word, and a
// humanized age. It is the shared truth table for the sensor list:
//
//	nil                    -> SensorNeutral, "no data", ""   (nothing recorded yet)
//	stale (age>staleAfter) -> SensorWarn,    "stale"
//	!OK && ReasonNoURL     -> SensorWarn,    "not provisioned"
//	!OK otherwise          -> SensorWarn,    "transmit failed"
//	OK && fresh            -> SensorOk,      "ok"
//
// staleAfter<=0 disables the staleness check (used for the event-driven backup sensor,
// which has no fixed cadence). now is injected so callers stay deterministic.
func sensorLevel(rec *PingRecord, staleAfter time.Duration, now time.Time) (SensorLevel, string, string) {
	if rec == nil {
		return SensorNeutral, "no data", ""
	}
	d := now.Sub(time.Unix(rec.TS, 0))
	age := HumanizeAge(d)
	if staleAfter > 0 && d > staleAfter {
		return SensorWarn, "stale", age
	}
	if !rec.OK {
		if rec.Reason == ReasonNoURL {
			return SensorWarn, "not provisioned", age
		}
		return SensorWarn, "transmit failed", age
	}
	return SensorOk, "ok", age
}

// SensorRows builds the canonical Fase-1 sensor table from a Status snapshot, mapping each
// sensor to its source record:
//
//	proxsave-alive   <- Heartbeat                    (stale window = 2x heartbeat interval)
//	proxsave-backup  <- newer(RunFinished, RunHang)  (event-driven: never "stale")
//	proxsave-updates <- Update.Ping, refined by Update.Available (stale window = 2x update interval)
//
// The updates sensor is special: PingRecord.OK is only the TRANSMISSION outcome, so a
// fresh, transmitted ping that reported "update available" (Available) is a healthy
// transmission of a RED check -> SensorError "update available". A fresh, transmitted "up
// to date" is SensorOk "up to date". The updates ping is refreshed on its own
// update-check cadence, so it ages against updateInterval, not the heartbeat; otherwise a
// longer HEALTHCHECK_UPDATE_INTERVAL would render a healthy fresh ping "stale".
// heartbeatInterval, updateInterval and now are injected (deterministic).
func SensorRows(st Status, heartbeatInterval, updateInterval time.Duration, now time.Time) []SensorRow {
	staleAfter := heartbeatStaleAfter(heartbeatInterval)

	aliveLvl, aliveState, aliveAge := sensorLevel(st.Record(KindHeartbeat), staleAfter, now)
	backupLvl, backupState, backupAge := sensorLevel(newerPing(st.Record(KindRunFinished), st.Record(KindRunHang)), 0, now)

	rows := []SensorRow{
		{Name: SensorAlive, Level: aliveLvl, State: aliveState, Age: aliveAge},
		{Name: SensorBackup, Level: backupLvl, State: backupState, Age: backupAge},
	}

	var upRec *PingRecord
	if st.Update != nil {
		upRec = &st.Update.Ping
	}
	upLvl, upState, upAge := sensorLevel(upRec, heartbeatStaleAfter(updateInterval), now)
	if upLvl == SensorOk {
		if st.Update != nil && st.Update.Available {
			upLvl, upState = SensorError, "update available"
		} else {
			upState = "up to date"
		}
	}
	rows = append(rows, SensorRow{Name: SensorUpdates, Level: upLvl, State: upState, Age: upAge})

	// Per-notification-channel sensors: one row per Records key with the notify- prefix,
	// sorted for a deterministic screen. Event-driven (they ride the daily run), so never
	// "stale". PingRecord.OK is only the TRANSMISSION outcome; the Down flag is the /1
	// "channel send failed" signal, so a fresh transmitted /1 is SensorError "send failed".
	var notifyKeys []string
	for k := range st.Records {
		if strings.HasPrefix(k, CheckKeyNotifyPrefix) {
			notifyKeys = append(notifyKeys, k)
		}
	}
	sort.Strings(notifyKeys)
	for _, k := range notifyKeys {
		rec := st.Records[k]
		lvl, state, age := sensorLevel(rec, 0, now)
		if lvl == SensorOk {
			if rec != nil && rec.Down {
				lvl, state = SensorError, "send failed"
			} else {
				state = "sent"
			}
		}
		rows = append(rows, SensorRow{Name: SensorProxsavePrefix + k, Level: lvl, State: state, Age: age})
	}

	return rows
}
