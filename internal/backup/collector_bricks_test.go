package backup

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestRunRecipeRunsBricksInOrder(t *testing.T) {
	var ran []BrickID
	state := &collectionState{}
	r := recipe{
		Name: "ordered",
		Bricks: []collectionBrick{
			{
				ID: brickSystemDirectories,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemDirectories)
					return nil
				},
			},
			{
				ID: brickSystemCommands,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemCommands)
					return nil
				},
			},
			{
				ID: brickSystemKernel,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemKernel)
					return nil
				},
			},
		},
	}

	if err := runRecipe(context.Background(), r, state); err != nil {
		t.Fatalf("runRecipe failed: %v", err)
	}

	want := []BrickID{brickSystemDirectories, brickSystemCommands, brickSystemKernel}
	if !reflect.DeepEqual(ran, want) {
		t.Fatalf("brick order = %v, want %v", ran, want)
	}
}

func TestRunRecipeStopsOnFirstError(t *testing.T) {
	wantErr := errors.New("stop")
	var ran []BrickID
	r := recipe{
		Name: "stop-on-error",
		Bricks: []collectionBrick{
			{
				ID: brickSystemDirectories,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemDirectories)
					return nil
				},
			},
			{
				ID: brickSystemCommands,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemCommands)
					return wantErr
				},
			},
			{
				ID: brickSystemKernel,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemKernel)
					return nil
				},
			},
		},
	}

	err := runRecipe(context.Background(), r, &collectionState{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("runRecipe error = %v, want %v", err, wantErr)
	}

	wantRan := []BrickID{brickSystemDirectories, brickSystemCommands}
	if !reflect.DeepEqual(ran, wantRan) {
		t.Fatalf("brick order after error = %v, want %v", ran, wantRan)
	}
}

func TestRunRecipePropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ran := false
	r := recipe{
		Name: "canceled",
		Bricks: []collectionBrick{
			{
				ID: brickSystemDirectories,
				Run: func(context.Context, *collectionState) error {
					ran = true
					return nil
				},
			},
		},
	}

	err := runRecipe(ctx, r, &collectionState{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runRecipe error = %v, want %v", err, context.Canceled)
	}
	if ran {
		t.Fatalf("expected no brick execution after context cancellation")
	}
}

func TestNewPVERecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newPVERecipe())
	want := []BrickID{
		brickPVEValidateAndCluster,
		brickPVEDirectories,
		brickPVECommands,
		brickPVEVMConfigs,
		brickPVEJobs,
		brickPVESchedules,
		brickPVEReplication,
		brickPVEStorageMetadata,
		brickPVECeph,
		brickPVEFinalize,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PVE recipe IDs = %v, want %v", got, want)
	}
}

func TestNewPBSRecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newPBSRecipe())
	want := []BrickID{
		brickPBSValidate,
		brickPBSDirectories,
		brickPBSDatastoreDiscovery,
		brickPBSCommands,
		brickPBSDatastoreInventory,
		brickPBSDatastoreConfigs,
		brickPBSUserConfigs,
		brickPBSPXAR,
		brickPBSFinalize,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PBS recipe IDs = %v, want %v", got, want)
	}
}

func TestNewSystemRecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newSystemRecipe())
	want := []BrickID{
		brickSystemDirectories,
		brickSystemCommands,
		brickSystemKernel,
		brickSystemHardware,
		brickSystemCriticalFiles,
		brickSystemConfigFile,
		brickSystemCustomPaths,
		brickSystemScriptDirs,
		brickSystemScriptRepo,
		brickSystemSSHKeys,
		brickSystemRootHome,
		brickSystemUserHomes,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system recipe IDs = %v, want %v", got, want)
	}
}

func TestPVECommandsBrickPopulatesRuntimeInfo(t *testing.T) {
	collector := newPVECollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "pveversion":
				return []byte("pve-manager/9.1.0\n"), nil
			case "pvesh":
				if len(args) >= 2 && args[0] == "get" && args[1] == "/nodes" {
					return []byte(`[{"node":"node1"}]`), nil
				}
				if len(args) >= 3 && args[0] == "get" && args[1] == "/nodes/test-node/storage" {
					return []byte(`[{"storage":"local","type":"dir"}]`), nil
				}
				return []byte("[]"), nil
			default:
				return []byte{}, nil
			}
		},
	})

	state := newCollectionState(collector)
	state.pve.clustered = false
	commandsBrick := newPVERecipe().Bricks[2]

	if err := commandsBrick.Run(context.Background(), state); err != nil {
		t.Fatalf("pve commands brick failed: %v", err)
	}
	if state.pve.runtimeInfo == nil {
		t.Fatalf("expected runtime info to be populated")
	}
	if len(state.pve.runtimeInfo.Nodes) == 0 {
		t.Fatalf("expected runtime nodes to be populated")
	}
}

func TestPBSDatastoreDiscoveryBrickPopulatesDatastores(t *testing.T) {
	collector := newTestCollector(t)
	collector.proxType = "pbs"
	collector.config.PBSDatastorePaths = []string{"/data/store1"}

	state := newCollectionState(collector)
	discoveryBrick := newPBSRecipe().Bricks[2]

	if err := discoveryBrick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs discovery brick failed: %v", err)
	}
	if len(state.pbs.datastores) != 1 {
		t.Fatalf("expected 1 datastore, got %d", len(state.pbs.datastores))
	}
	if state.pbs.datastores[0].Path != "/data/store1" {
		t.Fatalf("datastore path = %q, want %q", state.pbs.datastores[0].Path, "/data/store1")
	}
}
