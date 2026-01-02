package logging

import (
	"bytes"
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

	b.Flush(logger)

	out := buf.String()
	// Raw entries (Println/Printf) are only flushed to the log file,
	// not to the main logger output, to avoid duplicating banners on stdout.
	for _, msg := range []string{"info", "warn", "err"} {
		if !strings.Contains(out, msg) {
			t.Fatalf("output missing %s", msg)
		}
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

	b.Flush(logger)
	out := buf.String()
	if strings.Contains(out, "info skipped") {
		t.Fatalf("info should have been filtered out")
	}
	if !strings.Contains(out, "warn kept") || !strings.Contains(out, "err kept") {
		t.Fatalf("expected warn and err to be emitted, got %s", out)
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
