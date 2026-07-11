// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// shrinkRestartBudgets replaces the restart/backup-wait budgets with tiny values so the
// poll/wait loops resolve in milliseconds, then restores them.
func shrinkRestartBudgets(t *testing.T) {
	t.Helper()
	obwt, obwp := backupWaitTimeout, backupWaitPollInterval
	orvt, orvk := restartVerifyTimeout, restartVerifyTick
	t.Cleanup(func() {
		backupWaitTimeout, backupWaitPollInterval = obwt, obwp
		restartVerifyTimeout, restartVerifyTick = orvt, orvk
	})
	backupWaitTimeout = 60 * time.Millisecond
	backupWaitPollInterval = 2 * time.Millisecond
	restartVerifyTimeout = 120 * time.Millisecond
	restartVerifyTick = 2 * time.Millisecond
}

// stubRestartSeams overrides the restart/probe/state/presence seams for a test and
// restores them on cleanup. restartFn/backupFn/stateFn may be nil to keep the default.
func stubRestartSeams(t *testing.T, restartFn func(context.Context) error, backupFn func(string) bool, stateFn func(health.DaemonStateInput) health.DaemonState) {
	t.Helper()
	oRestart, oBackup, oState, oPresence := daemonRestartService, restartVerifyBackupRunning, restartVerifyDaemonState, daemonPresenceProbe
	t.Cleanup(func() {
		daemonRestartService = oRestart
		restartVerifyBackupRunning = oBackup
		restartVerifyDaemonState = oState
		daemonPresenceProbe = oPresence
	})
	if restartFn != nil {
		daemonRestartService = restartFn
	}
	if backupFn != nil {
		restartVerifyBackupRunning = backupFn
	}
	if stateFn != nil {
		restartVerifyDaemonState = stateFn
	}
	// The state stub ignores its input, but restartAndVerifyDaemon still probes systemd
	// presence to build the input; pin it so no real systemctl runs.
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}
}

func alignedFresh() health.DaemonState {
	return health.DaemonState{ProcessAlive: true, Aligned: true, AlignChecked: true, StartTS: 100, Version: "9.9.9"}
}

// TestRestartAndVerifyDaemonSuccess: a state that is not-aligned on the first poll and
// then fresh+aligned resolves to a success result (Restarted, alive, aligned, fresh).
func TestRestartAndVerifyDaemonSuccess(t *testing.T) {
	shrinkRestartBudgets(t)
	restarts := 0
	calls := 0
	stubRestartSeams(t,
		func(context.Context) error { restarts++; return nil },
		func(string) bool { return false }, // idle
		func(health.DaemonStateInput) health.DaemonState {
			calls++
			if calls == 1 {
				return health.DaemonState{ProcessAlive: true, Aligned: false, AlignChecked: false}
			}
			return alignedFresh()
		})

	rv := restartAndVerifyDaemon(context.Background(), t.TempDir(), "", 0)

	if restarts != 1 {
		t.Fatalf("restart must run once, got %d", restarts)
	}
	if !rv.Restarted || !rv.ProcessAlive || !rv.Aligned || !rv.FreshInfo || rv.TimedOut {
		t.Fatalf("expected fresh aligned success, got %+v", rv)
	}
	if rv.BackupWaitTimedOut || rv.Err != nil {
		t.Fatalf("unexpected deferral/error: %+v", rv)
	}
	if rv.State.Version != "9.9.9" {
		t.Fatalf("State not captured: %+v", rv.State)
	}
}

// TestRestartAndVerifyDaemonTimedOut: a daemon that never comes back aligned makes the
// bounded poll give up with TimedOut (still Restarted).
func TestRestartAndVerifyDaemonTimedOut(t *testing.T) {
	shrinkRestartBudgets(t)
	stubRestartSeams(t,
		func(context.Context) error { return nil },
		func(string) bool { return false },
		func(health.DaemonStateInput) health.DaemonState {
			return health.DaemonState{ProcessAlive: true, Aligned: false, AlignChecked: false} // never aligns
		})

	rv := restartAndVerifyDaemon(context.Background(), t.TempDir(), "", 0)

	if !rv.Restarted || !rv.TimedOut {
		t.Fatalf("expected restarted+timed-out, got %+v", rv)
	}
	if rv.Aligned {
		t.Fatalf("must not report aligned: %+v", rv)
	}
}

