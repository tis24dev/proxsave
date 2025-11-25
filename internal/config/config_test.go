package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/types"
)

func setBaseDirEnv(t *testing.T, value string) func() {
	t.Helper()

	prev := os.Getenv("BASE_DIR")
	if value == "" {
		_ = os.Unsetenv("BASE_DIR")
	} else {
		if err := os.Setenv("BASE_DIR", value); err != nil {
			t.Fatalf("failed to set BASE_DIR: %v", err)
		}
	}

	return func() {
		if prev == "" {
			_ = os.Unsetenv("BASE_DIR")
		} else {
			_ = os.Setenv("BASE_DIR", prev)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test.env")

	content := `# Test configuration
BACKUP_ENABLED=true
DEBUG_LEVEL=5
USE_COLOR=true
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=9
BACKUP_PATH=/test/backup
LOG_PATH=/test/log
LOCK_PATH=/test/lock
SECONDARY_ENABLED=true
SECONDARY_PATH=/test/secondary
LOCAL_RETENTION_DAYS=7
TELEGRAM_ENABLED=false
METRICS_ENABLED=true
BACKUP_PVE_JOBS=false
PXAR_SCAN_ENABLE=false
CUSTOM_BACKUP_PATHS=/etc/custom,/var/data
BACKUP_BLACKLIST=/var/data/tmp
`

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/env/base/dir")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	// Test parsed values
	if !cfg.BackupEnabled {
		t.Error("Expected BackupEnabled to be true")
	}

	if cfg.DebugLevel != types.LogLevelDebug {
		t.Errorf("DebugLevel = %v; want %v", cfg.DebugLevel, types.LogLevelDebug)
	}

	if !cfg.UseColor {
		t.Error("Expected UseColor to be true")
	}

	if cfg.CompressionType != types.CompressionXZ {
		t.Errorf("CompressionType = %v; want %v", cfg.CompressionType, types.CompressionXZ)
	}

	if cfg.CompressionLevel != 9 {
		t.Errorf("CompressionLevel = %d; want 9", cfg.CompressionLevel)
	}

	if cfg.BackupPath != "/test/backup" {
		t.Errorf("BackupPath = %q; want %q", cfg.BackupPath, "/test/backup")
	}

	if !cfg.SecondaryEnabled {
		t.Error("Expected SecondaryEnabled to be true")
	}

	if cfg.SecondaryPath != "/test/secondary" {
		t.Errorf("SecondaryPath = %q; want %q", cfg.SecondaryPath, "/test/secondary")
	}

	if cfg.LocalRetentionDays != 7 {
		t.Errorf("LocalRetentionDays = %d; want 7", cfg.LocalRetentionDays)
	}

	if cfg.TelegramEnabled {
		t.Error("Expected TelegramEnabled to be false")
	}

	if !cfg.MetricsEnabled {
		t.Error("Expected MetricsEnabled to be true")
	}

	if cfg.BaseDir != "/env/base/dir" {
		t.Errorf("BaseDir = %q; want %q", cfg.BaseDir, "/env/base/dir")
	}

	if !cfg.SecurityCheckEnabled {
		t.Error("Expected SecurityCheckEnabled to be true by default")
	}

	if !cfg.AbortOnSecurityIssues {
		t.Error("Expected AbortOnSecurityIssues to be true by default")
	}

	if cfg.ContinueOnSecurityIssues {
		t.Error("Expected ContinueOnSecurityIssues to be false by default")
	}

	if cfg.AutoFixPermissions {
		t.Error("Expected AutoFixPermissions to be false by default")
	}

	if !cfg.AutoUpdateHashes {
		t.Error("Expected AutoUpdateHashes to be true by default")
	}

	if cfg.CheckNetworkSecurity {
		t.Error("Expected CheckNetworkSecurity to be false by default")
	}

	if len(cfg.SuspiciousPorts) == 0 {
		t.Error("Expected SuspiciousPorts to have default values")
	}

	if len(cfg.SuspiciousProcesses) == 0 {
		t.Error("Expected SuspiciousProcesses to have default values")
	}

	if len(cfg.SafeBracketProcesses) == 0 {
		t.Error("Expected SafeBracketProcesses to have default values")
	}

	if len(cfg.SafeKernelProcesses) == 0 {
		t.Error("Expected SafeKernelProcesses to have default values")
	}

	if cfg.EncryptArchive {
		t.Error("Expected EncryptArchive to be false by default")
	}

	if len(cfg.AgeRecipients) != 0 {
		t.Errorf("Expected AgeRecipients to be empty by default, got %#v", cfg.AgeRecipients)
	}

	if cfg.AgeRecipientFile != "" {
		t.Errorf("Expected AgeRecipientFile to be empty by default, got %q", cfg.AgeRecipientFile)
	}

	if cfg.BackupPVEJobs {
		t.Error("Expected BackupPVEJobs to be false")
	}

	if cfg.BackupPxarFiles {
		t.Error("Expected BackupPxarFiles to be false")
	}

	if len(cfg.CustomBackupPaths) != 2 || cfg.CustomBackupPaths[0] != "/etc/custom" || cfg.CustomBackupPaths[1] != "/var/data" {
		t.Errorf("CustomBackupPaths = %#v; want [/etc/custom /var/data]", cfg.CustomBackupPaths)
	}

	if len(cfg.BackupBlacklist) != 1 || cfg.BackupBlacklist[0] != "/var/data/tmp" {
		t.Errorf("BackupBlacklist = %#v; want [/var/data/tmp]", cfg.BackupBlacklist)
	}
}

func TestConfigAdvancedOptions(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "advanced.env")
	content := `CLOUD_REMOTE_PATH=/tenants/prod/
CLOUD_UPLOAD_MODE=PARALLEL
CLOUD_PARALLEL_MAX_JOBS=5
CLOUD_PARALLEL_VERIFICATION=true
PXAR_FILE_INCLUDE_PATTERN=*.pxar, catalog.pxar.*
PXAR_FILE_EXCLUDE_PATTERN=*.tmp;*.lock
PVE_CONFIG_PATH=/data/etc/pve
PVE_CLUSTER_PATH=/data/cluster
COROSYNC_CONFIG_PATH=/data/etc/pve/custom.conf
VZDUMP_CONFIG_PATH=/data/etc/vzdump.conf
PBS_CONFIG_PATH=/data/etc/pbs
PBS_DATASTORE_PATH=/mnt/pbs1,/mnt/pbs2,/mnt/pbs3
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create config file: %v", err)
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.CloudRemotePath != "tenants/prod" {
		t.Errorf("CloudRemotePath = %q; want %q", cfg.CloudRemotePath, "tenants/prod")
	}
	if cfg.CloudUploadMode != "parallel" {
		t.Errorf("CloudUploadMode = %q; want parallel", cfg.CloudUploadMode)
	}
	if cfg.CloudParallelJobs != 5 {
		t.Errorf("CloudParallelJobs = %d; want 5", cfg.CloudParallelJobs)
	}
	if !cfg.CloudParallelVerify {
		t.Error("CloudParallelVerify expected true")
	}
	if got := cfg.PxarFileIncludePatterns; len(got) != 2 || got[0] != "*.pxar" || got[1] != "catalog.pxar.*" {
		t.Errorf("PxarFileIncludePatterns = %#v; want [*.pxar catalog.pxar.*]", got)
	}
	if got := cfg.PxarFileExcludePatterns; len(got) != 2 || got[0] != "*.tmp" || got[1] != "*.lock" {
		t.Errorf("PxarFileExcludePatterns = %#v; want [*.tmp *.lock]", got)
	}
	if cfg.PVEConfigPath != "/data/etc/pve" {
		t.Errorf("PVEConfigPath = %q; want /data/etc/pve", cfg.PVEConfigPath)
	}
	if cfg.PVEClusterPath != "/data/cluster" {
		t.Errorf("PVEClusterPath = %q; want /data/cluster", cfg.PVEClusterPath)
	}
	if cfg.CorosyncConfigPath != "/data/etc/pve/custom.conf" {
		t.Errorf("CorosyncConfigPath = %q; want /data/etc/pve/custom.conf", cfg.CorosyncConfigPath)
	}
	if cfg.VzdumpConfigPath != "/data/etc/vzdump.conf" {
		t.Errorf("VzdumpConfigPath = %q; want /data/etc/vzdump.conf", cfg.VzdumpConfigPath)
	}
	if cfg.PBSConfigPath != "/data/etc/pbs" {
		t.Errorf("PBSConfigPath = %q; want /data/etc/pbs", cfg.PBSConfigPath)
	}
	if got := cfg.PBSDatastorePaths; len(got) != 3 || got[0] != "/mnt/pbs1" || got[1] != "/mnt/pbs2" || got[2] != "/mnt/pbs3" {
		t.Errorf("PBSDatastorePaths = %#v; want [/mnt/pbs1 /mnt/pbs2 /mnt/pbs3]", got)
	}
}

func TestConfigAgeRecipients(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "age.env")
	content := `ENCRYPT_ARCHIVE=true
AGE_RECIPIENT=age1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqs0cze5
AGE_RECIPIENT= age1rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrh0r
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/custom/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if !cfg.EncryptArchive {
		t.Fatal("EncryptArchive expected true")
	}

	if len(cfg.AgeRecipients) != 2 {
		t.Fatalf("AgeRecipients = %#v; want 2 entries", cfg.AgeRecipients)
	}

	if cfg.AgeRecipientFile != "/custom/base/identity/age/recipient.txt" {
		t.Errorf("AgeRecipientFile = %q; want %q", cfg.AgeRecipientFile, "/custom/base/identity/age/recipient.txt")
	}
}

func TestLoadConfigNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/path/config.env")
	if err == nil {
		t.Error("Expected error for nonexistent config file")
	}
}

