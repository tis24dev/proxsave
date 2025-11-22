package orchestrator

import (
	"fmt"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/backup"
	"github.com/tis24dev/proxmox-backup/internal/notify"
)

// ========================================
// categories.go tests
// ========================================

func TestGetAllCategories(t *testing.T) {
	categories := GetAllCategories()

	if len(categories) == 0 {
		t.Fatal("GetAllCategories returned empty slice")
	}

	// Check for expected category IDs
	expectedIDs := []string{"pve_cluster", "pbs_config", "network", "ssl", "ssh"}
	for _, expectedID := range expectedIDs {
		found := false
		for _, cat := range categories {
			if cat.ID == expectedID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected category ID %q not found", expectedID)
		}
	}
}

func TestGetPVECategories(t *testing.T) {
	categories := GetPVECategories()

	for _, cat := range categories {
		if cat.Type != CategoryTypePVE {
			t.Errorf("GetPVECategories returned non-PVE category: %s (type=%s)", cat.ID, cat.Type)
		}
	}

	// Should have at least pve_cluster
	found := false
	for _, cat := range categories {
		if cat.ID == "pve_cluster" {
			found = true
			break
		}
	}
	if !found {
		t.Error("GetPVECategories should include pve_cluster")
	}
}

func TestGetPBSCategories(t *testing.T) {
	categories := GetPBSCategories()

	for _, cat := range categories {
		if cat.Type != CategoryTypePBS {
			t.Errorf("GetPBSCategories returned non-PBS category: %s (type=%s)", cat.ID, cat.Type)
		}
	}

	// Should have at least pbs_config
	found := false
	for _, cat := range categories {
		if cat.ID == "pbs_config" {
			found = true
			break
		}
	}
	if !found {
		t.Error("GetPBSCategories should include pbs_config")
	}
}

func TestGetCommonCategories(t *testing.T) {
	categories := GetCommonCategories()

	for _, cat := range categories {
		if cat.Type != CategoryTypeCommon {
			t.Errorf("GetCommonCategories returned non-common category: %s (type=%s)", cat.ID, cat.Type)
		}
	}

	// Should have network, ssl, ssh
	expectedIDs := []string{"network", "ssl", "ssh"}
	for _, expectedID := range expectedIDs {
		found := false
		for _, cat := range categories {
			if cat.ID == expectedID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("GetCommonCategories should include %s", expectedID)
		}
	}
}

func TestGetCategoriesForSystem(t *testing.T) {
	tests := []struct {
		name       string
		systemType string
		wantPVE    bool
		wantPBS    bool
		wantCommon bool
	}{
		{"pve system", "pve", true, false, true},
		{"pbs system", "pbs", false, true, true},
		{"unknown system", "unknown", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			categories := GetCategoriesForSystem(tt.systemType)

			hasPVE := false
			hasPBS := false
			hasCommon := false

			for _, cat := range categories {
				switch cat.Type {
				case CategoryTypePVE:
					hasPVE = true
				case CategoryTypePBS:
					hasPBS = true
				case CategoryTypeCommon:
					hasCommon = true
				}
			}

			if hasPVE != tt.wantPVE {
				t.Errorf("PVE categories: got %v, want %v", hasPVE, tt.wantPVE)
			}
			if hasPBS != tt.wantPBS {
				t.Errorf("PBS categories: got %v, want %v", hasPBS, tt.wantPBS)
			}
			if hasCommon != tt.wantCommon {
				t.Errorf("Common categories: got %v, want %v", hasCommon, tt.wantCommon)
			}
		})
	}
}

