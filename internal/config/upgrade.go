package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/pkg/utils"
)

// UpgradeResult describes the outcome of a configuration upgrade.
type UpgradeResult struct {
	// BackupPath is the path of the backup created from the previous config.
	BackupPath string
	// MissingKeys are keys that were present in the template but not in the
	// user's config; template defaults were added for these.
	MissingKeys []string
	// ExtraKeys are keys that were present in the user's config but not in the
	// template. They are preserved in a dedicated "Custom keys" section.
	ExtraKeys []string
	// PreservedValues is the number of existing key=value pairs from the user's
	// configuration that were kept during the merge for keys present in the
	// template.
	PreservedValues int
	// Changed reports whether the config file was actually modified.
	Changed bool
}

// UpgradeConfigFile merges the user's configuration with the embedded template.
//
// Goals:
//   - Ensure all keys present in the template exist in the user's config.
//   - Preserve all existing user values for known keys.
//   - Preserve custom/legacy keys not present in the template in a dedicated
//     section at the end of the file.
//   - Keep the layout and comments of the template as much as possible.
//
// The procedure is:
//   1. Parse the existing config file and collect all KEY=VALUE entries.
//   2. Walk the template line-by-line:
//      - Comments/blank lines are copied as-is.
//      - For KEY=VALUE lines:
//        * If the user has values for that key, all of them are written
//          (one KEY=VALUE per line, in original order).
//        * Otherwise the template's line is kept and the key is recorded as
//          "missing" (a new default added).
//   3. Keys present in the user config but not in the template are appended
//      to a "Custom keys" section at the bottom of the file.
//   4. The original file is backed up before writing the new version.
func UpgradeConfigFile(configPath string) (*UpgradeResult, error) {
	result, newContent, originalContent, err := computeConfigUpgrade(configPath)
	if err != nil {
		return result, err
	}
	if result == nil {
		// Should not happen, but guard anyway
		result = &UpgradeResult{}
	}
	if !result.Changed {
		return result, nil
	}

	// 4. Backup original file and write the new version atomically.
	mode := os.FileMode(0o600)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode() & os.ModePerm
	}

	ts := time.Now().Format("20060102_150405")
	backupPath := fmt.Sprintf("%s.backup.%s", configPath, ts)

	if err := os.WriteFile(backupPath, originalContent, mode); err != nil {
		return result, fmt.Errorf("failed to create backup %s: %w", backupPath, err)
	}

	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(newContent), mode); err != nil {
		_ = os.Remove(tmpPath)
		return result, fmt.Errorf("failed to write temporary config %s: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return result, fmt.Errorf("failed to replace config %s: %w", configPath, err)
	}

	result.BackupPath = backupPath

	// Post-upgrade validation: ensure the upgraded configuration can be parsed.
	if _, err := LoadConfig(configPath); err != nil {
		// Attempt automatic rollback to the backup.
		_ = os.Rename(backupPath, configPath)
		return result, fmt.Errorf("upgraded config invalid, restored backup: %w", err)
	}
	return result, nil
}

// PlanUpgradeConfigFile computes what an upgrade would do without modifying
// the configuration file on disk.
//
// It returns an UpgradeResult populated with:
//   - MissingKeys: keys that would be added from the template.
//   - ExtraKeys: keys that would be preserved in the custom section.
//   - Changed: true if an upgrade would actually modify the file.
// BackupPath is always empty in dry-run mode.
func PlanUpgradeConfigFile(configPath string) (*UpgradeResult, error) {
	result, _, _, err := computeConfigUpgrade(configPath)
	if result == nil {
		result = &UpgradeResult{}
	}
	// In dry-run mode, no backup is created.
	result.BackupPath = ""
	return result, err
}

