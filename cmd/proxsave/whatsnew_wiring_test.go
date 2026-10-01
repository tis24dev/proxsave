package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/version"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// testWhatsnewLoc is the seen-flag Location the cmd tests use: LOG_PATH and BASE_DIR on the
// same directory and no timeout, so whatsnew.StatePath(base) is the flag and base/identity/
// the location an older release used.
func testWhatsnewLoc(base string) whatsnew.Location {
	return whatsnew.Location{LogPath: base, BaseDir: base}
}

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

// The seen-flag is written EXACTLY ONCE once the screen has been shown, however the
// operator left it. The old contract wrote only on an explicit continue, so reading
// the notes and closing with Esc or Ctrl+C left the flag unwritten and the next
// scheduled backup logged a WARNING that ParseLogCounts counted and
// applyIssueExitCode promoted to exit 1, which the daemon reports to Healthchecks as
// down (issue #305).
//
// Every resolution except the timeout is a KEYSTROKE, which is the evidence that a
// person was there. isTerminalInteractive only proves a terminal: a detached tmux
// window, an expect script or an `ssh -t` from a wrapper all carry a real TTY, and
// there the screen would sit untouched until the 10-minute timeout. That one does
// not write, and neither does a torn-down parent (TestMaybeShowWhatsnewTimeout).
func TestScreen0WritesOnceHoweverTheScreenIsClosed(t *testing.T) {
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
		{"esc writes: the pager resolves it as its own abort sentinel", shell.ErrAborted, 1},
		// The real ctrl+c value, not a bare ErrClosed: the router turns the key into
		// tea.Interrupt, Program.Run wraps it as ErrProgramKilled, and Ask resolves
		// through Session.closedErr. A bare ErrClosed is a different fact and is
		// covered by its own row below.
		{"ctrl+c writes: a person closed the screen", shell.ClosedByInterrupt(), 1},
		// A UI that died. It reaches whatsnewRender through the SAME ErrClosed door as
		// ctrl+c, and writing for it marked the notes seen for an operator who never
		// got to read them - permanently, since the version does not change again.
		{"a dead UI does NOT write: nobody read anything", shell.ClosedByUIFailure(errors.New("read /dev/tty: input/output error")), 0},
		// Bare ErrClosed is Session.closedErr with no program error at all: the program
		// quit cleanly while an Ask was still pending, so something else tore the
		// session down and the screen was never resolved by anyone.
		{"a session closed with no error does NOT write: nothing resolved the screen", shell.ErrClosed, 0},
		{"the 10-minute timeout does NOT write: a TTY is not a person", context.DeadlineExceeded, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubWhatsnewSeams(t)

			whatsnewDecide = func(loc whatsnew.Location, curr string) (bool, string, error) {
				return true, "body", nil
			}
			whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
				return tc.runErr
			}
			var (
				calls  int
				gotLoc whatsnew.Location
				gotVer string
			)
			whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
				calls++
				gotLoc = loc
				gotVer = v
				return nil
			}

			maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), current)

			if calls != tc.wantCalls {
				t.Fatalf("whatsnewSaveSeen calls = %d, want %d", calls, tc.wantCalls)
			}
			if tc.wantCalls == 1 {
				if gotLoc != testWhatsnewLoc(base) || gotVer != current {
					t.Fatalf("write args = (%+v, %q), want (%+v, %q)", gotLoc, gotVer, testWhatsnewLoc(base), current)
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

			whatsnewDecide = func(loc whatsnew.Location, curr string) (bool, string, error) {
				return false, "", tc.decideErr
			}
			runCalls := 0
			whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
				runCalls++
				return nil
			}
			saveCalls := 0
			whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
				saveCalls++
				return nil
			}

			maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc("/tmp/whatsnew-base"), "0.30.0")

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

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.30.0")

		if runCalls != 0 {
			t.Fatalf("whatsnewRun called %d times on a corrupt flag, want 0 (no Screen 0)", runCalls)
		}
		st, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
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

	t.Run("valid JSON non-semver version re-seeds and stays silent", func(t *testing.T) {
		stubWhatsnewSeams(t) // only whatsnewRun is spied; real Decide + real MarkSeen
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(whatsnew.StatePath(base)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// Syntactically valid JSON, but the stored version is not semver: LoadState now
		// surfaces ErrStateParse, so the wiring self-heals instead of silencing forever.
		if err := os.WriteFile(whatsnew.StatePath(base), []byte(`{"last_seen_notes_version":"garbage"}`), 0o644); err != nil {
			t.Fatalf("write bad-version flag: %v", err)
		}
		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.30.0")

		if runCalls != 0 {
			t.Fatalf("whatsnewRun called %d times on a non-semver flag, want 0 (no Screen 0)", runCalls)
		}
		st, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
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
		whatsnewDecide = func(loc whatsnew.Location, curr string) (bool, string, error) {
			return false, "", errors.New("io")
		}
		saveCalls := 0
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			saveCalls++
			return nil
		}
		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(t.TempDir()), "0.30.0")

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

		maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.30.0", false) // real ShouldWarn + real MarkSeen

		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d on a corrupt flag, want 0", got)
		}
		st, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
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
		stubWhatsnewShouldWarn(t, func(loc whatsnew.Location, current string) (bool, string, error) {
			return false, "", errors.New("io")
		})
		saveCalls := 0
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			saveCalls++
			return nil
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(t.TempDir()), "0.30.0", false)

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

	t.Run("valid JSON non-semver version re-seeds and stays silent", func(t *testing.T) {
		base := t.TempDir()
		if err := os.MkdirAll(filepath.Dir(whatsnew.StatePath(base)), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(whatsnew.StatePath(base), []byte(`{"last_seen_notes_version":"garbage"}`), 0o644); err != nil {
			t.Fatalf("write bad-version flag: %v", err)
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.30.0", false) // real ShouldWarn + real MarkSeen

		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d on a non-semver flag, want 0", got)
		}
		st, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
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

	t.Run("self-heal write failure logs the failure line, no false success", func(t *testing.T) {
		stubWhatsnewSeams(t)
		stubWhatsnewShouldWarn(t, func(loc whatsnew.Location, current string) (bool, string, error) {
			return false, "", whatsnew.ErrStateParse // corrupt flag path
		})
		saveCalls := 0
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			saveCalls++
			return errors.New("write failed") // read-only identity dir
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(t.TempDir()), "0.30.0", false)

		if saveCalls != 1 {
			t.Fatalf("whatsnewSaveSeen calls = %d, want 1 (self-heal attempted)", saveCalls)
		}
		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d, want 0", got)
		}
		if !strings.Contains(buf.String(), "self-heal write failed") {
			t.Fatalf("missing write-failure DEBUG line\n%s", buf.String())
		}
		if strings.Contains(buf.String(), "self-healed") {
			t.Fatalf("must not claim self-healed when the write failed\n%s", buf.String())
		}
	})

	t.Run("dry-run skips the self-heal write", func(t *testing.T) {
		stubWhatsnewSeams(t)
		stubWhatsnewShouldWarn(t, func(loc whatsnew.Location, current string) (bool, string, error) {
			return false, "", whatsnew.ErrStateParse // corrupt flag path
		})
		saveCalls := 0
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			saveCalls++
			return nil
		}
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(t.TempDir()), "0.30.0", true) // dry-run: must not mutate the FS

		if saveCalls != 0 {
			t.Fatalf("whatsnewSaveSeen called %d times under dry-run, want 0 (no FS mutation)", saveCalls)
		}
		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d, want 0", got)
		}
		if !strings.Contains(buf.String(), "skipped (dry-run)") {
			t.Fatalf("missing dry-run skip DEBUG line\n%s", buf.String())
		}
	})
}