// TestRestartAndVerifyDaemonRestartError: a restart error returns {Err} with Restarted
// false and no poll performed.
func TestRestartAndVerifyDaemonRestartError(t *testing.T) {
	shrinkRestartBudgets(t)
	sentinel := errors.New("systemctl boom")
	polls := 0
	stubRestartSeams(t,
		func(context.Context) error { return sentinel },
		func(string) bool { return false },
		func(health.DaemonStateInput) health.DaemonState { polls++; return alignedFresh() })

	rv := restartAndVerifyDaemon(context.Background(), t.TempDir(), "", 0)

	if rv.Err == nil || !errors.Is(rv.Err, sentinel) {
		t.Fatalf("expected restart error, got %+v", rv)
	}
	if rv.Restarted {
		t.Fatalf("Restarted must be false on restart error: %+v", rv)
	}
	if polls != 0 {
		t.Fatalf("must not poll after a restart error, polled %d", polls)
	}
}

// TestRestartAndVerifyDaemonBackupWaitThenRestart: a backup that is running for the first
// few probes and then frees makes restartAndVerifyDaemon WAIT, then restart+verify.
func TestRestartAndVerifyDaemonBackupWaitThenRestart(t *testing.T) {
	shrinkRestartBudgets(t)
	restarts := 0
	probe := 0
	stubRestartSeams(t,
		func(context.Context) error { restarts++; return nil },
		func(string) bool { probe++; return probe <= 3 }, // busy for the first 3 probes
		func(health.DaemonStateInput) health.DaemonState { return alignedFresh() })

	rv := restartAndVerifyDaemon(context.Background(), t.TempDir(), "", 0)

	if probe < 4 {
		t.Fatalf("expected to poll the backup probe until it freed, got %d", probe)
	}
	if restarts != 1 {
		t.Fatalf("restart must run after the backup frees, got %d", restarts)
	}
	if !rv.Restarted || rv.BackupWaitTimedOut {
		t.Fatalf("expected a restart after waiting, got %+v", rv)
	}
}

// TestRestartAndVerifyDaemonBackupNeverFree: a backup that never frees defers the restart
// (BackupWaitTimedOut) and NEVER calls the restart -- a running backup is not killed.
func TestRestartAndVerifyDaemonBackupNeverFree(t *testing.T) {
	shrinkRestartBudgets(t)
	restarts := 0
	stubRestartSeams(t,
		func(context.Context) error { restarts++; return nil },
		func(string) bool { return true }, // always busy
		func(health.DaemonStateInput) health.DaemonState { return alignedFresh() })

	rv := restartAndVerifyDaemon(context.Background(), t.TempDir(), "", 0)

	if !rv.BackupWaitTimedOut {
		t.Fatalf("expected BackupWaitTimedOut, got %+v", rv)
	}
	if rv.Restarted || restarts != 0 {
		t.Fatalf("must NOT restart while a backup runs: restarted=%v calls=%d", rv.Restarted, restarts)
	}
}

