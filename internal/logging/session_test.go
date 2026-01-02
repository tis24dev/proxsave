package logging

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestSanitizeFlowName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "session"},
		{"   ", "session"},
		{"My Flow", "my-flow"},
		{"a__b", "a-b"},
		{"----", "session"},
		{"AA..BB", "aa-bb"},
	}

	for _, tt := range tests {
		got := sanitizeFlowName(tt.in)
		if got != tt.want {
			t.Fatalf("sanitizeFlowName(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestDetectHostname(t *testing.T) {
	host := detectHostname()
	if host == "" {
		t.Fatalf("expected non-empty hostname")
	}
	if strings.HasPrefix(host, "-") || strings.HasSuffix(host, "-") {
		t.Fatalf("unexpected leading/trailing dash: %q", host)
	}
	for _, r := range host {
		isLower := r >= 'a' && r <= 'z'
		isDigit := r >= '0' && r <= '9'
		if !(isLower || isDigit || r == '-') {
			t.Fatalf("unexpected rune %q in hostname %q", r, host)
		}
	}
}

func TestStartSessionLogger_CreatesAndWritesLogFile(t *testing.T) {
	logger, logPath, cleanup, err := StartSessionLogger("My Flow", types.LogLevelDebug, false)
	if err != nil {
		t.Fatalf("StartSessionLogger error: %v", err)
	}
	if logger == nil || cleanup == nil {
		t.Fatalf("expected logger and cleanup func")
	}
	t.Cleanup(cleanup)

	if got := logger.GetLogFilePath(); got != logPath {
		t.Fatalf("GetLogFilePath() = %q; want %q", got, logPath)
	}
	if filepath.Dir(logPath) != sessionLogDir {
		t.Fatalf("logPath dir = %q; want %q", filepath.Dir(logPath), sessionLogDir)
	}
	base := filepath.Base(logPath)
	if !strings.HasPrefix(base, "my-flow-") || !strings.HasSuffix(base, ".log") {
		t.Fatalf("unexpected log file name: %q", base)
	}

	logger.SetOutput(io.Discard)
	logger.Info("hello session")
	cleanup()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", logPath, err)
	}
	if !strings.Contains(string(data), "hello session") {
		t.Fatalf("expected log file to contain message, got %q", string(data))
	}
	_ = os.Remove(logPath)
}
