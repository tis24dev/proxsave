package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// loadEnvErrForTest is loadEnvForTest for a config that must fail to load: it neutralises
// the environment overrides the same way and returns the loader's error.
func loadEnvErrForTest(t *testing.T, name, content string) error {
	t.Helper()
	for _, key := range envOverrideKeys {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	_, err := LoadConfigWithBaseDir(path, "/custom/base")
	return err
}

func TestPBSTargetDefaultsWhenAbsent(t *testing.T) {
	cfg := loadEnvForTest(t, "absent.env", "# no PBS storage keys\n")
	if cfg.PBSTargetEnabled {
		t.Error("PBS_TARGET_ENABLED default should be false")
	}
	if cfg.PBSTargetStorage != "" {
		t.Errorf("PBS_TARGET_STORAGE default = %q, want empty", cfg.PBSTargetStorage)
	}
	if cfg.MaxPBSTargetBackups != 15 {
		t.Errorf("MAX_PBS_TARGET_BACKUPS default = %d, want 15", cfg.MaxPBSTargetBackups)
	}
	// Code default of the PBS threshold is the Primary's, whatever the Primary resolved to.
	if cfg.MinDiskPBSGB != cfg.MinDiskPrimaryGB {
		t.Errorf("MIN_DISK_SPACE_PBS_GB default = %v, want the Primary's %v", cfg.MinDiskPBSGB, cfg.MinDiskPrimaryGB)
	}

	cfg = loadEnvForTest(t, "primary.env", "MIN_DISK_SPACE_PRIMARY_GB=7\n")
	if cfg.MinDiskPBSGB != 7 {
		t.Errorf("MIN_DISK_SPACE_PBS_GB default with Primary 7 = %v, want 7", cfg.MinDiskPBSGB)
	}
}

func TestPBSTargetShippedTemplateValues(t *testing.T) {
	cfg := loadEnvForTest(t, "shipped.env", DefaultEnvTemplate())
	if cfg.PBSTargetEnabled {
		t.Error("shipped template should leave the PBS storage disabled")
	}
	if cfg.PBSTargetStorage != "" {
		t.Errorf("shipped PBS_TARGET_STORAGE = %q, want empty", cfg.PBSTargetStorage)
	}
	if cfg.MaxPBSTargetBackups != 15 {
		t.Errorf("shipped MAX_PBS_TARGET_BACKUPS = %d, want 15", cfg.MaxPBSTargetBackups)
	}
	if cfg.MinDiskPBSGB != 1 {
		t.Errorf("shipped MIN_DISK_SPACE_PBS_GB = %v, want 1", cfg.MinDiskPBSGB)
	}
}

func TestPBSTargetExplicitValuesAreRead(t *testing.T) {
	cfg := loadEnvForTest(t, "pbs.env", strings.Join([]string{
		"PBS_TARGET_ENABLED=true",
		"PBS_TARGET_STORAGE=  pbs-main  ",
		"MAX_PBS_TARGET_BACKUPS=3",
		"MIN_DISK_SPACE_PBS_GB=2.5",
		"",
	}, "\n"))
	if !cfg.PBSTargetEnabled {
		t.Error("PBSTargetEnabled = false, want true")
	}
	if cfg.PBSTargetStorage != "pbs-main" {
		t.Errorf("PBSTargetStorage = %q, want %q", cfg.PBSTargetStorage, "pbs-main")
	}
	if cfg.MaxPBSTargetBackups != 3 {
		t.Errorf("MaxPBSTargetBackups = %d, want 3", cfg.MaxPBSTargetBackups)
	}
	if cfg.MinDiskPBSGB != 2.5 {
		t.Errorf("MinDiskPBSGB = %v, want 2.5", cfg.MinDiskPBSGB)
	}
}

// 0 or less means no check for PBS, while a sibling's 0 is raised to 10 GB by
// sanitizeMinDisk: the PBS value must not go through it.
func TestPBSTargetMinDiskZeroOrLessIsNoCheck(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  float64
	}{
		{"0", 0},
		{"-5", 0},
	} {
		cfg := loadEnvForTest(t, "mindisk.env", "MIN_DISK_SPACE_PBS_GB="+tc.value+"\n")
		if cfg.MinDiskPBSGB != tc.want {
			t.Errorf("MIN_DISK_SPACE_PBS_GB=%s gave %v, want %v", tc.value, cfg.MinDiskPBSGB, tc.want)
		}
	}
}

func TestPBSTargetZeroRetentionIsAccepted(t *testing.T) {
	cfg := loadEnvForTest(t, "noprune.env", "PBS_TARGET_ENABLED=true\nPBS_TARGET_STORAGE=pbs-main\nMAX_PBS_TARGET_BACKUPS=0\n")
	if cfg.MaxPBSTargetBackups != 0 {
		t.Errorf("MaxPBSTargetBackups = %d, want 0 (no prune)", cfg.MaxPBSTargetBackups)
	}
}

