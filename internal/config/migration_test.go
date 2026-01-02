package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvFileHandlesBlocksAndMultiValues(t *testing.T) {
	tmpDir := t.TempDir()
	envFile := filepath.Join(tmpDir, "legacy.env")
	content := `
# comment
KEY1=value1
BACKUP_EXCLUDE_PATTERNS=*.log
BACKUP_EXCLUDE_PATTERNS=*.tmp
CUSTOM_BACKUP_PATHS="
/etc/custom
/var/lib/custom
"
BACKUP_BLACKLIST="
/tmp
/var/tmp
"
`
	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		t.Fatalf("failed to write temp legacy env: %v", err)
	}

	values, err := parseEnvFile(envFile)
	if err != nil {
		t.Fatalf("parseEnvFile returned error: %v", err)
	}

	if got := values["KEY1"]; got != "value1" {
		t.Fatalf("KEY1 = %q; want %q", got, "value1")
	}

	if got := values["BACKUP_EXCLUDE_PATTERNS"]; got != "*.log\n*.tmp" {
		t.Fatalf("BACKUP_EXCLUDE_PATTERNS = %q; want \"*.log\\n*.tmp\"", got)
	}

	if got := values["CUSTOM_BACKUP_PATHS"]; got != "/etc/custom\n/var/lib/custom" {
		t.Fatalf("CUSTOM_BACKUP_PATHS = %q; want block content", got)
	}

	if got := values["BACKUP_BLACKLIST"]; got != "/tmp\n/var/tmp" {
		t.Fatalf("BACKUP_BLACKLIST = %q; want block content", got)
	}
}

const testTemplate = `# test template
BACKUP_PATH=/default/backup
LOG_PATH=/default/log
KEY1=template
BACKUP_EXCLUDE_PATTERNS=*.tmp
CUSTOM_BACKUP_PATHS="
default
"
BACKUP_BLACKLIST="
temp
"
`

func TestMergeTemplateWithLegacySameAndRenamedKeys(t *testing.T) {
	legacy := map[string]string{
		"KEY1":                    "legacy-value",
		"LOCAL_BACKUP_PATH":       "/legacy/backup",
		"BACKUP_EXCLUDE_PATTERNS": "*.log\n*.tmp",
	}

	merged, summary := mergeTemplateWithLegacy(testTemplate, legacy)

	if !strings.Contains(merged, "KEY1=legacy-value") {
		t.Fatalf("merged template does not contain legacy KEY1 value:\n%s", merged)
	}

	if !strings.Contains(merged, "BACKUP_PATH=/legacy/backup") {
		t.Fatalf("merged template does not apply LOCAL_BACKUP_PATH -> BACKUP_PATH mapping:\n%s", merged)
	}

	if summary.MigratedKeys["BACKUP_PATH"] != "LOCAL_BACKUP_PATH" {
		t.Fatalf("MigratedKeys BACKUP_PATH = %q; want LOCAL_BACKUP_PATH", summary.MigratedKeys["BACKUP_PATH"])
	}

	if summary.MigratedKeys["KEY1"] != "KEY1" {
		t.Fatalf("MigratedKeys KEY1 = %q; want KEY1", summary.MigratedKeys["KEY1"])
	}
}

func TestMergeTemplateWithLegacyTracksUnmappedKeys(t *testing.T) {
	legacy := map[string]string{
		"AUTO_DETECT_DATASTORES": "false",
	}

	_, summary := mergeTemplateWithLegacy(testTemplate, legacy)

	if len(summary.UnmappedLegacyKeys) != 1 || summary.UnmappedLegacyKeys[0] != "AUTO_DETECT_DATASTORES" {
		t.Fatalf("UnmappedLegacyKeys = %v; want [AUTO_DETECT_DATASTORES]", summary.UnmappedLegacyKeys)
	}
}

const baseInstallTemplate = `BACKUP_ENABLED=true
BACKUP_PATH=/default/backup
LOG_PATH=/default/log
SECONDARY_ENABLED=false
CLOUD_ENABLED=false
SET_BACKUP_PERMISSIONS=false
BACKUP_USER=backup
BACKUP_GROUP=backup
`