func TestLoadConfigWithQuotes(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_quotes.env")

	content := `BACKUP_PATH="/path/with spaces/backup"
CLOUD_REMOTE='my-remote'
LOG_PATH=/path/without/quotes
`

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/quotes/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.BackupPath != "/path/with spaces/backup" {
		t.Errorf("BackupPath = %q; want %q", cfg.BackupPath, "/path/with spaces/backup")
	}

	if cfg.CloudRemote != "my-remote" {
		t.Errorf("CloudRemote = %q; want %q", cfg.CloudRemote, "my-remote")
	}

	if cfg.LogPath != "/path/without/quotes" {
		t.Errorf("LogPath = %q; want %q", cfg.LogPath, "/path/without/quotes")
	}

	if cfg.BaseDir != "/quotes/base" {
		t.Errorf("BaseDir = %q; want %q", cfg.BaseDir, "/quotes/base")
	}
}

func TestLoadConfigWithComments(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_comments.env")

	content := `# This is a comment
BACKUP_ENABLED=true
# Another comment
  # Comment with spaces
COMPRESSION_TYPE=xz

# Empty line above
DEBUG_LEVEL=4
`

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/comments/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if !cfg.BackupEnabled {
		t.Error("Expected BackupEnabled to be true")
	}

	if cfg.CompressionType != types.CompressionXZ {
		t.Errorf("CompressionType = %v; want %v", cfg.CompressionType, types.CompressionXZ)
	}

	if cfg.DebugLevel != types.LogLevelInfo {
		t.Errorf("DebugLevel = %v; want %v", cfg.DebugLevel, types.LogLevelInfo)
	}
}

