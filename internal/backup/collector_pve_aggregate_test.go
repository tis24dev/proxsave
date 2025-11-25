package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAggregateBackupHistory tests aggregation of backup history JSON files
func TestAggregateBackupHistory(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	tests := []struct {
		name        string
		files       map[string]string
		expectError bool
		validateFn  func(t *testing.T, output string)
	}{
		{
			name: "single backup history file",
			files: map[string]string{
				"vm-100_backup_history.json": `[{"time":1234567890,"status":"ok"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "1234567890") {
					t.Error("output should contain backup history data")
				}
			},
		},
		{
			name: "multiple backup history files",
			files: map[string]string{
				"vm-100_backup_history.json": `[{"vmid":"100"}]`,
				"vm-101_backup_history.json": `[{"vmid":"101"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "100") || !strings.Contains(output, "101") {
					t.Error("output should contain both backup histories")
				}
			},
		},
		{
			name: "no backup history files",
			files: map[string]string{
				"other.json": `{"test":"data"}`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if output != "[]" {
					t.Errorf("output should be empty array, got: %s", output)
				}
			},
		},
		{
			name:        "empty directory",
			files:       map[string]string{},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if output != "[]" {
					t.Errorf("output should be empty array, got: %s", output)
				}
			},
		},
		{
			name: "mixed files",
			files: map[string]string{
				"vm-100_backup_history.json": `[{"vmid":"100"}]`,
				"notes.txt":                  "some notes",
				"config.json":                `{"test":"value"}`,
				"vm-101_backup_history.json": `[{"vmid":"101"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "100") || !strings.Contains(output, "101") {
					t.Error("output should contain backup histories but not other files")
				}
				if strings.Contains(output, "notes") || strings.Contains(output, "some notes") {
					t.Error("output should not contain non-backup files")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test directory
			jobsDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(jobsDir, 0755); err != nil {
				t.Fatal(err)
			}

			// Create test files
			for filename, content := range tt.files {
				path := filepath.Join(jobsDir, filename)
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Run aggregation
			targetFile := filepath.Join(jobsDir, "aggregated.json")
			err := collector.aggregateBackupHistory(context.Background(), jobsDir, targetFile)

			if tt.expectError {
				if err == nil {
					t.Error("aggregateBackupHistory() should return error")
				}
				return
			}

			if err != nil {
				t.Errorf("aggregateBackupHistory() unexpected error = %v", err)
				return
			}

			// Read and validate output
			output, err := os.ReadFile(targetFile)
			if err != nil {
				t.Fatalf("failed to read output file: %v", err)
			}

			tt.validateFn(t, string(output))
		})
	}
}

// TestAggregateReplicationStatus tests aggregation of replication status JSON files
func TestAggregateReplicationStatus(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	tests := []struct {
		name        string
		files       map[string]string
		expectError bool
		validateFn  func(t *testing.T, output string)
	}{
		{
			name: "single replication status file",
			files: map[string]string{
				"vm-100_replication_status.json": `[{"vmid":"100","status":"synced"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "100") || !strings.Contains(output, "synced") {
					t.Error("output should contain replication status data")
				}
			},
		},
		{
			name: "multiple replication status files",
			files: map[string]string{
				"vm-100_replication_status.json": `[{"vmid":"100"}]`,
				"vm-200_replication_status.json": `[{"vmid":"200"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "100") || !strings.Contains(output, "200") {
					t.Error("output should contain both replication statuses")
				}
			},
		},
		{
			name: "no replication status files",
			files: map[string]string{
				"backup.json": `{"test":"data"}`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if output != "[]" {
					t.Errorf("output should be empty array, got: %s", output)
				}
			},
		},
		{
			name:        "empty directory",
			files:       map[string]string{},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if output != "[]" {
					t.Errorf("output should be empty array, got: %s", output)
				}
			},
		},
		{
			name: "mixed files including subdirectories",
			files: map[string]string{
				"vm-100_replication_status.json": `[{"vmid":"100"}]`,
				"readme.txt":                     "documentation",
				"vm-200_replication_status.json": `[{"vmid":"200"}]`,
			},
			expectError: false,
			validateFn: func(t *testing.T, output string) {
				if !strings.Contains(output, "100") || !strings.Contains(output, "200") {
					t.Error("output should contain replication statuses")
				}
				if strings.Contains(output, "documentation") {
					t.Error("output should not contain non-replication files")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test directory
			replicationDir := filepath.Join(tmpDir, tt.name)
			if err := os.MkdirAll(replicationDir, 0755); err != nil {
				t.Fatal(err)
			}

			// Create test files
			for filename, content := range tt.files {
				path := filepath.Join(replicationDir, filename)
				if err := os.WriteFile(path, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Run aggregation
			targetFile := filepath.Join(replicationDir, "aggregated.json")
			err := collector.aggregateReplicationStatus(context.Background(), replicationDir, targetFile)

			if tt.expectError {
				if err == nil {
					t.Error("aggregateReplicationStatus() should return error")
				}
				return
			}

			if err != nil {
				t.Errorf("aggregateReplicationStatus() unexpected error = %v", err)
				return
			}

			// Read and validate output
			output, err := os.ReadFile(targetFile)
			if err != nil {
				t.Fatalf("failed to read output file: %v", err)
			}

			tt.validateFn(t, string(output))
		})
	}
}

// TestAggregateBackupHistoryNonexistentDir tests behavior with nonexistent directory
func TestAggregateBackupHistoryNonexistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	nonexistentDir := filepath.Join(tmpDir, "does_not_exist")
	targetFile := filepath.Join(tmpDir, "output.json")

	err := collector.aggregateBackupHistory(context.Background(), nonexistentDir, targetFile)
	if err == nil {
		t.Error("aggregateBackupHistory() should return error for nonexistent directory")
	}
}

// TestAggregateReplicationStatusNonexistentDir tests behavior with nonexistent directory
func TestAggregateReplicationStatusNonexistentDir(t *testing.T) {
	tmpDir := t.TempDir()
	logger := newTestLogger()
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(logger, cfg, tmpDir, "pve", false)

	nonexistentDir := filepath.Join(tmpDir, "does_not_exist")
	targetFile := filepath.Join(tmpDir, "output.json")

	err := collector.aggregateReplicationStatus(context.Background(), nonexistentDir, targetFile)
	if err == nil {
		t.Error("aggregateReplicationStatus() should return error for nonexistent directory")
	}
}
