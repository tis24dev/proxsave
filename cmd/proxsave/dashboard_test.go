package main

import (
	"context"
	"errors"
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
	"github.com/tis24dev/proxsave/internal/ui/flows/menu"
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
	daemonApplyDaemonMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) (cronRemovalOutcome, error) {
		return cronRemovalOutcome{Removed: 1, Verified: true}, nil
	}
	daemonApplyCronMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{}, nil
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
	// Neutralize the Screen 0 (what's new) hook for the menu-driving tests: these
	// drive keys for the MENU, never for Screen 0, so a real Decide (which reports
	// unseen under the test base) would block the flow on input the driver never
	// sends. Stubbing Decide to "nothing to show" keeps maybeRunDashboard's path
	// byte-identical to before this hook existed. The Screen 0 behavior itself is
	// covered by whatsnew_wiring_test.go.
	origDecide := whatsnewDecide
	whatsnewDecide = func(baseDir, current string) (bool, string, error) { return false, "", nil }
	t.Cleanup(func() {
		testDashboardSession = orig
		whatsnewDecide = origDecide
		releaseDashboardLeftovers()
	})
	return d
}

func runDashboardWith(t *testing.T, keys string) (*cli.Args, int, bool) {
	t.Helper()
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	args := &cli.Args{}
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys(keys)
	select {
	case r := <-res:
		return args, r.code, r.handled
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
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down enter") // Install (4 downs) -> chooser
	driver.waitScreen("Install")             // the in-session chooser
	driver.keys(chooserKeys)
	if loopsBack {
		driver.waitScreen("Dashboard") // Back re-opened the menu
		driver.keys("esc")             // exit it so maybeRunDashboard resolves
	}
	select {
	case r := <-res:
		return args, r.handled
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
	prompt := ansi.Strip(buildDaemonStatusPrompt(level, keyword, expl, "daemon", "installed", "active", behind))
	for _, want := range []string{
		"Status: ",
		keyword,
		expl,
		"Details:",
		"Scheduler mode: daemon",
		"Daemon service (proxsave-daemon.service): installed",
		"Service state (systemctl is-active): active",
		"Running version: 1.2.3 (abc1234)",
		"Binary alignment: BEHIND (restart needed)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n---\n%s", want, prompt)
		}
	}

	// The screen used to carry "Opted out of auto-migration (--daemon-remove): yes|no". There is
	// no opt-out any longer: the retrofit reads whether SCHEDULER_MODE was already in the file,
	// and the "Scheduler mode:" line directly above already states the answer that matters. A
	// line naming a key nobody writes is a claim about the host that is no longer true.
	for _, gone := range []string{"Opted out", "auto-migration", "DAEMON_OPT_OUT"} {
		if strings.Contains(prompt, gone) {
			t.Errorf("the status screen must not mention %q\n---\n%s", gone, prompt)
		}
	}
}

