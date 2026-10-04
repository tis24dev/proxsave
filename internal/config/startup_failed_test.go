package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SecondaryStartupFailed and CloudStartupFailed belong to the run, not to backup.env:
// no variable sets them, the shipped template has none, and the install/upgrade path
// leaves the shipped template byte for byte as it is.
func TestStartupFailedFieldsAreNotConfiguration(t *testing.T) {
	if strings.Contains(DefaultEnvTemplate(), "STARTUP_FAILED") {
		t.Fatal("the shipped template documents a STARTUP_FAILED variable")
	}

	cfg := loadEnvForTest(t, "startup-failed.env", DefaultEnvTemplate()+"\nSECONDARY_STARTUP_FAILED=true\nCLOUD_STARTUP_FAILED=true\n")
	if cfg.SecondaryStartupFailed || cfg.CloudStartupFailed {
		t.Fatalf("backup.env set the run's startup record: secondary=%v cloud=%v", cfg.SecondaryStartupFailed, cfg.CloudStartupFailed)
	}

	path := filepath.Join(t.TempDir(), "backup.env")
	shipped := []byte(DefaultEnvTemplate())
	if err := os.WriteFile(path, shipped, 0o600); err != nil {
		t.Fatalf("write backup.env: %v", err)
	}
	plan, err := PlanUpgradeConfigFile(path)
	if err != nil {
		t.Fatalf("PlanUpgradeConfigFile: %v", err)
	}
	if plan.Changed {
		t.Fatalf("the upgrade plans a change to the shipped template: %+v", plan)
	}
	if _, err := UpgradeConfigFile(path); err != nil {
		t.Fatalf("UpgradeConfigFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read backup.env: %v", err)
	}
	if !bytes.Equal(got, shipped) {
		t.Fatal("the upgrade rewrote the shipped template")
	}
}
