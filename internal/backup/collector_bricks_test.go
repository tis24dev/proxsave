// Package backup provides collection, archive, and verification logic for ProxSave backups.
package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRunRecipeRunsBricksInOrder(t *testing.T) {
	var ran []BrickID
	state := &collectionState{}
	r := recipe{
		Name: "ordered",
		Bricks: []collectionBrick{
			{
				ID: brickSystemIdentityStatic,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemIdentityStatic)
					return nil
				},
			},
			{
				ID: brickSystemCoreRuntime,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemCoreRuntime)
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

	want := []BrickID{brickSystemIdentityStatic, brickSystemCoreRuntime, brickSystemKernel}
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
				ID: brickSystemIdentityStatic,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemIdentityStatic)
					return nil
				},
			},
			{
				ID: brickSystemCoreRuntime,
				Run: func(context.Context, *collectionState) error {
					ran = append(ran, brickSystemCoreRuntime)
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

	wantRan := []BrickID{brickSystemIdentityStatic, brickSystemCoreRuntime}
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
				ID: brickSystemIdentityStatic,
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

func TestPVEGuestBrickPropagatesQEMUContextCancellation(t *testing.T) {
	cfg := &CollectorConfig{
		BackupVMConfigs: true,
		PVEConfigPath:   filepath.Join(t.TempDir(), "etc", "pve"),
	}
	if err := os.MkdirAll(filepath.Join(cfg.PVEConfigPath, "qemu-server"), 0o755); err != nil {
		t.Fatalf("mkdir qemu-server: %v", err)
	}

	collector := NewCollector(logging.New(types.LogLevelError, false), cfg, t.TempDir(), types.ProxmoxVE, false)
	state := newCollectionState(collector)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	brick := requireBrick(t, recipe{Name: "pve-guest", Bricks: newPVEGuestBricks()}, brickPVEVMQEMUConfigs)
	err := brick.Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("guest brick error = %v, want %v", err, context.Canceled)
	}
	if state.pve.guestCollectionAborted {
		t.Fatalf("guest collection should not be marked aborted for context cancellation")
	}
}

func TestPVEStorageProbeBrickPropagatesContextCancellation(t *testing.T) {
	cfg := &CollectorConfig{BackupPVEBackupFiles: true}
	collector := NewCollector(logging.New(types.LogLevelError, false), cfg, t.TempDir(), types.ProxmoxVE, false)
	state := newCollectionState(collector)
	state.pve.resolvedStorages = []pveStorageEntry{{Name: "local", Path: t.TempDir()}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	brick := requireBrick(t, recipe{Name: "pve-storage-probe", Bricks: newPVEStorageProbeBricks()}, brickPVEStorageProbe)
	err := brick.Run(ctx, state)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("storage probe brick error = %v, want %v", err, context.Canceled)
	}
	if state.pve.storageCollectionAborted {
		t.Fatalf("storage collection should not be marked aborted for context cancellation")
	}
}

func recipeBrickIDs(r recipe) []BrickID {
	ids := make([]BrickID, 0, len(r.Bricks))
	for _, brick := range r.Bricks {
		ids = append(ids, brick.ID)
	}
	return ids
}

func TestRealRecipesHaveCompleteUniqueBricks(t *testing.T) {
	recipes := []recipe{
		newPVERecipe(),
		newPBSRecipe(),
		newPBSCommandsRecipe(),
		newPBSDatastoreInventoryRecipe(),
		newPBSDatastoreConfigRecipe(),
		newPBSPXARRecipe(),
		newPBSUserConfigRecipe(),
		newSystemRecipe(),
		newDualRecipe(),
	}

	for _, r := range recipes {
		t.Run(r.Name, func(t *testing.T) {
			if r.Name == "" {
				t.Fatalf("recipe name is empty")
			}
			if len(r.Bricks) == 0 {
				t.Fatalf("recipe %s has no bricks", r.Name)
			}

			seen := make(map[BrickID]int, len(r.Bricks))
			for i, brick := range r.Bricks {
				if brick.ID == "" {
					t.Fatalf("recipe %s brick %d has empty ID", r.Name, i)
				}
				if brick.Description == "" {
					t.Fatalf("recipe %s brick %s has empty description", r.Name, brick.ID)
				}
				if brick.Run == nil {
					t.Fatalf("recipe %s brick %s has nil Run", r.Name, brick.ID)
				}
				if first, ok := seen[brick.ID]; ok {
					t.Fatalf("recipe %s has duplicate brick ID %s at indexes %d and %d", r.Name, brick.ID, first, i)
				}
				seen[brick.ID] = i
			}
		})
	}
}

func TestNewPVERecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newPVERecipe())
	want := []BrickID{
		brickPVEValidateAndCluster,
		brickPVEConfigSnapshot,
		brickPVEClusterSnapshot,
		brickPVEFirewallSnapshot,
		brickPVEVZDumpSnapshot,
		brickPVERuntimeCore,
		brickPVERuntimeACL,
		brickPVERuntimeCluster,
		brickPVERuntimeStorage,
		brickPVEVMQEMUConfigs,
		brickPVEVMLXCConfigs,
		brickPVEGuestInventory,
		brickPVEBackupJobDefs,
		brickPVEBackupJobHistory,
		brickPVEVZDumpCron,
		brickPVEScheduleCrontab,
		brickPVEScheduleTimers,
		brickPVEScheduleCronFiles,
		brickPVEReplicationDefs,
		brickPVEReplicationStatus,
		brickPVEStorageResolve,
		brickPVEStorageProbe,
		brickPVEStorageMetadataJSON,
		brickPVEStorageMetadataText,
		brickPVEStorageBackupAnalysis,
		brickPVEStorageSummary,
		brickPVECephConfigSnapshot,
		brickPVECephRuntime,
		brickPVEAliasCore,
		brickPVEAggregateBackupHistory,
		brickPVEAggregateReplicationStatus,
		brickPVEVersionInfo,
		brickPVEManifestFinalize,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PVE recipe IDs = %v, want %v", got, want)
	}
}

func TestNewPBSRecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newPBSRecipe())
	want := []BrickID{
		brickPBSValidate,
		brickPBSConfigDirectoryCopy,
		brickPBSManifestInit,
		brickPBSDatastoreDiscovery,
		brickPBSManifestDatastore,
		brickPBSManifestS3,
		brickPBSManifestNode,
		brickPBSManifestACMEAccounts,
		brickPBSManifestACMEPlugins,
		brickPBSManifestMetricServers,
		brickPBSManifestTrafficControl,
		brickPBSManifestNotifications,
		brickPBSManifestNotificationsPriv,
		brickPBSManifestUserCfg,
		brickPBSManifestACLCfg,
		brickPBSManifestDomainsCfg,
		brickPBSManifestRemote,
		brickPBSManifestSyncJobs,
		brickPBSManifestVerificationJobs,
		brickPBSManifestTapeCfg,
		brickPBSManifestTapeJobs,
		brickPBSManifestMediaPools,
		brickPBSManifestTapeEncryptionKeys,
		brickPBSManifestNetwork,
		brickPBSManifestPrune,
		brickPBSRuntimeCore,
		brickPBSRuntimeNode,
		brickPBSRuntimeDatastoreList,
		brickPBSRuntimeDatastoreStatus,
		brickPBSRuntimeACMEAccountsList,
		brickPBSRuntimeACMEAccountInfo,
		brickPBSRuntimeACMEPluginsList,
		brickPBSRuntimeACMEPluginConfig,
		brickPBSRuntimeNotificationTargets,
		brickPBSRuntimeNotificationMatchers,
		brickPBSRuntimeNotificationEndpointSMTP,
		brickPBSRuntimeNotificationEndpointSendmail,
		brickPBSRuntimeNotificationEndpointGotify,
		brickPBSRuntimeNotificationEndpointWebhook,
		brickPBSRuntimeNotificationSummary,
		brickPBSRuntimeAccessUsers,
		brickPBSRuntimeAccessRealmsLDAP,
		brickPBSRuntimeAccessRealmsAD,
		brickPBSRuntimeAccessRealmsOpenID,
		brickPBSRuntimeAccessACL,
		brickPBSRuntimeAccessUserTokens,
		brickPBSRuntimeAccessTokensAggregate,
		brickPBSRuntimeRemotes,
		brickPBSRuntimeSyncJobs,
		brickPBSRuntimeVerificationJobs,
		brickPBSRuntimePruneJobs,
		brickPBSRuntimeGCJobs,
		brickPBSRuntimeTapeDetect,
		brickPBSRuntimeTapeDrives,
		brickPBSRuntimeTapeChangers,
		brickPBSRuntimeTapePools,
		brickPBSRuntimeNetwork,
		brickPBSRuntimeDisks,
		brickPBSRuntimeCertInfo,
		brickPBSRuntimeTrafficControl,
		brickPBSRuntimeRecentTasks,
		brickPBSRuntimeS3Endpoints,
		brickPBSRuntimeS3EndpointBuckets,
		brickPBSInventoryInit,
		brickPBSInventoryMountFiles,
		brickPBSInventoryOSFiles,
		brickPBSInventoryMultipathFiles,
		brickPBSInventoryISCSIFiles,
		brickPBSInventoryAutofsFiles,
		brickPBSInventoryZFSFiles,
		brickPBSInventoryLVMDirs,
		brickPBSInventorySystemdMountUnits,
		brickPBSInventoryReferencedFiles,
		brickPBSInventoryHostCommandsCore,
		brickPBSInventoryHostCommandsDMSetup,
		brickPBSInventoryHostCommandsLVM,
		brickPBSInventoryHostCommandsMDADM,
		brickPBSInventoryHostCommandsMultipath,
		brickPBSInventoryHostCommandsISCSI,
		brickPBSInventoryHostCommandsZFS,
		brickPBSInventoryCommandFiles,
		brickPBSInventoryDatastores,
		brickPBSInventoryWrite,
		brickPBSDatastoreCLIConfigs,
		brickPBSDatastoreNamespaces,
		brickPBSPXARPrepare,
		brickPBSPXARMetadata,
		brickPBSPXARSubdirReports,
		brickPBSPXARVMLists,
		brickPBSPXARCTLists,
		brickPBSFinalizeSummary,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PBS recipe IDs = %v, want %v", got, want)
	}
}

