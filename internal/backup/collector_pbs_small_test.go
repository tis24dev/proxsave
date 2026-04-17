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
	runRecipeForTest(t, context.Background(), c, newPBSDatastoreConfigRecipe(), nil)
}

func TestCollectUserConfigsMissingUserListSmall(t *testing.T) {
	tmp := t.TempDir()
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), tmp, types.ProxmoxBS, false)
	runRecipeForTest(t, context.Background(), c, newPBSUserConfigRecipe(), nil)
	usersDir := filepath.Join(tmp, "var/lib/proxsave-info", "pbs", "access-control")
	if _, err := os.Stat(filepath.Join(usersDir, "tokens.json")); err == nil {
		t.Fatalf("tokens.json should not be created when user_list.json is missing")
	}
}