func TestPBSTargetDisabledWithoutStorageLoads(t *testing.T) {
	cfg := loadEnvForTest(t, "disabled.env", "PBS_TARGET_ENABLED=false\nPBS_TARGET_STORAGE=\n")
	if cfg.PBSTargetEnabled || cfg.PBSTargetStorage != "" {
		t.Fatalf("got enabled=%v storage=%q, want disabled and empty", cfg.PBSTargetEnabled, cfg.PBSTargetStorage)
	}
}

func TestPBSTargetValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "enabled without storage",
			content: "PBS_TARGET_ENABLED=true\nPBS_TARGET_STORAGE=\n",
			want:    "PBS_TARGET_STORAGE is required when PBS_TARGET_ENABLED=true",
		},
		{
			name:    "enabled with blank storage",
			content: "PBS_TARGET_ENABLED=true\nPBS_TARGET_STORAGE=\"   \"\n",
			want:    "PBS_TARGET_STORAGE is required when PBS_TARGET_ENABLED=true",
		},
		{
			name:    "negative retention",
			content: "PBS_TARGET_ENABLED=true\nPBS_TARGET_STORAGE=pbs-main\nMAX_PBS_TARGET_BACKUPS=-1\n",
			want:    "MAX_PBS_TARGET_BACKUPS must not be negative (got -1)",
		},
		{
			name:    "negative retention with PBS disabled",
			content: "MAX_PBS_TARGET_BACKUPS=-2\n",
			want:    "MAX_PBS_TARGET_BACKUPS must not be negative (got -2)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := loadEnvErrForTest(t, "invalid.env", tc.content)
			if err == nil {
				t.Fatal("expected the configuration to be rejected")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestPBSTargetEnvironmentOverrides(t *testing.T) {
	for _, key := range envOverrideKeys {
		t.Setenv(key, "")
	}
	path := filepath.Join(t.TempDir(), "file.env")
	if err := os.WriteFile(path, []byte("PBS_TARGET_ENABLED=false\nMAX_PBS_TARGET_BACKUPS=15\nMIN_DISK_SPACE_PBS_GB=1\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("PBS_TARGET_ENABLED", "true")
	t.Setenv("PBS_TARGET_STORAGE", "pbs-env")
	t.Setenv("MAX_PBS_TARGET_BACKUPS", "4")
	t.Setenv("MIN_DISK_SPACE_PBS_GB", "3")
	cfg, err := LoadConfigWithBaseDir(path, "/custom/base")
	if err != nil {
		t.Fatalf("LoadConfigWithBaseDir: %v", err)
	}
	if !cfg.PBSTargetEnabled || cfg.PBSTargetStorage != "pbs-env" || cfg.MaxPBSTargetBackups != 4 || cfg.MinDiskPBSGB != 3 {
		t.Fatalf("environment not applied: enabled=%v storage=%q max=%d min=%v",
			cfg.PBSTargetEnabled, cfg.PBSTargetStorage, cfg.MaxPBSTargetBackups, cfg.MinDiskPBSGB)
	}
}

// An existing backup.env that predates the PBS storage receives the four variables with
// the template defaults on upgrade, and still loads with PBS disabled.
func TestUpgradeAddsPBSTargetVariables(t *testing.T) {
	pbsKeys := []string{"MAX_PBS_TARGET_BACKUPS", "MIN_DISK_SPACE_PBS_GB", "PBS_TARGET_ENABLED", "PBS_TARGET_STORAGE"}
	var kept []string
	for _, line := range strings.Split(DefaultEnvTemplate(), "\n") {
		drop := false
		for _, key := range pbsKeys {
			if strings.HasPrefix(line, key+"=") {
				drop = true
			}
		}
		if !drop {
			kept = append(kept, line)
		}
	}
	configPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(configPath, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatalf("write old config: %v", err)
	}

	result, err := UpgradeConfigFile(configPath)
	if err != nil {
		t.Fatalf("UpgradeConfigFile: %v", err)
	}
	missing := append([]string(nil), result.MissingKeys...)
	sort.Strings(missing)
	if strings.Join(missing, ",") != strings.Join(pbsKeys, ",") {
		t.Fatalf("MissingKeys = %v, want %v", missing, pbsKeys)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read upgraded config: %v", err)
	}
	cfg := loadEnvForTest(t, "upgraded.env", string(data))
	if cfg.PBSTargetEnabled || cfg.PBSTargetStorage != "" || cfg.MaxPBSTargetBackups != 15 || cfg.MinDiskPBSGB != 1 {
		t.Fatalf("upgraded config: enabled=%v storage=%q max=%d min=%v, want false, empty, 15, 1",
			cfg.PBSTargetEnabled, cfg.PBSTargetStorage, cfg.MaxPBSTargetBackups, cfg.MinDiskPBSGB)
	}
}
