package logging

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// captureStdout / captureStderr run fn with the corresponding standard stream
// redirected to a pipe and return what was written. The bootstrap console
// methods read os.Stdout/os.Stderr at call time, so redirection here also makes
// consoleUseColor detect a non-terminal (pipe) and emit deterministic no-color
// output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	data, _ := io.ReadAll(r)
	return string(data)
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = orig
	data, _ := io.ReadAll(r)
	return string(data)
}

func TestFormatConsoleLogLineNoColor(t *testing.T) {
	got := FormatConsoleLogLine("2026-07-07 20:55:04", types.LogLevelInfo, "hello world", false)
	want := "[2026-07-07 20:55:04] INFO     hello world\n"
	if got != want {
		t.Fatalf("no-color mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestFormatConsoleLogLineColorWrapsLevel(t *testing.T) {
	got := FormatConsoleLogLine("2026-07-07 20:55:04", types.LogLevelInfo, "hi", true)
	// Green code wraps the padded level, then reset, then a separator space.
	want := "[2026-07-07 20:55:04] \033[32mINFO    \033[0m hi\n"
	if got != want {
		t.Fatalf("color mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestFormatConsoleLogLinePaddingPerLevel(t *testing.T) {
	cases := []struct {
		level  types.LogLevel
		padded string // level padded to width 8 (%-8s)
		color  string
	}{
		{types.LogLevelDebug, "DEBUG   ", "\033[36m"},
		{types.LogLevelInfo, "INFO    ", "\033[32m"},
		{types.LogLevelWarning, "WARNING ", "\033[33m"},
		{types.LogLevelError, "ERROR   ", "\033[31m"},
		{types.LogLevelCritical, "CRITICAL", "\033[1;31m"},
	}
	for _, c := range cases {
		t.Run(c.level.String(), func(t *testing.T) {
			gotNC := FormatConsoleLogLine("TS", c.level, "m", false)
			wantNC := "[TS] " + c.padded + " m\n"
			if gotNC != wantNC {
				t.Fatalf("no-color: got %q want %q", gotNC, wantNC)
			}
			gotC := FormatConsoleLogLine("TS", c.level, "m", true)
			wantC := "[TS] " + c.color + c.padded + "\033[0m m\n"
			if gotC != wantC {
				t.Fatalf("color: got %q want %q", gotC, wantC)
			}
		})
	}
}

// TestMainLoggerConsoleLineMatchesFormatter proves the main Logger's stdout line
// is byte-identical to FormatConsoleLogLine (they share the same assembler, so
// the two formats can never drift).
func TestMainLoggerConsoleLineMatchesFormatter(t *testing.T) {
	for _, useColor := range []bool{false, true} {
		var buf bytes.Buffer
		logger := New(types.LogLevelInfo, useColor)
		logger.SetOutput(&buf)
		logger.Info("payload %d", 7)

		got := buf.String()
		if len(got) < 21 || got[0] != '[' || got[20] != ']' {
			t.Fatalf("unexpected line shape (useColor=%v): %q", useColor, got)
		}
		ts := got[1:20] // the 19-char timestamp between "[" and "]"
		want := FormatConsoleLogLine(ts, types.LogLevelInfo, "payload 7", useColor)
		if got != want {
			t.Fatalf("main logger drifted from formatter (useColor=%v):\n got %q\nwant %q", useColor, got, want)
		}
	}
}

func TestBootstrapInfoPrintsPrefixedLineToStdout(t *testing.T) {
	b := NewBootstrapLogger()
	msg := "Global proxsave/proxmox-backup entrypoint scan: removed=0 kept=1"
	out := captureStdout(t, func() { b.Info("%s", msg) })

	if len(out) < 21 || out[0] != '[' || out[20] != ']' {
		t.Fatalf("bootstrap Info must print a timestamp prefix, got %q", out)
	}
	want := FormatConsoleLogLine(out[1:20], types.LogLevelInfo, msg, false)
	if out != want {
		t.Fatalf("bootstrap Info line mismatch:\n got %q\nwant %q", out, want)
	}
}

func TestBootstrapWarningAndErrorPrintPrefixedToStderr(t *testing.T) {
	b := NewBootstrapLogger()

	wOut := captureStderr(t, func() { b.Warning("%s", "heads up") })
	if len(wOut) < 21 || wOut[0] != '[' {
		t.Fatalf("Warning must print a timestamp prefix, got %q", wOut)
	}
	if want := FormatConsoleLogLine(wOut[1:20], types.LogLevelWarning, "heads up", false); wOut != want {
		t.Fatalf("Warning stderr mismatch:\n got %q\nwant %q", wOut, want)
	}

	eOut := captureStderr(t, func() { b.Error("%s", "boom") })
	if len(eOut) < 21 || eOut[0] != '[' {
		t.Fatalf("Error must print a timestamp prefix, got %q", eOut)
	}
	if want := FormatConsoleLogLine(eOut[1:20], types.LogLevelError, "boom", false); eOut != want {
		t.Fatalf("Error stderr mismatch:\n got %q\nwant %q", eOut, want)
	}
}

func TestBootstrapPrintlnStaysRaw(t *testing.T) {
	b := NewBootstrapLogger()
	out := captureStdout(t, func() { b.Println("=== banner line ===") })
	if out != "=== banner line ===\n" {
		t.Fatalf("Println must stay raw (no prefix), got %q", out)
	}
}

func TestBootstrapPrintfStaysRaw(t *testing.T) {
	b := NewBootstrapLogger()
	out := captureStdout(t, func() { b.Printf("banner %d", 42) })
	if out != "banner 42\n" {
		t.Fatalf("Printf must stay raw (no prefix), got %q", out)
	}
}

// TestBootstrapRecordsRawAndFlushesSinglePrefix proves the recorded entry stays
// RAW so Flush re-formats it through the main logger exactly once (no double
// prefix).
func TestBootstrapRecordsRawAndFlushesSinglePrefix(t *testing.T) {
	b := NewBootstrapLogger()
	b.SetConsoleQuiet(true) // don't touch the real console; only exercise recording
	b.Info("single install line")

	b.mu.Lock()
	n := len(b.entries)
	var rec string
	if n == 1 {
		rec = b.entries[0].message
	}
	b.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected exactly 1 recorded entry, got %d", n)
	}
	if rec != "single install line" {
		t.Fatalf("recorded entry must stay RAW (no prefix), got %q", rec)
	}

	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	b.Flush(logger)

	got := buf.String()
	if strings.Count(got, "single install line") != 1 {
		t.Fatalf("flush must emit the message exactly once, got %q", got)
	}
	if strings.Count(got, "] INFO") != 1 {
		t.Fatalf("flush must produce exactly one INFO prefix, got %q", got)
	}
	if strings.Contains(got, "INFO     [") {
		t.Fatalf("double prefix detected in flush output: %q", got)
	}
}