func TestMigrateLegacyEnvCreatesConfigAndKeepsValues(t *testing.T) {
	withTemplate(t, baseInstallTemplate, func() {
		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")
		if err := os.WriteFile(legacyPath, []byte("LOCAL_BACKUP_PATH=/legacy/backup\n"), 0600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		summary, err := MigrateLegacyEnv(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("MigrateLegacyEnv returned error: %v", err)
		}
		if summary.OutputPath != outputPath {
			t.Fatalf("summary.OutputPath=%s; want %s", summary.OutputPath, outputPath)
		}

		cfg, err := LoadConfig(outputPath)
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if cfg.BackupPath != "/legacy/backup" {
			t.Fatalf("cfg.BackupPath=%s; want /legacy/backup", cfg.BackupPath)
		}
	})
}

func TestMigrateLegacyEnvCreatesBackupWhenOverwriting(t *testing.T) {
	withTemplate(t, baseInstallTemplate, func() {
		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")
		if err := os.WriteFile(legacyPath, []byte("LOCAL_BACKUP_PATH=/new/path\n"), 0600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		originalContent := []byte("ORIGINAL_CONFIG")
		if err := os.WriteFile(outputPath, originalContent, 0600); err != nil {
			t.Fatalf("failed to seed existing config: %v", err)
		}

		summary, err := MigrateLegacyEnv(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("MigrateLegacyEnv returned error: %v", err)
		}
		if summary.BackupPath == "" {
			t.Fatalf("expected backup path when overwriting existing config")
		}

		data, err := os.ReadFile(summary.BackupPath)
		if err != nil {
			t.Fatalf("failed to read backup file: %v", err)
		}
		if string(data) != string(originalContent) {
			t.Fatalf("backup content = %q; want %q", string(data), string(originalContent))
		}
	})
}

const invalidPermissionsTemplate = `BACKUP_ENABLED=true
BACKUP_PATH=/default/backup
LOG_PATH=/default/log
SECONDARY_ENABLED=false
CLOUD_ENABLED=false
SET_BACKUP_PERMISSIONS=true
BACKUP_USER=
BACKUP_GROUP=
`

func TestMigrateLegacyEnvRollsBackOnValidationFailure(t *testing.T) {
	withTemplate(t, invalidPermissionsTemplate, func() {
		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")
		if err := os.WriteFile(legacyPath, []byte(""), 0600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		originalContent := []byte(invalidPermissionsTemplate)
		if err := os.WriteFile(outputPath, originalContent, 0600); err != nil {
			t.Fatalf("failed to seed existing config: %v", err)
		}

		_, err := MigrateLegacyEnv(legacyPath, outputPath)
		if err == nil {
			t.Fatalf("expected validation error, got nil")
		}

		data, err := os.ReadFile(outputPath)
		if err != nil {
			t.Fatalf("expected restored config file: %v", err)
		}
		if string(data) != string(originalContent) {
			t.Fatalf("restored config content = %q; want %q", string(data), string(originalContent))
		}
	})
}

func TestMigrateLegacyEnvAutoDisablesCephWhenUnavailable(t *testing.T) {
	template := baseInstallTemplate + "BACKUP_CEPH_CONFIG=true\nCEPH_CONFIG_PATH=/etc/ceph\n"
	withTemplate(t, template, func() {
		restore := cephPresenceChecker
		cephPresenceChecker = func(paths []string) bool { return false }
		t.Cleanup(func() { cephPresenceChecker = restore })

		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")
		content := "BACKUP_CEPH_CONFIG=true\nCEPH_CONFIG_PATH=/missing/path\n"
		if err := os.WriteFile(legacyPath, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		summary, err := MigrateLegacyEnv(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("MigrateLegacyEnv returned error: %v", err)
		}
		if !summary.AutoDisabledCeph {
			t.Fatalf("expected AutoDisabledCeph to be true")
		}

		cfg, err := LoadConfig(outputPath)
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if cfg.BackupCephConfig {
			t.Fatalf("expected Ceph backup to be disabled when no config detected")
		}
	})
}

func TestMigrateLegacyEnvKeepsCephWhenDetected(t *testing.T) {
	template := baseInstallTemplate + "BACKUP_CEPH_CONFIG=true\nCEPH_CONFIG_PATH=/etc/ceph\n"
	withTemplate(t, template, func() {
		restore := cephPresenceChecker
		cephPresenceChecker = func(paths []string) bool { return true }
		t.Cleanup(func() { cephPresenceChecker = restore })

		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")
		content := "BACKUP_CEPH_CONFIG=true\nCEPH_CONFIG_PATH=/detected/path\n"
		if err := os.WriteFile(legacyPath, []byte(content), 0o600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		summary, err := MigrateLegacyEnv(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("MigrateLegacyEnv returned error: %v", err)
		}
		if summary.AutoDisabledCeph {
			t.Fatalf("did not expect AutoDisabledCeph when Ceph is detected")
		}

		cfg, err := LoadConfig(outputPath)
		if err != nil {
			t.Fatalf("LoadConfig returned error: %v", err)
		}
		if !cfg.BackupCephConfig {
			t.Fatalf("expected Ceph backup to remain enabled")
		}
	})
}

func TestPlanLegacyEnvMigrationFailsWhenLegacyMissing(t *testing.T) {
	withTemplate(t, baseInstallTemplate, func() {
		tmpDir := t.TempDir()
		outputPath := filepath.Join(tmpDir, "out.env")
		if _, _, err := PlanLegacyEnvMigration(filepath.Join(tmpDir, "missing.env"), outputPath); err == nil {
			t.Fatalf("expected error for missing legacy env, got nil")
		}
	})
}

func TestPlanLegacyEnvMigrationUsesExistingConfigAsBase(t *testing.T) {
	withTemplate(t, baseInstallTemplate, func() {
		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")

		if err := os.WriteFile(legacyPath, []byte("LOCAL_BACKUP_PATH=/legacy/backup\n"), 0600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		existingContent := "BACKUP_PATH=/existing/backup\nLOG_PATH=/existing/log\nCUSTOM_SETTING=keep\n"
		if err := os.WriteFile(outputPath, []byte(existingContent), 0600); err != nil {
			t.Fatalf("failed to write existing config: %v", err)
		}

		summary, merged, err := PlanLegacyEnvMigration(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("PlanLegacyEnvMigration returned error: %v", err)
		}
		if summary.OutputPath != outputPath {
			t.Fatalf("summary.OutputPath=%s; want %s", summary.OutputPath, outputPath)
		}

		if !strings.Contains(merged, "CUSTOM_SETTING=keep") {
			t.Fatalf("existing config content not preserved in merge:\n%s", merged)
		}
		if !strings.Contains(merged, "LOG_PATH=/existing/log") {
			t.Fatalf("existing LOG_PATH not preserved:\n%s", merged)
		}
		if !strings.Contains(merged, "BACKUP_PATH=/legacy/backup") {
			t.Fatalf("legacy LOCAL_BACKUP_PATH did not override existing backup path:\n%s", merged)
		}
	})
}

func TestPlanLegacyEnvMigrationFallsBackToTemplateWhenNoExistingConfig(t *testing.T) {
	template := `BACKUP_PATH=/template/backup
LOG_PATH=/template/log
NEW_FLAG=true
`
	withTemplate(t, template, func() {
		tmpDir := t.TempDir()
		legacyPath := filepath.Join(tmpDir, "legacy.env")
		outputPath := filepath.Join(tmpDir, "backup.env")

		if err := os.WriteFile(legacyPath, []byte("LOCAL_BACKUP_PATH=/legacy/backup\n"), 0600); err != nil {
			t.Fatalf("failed to write legacy env: %v", err)
		}

		_, merged, err := PlanLegacyEnvMigration(legacyPath, outputPath)
		if err != nil {
			t.Fatalf("PlanLegacyEnvMigration returned error: %v", err)
		}
		if !strings.Contains(merged, "NEW_FLAG=true") {
			t.Fatalf("expected template-only value NEW_FLAG to be present:\n%s", merged)
		}
		if !strings.Contains(merged, "BACKUP_PATH=/legacy/backup") {
			t.Fatalf("legacy value not merged:\n%s", merged)
		}
	})
}

func TestInvertBoolAndBoolToString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"true", "false"},
		{"1", "false"},
		{"yes", "false"},
		{"enabled", "false"},
		{"0", "true"},
		{"no", "true"},
		{"", "true"},
	}

	for _, tt := range tests {
		got, ok := invertBool(tt.in)
		if !ok {
			t.Fatalf("invertBool(%q) ok=false; want true", tt.in)
		}
		if got != tt.want {
			t.Fatalf("invertBool(%q)=%q; want %q", tt.in, got, tt.want)
		}
	}

	if got := boolToString(true); got != "true" {
		t.Fatalf("boolToString(true)=%q; want true", got)
	}
	if got := boolToString(false); got != "false" {
		t.Fatalf("boolToString(false)=%q; want false", got)
	}
}
