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

	baseDir, _ := detectedBaseDirOrFallback()
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
	return utils.SetEnvValue(template, key, value)
}

func unsetEnvValue(template, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return template
	}

	lines := strings.Split(template, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			out = append(out, line)
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			out = append(out, line)
			continue
		}
		parsedKey := strings.TrimSpace(parts[0])
		if fields := strings.Fields(parsedKey); len(fields) >= 2 && fields[0] == "export" {
			parsedKey = fields[1]
		}
		if strings.EqualFold(parsedKey, key) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
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
