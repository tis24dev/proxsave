package main

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// installDashboardGates fixes the two gate seams for a test. It also pins the
// daemon menu state to DaemonStateOnCron (config load -> cron) so the menu layout
// is deterministic (offers "Install daemon" + "Daemon status").
func installDashboardGates(t *testing.T, bare, interactive bool) {
	t.Helper()
	origBare := dashboardIsBareInvocation
	origInteractive := dashboardIsInteractive
	origDaemonCfg := daemonStatusLoadConfig
	origApplyDaemon := daemonApplyDaemonMode
	origApplyCron := daemonApplyCronMode
	dashboardIsBareInvocation = func() bool { return bare }
	dashboardIsInteractive = func() bool { return interactive }
	daemonStatusLoadConfig = func(configPath, baseDir string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "cron"}, nil
	}
	// Stub the privileged apply ops (no real systemctl / /etc/systemd writes).
	daemonApplyDaemonMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) error {
		return nil
	}
	daemonApplyCronMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger, optOut bool) error {
		return nil
	}
	t.Cleanup(func() {
		dashboardIsBareInvocation = origBare
		dashboardIsInteractive = origInteractive
		daemonStatusLoadConfig = origDaemonCfg
		daemonApplyDaemonMode = origApplyDaemon
		daemonApplyCronMode = origApplyCron
	})
}

// TestDashboardGateNonInteractiveNeverIntercepts is the cron-safety
// contract: without an interactive terminal (or with any flag present) the
// dashboard must never appear and the args must stay untouched, so the
// runtime path is byte-identical to today.
func TestDashboardGateNonInteractiveNeverIntercepts(t *testing.T) {
	cases := []struct {
		name        string
		bare        bool
		interactive bool
	}{
		{"cron (no tty)", true, false},
		{"flags present", false, true},
		{"flags present and no tty", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, tc.bare, tc.interactive)
			args := &cli.Args{}
			code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
			if handled {
				t.Fatalf("dashboard must not intercept (code=%d)", code)
			}
			if args.Restore || args.Decrypt || args.ForceNewKey || args.Install || args.Backup {
				t.Fatalf("args mutated: %+v", args)
			}
		})
	}
}

func installDashboardSessionSeam(t *testing.T) *newkeyUIDriver {
	t.Helper()
	d := installNewkeySessionSeam(t)
	orig := testDashboardSession
	testDashboardSession = func(ctx context.Context) *shell.Session {
		return newAgeSetupSession(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	}
	t.Cleanup(func() {
		testDashboardSession = orig
		releaseDashboardLeftovers()
	})
	return d
}

func runDashboardWith(t *testing.T, keys string) (*cli.Args, int, bool) {
	t.Helper()
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.keys(keys)
	select {
	case res := <-resCh:
		return args, res.code, res.handled
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
		return nil, 0, false
	}
}

func TestDashboardActions(t *testing.T) {
	cases := []struct {
		name    string
		keys    string
		handled bool
		check   func(t *testing.T, args *cli.Args)
	}{
		{"backup falls through", "enter", false, func(t *testing.T, args *cli.Args) {
			if args.Restore || args.Decrypt || args.ForceNewKey || args.Install {
				t.Fatalf("backup must not set mode flags: %+v", args)
			}
			if args.LogLevel == types.LogLevelDebug {
				t.Fatal("plain backup must not force debug log level")
			}
		}},
		{"restore", "down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.Restore {
				t.Fatal("restore flag not set")
			}
		}},
		{"decrypt", "down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.Decrypt {
				t.Fatal("decrypt flag not set")
			}
		}},
		{"newkey", "down down down enter", false, func(t *testing.T, args *cli.Args) {
			if !args.ForceNewKey {
				t.Fatal("newkey flag not set")
			}
		}},
		// Install is now a single row that opens an in-session chooser (Edit install /
		// Wipe install); its two flag dispatches are covered by the dedicated install
		// chooser tests below, not this fall-through harness.
		// Exit is the last selectable (14th): 13 downs, skipping every separator.
		{"exit row", "down down down down down down down down down down down down down enter", true, nil},
		{"esc exits", "esc", true, nil},
		{"ctrl+c exits", "ctrl+c", true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args, code, handled := runDashboardWith(t, tc.keys)
			if handled != tc.handled {
				t.Fatalf("handled = %v, want %v (code=%d)", handled, tc.handled, code)
			}
			if handled && code != types.ExitSuccess.Int() {
				t.Fatalf("exit path must be success, got %d", code)
			}
			if tc.check != nil {
				tc.check(t, args)
			}
		})
	}
}