// computeConfigUpgrade performs the core merge logic and returns:
//   - UpgradeResult (MissingKeys, ExtraKeys, Changed)
//   - newContent: the upgraded file content (only meaningful if Changed=true)
//   - originalContent: the original file content as read from disk
func computeConfigUpgrade(configPath string) (*UpgradeResult, string, []byte, error) {
	result := &UpgradeResult{Changed: false}

	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return result, "", nil, fmt.Errorf("configuration path is empty")
	}

	originalContent, err := os.ReadFile(configPath)
	if err != nil {
		return result, "", nil, fmt.Errorf("cannot read configuration file %s: %w", configPath, err)
	}

	// Detect original line ending style so we can preserve it.
	lineEnding := "\n"
	if strings.Contains(string(originalContent), "\r\n") {
		lineEnding = "\r\n"
	}

	normalizedOriginal := strings.ReplaceAll(string(originalContent), "\r\n", "\n")
	originalLines := strings.Split(normalizedOriginal, "\n")

	// 1. Collect user values: for each KEY we store all VALUE entries in order.
	userValues := make(map[string][]string)
	userKeyOrder := make([]string, 0)

	for _, line := range originalLines {
		if utils.IsComment(line) {
			continue
		}
		key, value, ok := utils.SplitKeyValue(line)
		if !ok || key == "" {
			continue
		}
		if _, seen := userValues[key]; !seen {
			userKeyOrder = append(userKeyOrder, key)
		}
		userValues[key] = append(userValues[key], value)
	}

	// 2. Walk the template line-by-line, merging values.
	template := DefaultEnvTemplate()
	normalizedTemplate := strings.ReplaceAll(template, "\r\n", "\n")
	templateLines := strings.Split(normalizedTemplate, "\n")

	templateKeys := make(map[string]bool)
	missingKeys := make([]string, 0)
	newLines := make([]string, 0, len(templateLines)+len(userValues))

	for _, line := range templateLines {
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			newLines = append(newLines, line)
			continue
		}

		key, _, ok := utils.SplitKeyValue(line)
		if !ok || key == "" {
			newLines = append(newLines, line)
			continue
		}

		templateKeys[key] = true

		if values, ok := userValues[key]; ok && len(values) > 0 {
			// Preserve all user-defined values for this key.
			for _, v := range values {
				newLines = append(newLines, fmt.Sprintf("%s=%s", key, v))
			}
		} else {
			// Key missing in user config: keep template default and record it.
			missingKeys = append(missingKeys, key)
			newLines = append(newLines, line)
		}
	}

	// 3. Append extra keys (present only in user config) in a dedicated section.
	extraKeys := make([]string, 0)
	extraLines := make([]string, 0)

	for _, key := range userKeyOrder {
		if templateKeys[key] {
			continue
		}
		values := userValues[key]
		if len(values) == 0 {
			continue
		}
		extraKeys = append(extraKeys, key)
		for _, v := range values {
			extraLines = append(extraLines, fmt.Sprintf("%s=%s", key, v))
		}
	}

	if len(extraLines) > 0 {
		newLines = append(newLines,
			"",
			"# ----------------------------------------------------------------------",
			"# Custom keys preserved from previous configuration (not present in template)",
			"# ----------------------------------------------------------------------",
		)
		newLines = append(newLines, extraLines...)
	}

	// Count preserved values: key=value pairs coming from user config for
	// keys that exist in the template.
	preserved := 0
	for key, values := range userValues {
		if templateKeys[key] {
			preserved += len(values)
		}
	}

	// If nothing changed (no missing keys and no extras), we can return early.
	if len(missingKeys) == 0 && len(extraKeys) == 0 {
		result.PreservedValues = preserved
		return result, "", originalContent, nil
	}

	newContent := strings.Join(newLines, lineEnding)
	// Preserve trailing newline if template had one.
	if strings.HasSuffix(normalizedTemplate, "\n") && !strings.HasSuffix(newContent, lineEnding) {
		newContent += lineEnding
	}

	result.MissingKeys = missingKeys
	result.ExtraKeys = extraKeys
	result.PreservedValues = preserved
	result.Changed = true
	return result, newContent, originalContent, nil
}
