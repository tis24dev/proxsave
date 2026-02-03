package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/pkg/utils"
)

type envValueKind int

const (
	envValueKindLine envValueKind = iota
	envValueKindBlock
)

type envValue struct {
	kind       envValueKind
	rawValue   string
	blockLines []string
	comment    string
}

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
	// Warnings includes non-fatal parsing or merge issues detected while upgrading.
	Warnings []string
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
//  1. Parse the existing config file and collect all KEY=VALUE entries.
//  2. Walk the template line-by-line:
//     - Comments/blank lines are copied as-is.
//     - For KEY=VALUE lines:
//     * If the user has values for that key, all of them are written
//     (one KEY=VALUE per line, in original order).
//     * Otherwise the template's line is kept and the key is recorded as
//     "missing" (a new default added).
//  3. Keys present in the user config but not in the template are appended
//     to a "Custom keys" section at the bottom of the file.
//  4. The original file is backed up before writing the new version.
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
//
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
	userValues, userKeyOrder, caseMap, caseConflicts, warnings, err := parseEnvValues(originalLines)
	if err != nil {
		return result, "", originalContent, fmt.Errorf("failed to parse config %s: %w", configPath, err)
	}

	// 2. Walk the template line-by-line, merging values.
	template := DefaultEnvTemplate()
	normalizedTemplate := strings.ReplaceAll(template, "\r\n", "\n")
	templateLines := strings.Split(normalizedTemplate, "\n")

	templateKeys := make(map[string]bool)
	templateKeyByUpper := make(map[string]string)
	missingKeys := make([]string, 0)
	newLines := make([]string, 0, len(templateLines)+len(userValues))
	processedUserKeys := make(map[string]bool) // Track which user keys (original case) have been used

	for i := 0; i < len(templateLines); i++ {
		line := templateLines[i]
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			newLines = append(newLines, line)
			continue
		}

		key, _, _, ok := splitKeyValueRaw(line)
		if !ok || key == "" {
			newLines = append(newLines, line)
			continue
		}

		templateKeys[key] = true
		upperKey := strings.ToUpper(key)
		if existing, ok := templateKeyByUpper[upperKey]; ok {
			if existing != key {
				warnings = append(warnings, fmt.Sprintf("Template contains duplicate keys differing only by case: %q and %q", existing, key))
			}
		} else {
			templateKeyByUpper[upperKey] = key
		}

		// Logic to find the user's values for this key.
		// 1. Try exact match
		targetUserKey := key
		if _, ok := userValues[key]; !ok {
			// 2. Try case-insensitive match
			if mappedKey, ok := caseMap[strings.ToUpper(key)]; ok {
				targetUserKey = mappedKey
			}
		}

		// Handle block values
		if blockValueKeys[key] && trimmed == fmt.Sprintf("%s=\"", key) {
			blockEnd, err := findClosingQuoteLine(templateLines, i+1)
			if err != nil {
				return result, "", originalContent, fmt.Errorf("template %s block invalid: %w", key, err)
			}

			if values, ok := userValues[targetUserKey]; ok && len(values) > 0 {
				processedUserKeys[targetUserKey] = true
				for _, v := range values {
					// Use TEMPLATE Key casing to enforce consistency
					newLines = append(newLines, renderEnvValue(key, v)...)
				}
			} else {
				missingKeys = append(missingKeys, key)
				newLines = append(newLines, templateLines[i:blockEnd+1]...)
			}

			i = blockEnd
			continue
		}

		if values, ok := userValues[targetUserKey]; ok && len(values) > 0 {
			processedUserKeys[targetUserKey] = true
			for _, v := range values {
				// Use TEMPLATE Key casing to enforce consistency
				newLines = append(newLines, renderEnvValue(key, v)...)
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
		if processedUserKeys[key] {
			continue
		}
		// If exact match was in template keys (should have been processed above), skip
		if templateKeys[key] {
			continue
		}
		// If case-insensitive match was in template keys (should have been processed), skip
		// Check by upper casing the user key and seeing if it exists in templateKeys?
		// But wait, templateKeys stores exact keys.
		// If user has "Backup_Enabled", and template has "BACKUP_ENABLED".
		// We processed "BACKUP_ENABLED", found "Backup_Enabled" via caseMap, and marked "Backup_Enabled" as processed.
		// So `processedUserKeys["Backup_Enabled"]` is true. We skip.
		// Correct.

		upperKey := strings.ToUpper(key)
		if templateKey, ok := templateKeyByUpper[upperKey]; ok && templateKey != key {
			if caseConflicts == nil || !caseConflicts[upperKey] {
				warnings = append(warnings, fmt.Sprintf("Key %q differs only by case from template key %q; preserved as custom entry", key, templateKey))
			}
		}

		values := userValues[key]
		if len(values) == 0 {
			continue
		}
		extraKeys = append(extraKeys, key)
		for _, v := range values {
			// Preserve USER's original key casing for extras
			extraLines = append(extraLines, renderEnvValue(key, v)...)
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

	// Count preserved values
	preserved := 0
	for key := range processedUserKeys {
		preserved += len(userValues[key])
	}

	// If nothing changed (no missing keys and no extras), we can return early.
	// BUT checking "nothing changed" is harder now because we might have renamed keys.
	// If we renamed a key, the content CHANGED.
	// So we should compare normalized content?
	// Or just assume if we parsed everything and re-rendered, and it matches original string...

	newContent := strings.Join(newLines, lineEnding)
	// Preserve trailing newline if template had one.
	if strings.HasSuffix(normalizedTemplate, "\n") && !strings.HasSuffix(newContent, lineEnding) {
		newContent += lineEnding
	}

	if newContent == string(originalContent) {
		result.Changed = false
		result.Warnings = warnings
		result.PreservedValues = preserved
		return result, "", originalContent, nil
	}

	result.MissingKeys = missingKeys
	result.ExtraKeys = extraKeys
	result.PreservedValues = preserved
	result.Warnings = warnings
	result.Changed = true
	return result, newContent, originalContent, nil
}

func parseEnvValues(lines []string) (map[string][]envValue, []string, map[string]string, map[string]bool, []string, error) {
	userValues := make(map[string][]envValue)
	userKeyOrder := make([]string, 0)
	caseMap := make(map[string]string) // UPPER -> original
	caseConflicts := make(map[string]bool)
	warnings := make([]string, 0)

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}

		key, rawValue, comment, ok := splitKeyValueRaw(line)
		if !ok || key == "" {
			if trimmed != "" {
				warnings = append(warnings, fmt.Sprintf("Ignored line %d: not a KEY=VALUE entry", i+1))
			}
			continue
		}

		upperKey := strings.ToUpper(key)
		if existing, ok := caseMap[upperKey]; ok && existing != key {
			caseConflicts[upperKey] = true
			warnings = append(warnings, fmt.Sprintf("Duplicate keys differ only by case: %q and %q (using last occurrence %q)", existing, key, key))
		}

		caseMap[upperKey] = key

		if blockValueKeys[key] && trimmed == fmt.Sprintf("%s=\"", key) {
			blockLines := make([]string, 0)
			blockEnd, err := findClosingQuoteLine(lines, i+1)
			if err != nil {
				return nil, nil, nil, nil, nil, fmt.Errorf("unterminated multi-line value for %s starting at line %d", key, i+1)
			}
			blockLines = append(blockLines, lines[i+1:blockEnd]...)

			if _, seen := userValues[key]; !seen {
				userKeyOrder = append(userKeyOrder, key)
			}
			userValues[key] = append(userValues[key], envValue{kind: envValueKindBlock, blockLines: blockLines})

			i = blockEnd
			continue
		}

		if _, seen := userValues[key]; !seen {
			userKeyOrder = append(userKeyOrder, key)
		}
		userValues[key] = append(userValues[key], envValue{kind: envValueKindLine, rawValue: rawValue, comment: comment})
	}

	return userValues, userKeyOrder, caseMap, caseConflicts, warnings, nil
}

