package backup

import (
	"path/filepath"
	"testing"
)

func TestCollectorConfigValidateDefaultsAndErrors(t *testing.T) {
	cfg := &CollectorConfig{}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error when no backup options enabled")
	}

	cfg = GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{"["}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for invalid glob pattern")
	}

	cfg = GetDefaultCollectorConfig()
	cfg.MaxPVEBackupSizeBytes = -1
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for negative MaxPVEBackupSizeBytes")
	}

	cfg = &CollectorConfig{BackupVMConfigs: true}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error for minimal valid config: %v", err)
	}
	if cfg.PxarDatastoreConcurrency != 3 || cfg.PxarIntraConcurrency != 4 || cfg.PxarScanFanoutLevel != 1 || cfg.PxarScanMaxRoots != 2048 || cfg.PxarEnumWorkers != 4 {
		t.Fatalf("defaults not applied correctly: %+v", cfg)
	}
}

func TestGlobHelpers(t *testing.T) {
	cases := []struct {
		pattern   string
		candidate string
		expect    bool
	}{
		{"*.log", "error.log", true},
		{"etc/pve/**", "etc/pve/nodes/node1.conf", true},
		{"**/cache/**", "var/tmp/cache/data.bin", true},
		{"config/*.yaml", "etc/config/app.json", false},
	}
	for _, tc := range cases {
		if got := matchesGlob(tc.pattern, tc.candidate); got != tc.expect {
			t.Fatalf("matchesGlob(%s,%s)=%v want %v", tc.pattern, tc.candidate, got, tc.expect)
		}
	}

	if !matchAnyPattern([]string{"*.txt"}, "note.txt", filepath.Join("dir", "note.txt")) {
		t.Fatalf("matchAnyPattern should match basename")
	}
	if matchAnyPattern([]string{"*.txt"}, "image.jpg", "dir/image.jpg") {
		t.Fatalf("matchAnyPattern should not match non-matching patterns")
	}
	if !matchAnyPattern(nil, "anything", "relative/path") {
		t.Fatalf("nil patterns should always match")
	}
}