func TestNewSystemRecipeOrder(t *testing.T) {
	got := recipeBrickIDs(newSystemRecipe())
	want := []BrickID{
		brickSystemNetworkStatic,
		brickSystemIdentityStatic,
		brickSystemAptStatic,
		brickSystemCronStatic,
		brickSystemServicesStatic,
		brickSystemLoggingStatic,
		brickSystemSSLStatic,
		brickSystemSysctlStatic,
		brickSystemKernelModulesStatic,
		brickCommonFilesystemFstab,
		brickCommonStorageStackCrypttab,
		brickCommonStorageStackISCSISnapshot,
		brickCommonStorageStackMultipathSnapshot,
		brickCommonStorageStackMDADMSnapshot,
		brickCommonStorageStackLVMSnapshot,
		brickCommonStorageStackMountUnitsSnapshot,
		brickCommonStorageStackAutofsSnapshot,
		brickCommonStorageStackReferencedFiles,
		brickSystemZFSStatic,
		brickSystemFirewallStatic,
		brickSystemRuntimeLeases,
		brickSystemCoreRuntime,
		brickSystemNetworkRuntimeAddr,
		brickSystemNetworkRuntimeRules,
		brickSystemNetworkRuntimeRoutes,
		brickSystemNetworkRuntimeLinks,
		brickSystemNetworkRuntimeNeighbors,
		brickSystemNetworkRuntimeBridges,
		brickSystemNetworkRuntimeInventory,
		brickSystemNetworkRuntimeBonding,
		brickSystemNetworkRuntimeDNS,
		brickSystemStorageRuntimeMounts,
		brickSystemStorageRuntimeBlock,
		brickSystemComputeRuntimeMemoryCPU,
		brickSystemComputeRuntimeBusInv,
		brickSystemServicesRuntime,
		brickSystemPackagesRuntimeInstalled,
		brickSystemPackagesRuntimeAPTPolicy,
		brickSystemFirewallRuntimeIPTables,
		brickSystemFirewallRuntimeIP6Tables,
		brickSystemFirewallRuntimeNFTables,
		brickSystemFirewallRuntimeUFW,
		brickSystemFirewallRuntimeFirewalld,
		brickSystemKernelModulesRuntime,
		brickSystemSysctlRuntime,
		brickSystemZFSRuntime,
		brickSystemLVMRuntime,
		brickSystemNetworkReport,
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
	commandsBrick := requireBrick(t, newPVERecipe(), brickPVERuntimeCore)

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

func TestPVEStorageResolveBrickPopulatesResolvedStorages(t *testing.T) {
	collector := newTestCollector(t)
	collector.proxType = "pve"

	state := newCollectionState(collector)
	state.ensurePVERuntimeInfo().Storages = []pveStorageEntry{{Name: "local", Path: "/data/local", Type: "dir"}}

	resolveBrick := requireBrick(t, newPVERecipe(), brickPVEStorageResolve)
	if err := resolveBrick.Run(context.Background(), state); err != nil {
		t.Fatalf("pve storage resolve brick failed: %v", err)
	}
	found := false
	for _, storage := range state.pve.resolvedStorages {
		if storage.Name == "local" && storage.Path == "/data/local" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected resolved storages to contain runtime storage local=/data/local, got %#v", state.pve.resolvedStorages)
	}
}

func TestPVEStorageProbeBrickStoresScanResults(t *testing.T) {
	collector := newTestCollector(t)
	collector.proxType = "pve"

	storageDir := t.TempDir()
	state := newCollectionState(collector)
	state.pve.resolvedStorages = []pveStorageEntry{{Name: "local", Path: storageDir, Type: "dir"}}

	probeBrick := requireBrick(t, newPVERecipe(), brickPVEStorageProbe)
	if err := probeBrick.Run(context.Background(), state); err != nil {
		t.Fatalf("pve storage probe brick failed: %v", err)
	}
	if len(state.pve.probedStorages) != 1 {
		t.Fatalf("expected 1 probed storage, got %d", len(state.pve.probedStorages))
	}
	result := state.pve.storageResult(state.pve.probedStorages[0])
	if result == nil {
		t.Fatalf("expected scan result to be stored")
	}
	if result.MetaDir == "" {
		t.Fatalf("expected metadata directory to be set")
	}
}

func TestPBSDatastoreDiscoveryBrickPopulatesDatastores(t *testing.T) {
	collector := newTestCollector(t)
	collector.proxType = "pbs"
	collector.config.PBSDatastorePaths = []string{"/data/store1"}

	state := newCollectionState(collector)
	discoveryBrick := requireBrick(t, newPBSRecipe(), brickPBSDatastoreDiscovery)

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

func TestPBSAccessUsersBrickPopulatesUserIDs(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "proxmox-backup-manager" {
				return []byte{}, nil
			}
			if len(args) >= 3 && args[0] == "user" && args[1] == "list" {
				return []byte(`[{"userid":"user@pam"},{"userid":"second@pbs"}]`), nil
			}
			return []byte(`[]`), nil
		},
	})
	collector.proxType = "pbs"

	state := newCollectionState(collector)
	brick := requireBrick(t, newPBSRecipe(), brickPBSRuntimeAccessUsers)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs access users brick failed: %v", err)
	}
	if !reflect.DeepEqual(state.pbs.userIDs, []string{"second@pbs", "user@pam"}) {
		t.Fatalf("user IDs = %v", state.pbs.userIDs)
	}
}

