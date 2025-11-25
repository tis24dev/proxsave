package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestShortHostname tests hostname shortening
func TestShortHostname(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "fqdn",
			input:    "pve1.example.com",
			expected: "pve1",
		},
		{
			name:     "simple hostname",
			input:    "pve1",
			expected: "pve1",
		},
		{
			name:     "multiple dots",
			input:    "pve1.sub.example.com",
			expected: "pve1",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "starts with dot",
			input:    ".example.com",
			expected: ".example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shortHostname(tt.input)
			if result != tt.expected {
				t.Errorf("shortHostname(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestRelativeDepth tests relative depth calculation
func TestRelativeDepth(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		path     string
		expected int
	}{
		{
			name:     "same path",
			base:     "/etc/pve",
			path:     "/etc/pve",
			expected: 0,
		},
		{
			name:     "one level deep",
			base:     "/etc/pve",
			path:     "/etc/pve/nodes",
			expected: 1,
		},
		{
			name:     "two levels deep",
			base:     "/etc/pve",
			path:     "/etc/pve/nodes/pve1",
			expected: 2,
		},
		{
			name:     "three levels deep",
			base:     "/etc/pve",
			path:     "/etc/pve/nodes/pve1/qemu-server",
			expected: 3,
		},
		{
			name:     "not under base",
			base:     "/etc/pve",
			path:     "/var/lib/pve",
			expected: 0,
		},
		{
			name:     "trailing slashes",
			base:     "/etc/pve/",
			path:     "/etc/pve/nodes/",
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := relativeDepth(tt.base, tt.path)
			if result != tt.expected {
				t.Errorf("relativeDepth(%q, %q) = %d, want %d", tt.base, tt.path, result, tt.expected)
			}
		})
	}
}

// TestCopyIfExists tests conditional file copying
func TestCopyIfExists(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	// Create source file
	srcDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "test.txt")
	content := []byte("test content")
	if err := os.WriteFile(srcFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	// Create destination directory
	dstDir := filepath.Join(tmpDir, "dst")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name        string
		srcPath     string
		dstPath     string
		expectError bool
		expectCopy  bool
	}{
		{
			name:        "copy existing file",
			srcPath:     srcFile,
			dstPath:     filepath.Join(dstDir, "test.txt"),
			expectError: false,
			expectCopy:  true,
		},
		{
			name:        "nonexistent source",
			srcPath:     filepath.Join(srcDir, "nonexistent.txt"),
			dstPath:     filepath.Join(dstDir, "nonexistent.txt"),
			expectError: true,
			expectCopy:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := collector.copyIfExists(tt.srcPath, tt.dstPath, "test")

			if tt.expectError {
				if err == nil {
					t.Error("copyIfExists() should return error")
				}
			} else {
				if err != nil {
					t.Errorf("copyIfExists() error = %v", err)
				}
			}

			if tt.expectCopy {
				// Verify file was copied
				if _, err := os.Stat(tt.dstPath); os.IsNotExist(err) {
					t.Error("copyIfExists() should have copied file")
				}

				// Verify content matches
				copiedContent, err := os.ReadFile(tt.dstPath)
				if err != nil {
					t.Errorf("Failed to read copied file: %v", err)
				}
				if string(copiedContent) != string(content) {
					t.Error("Copied content doesn't match original")
				}
			} else if !tt.expectError {
				// Verify file was NOT copied
				if _, err := os.Stat(tt.dstPath); err == nil {
					t.Error("copyIfExists() should not have copied nonexistent file")
				}
			}
		})
	}
}

// TestCephHasClusterConfig tests Ceph cluster configuration detection
func TestCephHasClusterConfig(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "valid ceph config with fsid",
			content: `[global]
fsid = 12345678-1234-1234-1234-123456789012
mon_host = 10.0.0.1,10.0.0.2,10.0.0.3`,
			expected: true,
		},
		{
			name: "valid config with mon_host",
			content: `[global]
mon_host = 10.0.0.1`,
			expected: true,
		},
		{
			name: "valid config with mon_initial_members",
			content: `[global]
mon_initial_members = pve1,pve2,pve3`,
			expected: true,
		},
		{
			name:     "empty config",
			content:  "",
			expected: false,
		},
		{
			name: "config without ceph keys",
			content: `[global]
some_other_key = value`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test directory
			testDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(testDir, 0755); err != nil {
				t.Fatal(err)
			}

			// Write config file
			configPath := filepath.Join(testDir, "ceph.conf")
			if tt.content != "" {
				if err := os.WriteFile(configPath, []byte(tt.content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			result := cephHasClusterConfig(testDir)
			if result != tt.expected {
				t.Errorf("cephHasClusterConfig() = %v, want %v", result, tt.expected)
			}
		})
	}

	// Test nonexistent directory
	t.Run("nonexistent directory", func(t *testing.T) {
		result := cephHasClusterConfig(filepath.Join(tmpDir, "nonexistent"))
		if result {
			t.Error("cephHasClusterConfig() should return false for nonexistent directory")
		}
	})
}

