package backup

import (
	"testing"
)

// TestCleanPatternName tests pattern name cleaning
func TestCleanPatternName(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected string
	}{
		{
			name:     "wildcard pattern",
			pattern:  "*.conf",
			expected: "_conf",
		},
		{
			name:     "glob pattern with multiple wildcards",
			pattern:  "*backup*.log",
			expected: "backup_log",
		},
		{
			name:     "path with slashes",
			pattern:  "path/to/file.txt",
			expected: "path_to_file_txt",
		},
		{
			name:     "only wildcards",
			pattern:  "***",
			expected: "all",
		},
		{
			name:     "empty pattern",
			pattern:  "",
			expected: "all",
		},
		{
			name:     "mixed special chars",
			pattern:  "*.conf/*.log",
			expected: "_conf__log",
		},
		{
			name:     "simple name",
			pattern:  "config",
			expected: "config",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cleanPatternName(tt.pattern)
			if result != tt.expected {
				t.Errorf("cleanPatternName(%q) = %q, want %q", tt.pattern, result, tt.expected)
			}
		})
	}
}

// TestDescribePatternForLog tests pattern description for logging
func TestDescribePatternForLog(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		expected string
	}{
		{
			name:     "wildcard pattern",
			pattern:  "*.conf",
			expected: ".conf",
		},
		{
			name:     "pattern with prefix wildcard",
			pattern:  "*backup",
			expected: "backup",
		},
		{
			name:     "pattern with suffix wildcard",
			pattern:  "log*",
			expected: "log",
		},
		{
			name:     "pattern with both wildcards",
			pattern:  "*important*",
			expected: "important",
		},
		{
			name:     "only wildcards",
			pattern:  "***",
			expected: "***",
		},
		{
			name:     "pattern with spaces",
			pattern:  "* test *",
			expected: "test",
		},
		{
			name:     "simple pattern",
			pattern:  "config.txt",
			expected: "config.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := describePatternForLog(tt.pattern)
			if result != tt.expected {
				t.Errorf("describePatternForLog(%q) = %q, want %q", tt.pattern, result, tt.expected)
			}
		})
	}
}

// TestMatchPattern tests filepath pattern matching
func TestMatchPattern(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		pattern  string
		expected bool
	}{
		{
			name:     "simple wildcard match",
			filename: "config.conf",
			pattern:  "*.conf",
			expected: true,
		},
		{
			name:     "simple wildcard no match",
			filename: "config.txt",
			pattern:  "*.conf",
			expected: false,
		},
		{
			name:     "prefix wildcard match",
			filename: "backup_file.log",
			pattern:  "*backup*.log",
			expected: true,
		},
		{
			name:     "exact match",
			filename: "test.txt",
			pattern:  "test.txt",
			expected: true,
		},
		{
			name:     "no wildcards no match",
			filename: "test.txt",
			pattern:  "other.txt",
			expected: false,
		},
		{
			name:     "multiple wildcards",
			filename: "vm-100-disk-1.qcow2",
			pattern:  "vm-*-disk-*.qcow2",
			expected: true,
		},
		{
			name:     "question mark wildcard",
			filename: "test1.txt",
			pattern:  "test?.txt",
			expected: true,
		},
		{
			name:     "brackets pattern match",
			filename: "test1.txt",
			pattern:  "test[0-9].txt",
			expected: true,
		},
		{
			name:     "brackets pattern no match",
			filename: "testa.txt",
			pattern:  "test[0-9].txt",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := matchPattern(tt.filename, tt.pattern)
			if result != tt.expected {
				t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.filename, tt.pattern, result, tt.expected)
			}
		})
	}
}

// TestEffectivePVEClusterPath tests PVE cluster path resolution
func TestEffectivePVEClusterPath(t *testing.T) {
	logger := newTestLogger()
	tmpDir := t.TempDir()

	tests := []struct {
		name       string
		configPath string
		expected   string
	}{
		{
			name:       "custom path",
			configPath: "/custom/cluster",
			expected:   "/custom/cluster",
		},
		{
			name:       "empty path uses default",
			configPath: "",
			expected:   "/var/lib/pve-cluster",
		},
		{
			name:       "whitespace only uses default",
			configPath: "   ",
			expected:   "/var/lib/pve-cluster",
		},
		{
			name:       "path with whitespace gets trimmed",
			configPath: "  /my/path  ",
			expected:   "/my/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GetDefaultCollectorConfig()
			cfg.PVEClusterPath = tt.configPath
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			result := collector.effectivePVEClusterPath()
			if result != tt.expected {
				t.Errorf("effectivePVEClusterPath() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestTargetPathFor tests target path generation
func TestTargetPathFor(t *testing.T) {
	logger := newTestLogger()
	tmpDir := t.TempDir()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	tests := []struct {
		name     string
		src      string
		expected string
	}{
		{
			name:     "absolute path",
			src:      "/etc/pve/config",
			expected: tmpDir + "/etc/pve/config",
		},
		{
			name:     "relative path",
			src:      "config/test",
			expected: tmpDir + "/config/test",
		},
		{
			name:     "path with trailing slash",
			src:      "/etc/pve/",
			expected: tmpDir + "/etc/pve",
		},
		{
			name:     "empty path",
			src:      "",
			expected: tmpDir + "/pve",
		},
		{
			name:     "dot path",
			src:      ".",
			expected: tmpDir + "/pve",
		},
		{
			name:     "root path",
			src:      "/",
			expected: tmpDir + "/pve",
		},
		{
			name:     "path with multiple slashes",
			src:      "//etc///pve//",
			expected: tmpDir + "/etc/pve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := collector.targetPathFor(tt.src)
			if result != tt.expected {
				t.Errorf("targetPathFor(%q) = %q, want %q", tt.src, result, tt.expected)
			}
		})
	}
}