func TestConfigGetSet(t *testing.T) {
	cfg := &Config{
		raw: make(map[string]string),
	}

	// Test Set
	cfg.Set("TEST_KEY", "test_value")

	// Test Get
	value, ok := cfg.Get("TEST_KEY")
	if !ok {
		t.Error("Expected key TEST_KEY to exist")
	}
	if value != "test_value" {
		t.Errorf("Get(TEST_KEY) = %q; want %q", value, "test_value")
	}

	// Test Get non-existent key
	_, ok = cfg.Get("NON_EXISTENT")
	if ok {
		t.Error("Expected NON_EXISTENT key to not exist")
	}
}

func TestConfigDefaults(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "empty.env")

	// Create empty config file
	if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/defaults/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	// Test default values
	if !cfg.BackupEnabled {
		t.Error("Expected default BackupEnabled to be true")
	}

	if cfg.DebugLevel != types.LogLevelInfo {
		t.Errorf("Default DebugLevel = %v; want %v", cfg.DebugLevel, types.LogLevelInfo)
	}

	if cfg.CompressionType != types.CompressionXZ {
		t.Errorf("Default CompressionType = %v; want %v", cfg.CompressionType, types.CompressionXZ)
	}

	if cfg.CompressionLevel != 6 {
		t.Errorf("Default CompressionLevel = %d; want 6", cfg.CompressionLevel)
	}

	if cfg.LocalRetentionDays != 7 {
		t.Errorf("Default LocalRetentionDays = %d; want 7", cfg.LocalRetentionDays)
	}

	if !cfg.EnableGoBackup {
		t.Error("Expected default EnableGoBackup to be true")
	}

	if cfg.BaseDir != "/defaults/base" {
		t.Errorf("Default BaseDir = %q; want %q", cfg.BaseDir, "/defaults/base")
	}
}