func TestPBSAcmeAccountsBrickPopulatesAccountNames(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "proxmox-backup-manager" {
				return []byte{}, nil
			}
			if len(args) >= 4 && args[0] == "acme" && args[1] == "account" && args[2] == "list" {
				return []byte(`[{"name":"acc-2"},{"name":"acc-1"}]`), nil
			}
			return []byte(`[]`), nil
		},
	})
	collector.proxType = "pbs"

	state := newCollectionState(collector)
	brick := requireBrick(t, newPBSRecipe(), brickPBSRuntimeACMEAccountsList)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs acme accounts brick failed: %v", err)
	}
	if !reflect.DeepEqual(state.pbs.acmeAccountNames, []string{"acc-1", "acc-2"}) {
		t.Fatalf("acme account names = %v", state.pbs.acmeAccountNames)
	}
}

func TestPBSS3EndpointsBrickPopulatesEndpointIDs(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name != "proxmox-backup-manager" {
				return []byte{}, nil
			}
			if len(args) >= 4 && args[0] == "s3" && args[1] == "endpoint" && args[2] == "list" {
				return []byte(`[{"id":"b"},{"id":"a"}]`), nil
			}
			return []byte(`[]`), nil
		},
	})
	collector.proxType = "pbs"

	state := newCollectionState(collector)
	brick := requireBrick(t, newPBSRecipe(), brickPBSRuntimeS3Endpoints)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs s3 endpoints brick failed: %v", err)
	}
	if !reflect.DeepEqual(state.pbs.s3EndpointIDs, []string{"a", "b"}) {
		t.Fatalf("s3 endpoint IDs = %v", state.pbs.s3EndpointIDs)
	}
}

func TestPBSTapeDetectBrickStoresSupportState(t *testing.T) {
	pbsRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(pbsRoot, "tape.cfg"), []byte("ok"), 0o640); err != nil {
		t.Fatalf("write tape.cfg: %v", err)
	}

	collector := newTestCollector(t)
	collector.proxType = "pbs"
	collector.config.PBSConfigPath = pbsRoot

	state := newCollectionState(collector)
	brick := requireBrick(t, newPBSRecipe(), brickPBSRuntimeTapeDetect)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs tape detect brick failed: %v", err)
	}
	if !state.pbs.tapeSupportKnown || !state.pbs.tapeSupported {
		t.Fatalf("expected tape support to be detected, got known=%v supported=%v", state.pbs.tapeSupportKnown, state.pbs.tapeSupported)
	}
}

