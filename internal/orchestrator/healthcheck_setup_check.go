package orchestrator

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
)

// ErrHealthcheckSelfNotConfigured is the sentinel CheckHealthcheckSelfConnection
// returns when it is called with an empty alive URL (the self params screen has not
// collected the URL yet). It maps to the NOT CONFIGURED classifier verdict.
var ErrHealthcheckSelfNotConfigured = errors.New("self healthcheck alive URL not configured")

// HealthcheckCheckResult is the outcome of one install-time connection check.
// A nil Err with Reachable=true means provisioning is ready AND the monitor is
// reachable from this host. LoginURL carries the portal magic-link whenever the
// server minted one (even if the subsequent reachability ping then failed).
//
// Daemon* carry the REAL operational state read from the on-disk status file (the
// SAME source the run-start init check and the Phase-7 section use): the check thus
// reports whether monitoring is actually running, not just one-shot reachability.
type HealthcheckCheckResult struct {
	Err       error
	LoginURL  string
	Reachable bool

	Daemon     health.Diagnosis // daemon liveness/transmission diagnosed from the status file
	DaemonRead bool             // false only if the status file existed but could not be read

	// RawStatus is the on-disk status snapshot Daemon was diagnosed from, exposed so the
	// install check screen can render the per-sensor list with no second read. HaveStatus
	// is false when the status file was absent/unreadable (RawStatus is then the zero value).
	RawStatus  health.Status
	HaveStatus bool

	// Daemon* alignment fields answer "is the running daemon on the SAME binary as the one now
	// on disk?". DaemonAligned is a real comparison only when DaemonAlignChecked is true (a record
	// was found, its recorded hash was non-empty, AND the on-disk binary re-hashed). When
	// DaemonAlignChecked is false the alignment is UNKNOWN and the "behind" verdict must NOT be
	// reported. DaemonStale carries the human phrasing when not aligned; DaemonVersion is the
	// running daemon's recorded version.
	DaemonAligned      bool
	DaemonAlignChecked bool
	DaemonStale        string
	DaemonVersion      string
}

// Seams for tests.
var (
	healthcheckSetupFetch = health.FetchCentralizedConfig
	healthcheckSetupPing  = func(ctx context.Context, aliveURL string) error {
		return health.NewReporter(health.Config{}).TestPing(ctx, aliveURL)
	}
	healthcheckSetupNow = time.Now
)

// DaemonPresenceProbe reports the daemon's authoritative systemd-level existence
// (installed + active). cmd/proxsave sets it at startup (the systemd unit helpers live
// there); when nil the checks fall back to a heartbeat-only diagnosis. This is what lets
// the checks tell "daemon not installed" / "not running" / "running but not reporting"
// apart instead of flattening everything to a heartbeat-derived "daemon not running".
var DaemonPresenceProbe func(context.Context) health.DaemonPresence

// probeDaemonPresence returns the injected systemd verdict, or an unprobed presence when
// no probe is wired (health.RefineWithPresence then leaves the diagnosis untouched).
func probeDaemonPresence(ctx context.Context) health.DaemonPresence {
	if DaemonPresenceProbe == nil {
		return health.DaemonPresence{}
	}
	return DaemonPresenceProbe(ctx)
}

// DaemonProcStale is the RECORD-INDEPENDENT, HASH-FREE /proc staleness probe (was the running
// daemon's executable replaced on disk? -- signalled by the " (deleted)" suffix on /proc/<pid>/exe).
// cmd/proxsave wires it at startup (the /proc read lives there, keeping this package a leaf); it is
// the SOLE source of the alignment verdict, so when nil (e.g. unit tests) alignment stays UNKNOWN.
// When wired, it lets the install/dashboard check catch a live daemon running a rebuilt/upgraded
// binary as BEHIND instead of a false WORKING.
var DaemonProcStale func(pid int) (stale bool, checked bool)

// CheckHealthcheckConnection runs one install-time check: fetch the centralized
// config (asking for a fresh magic-link) to confirm provisioning is ready, then a
// /log ping to the alive URL to confirm the monitor is reachable from this host. It
// then reads the daemon status file and diagnoses the REAL operational state (is the
// daemon running and actually transmitting?), so the reported status mirrors what the
// run says instead of a one-shot "verified". The /log ping records a ping on the
// MONITOR side WITHOUT touching the local status file (that is daemon-only). It loads
// the relay secret from baseDir itself, reusing the exact identity/secret/base-URL
// plumbing the daemon uses.
func CheckHealthcheckConnection(ctx context.Context, serverAPIHost, serverID, baseDir string, heartbeatInterval time.Duration) HealthcheckCheckResult {
	res := HealthcheckCheckResult{}

	// Real operational state via the SHARED daemon-state checker: the status file answers "is it
	// transmitting?", systemd answers "does the process exist and run?", and the /proc probe answers
	// "is the running binary aligned with the one on disk?". A readable file OR a probed systemd state
	// yields a verdict; only when BOTH are unavailable is the daemon genuinely unknown. ProcAlive is
	// nil (the leaf signal-0 liveness): alignment here does not need the /proc/cmdline gate. ProcStale
	// is the wired /proc probe -- the sole alignment signal (nil when cmd/proxsave has not wired it,
	// e.g. unit tests, leaving alignment UNKNOWN).
	ds := health.CheckDaemonState(health.DaemonStateInput{
		BaseDir:           baseDir,
		HeartbeatInterval: heartbeatInterval,
		Now:               healthcheckSetupNow(),
		Presence:          probeDaemonPresence(ctx),
		ProcAlive:         nil,
		ProcStale:         DaemonProcStale,
	})
	res.Daemon = ds.Diagnosis
	res.DaemonRead = ds.Probed || ds.HaveStatus
	res.RawStatus = ds.RawStatus
	res.HaveStatus = ds.HaveStatus
	res.DaemonAligned = ds.Aligned
	res.DaemonAlignChecked = ds.AlignChecked
	res.DaemonStale = ds.StaleReason
	res.DaemonVersion = ds.Version

	secret := healthcheckSetupLoadSecret(baseDir)
	cfg, err := healthcheckSetupFetch(ctx, nil, serverAPIHost, serverID, secret, true)
	if err != nil {
		res.Err = err
		return res
	}
	res.LoginURL = cfg.LoginURL
	if cfg.AliveURL != "" {
		if perr := healthcheckSetupPing(ctx, cfg.AliveURL); perr != nil {
			res.Err = perr
			return res
		}
		res.Reachable = true
	}
	return res
}

// CheckHealthcheckSelfConnection runs the self-mode install check: a single
// state-neutral reachability ping to the user's own alive URL. It performs NO
// server config fetch, NO magic-link/login, and NO daemon-state read (self mode
// has no centralized identity and the daemon has not started yet at install time),
// so the result carries only Reachable/Err. An empty alive URL yields
// ErrHealthcheckSelfNotConfigured without pinging.
func CheckHealthcheckSelfConnection(ctx context.Context, aliveURL string) HealthcheckCheckResult {
	res := HealthcheckCheckResult{}
	aliveURL = strings.TrimSpace(aliveURL)
	if aliveURL == "" {
		res.Err = ErrHealthcheckSelfNotConfigured
		return res
	}
	if err := healthcheckSetupPing(ctx, aliveURL); err != nil {
		res.Err = err
		return res
	}
	res.Reachable = true
	return res
}