func TestEnableGoBackupFlag(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "go_pipeline.env")

	content := `ENABLE_GO_BACKUP=false
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/flag/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.EnableGoBackup {
		t.Error("Expected EnableGoBackup to be false when explicitly disabled")
	}
}

func TestLoadConfigBaseDirFromConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "base_dir.env")

	content := `BASE_DIR=/custom/base
BACKUP_PATH=${BASE_DIR}/backup-data
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if cfg.BaseDir != "/custom/base" {
		t.Errorf("BaseDir = %q; want %q", cfg.BaseDir, "/custom/base")
	}

	if cfg.BackupPath != "/custom/base/backup-data" {
		t.Errorf("BackupPath = %q; want %q", cfg.BackupPath, "/custom/base/backup-data")
	}
}

func TestRetentionPolicyDefaultAndExplicit(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "retention_policy.env")

	content := `
# Defaults only, no explicit policy
MAX_LOCAL_BACKUPS=10
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to create test config: %v", err)
	}

	cleanup := setBaseDirEnv(t, "/retention/base")
	defer cleanup()

	cfg, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if policy := cfg.GetRetentionPolicy(); policy != "simple" {
		t.Errorf("Default GetRetentionPolicy() = %q; want %q", policy, "simple")
	}
	if cfg.IsGFSRetentionEnabled() {
		t.Errorf("IsGFSRetentionEnabled() = true; want false for default simple policy")
	}

	// Now force explicit GFS policy
	contentGFS := `
