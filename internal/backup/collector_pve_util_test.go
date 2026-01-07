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

// TestHasCorosyncClusterConfigDetailed tests corosync cluster configuration detection
func TestHasCorosyncClusterConfigDetailed(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "config with cluster_name",
			content: `totem {
    cluster_name: testcluster
}`,
			expected: true,
		},
		{
			name: "config with nodelist",
			content: `nodelist {
    node {
        ring0_addr: 10.0.0.1
        name: pve1
    }
}`,
			expected: true,
		},
		{
			name: "config with ring0_addr",
			content: `totem {
    interface {
        ring0_addr: 192.168.1.1
    }
}`,
			expected: true,
		},
		{
			name: "config without cluster keywords",
			content: `[global]
logging = debug`,
			expected: false,
		},
		{
			name:     "empty config",
			content:  "",
			expected: false,
		},
		{
			name: "config with uppercase keywords",
			content: `CLUSTER_NAME: uppercase
NODELIST {
}`,
			expected: true, // Should match case-insensitively
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pveDir := filepath.Join(tmpDir, "etc", "pve")
			if err := os.MkdirAll(pveDir, 0o755); err != nil {
				t.Fatal(err)
			}

			corosyncConf := filepath.Join(pveDir, "corosync.conf")
			if tt.content != "" {
				if err := os.WriteFile(corosyncConf, []byte(tt.content), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			logger := newTestLogger()
			cfg := GetDefaultCollectorConfig()
			cfg.PVEConfigPath = pveDir
			cfg.CorosyncConfigPath = "" // Use default based on PVEConfigPath
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			result := collector.hasCorosyncClusterConfig()
			if result != tt.expected {
				t.Errorf("hasCorosyncClusterConfig() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestHasCorosyncClusterConfigCustomPath tests custom corosync config path
func TestHasCorosyncClusterConfigCustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create custom corosync config in a different location
	customPath := filepath.Join(tmpDir, "custom", "corosync.conf")
	if err := os.MkdirAll(filepath.Dir(customPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customPath, []byte("cluster_name: test"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.CorosyncConfigPath = customPath // Absolute path
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	result := collector.hasCorosyncClusterConfig()
	if !result {
		t.Error("hasCorosyncClusterConfig() should return true with custom path")
	}
}

// TestHasCorosyncClusterConfigRelativePath tests relative corosync config path
func TestHasCorosyncClusterConfigRelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create corosync config in a subdirectory
	subDir := filepath.Join(pveDir, "cluster")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "corosync.conf"), []byte("cluster_name: test"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.CorosyncConfigPath = "cluster/corosync.conf" // Relative path
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	result := collector.hasCorosyncClusterConfig()
	if !result {
		t.Error("hasCorosyncClusterConfig() should return true with relative path")
	}
}

// TestIsClusteredPVE tests cluster detection
func TestIsClusteredPVE(t *testing.T) {
	t.Run("detects via corosync config", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		if err := os.MkdirAll(pveDir, 0o755); err != nil {
			t.Fatal(err)
		}

		// Create corosync config with cluster_name
		if err := os.WriteFile(filepath.Join(pveDir, "corosync.conf"), []byte("cluster_name: test"), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		cfg.CorosyncConfigPath = "" // Use default based on PVEConfigPath
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		// Since hasCorosyncClusterConfig should detect this, test it directly
		if !collector.hasCorosyncClusterConfig() {
			t.Error("hasCorosyncClusterConfig() should return true when corosync config exists")
		}

		// isClusteredPVE should detect cluster via corosync config
		clustered, err := collector.isClusteredPVE(context.Background())
		if err != nil {
			t.Fatalf("isClusteredPVE error: %v", err)
		}
		if !clustered {
			t.Error("isClusteredPVE() should return true when corosync config exists")
		}
	})

	t.Run("detects via multiple nodes", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes")
		if err := os.MkdirAll(filepath.Join(nodesDir, "pve1"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(nodesDir, "pve2"), 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		cfg.CorosyncConfigPath = ""
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		clustered, err := collector.isClusteredPVE(context.Background())
		if err != nil {
			t.Fatalf("isClusteredPVE error: %v", err)
		}
		if !clustered {
			t.Error("isClusteredPVE() should return true with multiple nodes")
		}
	})

	t.Run("returns false for standalone", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes", "single")
		if err := os.MkdirAll(nodesDir, 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		cfg.CorosyncConfigPath = ""
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		clustered, err := collector.isClusteredPVE(context.Background())
		if err != nil {
			// pvecm might fail, which is expected
			t.Logf("isClusteredPVE returned error (expected): %v", err)
		}
		if clustered {
			t.Error("isClusteredPVE() should return false for standalone node")
		}
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		if err := os.MkdirAll(pveDir, 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		cfg.CorosyncConfigPath = ""
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := collector.isClusteredPVE(ctx)
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got: %v", err)
		}
	})
}

// TestCollectPVEDirectoriesClusteredMode tests directory collection in cluster mode
func TestCollectPVEDirectoriesClusteredMode(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create corosync.conf
	if err := os.WriteFile(filepath.Join(pveDir, "corosync.conf"), []byte("cluster_name: test"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create cluster directory
	clusterDir := filepath.Join(tmpDir, "var", "lib", "pve-cluster")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.PVEClusterPath = clusterDir
	cfg.BackupClusterConfig = true
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), true)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesFirewallAsDirectory tests firewall as directory
func TestCollectPVEDirectoriesFirewallAsDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create firewall as directory with rules
	firewallDir := filepath.Join(pveDir, "firewall")
	if err := os.MkdirAll(firewallDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(firewallDir, "cluster.fw"), []byte("RULES"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.BackupPVEFirewall = true
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesFirewallAsFile tests firewall as single file
func TestCollectPVEDirectoriesFirewallAsFile(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create firewall as single file
	if err := os.WriteFile(filepath.Join(pveDir, "firewall"), []byte("RULES"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.BackupPVEFirewall = true
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesVZDumpConfig tests vzdump config collection
func TestCollectPVEDirectoriesVZDumpConfig(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create vzdump.conf
	vzdumpPath := filepath.Join(tmpDir, "etc", "vzdump.conf")
	if err := os.MkdirAll(filepath.Dir(vzdumpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vzdumpPath, []byte("compress: zstd"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.BackupVZDumpConfig = true
	cfg.VzdumpConfigPath = vzdumpPath
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesVZDumpRelativePath tests vzdump with relative path
func TestCollectPVEDirectoriesVZDumpRelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create vzdump.conf in pve directory with relative path
	if err := os.WriteFile(filepath.Join(pveDir, "vzdump.conf"), []byte("compress: lzo"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.BackupVZDumpConfig = true
	cfg.VzdumpConfigPath = "vzdump.conf" // Relative path
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesDisabledOptions tests with disabled options
func TestCollectPVEDirectoriesDisabledOptions(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.BackupClusterConfig = false
	cfg.BackupPVEFirewall = false
	cfg.BackupVZDumpConfig = false
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestCollectPVEDirectoriesWithConfigDB tests config.db handling
func TestCollectPVEDirectoriesWithConfigDB(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create cluster directory with config.db
	clusterDir := filepath.Join(tmpDir, "var", "lib", "pve-cluster")
	if err := os.MkdirAll(clusterDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clusterDir, "config.db"), []byte("sqlite db"), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	cfg.PVEClusterPath = clusterDir
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	err := collector.collectPVEDirectories(context.Background(), false)
	if err != nil {
		t.Fatalf("collectPVEDirectories error: %v", err)
	}
}

// TestHasMultiplePVENodesDetailed tests multiple node detection
func TestHasMultiplePVENodesDetailed(t *testing.T) {
	t.Run("single node", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes")
		if err := os.MkdirAll(filepath.Join(nodesDir, "node1"), 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		result := collector.hasMultiplePVENodes()
		if result {
			t.Error("hasMultiplePVENodes() should return false for single node")
		}
	})

	t.Run("multiple nodes", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes")
		if err := os.MkdirAll(filepath.Join(nodesDir, "node1"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(nodesDir, "node2"), 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		result := collector.hasMultiplePVENodes()
		if !result {
			t.Error("hasMultiplePVENodes() should return true for multiple nodes")
		}
	})

	t.Run("empty nodes directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes")
		if err := os.MkdirAll(nodesDir, 0o755); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		result := collector.hasMultiplePVENodes()
		if result {
			t.Error("hasMultiplePVENodes() should return false for empty nodes dir")
		}
	})

	t.Run("nonexistent nodes directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		if err := os.MkdirAll(pveDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Note: nodes directory not created

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		result := collector.hasMultiplePVENodes()
		if result {
			t.Error("hasMultiplePVENodes() should return false for missing nodes dir")
		}
	})

	t.Run("nodes dir contains files not directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		pveDir := filepath.Join(tmpDir, "etc", "pve")
		nodesDir := filepath.Join(pveDir, "nodes")
		if err := os.MkdirAll(nodesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// Create files instead of directories
		if err := os.WriteFile(filepath.Join(nodesDir, "file1.txt"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(nodesDir, "file2.txt"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}

		logger := newTestLogger()
		cfg := GetDefaultCollectorConfig()
		cfg.PVEConfigPath = pveDir
		collector := NewCollector(logger, cfg, tmpDir, "pve", false)

		result := collector.hasMultiplePVENodes()
		if result {
			t.Error("hasMultiplePVENodes() should return false when nodes contains only files")
		}
	})
}

// TestEffectivePVEConfigPathDetailed tests effective config path resolution
func TestEffectivePVEConfigPathDetailed(t *testing.T) {
	tests := []struct {
		name       string
		configPath string
		expected   string
	}{
		{
			name:       "custom absolute path",
			configPath: "/custom/pve",
			expected:   "/custom/pve",
		},
		{
			name:       "empty path uses default",
			configPath: "",
			expected:   "/etc/pve",
		},
		{
			name:       "whitespace only uses default",
			configPath: "   ",
			expected:   "/etc/pve",
		},
		{
			name:       "path with whitespace gets trimmed",
			configPath: "  /my/pve  ",
			expected:   "/my/pve",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			logger := newTestLogger()
			cfg := GetDefaultCollectorConfig()
			cfg.PVEConfigPath = tt.configPath
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			result := collector.effectivePVEConfigPath()
			if result != tt.expected {
				t.Errorf("effectivePVEConfigPath() = %q, want %q", result, tt.expected)
			}
		})
	}
}
