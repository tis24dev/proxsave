package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// restartVerdictFixtures is one representative result per outcome, in constant order. Each
// surface test walks it, so adding an outcome without a fixture fails the arity check rather
// than silently leaving a surface untested.
var restartVerdictFixtures = []struct {
	outcome restartVerifyOutcome
	rv      RestartVerifyResult
}{
	{restartVerifyError, RestartVerifyResult{Err: errors.New("unit not found")}},
	{restartVerifyDeferredConfig, RestartVerifyResult{LockPathUnknown: true}},
	{restartVerifyDeferredBackup, RestartVerifyResult{BackupWaitTimedOut: true}},
	{restartVerifyTimedOut, RestartVerifyResult{Restarted: true, TimedOut: true}},
	{restartVerifyAligned, RestartVerifyResult{Restarted: true, ProcessAlive: true, Aligned: true, FreshInfo: true}},
	{restartVerifyUnconfirmed, RestartVerifyResult{Restarted: true}},
}

// TestLogUpgradeDaemonRestartWordsEveryOutcome is the FIRST test of the upgrade bootstrap log
// surface, which had none. It is half of the pair that classifies the same restart result on the
// same --upgrade run: the footer is read on the terminal, this one is what an unattended upgrade
// leaves behind on disk, so a divergence between them is only ever discovered afterwards.
//
// It also pins the severity split. The success is the ONLY line that goes out at INFO (Println,
// stdout); every gap is a WARNING (stderr). Routing them all through one emitter would be
// invisible on the terminal and would either lose the success line for anyone redirecting stderr
// away, or turn a normal upgrade into a false positive for anyone grepping stderr.
func TestLogUpgradeDaemonRestartWordsEveryOutcome(t *testing.T) {
	want := map[restartVerifyOutcome]struct {
		message string
		warning bool
	}{
		restartVerifyError:          {"Daemon restart failed: unit not found (it may still run the old binary; restart it manually).", true},
		restartVerifyDeferredConfig: {"Config unreadable; daemon restart deferred. Restart when the config is readable or the daemon stays on the old binary.", true},
		restartVerifyDeferredBackup: {"A backup is running; daemon restart deferred. Restart when idle or the daemon stays on the old binary.", true},
		restartVerifyTimedOut:       {"Daemon restarted but alignment check timeout", true},
		restartVerifyAligned:        {"Daemon restarted and now aligned with the new binary.", false},
		restartVerifyUnconfirmed:    {"Daemon restarted but alignment could not be confirmed", true},
	}
	if len(want) != int(restartVerifyOutcomeCount) {
		t.Fatalf("table covers %d outcomes, want all %d", len(want), restartVerifyOutcomeCount)
	}

	for _, fixture := range restartVerdictFixtures {
		expected, ok := want[fixture.outcome]
		if !ok {
			t.Fatalf("no expectation for outcome %d", fixture.outcome)
		}
		bootstrap, buf := captureBootstrapLog(t)
		bootstrap.SetConsoleQuiet(true)
		rv := fixture.rv
		logUpgradeDaemonRestart(bootstrap, &rv)
		got := buf.String()
		if !strings.Contains(got, expected.message) {
			t.Fatalf("outcome %d logged %q, want it to contain %q", fixture.outcome, got, expected.message)
		}
		if isWarning := strings.Contains(got, "WARNING"); isWarning != expected.warning {
			t.Fatalf("outcome %d logged at warning=%v, want %v: %q", fixture.outcome, isWarning, expected.warning, got)
		}
	}

	// A nil result means no restart was attempted (an inactive daemon, or a cron host). It is
	// not an outcome OF a restart, so the log must stay silent rather than invent one.
	bootstrap, buf := captureBootstrapLog(t)
	bootstrap.SetConsoleQuiet(true)
	logUpgradeDaemonRestart(bootstrap, nil)
	if buf.String() != "" {
		t.Fatalf("a nil result must log nothing, got %q", buf.String())
	}
}

// TestSummarizeRestartVerifyDistinguishesTheDeferrals pins the two deferral lines apart on the
// CLI footer. They are different remedies -- make the config readable, versus wait for the backup
// to finish -- but both lines merely contain "deferred", so the pre-existing substring assertion
// could not tell them apart and the config-unreadable arm had no coverage at all on any surface.
func TestSummarizeRestartVerifyDistinguishesTheDeferrals(t *testing.T) {
	unknownLock := RestartVerifyResult{LockPathUnknown: true}
	line, warn := summarizeRestartVerify(&unknownLock, "1.2.3")
	if !warn {
		t.Fatalf("a deferred restart is a warning, got warn=false")
	}
	if !strings.Contains(line, "config unreadable") || !strings.Contains(line, "restart when the config is readable") {
		t.Fatalf("config-unreadable line wrong: %q", line)
	}
	if strings.Contains(line, "a backup is running") {
		t.Fatalf("config-unreadable line must not name the backup deferral: %q", line)
	}

	backup := RestartVerifyResult{BackupWaitTimedOut: true}
	backupLine, _ := summarizeRestartVerify(&backup, "1.2.3")
	if !strings.Contains(backupLine, "a backup is running") || strings.Contains(backupLine, "config unreadable") {
		t.Fatalf("backup deferral line wrong: %q", backupLine)
	}
	if line == backupLine {
		t.Fatalf("the two deferrals must not share a line: %q", line)
	}
}

