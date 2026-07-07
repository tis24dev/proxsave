package orchestrator

import (
	"errors"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/serverbot"
)

// Shared hints so the CLI and TUI render identical retry copy.
const (
	HealthcheckSetupRetryHint       = "You can run the check again."
	HealthcheckSetupMaxAttemptsHint = "Maximum checks reached. You can finish the install and verify monitoring later."
)

// HealthcheckSetupLevel is the severity a front-end renders (green/yellow/red). It keeps
// the classifier UI-agnostic: the CLI and TUI map it to their own styles.
type HealthcheckSetupLevel int

const (
	HealthcheckSetupLevelWarn    HealthcheckSetupLevel = iota // attention (yellow) - the default
	HealthcheckSetupLevelOk                                   // working (green)
	HealthcheckSetupLevelError                                // hard blocker (red)
	HealthcheckSetupLevelNeutral                              // pre-check (yellow, NO symbol) - set by the front-end, never by the classifier
)

// HealthcheckSetupState is the single mapping of a check result to user-facing copy +
// policy flags; both front-ends render it identically. Keyword is the short first-line
// state word (WORKING / NOT RUNNING / ...); Message is the second-line explanation; Level
// colors the keyword. The Message is always our OWN copy; LoginURL (server-minted) is
// passed through serverbot.SanitizeLoginURL so a hostile/MITM'd server cannot inject
// terminal escape sequences into the install console.
type HealthcheckSetupState struct {
	Keyword  string
	Message  string
	Level    HealthcheckSetupLevel
	LoginURL string
	Verified bool // provisioning ready AND monitor reachable -> Continue/return allowed
	Fatal    bool // another check cannot help -> do NOT offer Check again
}

// ClassifyHealthcheckSetupResult maps a HealthcheckCheckResult to display state. The
// headline is the REAL operational state (mirroring the run): only a live daemon that is
// actually transmitting reads WORKING; a reachable monitor with a down/stale daemon reads
// NOT RUNNING / STALE, because monitoring is not actually operating. Hard provisioning
// blockers (bad credentials, unregistered host, server-disabled) override the daemon state
// since monitoring cannot operate until they are resolved. Verified (the install Continue
// latch) stays connectivity-based: the daemon legitimately has not started yet at install.
func ClassifyHealthcheckSetupResult(res HealthcheckCheckResult) HealthcheckSetupState {
	st := HealthcheckSetupState{LoginURL: serverbot.SanitizeLoginURL(res.LoginURL)}

	// 1) Hard provisioning blockers: monitoring cannot operate until fixed -> override.
	switch {
	case errors.Is(res.Err, health.ErrHCAuth):
		st.Fatal, st.Level, st.Keyword = true, HealthcheckSetupLevelError, "REJECTED"
		st.Message = "The monitoring server rejected this host's credentials. Complete Telegram pairing, then reinstall to retry."
		return st
	case errors.Is(res.Err, health.ErrHCUnknown):
		st.Fatal, st.Level, st.Keyword = true, HealthcheckSetupLevelError, "NOT REGISTERED"
		st.Message = "This host is not registered on the server yet. Complete Telegram pairing first."
		return st
	case errors.Is(res.Err, health.ErrHCDisabled):
		st.Fatal, st.Level, st.Keyword = true, HealthcheckSetupLevelError, "DISABLED"
		st.Message = "Centralized monitoring is currently disabled on the server; nothing to configure here."
		return st
	}

	// 2) Other connectivity problems: cannot confirm the monitor is reachable from here
	//    right now, so we cannot vouch for the daemon's target either. Retryable.
	switch {
	case errors.Is(res.Err, health.ErrHCNotReady):
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "PROVISIONING"
		st.Message = "Monitoring is still being provisioned on the server. Try the check again in a moment."
		return st
	case res.Err != nil:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "UNREACHABLE"
		st.Message = "Could not reach the monitoring server. Check this host's connectivity and try again."
		return st
	case !res.Reachable:
		// Provisioning fetched but the reachability ping was skipped (empty alive URL);
		// defensive, since the server rejects empty ping-urls in practice.
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "UNCONFIRMED"
		st.Message = "Monitoring is provisioned, but reachability could not be confirmed. Try the check again."
		return st
	}

	// 3) Provisioned + reachable: install may Continue, and the headline becomes the REAL
	//    daemon state read from the status file (the same source the run reports).
	st.Verified = true
	if !res.DaemonRead {
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "STATUS UNREADABLE"
		st.Message = "The monitoring status file could not be read, so the daemon state is unknown."
		return st
	}
	// A running daemon on an OLDER binary than the one now on disk (an in-place upgrade replaced
	// the file without a restart) needs a restart to load the update. This is DISTINCT from "not
	// reporting yet" and takes precedence over the transmission-state headline. Only reported when
	// alignment was actually determined (DaemonAlignChecked) by the record-independent /proc probe;
	// when it could not be determined, alignment is UNKNOWN and must NOT read as behind. A record
	// (DaemonHaveInfo) is not required, so any live daemon on a replaced binary is caught.
	if res.DaemonAlignChecked && !res.DaemonAligned {
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "BEHIND"
		st.Message = "The monitoring daemon is running an older binary than the one now on disk; restart it to load the update."
		return st
	}
	return applyHealthcheckDaemonState(st, res.Daemon)
}