func TestPathMatchesCategory(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		category Category
		want     bool
	}{
		{
			name:     "absolute path match",
			filePath: "/etc/hosts",
			category: Category{Paths: []string{"/etc/hosts"}},
			want:     false, // current implementation expects ./-prefixed matches
		},
		{
			name:     "exact match with prefix",
			filePath: "./etc/hosts",
			category: Category{Paths: []string{"./etc/hosts"}},
			want:     true,
		},
		{
			name:     "exact match without prefix",
			filePath: "etc/hosts",
			category: Category{Paths: []string{"./etc/hosts"}},
			want:     true,
		},
		{
			name:     "directory prefix match",
			filePath: "./etc/network/interfaces",
			category: Category{Paths: []string{"./etc/network/"}},
			want:     true,
		},
		{
			name:     "directory match without trailing slash",
			filePath: "./etc/network",
			category: Category{Paths: []string{"./etc/network/"}},
			want:     true,
		},
		{
			name:     "no match different path",
			filePath: "./var/log/syslog",
			category: Category{Paths: []string{"./etc/hosts"}},
			want:     false,
		},
		{
			name:     "no match partial directory",
			filePath: "./etc/networkOther/file",
			category: Category{Paths: []string{"./etc/network/"}},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PathMatchesCategory(tt.filePath, tt.category)
			if got != tt.want {
				t.Errorf("PathMatchesCategory(%q, %+v) = %v; want %v",
					tt.filePath, tt.category.Paths, got, tt.want)
			}
		})
	}
}

func TestGetSelectedPaths(t *testing.T) {
	categories := []Category{
		{ID: "network", Paths: []string{"./etc/network/", "./etc/hosts"}},
		{ID: "ssh", Paths: []string{"./root/.ssh/", "./etc/ssh/"}},
	}

	paths := GetSelectedPaths(categories)

	expectedPaths := []string{"./etc/network/", "./etc/hosts", "./root/.ssh/", "./etc/ssh/"}
	if len(paths) != len(expectedPaths) {
		t.Errorf("GetSelectedPaths returned %d paths; want %d", len(paths), len(expectedPaths))
	}

	// Check all expected paths are present (order may vary due to map)
	pathMap := make(map[string]bool)
	for _, p := range paths {
		pathMap[p] = true
	}
	for _, expected := range expectedPaths {
		if !pathMap[expected] {
			t.Errorf("Expected path %q not found in result", expected)
		}
	}
}

func TestGetSelectedPathsDeduplication(t *testing.T) {
	// Two categories sharing the same path
	categories := []Category{
		{ID: "cat1", Paths: []string{"./etc/hosts", "./etc/network/"}},
		{ID: "cat2", Paths: []string{"./etc/hosts", "./var/log/"}},
		{ID: "cat3", Paths: []string{"/etc/hosts"}}, // absolute duplicate
	}

	paths := GetSelectedPaths(categories)

	// Should have 3 unique paths, not 4
	pathCount := make(map[string]int)
	for _, p := range paths {
		pathCount[p]++
	}

	for path, count := range pathCount {
		if count > 1 {
			t.Errorf("Path %q appears %d times (should be deduplicated)", path, count)
		}
	}
}

func TestGetCategoryByID(t *testing.T) {
	categories := []Category{
		{ID: "network", Name: "Network Configuration"},
		{ID: "ssh", Name: "SSH Configuration"},
		{ID: "ssl", Name: "SSL Certificates"},
	}

	tests := []struct {
		name     string
		id       string
		wantName string
		wantNil  bool
	}{
		{"found network", "network", "Network Configuration", false},
		{"found ssh", "ssh", "SSH Configuration", false},
		{"not found", "nonexistent", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetCategoryByID(tt.id, categories)
			if tt.wantNil {
				if result != nil {
					t.Errorf("GetCategoryByID(%q) = %+v; want nil", tt.id, result)
				}
			} else {
				if result == nil {
					t.Fatalf("GetCategoryByID(%q) = nil; want non-nil", tt.id)
				}
				if result.Name != tt.wantName {
					t.Errorf("GetCategoryByID(%q).Name = %q; want %q", tt.id, result.Name, tt.wantName)
				}
			}
		})
	}

	t.Run("empty slice returns nil", func(t *testing.T) {
		if got := GetCategoryByID("network", nil); got != nil {
			t.Fatalf("expected nil for empty categories, got %+v", got)
		}
	})
}

