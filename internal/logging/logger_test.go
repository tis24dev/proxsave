package logging

import (
	"bytes"
	"strings"
	"testing"

	"os"
	"path/filepath"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestNew(t *testing.T) {
	logger := New(types.LogLevelInfo, true)

	if logger.level != types.LogLevelInfo {
		t.Errorf("Expected level %v, got %v", types.LogLevelInfo, logger.level)
	}

	if !logger.useColor {
		t.Error("Expected useColor to be true")
	}

	if logger.output == nil {
		t.Error("Expected output to be set")
	}
}

func TestSetLevel(t *testing.T) {
	logger := New(types.LogLevelInfo, false)

	logger.SetLevel(types.LogLevelDebug)

	if logger.GetLevel() != types.LogLevelDebug {
		t.Errorf("Expected level %v, got %v", types.LogLevelDebug, logger.GetLevel())
	}
}

func TestLogLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelWarning, false)
	logger.SetOutput(&buf)

	// These should not appear (below warning level)
	logger.Debug("debug message")
	logger.Info("info message")

	// These should appear
	logger.Warning("warning message")
	logger.Error("error message")
	logger.Critical("critical message")

	output := buf.String()

	// Debug and Info should not be in output
	if strings.Contains(output, "debug message") {
		t.Error("Debug message should not appear when level is WARNING")
	}
	if strings.Contains(output, "info message") {
		t.Error("Info message should not appear when level is WARNING")
	}

	// Warning, Error, Critical should be in output
	if !strings.Contains(output, "warning message") {
		t.Error("Warning message should appear")
	}
	if !strings.Contains(output, "error message") {
		t.Error("Error message should appear")
	}
	if !strings.Contains(output, "critical message") {
		t.Error("Critical message should appear")
	}
}

func TestLogFormatting(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)

	logger.Info("test message")

	output := buf.String()

	// Check that output contains expected parts
	if !strings.Contains(output, "INFO") {
		t.Error("Output should contain log level INFO")
	}
	if !strings.Contains(output, "test message") {
		t.Error("Output should contain the message")
	}
	// Check for timestamp (format: YYYY-MM-DD HH:MM:SS)
	if !strings.Contains(output, "[") || !strings.Contains(output, "]") {
		t.Error("Output should contain timestamp in brackets")
	}
}

func TestPhaseLogging(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)

	logger.Phase("Phase message")

	output := buf.String()

	if !strings.Contains(output, "PHASE") {
		t.Error("Output should contain level PHASE")
	}
	if !strings.Contains(output, "Phase message") {
		t.Error("Output should contain the phase message")
	}
}

func TestLogWithFormatting(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)

	logger.Info("Number: %d, String: %s", 42, "test")

	output := buf.String()

	if !strings.Contains(output, "Number: 42") {
		t.Error("Output should contain formatted number")
	}
	if !strings.Contains(output, "String: test") {
		t.Error("Output should contain formatted string")
	}
}

func TestColorOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, true) // with colors
	logger.SetOutput(&buf)

	logger.Info("test")

	output := buf.String()

	// Should contain ANSI color codes
	if !strings.Contains(output, "\033[") {
		t.Error("Colored output should contain ANSI codes")
	}
}

func TestNoColorOutput(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, false) // without colors
	logger.SetOutput(&buf)

	logger.Info("test")

	output := buf.String()

	// Should NOT contain ANSI color codes
	if strings.Contains(output, "\033[") {
		t.Error("Non-colored output should not contain ANSI codes")
	}
}

func TestDifferentLogLevels(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	tests := []struct {
		name     string
		logFunc  func(string, ...interface{})
		message  string
		levelStr string
	}{
		{"debug", logger.Debug, "debug test", "DEBUG"},
		{"info", logger.Info, "info test", "INFO"},
		{"warning", logger.Warning, "warning test", "WARNING"},
		{"error", logger.Error, "error test", "ERROR"},
		{"critical", logger.Critical, "critical test", "CRITICAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			tt.logFunc(tt.message)
			output := buf.String()

			if !strings.Contains(output, tt.levelStr) {
				t.Errorf("Output should contain level %s", tt.levelStr)
			}
			if !strings.Contains(output, tt.message) {
				t.Errorf("Output should contain message %s", tt.message)
			}
		})
	}
}