RETENTION_POLICY=gfs
RETENTION_DAILY=0
RETENTION_WEEKLY=4
`
	if err := os.WriteFile(configPath, []byte(contentGFS), 0644); err != nil {
		t.Fatalf("Failed to overwrite test config: %v", err)
	}

	cfg, err = LoadConfig(configPath)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}

	if policy := cfg.GetRetentionPolicy(); policy != "gfs" {
		t.Errorf("Explicit GetRetentionPolicy() = %q; want %q", policy, "gfs")
	}
	if !cfg.IsGFSRetentionEnabled() {
		t.Errorf("IsGFSRetentionEnabled() = false; want true when RETENTION_POLICY=gfs")
	}
}

func TestParseSizeToBytes(t *testing.T) {
	t.Run("valid units", func(t *testing.T) {
		tests := []struct {
			input string
			want  int64
		}{
			{"10K", 10 * 1024},
			{"2m", 2 * 1024 * 1024},
			{"3G", 3 * 1024 * 1024 * 1024},
			{"1t", 1 * 1024 * 1024 * 1024 * 1024},
			{"42", 42},
		}
		for _, tt := range tests {
			got, err := parseSizeToBytes(tt.input)
			if err != nil {
				t.Fatalf("parseSizeToBytes(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("parseSizeToBytes(%q) = %d; want %d", tt.input, got, tt.want)
			}
		}
	})

	t.Run("invalid values", func(t *testing.T) {
		if _, err := parseSizeToBytes("-5"); err == nil {
			t.Fatal("expected error for negative value")
		}
		if _, err := parseSizeToBytes("K"); err == nil {
			t.Fatal("expected error for missing numeric value")
		}
	})
}

func TestAdjustLevelForMode(t *testing.T) {
	tests := []struct {
		comp   types.CompressionType
		mode   string
		level  int
		expect int
	}{
		{types.CompressionXZ, "fast", 6, 1},
		{types.CompressionZstd, "maximum", 3, 19},
		{types.CompressionZstd, "ultra", 3, 22},
		{types.CompressionXZ, "maximum", 2, 9},
		{types.CompressionZstd, "standard", 4, 4},
	}

	for _, tt := range tests {
		if got := adjustLevelForMode(tt.comp, tt.mode, tt.level); got != tt.expect {
			t.Fatalf("adjustLevelForMode(%s, %s, %d) = %d; want %d",
				tt.comp, tt.mode, tt.level, got, tt.expect)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := sanitizeMinDisk(-1); got != 10 {
		t.Fatalf("sanitizeMinDisk(-1) = %f; want 10", got)
	}
	if got := sanitizeMinDisk(5.5); got != 5.5 {
		t.Fatalf("sanitizeMinDisk(5.5) = %f; want 5.5", got)
	}
}

func TestNormalizeAndMergeLists(t *testing.T) {
	values := []string{" foo ", "", "bar", "  "}
	normalized := normalizeList(values)
	if len(normalized) != 2 || normalized[0] != "foo" || normalized[1] != "bar" {
		t.Fatalf("normalizeList = %#v; want [foo bar]", normalized)
	}

	merged := mergeStringSlices([]string{"a", "b"}, []string{"b", "c", " "})
	if len(merged) < 3 {
		t.Fatalf("mergeStringSlices unexpected length: %#v", merged)
	}
	for _, must := range []string{"a", "b", "c"} {
		found := false
		for _, v := range merged {
			if v == must {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("mergeStringSlices missing %s: %#v", must, merged)
		}
	}

	if res := mergeStringSlices(nil, nil); res != nil {
		t.Fatalf("mergeStringSlices(nil,nil) = %#v; want nil", res)
	}
}

func TestConfigGetIntList(t *testing.T) {
	cfg := &Config{
		raw: map[string]string{
			"TEST_INT_LIST": "1, 2; 3|4\n5",
		},
	}
	got := cfg.getIntList("TEST_INT_LIST", []int{})
	want := []int{1, 2, 3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("getIntList length mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("getIntList[%d] = %d; want %d", i, got[i], want[i])
		}
	}

	// Ensure default slice is copied, not referenced
	defaultSlice := []int{9, 9}
	got = cfg.getIntList("MISSING", defaultSlice)
	if len(got) != len(defaultSlice) || got[0] != 9 || got[1] != 9 {
		t.Fatalf("getIntList default copy failed: %v", got)
	}
	got[0] = 1
	if defaultSlice[0] != 9 {
		t.Fatalf("expected default slice to remain unchanged, got %v", defaultSlice)
	}
}
