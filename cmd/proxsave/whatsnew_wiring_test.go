package main

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/version"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// stubWhatsnewSeams saves and restores the three Screen 0 seams so a test can drive
// Decide/Run/MarkSeen outcomes without a real TTY or disk, and leak nothing.
func stubWhatsnewSeams(t *testing.T) {
	t.Helper()
	origDecide := whatsnewDecide
	origRun := whatsnewRun
	origSave := whatsnewSaveSeen
	t.Cleanup(func() {
		whatsnewDecide = origDecide
		whatsnewRun = origRun
		whatsnewSaveSeen = origSave
	})
}

// TestScreen0WriteOnlyOnContinue is the MANDATORY continue-only-write contract
// (SCRN-03, Pitfall 9, threat T-01-07): the seen-flag is written EXACTLY ONCE on an
// explicit continue (whatsnewRun returns nil) and NEVER on Esc (shell.ErrAborted) or
// a timeout (context.DeadlineExceeded).
func TestScreen0WriteOnlyOnContinue(t *testing.T) {
	const (
		base    = "/tmp/whatsnew-base"
		current = "0.30.0"
	)
	cases := []struct {
		name      string
		runErr    error
		wantCalls int
	}{
		{"continue writes once", nil, 1},
		{"esc never writes", shell.ErrAborted, 0},
		{"timeout never writes", context.DeadlineExceeded, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubWhatsnewSeams(t)

			whatsnewDecide = func(baseDir, curr string) (bool, string, error) {
				return true, "body", nil
			}
			whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
				return tc.runErr
			}
			var (
				calls   int
				gotBase string
				gotVer  string
			)
			whatsnewSaveSeen = func(baseDir, v string) error {
				calls++
				gotBase = baseDir
				gotVer = v
				return nil
			}

			maybeShowWhatsnew(context.Background(), nil, base, current)

			if calls != tc.wantCalls {
				t.Fatalf("whatsnewSaveSeen calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantCalls == 1 {
				if gotBase != base || gotVer != current {
					t.Fatalf("write args = (%q, %q), want (%q, %q)", gotBase, gotVer, base, current)
				}
			}
		})
	}
}

// TestMaybeShowWhatsnewGateSkips covers the two fail-toward-silence gate verdicts:
// a seen host (show=false, no error) and a broken state file (show=false, error).
// Neither runs the flow nor writes the flag; the dashboard proceeds to the menu.
func TestMaybeShowWhatsnewGateSkips(t *testing.T) {
	cases := []struct {
		name      string
		decideErr error
	}{
		{"skip when seen", nil},
		{"fail toward silence on decide error", errors.New("broken state file")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubWhatsnewSeams(t)

			whatsnewDecide = func(baseDir, curr string) (bool, string, error) {
				return false, "", tc.decideErr
			}
			runCalls := 0
			whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
				runCalls++
				return nil
			}
			saveCalls := 0
			whatsnewSaveSeen = func(baseDir, v string) error {
				saveCalls++
				return nil
			}

			maybeShowWhatsnew(context.Background(), nil, "/tmp/whatsnew-base", "0.30.0")

			if runCalls != 0 {
				t.Fatalf("whatsnewRun called %d times, want 0", runCalls)
			}
			if saveCalls != 0 {
				t.Fatalf("whatsnewSaveSeen called %d times, want 0", saveCalls)
			}
		})
	}
}

