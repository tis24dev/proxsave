package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/pkg/utils"
)

// EnvMigrationSummary describes the outcome of a legacy -> Go config migration.
type EnvMigrationSummary struct {
	OutputPath         string
	BackupPath         string
	MigratedKeys       map[string]string
	UnmappedLegacyKeys []string
	AutoDisabledCeph   bool
}

// migrationRule describes how to map one or more legacy keys into a template key.
type migrationRule struct {
	LegacyKeys []string
	Transform  func(string) (string, bool)
}

func (r migrationRule) apply(values map[string]string) (string, string, bool) {
	for _, legacyKey := range r.LegacyKeys {
		if val, ok := values[legacyKey]; ok && strings.TrimSpace(val) != "" {
			if r.Transform != nil {
				if newVal, ok := r.Transform(val); ok {
					return newVal, legacyKey, true
				}
				continue
			}
			return val, legacyKey, true
		}
	}
	return "", "", false
}

var migrationRules = map[string]migrationRule{
	"BACKUP_PATH":                 {LegacyKeys: []string{"LOCAL_BACKUP_PATH"}},
	"LOG_PATH":                    {LegacyKeys: []string{"LOCAL_LOG_PATH"}},
	"SECONDARY_ENABLED":           {LegacyKeys: []string{"ENABLE_SECONDARY_BACKUP"}},
	"SECONDARY_PATH":              {LegacyKeys: []string{"SECONDARY_BACKUP_PATH"}},
	"CLOUD_ENABLED":               {LegacyKeys: []string{"ENABLE_CLOUD_BACKUP"}},
	"CLOUD_REMOTE":                {LegacyKeys: []string{"RCLONE_REMOTE"}},
	"CLOUD_REMOTE_PATH":           {LegacyKeys: []string{"CLOUD_BACKUP_PATH"}},
	"RCLONE_TIMEOUT_CONNECTION":   {LegacyKeys: []string{"CLOUD_CONNECTIVITY_TIMEOUT"}},
	"BACKUP_NETWORK_CONFIGS":      {LegacyKeys: []string{"BACKUP_NETWORK_CONFIG"}},
	"BACKUP_REMOTE_CONFIGS":       {LegacyKeys: []string{"BACKUP_REMOTE_CFG"}},
	"BACKUP_CRON_JOBS":            {LegacyKeys: []string{"BACKUP_CRONTABS"}},
	"METRICS_ENABLED":             {LegacyKeys: []string{"PROMETHEUS_ENABLED"}},
	"METRICS_PATH":                {LegacyKeys: []string{"PROMETHEUS_TEXTFILE_DIR"}},
	"PXAR_FILE_INCLUDE_PATTERN":   {LegacyKeys: []string{"PXAR_INCLUDE_PATTERN"}},
	"MIN_DISK_SPACE_SECONDARY_GB": {LegacyKeys: []string{"STORAGE_WARNING_THRESHOLD_SECONDARY"}},
	"CONTINUE_ON_SECURITY_ISSUES": {LegacyKeys: []string{"ABORT_ON_SECURITY_ISSUES"}, Transform: invertBool},
	"USE_COLOR":                   {LegacyKeys: []string{"DISABLE_COLORS"}, Transform: invertBool},
	"SECURITY_CHECK_ENABLED":      {LegacyKeys: []string{"FULL_SECURITY_CHECK"}},
}

// MigrateLegacyEnv creates a new Go-style backup.env by reading the legacy Bash
// configuration and merging it with the embedded template.
func MigrateLegacyEnv(legacyPath, outputPath string) (*EnvMigrationSummary, error) {
	summary, mergedContent, err := PlanLegacyEnvMigration(legacyPath, outputPath)
	if err != nil {
		return nil, err
	}

	if utils.FileExists(outputPath) {
		if summary.BackupPath, err = createMigrationBackup(outputPath); err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return nil, fmt.Errorf("failed to create configuration directory: %w", err)
	}

	tmpPath := outputPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(mergedContent), 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to write configuration file: %w", err)
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("failed to finalize configuration file: %w", err)
	}

	cfg, err := LoadConfig(outputPath)
	if err != nil {
		restoreMigratedConfig(summary, outputPath)
		return nil, fmt.Errorf("failed to reload migrated configuration: %w", err)
	}
	if err := validateMigratedConfig(cfg); err != nil {
		restoreMigratedConfig(summary, outputPath)
		return nil, fmt.Errorf("invalid migrated configuration: %w", err)
	}

	return summary, nil
}

func mergeTemplateWithLegacy(template string, legacy map[string]string) (string, *EnvMigrationSummary) {
	template = strings.ReplaceAll(template, "\r\n", "\n")
	lines := strings.Split(template, "\n")

	summary := &EnvMigrationSummary{
		MigratedKeys: make(map[string]string),
	}
	usedLegacy := make(map[string]bool)
	outLines := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			outLines = append(outLines, line)
			continue
		}

		key, _, ok := utils.SplitKeyValue(line)
		if !ok {
			outLines = append(outLines, line)
			continue
		}

		if val, ok := legacy[key]; ok && strings.TrimSpace(val) != "" {
			outLines = append(outLines, renderValueLines(key, val)...)
			summary.MigratedKeys[key] = key
			usedLegacy[key] = true
			continue
		}

		if rule, ok := migrationRules[key]; ok {
			if newVal, sourceKey, applied := rule.apply(legacy); applied {
				outLines = append(outLines, renderValueLines(key, newVal)...)
				summary.MigratedKeys[key] = sourceKey
				usedLegacy[sourceKey] = true
				continue
			}
		}

		outLines = append(outLines, line)
	}

	for key := range legacy {
		if !usedLegacy[key] {
			summary.UnmappedLegacyKeys = append(summary.UnmappedLegacyKeys, key)
		}
	}
	sort.Strings(summary.UnmappedLegacyKeys)

	merged := strings.Join(outLines, "\n")
	if strings.HasSuffix(template, "\n") && !strings.HasSuffix(merged, "\n") {
		merged += "\n"
	}
	return merged, summary
}

