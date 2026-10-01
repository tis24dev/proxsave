package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// TestVERF01 is the consolidated end-to-end verification of the "what's new" feature over
// the REAL gate (Decide/ShouldWarn/MarkSeen/LoadState/StatePath) and the two wirings. Five
// scenarios must hold together in one session:
//  1. a fresh install (seeded to current) shows nothing on either channel;
//  2. a real upgrader (absent flag) shows Screen 0 exactly once, then goes silent;
//  3. a non-interactive run emits exactly one captured WARNING WITHOUT writing the flag,
//     so the backup state is untouched and the warning re-fires on every run;
//  4. an auto-confirmed upgrade without a TTY skips Screen 0 and leaves the previous seen
//     version intact, so exactly one warning reaches notification accounting;
//  5. a timeout/Esc leaves the flag unwritten, so ShouldWarn still reports unseen.
//
// The live on-TTY paint of Screen 0 and the real --backup channel delivery stay manual UAT
// (03-VALIDATION.md); everything gate/wiring is verified here.
func TestVERF01(t *testing.T) {
	t.Run("fresh_install_silent", func(t *testing.T) {
		base := t.TempDir()
		if err := whatsnew.MarkSeen(testWhatsnewLoc(base), "0.30.0"); err != nil { // STATE-03 install seed
			t.Fatalf("MarkSeen (install seed): %v", err)
		}
		if show, body, err := whatsnew.Decide(testWhatsnewLoc(base), "0.30.0"); show || body != "" || err != nil {
			t.Fatalf("Decide after fresh seed = (%v, %q, %v), want (false, \"\", nil)", show, body, err)
		}
		if show, ver, err := whatsnew.ShouldWarn(testWhatsnewLoc(base), "0.30.0"); show || ver != "" || err != nil {
			t.Fatalf("ShouldWarn after fresh seed = (%v, %q, %v), want (false, \"\", nil)", show, ver, err)
		}
	})

	t.Run("upgrade_shows_once_then_silent", func(t *testing.T) {
		stubWhatsnewSeams(t) // only whatsnewRun is spied; Decide + SaveSeen stay REAL
		base := t.TempDir()  // absent flag = a real upgrader
		const current = "0.30.0"

		runCalls := 0
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			runCalls++
			return nil // continue
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), current)
		if runCalls != 1 {
			t.Fatalf("first pass: whatsnewRun calls = %d, want 1", runCalls)
		}
		if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
			t.Fatalf("first pass: seen flag not written: %v", err)
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), current)
		if runCalls != 1 {
			t.Fatalf("second pass: whatsnewRun calls = %d, want 1 (Screen 0 shows once)", runCalls)
		}
		if show, ver, err := whatsnew.ShouldWarn(testWhatsnewLoc(base), current); show || ver != "" || err != nil {
			t.Fatalf("ShouldWarn after continue = (%v, %q, %v), want (false, \"\", nil)", show, ver, err)
		}
	})

	t.Run("noninteractive_warning_fires_backup_unaffected", func(t *testing.T) {
		// warnOnce runs the REAL non-interactive warn path into a fresh run-log file and
		// returns the warning count ParseLogCounts derives (the email/webhook surface).
		warnOnce := func(t *testing.T, base string) int {
			t.Helper()
			logPath := filepath.Join(t.TempDir(), "run.log")
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&bytes.Buffer{}) // silence the console sink; read the file sink
			if err := logger.OpenLogFile(logPath); err != nil {
				t.Fatalf("OpenLogFile: %v", err)
			}
			maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.30.0", false)
			if err := logger.CloseLogFile(); err != nil {
				t.Fatalf("CloseLogFile: %v", err)
			}
			_, _, warningCount, _ := orchestrator.ParseLogCounts(logPath, 10)
			return warningCount
		}

		base := t.TempDir() // absent flag = unseen upgrader
		if got := warnOnce(t, base); got != 1 {
			t.Fatalf("first non-interactive run warningCount = %d, want 1", got)
		}
		// The warn path must NOT write the flag on a normal unseen flag: backup state stays
		// untouched and the warning keeps firing.
		if _, err := os.Stat(whatsnew.StatePath(base)); !os.IsNotExist(err) {
			t.Fatalf("non-interactive warn wrote the seen-flag; StatePath err = %v, want not-exist", err)
		}
		if got := warnOnce(t, base); got != 1 {
			t.Fatalf("second non-interactive run warningCount = %d, want 1 (warns every run)", got)
		}
	})

	t.Run("auto_yes_without_tty_leaves_healthchecks_warning_armed", func(t *testing.T) {
		origInteractive := whatsnewAfterUpgradeInteractive
		whatsnewAfterUpgradeInteractive = func() bool { return false }
		t.Cleanup(func() { whatsnewAfterUpgradeInteractive = origInteractive })

		base := t.TempDir()
		if err := whatsnew.MarkSeen(testWhatsnewLoc(base), "0.32.0"); err != nil {
			t.Fatalf("seed previous version: %v", err)
		}
		if shouldRunWhatsnewAfterUpgrade(&cli.Args{UpgradeAutoYes: true}, upgradeRunOptions{}) {
			t.Fatal("auto-confirmed no-TTY upgrade must not start Screen 0")
		}

		logPath := filepath.Join(t.TempDir(), "run.log")
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&bytes.Buffer{})
		if err := logger.OpenLogFile(logPath); err != nil {
			t.Fatalf("OpenLogFile: %v", err)
		}
		maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.33.0", false)
		if err := logger.CloseLogFile(); err != nil {
			t.Fatalf("CloseLogFile: %v", err)
		}

		_, _, warningCount, _ := orchestrator.ParseLogCounts(logPath, 10)
		if warningCount != 1 {
			t.Fatalf("warningCount = %d, want 1", warningCount)
		}
		state, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
		if err != nil || !present || state.LastSeenNotesVersion != "0.32.0" {
			t.Fatalf("seen state changed after unattended path: state=%+v present=%v err=%v", state, present, err)
		}
	})

	// End to end on the REAL state file: showing the screen disarms the warning
	// however it was closed. Before issue #305 only an explicit continue did, so an
	// operator who read the notes and pressed Esc kept getting a WARNING on every
	// scheduled backup, which ParseLogCounts counted and applyIssueExitCode promoted
	// to exit 1, reported to Healthchecks as down.
	t.Run("any_keystroke_that_closes_the_screen_disarms_the_warning", func(t *testing.T) {
		// Each row is the value whatsnewflow.Run REALLY returns for that keystroke,
		// not a stand-in that merely lands in the same branch today. continue comes
		// back nil from the resolved pager; Esc resolves the pager's abort sentinel
		// (internal/ui/components/pager.go:41); and Ctrl+C terminates the program, so
		// the pending Ask resolves through Session.closedErr - a bare shell.ErrClosed,
		// which stood here before, is a shape production never emits. The distinction
		// costs nothing while the rule saves on every non-timeout error, and it is the
		// whole test the day the rule is narrowed to specific resolutions.
		cases := []struct {
			name   string
			runErr error
		}{
			{"continue", nil},
			{"esc", shell.ErrAborted},
			{"ctrl+c", shell.ClosedByInterrupt()},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				stubWhatsnewSeams(t) // Decide + SaveSeen stay REAL; only whatsnewRun is stubbed
				base := t.TempDir()  // absent flag
				whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
					return tc.runErr
				}

				maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.30.0")

				state, present, err := whatsnew.LoadState(testWhatsnewLoc(base))
				if err != nil || !present || state.LastSeenNotesVersion != "0.30.0" {
					t.Fatalf("%s left the flag unwritten: state=%+v present=%v err=%v", tc.name, state, present, err)
				}
				if show, _, err := whatsnew.ShouldWarn(testWhatsnewLoc(base), "0.30.0"); show || err != nil {
					t.Fatalf("the warning survived a %s: ShouldWarn = (%v, %v)", tc.name, show, err)
				}
			})
		}
	})

	// The other side of the same rule. shell.Ask returns ErrClosed for a UI that DIED
	// as well as for ctrl+c, and the fallback saved the flag for both, so a renderer
	// failure or a terminal that stopped answering marked the notes seen for an
	// operator who never saw them - permanently, since the version does not change
	// again. Only a resolution that came from a person counts.
	t.Run("a_ui_that_died_does_not_disarm_the_warning", func(t *testing.T) {
		stubWhatsnewSeams(t)
		base := t.TempDir()
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			return shell.ClosedByUIFailure(errors.New("read /dev/tty: input/output error"))
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.30.0")

		if _, err := os.Stat(whatsnew.StatePath(base)); !os.IsNotExist(err) {
			t.Fatalf("the seen-flag was written after the UI died: the operator never saw the notes (stat err=%v)", err)
		}
		if show, _, err := whatsnew.ShouldWarn(testWhatsnewLoc(base), "0.30.0"); !show || err != nil {
			t.Fatalf("the warning was disarmed by a UI failure: ShouldWarn = (%v, %v)", show, err)
		}
	})

	// The 10-minute timeout stays armed. It is the only resolution that is not a
	// keystroke, and it is exactly what a detached tmux window or an `ssh -t` from a
	// wrapper produces: a real TTY with nobody in front of it, which
	// isTerminalInteractive cannot tell apart from a person.
	t.Run("the_screen_timing_out_leaves_the_warning_armed", func(t *testing.T) {
		stubWhatsnewSeams(t)
		base := t.TempDir()
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			return context.DeadlineExceeded
		}

		maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.30.0")

		if _, err := os.Stat(whatsnew.StatePath(base)); !os.IsNotExist(err) {
			t.Fatalf("an untouched screen wrote the flag; StatePath err = %v, want not-exist", err)
		}
	})

	// The one exit that still does not count: the PARENT was torn down, so the screen
	// never really ran. That is an external SIGINT or SIGTERM, not the operator
	// closing the screen; a Ctrl+C typed into the TUI never reaches this branch,
	// because the terminal is in raw mode and bubbletea reads it as a key.
	t.Run("a_torn_down_parent_leaves_the_warning_armed", func(t *testing.T) {
		stubWhatsnewSeams(t)
		base := t.TempDir()
		whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
			return ctx.Err()
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		maybeShowWhatsnew(ctx, nil, testWhatsnewLoc(base), "0.30.0")

		if _, err := os.Stat(whatsnew.StatePath(base)); !os.IsNotExist(err) {
			t.Fatalf("a cancelled parent wrote the flag; StatePath err = %v, want not-exist", err)
		}
		if show, ver, err := whatsnew.ShouldWarn(testWhatsnewLoc(base), "0.30.0"); !show || ver != "0.30.0" || err != nil {
			t.Fatalf("ShouldWarn = (%v, %q, %v), want (true, \"0.30.0\", nil)", show, ver, err)
		}
	})
}
