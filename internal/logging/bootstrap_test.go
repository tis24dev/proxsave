package logging

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestBootstrapLoggerRecordAndFlushDefaultLevel(t *testing.T) {
	b := NewBootstrapLogger()
	if b.minLevel != types.LogLevelInfo {
		t.Fatalf("default minLevel should be INFO, got %v", b.minLevel)
	}

	// Record various entries
	b.Println("plain1")
	b.Printf("plain-%d", 2)
	b.Info("info")
	b.Warning("warn")
	b.Error("err")

	if len(b.entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(b.entries))
	}

	// Prepare main logger
	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatal(err)
	}

	b.Flush(logger)

	// Every entry reaches the log file. The console already showed them when they
	// were recorded, so the logger does not print them again; raw entries
	// (Println/Printf) never go to the logger's console either.
	_ = logger.CloseLogFile()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []string{"plain1", "plain-2", "info", "warn", "err"} {
		if !strings.Contains(string(data), msg) {
			t.Fatalf("log file missing %s:\n%s", msg, data)
		}
	}
	if buf.Len() != 0 {
		t.Fatalf("lines already on the console must not be printed again:\n%s", buf.String())
	}
	if logger.WarningCount() != 1 || !logger.HasErrors() {
		t.Fatalf("flushed warning/error must still count: warnings=%d errors=%v", logger.WarningCount(), logger.HasErrors())
	}

	// Flush should be idempotent
	buf.Reset()
	b.Flush(logger)
	if buf.Len() != 0 {
		t.Fatalf("second flush should not emit logs")
	}
}

func TestBootstrapLoggerLevelFiltering(t *testing.T) {
	b := NewBootstrapLogger()
	b.SetLevel(types.LogLevelWarning)
	b.Info("info skipped")
	b.Warning("warn kept")
	b.Error("err kept")

	var buf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	logPath := filepath.Join(t.TempDir(), "run.log")
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatal(err)
	}

	b.Flush(logger)
	_ = logger.CloseLogFile()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)
	if strings.Contains(out, "info skipped") {
		t.Fatalf("info should have been filtered out")
	}
	if !strings.Contains(out, "warn kept") || !strings.Contains(out, "err kept") {
		t.Fatalf("expected warn and err in the log file, got %s", out)
	}
}

func TestBootstrapLoggerDebugMirrorsAndFlushesAtDebugLevel(t *testing.T) {
	b := NewBootstrapLogger()
	b.SetLevel(types.LogLevelDebug)

	var mirrorBuf bytes.Buffer
	mirror := New(types.LogLevelDebug, false)
	mirror.SetOutput(&mirrorBuf)
	b.SetMirrorLogger(mirror)

	b.Debug("debug-%d", 1)
	if !strings.Contains(mirrorBuf.String(), "debug-1") {
		t.Fatalf("expected mirror logger to receive debug message, got %q", mirrorBuf.String())
	}

	var flushBuf bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&flushBuf)
	b.Flush(logger)
	if !strings.Contains(flushBuf.String(), "debug-1") {
		t.Fatalf("expected debug message to be flushed, got %q", flushBuf.String())
	}
}

func TestBootstrapLoggerNilReceiverNoops(t *testing.T) {
	var b *BootstrapLogger

	logger := New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	b.SetLevel(types.LogLevelDebug)
	b.SetMirrorLogger(logger)
	b.Println("plain")
	b.Printf("plain-%d", 1)
	b.Debug("debug")
	b.Info("info")
	b.Warning("warn")
	b.Error("err")
	b.Flush(logger)
	b.Flush(nil)

	if buf.Len() != 0 {
		t.Fatalf("nil bootstrap receiver should not write to logger, got %q", buf.String())
	}
}
