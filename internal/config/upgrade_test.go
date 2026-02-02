package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const upgradeTemplate = `BACKUP_PATH=/default/backup
LOG_PATH=/default/log
KEY1=template
`

func TestPlanUpgradeConfigNoChanges(t *testing.T) {
	withTemplate(t, upgradeTemplate, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		if err := os.WriteFile(configPath, []byte(upgradeTemplate), 0600); err != nil {
			t.Fatalf("failed to seed config: %v", err)
		}

		result, err := PlanUpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("PlanUpgradeConfigFile returned error: %v", err)
		}
		if result.Changed {
			t.Fatalf("result.Changed = true; want false for identical config")
		}
	})
}

func TestUpgradeConfigAddsMissingKeys(t *testing.T) {
	withTemplate(t, upgradeTemplate, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		legacy := "BACKUP_PATH=/legacy\n"
		if err := os.WriteFile(configPath, []byte(legacy), 0600); err != nil {
			t.Fatalf("failed to write legacy config: %v", err)
		}

		result, err := UpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("UpgradeConfigFile returned error: %v", err)
		}
		if !result.Changed {
			t.Fatalf("expected result.Changed=true for missing keys")
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read upgraded config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "BACKUP_PATH=/legacy") {
			t.Fatalf("upgraded config does not keep legacy BACKUP_PATH: %s", content)
		}
		if !strings.Contains(content, "LOG_PATH=/default/log") {
			t.Fatalf("upgraded config missing template key LOG_PATH")
		}
	})
}

func TestPlanUpgradeTracksExtraKeys(t *testing.T) {
	withTemplate(t, upgradeTemplate, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		content := "BACKUP_PATH=/legacy\nEXTRA_KEY=value\n"
		if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		result, err := PlanUpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("PlanUpgradeConfigFile returned error: %v", err)
		}
		if len(result.ExtraKeys) != 1 || result.ExtraKeys[0] != "EXTRA_KEY" {
			t.Fatalf("ExtraKeys = %v; want [EXTRA_KEY]", result.ExtraKeys)
		}
	})
}

func TestUpgradeConfigCreatesBackupAndCustomSection(t *testing.T) {
	withTemplate(t, upgradeTemplate, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		legacy := "BACKUP_PATH=/legacy\nEXTRA_KEY=value\n"
		if err := os.WriteFile(configPath, []byte(legacy), 0600); err != nil {
			t.Fatalf("failed to write legacy config: %v", err)
		}

		result, err := UpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("UpgradeConfigFile returned error: %v", err)
		}
		if result.BackupPath == "" {
			t.Fatal("expected BackupPath to be populated after upgrade")
		}
		backupContent, err := os.ReadFile(result.BackupPath)
		if err != nil {
			t.Fatalf("failed to read backup: %v", err)
		}
		if string(backupContent) != legacy {
			t.Fatalf("backup content mismatch: got %q want %q", string(backupContent), legacy)
		}

		updated, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read upgraded config: %v", err)
		}
		content := string(updated)
		if !strings.Contains(content, "Custom keys preserved") {
			t.Fatalf("expected custom section header, got: %s", content)
		}
		if !strings.Contains(content, "EXTRA_KEY=value") {
			t.Fatalf("expected EXTRA_KEY preserved, got: %s", content)
		}
	})
}

func TestPlanUpgradeEmptyPath(t *testing.T) {
	if _, err := PlanUpgradeConfigFile("   "); err == nil {
		t.Fatal("expected error for empty config path")
	}
}

func TestComputeConfigUpgradePreservesValues(t *testing.T) {
	template := `KEY1=default1
KEY2=default2
KEY3=default3
`
	withTemplate(t, template, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		userConfig := "KEY2=valueA\nKEY2=valueB\n"
		if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
			t.Fatalf("failed to seed config: %v", err)
		}

		result, newContent, _, err := computeConfigUpgrade(configPath)
		if err != nil {
			t.Fatalf("computeConfigUpgrade returned error: %v", err)
		}
		if !result.Changed {
			t.Fatal("expected result.Changed=true when keys missing")
		}
		if result.PreservedValues != 2 {
			t.Fatalf("PreservedValues = %d; want 2", result.PreservedValues)
		}
		if len(result.MissingKeys) != 2 || result.MissingKeys[0] != "KEY1" || result.MissingKeys[1] != "KEY3" {
			t.Fatalf("MissingKeys = %v; want [KEY1 KEY3]", result.MissingKeys)
		}
		if !strings.Contains(newContent, "KEY2=valueA") || !strings.Contains(newContent, "KEY2=valueB") {
			t.Fatalf("upgraded content missing preserved values:\n%s", newContent)
		}
	})
}

func TestUpgradeConfigPreservesBlockValues(t *testing.T) {
	template := `BACKUP_PATH=/default/backup
LOG_PATH=/default/log
CUSTOM_BACKUP_PATHS="
# /template/example
"
`
	withTemplate(t, template, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		userConfig := `BACKUP_PATH=/legacy/backup
CUSTOM_BACKUP_PATHS="
/etc/custom.conf
"
`
		if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
			t.Fatalf("failed to seed config: %v", err)
		}

		result, err := UpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("UpgradeConfigFile returned error: %v", err)
		}
		if !result.Changed {
			t.Fatal("expected result.Changed=true when template has missing keys")
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("failed to read upgraded config: %v", err)
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		if !strings.Contains(content, "BACKUP_PATH=/legacy/backup") {
			t.Fatalf("upgraded config missing preserved BACKUP_PATH:\n%s", content)
		}
		if !strings.Contains(content, "CUSTOM_BACKUP_PATHS=\"\n/etc/custom.conf\n\"\n") {
			t.Fatalf("upgraded config missing preserved CUSTOM_BACKUP_PATHS block:\n%s", content)
		}
		if strings.Contains(content, "# /template/example") {
			t.Fatalf("template example unexpectedly present in preserved block:\n%s", content)
		}
	})
}

func TestPlanUpgradeConfigTracksMissingBlockKey(t *testing.T) {
	template := `BACKUP_PATH=/default/backup
CUSTOM_BACKUP_PATHS="
# /template/example
"
`
	withTemplate(t, template, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		userConfig := "BACKUP_PATH=/legacy/backup\n"
		if err := os.WriteFile(configPath, []byte(userConfig), 0600); err != nil {
			t.Fatalf("failed to seed config: %v", err)
		}

		result, err := PlanUpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("PlanUpgradeConfigFile returned error: %v", err)
		}
		if !result.Changed {
			t.Fatal("expected result.Changed=true when keys are missing")
		}
		found := false
		for _, key := range result.MissingKeys {
			if key == "CUSTOM_BACKUP_PATHS" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("MissingKeys=%v; expected CUSTOM_BACKUP_PATHS", result.MissingKeys)
		}
	})
}
