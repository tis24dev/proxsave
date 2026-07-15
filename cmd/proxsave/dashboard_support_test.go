package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/support"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestValidateSupportIssue pins the #<number> issue format (mirrors support.RunIntro).
func TestValidateSupportIssue(t *testing.T) {
	for _, v := range []string{"#1", "#1234", "  #42  "} {
		if err := validateSupportIssue(v); err != nil {
			t.Errorf("%q should be valid: %v", v, err)
		}
	}
	for _, v := range []string{"", "1234", "#", "#abc", "abc", "#12a"} {
		if err := validateSupportIssue(v); err == nil {
			t.Errorf("%q should be invalid", v)
		}
	}
}

// TestHandleSupportIntroSkipsWhenMetaProvided: when the dashboard has already collected the
// metadata, the stdin RunIntro is skipped (handled=false => the run proceeds), never reading
// stdin over the graphical run.
func TestHandleSupportIntroSkipsWhenMetaProvided(t *testing.T) {
	args := &cli.Args{Support: true, SupportMetaProvided: true, SupportGitHubUser: "alice", SupportIssueID: "#42"}
	code, handled := handleSupportIntro(context.Background(), args, nil, nil)
	if handled {
		t.Fatalf("meta-provided support must proceed to the run (handled=false), got handled=true code=%d", code)
	}
}

// stubSupportForm swaps the form seam for a test.
func stubSupportForm(t *testing.T, meta support.Meta, ok bool) {
	t.Helper()
	orig := dashboardRunSupportForm
	t.Cleanup(func() { dashboardRunSupportForm = orig })
	dashboardRunSupportForm = func(context.Context, *shell.Session) (support.Meta, bool) { return meta, ok }
}

// TestDashboardSupportArmsBackup: confirming the support form arms support mode (Support +
// SupportMetaProvided + the meta) and falls through to the backup handoff (handled=false).
func TestDashboardSupportArmsBackup(t *testing.T) {
	installDashboardGates(t, true, true)
	stubSupportForm(t, support.Meta{GitHubUser: "alice", IssueID: "#42"}, true)
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down down down down enter") // Support (12 downs)
	select {
	case handled := <-resCh:
		if handled {
			t.Fatal("support must fall through to the backup handoff (handled=false)")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if !args.Support || !args.SupportMetaProvided {
		t.Fatalf("support must arm Support + SupportMetaProvided: %+v", args)
	}
	if args.SupportGitHubUser != "alice" || args.SupportIssueID != "#42" {
		t.Fatalf("support must carry the collected meta: %+v", args)
	}
}

// TestDashboardSupportCancelLoops: cancelling the support form returns to the menu WITHOUT
// arming support mode.
func TestDashboardSupportCancelLoops(t *testing.T) {
	installDashboardGates(t, true, true)
	stubSupportForm(t, support.Meta{}, false)
	driver := installDashboardSessionSeam(t)
	args := &cli.Args{}
	resCh := make(chan bool, 1)
	go func() {
		_, handled := maybeRunDashboard(context.Background(), args, nil, "1.0.0")
		resCh <- handled
	}()
	driver.waitScreen("Dashboard")
	driver.keys("down down down down down down down down down down down down enter") // Support (12 downs)
	driver.waitScreen("Dashboard")                                                   // form cancelled -> back to the menu
	driver.keys("esc")                                                               // exit
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from the menu must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if args.Support || args.SupportMetaProvided {
		t.Fatalf("cancelled support must not arm support mode: %+v", args)
	}
}
