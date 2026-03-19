package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parseMutatedEnvTemplate(t *testing.T, template string) (map[string]string, map[string]int) {
	t.Helper()

	values := make(map[string]string)
	counts := make(map[string]int)

	for _, line := range strings.Split(template, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			t.Fatalf("invalid env line %q in template:\n%s", line, template)
		}

		key := strings.TrimSpace(parts[0])
		value := parts[1]
		counts[key]++
		values[key] = value
	}

	return values, counts
}

func assertMutatedEnvValue(t *testing.T, values map[string]string, counts map[string]int, key, want string) {
	t.Helper()

	if got := counts[key]; got != 1 {
		t.Fatalf("%s occurrences = %d; want 1", key, got)
	}
	if got := values[key]; got != want {
		t.Fatalf("%s = %q; want %q", key, got, want)
	}
}

func TestApplySecondaryStorageSettingsEnabled(t *testing.T) {
	template := "SECONDARY_ENABLED=false\nSECONDARY_PATH=\nSECONDARY_LOG_PATH=\n"

	got := ApplySecondaryStorageSettings(template, true, " /mnt/secondary ", " /mnt/secondary/log ")
	values, counts := parseMutatedEnvTemplate(t, got)
	assertMutatedEnvValue(t, values, counts, "SECONDARY_ENABLED", "true")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_PATH", "/mnt/secondary")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_LOG_PATH", "/mnt/secondary/log")
}

func TestApplySecondaryStorageSettingsEnabledWithEmptyLogPath(t *testing.T) {
	template := "SECONDARY_ENABLED=false\nSECONDARY_PATH=\nSECONDARY_LOG_PATH=/old/log\n"

	got := ApplySecondaryStorageSettings(template, true, "/mnt/secondary", "")
	values, counts := parseMutatedEnvTemplate(t, got)
	assertMutatedEnvValue(t, values, counts, "SECONDARY_ENABLED", "true")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_PATH", "/mnt/secondary")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_LOG_PATH", "")
}

func TestApplySecondaryStorageSettingsDisabledClearsValues(t *testing.T) {
	template := "SECONDARY_ENABLED=true\nSECONDARY_PATH=/mnt/old-secondary\nSECONDARY_LOG_PATH=/mnt/old-secondary/logs\n"

	got := ApplySecondaryStorageSettings(template, false, "/ignored", "/ignored/logs")
	values, counts := parseMutatedEnvTemplate(t, got)
	assertMutatedEnvValue(t, values, counts, "SECONDARY_ENABLED", "false")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_PATH", "")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_LOG_PATH", "")
}

func TestApplySecondaryStorageSettingsDisabledAppendsCanonicalState(t *testing.T) {
	template := "BACKUP_ENABLED=true\n"

	got := ApplySecondaryStorageSettings(template, false, "", "")
	values, counts := parseMutatedEnvTemplate(t, got)
	assertMutatedEnvValue(t, values, counts, "BACKUP_ENABLED", "true")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_ENABLED", "false")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_PATH", "")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_LOG_PATH", "")
}

func TestApplySecondaryStorageSettingsQuotesUnsafePaths(t *testing.T) {
	template := "SECONDARY_ENABLED=false\nSECONDARY_PATH=\nSECONDARY_LOG_PATH=\n"

	got := ApplySecondaryStorageSettings(template, true, " /mnt/secondary #1 ", " /mnt/secondary/log dir ")
	values, counts := parseMutatedEnvTemplate(t, got)
	assertMutatedEnvValue(t, values, counts, "SECONDARY_ENABLED", "true")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_PATH", "'/mnt/secondary #1'")
	assertMutatedEnvValue(t, values, counts, "SECONDARY_LOG_PATH", "'/mnt/secondary/log dir'")

	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte(got), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	raw, err := parseEnvFile(configPath)
	if err != nil {
		t.Fatalf("parseEnvFile() error = %v", err)
	}
	if gotPath := raw["SECONDARY_PATH"]; gotPath != "/mnt/secondary #1" {
		t.Fatalf("SECONDARY_PATH = %q; want %q", gotPath, "/mnt/secondary #1")
	}
	if gotLogPath := raw["SECONDARY_LOG_PATH"]; gotLogPath != "/mnt/secondary/log dir" {
		t.Fatalf("SECONDARY_LOG_PATH = %q; want %q", gotLogPath, "/mnt/secondary/log dir")
	}
}
