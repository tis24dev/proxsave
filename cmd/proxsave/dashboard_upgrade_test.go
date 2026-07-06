// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/shell"
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

// TestDashboardUpgradeScreen drives the in-session upgrade screen directly: Check (shows
// the available version, sanitized) -> Run upgrade (calls the real upgrade with
// Upgrade+AutoYes+ConfigPath, shows the success notice) -> Back.
func TestDashboardUpgradeScreen(t *testing.T) {
	origVer, origChk := dashboardUpgradeVersion, dashboardUpgradeCheck
	origRun, origMute := dashboardUpgradeRun, dashboardUpgradeMute
	t.Cleanup(func() {
		dashboardUpgradeVersion, dashboardUpgradeCheck = origVer, origChk
		dashboardUpgradeRun, dashboardUpgradeMute = origRun, origMute
	})

	gotVersion := ""
	dashboardUpgradeVersion = func() string { return "1.0.0" }
	dashboardUpgradeCheck = func(ctx context.Context, logger *logging.Logger, current string) *UpdateInfo {
		gotVersion = current
		// A hostile GitHub version with a non-allowlist byte ('!'): the screen MUST scrub
		// it (a real ESC would be invisible after ansi.Strip, so '!' is the visible probe).
		return &UpdateInfo{NewVersion: true, Current: current, Latest: "2.0.0!"}
	}
	var gotArgs *cli.Args
	dashboardUpgradeRun = func(ctx context.Context, args *cli.Args, bl *logging.BootstrapLogger) int {
		gotArgs = args
		return 0 // success
	}
	dashboardUpgradeMute = func() func() { return func() {} } // no real stdio swap in tests

	// Build an observed session eagerly (the shared seam creates it lazily via a flow).
	driver := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan string, 64)}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	driver.session = shell.StartObservedForTest(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Dashboard"},
		driver.buf, func(title string) { driver.pushes <- title })

	done := make(chan struct{})
	go func() {
		runDashboardUpgrade(ctx, driver.session, "/tmp/backup.env")
		close(done)
	}()

	driver.waitScreen("Upgrade")

	// waitFor polls the accumulated output until it contains substr (the screen-title
	// push can precede the rendered bytes, so a bare read races the flush).
	waitFor := func(substr string) string {
		deadline := time.After(15 * time.Second)
		for {
			out := ansi.Strip(driver.buf.String())
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

	// Check: the loop re-renders the Upgrade screen with the available version.
	driver.keys("enter")
	out := waitFor("2.0.0")
	if gotVersion != "1.0.0" {
		t.Fatalf("check must run against the current version, got %q", gotVersion)
	}
	if strings.Contains(out, "2.0.0!") {
		t.Fatalf("the non-allowlist byte from the GitHub version must be scrubbed, out tail:\n%s", tailStr(out))
	}

	// Run upgrade (the button swapped to "Run upgrade").
	driver.keys("enter")
	driver.waitScreen("Upgrade complete")
	if gotArgs == nil || !gotArgs.Upgrade || !gotArgs.UpgradeAutoYes || gotArgs.ConfigPath != "/tmp/backup.env" {
		t.Fatalf("run upgrade must pass Upgrade+AutoYes+ConfigPath, got %+v", gotArgs)
	}

	driver.keys("enter")         // dismiss the notice
	driver.waitScreen("Upgrade") // back on the screen (button reverted to Check upgrade)
	driver.keys("down enter")    // Back -> return
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("upgrade screen did not return")
	}
}
