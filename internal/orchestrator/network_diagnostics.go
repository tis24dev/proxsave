package orchestrator

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var networkDiagnosticsSequence uint64

const snapshotLogMaxExtractedLines = 20

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

	b.WriteString("=== LIVE NETWORK STATE ===\n\n")
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
		if logger != nil {
			debugLogCommandResult(logger, "network snapshot", label, cmd, out, err)
		}
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

	b.WriteString("=== ON-DISK NETWORK CONFIG ===\n\n")
	appendFileSnapshot(logger, label, &b, "/etc/network/interfaces", 64*1024)
	appendDirSnapshot(logger, label, &b, "/etc/network/interfaces.d", 64*1024)
	appendFileSnapshot(logger, label, &b, "/etc/hosts", 64*1024)
	appendFileSnapshot(logger, label, &b, "/etc/hostname", 8*1024)
	appendFileSnapshot(logger, label, &b, "/etc/resolv.conf", 16*1024)

	b.WriteString("=== NETWORK STACK SERVICES ===\n\n")
	appendCommandSnapshot(ctx, logger, &b, timeout, []string{"systemctl", "is-active", "networking"})
	appendCommandSnapshot(ctx, logger, &b, timeout, []string{"systemctl", "is-active", "systemd-networkd"})
	appendCommandSnapshot(ctx, logger, &b, timeout, []string{"systemctl", "is-active", "NetworkManager"})
	b.WriteString("\n")

	b.WriteString("=== ifupdown (ifquery --running -a) ===\n\n")
	appendCommandSnapshot(ctx, logger, &b, timeout, []string{"ifquery", "--running", "-a"})
	b.WriteString("\n")

	if err := restoreFS.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return "", err
	}
	logging.DebugStep(logger, "network snapshot", "Saved: %s", path)
	return path, nil
}

func appendCommandSnapshot(ctx context.Context, logger *logging.Logger, b *strings.Builder, timeout time.Duration, cmd []string) {
	if len(cmd) == 0 {
		return
	}
	b.WriteString("$ " + strings.Join(cmd, " ") + "\n")
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	out, err := restoreCmd.Run(ctxTimeout, cmd[0], cmd[1:]...)
	cancel()
	if logger != nil {
		debugLogCommandResult(logger, "network snapshot", "command", cmd, out, err)
	}
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

func appendFileSnapshot(logger *logging.Logger, label string, b *strings.Builder, path string, maxBytes int) {
	b.WriteString("## File: " + path + "\n")
	if logger != nil {
		logging.DebugStep(logger, "network snapshot", "Read file (%s): %s", label, path)
	}
	info, err := restoreFS.Stat(path)
	if err != nil {
		b.WriteString(fmt.Sprintf("ERROR: %v\n\n", err))
		if logger != nil {
			logging.DebugStep(logger, "network snapshot", "Stat failed (%s): %s: %v", label, path, err)
		}
		return
	}
	b.WriteString(fmt.Sprintf("Mode: %s\n", info.Mode().String()))
	b.WriteString(fmt.Sprintf("Size: %d\n", info.Size()))
	b.WriteString(fmt.Sprintf("ModTime: %s\n\n", info.ModTime().Format(time.RFC3339)))
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		b.WriteString(fmt.Sprintf("ERROR: %v\n\n", err))
		if logger != nil {
			logging.DebugStep(logger, "network snapshot", "Read failed (%s): %s: %v", label, path, err)
		}
		return
	}
	if logger != nil {
		debugLogOnDiskFile(logger, label, path, info, data)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		b.Write(data[:maxBytes])
		if maxBytes > 0 && (len(data) == 0 || data[maxBytes-1] != '\n') {
			b.WriteString("\n")
		}
		b.WriteString(fmt.Sprintf("\n[truncated: %d of %d bytes]\n\n", maxBytes, len(data)))
		return
	}
	b.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString("\n")
}