// installChooserResult drives the menu to Install (4 downs), then the given chooser keys,
// and returns whether maybeRunDashboard reported handled plus the mutated args. loopsBack
// is true for the Back choice (which re-opens the menu, so it esc-exits afterwards).
func installChooserResult(t *testing.T, chooserKeys string, loopsBack bool) (*cli.Args, bool) {
	t.Helper()
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down enter") // Install (4 downs) -> chooser
	driver.waitScreen("Install")             // the in-session chooser
	driver.keys(chooserKeys)
	if loopsBack {
		driver.waitScreen("Dashboard") // Back re-opened the menu
		driver.keys("esc")             // exit it so maybeRunDashboard resolves
	}
	select {
	case handled := <-resCh:
		return args, handled
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
		return nil, false
	}
}

// TestDashboardInstallEditDispatches: the Install chooser's "Edit install" resolves to the
// --install flow (args.Install), falling through to the normal flag dispatch.
func TestDashboardInstallEditDispatches(t *testing.T) {
	args, handled := installChooserResult(t, "enter", false) // Edit install (1st item)
	if handled {
		t.Fatal("Edit install must fall through to the flag dispatch")
	}
	if !args.Install || args.NewInstall {
		t.Fatalf("Edit install must set --install only: %+v", args)
	}
}

// TestDashboardInstallWipeDispatches: "Wipe install" resolves to the --new-install flow.
func TestDashboardInstallWipeDispatches(t *testing.T) {
	args, handled := installChooserResult(t, "down enter", false) // Wipe install (2nd item)
	if handled {
		t.Fatal("Wipe install must fall through to the flag dispatch")
	}
	if !args.NewInstall || args.Install {
		t.Fatalf("Wipe install must set --new-install only: %+v", args)
	}
}

// TestDashboardInstallBackLoops: Back on the Install chooser returns to the menu WITHOUT
// setting any install flag (then esc exits).
func TestDashboardInstallBackLoops(t *testing.T) {
	args, handled := installChooserResult(t, "down down enter", true) // Back (3rd item)
	if !handled {
		t.Fatal("esc from the menu must exit handled")
	}
	if args.Install || args.NewInstall {
		t.Fatalf("Back must set no install flag: %+v", args)
	}
}

// TestDaemonStatusStyleBehind: a daemon on an OLDER binary than the one on disk reads the
// distinct "behind - restart needed" warning, which takes precedence over the heartbeat-derived
// "running, not reporting" (that keeps rendering for an aligned daemon, so the two stay DISTINCT).
func TestDaemonStatusStyleBehind(t *testing.T) {
	behind := health.DaemonState{
		HaveInfo:     true,
		AlignChecked: true, // a real comparison ran and mismatched -> genuinely behind
		Aligned:      false,
		Active:       true,
		Diagnosis:    health.Diagnosis{State: health.TxRunningNoReport},
	}
	level, outcome, expl := daemonStatusStyle(behind)
	if level != orchestrator.HealthcheckSetupLevelWarn {
		t.Fatalf("behind level = %v, want HealthcheckSetupLevelWarn", level)
	}
	if outcome != "behind - restart needed" {
		t.Fatalf("behind outcome = %q, want %q", outcome, "behind - restart needed")
	}
	if !strings.Contains(expl, "restart") {
		t.Fatalf("behind explanation should mention restart, got %q", expl)
	}

	// The SAME underlying TxRunningNoReport but ALIGNED must still read as the separate
	// "running, not reporting" state, never conflated with "behind".
	running := health.DaemonState{
		HaveInfo:     true,
		AlignChecked: true,
		Aligned:      true,
		Active:       true,
		Diagnosis:    health.Diagnosis{State: health.TxRunningNoReport},
	}
	if _, gotOutcome, _ := daemonStatusStyle(running); gotOutcome != "running, not reporting" {
		t.Fatalf("aligned running outcome = %q, want %q", gotOutcome, "running, not reporting")
	}
}