// TestVerifyDaemonAligned: the poll-only variant returns success once the daemon is
// alive+aligned (no restart), and TimedOut when it never aligns.
func TestVerifyDaemonAligned(t *testing.T) {
	shrinkRestartBudgets(t)

	t.Run("success", func(t *testing.T) {
		restarts := 0
		stubRestartSeams(t,
			func(context.Context) error { restarts++; return nil },
			func(string) bool { return false },
			func(health.DaemonStateInput) health.DaemonState { return alignedFresh() })
		rv := verifyDaemonAligned(context.Background(), t.TempDir(), 0)
		if !rv.ProcessAlive || !rv.Aligned || rv.TimedOut {
			t.Fatalf("expected aligned success, got %+v", rv)
		}
		if rv.Restarted || restarts != 0 {
			t.Fatalf("verify must NOT restart: restarted=%v calls=%d", rv.Restarted, restarts)
		}
	})

	t.Run("behind returns immediately, not a timeout", func(t *testing.T) {
		// A daemon that is up and assessable but NOT aligned (behind) must be reported at once,
		// the SAME verdict --daemon-status gives - never polled-until-aligned into a timeout.
		stubRestartSeams(t, nil,
			func(string) bool { return false },
			func(health.DaemonStateInput) health.DaemonState {
				return health.DaemonState{ProcessAlive: true, AlignChecked: true, Aligned: false, HaveInfo: true}
			})
		rv := verifyDaemonAligned(context.Background(), t.TempDir(), 0)
		if rv.TimedOut {
			t.Fatalf("a running-but-behind daemon must be reported, not time out: %+v", rv)
		}
		if !rv.ProcessAlive || rv.Aligned {
			t.Fatalf("expected process-alive + not-aligned (behind), got %+v", rv)
		}
	})

	t.Run("timeout only when never alive", func(t *testing.T) {
		stubRestartSeams(t, nil,
			func(string) bool { return false },
			func(health.DaemonStateInput) health.DaemonState {
				return health.DaemonState{ProcessAlive: false, Aligned: false}
			})
		rv := verifyDaemonAligned(context.Background(), t.TempDir(), 0)
		if !rv.TimedOut || rv.Aligned {
			t.Fatalf("expected timeout, got %+v", rv)
		}
	})
}

