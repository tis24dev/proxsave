package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The loader still read DISABLE_COLORS, a Bash-era name, and let it override
// USE_COLOR, while the integrity check reported it unknown and ignored: deleting the
// line as told brought the colors back. It is no longer read (approved).
func TestDisableColorsNoLongerOverridesUseColor(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(envPath, []byte("USE_COLOR=true\nDISABLE_COLORS=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigWithBaseDir(envPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfigWithBaseDir: %v", err)
	}
	if !cfg.UseColor {
		t.Fatalf("UseColor = false: DISABLE_COLORS still overrides USE_COLOR=true")
	}
}
