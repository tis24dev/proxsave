package installer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// An rclone path holding '#' (gdrive:Server #2/proxsave) was written unquoted: the
// loader read '#' as the start of a comment and the destination came back truncated
// to "gdrive:Server", which validation accepted. CLOUD_REMOTE and CLOUD_LOG_PATH are
// now quoted like SECONDARY_PATH, and load back whole.
func TestApplyInstallDataQuotesTheCloudPaths(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(base, "configs", "backup.env")
	data := &InstallWizardData{EnableCloudStorage: true, RcloneBackupRemote: "gdrive:Server #2/proxsave", RcloneLogRemote: "gdrive:Server #2/logs"}
	content, err := ApplyInstallData(config.DefaultEnvTemplate(), data)
	if err != nil {
		t.Fatalf("ApplyInstallData: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfigWithBaseDir(cfgPath, base)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.CloudRemote != "gdrive:Server #2/proxsave" || cfg.CloudLogPath != "gdrive:Server #2/logs" {
		t.Fatalf("loaded CLOUD_REMOTE=%q CLOUD_LOG_PATH=%q, want the paths whole", cfg.CloudRemote, cfg.CloudLogPath)
	}
}
