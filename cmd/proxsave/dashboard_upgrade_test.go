// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

func TestUpgradeSafeToken(t *testing.T) {
	cases := map[string]string{
		"1.2.3":                 "1.2.3",
		"v2.0.0-beta+meta_1":    "v2.0.0-beta+meta_1",
		"  1.0.0  ":             "1.0.0",
		"1.0\x1b.0":             "1.0.0",      // bare ESC stripped
		"a\nb\tc\x00d":          "abcd",       // control chars stripped
		"\x1b]0;pwn\x07 v9.9.9": "0pwnv9.9.9", // OSC control bytes stripped, letters survive (harmless as plain text)
	}
	for in, want := range cases {
		if got := upgradeSafeToken(in); got != want {
			t.Errorf("upgradeSafeToken(%q)=%q want %q", in, got, want)
		}
	}
	if got := upgradeSafeToken(strings.Repeat("a", 100)); len([]rune(got)) != 40 {
		t.Fatalf("length cap not applied: len=%d", len([]rune(got)))
	}
}

func TestExtractReleaseNotes(t *testing.T) {
	body := "Some header text\n" +
		coderabbitNotesStart + "\n## Release Notes\n\n* **New Features**\n  * Thing\n" + coderabbitNotesEnd +
		"\nfooter with a checklist"
	got := extractReleaseNotes(body)
	want := "## Release Notes\n\n* **New Features**\n  * Thing"
	if got != want {
		t.Fatalf("extractReleaseNotes = %q, want %q", got, want)
	}
	if extractReleaseNotes("no markers here at all") != "" {
		t.Fatal("absent block must yield empty")
	}
	// Start marker but no end marker -> everything after start (trimmed).
	if got := extractReleaseNotes(coderabbitNotesStart + "\ntail"); got != "tail" {
		t.Fatalf("unterminated block = %q, want %q", got, "tail")
	}
}

func TestRenderReleaseNotes(t *testing.T) {
	// Control/ANSI bytes in the remote notes MUST be stripped; bold markers dropped;
	// headers emphasized (the '##' prefix removed from the visible text).
	notes := "## Release Notes\n* **New Features**\n  * shiny\x1b[31m red\x00 thing\ttabbed"
	out := ansi.Strip(renderReleaseNotes(notes))
	for _, bad := range []string{"\x1b", "\x00", "**", "## "} {
		if strings.Contains(out, bad) {
			t.Fatalf("renderReleaseNotes must strip %q, got %q", bad, out)
		}
	}
	if !strings.Contains(out, "Release Notes") || !strings.Contains(out, "shiny") || !strings.Contains(out, "red") {
		t.Fatalf("visible notes text must survive, got %q", out)
	}
	// Line-COUNT cap: a very long block is truncated with an ellipsis.
	long := strings.Repeat("* line\n", 60)
	capped := ansi.Strip(renderReleaseNotes(long))
	if strings.Count(capped, "line") > 20 || !strings.Contains(capped, "...") {
		t.Fatalf("long notes must be capped with an ellipsis, got %d lines", strings.Count(capped, "line"))
	}
	// Line-WIDTH cap: a single newline-free line is truncated so it can't soft-wrap the menu.
	wide := ansi.Strip(renderReleaseNotes(strings.Repeat("x", 500)))
	if len([]rune(strings.TrimSpace(wide))) > 110 || !strings.Contains(wide, "…") {
		t.Fatalf("a very long single line must be width-capped, got %d runes", len([]rune(strings.TrimSpace(wide))))
	}
}