func TestPBSInventoryInitBrickBuildsInventoryState(t *testing.T) {
	root := t.TempDir()
	pbsRoot := filepath.Join(root, "etc", "proxmox-backup")
	if err := os.MkdirAll(pbsRoot, 0o755); err != nil {
		t.Fatalf("mkdir pbs root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatalf("mkdir etc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pbsRoot, "datastore.cfg"), []byte("datastore: store1\npath /data/store1\n"), 0o640); err != nil {
		t.Fatalf("write datastore.cfg: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "fstab"), []byte("/dev/sda1 / ext4 defaults 0 1\n"), 0o644); err != nil {
		t.Fatalf("write fstab: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "crypttab"), []byte("crypt1 UUID=abcd /etc/keys/disk.key luks\n"), 0o644); err != nil {
		t.Fatalf("write crypttab: %v", err)
	}

	collector := newTestCollector(t)
	collector.proxType = "pbs"
	collector.config.SystemRootPrefix = root

	state := newCollectionState(collector)
	state.pbs.datastores = []pbsDatastore{{Name: "store1", Path: "/data/store1"}}
	brick := requireBrick(t, newPBSRecipe(), brickPBSInventoryInit)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs inventory init brick failed: %v", err)
	}
	if state.pbs.inventory == nil {
		t.Fatalf("expected inventory state to be initialized")
	}
	if len(state.pbs.inventory.mergedDatastores) == 0 {
		t.Fatalf("expected merged datastores to be populated")
	}
	if _, ok := state.pbs.inventory.report.Files["pbs_datastore_cfg"]; !ok {
		t.Fatalf("expected datastore cfg snapshot in inventory state")
	}
	if !reflect.DeepEqual(state.pbs.inventory.referencedFiles, []string{"/etc/keys/disk.key"}) {
		t.Fatalf("referenced files = %v", state.pbs.inventory.referencedFiles)
	}
}

func TestPBSDatastoreCLIConfigsBrickPreparesConfigState(t *testing.T) {
	collector := newTestCollectorWithDeps(t, CollectorDeps{
		LookPath: func(cmd string) (string, error) {
			return "/usr/bin/" + cmd, nil
		},
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte(`{}`), nil
		},
	})
	collector.proxType = "pbs"

	state := newCollectionState(collector)
	state.pbs.datastores = []pbsDatastore{{Name: "store1", Path: "/data/store1", CLIName: "store1"}}

	brick := requireBrick(t, newPBSRecipe(), brickPBSDatastoreCLIConfigs)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs datastore cli configs brick failed: %v", err)
	}
	if state.pbs.datastoreConfig == nil {
		t.Fatalf("expected datastore config state to be initialized")
	}
	if len(state.pbs.datastoreConfig.datastores) != 1 {
		t.Fatalf("expected 1 datastore in config state, got %d", len(state.pbs.datastoreConfig.datastores))
	}
	if state.pbs.datastoreConfig.datastoreDir == "" {
		t.Fatalf("expected datastore config output dir to be set")
	}
}

func TestPBSPXARPrepareBrickBuildsPxarState(t *testing.T) {
	collector := newTestCollector(t)
	collector.proxType = "pbs"

	dsPath := t.TempDir()
	state := newCollectionState(collector)
	state.pbs.datastores = []pbsDatastore{{Name: "store1", Path: dsPath}}

	brick := requireBrick(t, newPBSRecipe(), brickPBSPXARPrepare)
	if err := brick.Run(context.Background(), state); err != nil {
		t.Fatalf("pbs pxar prepare brick failed: %v", err)
	}
	if state.pbs.pxar == nil {
		t.Fatalf("expected pxar state to be initialized")
	}
	if len(state.pbs.pxar.eligible) != 1 {
		t.Fatalf("expected 1 eligible datastore, got %d", len(state.pbs.pxar.eligible))
	}
	if state.pbs.pxar.metaRoot == "" || state.pbs.pxar.selectedRoot == "" || state.pbs.pxar.smallRoot == "" {
		t.Fatalf("expected PXAR output roots to be initialized, got %+v", state.pbs.pxar)
	}
}

