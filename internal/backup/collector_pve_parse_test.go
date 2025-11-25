package backup

import (
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
