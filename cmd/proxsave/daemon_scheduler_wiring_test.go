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
// caveat applies: a green run is not proof that everything is wired, only that these rows are.
// If a legitimate refactor moves a call, point its row at the new home rather than deleting it.
//
// What it pins is the IDENTIFIER passed, at every call site, not the value that identifier holds:
// reassigning cfgUpgradeResult above the call keeps this green. What makes that acceptable here
// is that the identifier is filled two statements earlier by the merge and read nowhere else, so
// a reassignment would be a deliberate act rather than a drift. Reachability is not pinned
// either: the call sits inside "if upgradeErr == nil", which is correct - a failed binary install
// must not retrofit anything - so the unconditional check the hostname guard uses does not apply.
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

			// EVERY call is collected, not just the last one seen. A second call site added
			// beside the first - a retry, a branch for the --localfile path - would otherwise
			// pass on the strength of whichever the walk reached last, while the other one
			// decided the host's scheduler from a value nothing checked.
			calls := []*ast.CallExpr{}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok && gotypes.ExprString(call.Fun) == site.callee {
					calls = append(calls, call)
				}
				return true
			})

			if len(calls) == 0 {
				t.Fatalf("%s: %s no longer calls %s. If the call moved, point this row at its new home rather than deleting it: %s",
					site.file, site.enclosing, site.callee, site.consequence)
			}
			for i, call := range calls {
				if site.argIndex >= len(call.Args) {
					t.Fatalf("%s: %s calls %s with %d argument(s) at call %d of %d, so there is no argument %d: %s",
						site.file, site.enclosing, site.callee, len(call.Args), i+1, len(calls), site.argIndex, site.consequence)
				}
				if got := gotypes.ExprString(call.Args[site.argIndex]); got != site.wantArg {
					t.Fatalf("%s: %s calls %s with arg %d = %q at call %d of %d, want %q. Then %s",
						site.file, site.enclosing, site.callee, site.argIndex, got, i+1, len(calls), site.wantArg, site.consequence)
				}
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

	// The handover's RESULT is what carries the removal count and the duplicate count out of
	// here. Calling it and dropping what it returns leaves every screen reporting zero.
	//
	// EVERY call is checked, not the last one the walk happens to reach. A second call site added
	// beside the first would otherwise pass on the strength of whichever came last, which is the
	// same defect this file already fixed for the provenance guard.
	handoverCalls := []*ast.CallExpr{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && gotypes.ExprString(call.Fun) == handover {
			handoverCalls = append(handoverCalls, call)
		}
		return true
	})
	if len(handoverCalls) == 0 {
		t.Fatalf("daemon_setup.go: applyDaemonMode no longer calls %s at all", handover)
	}
	wantArgs := []string{"ctx", "configPath", "execToken", "bootstrap"}
	for c, handoverCall := range handoverCalls {
		if len(handoverCall.Args) != len(wantArgs) {
			t.Fatalf("daemon_setup.go: %s is called with %d argument(s) at call %d of %d, want %d (%v)", handover, len(handoverCall.Args), c+1, len(handoverCalls), len(wantArgs), wantArgs)
		}
		for i, want := range wantArgs {
			if got := gotypes.ExprString(handoverCall.Args[i]); got != want {
				t.Errorf("daemon_setup.go: %s arg %d = %q at call %d of %d, want %q. A different config path or exec token here hands the schedule over for the wrong host", handover, i, got, c+1, len(handoverCalls), want)
			}
		}
	}
	if !callResultIsAssigned(fn, handover) {
		t.Fatalf("daemon_setup.go: applyDaemonMode calls %s and discards its result. The removal count and the duplicate count then never leave this function, so every screen reports zero", handover)
	}

	// The ordering has to be read from the STATEMENT that contains each call, not from a
	// top-level call statement: setBackupEnvKeys is written as "if err := setBackupEnvKeys(...)",
	// an IfStmt, which topLevelCallIndex does not recognise. Keyed on that helper this assertion
	// compared -1 against everything and never ran at all.
	handoverAt := statementIndexContainingCall(fn, handover)
	writerAt := statementIndexContainingCall(fn, envWriter)
	if handoverAt < 0 {
		t.Fatalf("daemon_setup.go: %s is not called from a statement of applyDaemonMode's body", handover)
	}
	if writerAt < 0 {
		t.Fatalf("daemon_setup.go: applyDaemonMode no longer calls %s, so the ordering this guard exists for cannot be checked; point it at the new writer rather than deleting it", envWriter)
	}
	if handoverAt > writerAt {
		t.Fatalf("daemon_setup.go: applyDaemonMode calls %s AFTER %s. A setup that aborts in between then leaves SCHEDULER_MODE=daemon recorded on a host whose proxsave cron lines are still live", handover, envWriter)
	}
}

// statementIndexContainingCall returns the index of the top-level statement of fn whose SUBTREE
// contains a call to callee, or -1.
//
// topLevelCallIndex answers a narrower question - a call that IS the statement, or the single
// right-hand side of an assignment - and that narrowness is right where it is used. Here it is
// wrong: the writer this guard orders against is written as "if err := setBackupEnvKeys(...);
// err != nil", so the call lives in an IfStmt's init and the index came back -1 for it every
// time, which made the comparison vacuous.
func statementIndexContainingCall(fn *ast.FuncDecl, callee string) int {
	for i, stmt := range fn.Body.List {
		found := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && gotypes.ExprString(call.Fun) == callee {
				found = true
			}
			return !found
		})
		if found {
			return i
		}
	}
	return -1
}

// callResultIsAssigned reports whether some statement of fn assigns the result of calling callee
// to something USABLE, rather than calling it for its side effects alone.
//
// "_ = f()" is not that. It compiles, it is an *ast.AssignStmt, and it discards the result just
// as completely as a bare call - which is exactly the change this assertion exists to catch, so
// accepting it made the assertion prove nothing. At least one left-hand side has to be a name the
// function can go on to use.
func callResultIsAssigned(fn *ast.FuncDecl, callee string) bool {
	assigned := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || gotypes.ExprString(call.Fun) != callee {
			return true
		}
		for _, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); ok && ident.Name != "_" {
				assigned = true
			}
		}
		return true
	})
	return assigned
}