// TestInstallVerifyVerdictNamesWhatItCouldNotEstablish covers the poll-only verdict, including
// the arm that used to lie. A daemon that IS process-alive but whose /proc alignment probe never
// returned a verdict was reported as "not running": wrong about the process, and silent about the
// fact that alignment -- not existence -- is what could not be established. An operator reading
// that after an install would go looking for a dead daemon that is running fine.
func TestInstallVerifyVerdictNamesWhatItCouldNotEstablish(t *testing.T) {
	aliveUnverifiable := RestartVerifyResult{ProcessAlive: true}
	level, keyword := installVerifyVerdict(aliveUnverifiable)
	if level != orchestrator.HealthcheckSetupLevelWarn {
		t.Fatalf("an unverifiable alignment is a warning, got level=%v", level)
	}
	if strings.Contains(keyword, "not running") {
		t.Fatalf("a live daemon must not be reported as not running: %q", keyword)
	}
	if !strings.Contains(keyword, "running") || !strings.Contains(keyword, "could not be verified") {
		t.Fatalf("the keyword must say it is running and that verification failed: %q", keyword)
	}

	// The other three arms, so the split above cannot be widened over them.
	aligned := RestartVerifyResult{ProcessAlive: true, Aligned: true, State: health.DaemonState{Version: "1.2.3", AlignChecked: true}}
	if level, keyword := installVerifyVerdict(aligned); level != orchestrator.HealthcheckSetupLevelOk ||
		keyword != "running and aligned (v1.2.3)" {
		t.Fatalf("aligned verdict wrong: level=%v keyword=%q", level, keyword)
	}
	behind := RestartVerifyResult{ProcessAlive: true, State: health.DaemonState{AlignChecked: true}}
	if level, keyword := installVerifyVerdict(behind); level != orchestrator.HealthcheckSetupLevelWarn ||
		keyword != "running but not aligned (behind)" {
		t.Fatalf("behind verdict wrong: level=%v keyword=%q", level, keyword)
	}
	// Genuinely down after the full poll budget: "not running" is a measured fact here, and the
	// only arm entitled to say it.
	down := RestartVerifyResult{TimedOut: true}
	if level, keyword := installVerifyVerdict(down); level != orchestrator.HealthcheckSetupLevelWarn ||
		keyword != "not running" {
		t.Fatalf("not-running verdict wrong: level=%v keyword=%q", level, keyword)
	}
}

// TestInstallVerifyVerdictIsNotTheRestartClassifier pins the separation the comment used to only
// assert. A poll-only result never carries Restarted, so routing it through classifyRestartVerify
// would put every install on a "restarted but..." arm -- the regression that once made the install
// always say "not confirmed" -- and would lose the BEHIND verdict, which that classifier has no
// arm for at all.
func TestInstallVerifyVerdictIsNotTheRestartClassifier(t *testing.T) {
	// The shape verifyDaemonAligned returns for a healthy, aligned daemon: no Restarted flag.
	pollOnlySuccess := RestartVerifyResult{
		ProcessAlive: true,
		Aligned:      true,
		FreshInfo:    true, // set from State.HaveInfo here, NOT from a new start timestamp
		State:        health.DaemonState{Version: "1.2.3", AlignChecked: true},
	}
	if got := classifyRestartVerify(pollOnlySuccess); got == restartVerifyAligned {
		t.Fatalf("the restart classifier must not call a poll-only result aligned, got %d", got)
	}
	if level, keyword := installVerifyVerdict(pollOnlySuccess); level != orchestrator.HealthcheckSetupLevelOk ||
		!strings.Contains(keyword, "running and aligned") {
		t.Fatalf("the poll-only verdict must call it aligned: level=%v keyword=%q", level, keyword)
	}

	// The BEHIND verdict has no counterpart in the restart classifier's six arms.
	behind := RestartVerifyResult{ProcessAlive: true, State: health.DaemonState{AlignChecked: true}}
	_, keyword := installVerifyVerdict(behind)
	for _, rv := range restartVerdictFixtures {
		if line, _ := summarizeRestartVerify(&rv.rv, ""); strings.Contains(line, keyword) {
			t.Fatalf("restart outcome %d already words the behind verdict: %q", rv.outcome, line)
		}
	}
}
