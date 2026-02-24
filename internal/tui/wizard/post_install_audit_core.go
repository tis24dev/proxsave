package wizard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// PostInstallAuditSuggestion represents an optional feature that appears to be enabled
// but not configured/detected on this system. Users can disable the feature by setting
// the corresponding KEY=false in backup.env.
type PostInstallAuditSuggestion struct {
	Key      string
	Messages []string
}

var (
	postInstallAuditDisableHintRe = regexp.MustCompile(`(?i)\bset\s+([A-Z0-9_]+)=false\b`)
	postInstallAuditANSISGRRe     = regexp.MustCompile(`\x1b\[[0-9;]*m`)

	postInstallAuditAllowedKeysOnce sync.Once
	postInstallAuditAllowedKeys     map[string]struct{}

	postInstallAuditRunner = runPostInstallAuditDryRun
)

func postInstallAuditAllowedKeysSet() map[string]struct{} {
	postInstallAuditAllowedKeysOnce.Do(func() {
		allowed := make(map[string]struct{})
		values := parseEnvTemplate(config.DefaultEnvTemplate())
		for key := range values {
			key = strings.ToUpper(strings.TrimSpace(key))
			if strings.HasPrefix(key, "BACKUP_") {
				allowed[key] = struct{}{}
			}
		}
		postInstallAuditAllowedKeys = allowed
	})
	return postInstallAuditAllowedKeys
}

func runPostInstallAuditDryRun(ctx context.Context, execPath, configPath string) (output string, exitCode int, err error) {
	// Run a dry-run with warning-level logs to keep output minimal while still capturing
	// all actionable "set KEY=false" hints.
	cmd := exec.CommandContext(ctx, execPath,
		"--dry-run",
		"--log-level", "warning",
		"--config", configPath,
	)
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		return string(out), 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		// Non-zero exit codes are expected when warnings are emitted (exit code 1).
		return string(out), exitErr.ExitCode(), nil
	}
	return string(out), -1, fmt.Errorf("post-install audit dry-run failed: %w", runErr)
}

// CollectPostInstallDisableSuggestions runs a proxsave dry-run and extracts actionable
// "set KEY=false" hints from the resulting warnings/errors. It only returns keys that:
//   - exist in the embedded template, and
//   - start with "BACKUP_", and
//   - are currently enabled (true) in the provided config file.
func CollectPostInstallDisableSuggestions(ctx context.Context, execPath, configPath string) ([]PostInstallAuditSuggestion, error) {
	if strings.TrimSpace(execPath) == "" {
		return nil, fmt.Errorf("exec path cannot be empty")
	}
	if strings.TrimSpace(configPath) == "" {
		return nil, fmt.Errorf("config path cannot be empty")
	}

	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read configuration for audit: %w", err)
	}
	configValues := parseEnvTemplate(string(configBytes))
	allowed := postInstallAuditAllowedKeysSet()

	auditCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	output, _, err := postInstallAuditRunner(auditCtx, execPath, configPath)
	if err != nil {
		return nil, err
	}

	issueLines := extractIssueLinesFromProxsaveOutput(output)
	return extractDisableSuggestionsFromIssueLines(issueLines, allowed, configValues), nil
}

func extractIssueLinesFromProxsaveOutput(output string) []string {
	lines := splitNormalizedLines(output)

	// Prefer the end-of-run summary, which is clean (no ANSI) and deduplicated.
	const header = "WARNINGS/ERRORS DURING RUN"
	inSummary := false
	issues := make([]string, 0, 16)

	for _, line := range lines {
		if strings.Contains(line, header) {
			inSummary = true
			continue
		}
		if !inSummary {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "===========================================") {
			break
		}
		issues = append(issues, trimmed)
	}

	if len(issues) > 0 {
		return issues
	}

	// Fallback: scan the entire output and keep only actionable lines. This is less
	// robust because the live log output may contain ANSI codes.
	for _, line := range lines {
		clean := stripANSI(strings.TrimSpace(line))
		if clean == "" {
			continue
		}
		if postInstallAuditDisableHintRe.MatchString(clean) {
			issues = append(issues, clean)
		}
	}
	return issues
}

func extractDisableSuggestionsFromIssueLines(issueLines []string, allowed map[string]struct{}, configValues map[string]string) []PostInstallAuditSuggestion {
	if len(issueLines) == 0 {
		return nil
	}
	if allowed == nil {
		allowed = map[string]struct{}{}
	}
	if configValues == nil {
		configValues = map[string]string{}
	}

	type msgSet map[string]struct{}
	byKey := make(map[string]msgSet)

	for _, raw := range issueLines {
		line := strings.TrimSpace(stripANSI(raw))
		if line == "" {
			continue
		}

		matches := postInstallAuditDisableHintRe.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}

		msg := normalizeIssueMessage(line)
		if msg == "" {
			msg = line
		}

		for _, m := range matches {
			if len(m) < 2 {
				continue
			}
			key := strings.ToUpper(strings.TrimSpace(m[1]))
			if !strings.HasPrefix(key, "BACKUP_") {
				continue
			}
			if _, ok := allowed[key]; !ok {
				continue
			}
			// Only suggest disabling keys that are currently enabled in the config.
			if !readTemplateBool(configValues, key) {
				continue
			}
			if _, ok := byKey[key]; !ok {
				byKey[key] = make(msgSet)
			}
			byKey[key][msg] = struct{}{}
		}
	}

	if len(byKey) == 0 {
		return nil
	}

	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]PostInstallAuditSuggestion, 0, len(keys))
	for _, key := range keys {
		msgs := make([]string, 0, len(byKey[key]))
		for msg := range byKey[key] {
			msgs = append(msgs, msg)
		}
		sort.Strings(msgs)
		out = append(out, PostInstallAuditSuggestion{
			Key:      key,
			Messages: msgs,
		})
	}
	return out
}

func normalizeIssueMessage(line string) string {
	line = strings.TrimSpace(stripANSI(line))
	if line == "" {
		return ""
	}
	// Prefer to remove "[timestamp] LEVEL" prefix when present.
	if strings.HasPrefix(line, "[") {
		if idx := strings.Index(line, "]"); idx >= 0 {
			rest := strings.TrimSpace(line[idx+1:])
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				level := fields[0]
				rest = strings.TrimSpace(rest[len(level):])
				if rest != "" {
					return rest
				}
			}
		}
	}
	return line
}

func splitNormalizedLines(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	// Normalize CRLF and split.
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func stripANSI(s string) string {
	// Best-effort removal of common ANSI SGR sequences.
	// Example: "\x1b[33mWARNING\x1b[0m"
	const esc = "\x1b["
	if !strings.Contains(s, esc) {
		return s
	}
	return postInstallAuditANSISGRRe.ReplaceAllString(s, "")
}
