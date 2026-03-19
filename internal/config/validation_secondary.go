package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

const secondaryPathFormatMessage = "must be an absolute local filesystem path"

// ValidateRequiredSecondaryPath validates SECONDARY_PATH when secondary storage is enabled.
func ValidateRequiredSecondaryPath(path string) error {
	return validateSecondaryLocalPath(path, "SECONDARY_PATH", true)
}

// ValidateOptionalSecondaryPath validates SECONDARY_PATH when configured but not required.
func ValidateOptionalSecondaryPath(path string) error {
	return validateSecondaryLocalPath(path, "SECONDARY_PATH", false)
}

// ValidateOptionalSecondaryLogPath validates SECONDARY_LOG_PATH when provided.
func ValidateOptionalSecondaryLogPath(path string) error {
	return validateSecondaryLocalPath(path, "SECONDARY_LOG_PATH", false)
}

func validateSecondaryLocalPath(path, fieldName string, required bool) error {
	clean := strings.TrimSpace(path)
	if clean == "" {
		if required {
			return fmt.Errorf("%s is required when SECONDARY_ENABLED=true", fieldName)
		}
		return nil
	}

	if isUNCStylePath(clean) {
		return fmt.Errorf("%s %s", fieldName, secondaryPathFormatMessage)
	}

	if strings.Contains(clean, ":") && !filepath.IsAbs(clean) {
		return fmt.Errorf("%s %s", fieldName, secondaryPathFormatMessage)
	}

	if !filepath.IsAbs(clean) {
		return fmt.Errorf("%s %s", fieldName, secondaryPathFormatMessage)
	}

	return nil
}

func isUNCStylePath(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if strings.HasPrefix(path, "//") {
		return len(path) == 2 || path[2] != '/'
	}
	return false
}