// TestDashboardUpgradeScreen drives a successful in-session upgrade. After the daemon
// result is dismissed, control must propagate a reload disposition instead of returning to
// the old binary's upgrade screen.
func TestDashboardUpgradeScreen(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	origRun := dashboardUpgradeRun
	t.Cleanup(func() {
		dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk
		dashboardUpgradeRun = origRun
	})

	gotVersion := ""
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(ctx context.Context, logger *logging.Logger, current string) *UpdateInfo {
		gotVersion = current
		// A hostile GitHub version with a non-allowlist byte ('!'): the screen MUST scrub
		// it (a real ESC would be invisible after ansi.Strip, so '!' is the visible probe).
		return &UpdateInfo{
			NewVersion: true, Current: current, Latest: "2.0.0!", Tag: "v2.0.0",
			Notes: "## Release Notes\n\n* **New Features**\n  * Shiny new widget",
		}
	}
	var gotArgs *cli.Args
	var gotOpts upgradeRunOptions
	runCalls := 0
	dashboardUpgradeRun = func(ctx context.Context, args *cli.Args, bl *logging.BootstrapLogger, opts upgradeRunOptions) int {
		runCalls++
		gotArgs = args
		gotOpts = opts
		return 0
	}

	// Build an observed session eagerly (the shared seam creates it lazily via a flow).
	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })

	done := make(chan dashboardActionDisposition, 1)
	go func() {
		done <- runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env")
	}()

	// waitFor polls the accumulated output until it contains substr (the screen-title
	// push can precede the rendered bytes, so a bare read races the flush).
	waitFor := func(substr string) string {
		deadline := time.After(uitest.Deadline(15 * time.Second))
		for {
			out := ansi.Strip(driver.buf.String()[driver.matchStart:]) // from the current screen, not stale earlier-screen text
			if strings.Contains(out, substr) {
				return out
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %q in output, tail:\n%s", substr, tailStr(out))
			case <-time.After(20 * time.Millisecond):
			}
		}
	}

	driver.waitScreen("Upgrade")
	// Auto-check on entry (like Daemon status): the screen runs the release check immediately
	// and shows the available version -- no "NOT CHECKED" pre-state, no manual Check press.
	out := waitFor("2.0.0")
	if gotVersion != "1.0.0" {
		t.Fatalf("check must run against the current version, got %q", gotVersion)
	}
	if strings.Contains(out, "2.0.0!") {
		t.Fatalf("the non-allowlist byte from the GitHub version must be scrubbed, out tail:\n%s", tailStr(out))
	}
	_ = waitFor("https://github.com/tis24dev/proxsave/releases/tag/v2.0.0") // release link under the version
	_ = waitFor("Shiny new widget")                                         // the release-notes summary below
	if strings.Contains(ansi.Strip(driver.buf.String()), "**New Features**") {
		t.Fatalf("markdown bold markers must be stripped from the notes")
	}

	// The button is "Run upgrade" (an update was found): press it. The upgrade now streams
	// into a contained viewport (like backup/install), so wait for the run to finish (the
	// Continue hint) and press Enter to leave the stream panel before the daemon-restart step.
	driver.keys("enter")
	driver.waitScreen("Running upgrade")
	driver.waitOutput("enter continue")
	driver.keys("enter")
	driver.waitScreen("Upgrade complete")
	_ = waitFor("NEW BINARY ON DISK") // inactive-daemon success uses the dashboard ALL-CAPS status convention
	if gotArgs == nil || !gotArgs.Upgrade || !gotArgs.UpgradeAutoYes || gotArgs.ConfigPath != "/tmp/backup.env" {
		t.Fatalf("run upgrade must pass Upgrade+AutoYes+ConfigPath, got %+v", gotArgs)
	}
	if !gotOpts.deferWhatsnew {
		t.Fatal("dashboard upgrade must defer Screen 0 to the replacement dashboard")
	}

	driver.keys("enter") // dismiss the notice
	if runCalls != 1 {
		t.Fatalf("run upgrade calls = %d, want 1", runCalls)
	}
	select {
	case got := <-done:
		if got != dashboardActionReload {
			t.Fatalf("successful dashboard upgrade disposition = %v, want reload", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("successful upgrade did not request dashboard reload")
	}
}

// TestDashboardUpgradeFailureStaysInScreen catches the opposite branch: a failed upgrade
// remains retryable in the current dashboard and returns handled only when the user backs out.
func TestDashboardUpgradeFailureStaysInScreen(t *testing.T) {
	origVer, origChk, origRun := dashboardUpgradeVersion, dashboardUpgradeCheck, dashboardUpgradeRun
	t.Cleanup(func() {
		dashboardUpgradeVersion, dashboardUpgradeCheck, dashboardUpgradeRun = origVer, origChk, origRun
	})
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(context.Context, *logging.Logger, string) *UpdateInfo {
		return &UpdateInfo{NewVersion: true, Current: "1.0.0", Latest: "2.0.0", Tag: "v2.0.0"}
	}
	dashboardUpgradeRun = func(context.Context, *cli.Args, *logging.BootstrapLogger, upgradeRunOptions) int { return 1 }

	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })

	done := make(chan dashboardActionDisposition, 1)
	go func() { done <- runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env") }()

	driver.waitScreen("Upgrade")
	driver.keys("enter")
	driver.waitScreen("Running upgrade")
	driver.waitOutput("enter continue")
	driver.keys("enter")
	driver.waitScreen("Upgrade failed")
	driver.waitOutput("FAILED")
	driver.keys("enter")
	driver.waitScreen("Upgrade")
	driver.keys("down enter")

	select {
	case got := <-done:
		if got != dashboardActionHandled {
			t.Fatalf("failed dashboard upgrade disposition = %v, want handled", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("failed upgrade screen did not return after Back")
	}
}

// TestDashboardUpgradeExternalCheckFailureIsWarning preserves the deliberate
// retryable-external-resource policy: GitHub being unavailable is yellow, not
// a red local-operation failure, and the user can return without an action.
func TestDashboardUpgradeExternalCheckFailureIsWarning(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	t.Cleanup(func() { dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk })
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(context.Context, *logging.Logger, string) *UpdateInfo { return nil }

	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })

	done := make(chan dashboardActionDisposition, 1)
	go func() {
		done <- runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env")
	}()

	driver.waitScreen("Upgrade")
	deadline := time.After(uitest.Deadline(15 * time.Second))
	for {
		out := ansi.Strip(driver.buf.String())
		if strings.Contains(out, "CHECK FAILED") {
			if !strings.Contains(out, "⚠ CHECK FAILED") {
				t.Fatalf("external check failure must retain the warning symbol, got %q", tailStr(out))
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for CHECK FAILED, tail:\n%s", tailStr(out))
		case <-time.After(20 * time.Millisecond):
		}
	}

	driver.keys("down enter") // Back, not Re-check
	select {
	case got := <-done:
		if got != dashboardActionHandled {
			t.Fatalf("external-check Back disposition = %v, want handled", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("upgrade screen did not return after external check failure")
	}
}

// TestDashboardUpgradeMenu drives the Upgrade CHOOSER: it offers Check upgrade / Check
// config / Back, and "Check upgrade" opens the binary upgrade screen (which no longer
// carries the config button). The chooser's Back is exercised by the Update config driver
// tests (escOutOfUpgrade), and the binary screen's item layout by TestDashboardUpgradeScreen.
func TestDashboardUpgradeMenu(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	t.Cleanup(func() { dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk })
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	// The binary screen auto-checks on entry now; stub the check so it is deterministic (no network).
	dashboardUpgradeCheck = func(context.Context, *logging.Logger, string) *UpdateInfo {
		return &UpdateInfo{NewVersion: false, Latest: "1.0.0", Current: "1.0.0"}
	}

	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })

	done := make(chan dashboardActionDisposition, 1)
	go func() {
		done <- runDashboardUpgradeMenu(ctx, driver.session, "/tmp/backup.env")
	}()

	waitFor := func(substr string) {
		deadline := time.After(uitest.Deadline(15 * time.Second))
		for {
			if strings.Contains(ansi.Strip(driver.buf.String()[driver.matchStart:]), substr) {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %q", substr)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}

	driver.waitScreen("Upgrade") // the chooser
	waitFor("Check config")      // the chooser carries the config button (the binary screen does not)
	driver.keys("enter")         // Check upgrade (1st item) -> binary upgrade screen
	waitFor("NO UPGRADE")        // ...which opened AND auto-checked (deterministic stub): the two are split
	cancel()
	select {
	case got := <-done:
		if got != dashboardActionHandled {
			t.Fatalf("upgrade chooser disposition = %v, want handled", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("upgrade chooser did not return")
	}
}

// TestDashboardUpgradeRestartDaemonRelaunchNote drives a daemon-ACTIVE upgrade: after the
// upgrade installs and the resident daemon restarts cleanly (aligned), the result screen must
// still tell the user their interactive process runs the old binary and must be relaunched --
// the same relaunch note the daemon-inactive branch already shows. Both the "RESTARTED, ALIGNED"
// keyword AND the "relaunch proxsave" note must appear.
func TestDashboardUpgradeRestartDaemonRelaunchNote(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	origRun, origInstalled, origLoad := dashboardUpgradeRun, daemonInstalledProbe, upgradeLoadConfig
	t.Cleanup(func() {
		dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk
		dashboardUpgradeRun, daemonInstalledProbe, upgradeLoadConfig = origRun, origInstalled, origLoad
	})

	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(context.Context, *logging.Logger, string) *UpdateInfo {
		return &UpdateInfo{NewVersion: true, Current: "1.0.0", Latest: "2.0.0", Tag: "v2.0.0"}
	}
	dashboardUpgradeRun = func(context.Context, *cli.Args, *logging.BootstrapLogger, upgradeRunOptions) int { return 0 }

	// Daemon-ACTIVE path: an installed unit + active presence route the post-upgrade restart
	// through restartAndVerifyDaemon. A readable (empty) config keeps the backup lock path
	// KNOWN so the restart is not deferred.
	daemonInstalledProbe = func() bool { return true }
	upgradeLoadConfig = func(string, string) (*config.Config, error) { return &config.Config{}, nil }

	// A clean, fresh, aligned restart: idle backup, no-op restart, and a state that is
	// process-alive + aligned + fresh (StartTS far past any snapshot). stubRestartSeams also
	// pins daemonPresenceProbe to Active so daemonIsActive is true.
	shrinkRestartBudgets(t)
	stubRestartSeams(t,
		func(context.Context) error { return nil },
		func(string) bool { return false },
		func(health.DaemonStateInput) health.DaemonState {
			return health.DaemonState{ProcessAlive: true, Aligned: true, AlignChecked: true, StartTS: 1 << 60, Version: "9.9.9"}
		})

	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })

	done := make(chan dashboardActionDisposition, 1)
	go func() {
		done <- runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env")
	}()

	waitFor := func(substr string) {
		deadline := time.After(uitest.Deadline(15 * time.Second))
		for {
			if strings.Contains(ansi.Strip(driver.buf.String()[driver.matchStart:]), substr) {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %q, tail:\n%s", substr, tailStr(ansi.Strip(driver.buf.String())))
			case <-time.After(20 * time.Millisecond):
			}
		}
	}

	driver.waitScreen("Upgrade")
	waitFor("2.0.0")     // auto-check found the update
	driver.keys("enter") // Run upgrade
	driver.waitScreen("Running upgrade")
	driver.waitOutput("enter continue")
	driver.keys("enter") // leave the stream panel -> daemon-active restart step
	driver.waitScreen("Daemon restart")
	waitFor("RESTARTED, ALIGNED") // the aligned success keyword
	waitFor("relaunch proxsave")  // ...plus the relaunch note (absent in the active branch before the fix)
	driver.keys("enter")

	select {
	case got := <-done:
		if got != dashboardActionReload {
			t.Fatalf("aligned restart disposition = %v, want reload", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("aligned restart did not request dashboard reload")
	}
}

// TestDashboardUpgradeRestartFailureStillReloads pins the installed-binary boundary: once
// binary and config upgrade succeeded, a daemon restart error is shown but cannot send the
// operator back into the old process.
func TestDashboardUpgradeRestartFailureStillReloads(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	origRun, origInstalled, origLoad := dashboardUpgradeRun, daemonInstalledProbe, upgradeLoadConfig
	t.Cleanup(func() {
		dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk
		dashboardUpgradeRun, daemonInstalledProbe, upgradeLoadConfig = origRun, origInstalled, origLoad
	})
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(context.Context, *logging.Logger, string) *UpdateInfo {
		return &UpdateInfo{NewVersion: true, Current: "1.0.0", Latest: "2.0.0", Tag: "v2.0.0"}
	}
	dashboardUpgradeRun = func(context.Context, *cli.Args, *logging.BootstrapLogger, upgradeRunOptions) int { return 0 }
	daemonInstalledProbe = func() bool { return true }
	upgradeLoadConfig = func(string, string) (*config.Config, error) { return &config.Config{}, nil }
	shrinkRestartBudgets(t)
	stubRestartSeams(t,
		func(context.Context) error { return errors.New("systemctl boom") },
		func(string) bool { return false },
		func(health.DaemonStateInput) health.DaemonState { return health.DaemonState{} })

	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan screenPush, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- screenPush{title: title, at: driver.buf.Len()} })
	done := make(chan dashboardActionDisposition, 1)
	go func() { done <- runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env") }()

	driver.waitScreen("Upgrade")
	driver.keys("enter")
	driver.waitScreen("Running upgrade")
	driver.waitOutput("enter continue")
	driver.keys("enter")
	driver.waitScreen("Daemon restart")
	driver.waitOutput("RESTART FAILED")
	driver.keys("enter")

	select {
	case got := <-done:
		if got != dashboardActionReload {
			t.Fatalf("failed restart disposition = %v, want reload", got)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("failed restart did not request dashboard reload")
	}
}
