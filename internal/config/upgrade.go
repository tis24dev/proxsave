package config

import (
	"fmt"
	"os"
	"sort"
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

type keyRange struct {
	start int
	end   int
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
	userValues, userKeyOrder, caseMap, caseConflicts, warnings, userRanges, err := parseEnvValues(originalLines)
	if err != nil {
		return result, "", originalContent, fmt.Errorf("failed to parse config %s: %w", configPath, err)
	}

	// 2. Walk the template line-by-line and collect template entries.
	template := DefaultEnvTemplate()
	normalizedTemplate := strings.ReplaceAll(template, "\r\n", "\n")
	templateLines := strings.Split(normalizedTemplate, "\n")

	type templateEntry struct {
		key   string
		upper string
		lines []string
		index int
	}

	templateEntries := make([]templateEntry, 0)
	templateKeyByUpper := make(map[string]string)

	for i := 0; i < len(templateLines); i++ {
		line := templateLines[i]
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}

		key, _, _, ok := splitKeyValueRaw(line)
		if !ok || key == "" {
			continue
		}

		upperKey := strings.ToUpper(key)
		if existing, ok := templateKeyByUpper[upperKey]; ok {
			if existing != key {
				warnings = append(warnings, fmt.Sprintf("Template contains duplicate keys differing only by case: %q and %q", existing, key))
			}
		} else {
			templateKeyByUpper[upperKey] = key
		}

		if blockValueKeys[upperKey] && trimmed == fmt.Sprintf("%s=\"", key) {
			blockEnd, err := findClosingQuoteLine(templateLines, i+1)
			if err != nil {
				return result, "", originalContent, fmt.Errorf("template %s block invalid: %w", key, err)
			}
			templateEntries = append(templateEntries, templateEntry{
				key:   key,
				upper: upperKey,
				lines: templateLines[i : blockEnd+1],
				index: len(templateEntries),
			})
			i = blockEnd
			continue
		}
		templateEntries = append(templateEntries, templateEntry{
			key:   key,
			upper: upperKey,
			lines: []string{line},
			index: len(templateEntries),
		})
	}

	// 3. Compute missing and extra keys.
	missingKeys := make([]string, 0)
	missingEntries := make([]templateEntry, 0)
	for _, entry := range templateEntries {
		targetUserKey := entry.key
		if _, ok := userValues[entry.key]; !ok {
			if mappedKey, ok := caseMap[entry.upper]; ok {
				targetUserKey = mappedKey
			}
		}
		if values, ok := userValues[targetUserKey]; ok && len(values) > 0 {
			continue
		}
		missingKeys = append(missingKeys, entry.key)
		missingEntries = append(missingEntries, entry)
	}

	extraKeys := make([]string, 0)
	for _, key := range userKeyOrder {
		upperKey := strings.ToUpper(key)
		if _, ok := templateKeyByUpper[upperKey]; !ok {
			extraKeys = append(extraKeys, key)
			continue
		}
		if caseConflicts[upperKey] && caseMap[upperKey] != key {
			extraKeys = append(extraKeys, key)
			continue
		}
		if templateKey, ok := templateKeyByUpper[upperKey]; ok && templateKey != key && !caseConflicts[upperKey] {
			warnings = append(warnings, fmt.Sprintf("Key %q differs only by case from template key %q; preserved as custom entry", key, templateKey))
		}
	}

	// Count preserved values
	preserved := 0
	for key, values := range userValues {
		if _, ok := templateKeyByUpper[strings.ToUpper(key)]; ok {
			preserved += len(values)
		}
	}

	// If nothing is missing, do not rewrite the file.
	if len(missingKeys) == 0 {
		result.Changed = false
		result.Warnings = warnings
		result.ExtraKeys = extraKeys
		result.PreservedValues = preserved
		return result, "", originalContent, nil
	}

	type insertOp struct {
		index int
		lines []string
		order int
	}

	hasTrailingNewline := strings.HasSuffix(normalizedOriginal, "\n")
	appendIndex := len(originalLines)
	if hasTrailingNewline && len(originalLines) > 0 && originalLines[len(originalLines)-1] == "" {
		appendIndex = len(originalLines) - 1
	}
	normalizeInsertIndex := func(idx int) int {
		if idx < 0 {
			return 0
		}
		if idx > appendIndex {
			return appendIndex
		}
		if hasTrailingNewline && idx == len(originalLines) {
			return appendIndex
		}
		return idx
	}

	resolveUserKey := func(entry templateEntry) (string, bool) {
		if values, ok := userValues[entry.key]; ok && len(values) > 0 {
			return entry.key, true
		}
		if mappedKey, ok := caseMap[entry.upper]; ok {
			if values, ok := userValues[mappedKey]; ok && len(values) > 0 {
				return mappedKey, true
			}
		}
		return "", false
	}

	findPrevAnchor := func(entryIndex int) (int, bool) {
		for i := entryIndex - 1; i >= 0; i-- {
			if userKey, ok := resolveUserKey(templateEntries[i]); ok {
				ranges := userRanges[userKey]
				if len(ranges) == 0 {
					continue
				}
				return ranges[len(ranges)-1].end + 1, true
			}
		}
		return 0, false
	}

	ops := make([]insertOp, 0, len(missingEntries))
	unanchored := make([]templateEntry, 0)
	for _, entry := range missingEntries {
		insertIndex := appendIndex
		if prev, ok := findPrevAnchor(entry.index); ok {
			insertIndex = prev
		} else {
			unanchored = append(unanchored, entry)
			continue
		}
		insertIndex = normalizeInsertIndex(insertIndex)
		ops = append(ops, insertOp{
			index: insertIndex,
			lines: entry.lines,
			order: entry.index,
		})
	}

	if len(unanchored) > 0 {
		section := []string{"# Added by upgrade"}
		if appendIndex > 0 && strings.TrimSpace(originalLines[appendIndex-1]) != "" {
			section = append([]string{""}, section...)
		}
		for _, entry := range unanchored {
			section = append(section, entry.lines...)
		}
		ops = append(ops, insertOp{
			index: normalizeInsertIndex(appendIndex),
			lines: section,
			order: len(templateEntries),
		})
	}

	sort.SliceStable(ops, func(i, j int) bool {
		if ops[i].index != ops[j].index {
			return ops[i].index < ops[j].index
		}
		return ops[i].order < ops[j].order
	})

	newLines := make([]string, 0, len(originalLines)+len(ops))
	opIdx := 0
	for i := 0; i < len(originalLines); i++ {
		for opIdx < len(ops) && ops[opIdx].index == i {
			newLines = append(newLines, ops[opIdx].lines...)
			opIdx++
		}
		newLines = append(newLines, originalLines[i])
	}
	for opIdx < len(ops) {
		newLines = append(newLines, ops[opIdx].lines...)
		opIdx++
	}

	newContent := strings.Join(newLines, lineEnding)
	if newContent == string(originalContent) {
		result.Changed = false
		result.Warnings = warnings
		result.ExtraKeys = extraKeys
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

func parseEnvValues(lines []string) (map[string][]envValue, []string, map[string]string, map[string]bool, []string, map[string][]keyRange, error) {
	userValues := make(map[string][]envValue)
	userKeyOrder := make([]string, 0)
	caseMap := make(map[string]string) // UPPER -> original
	caseConflicts := make(map[string]bool)
	warnings := make([]string, 0)
	userRanges := make(map[string][]keyRange)

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

		if blockValueKeys[upperKey] && trimmed == fmt.Sprintf("%s=\"", key) {
			blockLines := make([]string, 0)
			blockEnd, err := findClosingQuoteLine(lines, i+1)
			if err != nil {
				return nil, nil, nil, nil, nil, nil, fmt.Errorf("unterminated multi-line value for %s starting at line %d", key, i+1)
			}
			blockLines = append(blockLines, lines[i+1:blockEnd]...)

			if _, seen := userValues[key]; !seen {
				userKeyOrder = append(userKeyOrder, key)
			}
			userValues[key] = append(userValues[key], envValue{kind: envValueKindBlock, blockLines: blockLines})
			userRanges[key] = append(userRanges[key], keyRange{start: i, end: blockEnd})

			i = blockEnd
			continue
		}

		if _, seen := userValues[key]; !seen {
			userKeyOrder = append(userKeyOrder, key)
		}
		userValues[key] = append(userValues[key], envValue{kind: envValueKindLine, rawValue: rawValue, comment: comment})
		userRanges[key] = append(userRanges[key], keyRange{start: i, end: i})
	}

	return userValues, userKeyOrder, caseMap, caseConflicts, warnings, userRanges, nil
}

func splitKeyValueRaw(line string) (string, string, string, bool) {
	comment := ""
	body := line
	if commentIdx := utils.FindInlineCommentIndex(line); commentIdx >= 0 {
		comment = strings.TrimSpace(line[commentIdx:])
		body = line[:commentIdx]
	}

	parts := strings.SplitN(body, "=", 2)
	if len(parts) != 2 {
		return "", "", "", false
	}

	key := strings.TrimSpace(parts[0])
	// Handle legacy "export KEY=VALUE" lines with arbitrary whitespace
	if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
		key = fields[1]
	}

	valuePart := strings.TrimSpace(parts[1])

	value := valuePart
	if strings.HasPrefix(valuePart, "\"") || strings.HasPrefix(valuePart, "'") {
		quote := valuePart[0]
		if endIdx := utils.FindClosingQuoteIndex(valuePart, quote); endIdx >= 0 {
			value = valuePart[:endIdx+1]
		}
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
