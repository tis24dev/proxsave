package health

import (
	"fmt"
	"time"
)

// TxState is the real transmission state derived from the daemon's persisted ping
// outcomes. It is the SINGLE source of truth shared by the run-start init check (which
// gates on whether the daemon is even alive) and the Phase-7 section (which renders the
// full detail) so the two can never drift.
type TxState string

const (
	// TxNoHeartbeat: no heartbeat recorded at all. Because the daemon records its very
	// first beat immediately on startup (even before a URL resolves), this means the
	// monitoring daemon is NOT running.
	TxNoHeartbeat TxState = "daemon-down"
	// TxStale: the last heartbeat is older than the stale window, so the daemon ran
	// before but is now down, crashed, or wedged.
	TxStale TxState = "stale"
	// TxNotProvisioned: a fresh beat that did not transmit because no ping URL is
	// resolved (pairing pending / server unreachable). The daemon IS alive.
	TxNotProvisioned TxState = "not-provisioned"
	// TxUnreachable: a fresh beat that failed with a real transport error - the monitor
	// is unreachable right now. The daemon IS alive.
	TxUnreachable TxState = "unreachable"
	// TxTransmitFailed: heartbeat is healthy but the latest backup-outcome ping failed.
	TxTransmitFailed TxState = "transmit-failed"
	// TxTransmitting: fresh healthy heartbeat and last outcome ok/absent - the only fully
	// healthy state.
	TxTransmitting TxState = "transmitting"
)

// Diagnosis is the outcome of Diagnose. DaemonUp is the load-bearing signal for the
// init-time check: a fresh heartbeat proves the daemon process is alive and beating.
type Diagnosis struct {
	State      TxState
	DaemonUp   bool          // heartbeat present AND fresh -> daemon process is alive
	HbAge      time.Duration // age of the last heartbeat (0 when none)
	HasOutcome bool          // a backup outcome (finished/hang) is recorded
	OutAge     time.Duration // age of the latest backup outcome (valid iff HasOutcome)
	Err        string        // redacted error text for TxUnreachable / TxTransmitFailed
}

// heartbeatStaleFloor keeps 2x-interval at a sane minimum so a beat that merely slipped
// one tick does not read as "daemon down".
const heartbeatStaleFloor = time.Minute

// heartbeatStaleAfter is the age past which a heartbeat is treated as stale: 2x the
// configured interval, with an unset interval falling back to the 5m default and a very
// small one floored.
func heartbeatStaleAfter(interval time.Duration) time.Duration {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if interval < heartbeatStaleFloor {
		interval = heartbeatStaleFloor
	}
	return 2 * interval
}

// newerPing returns whichever record has the larger timestamp (nil-tolerant); used to
// pick the most recent backup outcome between RunFinished and RunHang.
func newerPing(a, b *PingRecord) *PingRecord {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.TS > a.TS:
		return b
	default:
		return a
	}
}

// Diagnose maps a status snapshot to the real transmission state. interval and now are
// injected (this package never reads the clock), so callers get deterministic results.
// The branch order is load-bearing: no heartbeat -> daemon-down; stale -> down/stuck;
// then, only for a FRESH beat (DaemonUp), the transmit outcome. A running-but-failing
// daemon keeps a fresh TS forever, so the OK flag - not staleness - is what catches a
// live monitor outage.
func Diagnose(st Status, heartbeatInterval time.Duration, now time.Time) Diagnosis {
	staleAfter := heartbeatStaleAfter(heartbeatInterval)

	d := Diagnosis{}
	outcome := newerPing(st.RunFinished, st.RunHang)
	if outcome != nil {
		d.HasOutcome = true
		d.OutAge = now.Sub(time.Unix(outcome.TS, 0))
	}

	if st.Heartbeat == nil {
		d.State = TxNoHeartbeat
		return d
	}
	d.HbAge = now.Sub(time.Unix(st.Heartbeat.TS, 0))
	if d.HbAge > staleAfter {
		d.State = TxStale
		return d
	}

	// Fresh heartbeat: the daemon process is alive.
	d.DaemonUp = true
	if !st.Heartbeat.OK {
		if st.Heartbeat.Reason == ReasonNoURL {
			d.State = TxNotProvisioned
			return d
		}
		d.State = TxUnreachable
		d.Err = st.Heartbeat.Err
		return d
	}
	if outcome != nil && !outcome.OK {
		d.State = TxTransmitFailed
		d.Err = outcome.Err
		return d
	}
	d.State = TxTransmitting
	return d
}

// HumanizeAge renders an age as a coarse single-unit "<n><unit> ago" string. It is
// intentionally approximate (the exact value is debug-only); a sub-second or negative
// age (clock skew) reads "just now".
func HumanizeAge(d time.Duration) string {
	switch {
	case d < time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}
