package backup

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseNodeStorageList tests parsing PVE storage entries from JSON
func TestParseNodeStorageList(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expectError bool
		expected    []pveStorageEntry
	}{
		{
			name: "valid storage list",
			input: `[
				{"storage": "local", "path": "/var/lib/vz", "type": "dir", "content": "vztmpl,iso"},
				{"storage": "local-lvm", "path": "", "type": "lvmthin", "content": "images,rootdir"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "local", Path: "/var/lib/vz", Type: "dir", Content: "vztmpl,iso"},
				{Name: "local-lvm", Path: "", Type: "lvmthin", Content: "images,rootdir"},
			},
		},
		{
			name: "storage with name field instead of storage",
			input: `[
				{"name": "backup-store", "path": "/mnt/backup", "type": "dir", "content": "backup"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "backup-store", Path: "/mnt/backup", Type: "dir", Content: "backup"},
			},
		},
		{
			name: "storage with both name and storage fields",
			input: `[
				{"storage": "primary", "name": "secondary", "path": "/storage", "type": "dir", "content": "images"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "primary", Path: "/storage", Type: "dir", Content: "images"},
			},
		},
		{
			name: "entry with whitespace",
			input: `[
				{"storage": "  local  ", "path": "  /var/lib/vz  ", "type": "  dir  ", "content": "  iso  "}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "local", Path: "/var/lib/vz", Type: "dir", Content: "iso"},
			},
		},
		{
			name: "duplicate storage names",
			input: `[
				{"storage": "local", "path": "/path1", "type": "dir", "content": "iso"},
				{"storage": "local", "path": "/path2", "type": "dir", "content": "backup"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "local", Path: "/path1", Type: "dir", Content: "iso"},
			},
		},
		{
			name: "empty storage name",
			input: `[
				{"storage": "", "name": "", "path": "/path", "type": "dir", "content": "iso"},
				{"storage": "valid", "path": "/valid", "type": "dir", "content": "backup"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "valid", Path: "/valid", Type: "dir", Content: "backup"},
			},
		},
		{
			name:        "empty array",
			input:       `[]`,
			expectError: false,
			expected:    []pveStorageEntry{},
		},
		{
			name:        "invalid JSON",
			input:       `{not valid json}`,
			expectError: true,
			expected:    nil,
		},
		{
			name:        "null input",
			input:       `null`,
			expectError: false,
			expected:    []pveStorageEntry{},
		},
		{
			name: "mixed valid and empty entries",
			input: `[
				{"storage": "storage1", "path": "/path1", "type": "dir", "content": "iso"},
				{"storage": "", "path": "/path2", "type": "dir", "content": "backup"},
				{"storage": "storage2", "path": "/path3", "type": "nfs", "content": "images"}
			]`,
			expectError: false,
			expected: []pveStorageEntry{
				{Name: "storage1", Path: "/path1", Type: "dir", Content: "iso"},
				{Name: "storage2", Path: "/path3", Type: "nfs", Content: "images"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseNodeStorageList([]byte(tt.input))

			if tt.expectError {
				if err == nil {
					t.Error("parseNodeStorageList() should return error")
				}
				return
			}

			if err != nil {
				t.Errorf("parseNodeStorageList() unexpected error = %v", err)
				return
			}

			if len(result) != len(tt.expected) {
				t.Errorf("parseNodeStorageList() returned %d entries, want %d", len(result), len(tt.expected))
				return
			}

			for i, entry := range result {
				if entry.Name != tt.expected[i].Name {
					t.Errorf("entry[%d].Name = %q, want %q", i, entry.Name, tt.expected[i].Name)
				}
				if entry.Path != tt.expected[i].Path {
					t.Errorf("entry[%d].Path = %q, want %q", i, entry.Path, tt.expected[i].Path)
				}
				if entry.Type != tt.expected[i].Type {
					t.Errorf("entry[%d].Type = %q, want %q", i, entry.Type, tt.expected[i].Type)
				}
				if entry.Content != tt.expected[i].Content {
					t.Errorf("entry[%d].Content = %q, want %q", i, entry.Content, tt.expected[i].Content)
				}
			}
		})
	}
}