// TestCephHasKeyring tests Ceph keyring detection
func TestCephHasKeyring(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name      string
		files     []string
		expected  bool
		setupFile bool
	}{
		{
			name:      "directory with keyring",
			files:     []string{"ceph.client.admin.keyring"},
			expected:  true,
			setupFile: true,
		},
		{
			name:      "directory with multiple keyrings",
			files:     []string{"ceph.client.admin.keyring", "ceph.mon.keyring"},
			expected:  true,
			setupFile: true,
		},
		{
			name:      "directory without keyring",
			files:     []string{"config.txt", "data.json"},
			expected:  false,
			setupFile: true,
		},
		{
			name:      "empty directory",
			files:     []string{},
			expected:  false,
			setupFile: true,
		},
		{
			name:      "nonexistent path",
			files:     []string{},
			expected:  false,
			setupFile: false,
		},
		{
			name:      "uppercase keyring extension",
			files:     []string{"ceph.KEYRING"},
			expected:  true,
			setupFile: true,
		},
		{
			name:      "mixed case keyring",
			files:     []string{"test.KeyRing"},
			expected:  true,
			setupFile: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := filepath.Join(tmpDir, tt.name)

			if tt.setupFile {
				if err := os.MkdirAll(testDir, 0755); err != nil {
					t.Fatal(err)
				}

				for _, file := range tt.files {
					filePath := filepath.Join(testDir, file)
					if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}

			result := cephHasKeyring(testDir)
			if result != tt.expected {
				t.Errorf("cephHasKeyring() = %v, want %v (files: %v)", result, tt.expected, tt.files)
			}
		})
	}

	// Test with a file instead of directory
	t.Run("file instead of directory", func(t *testing.T) {
		testFile := filepath.Join(tmpDir, "test_file.txt")
		if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}

		result := cephHasKeyring(testFile)
		if result {
			t.Error("cephHasKeyring() should return false for a file")
		}
	})
}

// TestCephServiceActive tests Ceph service detection
func TestCephServiceActive(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	ctx := context.Background()

	// This test just verifies the function doesn't crash
	// In a real environment, it would check actual systemctl output
	result := collector.cephServiceActive(ctx)

	// Should return bool without panic
	_ = result
}

// TestCephConfigPaths tests Ceph config path resolution
func TestCephConfigPaths(t *testing.T) {
	tests := []struct {
		name           string
		configPath     string
		pveConfigPath  string
		expectedCount  int
		expectContains []string
	}{
		{
			name:          "default paths",
			configPath:    "",
			pveConfigPath: "/etc/pve",
			expectedCount: 2,
			expectContains: []string{
				"/etc/pve/ceph",
				"/etc/ceph",
			},
		},
		{
			name:          "custom config path",
			configPath:    "/custom/ceph",
			pveConfigPath: "/etc/pve",
			expectedCount: 3,
			expectContains: []string{
				"/custom/ceph",
				"/etc/pve/ceph",
				"/etc/ceph",
			},
		},
		{
			name:          "relative config path",
			configPath:    "ceph-custom",
			pveConfigPath: "/etc/pve",
			expectedCount: 3,
			expectContains: []string{
				"/ceph-custom",
				"/etc/pve/ceph",
				"/etc/ceph",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logger := newTestLogger()
			cfg := GetDefaultCollectorConfig()
			cfg.PVEConfigPath = tt.pveConfigPath
			cfg.CephConfigPath = tt.configPath
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			paths := collector.cephConfigPaths()

			if len(paths) != tt.expectedCount {
				t.Errorf("cephConfigPaths() returned %d paths, want %d", len(paths), tt.expectedCount)
			}

			for _, expected := range tt.expectContains {
				found := false
				for _, path := range paths {
					if path == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("cephConfigPaths() should contain %q, got: %v", expected, paths)
				}
			}

			// Verify no duplicates
			seen := make(map[string]bool)
			for _, path := range paths {
				if seen[path] {
					t.Errorf("cephConfigPaths() contains duplicate: %q", path)
				}
				seen[path] = true
			}
		})
	}
}

// TestIsCephConfigured tests overall Ceph configuration detection
func TestIsCephConfigured(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test Ceph directory with config
	cephDir := filepath.Join(tmpDir, "ceph")
	if err := os.MkdirAll(cephDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write a valid Ceph config
	configContent := `[global]
fsid = 12345678-1234-1234-1234-123456789012
mon_host = 10.0.0.1`
	configPath := filepath.Join(cephDir, "ceph.conf")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.CephConfigPath = cephDir
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	ctx := context.Background()
	result := collector.isCephConfigured(ctx)

	// Should detect Ceph configuration
	if !result {
		t.Error("isCephConfigured() should return true when ceph.conf exists")
	}

	// Test with nonexistent Ceph config
	cfg2 := GetDefaultCollectorConfig()
	cfg2.CephConfigPath = filepath.Join(tmpDir, "nonexistent")
	collector2 := NewCollector(logger, cfg2, tmpDir, "pve", false)

	result2 := collector2.isCephConfigured(ctx)
	// May return false or true depending on system state, just verify no panic
	_ = result2
}