func TestDefaultLogger(t *testing.T) {
	// Test that default logger exists
	defaultLog := GetDefaultLogger()
	if defaultLog == nil {
		t.Fatal("Default logger should not be nil")
	}

	// Test setting custom default logger
	customLogger := New(types.LogLevelDebug, false)
	SetDefaultLogger(customLogger)

	if GetDefaultLogger() != customLogger {
		t.Error("GetDefaultLogger should return the custom logger")
	}
}

func TestPackageLevelFunctions(t *testing.T) {
	var buf bytes.Buffer
	customLogger := New(types.LogLevelDebug, false)
	customLogger.SetOutput(&buf)
	SetDefaultLogger(customLogger)

	// Test package-level functions
	Debug("debug")
	Info("info")
	Warning("warning")
	Error("error")
	Critical("critical")

	output := buf.String()

	levels := []string{"DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL"}
	for _, level := range levels {
		if !strings.Contains(output, level) {
			t.Errorf("Output should contain %s", level)
		}
	}
}

func TestFatalUsesExitFunc(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	exitCalled := 0
	var exitCode int
	logger.SetExitFunc(func(code int) {
		exitCalled++
		exitCode = code
	})

	logger.Fatal(types.ExitGenericError, "fatal message")

	if exitCalled != 1 || exitCode != types.ExitGenericError.Int() {
		t.Fatalf("exitFunc not called as expected, called=%d code=%d", exitCalled, exitCode)
	}
	if !strings.Contains(buf.String(), "fatal message") {
		t.Fatalf("fatal log missing message: %s", buf.String())
	}
}

func TestSetOutputNilDefaultsToStdout(t *testing.T) {
	logger := New(types.LogLevelInfo, false)
	logger.SetOutput(nil)
	if logger.output != os.Stdout {
		t.Fatalf("expected output to default to stdout when nil provided")
	}
}

func TestOpenAndCloseLogFile(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "app.log")

	logger := New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile error: %v", err)
	}
	if logger.GetLogFilePath() != logPath {
		t.Fatalf("GetLogFilePath = %s, want %s", logger.GetLogFilePath(), logPath)
	}

	logger.Info("hello")
	logger.Warning("warn")
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile error: %v", err)
	}
	if logger.GetLogFilePath() != "" {
		t.Fatalf("expected log file path to be cleared after close")
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(content), "hello") || !strings.Contains(string(content), "warn") {
		t.Fatalf("log file missing expected entries: %s", string(content))
	}

	// Second close should be a no-op
	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("second CloseLogFile should not error: %v", err)
	}
}

func TestUsesColorAndGetLevel(t *testing.T) {
	col := New(types.LogLevelInfo, true)
	if !col.UsesColor() {
		t.Fatalf("UsesColor should be true when enabled")
	}
	if col.GetLevel() != types.LogLevelInfo {
		t.Fatalf("GetLevel mismatch")
	}
	col.SetLevel(types.LogLevelDebug)
	if col.GetLevel() != types.LogLevelDebug {
		t.Fatalf("GetLevel should reflect updated level")
	}
	plain := New(types.LogLevelInfo, false)
	if plain.UsesColor() {
		t.Fatalf("UsesColor should be false when disabled")
	}
}

func TestWarningAndErrorCounters(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	if logger.HasWarnings() || logger.HasErrors() {
		t.Fatalf("counters should be false initially")
	}
	logger.Warning("warn")
	if !logger.HasWarnings() || logger.HasErrors() {
		t.Fatalf("warning should set HasWarnings only")
	}
	logger.Error("err")
	logger.Critical("crit")
	if !logger.HasErrors() {
		t.Fatalf("error counter should be set after error/critical")
	}
	logger.Info("info")
	logger.Debug("dbg")
	if !strings.Contains(buf.String(), "warn") || !strings.Contains(buf.String(), "err") {
		t.Fatalf("output missing expected messages: %s", buf.String())
	}
}

func TestPhaseStepSkipNilReceiver(t *testing.T) {
	var l *Logger
	l.Phase("phase")
	l.Step("step")
	l.Skip("skip")
	// No panic expected
}

func TestPackageLevelStepAndSkip(t *testing.T) {
	var buf bytes.Buffer
	customLogger := New(types.LogLevelDebug, false)
	customLogger.SetOutput(&buf)
	SetDefaultLogger(customLogger)

	Step("step msg")
	Skip("skip msg")

	out := buf.String()
	if !strings.Contains(out, "STEP") || !strings.Contains(out, "step msg") {
		t.Fatalf("expected STEP output, got %s", out)
	}
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "skip msg") {
		t.Fatalf("expected SKIP output, got %s", out)
	}
}