// TestBuildDaemonStatusPromptSanitizesInjection: a daemon whose Version/Commit (RAW from
// .daemon_info.json) and whose config-derived mode / systemctl-derived active carry raw escape
// bytes must NOT render those sequences into the verbatim WithSelectorPromptStyled path, while the
// human-readable text survives. This closes the daemon-info escape path (Version/Commit) plus the
// external mode/active segments. assertNoRawInjection lives in daemon_restart_verify_test.go (same
// package): it asserts on ABSENCE of the injected OSC/BEL/C1/CSI markers, not "no ESC at all"
// (theme rendering adds its own legitimate SGR color codes).
func TestBuildDaemonStatusPromptSanitizesInjection(t *testing.T) {
	behind := health.DaemonState{
		HaveInfo:     true,
		Version:      "1.0\x1b]0;pwned\x07",
		Commit:       "abc\x1b[2Jdef",
		AlignChecked: true,
		Aligned:      false,
		Active:       true,
		Diagnosis:    health.Diagnosis{State: health.TxRunningNoReport},
	}
	level, keyword, expl := daemonStatusStyle(behind)
	prompt := buildDaemonStatusPrompt(
		level, keyword, expl,
		"cron\x1b]0;evilmode\x07", // mode: from the config file
		"installed",
		"active\x9b\x1b]0;x\x07running", // active: from systemctl (0x9b is the bare C1 CSI byte)
		behind,
	)
	assertNoRawInjection(t, prompt)
	// OSC payloads ("pwned", "evilmode") are stripped WHOLE with their escape, so
	// they must NOT survive. Commit's CSI erase carries no payload, so "abcdef"
	// rejoins; the bare 0x9b in "active" drops without eating a real char.
	for _, bad := range []string{"pwned", "evilmode"} {
		if strings.Contains(prompt, bad) {
			t.Fatalf("OSC payload %q must be stripped with its escape\n---\n%s", bad, prompt)
		}
	}
	for _, want := range []string{
		"Running version: 1.0",
		"abcdef",
		"Scheduler mode: cron",
		"Service state (systemctl is-active): activerunning",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("sanitized status prompt dropped legitimate text %q\n---\n%s", want, prompt)
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
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down down enter") // Daemon status (10 downs)
	driver.waitScreen("Daemon status")                                     // the styled selector screen
	driver.keys("esc")                                                     // Back to the menu
	driver.waitScreen("Dashboard")                                         // back at the menu
	driver.keys("esc")                                                     // exit
	select {
	case r := <-res:
		if !r.handled {
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
	daemonApplyDaemonMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) (cronRemovalOutcome, error) {
		applied++
		return cronRemovalOutcome{Removed: 1, Verified: true}, nil
	}
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Install daemon (9 downs)
	driver.waitScreen("Daemon installed")                             // success notice (after the RunTask)
	driver.keys("enter")                                              // dismiss
	driver.waitScreen("Dashboard")                                    // looped back
	driver.keys("esc")                                                // exit
	select {
	case r := <-res:
		if !r.handled {
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

// The dashboard mutes the global logger and the bootstrap console for the whole operation and
// never flushes them (dashboard.go:589-595), because raw log lines would corrupt the alternate
// screen. The result screen is therefore the ONLY channel open on this path, and the install
// direction had none for the #298 finding: warnIndirectProxsaveCronOnDaemonInstall wrote its
// three lines into the muted logger and cronRemovalOutcome had no field to carry them, so an
// operator installing the daemon on a host whose backup runs through their own script saw a
// green INSTALLED and got two backups a night. The revert direction already had this channel.
func TestDashboardDaemonInstallWarnsOnADuplicateSchedule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		outcome     cronRemovalOutcome
		wantLevel   orchestrator.HealthcheckSetupLevel
		wantKeyword string
		wantText    string
		alsoWants   []string
	}{
		{
			name:        "nothing else schedules ProxSave: unchanged",
			outcome:     cronRemovalOutcome{Removed: 1, Verified: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelOk,
			wantKeyword: "INSTALLED",
			wantText:    "Cron entry: removed.",
		},
		{
			// An unverified removal already SAYS a proxsave entry may still be scheduled next to
			// the daemon just installed. Saying that under a green tick is the contradiction: the
			// level has to carry the same doubt the sentence does.
			name:        "the crontab could not be checked: not a green tick",
			outcome:     cronRemovalOutcome{},
			wantLevel:   orchestrator.HealthcheckSetupLevelWarn,
			wantKeyword: "INSTALLED - NO CRON ENTRY REMOVED",
			wantText:    "Cron entry: could not be checked, one may still be scheduled alongside the daemon.",
		},
		{
			name:        "an unmanaged schedule survives: warning",
			outcome:     cronRemovalOutcome{Verified: true, UnmanagedSchedules: 1},
			wantLevel:   orchestrator.HealthcheckSetupLevelWarn,
			wantKeyword: "INSTALLED - DUPLICATE SCHEDULE",
			wantText:    "Check your crons to remove duplication.",
			// The duplicate sentence is ADDED, not swapped in. Replacing the message threw away
			// the line saying what the removal actually did, on the screen that is the only
			// channel this path has.
			alsoWants: []string{"Daemon service: active (", "Cron entry: none was present to remove."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, true, true)
			origShow := showDaemonResultScreenFn
			t.Cleanup(func() { showDaemonResultScreenFn = origShow })
			daemonApplyDaemonMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
				return tc.outcome, nil
			}
			type shown struct {
				level   orchestrator.HealthcheckSetupLevel
				keyword string
				text    string
			}
			ch := make(chan shown, 1)
			showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, level orchestrator.HealthcheckSetupLevel, kw, explanation string) {
				ch <- shown{level, kw, explanation}
			}

			driver := installDashboardSessionSeam(t)
			res := driver.spawn(&cli.Args{})
			driver.waitScreen("Dashboard")
			driver.keys("down down down down down down down down down enter") // Install daemon

			var got shown
			select {
			case got = <-ch:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("the install never reached the result screen")
			}
			driver.waitScreen("Dashboard")
			driver.keys("esc")
			select {
			case <-res:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("dashboard did not resolve")
			}

			if got.level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got.level, tc.wantLevel)
			}
			if got.keyword != tc.wantKeyword {
				t.Errorf("keyword = %q, want %q", got.keyword, tc.wantKeyword)
			}
			if !strings.Contains(got.text, tc.wantText) {
				t.Errorf("the screen must state %q, got:\n%s", tc.wantText, got.text)
			}
			for _, want := range tc.alsoWants {
				if !strings.Contains(got.text, want) {
					t.Errorf("the screen must also state %q, got:\n%s", want, got.text)
				}
			}
		})
	}
}