// TestDaemonIsActiveGating: the restart decision predicate is true only when the unit is
// installed AND the presence probe reports active.
func TestDaemonIsActiveGating(t *testing.T) {
	oInstalled, oPresence := daemonInstalledProbe, daemonPresenceProbe
	t.Cleanup(func() { daemonInstalledProbe = oInstalled; daemonPresenceProbe = oPresence })

	cases := []struct {
		name      string
		installed bool
		active    bool
		want      bool
	}{
		{"installed+active", true, true, true},
		{"installed+inactive", true, false, false},
		{"absent+active", false, true, false},
		{"absent+inactive", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			daemonInstalledProbe = func() bool { return tc.installed }
			daemonPresenceProbe = func(context.Context) health.DaemonPresence {
				return health.DaemonPresence{Probed: true, Installed: tc.installed, Active: tc.active}
			}
			if got := daemonIsActive(context.Background()); got != tc.want {
				t.Fatalf("daemonIsActive = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpgradeRestartDecisionGate: the upgrade path restarts only when it is enabled AND
// the daemon is active. The dashboard suppresses it (upgradeRestartsDaemon=false), so
// the combined gate is false even with an active daemon.
func TestUpgradeRestartDecisionGate(t *testing.T) {
	oInstalled, oPresence, oGate := daemonInstalledProbe, daemonPresenceProbe, upgradeRestartsDaemon
	t.Cleanup(func() {
		daemonInstalledProbe = oInstalled
		daemonPresenceProbe = oPresence
		upgradeRestartsDaemon = oGate
	})
	daemonInstalledProbe = func() bool { return true }
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: true, Active: true}
	}

	upgradeRestartsDaemon = true
	if !(upgradeRestartsDaemon && daemonIsActive(context.Background())) {
		t.Fatal("CLI upgrade with an active daemon must restart")
	}
	upgradeRestartsDaemon = false
	if upgradeRestartsDaemon && daemonIsActive(context.Background()) {
		t.Fatal("dashboard-suppressed upgrade must NOT restart inline")
	}
}

// TestSummarizeRestartVerify covers the upgrade-footer one-liner for each outcome.
func TestSummarizeRestartVerify(t *testing.T) {
	if line, warn := summarizeRestartVerify(nil, "1.2.3"); line != "" || warn {
		t.Fatalf("nil must yield empty, got %q warn=%v", line, warn)
	}
	success := &RestartVerifyResult{Restarted: true, ProcessAlive: true, Aligned: true, FreshInfo: true}
	if line, warn := summarizeRestartVerify(success, "1.2.3"); warn || !strings.Contains(line, "aligned (v1.2.3)") {
		t.Fatalf("success line wrong: %q warn=%v", line, warn)
	}
	deferred := &RestartVerifyResult{BackupWaitTimedOut: true}
	if line, warn := summarizeRestartVerify(deferred, ""); !warn || !strings.Contains(line, "deferred") {
		t.Fatalf("deferred line wrong: %q warn=%v", line, warn)
	}
	timedOut := &RestartVerifyResult{Restarted: true, TimedOut: true}
	if line, warn := summarizeRestartVerify(timedOut, ""); !warn || !strings.Contains(line, "alignment check timeout") {
		t.Fatalf("timeout line wrong: %q warn=%v", line, warn)
	}
	failed := &RestartVerifyResult{Err: errors.New("boom")}
	if line, warn := summarizeRestartVerify(failed, ""); !warn || !strings.Contains(line, "restart failed") {
		t.Fatalf("error line wrong: %q warn=%v", line, warn)
	}
}

// TestRestartVerifyStatus covers the styled daemon-result mapping (level + short keyword +
// explanation) for each restart+verify outcome, shared by the restart button and the
// post-upgrade restart.
func TestRestartVerifyStatus(t *testing.T) {
	success := RestartVerifyResult{Restarted: true, ProcessAlive: true, Aligned: true, FreshInfo: true, State: health.DaemonState{Version: "4.5.6"}}
	// Success: the version is in the keyword; the explanation is EMPTY (a what-to-do suggestion
	// appears only on a problem outcome).
	if level, keyword, msg := restartVerifyStatus(success); level != orchestrator.HealthcheckSetupLevelOk ||
		keyword != "RESTARTED, ALIGNED (v4.5.6)" || msg != "" {
		t.Fatalf("success status wrong: level=%v keyword=%q msg=%q", level, keyword, msg)
	}
	deferred := RestartVerifyResult{BackupWaitTimedOut: true}
	if level, keyword, _ := restartVerifyStatus(deferred); level != orchestrator.HealthcheckSetupLevelWarn ||
		keyword != "DEFERRED - BACKUP RUNNING" {
		t.Fatalf("deferred status wrong: level=%v keyword=%q", level, keyword)
	}
	timedOut := RestartVerifyResult{Restarted: true, TimedOut: true}
	if level, keyword, _ := restartVerifyStatus(timedOut); level != orchestrator.HealthcheckSetupLevelWarn ||
		keyword != "RESTARTED, NOT CONFIRMED" {
		t.Fatalf("timed-out status wrong: level=%v keyword=%q", level, keyword)
	}
	ambiguous := RestartVerifyResult{Restarted: true} // restarted but not confirmed aligned (default arm)
	if level, keyword, _ := restartVerifyStatus(ambiguous); level != orchestrator.HealthcheckSetupLevelWarn ||
		keyword != "RESTARTED, NOT CONFIRMED" {
		t.Fatalf("ambiguous status wrong: level=%v keyword=%q", level, keyword)
	}
	failed := RestartVerifyResult{Err: errors.New("x")}
	if level, keyword, msg := restartVerifyStatus(failed); level != orchestrator.HealthcheckSetupLevelError ||
		keyword != "RESTART FAILED" || msg != "x" {
		t.Fatalf("failed status wrong: level=%v keyword=%q msg=%q", level, keyword, msg)
	}
}

// TestBuildDaemonResultPrompt: the styled result prompt carries the "Status: " label and the
// colored keyword (matching the daemon-status screen's Status block), plus the explanation.
func TestBuildDaemonResultPrompt(t *testing.T) {
	prompt := ansi.Strip(buildDaemonResultPrompt(orchestrator.HealthcheckSetupLevelOk, "restarted, aligned (v9.9.9)", "all good"))
	if !strings.Contains(prompt, "Status: ") {
		t.Fatalf("prompt must carry the Status label: %q", prompt)
	}
	if !strings.Contains(prompt, "restarted, aligned (v9.9.9)") {
		t.Fatalf("prompt must carry the keyword: %q", prompt)
	}
	if !strings.Contains(prompt, "all good") {
		t.Fatalf("prompt must carry the explanation: %q", prompt)
	}
	// A blank line separates the Status line from the what-to-do suggestion.
	if !strings.Contains(prompt, "\n\nall good") {
		t.Fatalf("a blank line must separate Status from the suggestion: %q", prompt)
	}
	// A success outcome (empty explanation) is just the Status line, no trailing text.
	okOnly := ansi.Strip(buildDaemonResultPrompt(orchestrator.HealthcheckSetupLevelOk, "restarted, aligned (v9.9.9)", ""))
	if strings.Contains(okOnly, "\n") {
		t.Fatalf("a success result must be a single Status line, got: %q", okOnly)
	}
}

// assertNoRawInjection fails if the rendered prompt carries any raw terminal-control marker an
// attacker could weaponize: an OSC title-set (ESC ] 0 ;), a bare BEL (0x07), the C1 CSI byte
// (0x9b), or a CSI erase-screen (ESC [ 2 J). It asserts on ABSENCE of the INJECTED markers, not
// "no ESC at all": theme rendering adds its own ESC[..m SGR color codes, which are legitimate.
func assertNoRawInjection(t *testing.T, out string) {
	t.Helper()
	for _, bad := range []string{"\x1b]0;", "\x07", "\x9b", "\x1b[2J"} {
		if strings.Contains(out, bad) {
			t.Fatalf("prompt leaks raw control sequence %q into the terminal: %q", bad, out)
		}
	}
}

// TestBuildDaemonResultPromptSanitizesInjection: a keyword/explanation carrying raw escape bytes
// (OSC title-set + BEL, a CSI erase-screen) must NOT reach the verbatim WithSelectorPromptStyled
// path as raw sequences, while the human-readable text survives. This closes the injection path
// for daemon action outcomes (restart keyword embeds the daemon Version; error explanations embed
// external tool / error strings).
func TestBuildDaemonResultPromptSanitizesInjection(t *testing.T) {
	prompt := buildDaemonResultPrompt(
		orchestrator.HealthcheckSetupLevelOk,
		"restarted, aligned (v1.0\x1b]0;pwned\x07)",
		"\x1b[2J\x07evil",
	)
	assertNoRawInjection(t, prompt)
	// An OSC ("...pwned...") is stripped WHOLE, payload included; only text that
	// sits outside any escape survives.
	if strings.Contains(prompt, "pwned") {
		t.Fatalf("OSC payload must be stripped with its escape: %q", prompt)
	}
	for _, want := range []string{"restarted, aligned (v1.0)", "evil"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("sanitized prompt dropped legitimate text %q: %q", want, prompt)
		}
	}
}

// TestRestartKeywordVersionInjectionSanitized: a hostile daemon Version (RAW from
// .daemon_info.json) reaches the restart-result keyword as "restarted, aligned (v<version>)",
// then flows restartVerifyStatus -> buildDaemonResultPrompt. Confirm the Version-carrying keyword
// actually goes through the sanitized builder (no separate raw render), so the escape is scrubbed
// end-to-end.
func TestRestartKeywordVersionInjectionSanitized(t *testing.T) {
	hostile := RestartVerifyResult{
		Restarted:    true,
		ProcessAlive: true,
		Aligned:      true,
		FreshInfo:    true,
		State:        health.DaemonState{Version: "1.0\x1b]0;pwned\x07"},
	}
	level, keyword, explanation := restartVerifyStatus(hostile)
	// The raw Version rides in the keyword verbatim; the builder is the sanitize boundary.
	if !strings.Contains(keyword, "\x1b]0;") {
		t.Fatalf("expected the raw Version to ride in the keyword (sanitize happens in the builder): %q", keyword)
	}
	prompt := buildDaemonResultPrompt(level, keyword, explanation)
	assertNoRawInjection(t, prompt)
	if !strings.Contains(prompt, "RESTARTED, ALIGNED (v1.0)") || strings.Contains(prompt, "pwned") {
		t.Fatalf("sanitized restart keyword should keep the plaintext version and drop the OSC payload: %q", prompt)
	}
}

// TestDefaultBackupRunningNoLock: with no lock file at the resolved path, the production
// probe reports "not running" (nothing to wait for).
func TestDefaultBackupRunningNoLock(t *testing.T) {
	if defaultBackupRunning(checks.DefaultBackupLockPath(t.TempDir())) {
		t.Fatal("missing lock file must report no backup running")
	}
}

// TestBackupLockFilePathHonoursCustomLockPath: the probe path is derived from cfg.LockPath
// (the same <cfg.LockPath>/.backup.lock the orchestrator's Checker acquires), NOT the
// base-dir default -- the exact regression that let a restart kill a backup on a
// custom-LOCK_PATH host. A nil/blank-LockPath config falls back to the base-dir default.
func TestBackupLockFilePathHonoursCustomLockPath(t *testing.T) {
	baseDir := t.TempDir()
	custom := t.TempDir() // a LOCK_PATH override that is NOT <baseDir>/lock
	if got, want := backupLockFilePath(&config.Config{LockPath: custom}, baseDir),
		filepath.Join(custom, checks.BackupLockFileName); got != want {
		t.Fatalf("custom LOCK_PATH: got %q, want %q", got, want)
	}
	if got, want := backupLockFilePath(&config.Config{}, baseDir), checks.DefaultBackupLockPath(baseDir); got != want {
		t.Fatalf("blank LockPath must fall back: got %q, want %q", got, want)
	}
	if got, want := backupLockFilePath(nil, baseDir), checks.DefaultBackupLockPath(baseDir); got != want {
		t.Fatalf("nil cfg must fall back: got %q, want %q", got, want)
	}
}

// TestRestartAndVerifyDaemonFreshnessGate: the freshness clause (st.StartTS > preStartTS)
// is load-bearing. A pre-restart identity record with StartTS=100 is written, and the
// daemon-state seam reports ProcessAlive+Aligned+AlignChecked but the SAME StartTS=100
// (not advanced past the snapshot). The restart must NOT count as successful -- the poll
// must exhaust its budget and return TimedOut. This catches a mutation that drops the
// `&& st.StartTS > preStartTS` clause (which would otherwise report a stale process as a
// fresh, successful restart).
func TestRestartAndVerifyDaemonFreshnessGate(t *testing.T) {
	shrinkRestartBudgets(t)
	baseDir := t.TempDir()
	if err := health.WriteDaemonInfo(baseDir, health.DaemonInfo{StartTS: 100}); err != nil {
		t.Fatalf("seed daemon info: %v", err)
	}
	stubRestartSeams(t,
		func(context.Context) error { return nil },
		func(string) bool { return false }, // idle backup
		// Alive+aligned but StartTS==preStartTS (not strictly greater): not fresh.
		func(health.DaemonStateInput) health.DaemonState {
			return health.DaemonState{ProcessAlive: true, Aligned: true, AlignChecked: true, StartTS: 100}
		})

	rv := restartAndVerifyDaemon(context.Background(), baseDir, "", 0)

	if !rv.Restarted || !rv.TimedOut {
		t.Fatalf("same-or-older StartTS must NOT count as fresh; expected restarted+timed-out, got %+v", rv)
	}
	if rv.FreshInfo {
		t.Fatalf("FreshInfo must be false when StartTS did not advance: %+v", rv)
	}
}

// TestDashboardDaemonRestartButton: the in-session "Restart daemon" button drives
// restartAndVerifyDaemon (seams stubbed: idle backup, no-op restart, aligned state) and
// shows the success notice, then loops back to the menu without setting a flag.
func TestDashboardDaemonRestartButton(t *testing.T) {
	installDashboardGates(t, true, true)
	shrinkRestartBudgets(t)
	tmp := t.TempDir()
	// Active daemon so the menu offers the Restart row, and BaseDir points at a temp dir.
	orig := daemonStatusLoadConfig
	daemonStatusLoadConfig = func(configPath, baseDir string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon", BaseDir: tmp}, nil
	}
	t.Cleanup(func() { daemonStatusLoadConfig = orig })

	restarts := 0
	stubRestartSeams(t,
		func(context.Context) error { restarts++; return nil },
		func(string) bool { return false },
		func(health.DaemonStateInput) health.DaemonState { return alignedFresh() })

	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	// Active layout: Disable daemon (9 downs) -> Restart daemon (10 downs).
	driver.keys("down down down down down down down down down down enter")
	driver.waitScreen("Daemon restart") // styled result screen (selector title)
	driver.keys("enter")                // Back -> menu
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc must exit handled")
		}
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}
	if restarts != 1 {
		t.Fatalf("Restart daemon button must restart once, got %d", restarts)
	}
	if args.DaemonSetup || args.DaemonRemove {
		t.Fatalf("Restart button must set no flag: %+v", args)
	}
}
