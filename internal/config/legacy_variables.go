package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tis24dev/proxsave/pkg/utils"
)

// legacyValueKind is how the loader used to decide that a value written under a
// variable was the one it read: the four multi-name helpers it had did not agree.
type legacyValueKind int

const (
	// legacyString is read when its value is not empty.
	legacyString legacyValueKind = iota
	// legacyBool is read whenever it is assigned, an empty value included.
	legacyBool
	// legacyInt is read when its value is an integer.
	legacyInt
	// legacyList is read when its value holds at least one item.
	legacyList
)

// legacyVariable is a variable name of the Bash version of ProxSave together with the
// name that replaced it. The loader reads only the current names; the configuration
// upgrade and the install wizard carry a value still written under the old name over
// to the current one (migrateLegacyLines).
type legacyVariable struct {
	Name    string
	Current string
	// First records that the loader used to consult the old name BEFORE the current
	// one, so with both assigned it was the old line that took effect.
	First bool
	Kind  legacyValueKind
}

// legacyVariables lists every old name the loader used to read, in the order the
// configuration upgrade reports them.
var legacyVariables = []legacyVariable{
	{Name: "LOCAL_BACKUP_PATH", Current: "BACKUP_PATH", First: true, Kind: legacyString},
	{Name: "LOCAL_LOG_PATH", Current: "LOG_PATH", First: true, Kind: legacyString},
	{Name: "ENABLE_SECONDARY_BACKUP", Current: "SECONDARY_ENABLED", First: true, Kind: legacyBool},
	{Name: "SECONDARY_BACKUP_PATH", Current: "SECONDARY_PATH", First: true, Kind: legacyString},
	{Name: "ENABLE_CLOUD_BACKUP", Current: "CLOUD_ENABLED", First: true, Kind: legacyBool},
	{Name: "RCLONE_REMOTE", Current: "CLOUD_REMOTE", First: true, Kind: legacyString},
	{Name: "PROMETHEUS_ENABLED", Current: "METRICS_ENABLED", First: true, Kind: legacyBool},

	{Name: "TELEGRAM_ENABLE", Current: telegramEnabledKey, Kind: legacyBool},
	{Name: "EMAIL_ENABLE", Current: emailEnabledKey, Kind: legacyBool},
	{Name: "GOTIFY_ENABLE", Current: gotifyEnabledKey, Kind: legacyBool},
	{Name: "WEBHOOK_ENABLE", Current: webhookEnabledKey, Kind: legacyBool},
	{Name: "EMAIL_FALLBACK_PMF", Current: emailFallbackSendmailKey, Kind: legacyBool},

	{Name: "FULL_SECURITY_CHECK", Current: "SECURITY_CHECK_ENABLED", Kind: legacyBool},
	{Name: "CLOUD_CONNECTIVITY_TIMEOUT", Current: "RCLONE_TIMEOUT_CONNECTION", Kind: legacyInt},
	{Name: "LOCAL_RETENTION_DAYS", Current: "MAX_LOCAL_BACKUPS", Kind: legacyInt},
	{Name: "SECONDARY_RETENTION_DAYS", Current: "MAX_SECONDARY_BACKUPS", Kind: legacyInt},
	{Name: "CLOUD_RETENTION_DAYS", Current: "MAX_CLOUD_BACKUPS", Kind: legacyInt},
	{Name: "PROMETHEUS_TEXTFILE_DIR", Current: "METRICS_PATH", Kind: legacyString},
	{Name: "BACKUP_REMOTE_CFG", Current: "BACKUP_REMOTE_CONFIGS", Kind: legacyBool},
	{Name: "BACKUP_NETWORK_CONFIG", Current: "BACKUP_NETWORK_CONFIGS", Kind: legacyBool},
	{Name: "BACKUP_PXAR_FILES", Current: "PXAR_SCAN_ENABLE", Kind: legacyBool},
	{Name: "PXAR_INCLUDE_PATTERN", Current: "PXAR_FILE_INCLUDE_PATTERN", Kind: legacyList},
	{Name: "BACKUP_CRONTABS", Current: "BACKUP_CRON_JOBS", Kind: legacyBool},
	{Name: "AGE_RECIPIENTS", Current: "AGE_RECIPIENT", Kind: legacyList},
}

