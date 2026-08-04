package main

import "github.com/tis24dev/proxsave/internal/orchestrator"

// restartVerifyOutcome is the SINGLE classification of a restart+verify result
// (restartAndVerifyDaemon). Three surfaces render it -- the CLI upgrade footer
// (summarizeRestartVerify), the upgrade bootstrap log (logUpgradeDaemonRestart) and the
// dashboard result screen (restartVerifyStatus) -- and the first two classify the SAME
// value on the SAME --upgrade run (upgrade.go). They used to re-derive the verdict from
// three hand-copied switches, so a change to the order or to a guard in one could leave
// the other two silently behind.
//
// It carries NO text and NO data. Each surface keeps its own wording, its own output
// channel (the log's stdout-vs-stderr split is Println vs Warning, not a colour) and its
// own version SOURCE: the footer names the version the upgrade just INSTALLED, while the
// dashboard names the version the running daemon REPORTS. Those are different facts and
// must not be merged. Only the VERDICT and its severity are shared.
//
// It is defined over a RESTART result. The poll-only verifyDaemonAligned result is
// classified separately by installVerifyVerdict -- see the note there for why the two
// cannot share this.
type restartVerifyOutcome int

const (
	// restartVerifyError: the restart call itself failed, so nothing was restarted.
	// rv.Err is non-nil on this arm and ONLY on this arm -- two surfaces dereference it
	// with no nil check, so widening this guard panics them.
	restartVerifyError restartVerifyOutcome = iota
	// restartVerifyDeferredConfig: the config was unreadable, so the REAL backup lock path
	// is unknown; the restart was deferred fail-closed rather than risk killing a backup on
	// a custom LOCK_PATH (F11-08).
	restartVerifyDeferredConfig
	// restartVerifyDeferredBackup: a backup still held the lock when the bounded wait
	// elapsed; the restart was deferred rather than killing it.
	restartVerifyDeferredBackup
	// restartVerifyTimedOut: restarted, but the alignment poll exhausted its budget.
	restartVerifyTimedOut
	// restartVerifyAligned: the only success -- restarted, live, aligned AND fresh.
	restartVerifyAligned
	// restartVerifyUnconfirmed: restarted and the poll returned, but the success gate was
	// not met. No return of restartAndVerifyDaemon reaches it today (its five returns each
	// land on one of the arms above); it is the fail-safe for a future producer return, and
	// the arm every surface would fall into if a guard above it were weakened.
	restartVerifyUnconfirmed
	// restartVerifyOutcomeCount is the arity sentinel, NOT an outcome. It lets a test assert
	// its table covers every constant, so a seventh outcome cannot be added and then
	// silently rendered through three default arms.
	restartVerifyOutcomeCount
)

// classifyRestartVerify is the ONE classification of a restart+verify result. The branch
// ORDER is the contract, not an accident: the flags are not mutually exclusive, and
// testing TimedOut BEFORE the success conjunction is what stops a restart that ran out of
// budget from being reported as "now aligned".
//
// It takes a VALUE and has no nil arm. A nil *RestartVerifyResult means "no restart was
// attempted", which is not an outcome OF a restart, and the two pointer surfaces already
// answer it differently: the footer returns an empty line (upgradeFooterBody keys off that
// to print nothing at all), the log returns silently.
func classifyRestartVerify(rv RestartVerifyResult) restartVerifyOutcome {
	switch {
	case rv.Err != nil:
		return restartVerifyError
	case rv.LockPathUnknown:
		return restartVerifyDeferredConfig
	case rv.BackupWaitTimedOut:
		return restartVerifyDeferredBackup
	case rv.TimedOut:
		return restartVerifyTimedOut
	case rv.Restarted && rv.ProcessAlive && rv.Aligned && rv.FreshInfo:
		return restartVerifyAligned
	default:
		return restartVerifyUnconfirmed
	}
}

// warn reports whether the outcome is a non-success, which is what the CLI upgrade footer
// styles on. Every non-success is a WARNING -- including a failed restart: an upgrade that
// installed the new binary but could not restart the daemon is not a failed upgrade, and
// neither front-end may style it as a hard error or let it change the exit code.
func (o restartVerifyOutcome) warn() bool { return o != restartVerifyAligned }

// level is the same partition as warn, in the vocabulary the dashboard renders. Green only
// for the one success; every gap is yellow. It is single-sourced with warn so the two
// front-ends cannot disagree about the severity of one outcome, which they used to: the
// footer painted a failed restart yellow while the dashboard painted it red. Warn is the
// side that matches daemonStatusStyle, the repo's other daemon verdict, which has no red
// state at all.
func (o restartVerifyOutcome) level() orchestrator.HealthcheckSetupLevel {
	if o == restartVerifyAligned {
		return orchestrator.HealthcheckSetupLevelOk
	}
	return orchestrator.HealthcheckSetupLevelWarn
}