func TestPBSManifestAccessAndTapeBricksPopulateEntriesIndividually(t *testing.T) {
	pbsRoot := t.TempDir()
	for name := range map[string]string{
		"user.cfg":                  "users",
		"acl.cfg":                   "acl",
		"domains.cfg":               "domains",
		"tape.cfg":                  "tape",
		"tape-job.cfg":              "jobs",
		"media-pool.cfg":            "pool",
		"tape-encryption-keys.json": "{}",
	} {
		if err := os.WriteFile(filepath.Join(pbsRoot, name), []byte(name), 0o640); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	collector := newTestCollector(t)
	collector.proxType = "pbs"
	collector.config.PBSConfigPath = pbsRoot

	state := newCollectionState(collector)
	collector.initPBSManifest()

	for _, id := range []BrickID{
		brickPBSManifestUserCfg,
		brickPBSManifestACLCfg,
		brickPBSManifestDomainsCfg,
		brickPBSManifestTapeCfg,
		brickPBSManifestTapeJobs,
		brickPBSManifestMediaPools,
		brickPBSManifestTapeEncryptionKeys,
	} {
		brick := requireBrick(t, newPBSRecipe(), id)
		if err := brick.Run(context.Background(), state); err != nil {
			t.Fatalf("manifest brick %s failed: %v", id, err)
		}
	}

	for _, key := range []string{
		"user.cfg",
		"acl.cfg",
		"domains.cfg",
		"tape.cfg",
		"tape-job.cfg",
		"media-pool.cfg",
		"tape-encryption-keys.json",
	} {
		entry, ok := collector.pbsManifest[key]
		if !ok {
			t.Fatalf("expected manifest entry for %s", key)
		}
		if entry.Status != StatusCollected {
			t.Fatalf("manifest entry %s status = %s, want %s", key, entry.Status, StatusCollected)
		}
	}
}

func TestSystemNetworkRuntimeBricksProduceOwnFileFamilies(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "sys", "class", "net", "eth0"),
		filepath.Join(root, "etc"),
		filepath.Join(root, "proc", "net", "bonding"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for rel, content := range map[string]string{
		"sys/class/net/eth0/address":   "00:11:22:33:44:55\n",
		"sys/class/net/eth0/ifindex":   "2\n",
		"sys/class/net/eth0/operstate": "up\n",
		"sys/class/net/eth0/speed":     "1000\n",
		"etc/resolv.conf":              "nameserver 1.1.1.1\n",
		"proc/net/bonding/bond0":       "Bonding Mode: active-backup\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	cases := []struct {
		id    BrickID
		files []string
	}{
		{id: brickSystemNetworkRuntimeAddr, files: []string{"ip_addr.json", "ip_addr.txt"}},
		{id: brickSystemNetworkRuntimeRules, files: []string{"ip_rule.json", "ip_rule.txt"}},
		{id: brickSystemNetworkRuntimeRoutes, files: []string{"ip_route.json", "ip_route.txt", "ip_route_all_v4.txt", "ip_route_all_v6.txt"}},
		{id: brickSystemNetworkRuntimeLinks, files: []string{"ip_link.json", "ip_link.txt"}},
		{id: brickSystemNetworkRuntimeNeighbors, files: []string{"ip6_neigh.txt", "ip_neigh.txt"}},
		{id: brickSystemNetworkRuntimeBridges, files: []string{"bridge_fdb.txt", "bridge_link.txt", "bridge_mdb.txt", "bridge_vlan.txt"}},
		{id: brickSystemNetworkRuntimeInventory, files: []string{"network_inventory.json"}},
		{id: brickSystemNetworkRuntimeBonding, files: []string{"bonding_bond0.txt"}},
		{id: brickSystemNetworkRuntimeDNS, files: []string{"resolv_conf.txt"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})
			collector.config.SystemRootPrefix = root

			state := newCollectionState(collector)
			brick := requireBrick(t, newSystemRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed: %v", tc.id, err)
			}

			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				t.Fatalf("ensureSystemCommandsDir: %v", err)
			}
			got := listDirEntries(t, commandsDir)
			if !reflect.DeepEqual(got, tc.files) {
				t.Fatalf("files for %s = %v, want %v", tc.id, got, tc.files)
			}
		})
	}
}

func TestSystemFirewallRuntimeBricksAreIndependentAndGated(t *testing.T) {
	cases := []struct {
		id    BrickID
		files []string
	}{
		{id: brickSystemFirewallRuntimeIPTables, files: []string{"iptables.txt", "iptables_nat.txt"}},
		{id: brickSystemFirewallRuntimeIP6Tables, files: []string{"ip6tables.txt", "ip6tables_nat.txt"}},
		{id: brickSystemFirewallRuntimeNFTables, files: []string{"nftables.txt"}},
		{id: brickSystemFirewallRuntimeUFW, files: []string{"systemctl_ufw.txt", "ufw_status.txt"}},
		{id: brickSystemFirewallRuntimeFirewalld, files: []string{"firewalld_list_all.txt", "firewalld_state.txt", "systemctl_firewalld.txt"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.id)+"_enabled", func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})
			collector.config.BackupFirewallRules = true

			state := newCollectionState(collector)
			brick := requireBrick(t, newSystemRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed: %v", tc.id, err)
			}

			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				t.Fatalf("ensureSystemCommandsDir: %v", err)
			}
			got := listDirEntries(t, commandsDir)
			if !reflect.DeepEqual(got, tc.files) {
				t.Fatalf("files for %s = %v, want %v", tc.id, got, tc.files)
			}
		})

		t.Run(string(tc.id)+"_disabled", func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})
			collector.config.BackupFirewallRules = false

			state := newCollectionState(collector)
			brick := requireBrick(t, newSystemRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed with firewall disabled: %v", tc.id, err)
			}

			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				t.Fatalf("ensureSystemCommandsDir: %v", err)
			}
			got := listDirEntries(t, commandsDir)
			if len(got) != 0 {
				t.Fatalf("expected no files for %s with firewall disabled, got %v", tc.id, got)
			}
		})
	}
}

