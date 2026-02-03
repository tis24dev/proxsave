package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/pkg/utils"
)

type configStatusLogger interface {
	Warning(format string, args ...interface{})
	Info(format string, args ...interface{})
}

func resolveInstallConfigPath(configPath string) (string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return "", fmt.Errorf("configuration path is empty")
	}

	if filepath.IsAbs(configPath) {
		return configPath, nil
	}

	baseDir, ok := detectBaseDir()
	if !ok {
		return "", fmt.Errorf("unable to determine base directory for configuration")
	}
	return filepath.Join(baseDir, configPath), nil
}

func ensureConfigExists(path string, logger configStatusLogger) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("configuration path is empty")
	}

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat configuration file: %w", err)
	}

	// No automatic migration or template creation: require explicit user action.
	logger.Warning("Configuration file not found: %s", path)
	logger.Warning("Run 'proxsave --install' (alias: proxmox-backup --install) to create a new configuration or '--env-migration' to import an existing Bash backup.env")
	return fmt.Errorf("configuration file is required to continue")
}

func setEnvValue(template, key, value string) string {
	lines := strings.Split(template, "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}

		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) >= 1 && strings.TrimSpace(parts[0]) == key {
			// Found match!
			// We try to preserve the indentation and comments from the original line.
			leadingLen := len(line) - len(strings.TrimLeft(line, " \t"))
			leading := ""
			if leadingLen > 0 {
				leading = line[:leadingLen]
			}

			// Extract comment if present in the original line logic
			// The original logic extracted comment from 'rest' after target match.
			// Here we can re-parse the line specifically for comment.
			comment := ""
			commentSpacing := ""

			if idx := strings.Index(line, "#"); idx >= 0 {
				// Verify # is not part of the key or value?
				// Assuming standard comment
				commentPart := line[idx:]
				// Ensure it's not inside quotes? The original logic didn't check quotes carefully but let's be safe(r).
				// For setEnvValue we are replacing the value, so we just want to keep the comment at the end.
				comment = commentPart

				// Find spacing before comment
				beforeComment := line[:idx]
				trimmedBefore := strings.TrimRight(beforeComment, " \t")
				commentSpacing = beforeComment[len(trimmedBefore):]
			}

			newLine := leading + key + "=" + value
			if comment != "" {
				spacing := commentSpacing
				if spacing == "" {
					spacing = " "
				}
				newLine += spacing + comment
			}
			lines[i] = newLine
			replaced = true
			// We stop after first match? Original code didn't break, but typically keys are unique.
			// Let's break to avoid multiple replacements if file is messy (or continue to fix all?)
			// Original didn't break. Let's not break to match behavior.
		}
	}
	if !replaced {
		lines = append(lines, key+"="+value)
	}
	return strings.Join(lines, "\n")
}

func sanitizeEnvValue(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\x00' {
			return -1
		}
		return r
	}, value)
	return strings.TrimSpace(value)
}