// TestDaemonStatusStyleLevels: the healthy/beating daemon reads Ok (green ✓); every gap reads
// Warn (yellow ⚠). This is the level mapping the styled Status line consumes.
func TestDaemonStatusStyleLevels(t *testing.T) {
	running := health.DaemonState{Diagnosis: health.Diagnosis{State: health.TxTransmitting}}
	if level, outcome, _ := daemonStatusStyle(running); level != orchestrator.HealthcheckSetupLevelOk || outcome != "running" {
		t.Fatalf("running -> (%v, %q), want (Ok, running)", level, outcome)
	}
	gaps := []struct {
		name  string
		state health.TxState
	}{
		{"not installed", health.TxNotInstalled},
		{"not active", health.TxNotActive},
		{"running no report", health.TxRunningNoReport},
		{"stale", health.TxStale},
		{"no heartbeat", health.TxNoHeartbeat},
	}
	for _, g := range gaps {
		ds := health.DaemonState{Diagnosis: health.Diagnosis{State: g.state}}
		if level, _, _ := daemonStatusStyle(ds); level != orchestrator.HealthcheckSetupLevelWarn {
			t.Fatalf("%s level = %v, want HealthcheckSetupLevelWarn", g.name, level)
		}
	}
}

// TestRenderDaemonStatusLevel: the colored-keyword renderer prefixes the success symbol for Ok
// and the warning symbol for Warn (same palette as the Telegram/Healthchecks screens), and emits
// no symbol for the Neutral pre-check level.
func TestRenderDaemonStatusLevel(t *testing.T) {
	ok := ansi.Strip(renderDaemonStatusLevel(orchestrator.HealthcheckSetupLevelOk, "running"))
	if !strings.Contains(ok, theme.SymbolSuccess) || !strings.Contains(ok, "running") {
		t.Fatalf("Ok render = %q, want success symbol + text", ok)
	}
	warn := ansi.Strip(renderDaemonStatusLevel(orchestrator.HealthcheckSetupLevelWarn, "behind - restart needed"))
	if !strings.Contains(warn, theme.SymbolWarning) || !strings.Contains(warn, "behind - restart needed") {
		t.Fatalf("Warn render = %q, want warning symbol + text", warn)
	}
	neutral := ansi.Strip(renderDaemonStatusLevel(orchestrator.HealthcheckSetupLevelNeutral, "not checked"))
	if strings.ContainsAny(neutral, theme.SymbolSuccess+theme.SymbolWarning+theme.SymbolError) {
		t.Fatalf("Neutral render = %q, want no symbol", neutral)
	}
}

