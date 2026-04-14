package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectDatastoreConfigsEmpty(t *testing.T) {
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)
	if err := c.collectDatastoreConfigs(context.Background(), nil); err != nil {
		t.Fatalf("collectDatastoreConfigs should return nil on empty list, got %v", err)
	}
}

func TestCollectUserConfigsMissingUserListSmall(t *testing.T) {
	tmp := t.TempDir()
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	if err := c.collectUserConfigs(context.Background()); err != nil {
		t.Fatalf("collectUserConfigs error: %v", err)
	}
	usersDir := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "access-control")
	if _, err := os.Stat(filepath.Join(usersDir, "tokens.json")); err == nil {
		t.Fatalf("tokens.json should not be created when user_list.json is missing")
	}
}
