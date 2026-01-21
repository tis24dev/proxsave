package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var networkDiagnosticsSequence uint64

func createNetworkDiagnosticsDir() (string, error) {
	baseDir := "/tmp/proxsave"
	if err := restoreFS.MkdirAll(baseDir, 0o755); err != nil {
		return "", fmt.Errorf("create diagnostics directory: %w", err)
	}
	seq := atomic.AddUint64(&networkDiagnosticsSequence, 1)
	dir := filepath.Join(baseDir, fmt.Sprintf("network_apply_%s_%d", nowRestore().Format("20060102_150405"), seq))
	if err := restoreFS.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create diagnostics directory %s: %w", dir, err)
	}
	return dir, nil
}

func writeNetworkSnapshot(ctx context.Context, logger *logging.Logger, diagnosticsDir, label string, timeout time.Duration) (path string, err error) {
	done := logging.DebugStart(logger, "network snapshot", "label=%s timeout=%s dir=%s", strings.TrimSpace(label), timeout, strings.TrimSpace(diagnosticsDir))
	defer func() { done(err) }()

	if strings.TrimSpace(diagnosticsDir) == "" {
		return "", fmt.Errorf("empty diagnostics directory")
	}
	if strings.TrimSpace(label) == "" {
		label = "snapshot"
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	path = filepath.Join(diagnosticsDir, fmt.Sprintf("%s.txt", label))
	var b strings.Builder
	b.WriteString(fmt.Sprintf("GeneratedAt: %s\n", nowRestore().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("Label: %s\n\n", label))

	commands := [][]string{
		{"ip", "-br", "link"},
		{"ip", "-br", "addr"},
		{"ip", "route", "show"},
		{"ip", "-6", "route", "show"},
	}
	for _, cmd := range commands {
		if len(cmd) == 0 {
			continue
		}
		logging.DebugStep(logger, "network snapshot", "Run: %s", strings.Join(cmd, " "))
		b.WriteString("$ " + strings.Join(cmd, " ") + "\n")
		ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
		out, err := restoreCmd.Run(ctxTimeout, cmd[0], cmd[1:]...)
		cancel()
		if len(out) > 0 {
			b.Write(out)
			if out[len(out)-1] != '\n' {
				b.WriteString("\n")
			}
		}
		if err != nil {
			b.WriteString(fmt.Sprintf("ERROR: %v\n", err))
			if logger != nil {
				logger.Debug("Network snapshot command failed: %s: %v", strings.Join(cmd, " "), err)
			}
		}
		b.WriteString("\n")
	}

	if err := restoreFS.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	logging.DebugStep(logger, "network snapshot", "Saved: %s", path)
	return path, nil
}

func writeNetworkHealthReportFile(diagnosticsDir string, report networkHealthReport) (string, error) {
	return writeNetworkHealthReportFileNamed(diagnosticsDir, "health_after.txt", report)
}

func writeNetworkHealthReportFileNamed(diagnosticsDir, filename string, report networkHealthReport) (string, error) {
	if strings.TrimSpace(diagnosticsDir) == "" {
		return "", fmt.Errorf("empty diagnostics directory")
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "health.txt"
	}
	path := filepath.Join(diagnosticsDir, name)
	if err := restoreFS.WriteFile(path, []byte(report.Details()+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeNetworkPreflightReportFile(diagnosticsDir string, report networkPreflightResult) (string, error) {
	if strings.TrimSpace(diagnosticsDir) == "" {
		return "", fmt.Errorf("empty diagnostics directory")
	}
	path := filepath.Join(diagnosticsDir, "preflight.txt")
	if err := restoreFS.WriteFile(path, []byte(report.Details()+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeNetworkIfqueryDiagnosticReportFile(diagnosticsDir, filename string, report networkPreflightResult) (string, error) {
	if strings.TrimSpace(diagnosticsDir) == "" {
		return "", fmt.Errorf("empty diagnostics directory")
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "ifquery_check.txt"
	}
	path := filepath.Join(diagnosticsDir, name)
	var b strings.Builder
	b.WriteString("NOTE: ifquery --check compares the running state vs the config.\n")
	b.WriteString("It may show [fail] before apply (expected) when the target config differs from the current runtime.\n\n")
	b.WriteString(report.Details())
	b.WriteString("\n")
	if err := restoreFS.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func writeNetworkTextReportFile(diagnosticsDir, filename, content string) (string, error) {
	if strings.TrimSpace(diagnosticsDir) == "" {
		return "", fmt.Errorf("empty diagnostics directory")
	}
	name := strings.TrimSpace(filename)
	if name == "" {
		name = "report.txt"
	}
	path := filepath.Join(diagnosticsDir, name)
	if err := restoreFS.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