// TestBuildDaemonStatusPrompt: the styled prompt carries the "Status: " header, the colored
// keyword, the explanation, and the Details block (including the version + BEHIND alignment line
// for a behind daemon) -- the same content the old Notice body carried, now above the selector.
func TestBuildDaemonStatusPrompt(t *testing.T) {
	behind := health.DaemonState{
		HaveInfo:     true,
		Version:      "1.2.3",
		Commit:       "abc1234",
		AlignChecked: true,
		Aligned:      false,
		Active:       true,
		Diagnosis:    health.Diagnosis{State: health.TxRunningNoReport},
	}
	level, keyword, expl := daemonStatusStyle(behind)
	prompt := ansi.Strip(buildDaemonStatusPrompt(level, keyword, expl, "daemon", "installed", "active", "no", behind))
	for _, want := range []string{
		"Status: ",
		keyword,
		expl,
		"Details:",
		"Scheduler mode: daemon",
		"Daemon service (proxsave-daemon.service): installed",
		"Service state (systemctl is-active): active",
		"Opted out of auto-migration (--daemon-remove): no",
		"Running version: 1.2.3 (abc1234)",
		"Binary alignment: BEHIND (restart needed)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

// TestDaemonStatusStyleBehindWithoutRecord: the record-less-but-stale daemon (HaveInfo=false, but
// alignment determined by the /proc fallback) must now render "behind - restart needed". This is the
// core of the fix: before, the behind gate required HaveInfo, so a record-less stale daemon read
// GREEN. The gate is now AlignChecked && !Aligned && (Active || ProcessAlive), no record required.
func TestDaemonStatusStyleBehindWithoutRecord(t *testing.T) {
	behind := health.DaemonState{
		HaveInfo:     false, // no identity record (predates the feature / bootstrap first-deploy)
		AlignChecked: true,  // yet the /proc fallback determined alignment
		Aligned:      false, // ...and found the running binary stale
		Active:       true,
		Diagnosis:    health.Diagnosis{State: health.TxRunningNoReport},
	}
	level, outcome, expl := daemonStatusStyle(behind)
	if level != orchestrator.HealthcheckSetupLevelWarn {
		t.Fatalf("behind level = %v, want HealthcheckSetupLevelWarn", level)
	}
	if outcome != "behind - restart needed" {
		t.Fatalf("record-less behind outcome = %q, want %q", outcome, "behind - restart needed")
	}
	if !strings.Contains(expl, "restart") {
		t.Fatalf("behind explanation should mention restart, got %q", expl)
	}

	// UNKNOWN alignment (AlignChecked=false) with no record must NOT read as behind -- it falls
	// through to the transmission-state verdict.
	unknown := behind
	unknown.AlignChecked = false
	if _, gotOutcome, _ := daemonStatusStyle(unknown); gotOutcome == "behind - restart needed" {
		t.Fatalf("UNKNOWN alignment must not read as behind")
	}
}

// TestDashboardDaemonStatusLoopsBack: Daemon status shows the styled selector screen in the
// live session; Back (esc) returns to the menu, setting no flag.
func TestDashboardDaemonStatusLoopsBack(t *testing.T) {
	installDashboardGates(t, true, true) // stubs cron -> Install daemon + Daemon status
	// Deterministic systemd verdict (avoid a real systemctl call): unit absent.
	origProbe := daemonPresenceProbe
	t.Cleanup(func() { daemonPresenceProbe = origProbe })
	daemonPresenceProbe = func(context.Context) health.DaemonPresence {
		return health.DaemonPresence{Probed: true, Installed: false}
	}
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down down enter") // Daemon status (10 downs)
	driver.waitScreen("Daemon status")                                     // the styled selector screen
	driver.keys("esc")                                                     // Back to the menu
	driver.waitScreen("Dashboard")                                         // back at the menu
	driver.keys("esc")                                                     // exit
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if args.DaemonSetup || args.DaemonRemove {
		t.Fatalf("Daemon status must set no flag: %+v", args)
	}
}

// TestDashboardDaemonInstallInSession: "Install daemon" runs the apply op inside a
// RunTask (graphical), shows a success notice, and loops back to the menu WITHOUT
// leaving the UI or setting a flag.
func TestDashboardDaemonInstallInSession(t *testing.T) {
	installDashboardGates(t, true, true) // cron -> Install daemon; apply stubbed -> nil
	applied := 0
	daemonApplyDaemonMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) error {
		applied++
		return nil
	}
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Install daemon (9 downs)
	driver.waitScreen("Daemon installed")                             // success notice (after the RunTask)
	driver.keys("enter")                                              // dismiss
	driver.waitScreen("Dashboard")                                    // looped back
	driver.keys("esc")                                                // exit
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if applied != 1 {
		t.Fatalf("apply-daemon must run once, got %d", applied)
	}
	if args.DaemonSetup || args.DaemonRemove {
		t.Fatalf("in-session daemon install must set no flag: %+v", args)
	}
}

// TestDashboardDaemonRemoveWhenActive: with the daemon active the menu offers
// "Disable daemon", which runs the revert op in-session (RunTask + notice) and
// loops back. An op failure surfaces as an error notice, still non-blocking.
func TestDashboardDaemonRemoveWhenActive(t *testing.T) {
	installDashboardGates(t, true, true)
	orig := daemonStatusLoadConfig
	daemonStatusLoadConfig = func(configPath, baseDir string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	t.Cleanup(func() { daemonStatusLoadConfig = orig })
	reverted := 0
	daemonApplyCronMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger, optOut bool) error {
		reverted++
		if !optOut {
			t.Errorf("disable must opt out (optOut=true)")
		}
		return nil
	}
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	// Active state: Daemon group = "Disable daemon" (row 11, 10 downs) + "Restart" + "Daemon status".
	driver.keys("down down down down down down down down down enter") // Disable daemon
	driver.waitScreen("Daemon disabled")                              // success notice
	driver.keys("enter")                                              // dismiss
	driver.waitScreen("Dashboard")                                    // looped back
	driver.keys("esc")
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if reverted != 1 {
		t.Fatalf("revert-to-cron must run once, got %d", reverted)
	}
	if args.DaemonSetup || args.DaemonRemove {
		t.Fatalf("in-session daemon disable must set no flag: %+v", args)
	}
}

// stubDashboardDiagnostics replaces the three diagnostics screen seams so the
// loop can be driven without an on-disk config or the real Charm screens.
func stubDashboardDiagnostics(t *testing.T, telegramShown, hcShown bool, tele, hc, audit *int) {
	t.Helper()
	origT, origH, origA := dashboardRunTelegramSetup, dashboardRunHealthcheckSetup, dashboardRunPostInstallAudit
	t.Cleanup(func() {
		dashboardRunTelegramSetup = origT
		dashboardRunHealthcheckSetup = origH
		dashboardRunPostInstallAudit = origA
	})
	dashboardRunTelegramSetup = func(ctx context.Context, s *shell.Session, baseDir, configPath string) (installer.TelegramSetupResult, error) {
		*tele++
		return installer.TelegramSetupResult{Shown: telegramShown}, nil
	}
	dashboardRunHealthcheckSetup = func(ctx context.Context, s *shell.Session, baseDir, configPath string) (installer.HealthcheckSetupResult, error) {
		*hc++
		return installer.HealthcheckSetupResult{Shown: hcShown}, nil
	}
	dashboardRunPostInstallAudit = func(ctx context.Context, s *shell.Session, execPath, configPath string) (installer.PostInstallAuditResult, error) {
		*audit++
		return installer.PostInstallAuditResult{}, nil
	}
}

// TestDashboardDiagnosticsLoopBackToMenu: each diagnostics item runs its screen
// in the live session and returns to the menu (never sets a mode flag, never
// ends the dashboard); only Exit/esc ends it.
func TestDashboardDiagnosticsLoopBackToMenu(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	var tele, hc, audit int
	stubDashboardDiagnostics(t, true, true, &tele, &hc, &audit)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down enter")      // Check Telegram (7th selectable)
	driver.waitScreen("Dashboard")                          // looped back after the screen
	driver.keys("down down down down down down down enter") // Check healthchecks (8th)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down enter") // Post-install check (9th)
	driver.waitScreen("Dashboard")
	driver.keys("esc") // exit

	select {
	case res := <-resCh:
		if !res.handled || res.code != types.ExitSuccess.Int() {
			t.Fatalf("esc from menu must exit cleanly, got %+v", res)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if tele != 1 || hc != 1 || audit != 1 {
		t.Fatalf("each diagnostic must run once: tele=%d hc=%d audit=%d", tele, hc, audit)
	}
	if args.Restore || args.Decrypt || args.ForceNewKey || args.Install || args.Backup {
		t.Fatalf("diagnostics must not set any mode flag: %+v", args)
	}
}

// TestDashboardDiagnosticNotConfiguredShowsNotice: when a setup screen is not
// eligible (Shown=false), a dismissible styled "Status: NOT CONFIGURED" result
// screen appears instead of a blank flicker, then the menu returns.
func TestDashboardDiagnosticNotConfiguredShowsNotice(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	var tele, hc, audit int
	stubDashboardDiagnostics(t, false, true, &tele, &hc, &audit) // telegram not configured

	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down enter") // Check Telegram (not configured)
	driver.waitScreen("Telegram")                      // the styled "Status:" result screen
	driver.waitOutput("NOT CONFIGURED")                // Status: ⚠ NOT CONFIGURED
	driver.keys("enter")                               // dismiss (Back)
	driver.waitScreen("Dashboard")                     // back at the menu
	driver.keys("esc")                                 // exit

	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if tele != 1 {
		t.Fatalf("the telegram check must have run once, got %d", tele)
	}
}

// TestDashboardUIDeathIsExitNotBackup: a dying UI must never fall through to
// a surprise backup for a human sitting at the menu.
func TestDashboardUIDeathIsExitNotBackup(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.cancel() // kill the UI program
	select {
	case res := <-resCh:
		if !res.handled || res.code != types.ExitSuccess.Int() {
			t.Fatalf("UI death must exit cleanly, got %+v", res)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve after UI death")
	}
}

// TestDashboardBareInvocationGateReal exercises the REAL bare-invocation
// check by swapping os.Args (a mutation like <=2 must fail here).
func TestDashboardBareInvocationGateReal(t *testing.T) {
	origArgs := os.Args
	t.Cleanup(func() { os.Args = origArgs })

	os.Args = []string{"proxsave"}
	if !dashboardBareInvocationCheck() {
		t.Fatal("bare invocation must be detected")
	}
	os.Args = []string{"proxsave", "--dry-run"}
	if dashboardBareInvocationCheck() {
		t.Fatal("any flag must make the invocation non-bare")
	}
	os.Args = []string{"proxsave", "--backup"}
	if dashboardBareInvocationCheck() {
		t.Fatal("--backup must bypass the dashboard")
	}
}

// TestDashboardInteractiveGateUnderTest: under go test there is no terminal,
// so the REAL interactive gate must be false (cron-safety default).
func TestDashboardInteractiveGateUnderTest(t *testing.T) {
	if isDashboardTerminalInteractive() {
		t.Fatal("gate must be false without a real terminal")
	}
}

// TestDashboardFlowActionHandsSessionOver: choosing a flow keeps the session
// alive (stashed for adoption) and adoption consumes it exactly once,
// restoring the console mute it installed.
func TestDashboardFlowActionHandsSessionOver(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	type outcome struct {
		code    int
		handled bool
	}
	resCh := make(chan outcome, 1)
	go func() {
		code, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- outcome{code, handled}
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down enter") // Restore (row 2, 1 down)
	var res outcome
	select {
	case res = <-resCh:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if res.handled || !args.Restore {
		t.Fatalf("restore dispatch broken: handled=%v args=%+v", res.handled, args)
	}
	if !dashboardHandoffPending() {
		t.Fatal("flow action must stash the session for adoption")
	}
	s := adoptDashboardSession(shell.Config{AppName: "ProxSave", Subtitle: "Restore Backup Workflow"})
	if s == nil {
		t.Fatal("adoption must return the stashed session")
	}
	if adoptDashboardSession(shell.Config{}) != nil {
		t.Fatal("adoption must consume the stash (second call nil)")
	}

	// The adopted session must be ALIVE: a real Ask must reach the screen
	// and resolve (this is the regression that shipped once: the dashboard
	// closed the session before stashing, so every flow Ask died with
	// ErrClosed and the workflow reported "aborted by user").
	type askOut struct {
		err error
	}
	askRes := make(chan askOut, 1)
	go func() {
		_, err := shell.Ask(context.Background(), s, components.NewNotice(components.NoticeInfo, "Adopted", "still alive"))
		askRes <- askOut{err}
	}()
	driver.waitScreen("Adopted")
	driver.keys("enter")
	select {
	case r := <-askRes:
		if r.err != nil {
			t.Fatalf("Ask on the adopted session must work, got %v", r.err)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("Ask on the adopted session did not resolve")
	}
	_ = s.Close()
}

// TestDashboardExitStillClosesSession: exit does NOT stash; backup DOES stash so
// runBackupStreamed can adopt the live session and stream the run in-graphics.
func TestDashboardExitStillClosesSession(t *testing.T) {
	_, _, handled := runDashboardWith(t, "esc")
	if !handled {
		t.Fatal("esc must exit")
	}
	if dashboardHandoffPending() {
		t.Fatal("exit must not leave a stashed session")
	}
	args, _, handled := runDashboardWith(t, "enter") // Run backup now
	if handled || args.Restore || args.Install {
		t.Fatalf("backup dispatch broken: %+v", args)
	}
	// Backup now mirrors the flow-action handoff: the graphical session stays
	// open and stashed for the backup to adopt (streamed in-graphics), instead
	// of being closed for a plain-terminal run. releaseDashboardLeftovers in the
	// session-seam cleanup closes the still-stashed session.
	if !dashboardHandoffPending() {
		t.Fatal("backup must stash the session for in-graphics streaming")
	}
}