// TestMaybeShowWhatsnewTimeout (SCRN-04): Screen 0 runs under a dedicated total
// whatsnewScreenTimeout, and a cancelled/timed-out run leaves the seen-flag unwritten.
func TestMaybeShowWhatsnewTimeout(t *testing.T) {
	t.Run("dedicated 10-minute total deadline applied", func(t *testing.T) {
		stubWhatsnewSeams(t)
		whatsnewDecide = func(loc whatsnew.Location, curr string) (bool, string, error) {
			return true, "body", nil
		}
		var (
			gotDeadline time.Time
			gotOK       bool
		)
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			gotDeadline, gotOK = ctx.Deadline()
			return nil
		}
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error { return nil } // no real disk write

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc("/tmp/whatsnew-timeout"), "0.30.0")

		if !gotOK {
			t.Fatal("Screen 0 run context carries no deadline; want a total whatsnewScreenTimeout cap")
		}
		// Assert against HARDCODED literals (not the production const): pin the absolute
		// 10-minute value so a const change to another duration is caught here, instead of
		// a self-referential bound that tracks whatever the const becomes.
		if whatsnewScreenTimeout != 10*time.Minute {
			t.Fatalf("whatsnewScreenTimeout = %v, want 10m0s (SCRN-04 total cap)", whatsnewScreenTimeout)
		}
		remaining := time.Until(gotDeadline)
		if remaining <= 9*time.Minute || remaining > 10*time.Minute {
			t.Fatalf("Screen 0 deadline remaining = %v, want in (9m0s, 10m0s]", remaining)
		}
	})

	t.Run("cancelled parent leaves the flag unwritten", func(t *testing.T) {
		stubWhatsnewSeams(t)
		whatsnewDecide = func(loc whatsnew.Location, curr string) (bool, string, error) {
			return true, "body", nil
		}
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			return ctx.Err()
		}
		saveCalls := 0
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			saveCalls++
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		maybeShowWhatsnew(ctx, nil, testWhatsnewLoc("/tmp/whatsnew-timeout"), "0.30.0")

		if saveCalls != 0 {
			t.Fatalf("whatsnewSaveSeen called %d times on a cancelled run, want 0", saveCalls)
		}
	})
}