// TestParseStorageConfigEntries tests parsing PVE storage.cfg file
func TestParseStorageConfigEntries(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected []pveStorageEntry
	}{
		{
			name: "single dir storage",
			content: `dir: local
	path /var/lib/vz
	content images,iso,vztmpl`,
			expected: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images,iso,vztmpl"},
			},
		},
		{
			name: "multiple storage types",
			content: `dir: local
	path /var/lib/vz
	content images,iso

nfs: backup-nfs
	path /mnt/backup
	content backup

zfspool: local-zfs
	content images,rootdir`,
			expected: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images,iso"},
				{Name: "backup-nfs", Type: "nfs", Path: "/mnt/backup", Content: "backup"},
				{Name: "local-zfs", Type: "zfspool", Path: "", Content: "images,rootdir"},
			},
		},
		{
			name: "with comments and empty lines",
			content: `# This is a comment
dir: local
	path /var/lib/vz

# Another comment
	content images

dir: other
	path /mnt/other`,
			expected: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
				{Name: "other", Type: "dir", Path: "/mnt/other", Content: ""},
			},
		},
		{
			name: "cephfs and rbd storage",
			content: `cephfs: cephfs-storage
	path /mnt/ceph
	content backup,iso

rbd: ceph-rbd
	content images,rootdir`,
			expected: []pveStorageEntry{
				{Name: "cephfs-storage", Type: "cephfs", Path: "/mnt/ceph", Content: "backup,iso"},
				{Name: "ceph-rbd", Type: "rbd", Path: "", Content: "images,rootdir"},
			},
		},
		{
			name: "path with quotes",
			content: `dir: quoted
	path "/var/lib/vz with spaces"
	content iso`,
			expected: []pveStorageEntry{
				{Name: "quoted", Type: "dir", Path: "/var/lib/vz with spaces", Content: "iso"},
			},
		},
		{
			name: "content with spaces",
			content: `dir: local
	path /var/lib/vz
	content images, iso, backup`,
			expected: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images,iso,backup"},
			},
		},
		{
			name: "invalid storage definition without name",
			content: `dir:
	path /var/lib/vz

dir: valid
	path /mnt/valid`,
			expected: []pveStorageEntry{
				{Name: "valid", Type: "dir", Path: "/mnt/valid", Content: ""},
			},
		},
		{
			name: "storage type with space in name should be skipped",
			content: `some thing: invalid
	path /invalid

dir: valid
	path /valid`,
			expected: []pveStorageEntry{
				{Name: "valid", Type: "dir", Path: "/valid", Content: ""},
			},
		},
		{
			name:     "empty file",
			content:  "",
			expected: []pveStorageEntry{},
		},
		{
			name:     "only comments",
			content:  "# comment 1\n# comment 2",
			expected: []pveStorageEntry{},
		},
		{
			name: "attributes before first storage definition",
			content: `	path /orphan
	content iso

dir: actual
	path /actual`,
			expected: []pveStorageEntry{
				{Name: "actual", Type: "dir", Path: "/actual", Content: ""},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pveDir := filepath.Join(tmpDir, "etc", "pve")
			if err := os.MkdirAll(pveDir, 0o755); err != nil {
				t.Fatalf("failed to create pve dir: %v", err)
			}

			storageCfg := filepath.Join(pveDir, "storage.cfg")
			if err := os.WriteFile(storageCfg, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("failed to write storage.cfg: %v", err)
			}

			logger := newTestLogger()
			cfg := GetDefaultCollectorConfig()
			cfg.PVEConfigPath = pveDir
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			result := collector.parseStorageConfigEntries()

			if len(result) != len(tt.expected) {
				t.Errorf("parseStorageConfigEntries() returned %d entries, want %d", len(result), len(tt.expected))
				for i, r := range result {
					t.Logf("  result[%d]: %+v", i, r)
				}
				return
			}

			for i, entry := range result {
				if entry.Name != tt.expected[i].Name {
					t.Errorf("entry[%d].Name = %q, want %q", i, entry.Name, tt.expected[i].Name)
				}
				if entry.Type != tt.expected[i].Type {
					t.Errorf("entry[%d].Type = %q, want %q", i, entry.Type, tt.expected[i].Type)
				}
				if entry.Path != tt.expected[i].Path {
					t.Errorf("entry[%d].Path = %q, want %q", i, entry.Path, tt.expected[i].Path)
				}
				if entry.Content != tt.expected[i].Content {
					t.Errorf("entry[%d].Content = %q, want %q", i, entry.Content, tt.expected[i].Content)
				}
			}
		})
	}
}

