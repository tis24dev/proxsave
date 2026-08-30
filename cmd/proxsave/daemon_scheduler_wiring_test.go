// Package main contains the proxsave command entrypoint.
package main

import (
	"go/ast"
	gotypes "go/types"
	"testing"
)

// daemonWiringSite names one call whose ARGUMENT decides a scheduler outcome and whose enclosing
// function no test can drive.
type daemonWiringSite struct {
	file      string
	enclosing string
	callee    string
	argIndex  int
	wantArg   string
	// consequence names what a wrong argument costs on a real host, so a failure reads as a
	// defect report rather than a style complaint.
	consequence string
}

// TestDaemonSchedulerDecisionsAreWiredFromTheirRealSource is the wiring guard for the two
// scheduler decisions whose enclosing function is out of reach of a behavioural test.
//
// upgradeFinalizePhase execs a binary and writes outside a temp dir; applyDaemonMode shells out
// to systemctl, provisions a relay secret and probes a running daemon. Neither can be driven, so
// what they PASS is unpinned even though both callees are covered: replacing the merge result
// with a literal that says "the key was absent", or dropping the handover call entirely, compiles
// and leaves the whole suite green while changing what happens to every upgraded host.
//
// This is the same shape as the hostname guard in run_hostname_wiring_test.go, and the same
// caveat applies: a green run is not proof that everything is wired, only that these two rows
// are. If a legitimate refactor moves a call, point its row at the new home rather than deleting
// it.
func TestDaemonSchedulerDecisionsAreWiredFromTheirRealSource(t *testing.T) {
	for _, site := range []daemonWiringSite{
		{
			file: "upgrade.go", enclosing: "upgradeFinalizePhase",
			callee: "maybeAutoMigrateDaemon", argIndex: 4, wantArg: "cfgUpgradeResult",
			consequence: "the retrofit stops deciding on what the config merge actually reported. A literal saying the key was absent migrates every upgraded host, including one that recorded cron by running --daemon-remove; a literal saying it was present retrofits nobody, ever",
		},
	} {
		t.Run(site.file+":"+site.enclosing, func(t *testing.T) {
			fn := findFuncDecl(t, site.file, site.enclosing)

			found := false
			gotArg := ""
			argCount := 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || gotypes.ExprString(call.Fun) != site.callee {
					return true
				}
				found = true
				argCount = len(call.Args)
				if site.argIndex < len(call.Args) {
					gotArg = gotypes.ExprString(call.Args[site.argIndex])
				}
				return true
			})

			if !found {
				t.Fatalf("%s: %s no longer calls %s. If the call moved, point this row at its new home rather than deleting it: %s",
					site.file, site.enclosing, site.callee, site.consequence)
			}
			if site.argIndex >= argCount {
				t.Fatalf("%s: %s calls %s with %d argument(s), so there is no argument %d: %s",
					site.file, site.enclosing, site.callee, argCount, site.argIndex, site.consequence)
			}
			if gotArg != site.wantArg {
				t.Fatalf("%s: %s calls %s with arg %d = %q, want %q. Then %s",
					site.file, site.enclosing, site.callee, site.argIndex, gotArg, site.wantArg, site.consequence)
			}
		})
	}
}

// The cron half of the switch to the daemon - adopt the run time, remove the cron lines, count
// what survives - lives in prepareCronHandoverForDaemon precisely so a test can drive it. That
// leaves one thing a test cannot see: whether applyDaemonMode still CALLS it. Dropping the call
// compiles, and every guarantee the extracted helper carries goes with it: the host keeps running
// at whatever hour the key held, its proxsave cron lines are never removed so the daemon shares
// the night with them, and the dashboard reports no duplicate because the count stays zero.
//
// It must also run BEFORE the config write, or an aborted setup leaves SCHEDULER_MODE=daemon on a
// host whose cron lines are still live.
func TestApplyDaemonModeStillHandsTheCronScheduleOver(t *testing.T) {
	const (
		handover  = "prepareCronHandoverForDaemon"
		envWriter = "setBackupEnvKeys"
	)
	fn := findFuncDecl(t, "daemon_setup.go", "applyDaemonMode")

	if !callIsTopLevelStatement(fn, handover) {
		t.Fatalf("daemon_setup.go: applyDaemonMode does not call %s as an unconditional top-level statement. Without it the host keeps the hour its key held, its proxsave cron lines are never removed, and the duplicate count the dashboard renders stays zero", handover)
	}
	// No callIsSkippedBeforeItRuns here, deliberately: applyDaemonMode returns early when
	// installDaemonService fails, and skipping the handover there is correct. There is no unit
	// to hand the schedule over to, and taking the cron lines away would leave the host with
	// neither.

	handoverAt := topLevelCallIndex(fn, handover)
	writerAt := topLevelCallIndex(fn, envWriter)
	if writerAt >= 0 && handoverAt > writerAt {
		t.Fatalf("daemon_setup.go: applyDaemonMode calls %s AFTER %s. A setup that aborts in between then leaves SCHEDULER_MODE=daemon recorded on a host whose proxsave cron lines are still live", handover, envWriter)
	}
}
