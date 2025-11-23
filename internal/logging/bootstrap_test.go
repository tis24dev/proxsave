package logging

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/types"
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
	for _, msg := range []string{"plain1", "plain-2", "info", "warn", "err"} {
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