// TestScreen0UsesDedicatedTimeout is a STRUCTURAL (AST) guard that Screen 0 is bounded by the
// DEDICATED whatsnewScreenTimeout, NOT the shared withDashboardIdle. A behavioral deadline test
// cannot tell the two apart today (dashboardIdleTimeout == whatsnewScreenTimeout == 10m), so this
// pins SCRN-04's intent -- a knob distinct from the menu/sub-screen idle cap -- against a silent
// revert to withDashboardIdle or a future change to dashboardIdleTimeout. Mirrors the AST idiom of
// TestWhatsnewWarnWiredInBootstrap.
func TestScreen0UsesDedicatedTimeout(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "dashboard.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dashboard.go: %v", err)
	}
	// The render + timeout live in whatsnewRender (shared by maybeShowWhatsnew and the
	// standalone showWhatsnewScreen), so the dedicated-cap guard inspects that helper.
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "whatsnewRender" {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("whatsnewRender not found in dashboard.go")
	}

	usesDedicated, usesSharedIdle := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr: // context.WithTimeout(ctx, whatsnewScreenTimeout)
			if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "context" && fun.Sel.Name == "WithTimeout" {
				if len(call.Args) == 2 {
					if id, ok := call.Args[1].(*ast.Ident); ok && id.Name == "whatsnewScreenTimeout" {
						usesDedicated = true
					}
				}
			}
		case *ast.Ident: // withDashboardIdle(ctx)
			if fun.Name == "withDashboardIdle" {
				usesSharedIdle = true
			}
		}
		return true
	})

	if !usesDedicated {
		t.Fatal("whatsnewRender must bound Screen 0 with context.WithTimeout(ctx, whatsnewScreenTimeout) (SCRN-04 dedicated cap)")
	}
	if usesSharedIdle {
		t.Fatal("whatsnewRender must NOT use the shared withDashboardIdle for Screen 0 (SCRN-04 requires a dedicated timeout distinct from the menu idle cap)")
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
		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), current)
		if runCalls != 1 {
			t.Fatalf("first pass: whatsnewRun calls = %d, want 1", runCalls)
		}
		if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
			t.Fatalf("first pass: seen flag not written at %s: %v", whatsnew.StatePath(base), err)
		}

		// Second pass: flag now present at current -> Decide returns show=false ->
		// the flow is never run again (shown exactly once).
		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), current)
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