// TestMaybeRunDashboardScreen0 covers the two product-path integration truths:
//   - shows-once (SCRN-02): with a real Decide over a TempDir base (absent flag) and
//     whatsnewRun stubbed to continue, the flag is written; a second pass now finds
//     the flag present so Decide returns show=false and the flow is not run again.
//   - non-interactive skip (SCRN-05, T-01-09): with the dashboardIsInteractive seam
//     forced false, maybeRunDashboard returns early (handled=false) and the Screen 0
//     flow is never reached.
func TestMaybeRunDashboardScreen0(t *testing.T) {
	t.Run("shows once then never again", func(t *testing.T) {
		stubWhatsnewSeams(t)

		base := t.TempDir()
		const current = "0.30.0"

		// Real Decide + real MarkSeen over the TempDir base: only whatsnewRun is
		// stubbed (continue), so this exercises the true gate/read/write wiring.
		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}

		// First pass: absent flag -> unseen -> flow runs and the flag is written.
		maybeShowWhatsnew(context.Background(), nil, base, current)
		if runCalls != 1 {
			t.Fatalf("first pass: whatsnewRun calls = %d, want 1", runCalls)
		}
		if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
			t.Fatalf("first pass: seen flag not written at %s: %v", whatsnew.StatePath(base), err)
		}

		// Second pass: flag now present at current -> Decide returns show=false ->
		// the flow is never run again (shown exactly once).
		maybeShowWhatsnew(context.Background(), nil, base, current)
		if runCalls != 1 {
			t.Fatalf("second pass: whatsnewRun calls = %d, want 1 (Screen 0 must show once)", runCalls)
		}
	})

	t.Run("non-interactive never reaches the hook", func(t *testing.T) {
		stubWhatsnewSeams(t)

		origBare := dashboardIsBareInvocation
		origInteractive := dashboardIsInteractive
		dashboardIsBareInvocation = func() bool { return true }
		dashboardIsInteractive = func() bool { return false }
		t.Cleanup(func() {
			dashboardIsBareInvocation = origBare
			dashboardIsInteractive = origInteractive
		})

		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}

		_, handled := maybeRunDashboard(context.Background(), &cli.Args{}, nil, "0.30.0")
		if handled {
			t.Fatalf("non-interactive launch must not be handled by the dashboard")
		}
		if runCalls != 0 {
			t.Fatalf("Screen 0 flow reached in a non-interactive context (calls=%d)", runCalls)
		}
	})
}

// TestInstallSeedsWhatsnew asserts the fresh-install seed (STATE-03): the helper
// resolves the base via detectedBaseDirOrFallback and seeds version.String() through
// the shared whatsnewSaveSeen seam, and never seeds an empty base.
func TestInstallSeedsWhatsnew(t *testing.T) {
	t.Run("seeds resolved base and normalized version", func(t *testing.T) {
		stubWhatsnewSeams(t)

		wantBase, _ := detectedBaseDirOrFallback()

		var (
			calls   int
			gotBase string
			gotVer  string
		)
		whatsnewSaveSeen = func(baseDir, v string) error {
			calls++
			gotBase = baseDir
			gotVer = v
			return nil
		}

		seedWhatsnewOnInstallSuccess()

		if calls != 1 {
			t.Fatalf("whatsnewSaveSeen calls = %d, want 1", calls)
		}
		if gotBase != wantBase {
			t.Fatalf("seed base = %q, want %q (detectedBaseDirOrFallback)", gotBase, wantBase)
		}
		if gotVer != version.String() {
			t.Fatalf("seed version = %q, want %q (version.String)", gotVer, version.String())
		}
		if gotVer == "" {
			t.Fatal("seed version must never be empty (Pitfall 4)")
		}
	})

	t.Run("best-effort: a seed error never propagates", func(t *testing.T) {
		stubWhatsnewSeams(t)
		whatsnewSaveSeen = func(baseDir, v string) error {
			return errors.New("write failed")
		}
		// The helper returns nothing and must not panic; the install exit code is
		// unaffected because the error is discarded.
		seedWhatsnewOnInstallSuccess()
	})
}

// TestSeedPathEqualsReadPath closes open question A1: the install seed writes exactly
// where the dashboard reads. MarkSeen(base, v) must produce a file at
// whatsnew.StatePath(base) that LoadState(base) reads back.
func TestSeedPathEqualsReadPath(t *testing.T) {
	base := t.TempDir()
	const seeded = "0.30.0"

	if err := whatsnew.MarkSeen(base, seeded); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
		t.Fatalf("write-path missing: os.Stat(%s): %v", whatsnew.StatePath(base), err)
	}
	st, present, err := whatsnew.LoadState(base)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !present {
		t.Fatal("read-path: LoadState reports the flag absent after MarkSeen")
	}
	if st.LastSeenNotesVersion != seeded {
		t.Fatalf("read-path value = %q, want %q", st.LastSeenNotesVersion, seeded)
	}
}
