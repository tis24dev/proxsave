package installer

import (
	"strings"
	"testing"
)

func TestPostInstallComponentDescription(t *testing.T) {
	if d := PostInstallComponentDescription("BACKUP_ZFS_CONFIG"); !strings.Contains(d, "ZFS") {
		t.Fatalf("expected a curated ZFS description, got %q", d)
	}
	// Case-insensitive + trims.
	if d := PostInstallComponentDescription("  backup_zfs_config  "); d == "" {
		t.Fatal("lookup must be case-insensitive and trim whitespace")
	}
	// Uncatalogued key -> empty (caller falls back to warnings).
	if d := PostInstallComponentDescription("BACKUP_DOES_NOT_EXIST"); d != "" {
		t.Fatalf("uncatalogued key must return empty, got %q", d)
	}
}