func TestSystemStorageRuntimeBricksProduceOwnFileFamilies(t *testing.T) {
	cases := []struct {
		id    BrickID
		files []string
	}{
		{id: brickSystemStorageRuntimeMounts, files: []string{"df.txt", "mount.txt"}},
		{id: brickSystemStorageRuntimeBlock, files: []string{"blkid.txt", "lsblk.txt", "lsblk_json.json"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})

			state := newCollectionState(collector)
			brick := requireBrick(t, newSystemRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed: %v", tc.id, err)
			}

			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				t.Fatalf("ensureSystemCommandsDir: %v", err)
			}
			got := listDirEntries(t, commandsDir)
			if !reflect.DeepEqual(got, tc.files) {
				t.Fatalf("files for %s = %v, want %v", tc.id, got, tc.files)
			}
		})
	}
}

func TestSystemComputeRuntimeBricksProduceOwnFileFamilies(t *testing.T) {
	cases := []struct {
		id    BrickID
		files []string
	}{
		{id: brickSystemComputeRuntimeMemoryCPU, files: []string{"free.txt", "lscpu.txt"}},
		{id: brickSystemComputeRuntimeBusInv, files: []string{"lspci.txt", "lsusb.txt"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})

			state := newCollectionState(collector)
			brick := requireBrick(t, newSystemRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed: %v", tc.id, err)
			}

			commandsDir, err := state.ensureSystemCommandsDir()
			if err != nil {
				t.Fatalf("ensureSystemCommandsDir: %v", err)
			}
			got := listDirEntries(t, commandsDir)
			if !reflect.DeepEqual(got, tc.files) {
				t.Fatalf("files for %s = %v, want %v", tc.id, got, tc.files)
			}
		})
	}
}

func TestSystemPackagesRuntimeBricksAreIndependentAndGated(t *testing.T) {
	t.Run(string(brickSystemPackagesRuntimeInstalled)+"_enabled", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return testCollectorCommandOutput(name, args...), nil
			},
		})
		collector.config.BackupInstalledPackages = true
		collector.config.BackupAptSources = false

		state := newCollectionState(collector)
		brick := requireBrick(t, newSystemRecipe(), brickSystemPackagesRuntimeInstalled)
		if err := brick.Run(context.Background(), state); err != nil {
			t.Fatalf("brick %s failed: %v", brickSystemPackagesRuntimeInstalled, err)
		}

		commandsDir, err := state.ensureSystemCommandsDir()
		if err != nil {
			t.Fatalf("ensureSystemCommandsDir: %v", err)
		}
		got := listRelativeFiles(t, commandsDir)
		want := []string{"packages/dpkg_list.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("files for %s = %v, want %v", brickSystemPackagesRuntimeInstalled, got, want)
		}
	})

	t.Run(string(brickSystemPackagesRuntimeInstalled)+"_disabled", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return testCollectorCommandOutput(name, args...), nil
			},
		})
		collector.config.BackupInstalledPackages = false

		state := newCollectionState(collector)
		brick := requireBrick(t, newSystemRecipe(), brickSystemPackagesRuntimeInstalled)
		if err := brick.Run(context.Background(), state); err != nil {
			t.Fatalf("brick %s failed with packages disabled: %v", brickSystemPackagesRuntimeInstalled, err)
		}

		commandsDir, err := state.ensureSystemCommandsDir()
		if err != nil {
			t.Fatalf("ensureSystemCommandsDir: %v", err)
		}
		got := listRelativeFiles(t, commandsDir)
		if len(got) != 0 {
			t.Fatalf("expected no files for %s with packages disabled, got %v", brickSystemPackagesRuntimeInstalled, got)
		}
	})

	t.Run(string(brickSystemPackagesRuntimeAPTPolicy)+"_enabled", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return testCollectorCommandOutput(name, args...), nil
			},
		})
		collector.config.BackupInstalledPackages = false
		collector.config.BackupAptSources = true

		state := newCollectionState(collector)
		brick := requireBrick(t, newSystemRecipe(), brickSystemPackagesRuntimeAPTPolicy)
		if err := brick.Run(context.Background(), state); err != nil {
			t.Fatalf("brick %s failed: %v", brickSystemPackagesRuntimeAPTPolicy, err)
		}

		commandsDir, err := state.ensureSystemCommandsDir()
		if err != nil {
			t.Fatalf("ensureSystemCommandsDir: %v", err)
		}
		got := listRelativeFiles(t, commandsDir)
		want := []string{"apt_policy.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("files for %s = %v, want %v", brickSystemPackagesRuntimeAPTPolicy, got, want)
		}
	})

	t.Run(string(brickSystemPackagesRuntimeAPTPolicy)+"_disabled", func(t *testing.T) {
		collector := newTestCollectorWithDeps(t, CollectorDeps{
			LookPath: func(cmd string) (string, error) {
				return "/usr/bin/" + cmd, nil
			},
			RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				return testCollectorCommandOutput(name, args...), nil
			},
		})
		collector.config.BackupAptSources = false

		state := newCollectionState(collector)
		brick := requireBrick(t, newSystemRecipe(), brickSystemPackagesRuntimeAPTPolicy)
		if err := brick.Run(context.Background(), state); err != nil {
			t.Fatalf("brick %s failed with apt disabled: %v", brickSystemPackagesRuntimeAPTPolicy, err)
		}

		commandsDir, err := state.ensureSystemCommandsDir()
		if err != nil {
			t.Fatalf("ensureSystemCommandsDir: %v", err)
		}
		got := listRelativeFiles(t, commandsDir)
		if len(got) != 0 {
			t.Fatalf("expected no files for %s with apt disabled, got %v", brickSystemPackagesRuntimeAPTPolicy, got)
		}
	})
}