// LegacyChange is one old variable name a configuration rewrite took out of the file.
type LegacyChange struct {
	// Name is the old name, Current the name that replaced it.
	Name    string
	Current string
	// Lines are the lines, numbered from 1 in the file as it was read, that assigned
	// the old name.
	Lines []int
	// Renamed means the old line was the one in effect: its value now sits under the
	// current name, and any line that assigned the current name was dropped. When it is
	// false the old line had no effect and was only dropped.
	Renamed bool
}

// legacyAssignment is one line assigning a variable, with the value the loader reads.
type legacyAssignment struct {
	index int
	value string
}

// taken reports whether the loader used to read the variable from these assignments
// rather than moving on to the next name or to the default.
func (k legacyValueKind) taken(at []legacyAssignment) bool {
	if len(at) == 0 {
		return false
	}
	last := at[len(at)-1].value
	switch k {
	case legacyBool:
		return true
	case legacyInt:
		_, err := strconv.Atoi(last)
		return err == nil
	case legacyList:
		for _, a := range at {
			if strings.Trim(a.value, " \t,;:|\n\"'") != "" {
				return true
			}
		}
		return false
	default:
		return last != ""
	}
}

// migrateLegacyLines takes every old variable name out of the lines of a backup.env,
// keeping the value the loader used to read. An old name that was in effect becomes the
// current name, in place of the line of the current name it overrode when there is one;
// an old name that had no effect is dropped. Lines are read the way parseEnvFile reads
// them, multi-line blocks included.
func migrateLegacyLines(lines []string) ([]string, []LegacyChange) {
	assigned := make(map[string][]legacyAssignment)
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}
		key, value, ok := utils.SplitKeyValue(line)
		if !ok {
			continue
		}
		if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
			key = fields[1]
		}
		upperKey := strings.ToUpper(key)
		assigned[upperKey] = append(assigned[upperKey], legacyAssignment{index: i, value: value})
		if blockValueKeys[upperKey] && trimmed == fmt.Sprintf("%s=\"", key) {
			for i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "\"" {
				i++
			}
			i++
		}
	}

	var changes []LegacyChange
	drop := make(map[int]bool)
	replace := make(map[int]string)
	for _, legacy := range legacyVariables {
		old := assigned[legacy.Name]
		if len(old) == 0 {
			continue
		}
		current := assigned[legacy.Current]
		inEffect := legacy.Kind.taken(old) && (legacy.First || !legacy.Kind.taken(current))
		change := LegacyChange{Name: legacy.Name, Current: legacy.Current, Renamed: inEffect}
		for _, a := range old {
			change.Lines = append(change.Lines, a.index+1)
			drop[a.index] = true
		}
		if inEffect {
			// No old name was a multi-value variable, so only its last line was read.
			// Renamed, it takes the place of the last line of the current name when the
			// file has one, so the variable stays in the section that documents it, and
			// the lines of the current name it overrode go.
			renamed := renameAssignedKey(lines[old[len(old)-1].index], legacy.Name, legacy.Current)
			at := old[len(old)-1].index
			if len(current) > 0 {
				for _, a := range current {
					drop[a.index] = true
				}
				at = current[len(current)-1].index
			}
			delete(drop, at)
			replace[at] = renamed
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		return lines, nil
	}

	out := make([]string, 0, len(lines))
	for i, line := range lines {
		if drop[i] {
			continue
		}
		if renamed, ok := replace[i]; ok {
			line = renamed
		}
		out = append(out, line)
	}
	return out, changes
}

// renameAssignedKey rewrites the variable name of one KEY=VALUE line, leaving an
// "export" prefix, the spacing, the value and any comment as they were.
func renameAssignedKey(line, name, current string) string {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return line
	}
	at := strings.LastIndex(strings.ToUpper(line[:eq]), name)
	if at < 0 {
		return line
	}
	return line[:at] + current + line[at+len(name):]
}

// MigrateLegacyVariables is migrateLegacyLines for a whole backup.env, keeping its line
// endings. The install wizard calls it on the file it is about to edit, so the wizard
// and the file it writes see the current names only.
func MigrateLegacyVariables(content string) (string, []LegacyChange) {
	lineEnding := "\n"
	if strings.Contains(content, "\r\n") {
		lineEnding = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	migrated, changes := migrateLegacyLines(lines)
	if len(changes) == 0 {
		return content, nil
	}
	return strings.Join(migrated, lineEnding), changes
}
