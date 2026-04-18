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