func TestAppendRawWritesOnlyToLogFile(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "raw.log")

	logger := New(types.LogLevelInfo, false)

	// No log file opened: AppendRaw should be a no-op and not panic
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.AppendRaw("no file")
	if buf.Len() != 0 {
		t.Fatalf("AppendRaw should not write to stdout when no log file is open")
	}

	// Now open a log file and ensure AppendRaw writes only there
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile error: %v", err)
	}
	buf.Reset()

	logger.AppendRaw("raw line")

	if buf.Len() != 0 {
		t.Fatalf("AppendRaw should not write to stdout; got %q", buf.String())
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read raw log file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "raw line") {
		t.Fatalf("raw log file missing message: %s", text)
	}
	if !strings.Contains(text, types.LogLevelInfo.String()) {
		t.Fatalf("raw log file should use INFO level label, got: %s", text)
	}
}

func TestPhaseStepSkipColorOverrides(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelInfo, true)
	logger.SetOutput(&buf)

	logger.Phase("phase msg")
	logger.Step("step msg")
	logger.Skip("skip msg")

	out := buf.String()

	// PHASE and STEP use blue, SKIP uses magenta
	if !strings.Contains(out, "\033[34m") {
		t.Fatalf("expected blue ANSI color code for PHASE/STEP, got %q", out)
	}
	if !strings.Contains(out, "\033[35m") {
		t.Fatalf("expected magenta ANSI color code for SKIP, got %q", out)
	}
	if !strings.Contains(out, "PHASE") || !strings.Contains(out, "phase msg") {
		t.Fatalf("expected PHASE label and message, got %q", out)
	}
	if !strings.Contains(out, "STEP") || !strings.Contains(out, "step msg") {
		t.Fatalf("expected STEP label and message, got %q", out)
	}
	if !strings.Contains(out, "SKIP") || !strings.Contains(out, "skip msg") {
		t.Fatalf("expected SKIP label and message, got %q", out)
	}
}

func TestSetExitFuncNilRestoresNonNilExitFunc(t *testing.T) {
	logger := New(types.LogLevelInfo, false)

	// Replace with custom, then reset to default via nil
	logger.SetExitFunc(func(int) {})
	logger.SetExitFunc(nil)

	if logger.exitFunc == nil {
		t.Fatalf("SetExitFunc(nil) should ensure exitFunc is non-nil")
	}
}

func TestOpenLogFileReopenClosesPrevious(t *testing.T) {
	tmp := t.TempDir()
	first := filepath.Join(tmp, "first.log")
	second := filepath.Join(tmp, "second.log")

	logger := New(types.LogLevelInfo, false)

	if err := logger.OpenLogFile(first); err != nil {
		t.Fatalf("OpenLogFile(first) error: %v", err)
	}
	oldFile := logger.logFile
	if oldFile == nil {
		t.Fatalf("expected logFile to be non-nil after first open")
	}

	if err := logger.OpenLogFile(second); err != nil {
		t.Fatalf("OpenLogFile(second) error: %v", err)
	}

	// Logger should now point to the second file
	if got := logger.GetLogFilePath(); got != second {
		t.Fatalf("GetLogFilePath = %s, want %s", got, second)
	}

	// Writing to the old file should now fail because it was closed
	if _, err := oldFile.Write([]byte("test")); err == nil {
		t.Fatalf("expected write to old closed file to fail")
	}
}

func TestPackageLevelFatalUsesDefaultLoggerExitFunc(t *testing.T) {
	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	exitCalled := 0
	var exitCode int
	logger.SetExitFunc(func(code int) {
		exitCalled++
		exitCode = code
	})

	SetDefaultLogger(logger)

	Fatal(types.ExitConfigError, "pkg fatal msg")

	if exitCalled != 1 || exitCode != types.ExitConfigError.Int() {
		t.Fatalf("package-level Fatal exitFunc not called as expected, called=%d code=%d", exitCalled, exitCode)
	}
	if !strings.Contains(buf.String(), "pkg fatal msg") {
		t.Fatalf("package-level fatal log missing message: %s", buf.String())
	}
}
