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
