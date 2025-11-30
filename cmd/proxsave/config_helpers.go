package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	target := key + "="
	lines := strings.Split(template, "\n")
	replaced := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, target) {
			leadingLen := len(line) - len(strings.TrimLeft(line, " \t"))
			leading := ""
			if leadingLen > 0 {
				leading = line[:leadingLen]
			}
			rest := line[leadingLen:]
			commentSpacing := ""
			comment := ""
			if idx := strings.Index(rest, "#"); idx >= 0 {
				before := rest[:idx]
				comment = rest[idx:]
				trimmedBefore := strings.TrimRight(before, " \t")
				commentSpacing = before[len(trimmedBefore):]
				rest = trimmedBefore
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
