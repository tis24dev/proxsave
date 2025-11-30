package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

const sessionLogDir = "/tmp/proxsave"

// StartSessionLogger creates a new logger instance that writes to a real-time log
// file under /tmp/proxsave. The caller receives the configured logger, the
// absolute log path, and a cleanup function that must be invoked when the
// session completes.
func StartSessionLogger(flow string, level types.LogLevel, useColor bool) (*Logger, string, func(), error) {
	flow = sanitizeFlowName(flow)
	if err := os.MkdirAll(sessionLogDir, 0o755); err != nil {
		return nil, "", nil, fmt.Errorf("create session log directory: %w", err)
	}

	hostname := detectHostname()
	timestamp := time.Now().Format("20060102-150405")
	logName := fmt.Sprintf("%s-%s-%s.log", flow, hostname, timestamp)
	logPath := filepath.Join(sessionLogDir, logName)

	logger := New(level, useColor)
	if err := logger.OpenLogFile(logPath); err != nil {
		return nil, "", nil, err
	}

	cleanup := func() {
		_ = logger.CloseLogFile()
	}

	return logger, logPath, cleanup, nil
}

func sanitizeFlowName(flow string) string {
	flow = strings.ToLower(strings.TrimSpace(flow))
	if flow == "" {
		flow = "session"
	}
	replacer := func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}
	sanitized := strings.Map(replacer, flow)
	sanitized = strings.Trim(sanitized, "-")
	for strings.Contains(sanitized, "--") {
		sanitized = strings.ReplaceAll(sanitized, "--", "-")
	}
	if sanitized == "" {
		sanitized = "session"
	}
	return sanitized
}

func detectHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return "host"
	}
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		host = "host"
	}
	return sanitizeFlowName(host)
}