func renderValueLines(key, value string) []string {
	if blockValueKeys[key] {
		blockLines := strings.Split(value, "\n")
		result := []string{fmt.Sprintf("%s=\"", key)}
		result = append(result, blockLines...)
		result = append(result, "\"")
		return result
	}
	return []string{fmt.Sprintf("%s=%s", key, value)}
}

func invertBool(val string) (string, bool) {
	return boolToString(!utils.ParseBool(val)), true
}

func boolToString(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func createMigrationBackup(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read existing configuration: %w", err)
	}
	backupPath := fmt.Sprintf("%s.migration-backup.%s", path, time.Now().Format("20060102_150405"))
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to create configuration backup: %w", err)
	}
	return backupPath, nil
}

func restoreMigratedConfig(summary *EnvMigrationSummary, outputPath string) {
	if summary != nil && summary.BackupPath != "" {
		_ = os.Rename(summary.BackupPath, outputPath)
	} else {
		_ = os.Remove(outputPath)
	}
}

func validateMigratedConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.BackupPath) == "" {
		return fmt.Errorf("BACKUP_PATH cannot be empty")
	}
	if strings.TrimSpace(cfg.LogPath) == "" {
		return fmt.Errorf("LOG_PATH cannot be empty")
	}
	if cfg.SecondaryEnabled && strings.TrimSpace(cfg.SecondaryPath) == "" {
		return fmt.Errorf("SECONDARY_PATH required when SECONDARY_ENABLED=true")
	}
	if cfg.CloudEnabled && strings.TrimSpace(cfg.CloudRemote) == "" {
		return fmt.Errorf("CLOUD_REMOTE required when CLOUD_ENABLED=true")
	}
	if cfg.SetBackupPermissions {
		if strings.TrimSpace(cfg.BackupUser) == "" || strings.TrimSpace(cfg.BackupGroup) == "" {
			return fmt.Errorf("BACKUP_USER/BACKUP_GROUP must be set when SET_BACKUP_PERMISSIONS=true")
		}
	}
	return nil
}

// PlanLegacyEnvMigration computes what the migrated configuration would look like
// without writing any files.
func PlanLegacyEnvMigration(legacyPath, outputPath string) (*EnvMigrationSummary, string, error) {
	if !utils.FileExists(legacyPath) {
		return nil, "", fmt.Errorf("legacy configuration not found: %s", legacyPath)
	}

	legacyValues, err := parseEnvFile(legacyPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to parse legacy configuration: %w", err)
	}
	cephAutoDisabled := autoDisableLegacyCephIfUnavailable(legacyValues)

	var baseTemplate string
	if utils.FileExists(outputPath) {
		content, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to read existing configuration: %w", err)
		}
		baseTemplate = string(content)
	} else {
		baseTemplate = DefaultEnvTemplate()
	}

	mergedContent, summary := mergeTemplateWithLegacy(baseTemplate, legacyValues)
	summary.OutputPath = outputPath
	summary.AutoDisabledCeph = cephAutoDisabled

	return summary, mergedContent, nil
}

func autoDisableLegacyCephIfUnavailable(values map[string]string) bool {
	raw, ok := values["BACKUP_CEPH_CONFIG"]
	if ok && !utils.ParseBool(raw) {
		return false
	}

	if cephPresenceChecker(gatherCephProbePaths(values)) {
		return false
	}

	values["BACKUP_CEPH_CONFIG"] = "false"
	return true
}

var cephPresenceChecker = defaultCephPresenceChecker

func gatherCephProbePaths(values map[string]string) []string {
	paths := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}

	add(values["CEPH_CONFIG_PATH"])
	add("/etc/ceph")
	add("/etc/pve")
	add("/var/lib/ceph")

	return paths
}

func defaultCephPresenceChecker(paths []string) bool {
	for _, path := range paths {
		if cephPathHasConfig(path) {
			return true
		}
	}
	return false
}

func cephPathHasConfig(path string) bool {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." || cleaned == string(filepath.Separator) {
		return false
	}

	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil && strings.TrimSpace(resolved) != "" {
		cleaned = resolved
	}

	info, err := os.Stat(cleaned)
	if err != nil {
		return false
	}

	if info.IsDir() {
		entries, err := os.ReadDir(cleaned)
		if err != nil {
			// If we cannot read the directory (permissions, etc.), assume Ceph might be present.
			return true
		}
		for _, entry := range entries {
			name := strings.ToLower(entry.Name())
			if entry.IsDir() {
				if strings.Contains(name, "ceph") {
					return true
				}
				continue
			}
			if name == "ceph.conf" || strings.HasSuffix(name, ".keyring") {
				return true
			}
		}
		return false
	}

	name := strings.ToLower(info.Name())
	return name == "ceph.conf" || strings.HasSuffix(name, ".keyring")
}
