package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestMaybeShowWhatsnewSelfHeal (STATE-06): a corrupt seen-flag on the interactive path
// self-heals (quarantine to .corrupt + re-seed last_seen=current) and stays silent (no
// Screen 0); any NON-parse Decide error writes nothing, so a real IO fault is not masked.
func TestMaybeShowWhatsnewSelfHeal(t *testing.T) {
	t.Run("corrupt flag re-seeds and stays silent", func(t *testing.T) {
		stubWhatsnewSeams(t) // restore the real seams after; only whatsnewRun is spied
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(whatsnew.StatePath(base)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(whatsnew.StatePath(base), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write garbage: %v", err)
		}
		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}
		// whatsnewDecide and whatsnewSaveSeen stay REAL, so the real ErrStateParse -> MarkSeen
		// self-heal path is exercised end to end.

		maybeShowWhatsnew(context.Background(), nil, base, "0.30.0")

		if runCalls != 0 {
			t.Fatalf("whatsnewRun called %d times on a corrupt flag, want 0 (no Screen 0)", runCalls)
		}
		st, present, err := whatsnew.LoadState(base)
		if err != nil {
			t.Fatalf("LoadState after self-heal: %v", err)
		}
		if !present || st.LastSeenNotesVersion != "0.30.0" {
			t.Fatalf("self-heal state = (present=%v, %q), want (true, \"0.30.0\")", present, st.LastSeenNotesVersion)
		}
		if _, err := os.Stat(whatsnew.StatePath(base) + ".corrupt"); err != nil {
			t.Fatalf("expected .corrupt sidecar: %v", err)
		}
	})

	t.Run("non-parse error does not write", func(t *testing.T) {
		stubWhatsnewSeams(t)
		whatsnewDecide = func(baseDir, curr string) (bool, string, error) {
			return false, "", errors.New("io")
		}
		saveCalls := 0
		whatsnewSaveSeen = func(baseDir, v string) error {
			saveCalls++
			return nil
		}
		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}

		maybeShowWhatsnew(context.Background(), nil, t.TempDir(), "0.30.0")

		if saveCalls != 0 {
			t.Fatalf("whatsnewSaveSeen called %d times on a non-parse error, want 0", saveCalls)
		}
		if runCalls != 0 {
			t.Fatalf("whatsnewRun called %d times on a non-parse error, want 0", runCalls)
		}
	})
}

// TestMaybeWarnWhatsnewSelfHeal (STATE-06): a corrupt seen-flag on the non-interactive path
// self-heals and emits no WARNING (only a bare-fact DEBUG self-heal line); a NON-parse gate
// error writes nothing and keeps the generic gate-error DEBUG line.
func TestMaybeWarnWhatsnewSelfHeal(t *testing.T) {
	t.Run("corrupt flag re-seeds and stays silent", func(t *testing.T) {
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(whatsnew.StatePath(base)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(whatsnew.StatePath(base), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write garbage: %v", err)
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, base, "0.30.0") // real ShouldWarn + real MarkSeen

		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d on a corrupt flag, want 0", got)
		}
		st, present, err := whatsnew.LoadState(base)
		if err != nil {
			t.Fatalf("LoadState after self-heal: %v", err)
		}
		if !present || st.LastSeenNotesVersion != "0.30.0" {
			t.Fatalf("self-heal state = (present=%v, %q), want (true, \"0.30.0\")", present, st.LastSeenNotesVersion)
		}
		if _, err := os.Stat(whatsnew.StatePath(base) + ".corrupt"); err != nil {
			t.Fatalf("expected .corrupt sidecar: %v", err)
		}
		if !strings.Contains(buf.String(), "self-healed") {
			t.Fatalf("missing self-heal DEBUG line\n%s", buf.String())
		}
	})

	t.Run("non-parse error does not write", func(t *testing.T) {
		stubWhatsnewSeams(t)
		stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
			return false, "", errors.New("io")
		})
		saveCalls := 0
		whatsnewSaveSeen = func(baseDir, v string) error {
			saveCalls++
			return nil
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, t.TempDir(), "0.30.0")

		if saveCalls != 0 {
			t.Fatalf("whatsnewSaveSeen called %d times on a non-parse error, want 0", saveCalls)
		}
		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d, want 0", got)
		}
		if !strings.Contains(buf.String(), "gate error") {
			t.Fatalf("missing generic gate-error DEBUG line\n%s", buf.String())
		}
	})
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