// stubWhatsnewLoadConfig swaps the configuration loader behind whatsnewLocation and records
// the arguments it was called with.
func stubWhatsnewLoadConfig(t *testing.T, fn func(configPath, baseDir string) (*config.Config, error)) {
	t.Helper()
	orig := whatsnewLoadConfig
	t.Cleanup(func() { whatsnewLoadConfig = orig })
	whatsnewLoadConfig = fn
}

// TestInstallSeedsWhatsnew asserts the fresh-install seed (STATE-03): the helper
// resolves the base via detectedBaseDirOrFallback and LOG_PATH from the installed
// configuration, seeds version.String() through the shared whatsnewSaveSeen seam,
// and never seeds an empty base.
func TestInstallSeedsWhatsnew(t *testing.T) {
	t.Run("seeds resolved base and normalized version", func(t *testing.T) {
		stubWhatsnewSeams(t)

		wantBase, _ := detectedBaseDirOrFallback()
		const configPath = "/opt/proxsave/env/backup.env"
		var gotConfigPath string
		stubWhatsnewLoadConfig(t, func(cp, bd string) (*config.Config, error) {
			gotConfigPath = cp
			return &config.Config{LogPath: "/srv/proxsave-log", FsIoTimeoutSeconds: 7}, nil
		})

		var (
			calls  int
			gotLoc whatsnew.Location
			gotVer string
		)
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			calls++
			gotLoc = loc
			gotVer = v
			return nil
		}

		seedWhatsnewOnInstallSuccess(configPath)

		if calls != 1 {
			t.Fatalf("whatsnewSaveSeen calls = %d, want 1", calls)
		}
		if gotConfigPath != configPath {
			t.Fatalf("seed read config %q, want %q", gotConfigPath, configPath)
		}
		wantLoc := whatsnew.Location{LogPath: "/srv/proxsave-log", BaseDir: wantBase, Timeout: 7 * time.Second}
		if gotLoc != wantLoc {
			t.Fatalf("seed location = %+v, want %+v (LOG_PATH + FS_IO_TIMEOUT from the config, base from detectedBaseDirOrFallback)", gotLoc, wantLoc)
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
		stubWhatsnewLoadConfig(t, func(string, string) (*config.Config, error) {
			return &config.Config{LogPath: t.TempDir()}, nil
		})
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			return errors.New("write failed")
		}
		// The helper returns nothing and must not panic; the install exit code is
		// unaffected because the error is discarded.
		seedWhatsnewOnInstallSuccess("/opt/proxsave/env/backup.env")
	})

	t.Run("an unreadable configuration seeds nothing", func(t *testing.T) {
		stubWhatsnewSeams(t) // whatsnewSaveSeen stays the REAL MarkSeen
		stubWhatsnewLoadConfig(t, func(string, string) (*config.Config, error) {
			return nil, errors.New("configuration file not found")
		})
		var (
			gotLoc whatsnew.Location
			gotErr error
		)
		realSave := whatsnewSaveSeen
		whatsnewSaveSeen = func(loc whatsnew.Location, v string) error {
			gotLoc = loc
			gotErr = realSave(loc, v)
			return gotErr
		}

		seedWhatsnewOnInstallSuccess("/nonexistent/backup.env")

		if gotLoc.LogPath != "" {
			t.Fatalf("seed location carries LOG_PATH %q without a configuration, want none", gotLoc.LogPath)
		}
		if !errors.Is(gotErr, whatsnew.ErrNoLogPath) {
			t.Fatalf("seed without LOG_PATH: MarkSeen err = %v, want ErrNoLogPath (nothing written)", gotErr)
		}
	})
}

// TestSeedPathEqualsReadPath closes open question A1: the install seed writes exactly
// where the dashboard reads. MarkSeen(loc, v) must produce a file at
// whatsnew.StatePath(loc.LogPath) that LoadState(loc) reads back.
func TestSeedPathEqualsReadPath(t *testing.T) {
	base := t.TempDir()
	const seeded = "0.30.0"

	if err := whatsnew.MarkSeen(testWhatsnewLoc(base), seeded); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
		t.Fatalf("write-path missing: os.Stat(%s): %v", whatsnew.StatePath(base), err)
	}
	st, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
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