func TestGetStorageModeCategories(t *testing.T) {
	pveCategories := GetStorageModeCategories("pve")
	pbsCategories := GetStorageModeCategories("pbs")

	// PVE should include pve_cluster, storage_pve
	pveIDs := make(map[string]bool)
	for _, cat := range pveCategories {
		pveIDs[cat.ID] = true
	}
	if !pveIDs["pve_cluster"] {
		t.Error("PVE storage mode should include pve_cluster")
	}

	// PBS should include pbs_config, datastore_pbs
	pbsIDs := make(map[string]bool)
	for _, cat := range pbsCategories {
		pbsIDs[cat.ID] = true
	}
	if !pbsIDs["pbs_config"] {
		t.Error("PBS storage mode should include pbs_config")
	}
}

func TestGetBaseModeCategories(t *testing.T) {
	categories := GetBaseModeCategories()

	ids := make(map[string]bool)
	for _, cat := range categories {
		ids[cat.ID] = true
	}

	expectedIDs := []string{"network", "ssl", "ssh", "services"}
	for _, expected := range expectedIDs {
		if !ids[expected] {
			t.Errorf("Base mode should include %s", expected)
		}
	}
}

// ========================================
// compatibility.go tests
// ========================================

func TestDetectBackupType(t *testing.T) {
	tests := []struct {
		name     string
		manifest *backup.Manifest
		want     SystemType
	}{
		{
			name:     "nil manifest",
			manifest: nil,
			want:     SystemTypeUnknown,
		},
		{
			name:     "PVE type explicit",
			manifest: &backup.Manifest{ProxmoxType: "pve"},
			want:     SystemTypePVE,
		},
		{
			name:     "PBS type explicit",
			manifest: &backup.Manifest{ProxmoxType: "pbs"},
			want:     SystemTypePBS,
		},
		{
			name:     "PVE type from proxmox-ve",
			manifest: &backup.Manifest{ProxmoxType: "proxmox-ve"},
			want:     SystemTypePVE,
		},
		{
			name:     "PBS type from proxmox-backup",
			manifest: &backup.Manifest{ProxmoxType: "proxmox-backup"},
			want:     SystemTypePBS,
		},
		{
			name:     "PVE from hostname",
			manifest: &backup.Manifest{ProxmoxType: "", Hostname: "pve-node1"},
			want:     SystemTypePVE,
		},
		{
			name:     "PBS from hostname",
			manifest: &backup.Manifest{ProxmoxType: "", Hostname: "pbs-backup"},
			want:     SystemTypePBS,
		},
		{
			name:     "unknown type",
			manifest: &backup.Manifest{ProxmoxType: "", Hostname: "server1"},
			want:     SystemTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectBackupType(tt.manifest)
			if got != tt.want {
				t.Errorf("DetectBackupType() = %v; want %v", got, tt.want)
			}
		})
	}
}