func appendDirSnapshot(logger *logging.Logger, label string, b *strings.Builder, dir string, maxBytesPerFile int) {
	b.WriteString("## Dir: " + dir + "\n")
	if logger != nil {
		logging.DebugStep(logger, "network snapshot", "Read dir (%s): %s", label, dir)
	}
	entries, err := restoreFS.ReadDir(dir)
	if err != nil {
		b.WriteString(fmt.Sprintf("ERROR: %v\n\n", err))
		if logger != nil {
			logging.DebugStep(logger, "network snapshot", "ReadDir failed (%s): %s: %v", label, dir, err)
		}
		return
	}
	if len(entries) == 0 {
		b.WriteString("(empty)\n\n")
		if logger != nil {
			logging.DebugStep(logger, "network snapshot", "Dir empty (%s): %s", label, dir)
		}
		return
	}

	type entryInfo struct {
		name string
		mode os.FileMode
	}
	var list []entryInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, entryInfo{name: e.Name(), mode: info.Mode()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].name < list[j].name })
	if logger != nil {
		names := make([]string, 0, len(list))
		for _, e := range list {
			names = append(names, e.name)
		}
		logging.DebugStep(logger, "network snapshot", "Dir entries (%s): %s: %s", label, dir, strings.Join(names, ", "))
	}
	for _, e := range list {
		b.WriteString(fmt.Sprintf("- %s (%s)\n", e.name, e.mode.String()))
	}
	b.WriteString("\n")

	for _, e := range list {
		full := filepath.Join(dir, e.name)
		if e.mode.IsRegular() || (e.mode&os.ModeSymlink != 0) {
			appendFileSnapshot(logger, label, b, full, maxBytesPerFile)
		}
	}
}

func debugLogCommandResult(logger *logging.Logger, operation, label string, cmd []string, out []byte, err error) {
	if logger == nil || len(cmd) == 0 {
		return
	}
	cmdStr := strings.Join(cmd, " ")
	if err != nil {
		logging.DebugStep(logger, operation, "Command error (%s): %s: %v", label, cmdStr, err)
	} else {
		logging.DebugStep(logger, operation, "Command ok (%s): %s", label, cmdStr)
	}
	if len(out) == 0 {
		return
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return
	}
	preview := s
	if len(preview) > 800 {
		preview = preview[:800] + "…"
	}
	logging.DebugStep(logger, operation, "Command output (%s): %s: %s", label, cmdStr, preview)
}

func debugLogOnDiskFile(logger *logging.Logger, label, path string, info os.FileInfo, data []byte) {
	if logger == nil || info == nil {
		return
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	logging.DebugStep(logger, "network snapshot", "File ok (%s): %s mode=%s size=%d mtime=%s sha256=%s", label, path, info.Mode().String(), info.Size(), info.ModTime().Format(time.RFC3339), sha)

	extracted := extractInterestingLines(path, data, snapshotLogMaxExtractedLines)
	if len(extracted) == 0 {
		return
	}
	for _, line := range extracted {
		logging.DebugStep(logger, "network snapshot", "File excerpt (%s): %s: %s", label, path, line)
	}
}

func extractInterestingLines(path string, data []byte, limit int) []string {
	if limit <= 0 {
		return nil
	}
	var out []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if isInterestingNetworkLine(path, line) {
			out = append(out, line)
			if len(out) >= limit {
				break
			}
		}
	}
	return out
}

func isInterestingNetworkLine(path, line string) bool {
	switch path {
	case "/etc/network/interfaces":
		return strings.HasPrefix(line, "auto ") ||
			strings.HasPrefix(line, "iface ") ||
			strings.HasPrefix(line, "allow-hotplug ") ||
			strings.HasPrefix(line, "address ") ||
			strings.HasPrefix(line, "gateway ") ||
			strings.HasPrefix(line, "bridge-") ||
			strings.HasPrefix(line, "source ")
	case "/etc/resolv.conf":
		return strings.HasPrefix(line, "nameserver ") ||
			strings.HasPrefix(line, "search ") ||
			strings.HasPrefix(line, "domain ")
	case "/etc/hostname":
		return true
	case "/etc/hosts":
		if strings.HasPrefix(line, "127.") || strings.HasPrefix(line, "::1") {
			return false
		}
		return strings.HasPrefix(line, "10.") ||
			strings.HasPrefix(line, "172.") ||
			strings.HasPrefix(line, "192.") ||
			strings.Contains(line, ".local") ||
			strings.Contains(line, "pbs") ||
			strings.Contains(line, "pve")
	default:
		return true
	}
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
