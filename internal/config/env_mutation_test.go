package config

import (
	"strings"
	"testing"
)

func TestApplySecondaryStorageSettingsEnabled(t *testing.T) {
	template := "SECONDARY_ENABLED=false\nSECONDARY_PATH=\nSECONDARY_LOG_PATH=\n"

	got := ApplySecondaryStorageSettings(template, true, " /mnt/secondary ", " /mnt/secondary/log ")

	for _, needle := range []string{
		"SECONDARY_ENABLED=true",
		"SECONDARY_PATH=/mnt/secondary",
		"SECONDARY_LOG_PATH=/mnt/secondary/log",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected %q in template:\n%s", needle, got)
		}
	}
}

func TestApplySecondaryStorageSettingsEnabledWithEmptyLogPath(t *testing.T) {
	template := "SECONDARY_ENABLED=false\nSECONDARY_PATH=\nSECONDARY_LOG_PATH=/old/log\n"

	got := ApplySecondaryStorageSettings(template, true, "/mnt/secondary", "")

	for _, needle := range []string{
		"SECONDARY_ENABLED=true",
		"SECONDARY_PATH=/mnt/secondary",
		"SECONDARY_LOG_PATH=",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected %q in template:\n%s", needle, got)
		}
	}
}

func TestApplySecondaryStorageSettingsDisabledClearsValues(t *testing.T) {
	template := "SECONDARY_ENABLED=true\nSECONDARY_PATH=/mnt/old-secondary\nSECONDARY_LOG_PATH=/mnt/old-secondary/logs\n"

	got := ApplySecondaryStorageSettings(template, false, "/ignored", "/ignored/logs")

	for _, needle := range []string{
		"SECONDARY_ENABLED=false",
		"SECONDARY_PATH=",
		"SECONDARY_LOG_PATH=",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected %q in template:\n%s", needle, got)
		}
	}
	if strings.Contains(got, "/mnt/old-secondary") {
		t.Fatalf("expected old secondary values to be cleared:\n%s", got)
	}
}

func TestApplySecondaryStorageSettingsDisabledAppendsCanonicalState(t *testing.T) {
	template := "BACKUP_ENABLED=true\n"

	got := ApplySecondaryStorageSettings(template, false, "", "")

	for _, needle := range []string{
		"BACKUP_ENABLED=true",
		"SECONDARY_ENABLED=false",
		"SECONDARY_PATH=",
		"SECONDARY_LOG_PATH=",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("expected %q in template:\n%s", needle, got)
		}
	}
}
