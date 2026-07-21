package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// stubWhatsnewShouldWarn saves and restores the whatsnewShouldWarn seam so a test can
// drive the gate verdict without real disk or a GitHub fetch, and leak nothing.
func stubWhatsnewShouldWarn(t *testing.T, fn func(baseDir, current string) (bool, string, error)) {
	t.Helper()
	orig := whatsnewShouldWarn
	t.Cleanup(func() { whatsnewShouldWarn = orig })
	whatsnewShouldWarn = fn
}

// captureLogger builds a Debug-level logger writing into a fresh buffer, so a test can
// assert both WarningCount and the emitted line text.
func captureLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)
	return logger, buf
}

// singleEmittedWarning asserts the logger captured EXACTLY ONE warning/error issue line and
// returns its message (the text after the level token). IssueLines uses the fixed
// "[ts] LEVEL msg" capture format, so this pins the copy with an equality check that Contains
// cannot give: a trailing or leading addition to the format string fails it.
func singleEmittedWarning(t *testing.T, logger *logging.Logger) string {
	t.Helper()
	lines := logger.IssueLines()
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 captured issue line, got %d: %q", len(lines), lines)
	}
	i := strings.Index(lines[0], "WARNING")
	if i < 0 {
		t.Fatalf("captured issue is not a WARNING: %q", lines[0])
	}
	return strings.TrimSpace(lines[0][i+len("WARNING"):])
}

const lockedWarnCopy = "ProxSave 0.30.0 has unseen release notes. Open proxsave to view the new features."

// TestMaybeWarnWhatsnewUnseen: an unseen verdict emits EXACTLY ONE WARNING (the locked
// copy) and the buffer carries it, bracketed by DEBUG lines.
func TestMaybeWarnWhatsnewUnseen(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 1 {
		t.Fatalf("WarningCount = %d, want 1", got)
	}
	if got := singleEmittedWarning(t, logger); got != lockedWarnCopy {
		t.Fatalf("emitted warning = %q, want exactly the locked copy %q", got, lockedWarnCopy)
	}
	if !strings.Contains(buf.String(), "Release notes check done") {
		t.Fatalf("missing DEBUG close bracket in buffer\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewSeen: a seen verdict emits no WARNING and a bare-fact DEBUG line.
func TestMaybeWarnWhatsnewSeen(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return false, "", nil
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 0 {
		t.Fatalf("WarningCount = %d, want 0", got)
	}
	if strings.Contains(buf.String(), lockedWarnCopy) {
		t.Fatalf("seen verdict must not emit the WARNING copy\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "already seen") {
		t.Fatalf("seen verdict missing the bare-fact DEBUG line\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewGateError: a gate error fails toward silence (no WARNING) and emits
// a bare-fact DEBUG skip line carrying the error.
func TestMaybeWarnWhatsnewGateError(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return false, "", errors.New("boom")
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 0 {
		t.Fatalf("WarningCount = %d, want 0 (fail toward silence)", got)
	}
	if strings.Contains(buf.String(), lockedWarnCopy) {
		t.Fatalf("gate error must not emit the WARNING copy\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("gate error DEBUG line missing the error text\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewCopy: the emitted WARNING equals the locked single line with the
// normalized version and is pure ASCII (no em dash U+2014, no en dash U+2013, no emoji).
func TestMaybeWarnWhatsnewCopy(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	logger, _ := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	got := singleEmittedWarning(t, logger)
	if got != lockedWarnCopy {
		t.Fatalf("emitted warning = %q, want EXACTLY the locked copy %q (Contains is insufficient: additions must fail)", got, lockedWarnCopy)
	}
	if i := strings.IndexFunc(got, func(r rune) bool { return r > 127 }); i != -1 {
		t.Fatalf("emitted warning carries a non-ASCII rune at %d: %q", i, got)
	}
}

// TestMaybeWarnWhatsnewNilLogger: a nil logger is a no-op and must not panic.
func TestMaybeWarnWhatsnewNilLogger(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	// Must not panic.
	maybeWarnWhatsnew(nil, "/base", "0.30.0")
}

// TestMaybeWarnWhatsnewDeliveredToEmailCategories exercises the REAL gate (no stub) and the
// REAL run-log FILE sink, then parses that file exactly as the backup completion path does,
// proving the WARNING is captured into a notify.LogCategory (the email/webhook surface). This
// closes the console-only gap: the other tests assert the in-memory console buffer, not the
// file sink that ParseLogCounts reads. It also integrates the real ShouldWarn v-strip
// (v0.30.0 -> 0.30.0) into the emitted copy.
func TestMaybeWarnWhatsnewDeliveredToEmailCategories(t *testing.T) {
	base := t.TempDir() // no identity/.whatsnew_seen.json -> unseen upgrader
	logPath := filepath.Join(t.TempDir(), "run.log")
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{}) // silence the console sink; we read the file sink
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}

	maybeWarnWhatsnew(logger, base, "v0.30.0") // real ShouldWarn; v-prefix exercises the v-strip

	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile: %v", err)
	}

	cats, _, warningCount, _ := orchestrator.ParseLogCounts(logPath, 10)
	if warningCount != 1 {
		t.Fatalf("ParseLogCounts warningCount = %d, want 1", warningCount)
	}
	found := false
	for _, c := range cats {
		if c.Type == "WARNING" && c.Label == lockedWarnCopy {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("locked warning copy not captured as a WARNING LogCategory: %+v", cats)
	}
}

// TestWhatsnewWarnWiredAfterUpdateCheck is a source guard for the single wiring line. The
// behavioral call lives inside bootstrapRuntime, which cannot be unit-invoked cheaply (it runs
// a real GitHub update probe and a full config load), so this cheaply catches accidental
// removal or relocation: the nudge MUST be called, and AFTER checkForUpdates (co-located with
// the update notice, NOTF-01), never before it or wrapped in an "update available" branch.
func TestWhatsnewWarnWiredAfterUpdateCheck(t *testing.T) {
	src, err := os.ReadFile("main_runtime.go")
	if err != nil {
		t.Fatalf("read main_runtime.go: %v", err)
	}
	s := string(src)
	iUpd := strings.Index(s, "checkForUpdates(")
	iWarn := strings.Index(s, "maybeWarnWhatsnew(")
	if iUpd < 0 {
		t.Fatal("checkForUpdates call not found in main_runtime.go")
	}
	if iWarn < 0 {
		t.Fatal("maybeWarnWhatsnew wiring missing from main_runtime.go (NOTF-01 delivery removed)")
	}
	if iWarn < iUpd {
		t.Fatal("maybeWarnWhatsnew must be wired AFTER checkForUpdates (co-located with the update notice)")
	}
}