// ClassifyHealthcheckSelfResult maps a self-mode check result to the SAME
// HealthcheckSetupState the centralized classifier produces, so the CLI and TUI
// renderers stay untouched. Self mode has no provisioning/daemon dimension: the
// verdict is pure reachability of the user's own alive URL. A successful ping reads
// REACHABLE (green, Verified so the install may Continue); a ping error reads
// UNREACHABLE (yellow, retryable, NOT fatal); an empty/not-configured URL reads NOT
// CONFIGURED (yellow). Self mode never mints a magic-link, so LoginURL stays empty.
func ClassifyHealthcheckSelfResult(res HealthcheckCheckResult) HealthcheckSetupState {
	st := HealthcheckSetupState{}
	switch {
	case errors.Is(res.Err, ErrHealthcheckSelfNotConfigured):
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "NOT CONFIGURED"
		st.Message = "No service-alive ping URL is configured for self mode yet. Enter the healthchecks parameters, then run the check."
		return st
	case res.Err != nil:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "UNREACHABLE"
		st.Message = "Could not reach the healthchecks ping URL. Check the URL and this host's connectivity, then try again."
		return st
	case !res.Reachable:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "UNREACHABLE"
		st.Message = "Could not confirm the healthchecks ping URL is reachable. Try the check again."
		return st
	}
	st.Verified = true
	st.Level, st.Keyword = HealthcheckSetupLevelOk, "REACHABLE"
	st.Message = "The healthchecks ping URL responded; your self-hosted monitor is reachable from this host."
	return st
}

// applyHealthcheckDaemonState maps the daemon diagnosis to the headline keyword/level and
// an explanation. WORKING (green) is the ONLY healthy state; every other state is a live
// gap between "reachable" and "actually monitoring".
func applyHealthcheckDaemonState(st HealthcheckSetupState, d health.Diagnosis) HealthcheckSetupState {
	switch d.State {
	case health.TxTransmitting:
		st.Level, st.Keyword = HealthcheckSetupLevelOk, "WORKING"
		st.Message = "The monitoring daemon is running and reporting this host's backups and heartbeats to the monitor."
	case health.TxNotInstalled:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "NOT INSTALLED"
		st.Message = "The monitor is reachable, but the monitoring daemon is not installed on this host, so nothing is reported."
	case health.TxNotActive:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "NOT RUNNING"
		st.Message = "The monitor is reachable, but the monitoring daemon is installed and stopped, so nothing is reported on schedule."
	case health.TxRunningNoReport:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "RUNNING, NOT REPORTING"
		st.Message = "The monitoring daemon is running but has not written a heartbeat yet; it may be a stale build that needs a restart."
	case health.TxNoHeartbeat:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "NOT RUNNING"
		st.Message = "The monitor is reachable, but the monitoring daemon is not running, so nothing is reported on schedule."
	case health.TxStale:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "STALE"
		st.Message = "The monitoring daemon's last heartbeat is old (" + health.HumanizeAge(d.HbAge) + "); it may be stopped or wedged."
	case health.TxNotProvisioned:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "NOT PROVISIONED"
		st.Message = "The monitoring daemon is running but has no ping target yet."
	case health.TxUnreachable:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "MONITOR UNREACHABLE"
		st.Message = "The monitoring daemon is running but could not reach the monitor: " + orNA(d.Err) + "."
	case health.TxTransmitFailed:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "TRANSMIT FAILED"
		st.Message = "The monitoring daemon is running but the last backup outcome was not transmitted: " + orNA(d.Err) + "."
	default:
		st.Level, st.Keyword = HealthcheckSetupLevelWarn, "UNKNOWN"
		st.Message = "The monitoring daemon state could not be determined."
	}
	return st
}
