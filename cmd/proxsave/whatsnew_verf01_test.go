package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// TestVERF01 is the consolidated end-to-end verification of the "what's new" feature over
// the REAL gate (Decide/ShouldWarn/MarkSeen/LoadState/StatePath) and the two wirings. Four
// scenarios must hold together in one session:
//   1. a fresh install (seeded to current) shows nothing on either channel;
//   2. a real upgrader (absent flag) shows Screen 0 exactly once, then goes silent;
//   3. a non-interactive run emits exactly one captured WARNING WITHOUT writing the flag,
//      so the backup state is untouched and the warning re-fires on every run;
//   4. a timeout/Esc leaves the flag unwritten, so ShouldWarn still reports unseen.
//
// The live on-TTY paint of Screen 0 and the real --backup channel delivery stay manual UAT
// (03-VALIDATION.md); everything gate/wiring is verified here.
func TestVERF01(t *testing.T) {
	t.Run("fresh_install_silent", func(t *testing.T) {
		base := t.TempDir()
		if err := whatsnew.MarkSeen(base, "0.30.0"); err != nil { // STATE-03 install seed
			t.Fatalf("MarkSeen (install seed): %v", err)
		}
		if show, body, err := whatsnew.Decide(base, "0.30.0"); show || body != "" || err != nil {
			t.Fatalf("Decide after fresh seed = (%v, %q, %v), want (false, \"\", nil)", show, body, err)
		}
		if show, ver, err := whatsnew.ShouldWarn(base, "0.30.0"); show || ver != "" || err != nil {
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

		maybeShowWhatsnew(context.Background(), nil, base, current)
		if runCalls != 1 {
			t.Fatalf("first pass: whatsnewRun calls = %d, want 1", runCalls)
		}
		if _, err := os.Stat(whatsnew.StatePath(base)); err != nil {
			t.Fatalf("first pass: seen flag not written: %v", err)
		}

		maybeShowWhatsnew(context.Background(), nil, base, current)
		if runCalls != 1 {
			t.Fatalf("second pass: whatsnewRun calls = %d, want 1 (Screen 0 shows once)", runCalls)
		}
		if show, ver, err := whatsnew.ShouldWarn(base, current); show || ver != "" || err != nil {
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
			maybeWarnWhatsnew(logger, base, "0.30.0")
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

	t.Run("timeout_esc_flag_not_cleared_warning_persists", func(t *testing.T) {
		cases := []struct {
			name   string
			runErr error
		}{
			{"timeout", context.DeadlineExceeded},
			{"esc", shell.ErrAborted},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				stubWhatsnewSeams(t) // Decide + SaveSeen stay REAL; only whatsnewRun is stubbed
				base := t.TempDir()  // absent flag
				whatsnewRun = func(ctx context.Context, session *shell.Session, body string) error {
					return tc.runErr
				}

				maybeShowWhatsnew(context.Background(), nil, base, "0.30.0")

				if _, err := os.Stat(whatsnew.StatePath(base)); !os.IsNotExist(err) {
					t.Fatalf("%s wrote/cleared the flag; StatePath err = %v, want not-exist", tc.name, err)
				}
				if show, ver, err := whatsnew.ShouldWarn(base, "0.30.0"); !show || ver != "0.30.0" || err != nil {
					t.Fatalf("ShouldWarn after %s = (%v, %q, %v), want (true, \"0.30.0\", nil)", tc.name, show, ver, err)
				}
			})
		}
	})
}