func TestPBSInventoryHostCommandStorageBricksPopulateOwnKeys(t *testing.T) {
	cases := []struct {
		id   BrickID
		keys []string
	}{
		{id: brickPBSInventoryHostCommandsDMSetup, keys: []string{"dmsetup_tree"}},
		{id: brickPBSInventoryHostCommandsLVM, keys: []string{"lvs_json", "pvs_json", "vgs_json"}},
		{id: brickPBSInventoryHostCommandsMDADM, keys: []string{"mdadm_scan", "proc_mdstat"}},
		{id: brickPBSInventoryHostCommandsMultipath, keys: []string{"multipath_ll"}},
		{id: brickPBSInventoryHostCommandsISCSI, keys: []string{"iscsi_ifaces", "iscsi_nodes", "iscsi_sessions"}},
	}

	for _, tc := range cases {
		t.Run(string(tc.id), func(t *testing.T) {
			collector := newTestCollectorWithDeps(t, CollectorDeps{
				LookPath: func(cmd string) (string, error) {
					return "/usr/bin/" + cmd, nil
				},
				RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
					return testCollectorCommandOutput(name, args...), nil
				},
			})
			collector.proxType = "pbs"

			state := newCollectionState(collector)
			state.pbs.inventory = &pbsInventoryState{
				report: pbsDatastoreInventoryReport{
					Commands: make(map[string]inventoryCommandSnapshot),
					Files:    make(map[string]inventoryFileSnapshot),
					Dirs:     make(map[string]inventoryDirSnapshot),
				},
				hostCommandsEnabled: true,
			}

			brick := requireBrick(t, newPBSRecipe(), tc.id)
			if err := brick.Run(context.Background(), state); err != nil {
				t.Fatalf("brick %s failed: %v", tc.id, err)
			}

			got := sortedInventoryCommandKeys(state.pbs.inventory.report.Commands)
			if !reflect.DeepEqual(got, tc.keys) {
				t.Fatalf("command keys for %s = %v, want %v", tc.id, got, tc.keys)
			}
		})
	}
}

func listDirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func listRelativeFiles(t *testing.T, root string) []string {
	t.Helper()

	var files []string
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func sortedInventoryCommandKeys(commands map[string]inventoryCommandSnapshot) []string {
	keys := make([]string, 0, len(commands))
	for key := range commands {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func testCollectorCommandOutput(name string, args ...string) []byte {
	if name == "cat" && len(args) == 1 {
		if data, err := os.ReadFile(args[0]); err == nil {
			return data
		}
	}
	return []byte(strings.TrimSpace(name+" "+strings.Join(args, " ")) + "\n")
}

func requireBrick(t *testing.T, r recipe, id BrickID) collectionBrick {
	t.Helper()
	for _, brick := range r.Bricks {
		if brick.ID == id {
			return brick
		}
	}
	t.Fatalf("brick %s not found in recipe %s", id, r.Name)
	return collectionBrick{}
}