func splitKeyValueRaw(line string) (string, string, string, bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", "", false
	}

	key := strings.TrimSpace(parts[0])
	// Handle legacy "export KEY=VALUE" lines
	if strings.HasPrefix(key, "export ") {
		key = strings.TrimSpace(strings.TrimPrefix(key, "export "))
	}
	// Also handle tab separation just in case "export\tKEY"
	if strings.HasPrefix(key, "export\t") {
		key = strings.TrimSpace(strings.TrimPrefix(key, "export\t"))
	}

	valuePart := strings.TrimSpace(parts[1])

	// Remove inline comments (but respect quotes)
	value := valuePart
	comment := ""

	if strings.HasPrefix(valuePart, "\"") || strings.HasPrefix(valuePart, "'") {
		quote := valuePart[0]
		endIdx := strings.IndexByte(valuePart[1:], quote)
		if endIdx >= 0 {
			value = valuePart[:endIdx+2]
			// Check for comment after quote
			rest := valuePart[endIdx+2:]
			if idx := strings.Index(rest, "#"); idx >= 0 {
				comment = strings.TrimSpace(rest[idx:])
			}
		}
		return key, value, comment, true
	}

	// Not quoted, remove everything after #
	if idx := strings.Index(valuePart, "#"); idx >= 0 {
		value = strings.TrimSpace(valuePart[:idx])
		comment = strings.TrimSpace(valuePart[idx:])
	}

	return key, value, comment, true
}

func findClosingQuoteLine(lines []string, start int) (int, error) {
	for i := start; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "\"" {
			return i, nil
		}
	}
	return 0, fmt.Errorf("closing quote not found")
}

func renderEnvValue(key string, value envValue) []string {
	if value.kind == envValueKindBlock {
		lines := []string{fmt.Sprintf("%s=\"", key)}
		lines = append(lines, value.blockLines...)
		lines = append(lines, "\"")
		return lines
	}
	line := fmt.Sprintf("%s=%s", key, value.rawValue)
	if value.comment != "" {
		line += " " + value.comment
	}
	return []string{line}
}
