# Collector Architecture

This document describes the current backup collector design after the refactor to
recipes and fine-grained bricks.

## Goals

The collector is designed around three principles:

- explicit orchestration
- fine-grained collection bricks
- role-aware composition for `pve`, `pbs`, `dual`, and `common/system`

The goal is to avoid hidden macro-flows and make each backup branch easy to
compose, test, and reuse.

## Domain Model

The collector recognizes four environment types:

- `pve`: Proxmox VE only
- `pbs`: Proxmox Backup Server only
- `dual`: host supports both PVE and PBS roles
- `unknown`: only `system/common` collection is considered authoritative

`dual` is a real domain type, not an alias. It is represented in
`internal/types/common.go` and propagated through detection, stats, manifest,
metadata, collector, and restore.

## Authoritative Entrypoints

Top-level collection flows live on the collector and are the only authoritative
entrypoints:

- `CollectAll()`
- `CollectPVEConfigs()`
- `CollectPBSConfigs()`
- `CollectDualConfigs()`
- `CollectSystemInfo()`

Internal legacy wrappers are not part of the architecture contract and should
not be reintroduced as hidden orchestration layers.

## Recipes

The collector runtime is built from explicit recipes in
`internal/backup/collector_bricks.go`.

The important builders are:

- `newPVERecipe()`
- `newPBSRecipe()`
- `newDualRecipe()`
- `newSystemRecipe()`

### Composition Rules

- `newPVERecipe()` = PVE-only bricks
- `newPBSRecipe()` = PBS-only bricks
- `newDualRecipe()` = PVE bricks + PBS bricks
- `newSystemRecipe()` = common/system bricks only

`system/common` is executed once. It is not duplicated inside `dual`.

## Bricks

Each recipe is composed of `collectionBrick` items identified by a stable
`BrickID`.

A brick should be one of:

- a domain brick with clear ownership
- a technical brick with a narrow, explicit purpose

Examples:

- domain bricks:
  - `pve_runtime_core`
  - `pbs_runtime_access_users`
  - `common_storage_stack_lvm`
  - `system_network_runtime_routes`
- technical bricks:
  - `pbs_config_directory_copy`

`pbs_config_directory_copy` is intentionally technical: it preserves pass-through
snapshot behavior for `/etc/proxmox-backup`, including unmodeled files.

## PVE Branch

The PVE branch is split into:

- validation and cluster detection
- config snapshots
- runtime data
- guest config and inventory
- jobs and schedules
- replication
- storage pipeline
- Ceph
- alias/finalize steps

The storage pipeline is explicitly broken into resolve, probe, metadata, backup
analysis, and summary steps.

## PBS Branch

The PBS branch is split into:

- validation
- config snapshot and manifest population
- runtime collection
- datastore discovery/config/namespaces
- PXAR pipeline
- datastore inventory
- final summary

PBS access control, notifications, remotes, jobs, tape, datastore state, and
PXAR metadata are no longer handled by macro-wrappers. They are exposed as
independent recipe bricks.

## Dual Branch

`CollectDualConfigs()` runs `newDualRecipe()` and collects both product roles in
a single backup run.

Important semantics:

- a `dual` backup creates one archive
- metadata persists `BACKUP_TYPE=dual`
- metadata/manifest also persist `BACKUP_TARGETS=pve,pbs`
- `system/common` remains single-pass

`dual` is therefore a composition of PVE + PBS payloads plus one shared
`common/system` payload, not a separate category namespace.

## Common/System Ownership

`storage_stack` now belongs to `common/system`, not PBS.

The common storage stack is split into dedicated bricks such as:

- `common_filesystem_fstab`
- `common_storage_stack_crypttab`
- `common_storage_stack_iscsi`
- `common_storage_stack_multipath`
- `common_storage_stack_mdadm`
- `common_storage_stack_lvm`
- `common_storage_stack_mount_units`
- `common_storage_stack_autofs`
- `common_storage_stack_referenced_files`

PBS inventory still records storage-related diagnostics, but it no longer owns
the copied files in the backup tree.

## State and Context

Recipes share state through `collectionState` and role-specific contexts:

- `pveContext`
- `pbsContext`
- `systemContext`

This allows later bricks to consume facts gathered earlier without re-reading
the environment implicitly.

Examples:

- datastore discovery feeding namespaces and PXAR
- PBS user IDs feeding token aggregation
- inventory state feeding report generation

## Manifest and Metadata

Two layers are important:

- collector manifest (`manifest.json`) written in the temp tree
- backup metadata/sidecars written by the orchestrator/archive flow

Current persisted role fields include:

- `ProxmoxType`
- `ProxmoxTargets`
- `PVEVersion`
- `PBSVersion`

These fields are used later by restore for backup-type detection and category
filtering.

## Restore Relationship

The collector does not introduce a new restore category for `dual`.

Restore still works with category types:

- `PVE`
- `PBS`
- `Common`

`dual` is reconstructed from metadata/manifest/targets and then filtered through
capability overlap:

- `dual` host can restore `PVE + PBS + Common`
- `pve` host can restore `PVE + Common` from a `dual` backup
- `pbs` host can restore `PBS + Common` from a `dual` backup

## Testing Policy

Tests should target:

- top-level real entrypoints for orchestration/integration
- recipes and bricks for feature-level behavior

Tests should not depend on historical wrapper functions that are not part of the
real collector flow.

## Related Files

- `internal/backup/collector.go`
- `internal/backup/collector_bricks.go`
- `internal/backup/collector_dual.go`
- `internal/backup/collector_manifest.go`
- `internal/backup/collector_storage_stack_common.go`
- `internal/environment/detect.go`
- `internal/orchestrator/compatibility.go`