// TestParseStorageConfigEntriesFileNotFound tests behavior when storage.cfg doesn't exist
func TestParseStorageConfigEntriesFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatalf("failed to create pve dir: %v", err)
	}
	// Note: storage.cfg is not created

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	result := collector.parseStorageConfigEntries()

	if result != nil {
		t.Errorf("parseStorageConfigEntries() should return nil when file not found, got %v", result)
	}
}

// TestAugmentStoragesWithConfig tests merging API storages with config file entries
func TestAugmentStoragesWithConfig(t *testing.T) {
	tests := []struct {
		name           string
		apiStorages    []pveStorageEntry
		configContent  string
		expectedCount  int
		expectedChecks map[string]pveStorageEntry // key is lowercase name
	}{
		{
			name:        "empty api storages with config entries",
			apiStorages: []pveStorageEntry{},
			configContent: `dir: local
	path /var/lib/vz
	content images`,
			expectedCount: 1,
			expectedChecks: map[string]pveStorageEntry{
				"local": {Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
			},
		},
		{
			name: "api storage missing path gets updated from config",
			apiStorages: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "", Content: "images"},
			},
			configContent: `dir: local
	path /var/lib/vz
	content backup`,
			expectedCount: 1,
			expectedChecks: map[string]pveStorageEntry{
				"local": {Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
			},
		},
		{
			name: "api storage missing type gets updated from config",
			apiStorages: []pveStorageEntry{
				{Name: "local", Type: "", Path: "/var/lib/vz", Content: "images"},
			},
			configContent: `dir: local
	path /other/path`,
			expectedCount: 1,
			expectedChecks: map[string]pveStorageEntry{
				"local": {Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
			},
		},
		{
			name: "api storage missing content gets updated from config",
			apiStorages: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: ""},
			},
			configContent: `dir: local
	path /var/lib/vz
	content backup,iso`,
			expectedCount: 1,
			expectedChecks: map[string]pveStorageEntry{
				"local": {Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "backup,iso"},
			},
		},
		{
			name: "case insensitive name matching",
			apiStorages: []pveStorageEntry{
				{Name: "LOCAL", Type: "dir", Path: "/api/path", Content: ""},
			},
			configContent: `dir: local
	path /config/path
	content images`,
			expectedCount: 1,
			expectedChecks: map[string]pveStorageEntry{
				"local": {Name: "LOCAL", Type: "dir", Path: "/api/path", Content: "images"},
			},
		},
		{
			name: "merge api and config with different storages",
			apiStorages: []pveStorageEntry{
				{Name: "api-only", Type: "dir", Path: "/api", Content: "images"},
			},
			configContent: `dir: config-only
	path /config
	content backup`,
			expectedCount: 2,
			expectedChecks: map[string]pveStorageEntry{
				"api-only":    {Name: "api-only", Type: "dir", Path: "/api", Content: "images"},
				"config-only": {Name: "config-only", Type: "dir", Path: "/config", Content: "backup"},
			},
		},
		{
			name: "empty config file returns original storages",
			apiStorages: []pveStorageEntry{
				{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
			},
			configContent:  "",
			expectedCount:  1,
			expectedChecks: nil, // skip detailed checks
		},
		{
			name: "skip empty name entries from api during merge",
			apiStorages: []pveStorageEntry{
				{Name: "", Type: "dir", Path: "/empty", Content: "images"},
				{Name: "valid", Type: "dir", Path: "/valid", Content: "backup"},
			},
			configContent: `dir: from-config
	path /config`,
			expectedCount: 2, // valid + from-config (empty name is skipped during merge)
			expectedChecks: map[string]pveStorageEntry{
				"valid":       {Name: "valid", Type: "dir", Path: "/valid", Content: "backup"},
				"from-config": {Name: "from-config", Type: "dir", Path: "/config", Content: ""},
			},
		},
		{
			name: "results are sorted alphabetically",
			apiStorages: []pveStorageEntry{
				{Name: "zebra", Type: "dir", Path: "/z", Content: ""},
				{Name: "alpha", Type: "dir", Path: "/a", Content: ""},
			},
			configContent: `dir: middle
	path /m`,
			expectedCount: 3,
			// Check order by verifying first entry is alpha
			expectedChecks: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pveDir := filepath.Join(tmpDir, "etc", "pve")
			if err := os.MkdirAll(pveDir, 0o755); err != nil {
				t.Fatalf("failed to create pve dir: %v", err)
			}

			storageCfg := filepath.Join(pveDir, "storage.cfg")
			if err := os.WriteFile(storageCfg, []byte(tt.configContent), 0o644); err != nil {
				t.Fatalf("failed to write storage.cfg: %v", err)
			}

			logger := newTestLogger()
			cfg := GetDefaultCollectorConfig()
			cfg.PVEConfigPath = pveDir
			collector := NewCollector(logger, cfg, tmpDir, "pve", false)

			result := collector.augmentStoragesWithConfig(tt.apiStorages)

			if len(result) != tt.expectedCount {
				t.Errorf("augmentStoragesWithConfig() returned %d entries, want %d", len(result), tt.expectedCount)
				for i, r := range result {
					t.Logf("  result[%d]: %+v", i, r)
				}
				return
			}

			if tt.expectedChecks != nil {
				resultMap := make(map[string]pveStorageEntry)
				for _, entry := range result {
					resultMap[entry.Name] = entry
				}

				for name, expected := range tt.expectedChecks {
					// Find by lowercase comparison
					var found *pveStorageEntry
					for _, entry := range result {
						if entry.Name == expected.Name || entry.Name == name {
							found = &entry
							break
						}
					}
					if found == nil {
						t.Errorf("expected entry %q not found in result", name)
						continue
					}
					if found.Type != expected.Type {
						t.Errorf("entry %q Type = %q, want %q", name, found.Type, expected.Type)
					}
					if found.Path != expected.Path {
						t.Errorf("entry %q Path = %q, want %q", name, found.Path, expected.Path)
					}
					if found.Content != expected.Content {
						t.Errorf("entry %q Content = %q, want %q", name, found.Content, expected.Content)
					}
				}
			}

			// Verify alphabetical sorting
			if tt.name == "results are sorted alphabetically" && len(result) >= 3 {
				if result[0].Name != "alpha" {
					t.Errorf("first entry should be 'alpha', got %q", result[0].Name)
				}
				if result[1].Name != "middle" {
					t.Errorf("second entry should be 'middle', got %q", result[1].Name)
				}
				if result[2].Name != "zebra" {
					t.Errorf("third entry should be 'zebra', got %q", result[2].Name)
				}
			}
		})
	}
}

// TestAugmentStoragesWithConfigNoFile tests behavior when config file doesn't exist
func TestAugmentStoragesWithConfigNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	pveDir := filepath.Join(tmpDir, "etc", "pve")
	if err := os.MkdirAll(pveDir, 0o755); err != nil {
		t.Fatalf("failed to create pve dir: %v", err)
	}
	// Note: storage.cfg is not created

	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	cfg.PVEConfigPath = pveDir
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	apiStorages := []pveStorageEntry{
		{Name: "local", Type: "dir", Path: "/var/lib/vz", Content: "images"},
	}

	result := collector.augmentStoragesWithConfig(apiStorages)

	if len(result) != 1 {
		t.Errorf("augmentStoragesWithConfig() should return original storages when config not found, got %d entries", len(result))
	}
	if result[0].Name != "local" {
		t.Errorf("expected 'local', got %q", result[0].Name)
	}
}
