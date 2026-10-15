package backup

import (
	"context"
	"testing"
)

func runRecipeForTest(t *testing.T, ctx context.Context, collector *Collector, r recipe, seed func(*collectionState)) *collectionState {
	t.Helper()

	state := newCollectionState(collector)
	if seed != nil {
		seed(state)
	}
	if err := runRecipe(ctx, r, state); err != nil {
		t.Fatalf("runRecipe(%s) failed: %v", r.Name, err)
	}
	return state
}

func newPBSCommandsRecipe() recipe {
	return recipe{Name: "pbs-commands", Bricks: newPBSRuntimeBricks()}
}

func newPBSDatastoreInventoryRecipe() recipe {
	return recipe{Name: "pbs-inventory", Bricks: newPBSInventoryBricks()}
}

func newPBSDatastoreConfigRecipe() recipe {
	return recipe{Name: "pbs-datastore-config", Bricks: newPBSDatastoreConfigBricks()}
}

func newPBSPXARRecipe() recipe {
	return recipe{Name: "pbs-pxar", Bricks: newPBSPXARBricks()}
}

func runSelectedBricksForTest(t *testing.T, ctx context.Context, collector *Collector, r recipe, seed func(*collectionState), ids ...BrickID) *collectionState {
	t.Helper()

	bricks := make([]collectionBrick, 0, len(ids))
	for _, id := range ids {
		bricks = append(bricks, requireBrick(t, r, id))
	}

	return runRecipeForTest(t, ctx, collector, recipe{
		Name:   r.Name + "-subset",
		Bricks: bricks,
	}, seed)
}
