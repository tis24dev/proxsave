package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// TestCleanupGuardsOutcomeClassification pins how the captured cleanup log maps to the
// styled "Status:" (level, keyword): dry-run always previews (Warn/DRY RUN); the real
// run reads DONE / NOTHING TO CLEAN / PENDING from the summary; any error is surfaced
// as Error/FAILED so the caller shows a red screen (this covers the root-required error).
func TestCleanupGuardsOutcomeClassification(t *testing.T) {
	orig := cleanupGuardsRun
	t.Cleanup(func() { cleanupGuardsRun = orig })

	cases := []struct {
		name      string
		dryRun    bool
		emit      func(lg *logging.Logger)
		runErr    error
		wantLevel orchestrator.HealthcheckSetupLevel
		wantKey   string
		wantErr   bool
	}{
		{"dry-run previews as warn", true, func(lg *logging.Logger) {
			lg.Info("DRY RUN: would remove /var/lib/proxsave/guards")
		}, nil, orchestrator.HealthcheckSetupLevelWarn, "DRY RUN", false},
		{"apply clean reads DONE", false, func(lg *logging.Logger) {
			lg.Info("Guard cleanup summary: bind-unmounted=1 guards-remaining=0 immutable-cleared=0 immutable-pending=0 guard-dir=removed")
		}, nil, orchestrator.HealthcheckSetupLevelOk, "DONE", false},
		{"apply with no guard dir reads NOTHING TO CLEAN", false, func(lg *logging.Logger) {
			lg.Info("No guard directory found at /var/lib/proxsave/guards — nothing to clean up.")
		}, nil, orchestrator.HealthcheckSetupLevelOk, "NOTHING TO CLEAN", false},
		{"apply with leftovers reads PENDING", false, func(lg *logging.Logger) {
			lg.Warning("Guard cleanup: 2 bind guard(s) still present")
		}, nil, orchestrator.HealthcheckSetupLevelWarn, "PENDING", false},
		{"failure surfaces FAILED", true, nil,
			errors.New("cleanup guards requires root privileges"),
			orchestrator.HealthcheckSetupLevelError, "FAILED", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cleanupGuardsRun = func(_ context.Context, lg *logging.Logger, dryRun bool) error {
				if dryRun != tc.dryRun {
					t.Fatalf("dryRun = %v, want %v", dryRun, tc.dryRun)
				}
				if tc.emit != nil {
					tc.emit(lg)
				}
				return tc.runErr
			}
			level, keyword, _, err := cleanupGuardsOutcome(context.Background(), tc.dryRun)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if level != tc.wantLevel || keyword != tc.wantKey {
				t.Fatalf("got (%v, %q), want (%v, %q)", level, keyword, tc.wantLevel, tc.wantKey)
			}
		})
	}
}

// TestCleanGuardLogStripsPrefix locks that the captured "[ts] LEVEL msg" console lines
// are reduced to bare messages for the "Status:" explanation (blank lines dropped),
// tested against the real logger format rather than a hand-written prefix.
func TestCleanGuardLogStripsPrefix(t *testing.T) {
	var buf bytes.Buffer
	lg := logging.New(types.LogLevelInfo, false)
	lg.SetOutput(&buf)
	lg.Info("Guard cleanup summary: bind-unmounted=0 guard-dir=kept")
	lg.Warning("2 bind guard(s) still present")

	got := cleanGuardLog(buf.String())
	want := "Guard cleanup summary: bind-unmounted=0 guard-dir=kept\n2 bind guard(s) still present"
	if got != want {
		t.Fatalf("cleanGuardLog =\n%q\nwant\n%q", got, want)
	}
}

// TestDashboardCleanupGuardsTwoStepApply drives the in-session flow: selecting Cleanup
// guards runs a DRY RUN, shows its "Status:" screen, and only on Apply does it run for
// real. It must call the cleanup exactly twice (dry-run then apply) and set no flag.
func TestDashboardCleanupGuardsTwoStepApply(t *testing.T) {
	installDashboardGates(t, true, true) // cron state -> Cleanup guards is the 13th selectable (12 downs)
	orig := cleanupGuardsRun
	t.Cleanup(func() { cleanupGuardsRun = orig })
	var dryRuns []bool
	cleanupGuardsRun = func(_ context.Context, lg *logging.Logger, dryRun bool) error {
		dryRuns = append(dryRuns, dryRun)
		lg.Info("Guard cleanup summary: bind-unmounted=1 guard-dir=removed")
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
	driver.keys("down down down down down down down down down down down down enter") // Cleanup guards (12 downs)
	driver.waitScreen("Cleanup guards")                                              // dry-run preview
	driver.keys("enter")                                                             // Apply
	driver.waitScreen("Cleanup guards")                                              // real-run result
	driver.keys("esc")                                                               // Back to the menu
	driver.waitScreen("Dashboard")
	driver.keys("esc") // exit
	select {
	case handled := <-resCh:
		if !handled {
			t.Fatal("esc from menu must exit handled")
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if len(dryRuns) != 2 || dryRuns[0] != true || dryRuns[1] != false {
		t.Fatalf("expected dry-run then apply, got %v", dryRuns)
	}
	if args.CleanupGuards {
		t.Fatal("Cleanup guards is an in-session action; it must not set the --cleanup-guards flag")
	}
}

// TestDashboardCleanupGuardsCancelSkipsApply: Cancel on the dry-run screen returns to
// the menu WITHOUT running the real cleanup (the dry run is the only call).
func TestDashboardCleanupGuardsCancelSkipsApply(t *testing.T) {
	installDashboardGates(t, true, true)
	orig := cleanupGuardsRun
	t.Cleanup(func() { cleanupGuardsRun = orig })
	var dryRuns []bool
	cleanupGuardsRun = func(_ context.Context, lg *logging.Logger, dryRun bool) error {
		dryRuns = append(dryRuns, dryRun)
		lg.Info("DRY RUN: would remove /var/lib/proxsave/guards")
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
	driver.keys("down down down down down down down down down down down down enter") // Cleanup guards
	driver.waitScreen("Cleanup guards")                                              // dry-run preview
	driver.keys("down enter")                                                        // Cancel (second item)
	driver.waitScreen("Dashboard")                                                   // straight back, no apply
	driver.keys("esc")
	select {
	case <-resCh:
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("dashboard did not resolve")
	}
	if len(dryRuns) != 1 || dryRuns[0] != true {
		t.Fatalf("Cancel must run the dry run only, got %v", dryRuns)
	}
}
