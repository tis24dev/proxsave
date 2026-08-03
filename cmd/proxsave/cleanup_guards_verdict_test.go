package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestCleanupGuardsExitCodeReportsPendingGuards is the regression proper: the CLI took
// the error-only wrapper and exited 0 for anything short of an engine failure, so a
// script gating on `proxsave --cleanup-guards` was told the storage was unlocked while
// guards were still holding it. Every non-clean outcome must now exit ExitGuardsPending
// — and it must stay distinct from ExitGenericError, because "the cleanup failed" and
// "the cleanup ran but the datastore is still mounted" need different remedies.
func TestCleanupGuardsExitCodeReportsPendingGuards(t *testing.T) {
	cases := []struct {
		name   string
		report orchestrator.GuardCleanupReport
		dryRun bool
		want   types.ExitCode
	}{
		{
			name:   "check finds guards",
			report: orchestrator.GuardCleanupReport{BindGuards: 2},
			dryRun: true,
			want:   types.ExitGuardsPending,
		},
		{
			name:   "check finds nothing",
			report: orchestrator.GuardCleanupReport{},
			dryRun: true,
			want:   types.ExitSuccess,
		},
		{
			name:   "run removes everything",
			report: orchestrator.GuardCleanupReport{BindGuards: 2, Unmounted: 2, GuardsRemaining: 0},
			want:   types.ExitSuccess,
		},
		{
			name:   "run leaves a guard behind",
			report: orchestrator.GuardCleanupReport{BindGuards: 2, Unmounted: 1, GuardsRemaining: 1},
			want:   types.ExitGuardsPending,
		},
		{
			name:   "run leaves an immutable flag pending",
			report: orchestrator.GuardCleanupReport{ImmutableGuards: 1, ImmutablePending: 1},
			want:   types.ExitGuardsPending,
		},
		{
			// -1 is the fail-closed unknown sentinel. A cleanup that cannot confirm
			// what remains must not be reported as having unlocked the storage — the
			// dashboard already treats it this way via guardApplyClean.
			name:   "run cannot confirm what remains",
			report: orchestrator.GuardCleanupReport{BindGuards: 1, GuardsRemaining: -1},
			want:   types.ExitGuardsPending,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := guardCleanupExitCode(tc.report, tc.dryRun); got != tc.want {
				t.Fatalf("guardCleanupExitCode = %d (%s), want %d (%s)", got, got, tc.want, tc.want)
			}
		})
	}
}

// TestRunCleanupGuardsModeReflectsTheVerdictInTheExitCode drives the mode end to end
// through the same seam the dashboard flow uses, so the exit code a caller actually
// observes is pinned, not just the classifier.
func TestRunCleanupGuardsModeReflectsTheVerdictInTheExitCode(t *testing.T) {
	cases := []struct {
		name   string
		report orchestrator.GuardCleanupReport
		dryRun bool
		want   int
	}{
		{"dry run, guards found", orchestrator.GuardCleanupReport{BindGuards: 1}, true, types.ExitGuardsPending.Int()},
		{"dry run, clean", orchestrator.GuardCleanupReport{}, true, types.ExitSuccess.Int()},
		{"real run, unlocked", orchestrator.GuardCleanupReport{BindGuards: 1, Unmounted: 1}, false, types.ExitSuccess.Int()},
		{"real run, still locked", orchestrator.GuardCleanupReport{BindGuards: 1, GuardsRemaining: 1}, false, types.ExitGuardsPending.Int()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubGuardReport(t, tc.report, tc.report)
			code, handled := runCleanupGuardsMode(context.Background(),
				&cli.Args{CleanupGuards: true, DryRun: tc.dryRun}, logging.NewBootstrapLogger())
			if !handled {
				t.Fatal("--cleanup-guards must be handled by this mode")
			}
			if code != tc.want {
				t.Fatalf("exit code = %d, want %d", code, tc.want)
			}
		})
	}
}

// TestRunCleanupGuardsModePrintsTheVerdict: the exit code is the machine-readable half
// of this fix and the verdict is the human half — an operator running the mode by hand
// needs to be told WHAT is still locking the storage, which the CLI never said. The
// mode builds its own logger, so this captures real stdout rather than calling
// logCLIGuardVerdict directly; a test that called it directly could not notice the mode
// dropping the call entirely, which is exactly what it used to do.
func TestRunCleanupGuardsModePrintsTheVerdict(t *testing.T) {
	stubGuardReport(t,
		orchestrator.GuardCleanupReport{BindGuards: 2},
		orchestrator.GuardCleanupReport{BindGuards: 2, GuardsRemaining: 2})

	out := captureNewKeyStdout(t, func() {
		runCleanupGuardsMode(context.Background(), //nolint:errcheck
			&cli.Args{CleanupGuards: true, DryRun: true}, logging.NewBootstrapLogger())
	})
	for _, want := range []string{"2 bind mount guards", "locking the storage"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the mode must print the verdict; want %q in %q", want, out)
		}
	}
}

