package config

import (
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/pkg/utils"
)

func encodeEnvValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	if !strings.ContainsAny(value, "# \t\r\n\"'") {
		return value
	}
	if !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	if !strings.Contains(value, `"`) {
		return `"` + value + `"`
	}
	return strconv.Quote(value)
}

// ApplySecondaryStorageSettings writes the canonical secondary-storage state
// into an env template. Disabled secondary storage always clears both
// SECONDARY_PATH and SECONDARY_LOG_PATH so the saved config matches user intent.
func ApplySecondaryStorageSettings(template string, enabled bool, secondaryPath string, secondaryLogPath string) string {
	if enabled {
		template = utils.SetEnvValue(template, "SECONDARY_ENABLED", "true")
		template = utils.SetEnvValue(template, "SECONDARY_PATH", encodeEnvValue(secondaryPath))
		template = utils.SetEnvValue(template, "SECONDARY_LOG_PATH", encodeEnvValue(secondaryLogPath))
		return template
	}

	template = utils.SetEnvValue(template, "SECONDARY_ENABLED", "false")
	template = utils.SetEnvValue(template, "SECONDARY_PATH", "")
	template = utils.SetEnvValue(template, "SECONDARY_LOG_PATH", "")
	return template
}

// RemoveEnvKeys removes active KEY=VALUE entries from an env template while
// preserving comments and unrelated lines.
func RemoveEnvKeys(template string, keys ...string) string {
	if len(keys) == 0 {
		return template
	}
	remove := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key != "" {
			remove[key] = struct{}{}
		}
	}
	if len(remove) == 0 {
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
		key, _, ok := utils.SplitKeyValue(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
			key = fields[1]
		}
		if _, drop := remove[strings.ToUpper(strings.TrimSpace(key))]; drop {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// RemoveRuntimeDerivedEnvKeys strips config keys that are intentionally derived
// at runtime instead of stored in backup.env.
func RemoveRuntimeDerivedEnvKeys(template string) string {
	return RemoveEnvKeys(template, "BASE_DIR", "CRON_SCHEDULE", "CRON_HOUR", "CRON_MINUTE")
}
