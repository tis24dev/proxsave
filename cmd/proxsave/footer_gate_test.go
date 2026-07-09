package main

import (
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// setDashboardGraphicalForTest sets the package-level graphical latch under the
// handoff mutex, with a cleanup that resets it (the latch is a shared global).
func setDashboardGraphicalForTest(t *testing.T, v bool) {
	t.Helper()
	dashboardHandoff.mu.Lock()
	dashboardHandoff.graphical = v
	dashboardHandoff.mu.Unlock()
	t.Cleanup(func() {
		dashboardHandoff.mu.Lock()
		dashboardHandoff.graphical = false
		dashboardHandoff.mu.Unlock()
	})
}

// TestFooterSuppressed pins the SHARED footer gate: an end-of-run CLI footer is
// suppressed for a graphical (dashboard) run, which shows its outcome on-screen,
// and prints for a plain CLI/cron run. This single predicate now gates
// printFinalSummary, printInstallFooter and printUpgradeFooter alike.
func TestFooterSuppressed(t *testing.T) {
	setDashboardGraphicalForTest(t, false)
	if footerSuppressed() {
		t.Fatal("a CLI run (not graphical) must NOT suppress the footer")
	}

	setDashboardGraphicalForTest(t, true)
	if !footerSuppressed() {
		t.Fatal("a graphical (dashboard) run must suppress the footer")
	}
}

// TestDashboardRunWasGraphical asserts the graphical latch: false for a fresh
// run, still false after a stash alone, true once a flow ADOPTS the session, and
// reset by releaseDashboardLeftovers (process-end isolation).
func TestDashboardRunWasGraphical(t *testing.T) {
	// stash/adopt mutate the default logger's output; isolate + restore it.
	prevLogger := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelInfo, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevLogger) })
	// Reset the latch even if the test fails mid-way.
	t.Cleanup(releaseDashboardLeftovers)

	// The latch is a shared package global; start from a known-false baseline so
	// the test is order-independent (e.g. under -shuffle).
	releaseDashboardLeftovers()
	if dashboardRunWasGraphical() {
		t.Fatal("baseline: the graphical latch must be false after release")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var buf shell.SyncBuffer
	session := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Backup"}, &buf)
	bootstrap := logging.NewBootstrapLogger()

	stashDashboardSession(session, bootstrap)
	if dashboardRunWasGraphical() {
		t.Fatal("stashing alone (not yet adopted) must not mark the run graphical")
	}

	if adoptDashboardSession(shell.Config{AppName: "ProxSave", Subtitle: "Backup"}) == nil {
		t.Fatal("adoption must return the stashed session")
	}
	if !dashboardRunWasGraphical() {
		t.Fatal("adopting the dashboard session must mark the run graphical")
	}

	releaseDashboardLeftovers()
	if dashboardRunWasGraphical() {
		t.Fatal("releaseDashboardLeftovers must reset the graphical latch (test isolation)")
	}
	_ = session.Close()
}