// TestRunCleanupGuardsModeKeepsEngineFailuresDistinct: an engine error is a failure to
// report, not a locked datastore to retry. Collapsing the two would defeat the reason
// ExitGuardsPending exists at all.
func TestRunCleanupGuardsModeKeepsEngineFailuresDistinct(t *testing.T) {
	orig := cleanupGuardsReport
	t.Cleanup(func() { cleanupGuardsReport = orig })
	cleanupGuardsReport = func(_ context.Context, _ *logging.Logger, _ bool) (orchestrator.GuardCleanupReport, error) {
		return orchestrator.GuardCleanupReport{}, errors.New("mountinfo unreadable")
	}

	code, handled := runCleanupGuardsMode(context.Background(),
		&cli.Args{CleanupGuards: true}, logging.NewBootstrapLogger())
	if !handled {
		t.Fatal("--cleanup-guards must be handled by this mode")
	}
	if code != types.ExitGenericError.Int() {
		t.Fatalf("an engine failure must exit %d, got %d", types.ExitGenericError.Int(), code)
	}
	if code == types.ExitGuardsPending.Int() {
		t.Fatal("an engine failure must not be reported as pending guards")
	}
}

// TestRunCleanupGuardsModeIsInertWithoutTheFlag: the mode dispatcher calls every
// runXxxMode in turn, so this one must decline cleanly — and must not touch the engine
// when it does.
func TestRunCleanupGuardsModeIsInertWithoutTheFlag(t *testing.T) {
	orig := cleanupGuardsReport
	t.Cleanup(func() { cleanupGuardsReport = orig })
	cleanupGuardsReport = func(_ context.Context, _ *logging.Logger, _ bool) (orchestrator.GuardCleanupReport, error) {
		t.Fatal("the engine must not run without --cleanup-guards")
		return orchestrator.GuardCleanupReport{}, nil
	}

	code, handled := runCleanupGuardsMode(context.Background(), &cli.Args{}, logging.NewBootstrapLogger())
	if handled || code != types.ExitSuccess.Int() {
		t.Fatalf("without the flag the mode must decline, got code=%d handled=%v", code, handled)
	}
}

// TestCLIGuardVerdictSaysWhatWasFoundAndWhatToDo: the exit code alone tells a script
// what happened but tells a human nothing. The CLI had no counterpart to the
// dashboard's CLEAN/FOUND verdict at all — the read was performed and the report
// discarded.
func TestCLIGuardVerdictSaysWhatWasFoundAndWhatToDo(t *testing.T) {
	cases := []struct {
		name    string
		report  orchestrator.GuardCleanupReport
		dryRun  bool
		want    []string
		notWant []string
	}{
		{
			name:   "check with two kinds of guard",
			report: orchestrator.GuardCleanupReport{BindGuards: 2, ImmutableGuards: 1},
			dryRun: true,
			want:   []string{"2 bind mount guards", "1 immutable flag", "locking the storage", "--dry-run"},
		},
		{
			name:    "check with nothing to unlock",
			report:  orchestrator.GuardCleanupReport{},
			dryRun:  true,
			want:    []string{"Nothing to unlock"},
			notWant: []string{"--dry-run"}, // no action to suggest
		},
		{
			name:   "real run leaves guards behind",
			report: orchestrator.GuardCleanupReport{BindGuards: 1, GuardsRemaining: 1},
			want:   []string{"still in place", "hidden under a live mount", "Unmount the datastore", "--cleanup-guards"},
		},
		{
			name:   "real run unlocks the storage",
			report: orchestrator.GuardCleanupReport{BindGuards: 1, Unmounted: 1},
			want:   []string{"storage is unlocked"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(buf)
			logCLIGuardVerdict(logger, tc.report, tc.dryRun)

			out := buf.String()
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("verdict must mention %q, got %q", want, out)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(out, notWant) {
					t.Errorf("verdict must not mention %q, got %q", notWant, out)
				}
			}
		})
	}
}

// TestCLIGuardVerdictWarnsWhenTheStorageStaysLocked: this mode usually runs from cron,
// where nobody is watching. A locked storage reported at INFO alongside every other
// line is a line nobody reads; the dashboard already colours it yellow.
func TestCLIGuardVerdictWarnsWhenTheStorageStaysLocked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report orchestrator.GuardCleanupReport
		dryRun bool
	}{
		{"guards found by the check", orchestrator.GuardCleanupReport{BindGuards: 1}, true},
		{"guards left by a real run", orchestrator.GuardCleanupReport{BindGuards: 1, GuardsRemaining: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(buf)
			logCLIGuardVerdict(logger, tc.report, tc.dryRun)
			if !strings.Contains(buf.String(), "WARNING") {
				t.Fatalf("a storage that stays locked must be logged as a warning, got %q", buf.String())
			}
		})
	}
}

// TestGuardFactsCarryNoFrontEndCallToAction pins the split the shared helpers exist
// for: the dashboard names a button, the CLI names a flag, and neither wording may
// leak into the facts both of them render.
func TestGuardFactsCarryNoFrontEndCallToAction(t *testing.T) {
	reports := []orchestrator.GuardCleanupReport{
		{},
		{BindGuards: 3},
		{ImmutableGuards: 2, ImmutablePending: 1},
		{BindGuards: 1, GuardsRemaining: -1},
	}
	for _, r := range reports {
		for _, facts := range []string{guardCheckFacts(r), guardApplyFacts(r)} {
			for _, leak := range []string{"Apply", "--dry-run", "--cleanup-guards", "Cleanup guards"} {
				if strings.Contains(facts, leak) {
					t.Errorf("shared facts %q must not name a front-end action (%q)", facts, leak)
				}
			}
		}
	}
}