func TestGetSystemTypeString(t *testing.T) {
	tests := []struct {
		input SystemType
		want  string
	}{
		{SystemTypePVE, "Proxmox Virtual Environment (PVE)"},
		{SystemTypePBS, "Proxmox Backup Server (PBS)"},
		{SystemTypeUnknown, "Unknown System"},
		{SystemType("other"), "Unknown System"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			got := GetSystemTypeString(tt.input)
			if got != tt.want {
				t.Errorf("GetSystemTypeString(%v) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ========================================
// extensions.go tests
// ========================================

func TestBuildCloudLogDestination(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
		fileName string
		want     string
	}{
		{"empty base", "", "backup.log", "backup.log"},
		{"remote only colon", "remote:", "backup.log", "remote:backup.log"},
		{"remote with path", "remote:/logs", "backup.log", "remote:/logs/backup.log"},
		{"remote with trailing slash", "remote:/logs/", "backup.log", "remote:/logs/backup.log"},
		{"local path", "/var/log", "backup.log", "/var/log/backup.log"},
		{"spaces trimmed", "  remote:/logs  ", "backup.log", "remote:/logs/backup.log"},
		{"empty filename", "remote:/logs", "", "remote:/logs/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildCloudLogDestination(tt.basePath, tt.fileName)
			if got != tt.want {
				t.Errorf("buildCloudLogDestination(%q, %q) = %q; want %q",
					tt.basePath, tt.fileName, got, tt.want)
			}
		})
	}
}

func TestDescribeEarlyErrorPhase(t *testing.T) {
	tests := []struct {
		phase string
		want  string
	}{
		{"encryption_setup", "Encryption setup failed"},
		{"checker_config", "Checker configuration failed"},
		{"storage_init", "Storage initialization failed"},
		{"pre_backup_checks", "Pre-backup checks failed"},
		{"ENCRYPTION_SETUP", "Encryption setup failed"},
		{" encryption_setup ", "Encryption setup failed"},
		{"", "Initialization failed"},
		{"custom_phase", "custom_phase failed"},
	}

	for _, tt := range tests {
		t.Run(tt.phase, func(t *testing.T) {
			got := describeEarlyErrorPhase(tt.phase)
			if got != tt.want {
				t.Errorf("describeEarlyErrorPhase(%q) = %q; want %q", tt.phase, got, tt.want)
			}
		})
	}
}

// ========================================
// selective.go tests
// ========================================

func TestPathMatchesPattern(t *testing.T) {
	tests := []struct {
		name        string
		archivePath string
		pattern     string
		want        bool
	}{
		{"exact match both prefixed", "./etc/hosts", "./etc/hosts", true},
		{"exact match archive unprefixed", "etc/hosts", "./etc/hosts", true},
		{"exact match pattern unprefixed", "./etc/hosts", "etc/hosts", true},
		{"directory match with trailing slash", "./etc/network/interfaces", "./etc/network/", true},
		{"directory match file in subdir", "./etc/network/interfaces.d/config", "./etc/network/", true},
		{"no match different path", "./var/log/syslog", "./etc/hosts", false},
		{"no match partial overlap", "./etc/networking/config", "./etc/network/", false},
		{"parent directory match", "./etc/network/subdir/file", "./etc/network", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathMatchesPattern(tt.archivePath, tt.pattern)
			if got != tt.want {
				t.Errorf("pathMatchesPattern(%q, %q) = %v; want %v",
					tt.archivePath, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestFilterAvailable(t *testing.T) {
	requested := []Category{
		{ID: "network", Name: "Network"},
		{ID: "ssh", Name: "SSH"},
		{ID: "ssl", Name: "SSL"},
	}

	available := []Category{
		{ID: "network", Name: "Network", IsAvailable: true},
		{ID: "ssl", Name: "SSL", IsAvailable: true},
		{ID: "zfs", Name: "ZFS", IsAvailable: true},
	}

	result := filterAvailable(requested, available)

	if len(result) != 2 {
		t.Fatalf("filterAvailable returned %d categories; want 2", len(result))
	}

	ids := make(map[string]bool)
	for _, cat := range result {
		ids[cat.ID] = true
	}

	if !ids["network"] || !ids["ssl"] {
		t.Error("filterAvailable should include network and ssl")
	}
	if ids["ssh"] {
		t.Error("filterAvailable should not include ssh (not in available)")
	}
}

func TestFilterOutExportOnly(t *testing.T) {
	categories := []Category{
		{ID: "pve_config_export", Name: "PVE Export", ExportOnly: true},
		{ID: "network", Name: "Network", ExportOnly: false},
		{ID: "ssh", Name: "SSH", ExportOnly: false},
	}

	result := filterOutExportOnly(categories)

	if len(result) != 2 {
		t.Fatalf("filterOutExportOnly returned %d categories; want 2", len(result))
	}

	for _, cat := range result {
		if cat.ExportOnly {
			t.Errorf("filterOutExportOnly included export-only category: %s", cat.ID)
		}
	}
}

func TestFilterOutExportOnlyEmpty(t *testing.T) {
	result := filterOutExportOnly(nil)
	if result != nil {
		t.Errorf("filterOutExportOnly(nil) should return nil, got %v", result)
	}

	result = filterOutExportOnly([]Category{})
	if len(result) != 0 {
		t.Errorf("filterOutExportOnly([]) should return empty slice, got %v", result)
	}
}

func TestGetCategoriesForMode(t *testing.T) {
	available := []Category{
		{ID: "pve_cluster", Type: CategoryTypePVE, IsAvailable: true},
		{ID: "storage_pve", Type: CategoryTypePVE, IsAvailable: true},
		{ID: "network", Type: CategoryTypeCommon, IsAvailable: true},
		{ID: "ssh", Type: CategoryTypeCommon, IsAvailable: true},
		{ID: "zfs", Type: CategoryTypeCommon, IsAvailable: true},
		{ID: "datastore_pbs", Type: CategoryTypePBS, IsAvailable: true},
		{ID: "pbs_config", Type: CategoryTypePBS, IsAvailable: true},
	}

	tests := []struct {
		name       string
		mode       RestoreMode
		systemType SystemType
		wantCount  int
	}{
		{"full mode", RestoreModeFull, SystemTypePVE, 7},
		{"custom mode returns empty", RestoreModeCustom, SystemTypePVE, 0},
		{"storage mode PBS filters PBS", RestoreModeStorage, SystemTypePBS, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetCategoriesForMode(tt.mode, tt.systemType, available)
			if len(result) != tt.wantCount {
				t.Errorf("GetCategoriesForMode(%v, %v) returned %d categories; want %d",
					tt.mode, tt.systemType, len(result), tt.wantCount)
			}
		})
	}
}

// ========================================
// storage_adapter.go tests
// ========================================

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{1099511627776, "1.0 TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.input)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestClampInt64ToUint64(t *testing.T) {
	tests := []struct {
		input int64
		want  uint64
	}{
		{-100, 0},
		{-1, 0},
		{0, 0},
		{1, 1},
		{100, 100},
		{9223372036854775807, 9223372036854775807}, // max int64
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got := clampInt64ToUint64(tt.input)
			if got != tt.want {
				t.Errorf("clampInt64ToUint64(%d) = %d; want %d", tt.input, got, tt.want)
			}
		})
	}
}

// ========================================
// notification_adapter.go tests
// ========================================

func TestFormatBytesHR(t *testing.T) {
	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
		{1099511627776, "1.00 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytesHR(tt.input)
			if got != tt.want {
				t.Errorf("formatBytesHR(%d) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCalculateUsagePercent(t *testing.T) {
	tests := []struct {
		name      string
		freeBytes uint64
		total     uint64
		want      float64
	}{
		{"zero total", 0, 0, 0.0},
		{"50% used", 500, 1000, 50.0},
		{"100% full", 0, 1000, 100.0},
		{"empty disk", 1000, 1000, 0.0},
		{"25% used", 750, 1000, 25.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateUsagePercent(tt.freeBytes, tt.total)
			if got != tt.want {
				t.Errorf("calculateUsagePercent(%d, %d) = %f; want %f",
					tt.freeBytes, tt.total, got, tt.want)
			}
		})
	}
}

func TestCalculateUsedBytes(t *testing.T) {
	tests := []struct {
		name      string
		freeBytes uint64
		total     uint64
		want      uint64
	}{
		{"zero total", 0, 0, 0},
		{"normal usage", 300, 1000, 700},
		{"full disk", 0, 1000, 1000},
		{"empty disk", 1000, 1000, 0},
		{"free > total (invalid)", 1500, 1000, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calculateUsedBytes(tt.freeBytes, tt.total)
			if got != tt.want {
				t.Errorf("calculateUsedBytes(%d, %d) = %d; want %d",
					tt.freeBytes, tt.total, got, tt.want)
			}
		})
	}
}

func TestFormatPercentString(t *testing.T) {
	tests := []struct {
		input float64
		want  string
	}{
		{0.0, "0%"},
		{-5.0, "0%"},
		{50.0, "50.0%"},
		{99.9, "99.9%"},
		{100.0, "100.0%"},
		{33.333, "33.3%"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatPercentString(tt.input)
			if got != tt.want {
				t.Errorf("formatPercentString(%f) = %q; want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFormatBackupStatusSummary(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		count  int
		max    int
		want   string
	}{
		{"gfs policy", "gfs", 5, 10, "5/-"},
		{"simple policy", "simple", 5, 10, "5/10"},
		{"simple no max", "simple", 3, 0, "3/?"},
		{"simple zero count no max", "simple", 0, 0, "0/?"},
		{"empty policy", "", 7, 14, "7/14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBackupStatusSummary(tt.policy, tt.count, tt.max)
			if got != tt.want {
				t.Errorf("formatBackupStatusSummary(%q, %d, %d) = %q; want %q",
					tt.policy, tt.count, tt.max, got, tt.want)
			}
		})
	}
}

func TestDescribeNotificationResult(t *testing.T) {
	tests := []struct {
		name   string
		result *notify.NotificationResult
		want   string
	}{
		{"nil result", nil, "unknown"},
		{
			"success with method",
			&notify.NotificationResult{Success: true, Method: "SMTP"},
			"sent (SMTP)",
		},
		{
			"success no method",
			&notify.NotificationResult{Success: true, Method: ""},
			"sent",
		},
		{
			"fallback with method",
			&notify.NotificationResult{Success: true, UsedFallback: true, Method: "sendmail"},
			"sent via sendmail fallback",
		},
		{
			"fallback no method",
			&notify.NotificationResult{Success: true, UsedFallback: true, Method: ""},
			"sent via fallback",
		},
		{
			"failure with error",
			&notify.NotificationResult{Success: false, Error: fmt.Errorf("connection timeout")},
			"failed: connection timeout",
		},
		{
			"failure no error",
			&notify.NotificationResult{Success: false},
			"failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeNotificationResult(tt.result)
			if got != tt.want {
				t.Errorf("describeNotificationResult() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestDescribeNotificationSeverity(t *testing.T) {
	tests := []struct {
		name   string
		result *notify.NotificationResult
		want   string
	}{
		{"nil result", nil, "disabled"},
		{"success", &notify.NotificationResult{Success: true}, "ok"},
		{"success with fallback", &notify.NotificationResult{Success: true, UsedFallback: true}, "warning"},
		{"failure", &notify.NotificationResult{Success: false}, "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeNotificationSeverity(tt.result)
			if got != tt.want {
				t.Errorf("describeNotificationSeverity() = %q; want %q", got, tt.want)
			}
		})
	}
}

// ========================================
// Benchmark tests for performance-critical functions
// ========================================

func BenchmarkPathMatchesCategory(b *testing.B) {
	category := Category{
		Paths: []string{"./etc/network/", "./etc/hosts", "./var/lib/pve-cluster/"},
	}
	filePath := "./etc/network/interfaces.d/config"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		PathMatchesCategory(filePath, category)
	}
}

func BenchmarkFormatBytes(b *testing.B) {
	sizes := []int64{0, 1024, 1048576, 1073741824, 1099511627776}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, size := range sizes {
			formatBytes(size)
		}
	}
}

func BenchmarkGetAllCategories(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		GetAllCategories()
	}
}
