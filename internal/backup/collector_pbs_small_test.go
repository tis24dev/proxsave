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

func TestCollectUserTokensMissingUserList(t *testing.T) {
	tmp := t.TempDir()
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	usersDir := filepath.Join(tmp, "users")
	if err := os.MkdirAll(usersDir, 0o755); err != nil {
		t.Fatalf("mkdir users: %v", err)
	}
	c.collectUserTokens(context.Background(), usersDir)
	if _, err := os.Stat(filepath.Join(usersDir, "tokens.json")); err == nil {
		t.Fatalf("tokens.json should not be created when user_list.json is missing")
	}
}
