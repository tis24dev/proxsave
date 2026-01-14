package backup

import (
	"path/filepath"
	"regexp"
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

func TestCollectorConfigValidateEmptyExcludePattern(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.ExcludePatterns = []string{""}
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for empty exclude pattern")
	}
}

func TestCollectorConfigValidateNormalizesNegativeBudget(t *testing.T) {
	cfg := &CollectorConfig{BackupVMConfigs: true}
	cfg.PxarEnumBudgetMs = -1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PxarEnumBudgetMs != 0 {
		t.Fatalf("expected PxarEnumBudgetMs to be normalized to 0, got %d", cfg.PxarEnumBudgetMs)
	}
}

func TestCollectorConfigValidateRequiresAbsoluteSystemRootPrefix(t *testing.T) {
	cfg := &CollectorConfig{BackupVMConfigs: true}
	cfg.SystemRootPrefix = "relative/path"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for relative system root prefix")
	}
}

func TestUniqueCandidatesSkipsEmptyEntries(t *testing.T) {
	got := uniqueCandidates("", "")
	if len(got) != 0 {
		t.Fatalf("expected no candidates for empty input, got %#v", got)
	}
}

func TestGlobToRegexCoversSpecialCases(t *testing.T) {
	cases := []struct {
		pattern string
		valid   bool
		match   string
		noMatch string
	}{
		{pattern: "a?c", valid: true, match: "abc", noMatch: "ac"},
		{pattern: "[\\]", valid: true, match: "\\", noMatch: "x"},
		{pattern: "\\", valid: true, match: "\\", noMatch: "x"},
		// Unclosed '[' produces a regex that does not compile; matchesGlob treats this as "no match".
		{pattern: "[", valid: false, match: "[", noMatch: "x"},
		{pattern: "a[!b]c", valid: true, match: "axc", noMatch: "abc"},
	}

	for _, tc := range cases {
		re := globToRegex(tc.pattern)
		compiled, err := regexp.Compile(re)
		if !tc.valid {
			if err == nil {
				t.Fatalf("globToRegex(%q) should produce an invalid regex, got %q", tc.pattern, re)
			}
			continue
		}
		if err != nil {
			t.Fatalf("globToRegex(%q) produced invalid regex %q: %v", tc.pattern, re, err)
		}
		if !compiled.MatchString(tc.match) {
			t.Fatalf("globToRegex(%q)=%q should match %q", tc.pattern, re, tc.match)
		}
		if compiled.MatchString(tc.noMatch) {
			t.Fatalf("globToRegex(%q)=%q should not match %q", tc.pattern, re, tc.noMatch)
		}
	}
}