// The other drivable #298 call site (see TestMaybeAutoMigrateDaemonReportsWhatTheRemovalDid).
// The result screen is the only channel open on this path, so if the removal claim regressed
// here the TUI would state a fact the code had not established while the CLI stated the truth.
func TestDashboardDaemonInstallReportsWhatTheRemovalDid(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome cronRemovalOutcome
	}{
		{"nothing matched", cronRemovalOutcome{Verified: true}},
		{"crontab unreadable", cronRemovalOutcome{}},
		{"one line removed", cronRemovalOutcome{Removed: 1, Verified: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, true, true) // cron -> the menu offers "Install daemon"
			origShow := showDaemonResultScreenFn
			t.Cleanup(func() { showDaemonResultScreenFn = origShow })
			daemonApplyDaemonMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
				return tc.outcome, nil
			}
			shown := make(chan string, 1)
			showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
				shown <- kw + "\n" + explanation
			}

			driver := installDashboardSessionSeam(t)
			res := driver.spawn(&cli.Args{})
			driver.waitScreen("Dashboard")
			driver.keys("down down down down down down down down down enter") // Install daemon

			var msg string
			select {
			case msg = <-shown:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("the install never reached the result screen")
			}
			driver.waitScreen("Dashboard")
			driver.keys("esc")
			select {
			case <-res:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("dashboard did not resolve")
			}

			if want := cronRemovalScreenClause(tc.outcome); !strings.Contains(msg, want) {
				t.Errorf("the result screen must carry %q, got:\n%s", want, msg)
			}
			if tc.outcome.Removed == 0 && strings.Contains(msg, "cron entry was removed") {
				t.Errorf("nothing was removed but the screen claims a removal, got:\n%s", msg)
			}
		})
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
	daemonApplyCronMode = func(ctx context.Context, cfg *config.Config, configPath, execToken string, bl *logging.BootstrapLogger) (cronRevertReport, error) {
		reverted++
		return cronRevertReport{}, nil
	}
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	// Active state: Daemon group = "Disable daemon" (row 11, 10 downs) + "Restart" + "Daemon status".
	driver.keys("down down down down down down down down down enter") // Disable daemon
	driver.waitScreen("Daemon disabled")                              // success notice
	driver.keys("enter")                                              // dismiss
	driver.waitScreen("Dashboard")                                    // looped back
	driver.keys("esc")
	select {
	case r := <-res:
		if !r.handled {
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
	res := driver.spawn(args)

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down enter")      // Check Telegram (7th selectable)
	driver.waitScreen("Dashboard")                          // looped back after the screen
	driver.keys("down down down down down down down enter") // Check healthchecks (8th)
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down enter") // Post-install check (9th)
	driver.waitScreen("Dashboard")
	driver.keys("esc") // exit

	select {
	case r := <-res:
		if !r.handled || r.code != types.ExitSuccess.Int() {
			t.Fatalf("esc from menu must exit cleanly, got %+v", r)
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
	res := driver.spawn(args)

	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down enter") // Check Telegram (not configured)
	driver.waitScreen("Telegram")                      // the styled "Status:" result screen
	driver.waitOutput("NOT CONFIGURED")                // Status: ⚠ NOT CONFIGURED
	driver.keys("enter")                               // dismiss (Back)
	driver.waitScreen("Dashboard")                     // back at the menu
	driver.keys("esc")                                 // exit

	select {
	case r := <-res:
		if !r.handled {
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
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.cancel() // kill the UI program
	select {
	case r := <-res:
		if !r.handled || r.code != types.ExitSuccess.Int() {
			t.Fatalf("UI death must exit cleanly, got %+v", r)
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
	if isTerminalInteractive() {
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
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("down enter") // Restore (row 2, 1 down)
	var r dashboardResult
	select {
	case r = <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if r.handled || !args.Restore {
		t.Fatalf("restore dispatch broken: handled=%v args=%+v", r.handled, args)
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

// F04-04: an abandoned dashboard sub-screen must hit the idle timeout and let the
// dashboard exit, not hang forever holding the terminal (root process).
func TestDashboardSubScreenIdleTimeoutExits(t *testing.T) {
	orig := dashboardIdleTimeout
	dashboardIdleTimeout = 50 * time.Millisecond
	t.Cleanup(func() { dashboardIdleTimeout = orig })

	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)

	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down enter") // open the Install chooser sub-screen, then send nothing

	select {
	case <-res:
		// The sub-screen (then the menu) hit the idle timeout and the dashboard exited.
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("dashboard sub-screen hung: the idle timeout did not bound it")
	}
}

// The revert screen's "Future upgrades will not reinstall it" was a claim about a config write
// that is best-effort: applyCronMode only warns when it fails and tears the daemon down anyway,
// so a read-only or full filesystem produced a green screen asserting something that had not
// happened. The host is then left with no unit and a config that still records the daemon.
func TestDashboardDaemonRevertReportsAFailedConfigWrite(t *testing.T) {
	installDashboardGates(t, true, true)
	origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
	t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{CronScheduled: true, ModeRecorded: false}, nil
	}
	type shown struct {
		level   orchestrator.HealthcheckSetupLevel
		keyword string
		text    string
	}
	ch := make(chan shown, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, level orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		ch <- shown{level, kw, explanation}
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var got shown
	select {
	case got = <-ch:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	if got.level != orchestrator.HealthcheckSetupLevelWarn {
		t.Errorf("a config write that did not land must not be green, level = %v", got.level)
	}
	if !strings.Contains(got.text, "Configuration: NOT updated") {
		t.Errorf("the screen must say the configuration was not updated, got:\n%s", got.text)
	}
	if strings.Contains(got.text, "SCHEDULER_MODE=cron recorded") {
		t.Errorf("the screen must not claim the record that was not written, got:\n%s", got.text)
	}
}

// The revert deliberately creates the duplicate it reports: it writes its cron line even when
// something unmanaged already schedules ProxSave, because withholding it would leave a
// misidentified host with nothing scheduled. The CLI says so with a WARNING. On the dashboard
// that warning goes into a logger muted for the whole operation and never flushed, and
// cronRevertReport carried only the /etc findings, so a host that now runs the backup twice got
// a green REVERTED TO CRON. The install direction already had this channel.
func TestDashboardDaemonRevertWarnsOnADuplicateSchedule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		report      cronRevertReport
		wantLevel   orchestrator.HealthcheckSetupLevel
		wantKeyword string
		wantText    string
	}{
		{
			name:        "nothing else schedules ProxSave: unchanged",
			report:      cronRevertReport{CronScheduled: true, ModeRecorded: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelOk,
			wantKeyword: "REVERTED TO CRON",
			wantText:    "Daemon service: removed.",
		},
		{
			name:        "an unmanaged schedule survives: warning",
			report:      cronRevertReport{CronScheduled: true, ModeRecorded: true, UnmanagedAdvisory: []string{"1 unmanaged crontab line(s) also appear to schedule ProxSave:", "  - 30 02 * * * /usr/local/sbin/nas-guard"}},
			wantLevel:   orchestrator.HealthcheckSetupLevelWarn,
			wantKeyword: "REVERTED - DUPLICATE SCHEDULE",
			wantText:    "Check your crons to remove duplication.",
		},
		{
			// Same fact, other habitat. The CLI already warns on both through
			// logBootstrapWarning, so a screen that stays green on this one hands the same host
			// two different levels depending on which channel the operator read.
			name:        "the surviving schedule is under /etc: same level, same keyword",
			report:      cronRevertReport{CronScheduled: true, ModeRecorded: true, SystemCronAdvisory: []string{"Reverting to cron: 1 possible ProxSave cron line(s) under /etc:", "  - 0 5 * * * root /usr/local/bin/proxsave --backup", "ProxSave owns the root crontab only and never edits files it did not place, 1 entry(ies) in /etc unchanged"}},
			wantLevel:   orchestrator.HealthcheckSetupLevelWarn,
			wantKeyword: "REVERTED - DUPLICATE SCHEDULE",
			wantText:    "1 entry(ies) in /etc unchanged",
		},
		{
			// A cron entry that could not be written takes the level, because it is the worst
			// thing that happened. It does NOT take the old headline: with an unmanaged line
			// listed below, "nothing is scheduling the backup" would deny what the screen goes
			// on to show, so the headline states the certain fact instead.
			name:        "unwritten entry takes the level, not the denial",
			report:      cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: true, UnmanagedAdvisory: []string{"1 unmanaged crontab line(s) also appear to schedule ProxSave:", "  - 30 02 * * * /usr/local/sbin/nas-guard"}},
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "CRON ENTRY NOT WRITTEN",
			wantText:    "Cron entry: NOT written.",
		},
		{
			// With nothing listed underneath, the denial is true and stays.
			name:        "nothing left at all: the denial stands",
			report:      cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "NO SCHEDULE",
			wantText:    "nothing is scheduling the backup",
		},
		{
			// ...and it stands only there. An unreadable crontab reaches this branch with the
			// same false, but nobody measured it: the write may well have landed. The level stays
			// Error because a host that cannot be checked may equally be one with nothing
			// scheduled, and that is the expensive reading.
			name:        "the crontab could not be read: no denial, only the fact",
			report:      cronRevertReport{CronScheduled: false, CronVerified: false, ModeRecorded: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "CRON ENTRY NOT CHECKED",
			wantText:    "Cron entry: could not be checked, the crontab was unreadable.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, true, true)
			origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
			t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
			daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
				return &config.Config{SchedulerMode: "daemon"}, nil
			}
			daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
				return tc.report, nil
			}
			type shown struct {
				level   orchestrator.HealthcheckSetupLevel
				keyword string
				text    string
			}
			ch := make(chan shown, 1)
			showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, level orchestrator.HealthcheckSetupLevel, kw, explanation string) {
				ch <- shown{level, kw, explanation}
			}

			driver := installDashboardSessionSeam(t)
			res := driver.spawn(&cli.Args{})
			driver.waitScreen("Dashboard")
			driver.keys("down down down down down down down down down enter") // Disable daemon

			var got shown
			select {
			case got = <-ch:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("the revert never reached the result screen")
			}
			driver.waitScreen("Dashboard")
			driver.keys("esc")
			select {
			case <-res:
			case <-time.After(uitest.Deadline(60 * time.Second)):
				t.Fatal("dashboard did not resolve")
			}

			if got.level != tc.wantLevel {
				t.Errorf("level = %v, want %v", got.level, tc.wantLevel)
			}
			if got.keyword != tc.wantKeyword {
				t.Errorf("keyword = %q, want %q", got.keyword, tc.wantKeyword)
			}
			if !strings.Contains(got.text, tc.wantText) {
				t.Errorf("the screen must state %q, got:\n%s", tc.wantText, got.text)
			}
		})
	}
}

// The daemon is gone and nothing replaced it: that is the one revert outcome that is an ERROR,
// not a warning, and it must outrank every other clause on the screen.
func TestDashboardDaemonRevertReportsAnUnscheduledHost(t *testing.T) {
	installDashboardGates(t, true, true)
	origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
	t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: true}, nil
	}
	type shown struct {
		level   orchestrator.HealthcheckSetupLevel
		keyword string
		text    string
	}
	ch := make(chan shown, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, level orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		ch <- shown{level, kw, explanation}
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var got shown
	select {
	case got = <-ch:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	if got.level != orchestrator.HealthcheckSetupLevelError {
		t.Errorf("an unscheduled host is an error, level = %v", got.level)
	}
	if !strings.Contains(got.text, "nothing is scheduling the backup") {
		t.Errorf("the screen must say nothing is scheduling the backup, got:\n%s", got.text)
	}
	if strings.Contains(got.text, "Cron entry: in the crontab") {
		t.Errorf("the success wording must not survive next to it, got:\n%s", got.text)
	}
}

// The screen is the ONLY channel on this path: the logger is muted for the whole operation and
// never flushed, so a fact the screen drops is a fact the operator never gets. The unscheduled
// branch used to REPLACE the message rather than compose it, which threw away the config-write
// clause with it. On the CLI the same host reads both, because applyCronMode's own warning is
// still on screen there.
func TestDashboardDaemonRevertKeepsEveryFactOnTheUnscheduledScreen(t *testing.T) {
	installDashboardGates(t, true, true)
	origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
	t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: false}, nil
	}
	shown := make(chan string, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		shown <- kw + "\n" + explanation
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var msg string
	select {
	case msg = <-shown:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	if !strings.Contains(msg, "NO SCHEDULE") {
		t.Errorf("nothing scheduling the backup is still the headline, got:\n%s", msg)
	}
	if !strings.Contains(msg, "nothing is scheduling the backup") {
		t.Errorf("the screen must state the unscheduled host, got:\n%s", msg)
	}
	// The second fact, which has no other channel here.
	if !strings.Contains(msg, "Configuration: NOT updated") {
		t.Errorf("the screen must also state that the configuration was not written, got:\n%s", msg)
	}
}

// CronScheduled is an assertion about the ROOT crontab alone: canonicalCronLinePresent reads
// crontabReadLinesFn and nothing else. So "nothing is scheduling the backup" is false on a host
// whose proxsave entry lives under /etc, and that is exactly the host where ProxSave could not
// write its own line and cannot remove the other one either - the screen said nothing was
// scheduled and then listed, three lines below, the entry that schedules it.
func TestDashboardDaemonRevertDoesNotDenyAScheduleItJustListed(t *testing.T) {
	advisory := []string{
		"Reverting to cron: 1 possible ProxSave cron line(s) under /etc:",
		"  - 0 5 * * * root /usr/local/bin/proxsave --backup  [/etc/cron.d/proxsave]",
		"ProxSave owns the root crontab only and never edits files it did not place, 1 entry(ies) in /etc unchanged",
	}
	installDashboardGates(t, true, true)
	origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
	t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: true, SystemCronAdvisory: advisory}, nil
	}
	shown := make(chan string, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		shown <- kw + "\n" + explanation
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var msg string
	select {
	case msg = <-shown:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	if strings.Contains(msg, "nothing is scheduling the backup") {
		t.Errorf("the screen may not deny a schedule it lists below, got:\n%s", msg)
	}
	// What IS certain, and what the operator has to act on.
	if !strings.Contains(msg, "Cron entry: NOT written.") {
		t.Errorf("the screen must still state that the cron entry was not written, got:\n%s", msg)
	}
	if !strings.Contains(msg, advisory[1]) {
		t.Errorf("the /etc finding must still be listed, got:\n%s", msg)
	}
}

// The screen is the only channel on this path, and it carried the /etc findings as rendered lines
// while carrying the root-crontab ones as a bare COUNT. So the unscheduled branch, which composes
// its own text, had nothing to list for them and the count went with it: the sentence pointed at
// an entry "below" that was never printed. The two habitats are now carried the same way, which
// is also how the CLI has always printed them.
func TestDashboardDaemonRevertListsBothHabitats(t *testing.T) {
	unmanaged := []string{
		"1 unmanaged crontab line(s) also appear to schedule ProxSave:",
		"  - 30 02 * * * /usr/local/sbin/nas-guard",
	}
	etc := []string{
		"Reverting to cron: 1 possible ProxSave cron line(s) under /etc:",
		"  - 0 5 * * * root /usr/local/bin/proxsave --backup  [/etc/cron.d/proxsave]",
	}
	installDashboardGates(t, true, true)
	origCfg, origShow := daemonStatusLoadConfig, showDaemonResultScreenFn
	t.Cleanup(func() { daemonStatusLoadConfig, showDaemonResultScreenFn = origCfg, origShow })
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{
			CronScheduled:      false,
			CronVerified:       true,
			ModeRecorded:       true,
			UnmanagedAdvisory:  unmanaged,
			SystemCronAdvisory: etc,
		}, nil
	}
	type shown struct {
		keyword string
		text    string
	}
	ch := make(chan shown, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		ch <- shown{kw, explanation}
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var got shown
	select {
	case got = <-ch:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	// The headline states the fact that is certain. NO SCHEDULE would deny two schedules the
	// screen lists underneath it.
	if got.keyword != "CRON ENTRY NOT WRITTEN" {
		t.Errorf("keyword = %q, want CRON ENTRY NOT WRITTEN", got.keyword)
	}
	for _, want := range append(append([]string{}, unmanaged...), etc...) {
		if !strings.Contains(got.text, want) {
			t.Errorf("the screen must list %q, got:\n%s", want, got.text)
		}
	}
	// And with nothing left to schedule the host, the denial is true and stays.
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{CronScheduled: false, CronVerified: true, ModeRecorded: true}, nil
	}
	driver2 := installDashboardSessionSeam(t)
	res2 := driver2.spawn(&cli.Args{})
	driver2.waitScreen("Dashboard")
	driver2.keys("down down down down down down down down down enter")
	select {
	case got = <-ch:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the second revert never reached the result screen")
	}
	driver2.waitScreen("Dashboard")
	driver2.keys("esc")
	<-res2
	if got.keyword != "NO SCHEDULE" {
		t.Errorf("with nothing listed the denial is true: keyword = %q, want NO SCHEDULE", got.keyword)
	}
}

// The failure screen the revert renders is the WRONG one for an install. applyDaemonMode's only
// error return is installDaemonService's, which fires before any cron or config work, so the
// report it hands back is a zero value: rendering it would tell the operator, on a host where
// nothing was touched, that the cron entry could not be written and the configuration still
// records the daemon engine. Both sentences would be inventions.
//
// The guard that keeps them apart is one "!install" nobody was checking.
func TestDashboardDaemonInstallFailureKeepsTheGenericScreen(t *testing.T) {
	installDashboardGates(t, true, true)
	origShow := showDaemonResultScreenFn
	t.Cleanup(func() { showDaemonResultScreenFn = origShow })
	daemonApplyDaemonMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRemovalOutcome, error) {
		return cronRemovalOutcome{}, errors.New("systemctl enable --now failed: exit status 1")
	}
	type shown struct {
		level   orchestrator.HealthcheckSetupLevel
		keyword string
		text    string
	}
	ch := make(chan shown, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, level orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		ch <- shown{level, kw, explanation}
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Install daemon

	var got shown
	select {
	case got = <-ch:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the failed install never reached the result screen")
	}
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	if got.keyword != "FAILED" {
		t.Errorf("a failed install keeps the generic failure screen, keyword = %q", got.keyword)
	}
	if got.level != orchestrator.HealthcheckSetupLevelError {
		t.Errorf("level = %v, want Error", got.level)
	}
	if !strings.Contains(got.text, "systemctl enable --now failed") {
		t.Errorf("the screen must carry the error it was given, got:\n%s", got.text)
	}
	// The revert screen's sentences describe work applyDaemonMode never reached.
	for _, invented := range []string{"cron entry", "configuration", "daemon service could NOT be removed"} {
		if strings.Contains(got.text, invented) {
			t.Errorf("a failed install must not borrow the revert screen's %q, got:\n%s", invented, got.text)
		}
	}
}

// The menu row is decided by the scheduler mode alone. It used to consult a second key,
// DAEMON_OPT_OUT, purely to tell "reverted from the daemon" apart from "never had it" and
// label the same command "Re-enable" instead of "Install". Both rows ran ActionDaemonSetup and
// the distinction was never one the operator could act on differently, so with that key
// retired the two states collapse into one rather than needing a replacement signal.
func TestDashboardDaemonStateIsSchedulerModeOnly(t *testing.T) {
	orig := daemonStatusLoadConfig
	t.Cleanup(func() { daemonStatusLoadConfig = orig })

	for _, tc := range []struct {
		name string
		cfg  *config.Config
		err  error
		want menu.DaemonState
	}{
		{"daemon", &config.Config{SchedulerMode: "daemon"}, nil, menu.DaemonStateActive},
		{"cron", &config.Config{SchedulerMode: "cron"}, nil, menu.DaemonStateOnCron},
		{"unrecognised mode falls back to cron, like the parser", &config.Config{SchedulerMode: ""}, nil, menu.DaemonStateOnCron},
		{"config unreadable: only Status", nil, errors.New("nope"), menu.DaemonStateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			daemonStatusLoadConfig = func(string, string) (*config.Config, error) { return tc.cfg, tc.err }
			if got := dashboardDaemonState(&cli.Args{}); got != tc.want {
				t.Errorf("dashboardDaemonState() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The /etc advisory used to be recorded and dropped on the TUI: runDashboardDaemonAdmin
// mutes the global logger and sets the bootstrap console quiet for the whole operation and
// never flushes it, so a revert on a host that may now be scheduled twice showed a green
// "REVERTED TO CRON" and nothing else. The CLI said it, the TUI did not. applyCronMode now
// returns the lines and the result screen carries them.
//
// The assertion goes through showDaemonResultScreenFn rather than the rendered screen: what
// matters is that the advisory reaches the only channel open on this path, and matching the
// painted text would instead be matching wherever BuildStatusPrompt happened to wrap it.
func TestDashboardDaemonRevertShowsTheSystemCronAdvisory(t *testing.T) {
	installDashboardGates(t, true, true)
	origCfg := daemonStatusLoadConfig
	origShow := showDaemonResultScreenFn
	t.Cleanup(func() {
		daemonStatusLoadConfig = origCfg
		showDaemonResultScreenFn = origShow
	})
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	// The stub hands back the advisory the PRODUCT builds, not a hand-written approximation of
	// it. A fabricated sentence would pin the plumbing and nothing else: it would keep passing
	// while the real wording drifted, and it would read to the next person as if the text on
	// the screen were pinned here when no line of it ever came from the product. The wording
	// itself is pinned where it is produced (cron_indirect_refs_test.go, daemon_cron_reporting_test.go);
	// what this test owns is that every line of it reaches the only channel open on this path.
	advisory := systemCronScheduleAdvisory([]indirectCronRef{{
		Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
		Command: "/usr/local/sbin/proxsave-nas-guard",
		Source:  "/etc/cron.d/proxsave-guard",
		Reason:  `command "proxsave-nas-guard" is named after proxsave and could not be read`,
	}})
	if len(advisory) == 0 {
		t.Fatal("the advisory builder returned nothing: this test would then assert on an empty set")
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{SystemCronAdvisory: advisory, CronScheduled: true, ModeRecorded: true}, nil
	}
	shown := make(chan string, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		shown <- kw + "\n" + explanation
	}

	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var msg string
	select {
	case msg = <-shown:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	// Wait for the menu to be repainted before sending esc: the stub returns immediately,
	// so without this the key can land while the loop is still between screens.
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}

	// The keyword is the duplicate one, not the plain success: an /etc finding raises the level
	// exactly as a root-crontab wrapper does, which is what the CLI has always done for both.
	// See TestDashboardDaemonRevertWarnsOnADuplicateSchedule for that rule; what this test owns
	// is that every line of the advisory reaches the screen.
	if !strings.Contains(msg, "REVERTED - DUPLICATE SCHEDULE") {
		t.Fatalf("an /etc finding must carry the duplicate keyword, got:\n%s", msg)
	}
	if !strings.Contains(msg, "Daemon service: removed.") {
		t.Errorf("the existing message must not be replaced, got:\n%s", msg)
	}
	// EVERY line, not just the first: the header carries the count, the item carries the cron
	// line and the file it lives in, and the closing line says what ProxSave did not touch.
	// Dropping any one of them leaves the operator a different fact.
	for _, want := range advisory {
		if !strings.Contains(msg, want) {
			t.Errorf("the revert result screen must carry %q, got:\n%s", want, msg)
		}
	}
}

// A failed teardown is the ONE revert failure that arrives with a populated report, and the
// screen used to throw the whole thing away: it showed opErr.Error() on one line and returned,
// so the duplicate-schedule finding, the /etc advisory and the config-write fact were all lost
// on the one path where the logger is muted for the whole operation and never flushed.
//
// What survives a failed removeDaemonServiceFn, and what does not, is decided by applyCronMode's
// F09-06 ordering rather than by guesswork. CronScheduled was read back from the crontab BEFORE
// the teardown; UnmanagedAdvisory is the root-crontab wrapper lines read before it, which ProxSave
// never edits; ModeRecorded is setBackupEnvKeys' result, before it; SystemCronAdvisory is the /etc
// scan, deliberately placed after it. All four are still true. Three things are NOT: that the
// daemon service was removed, cronModeRecordClause's tail "...while no daemon is installed", and
// "nothing is scheduling the backup" - because the daemon that could not be removed may still be
// scheduling it. And one fact is new and in no field: with a cron line in place and a daemon that
// may still be alive, the host now has two schedulers at the same minute.
func TestCronRevertScreenOnAFailedTeardown(t *testing.T) {
	teardown := errors.New("remove /etc/systemd/system/proxsave-daemon.service: permission denied")
	unmanaged := []string{
		"Reverting to cron: 1 unmanaged crontab line(s) also appear to schedule ProxSave:",
		"  - 30 02 * * * /usr/local/sbin/nas-guard",
	}
	etc := systemCronScheduleAdvisory([]indirectCronRef{{
		Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
		Command: "/usr/local/sbin/proxsave-nas-guard",
		Source:  "/etc/cron.d/proxsave-guard",
		Reason:  `command "proxsave-nas-guard" is named after proxsave and could not be read`,
	}})
	if len(etc) == 0 {
		t.Fatal("the advisory builder returned nothing: this test would then assert on an empty set")
	}

	for _, tc := range []struct {
		name        string
		revert      cronRevertReport
		err         error
		wantLevel   orchestrator.HealthcheckSetupLevel
		wantKeyword string
		wantSays    []string
		wantSilent  []string
	}{
		{
			name:        "the cron line landed and the mode was recorded",
			revert:      cronRevertReport{CronScheduled: true, ModeRecorded: true},
			err:         teardown,
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "DAEMON NOT REMOVED - DUPLICATE SCHEDULE",
			wantSays: []string{
				"Daemon service: NOT removed, may still be running.",
				"permission denied",
				"Cron entry: in the crontab, so the backup may run twice.",
				"Configuration: SCHEDULER_MODE=cron recorded.",
			},
			wantSilent: []string{"Daemon service: removed.", "nothing is scheduling the backup"},
		},
		{
			name:        "the config write failed too",
			revert:      cronRevertReport{CronScheduled: true},
			err:         teardown,
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "DAEMON NOT REMOVED - DUPLICATE SCHEDULE",
			wantSays:    []string{"Configuration: NOT updated", "still records the daemon engine"},
			// cronModeRecordClause's failure arm ends "...while no daemon is installed", which is
			// exactly what this failure disproves.
			wantSilent: []string{"while no daemon is installed"},
		},
		{
			name:        "the cron entry was not written either",
			revert:      cronRevertReport{ModeRecorded: true},
			err:         teardown,
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "DAEMON NOT REMOVED",
			wantSays:    []string{"Daemon service: NOT removed, may still be running.", "Cron entry: NOT written."},
			// The old wording closed with "the daemon that is still there may be the only thing
			// still scheduling the backup", which denies whatever the advisories below list.
			wantSilent: []string{"nothing is scheduling the backup", "may run twice", "only thing still scheduling"},
		},
		{
			name:        "both habitats still have to be listed",
			revert:      cronRevertReport{CronScheduled: true, ModeRecorded: true, UnmanagedAdvisory: unmanaged, SystemCronAdvisory: etc},
			err:         teardown,
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "DAEMON NOT REMOVED - DUPLICATE SCHEDULE",
			wantSays:    append(append([]string{}, unmanaged...), etc...),
		},

		// CONTROLS: the success path may not move.
		{
			name:        "success, scheduled and recorded",
			revert:      cronRevertReport{CronScheduled: true, ModeRecorded: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelOk,
			wantKeyword: "REVERTED TO CRON",
			wantSays:    []string{"Daemon service: removed.", "Cron entry: in the crontab."},
			wantSilent:  []string{"NOT removed"},
		},
		{
			name:        "success, nothing scheduling",
			revert:      cronRevertReport{ModeRecorded: true, CronVerified: true},
			wantLevel:   orchestrator.HealthcheckSetupLevelError,
			wantKeyword: "NO SCHEDULE",
			wantSays:    []string{"nothing is scheduling the backup"},
			wantSilent:  []string{"NOT removed"},
		},
		{
			name:        "success with an /etc finding",
			revert:      cronRevertReport{CronScheduled: true, ModeRecorded: true, SystemCronAdvisory: etc},
			wantLevel:   orchestrator.HealthcheckSetupLevelWarn,
			wantKeyword: "REVERTED - DUPLICATE SCHEDULE",
			wantSays:    etc,
			wantSilent:  []string{"NOT removed"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			level, keyword, msg := buildCronRevertScreen(tc.revert, tc.err)
			if level != tc.wantLevel {
				t.Errorf("level = %v, want %v", level, tc.wantLevel)
			}
			if keyword != tc.wantKeyword {
				t.Errorf("keyword = %q, want %q", keyword, tc.wantKeyword)
			}
			for _, want := range tc.wantSays {
				if !strings.Contains(msg, want) {
					t.Errorf("the screen must state %q, got:\n%s", want, msg)
				}
			}
			for _, absent := range tc.wantSilent {
				if strings.Contains(msg, absent) {
					t.Errorf("the screen must NOT claim %q, got:\n%s", absent, msg)
				}
			}
		})
	}
}

// The wiring. The renderer above can be perfect and the operator still see one line, because the
// generic failure arm showed opErr.Error() and returned before the report was ever read. This
// test drives the real dashboard action and asserts on what reaches the only channel open here.
func TestDashboardDaemonRevertKeepsTheReportWhenTheTeardownFails(t *testing.T) {
	installDashboardGates(t, true, true)
	origCfg := daemonStatusLoadConfig
	origShow := showDaemonResultScreenFn
	t.Cleanup(func() {
		daemonStatusLoadConfig = origCfg
		showDaemonResultScreenFn = origShow
	})
	daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
		return &config.Config{SchedulerMode: "daemon"}, nil
	}
	unmanaged := []string{
		"Reverting to cron: 1 unmanaged crontab line(s) also appear to schedule ProxSave:",
		"  - 30 02 * * * /usr/local/sbin/nas-guard",
	}
	etc := systemCronScheduleAdvisory([]indirectCronRef{{
		Line:    "17 02 * * * root /usr/local/sbin/proxsave-nas-guard",
		Command: "/usr/local/sbin/proxsave-nas-guard",
		Source:  "/etc/cron.d/proxsave-guard",
		Reason:  `command "proxsave-nas-guard" is named after proxsave and could not be read`,
	}})
	if len(etc) == 0 {
		t.Fatal("the advisory builder returned nothing: this test would then assert on an empty set")
	}
	daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
		return cronRevertReport{
			UnmanagedAdvisory:  unmanaged,
			SystemCronAdvisory: etc,
			CronScheduled:      true,
		}, errors.New("remove /etc/systemd/system/proxsave-daemon.service: permission denied")
	}
	msg := runDashboardRevertAndCaptureScreen(t)

	for _, want := range append(append(append([]string{
		"Daemon service: NOT removed, may still be running.",
		"permission denied",
		"Cron entry: in the crontab, so the backup may run twice.",
		"Configuration: NOT updated",
	}, unmanaged...), etc...), "DAEMON NOT REMOVED - DUPLICATE SCHEDULE") {
		if !strings.Contains(msg, want) {
			t.Errorf("a failed teardown must still state %q, got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "removed the daemon service") {
		t.Errorf("the screen must not claim a removal that failed, got:\n%s", msg)
	}
}

// The two sentinels abort BEFORE anything is written and hand back a zero report, so none of its
// fields is a measurement - they are defaults. Routing them through the revert renderer would
// state "the cron entry could NOT be written either" and "it still records the daemon engine"
// about a host where neither was attempted, which is why the guard keys on the sentinel and not
// on the report looking empty: a teardown failure on a host with no cron line, no config write
// and no findings produces a report byte-identical to this one.
func TestDashboardDaemonRevertSentinelsKeepTheirOwnScreens(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"config unreadable", errDaemonTeardownConfigUnreadable},
		{"backup running", errDaemonTeardownBackupRunning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			installDashboardGates(t, true, true)
			origCfg := daemonStatusLoadConfig
			origShow := showDaemonResultScreenFn
			t.Cleanup(func() {
				daemonStatusLoadConfig = origCfg
				showDaemonResultScreenFn = origShow
			})
			daemonStatusLoadConfig = func(string, string) (*config.Config, error) {
				return &config.Config{SchedulerMode: "daemon"}, nil
			}
			daemonApplyCronMode = func(context.Context, *config.Config, string, string, *logging.BootstrapLogger) (cronRevertReport, error) {
				return cronRevertReport{}, tc.err
			}
			msg := runDashboardRevertAndCaptureScreen(t)

			// Case-folded: the deferred screen writes "was NOT removed" and the sentinel string
			// writes "was not removed". Both say the same fact and neither is this test's to pin.
			if !strings.Contains(strings.ToLower(msg), "not removed") {
				t.Errorf("the sentinel must still say the daemon was not removed, got:\n%s", msg)
			}
			for _, absent := range []string{"Cron entry: NOT written.", "still records the daemon engine", "may run twice", "may still be running"} {
				if strings.Contains(msg, absent) {
					t.Errorf("a zero report must not be reported as a measurement (%q), got:\n%s", absent, msg)
				}
			}
		})
	}
}

// runDashboardRevertAndCaptureScreen drives the dashboard's "Disable daemon" action with whatever
// daemonApplyCronMode the caller installed, and returns the keyword and text the result screen
// received. It follows TestDashboardDaemonRevertShowsTheSystemCronAdvisory: the caller installs
// the gates, the config seam and the op seam; this only owns the key sequence and the deadlines.
func runDashboardRevertAndCaptureScreen(t *testing.T) string {
	t.Helper()
	shown := make(chan string, 1)
	showDaemonResultScreenFn = func(_ context.Context, _ *shell.Session, _ string, _ orchestrator.HealthcheckSetupLevel, kw, explanation string) {
		shown <- kw + "\n" + explanation
	}
	driver := installDashboardSessionSeam(t)
	res := driver.spawn(&cli.Args{})
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down enter") // Disable daemon

	var msg string
	select {
	case msg = <-shown:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("the revert never reached the result screen")
	}
	// Wait for the menu to be repainted before sending esc: the stub returns immediately, so
	// without this the key can land while the loop is still between screens.
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	return msg
}
