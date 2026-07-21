package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
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
	if !strings.Contains(buf.String(), lockedWarnCopy) {
		t.Fatalf("buffer missing locked warning copy\n%s", buf.String())
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
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	line := buf.String()
	if !strings.Contains(line, lockedWarnCopy) {
		t.Fatalf("emitted copy != locked line\n%s", line)
	}
	if strings.IndexFunc(lockedWarnCopy, func(r rune) bool { return r > 127 }) != -1 {
		t.Fatalf("locked warning copy is not pure ASCII")
	}
	if strings.IndexFunc(line, func(r rune) bool { return r > 127 }) != -1 {
		t.Fatalf("emitted warning line carries a non-ASCII rune\n%s", line)
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
