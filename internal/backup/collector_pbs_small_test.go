package backup

import (
	"context"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectDatastoreConfigsEmpty(t *testing.T) {
	c := NewCollector(newTestLogger(), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxBS, false)
	runRecipeForTest(t, context.Background(), c, newPBSDatastoreConfigRecipe(), nil)
}
