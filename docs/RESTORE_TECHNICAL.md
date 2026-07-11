# Proxsave - Restore Technical Documentation

Technical architecture and implementation details for the restore system.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Module Structure](#module-structure)
- [Execution Flow](#execution-flow)
- [Category System](#category-system)
- [Service Management](#service-management)
- [Extraction Engine](#extraction-engine)
- [Safety Mechanisms](#safety-mechanisms)
- [Error Handling](#error-handling)
- [Extension Guide](#extension-guide)

---

## Architecture Overview

This document is the implementation-oriented companion to
[RESTORE_GUIDE.md](RESTORE_GUIDE.md). Use the guide for operator behavior and
examples; use this file for internal restore logic, module responsibilities,
and decision flow details.

### Design Principles

1. **Safety First**: Multiple layers of protection against data loss
2. **Interactive Control**: User confirmation at critical points
3. **Fail-Fast**: Stop immediately on critical errors
4. **Auditability**: Comprehensive logging of all operations
5. **Modularity**: Clean separation of concerns

### High-Level Architecture

```
┌─────────────────────────────────────────────────────────┐
│                    CLI Entry Point                       │
│              cmd/proxsave/main.go                  │
└────────────────────┬────────────────────────────────────┘
                     │
                     ↓
┌─────────────────────────────────────────────────────────┐
│              Restore Orchestrator                        │
│        internal/orchestrator/restore.go                  │
│  ┌──────────────────────────────────────────────────┐   │
│  │  RunRestoreWorkflow()                            │   │
│  │   - Coordinate all phases                        │   │
│  │   - Manage service lifecycle                     │   │
│  │   - Error handling and cleanup                   │   │
│  └──────────────────────────────────────────────────┘   │
└─────┬──────────┬──────────┬──────────┬─────────────────┘
      │          │          │          │
      ↓          ↓          ↓          ↓
┌──────────┐ ┌────────┐ ┌────────┐ ┌──────────────┐
│ Decrypt  │ │Category│ │Extract │ │   Safety     │
│  Module  │ │ System │ │ Engine │ │   Backup     │
└──────────┘ └────────┘ └────────┘ └──────────────┘
│            │          │          │
│ decrypt.go │categories│restore_  │backup_safety │
│            │     .go  │archive*  │      .go     │
└────────────┴──────────┴──────────┴──────────────┘
```

### Data Flow

```
User Input (--restore flag)
  ↓
Backup Selection
  ├─ Scan configured paths
  ├─ Parse manifests
  └─ User selection
  ↓
Decryption (if encrypted)
  ├─ AGE key/passphrase
  ├─ Decrypt to /tmp
  └─ Verify checksum
  ↓
Compatibility Check
  ├─ Detect system type
  ├─ Read backup type
  └─ Validate compatibility
  ↓
Category Analysis
  ├─ Open archive
  ├─ Scan TAR entries
  └─ Mark available categories
  ↓
Mode & Category Selection
  ├─ Display modes
  ├─ User selection
  └─ Build category list
  ↓
Restore Plan & Confirmation
  ├─ Display plan
  ├─ Show warnings
  └─ User confirmation
  ↓
Safety Backup
  ├─ Backup existing files
  └─ Store in /tmp
  ↓
Service Management (if cluster)
  ├─ Stop PVE services
  └─ Unmount /etc/pve
  ↓
File Extraction (three tiers)
  ├─ Normal categories → /
  ├─ Export categories → export dir (read-only)
  ├─ Staged categories → stage dir, then applied
  └─ Log all operations
  ↓
Post-Restore Tasks
  ├─ Recreate directories
  ├─ Check ZFS pools (when the ZFS category is selected)
  └─ Restart services (deferred)
  ↓
Completion Summary
```

---

## Module Structure

### Core Files

| File | Purpose | Key Functions |
|------|---------|---------------|
| `cmd/proxsave/main.go` | Entry point, CLI parsing | `main()`, flag handling |
| `internal/orchestrator/restore.go` | Entry stub (body in `restore_workflow_ui_run.go`) | `RunRestoreWorkflow()` |
| `internal/orchestrator/categories.go` | Category definitions | `GetAllCategories()`, `PathMatchesCategory()` |
| `internal/orchestrator/selective.go` | Category selection/plan UI | `ShowRestoreModeMenuWithReader()`, `ShowRestorePlan()` |
| `internal/orchestrator/decrypt.go` | Decryption workflow | `prepareDecryptedBackup()` |
| `internal/orchestrator/compatibility.go` | System validation | `ValidateCompatibility()` |
| `internal/orchestrator/backup_safety.go` | Safety backups | `CreateSafetyBackup()` |
| `internal/orchestrator/directory_recreation.go` | Storage setup | `RecreateDirectoriesFromConfig()` |

### File: cmd/proxsave/main_restore_decrypt.go

**`runRestoreCLI()` / `runRestoreTUI()`**: Entry point for the restore flag

```go
if args.Restore {
    logging.Info("Restore mode enabled - starting interactive workflow...")
    if err := orchestrator.RunRestoreWorkflow(ctx, cfg, logger, version); err != nil {
        if errors.Is(err, orchestrator.ErrRestoreAborted) ||
           errors.Is(err, orchestrator.ErrDecryptAborted) {
            logging.Info("Restore workflow aborted by user")
            return finalize(exitCodeInterrupted)
        }
        logging.Error("Restore workflow failed: %v", err)
        return finalize(types.ExitGenericError.Int())
    }
    logging.Info("Restore workflow completed successfully")
    return finalize(types.ExitSuccess.Int())
}
```

**Responsibilities**:
- Parse `--restore` flag
- Call orchestrator
- Handle errors and exit codes
- Distinguish user abort vs system error

### File: internal/orchestrator/restore.go (entry point; workflow body in restore_workflow_ui_run.go)

**Main function**: `RunRestoreWorkflow()`, a thin dispatch stub that delegates to `runRestoreWorkflowWithUI()` (`restore_workflow_ui_run.go`)

**Signature**:
```go
func RunRestoreWorkflow(
    ctx context.Context,
    cfg *config.Config,
    logger *logging.Logger,
    version string,
) error
```

**Key Sections** (the workflow body lives in `restore_workflow_ui_run.go`, driven by
`run()` → `runSelectiveRestore()`; each step below names the implementing function(s)
instead of line numbers, which drift on every edit):

1. **Preparation** (`prepareBundleAndPlan()` → `prepareBundle()`, `detectTargetSystem()` /
   `DetectCurrentSystem()`, `analyzeArchive()` / `AnalyzeRestoreArchive()`,
   `confirmCompatibility()` / `ValidateCompatibility()`):
   - Decrypt backup if needed
   - Detect system type
   - Validate compatibility
   - Analyze categories

2. **Mode & Category Selection + Plan build** (`selectRestorePlan()` →
   `selectModeAndCategories()` / `GetCategoriesForMode()`, then `PlanRestore()` in
   `restore_plan.go` which splits the selection via `splitRestoreCategories()` in `staging.go`):
   - User selects restore mode (Full/Storage/Base/Custom)
   - Interactive category selection for Custom mode
   - Build the plan, splitting categories into normal / staged / export-only

3. **PBS Behavior + Cluster SAFE/RECOVERY Prompt** (`configurePlanForRuntime()` →
   `selectPBSRestoreBehavior()` and `selectClusterRestoreMode()` → `applyClusterRestoreChoice()`
   in `restore_workflow_ui_plan.go`; SAFE redirect via `RestorePlan.ApplyClusterSafeMode()` →
   `redirectClusterCategoryToExport()` in `restore_archive.go`):
   - Detect cluster payload in backup (`plan.ClusterBackup && plan.NeedsClusterRestore`)
   - Prompt user: SAFE (export+API) vs RECOVERY (full restore)
   - SAFE mode redirects pve_cluster to export-only

4. **Plan confirmation** (`confirmRestorePlan()` in `restore_workflow_ui_plan.go`, called from
   `runSelectiveRestore()` before any writes):
   - Show the final restore plan
   - User confirms (or aborts) before any data is written

5. **Safety Backup** (`createRollbackBackups()` in `restore_workflow_ui_backups_services.go`
   → `CreateSafetyBackup()` in `backup_safety.go`):
   - Backup files to be overwritten
   - Handle backup failures

6. **PVE Service Management** (`prepareRestoreServices()` → `preparePVEClusterRestore()`
   in `restore_workflow_ui_backups_services.go` → `stopPVEClusterServices()` /
   `unmountEtcPVE()` / `startPVEClusterServices()` in `restore_services.go`):
   - Detect cluster restore need (RECOVERY mode)
   - Stop PVE services: pve-cluster, pvedaemon, pveproxy, pvestatd
   - Unmount /etc/pve
   - Defer restart

7. **PBS Service Management** (`preparePBSServices()` in
   `restore_workflow_ui_backups_services.go` → `stopPBSServices()` / `startPBSServices()`
   in `restore_services.go`):
   - Detect PBS-specific category restore need
   - Stop PBS services: proxmox-backup-proxy, proxmox-backup
   - Prompt to continue if stop fails
   - Defer restart

8. **File Extraction** (`prepareAndRestoreSelectedPayloads()` → `extractNormalCategories()` /
   `exportCategories()` in `restore_workflow_ui_extract.go`; engine `extractSelectiveArchive()`
   in `restore_archive.go` → `extractArchiveNative()` in `restore_archive_extract.go`):
   - Extract normal categories to /
   - Extract export categories to timestamped directory
   - Handle extraction errors

9. **pvesh SAFE Apply** (`runClusterSafeApply()` in `restore_workflow_ui_extract.go` →
   `runSafeClusterApplyWithUI()` in `restore_workflow_ui_cluster_apply.go`, which drives
   `safeClusterApplyUIFlow.run()`; the standalone `runSafeClusterApply()` in
   `restore_cluster_apply.go` is the CLI wrapper, not used by this workflow):
   - If SAFE cluster mode selected
   - Apply VM/CT configs, storage.cfg, datacenter.cfg via API

10. **Post-Restore** (`runPostRestoreApplyWorkflows()` in `restore_workflow_ui_run.go` →
    `recreateStorageDirectories()` / `applyNetworkConfig()` / `applyFirewallConfig()` /
    `applyHAConfig()`; then `logRestoreCompletion()` and `checkZFSPoolsAfterRestore()`):
    - Recreate storage/datastore directories
    - Check ZFS pools (only when the `zfs` category is selected; no-op if `zpool` is absent)
    - Display completion summary

### File: internal/orchestrator/categories.go

**Purpose**: Define and manage category system

**Key Types**:

```go
type CategoryType string

const (
    CategoryTypePVE    CategoryType = "pve"
    CategoryTypePBS    CategoryType = "pbs"
    CategoryTypeCommon CategoryType = "common"
)

type Category struct {
    ID          string       // Unique identifier
    Name        string       // Display name
    Description string       // User-friendly description
    Type        CategoryType // PVE, PBS, or Common
    Paths       []string     // Archive paths included
    IsAvailable bool         // Present in backup
    ExportOnly  bool         // Never restore to system paths
}
```

**Key Functions**:

1. **`GetAllCategories()`** (`categories.go`):
   - Returns complete list of 15+ categories
   - Hardcoded category definitions
   - Each category includes ID, name, description, paths

2. **`PathMatchesCategory()`** (`categories.go`):
   - Check if archive path belongs to category
   - Handles exact matches and directory prefixes
   - Path normalization

3. **`GetCategoriesForMode()`** (`selective.go`):
   - Return categories for restore mode
   - Filters export-only categories
   - Mode-specific category lists

4. **`GetStorageModeCategories()`** (`categories.go`):
   - PVE: cluster, storage, jobs, zfs
   - PBS: config, datastore, jobs, zfs

5. **`GetBaseModeCategories()`** (`categories.go`):
   - Common categories only
   - Network, SSL, SSH, services

**SSH Category Coverage**

- `./etc/ssh/` → sshd configuration, host keys, authorized_keys
- `./root/.ssh/` → root private/public keys and known_hosts
  (these are the paths matched by the `ssh` category during restore)

### File: internal/orchestrator/selective.go

**Purpose**: Interactive category selection UI

**Key Functions**:

1. **`ShowRestoreModeMenuWithReader()`** (`selective.go`):
   - Display mode menu
   - Get user selection
   - Return RestoreMode enum
   - Reached via the `RestoreWorkflowUI.SelectRestoreMode()` interface (CLI/TUI implementations)

2. **`ShowCategorySelectionMenuWithReader()`** (`selective.go`):
   - Display checkbox menu
   - Toggle category selection
   - Commands: number, 'a', 'n', 'c', 'b', '0'
   - Reached via the `RestoreWorkflowUI.SelectCategories()` interface

3. **`ShowRestorePlan()`** (`selective.go`):
   - Display selected categories
   - Show file paths to be restored
   - Display warnings
   - Reached via the `RestoreWorkflowUI.ShowRestorePlan()` interface

4. **`ConfirmRestoreOperationWithReader()`** (`selective.go`):
   - User must type "RESTORE" (case-sensitive); "cancel"/"0" aborts
   - Reached via the `RestoreWorkflowUI.ConfirmRestore()` interface, orchestrated by the
     workflow method `confirmRestorePlan()` in `restore_workflow_ui_plan.go`
   - CLI flow is two-stage: confirm ("RESTORE") then an overwrite "yes"/"no" prompt

### File: internal/orchestrator/decrypt.go

**Purpose**: Handle backup decryption

**Key Functions**:

1. **`prepareDecryptedBackup()`** (`decrypt.go`):
   - Entry point for the CLI decryption workflow
   - Delegates to selection and decryption

2. **`prepareRestoreBundleWithUI()` / `selectBackupCandidateWithUI()`** (`restore_workflow_ui.go`, `decrypt_workflow_ui.go`):
   - Display configured paths
   - User selects location
   - Scans for backups

3. **`discoverBackupCandidates()`** (`backup_sources.go`):
   - Find .bundle.tar files
   - Parse manifests
   - Sort by creation date

4. **`selectBackupCandidateWithUI()`** (`decrypt_workflow_ui.go`):
   - Display backup list with metadata
   - User selects by number

5. **`preparePlainBundleCommon()` / `preparePlainBundleWithUI()`** (`decrypt_prepare_common.go`, `decrypt_workflow_ui.go`):
   - Check encryption status
   - Prompt for key/passphrase
   - Decrypt to /tmp
   - Verify checksum (`verifyStagedArchiveIntegrity()` in `decrypt_integrity.go`)

---

## Execution Flow

### Detailed Phase Breakdown

#### Phase 1: Initialization

**File**: `cmd/proxsave/main_restore_decrypt.go` (`runRestoreCLI()` / `runRestoreTUI()`)

```go
// runRestoreCLI dispatches to the orchestrator
err := orchestrator.RunRestoreWorkflow(ctx, cfg, logger, version)
```

**Inputs**:
- Context (for cancellation)
- Config (from backup.env)
- Logger
- Version string

**Outputs**:
- Error (or nil on success)

---

#### Phase 2: Backup Preparation

**File**: `internal/orchestrator/restore_workflow_ui_plan.go` → `prepareBundle()`

```go
candidate, prepared, err := prepareRestoreBundleFunc(ctx, cfg, logger, version, ui)
if err != nil {
    return err
}
// cleanup deferred
defer prepared.Cleanup()
```

**Sub-phases**:

1. **Path Selection** (`selectBackupCandidateWithUI()` in `decrypt_workflow_ui.go`):
   ```go
   Select backup source:
     [1] Primary: /opt/proxsave/backup
     [2] Secondary: /mnt/secondary/backups
     [3] Cloud: /mnt/cloud-backups
   ```

2. **Backup Discovery** (`discoverBackupCandidates()` in `backup_sources.go`):
   - Scan for `.bundle.tar` files
   - Parse JSON manifests
   - Extract metadata (date, encryption, version)

3. **Backup Selection** (`selectBackupCandidateWithUI()` in `decrypt_workflow_ui.go`):
   - Display sorted list (newest first)
   - User selects by index

4. **Decryption** (`preparePlainBundleCommon()` in `decrypt_prepare_common.go`):
   - Check if encrypted (manifest)
   - Prompt for AGE key/passphrase
   - Decrypt to `/tmp/proxsave/proxmox-decrypt-<random>/`
   - Verify SHA256 checksum

**Data Structure**:
```go
type preparedBundle struct {
    ArchivePath    string         // Path to plaintext archive
    Manifest       backup.Manifest // Parsed metadata
    Checksum       string         // Plaintext archive checksum
    SourceChecksum string         // Pre-decrypt integrity value
    cleanup        func()         // Cleanup temporary files (via Cleanup())
}
```

---

#### Phase 3: System Detection & Compatibility

**File**: `internal/orchestrator/restore_workflow_ui_plan.go` → `detectTargetSystem()`, `confirmCompatibility()`

This section is the technical source of truth for restore compatibility.
User-facing examples and warning text live in
[RESTORE_GUIDE.md](RESTORE_GUIDE.md#phase-3-compatibility-check).

```go
systemType := DetectCurrentSystem()
backupType := DetectBackupType(prepared.Manifest)

if err := ValidateCompatibility(systemType, backupType); err != nil {
    logger.Warning("Compatibility check: %v", err)
    // Continue with warning or abort depending on workflow context
}
```

**System Detection** (`compatibility.go`):
```go
func DetectCurrentSystem() SystemType {
    hasPVE := fileExists("/etc/pve") || fileExists("/usr/bin/qm") || fileExists("/usr/bin/pct")
    hasPBS := fileExists("/etc/proxmox-backup") || fileExists("/usr/sbin/proxmox-backup-proxy")

    switch {
    case hasPVE && hasPBS:
        return SystemTypeDual
    case hasPVE:
        return SystemTypePVE
    case hasPBS:
        return SystemTypePBS
    default:
        return SystemTypeUnknown
    }
}
```

Restore compatibility is therefore **capability-based**, not exact-match only.

**Backup Type Detection**:
```go
func DetectBackupType(manifest *backup.Manifest) SystemType {
    if len(manifest.ProxmoxTargets) > 0 {
        return parseSystemTargets(manifest.ProxmoxTargets)
    }
    if manifest.ProxmoxType != "" {
        return parseSystemTypeString(manifest.ProxmoxType)
    }
    // Fallback: hostname heuristics
    return SystemTypeUnknown
}
```

**Compatibility Check**:
- **incompatible**: no shared role between backup and current host
- **partial compatibility**: shared role exists, but backup and host are not identical
- **full compatibility**: same role set

When compatibility is partial, restore continues with warnings and later filters
the category set to the roles supported by the current host.

---

#### Phase 4: Category Analysis

**File**: `internal/orchestrator/restore_workflow_ui_plan.go` → `analyzeArchive()`

```go
availableCategories, err := AnalyzeBackupCategories(
    prepared.ArchivePath,
    logger,
)
```

**Implementation** (`AnalyzeBackupCategories()` in `selective.go`, a thin wrapper over
`AnalyzeRestoreArchive()` in `restore_decision.go`):

```go
func AnalyzeBackupCategories(
    archivePath string,
    logger *logging.Logger,
) ([]Category, error) {
    // 1. Open archive with decompression
    file, _ := os.Open(archivePath)
    reader := createDecompressionReader(file, archivePath)
    tarReader := tar.NewReader(reader)

    // 2. Collect all entry names
    var allPaths []string
    for {
        header, err := tarReader.Next()
        if err == io.EOF {
            break
        }
        allPaths = append(allPaths, header.Name)
    }

    // 3. Check each category for matches
    categories := GetAllCategories()
    for i := range categories {
        for _, path := range allPaths {
            if PathMatchesCategory(path, categories[i]) {
                categories[i].IsAvailable = true
                break
            }
        }
    }

    // 4. Filter to available only
    available := []Category{}
    for _, cat := range categories {
        if cat.IsAvailable {
            available = append(available, cat)
        }
    }

    return available, nil
}
```

**Path Matching** (`PathMatchesCategory()` in `categories.go`):
```go
func PathMatchesCategory(filePath string, category Category) bool {
    // Normalize paths to start with "./"
    normalized := filePath
    if !strings.HasPrefix(normalized, "./") {
        normalized = "./" + normalized
    }

    for _, catPath := range category.Paths {
        // Exact match
        if normalized == catPath {
            return true
        }

        // Directory prefix match
        if strings.HasSuffix(catPath, "/") {
            if strings.HasPrefix(normalized, catPath) {
                return true
            }
        }
    }

    return false
}
```

---

#### Phase 5: Category Selection

**File**: `internal/orchestrator/restore_workflow_ui_plan.go` → `selectRestorePlan()`

```go
// Pick the mode and the categories (GetCategoriesForMode for FULL/STORAGE/BASE,
// or the interactive SelectCategories for CUSTOM)
categories, mode, err := w.selectModeAndCategories()

// Build the plan; PlanRestore splits the selection 3-way via splitRestoreCategories
// (normal / staged / export-only)
w.plan = PlanRestore(w.decisionInfo.ClusterPayload, categories, w.systemType, mode)

// Later, in runSelectiveRestore(), the plan is shown and confirmed:
//   confirmRestorePlan() -> ui.ShowRestorePlan() + ui.ConfirmRestore()
if err := w.confirmRestorePlan(); err != nil {
    return err // ErrRestoreAborted on cancel
}
```

**Mode Selection UI** (`ShowRestoreModeMenuWithReader()` in `selective.go`):
```
Select restore mode:
  [1] FULL restore - Restore everything from backup
  [2] STORAGE only - Cluster/storage + jobs
  [3] SYSTEM BASE only - Network + SSL + SSH + services
  [4] CUSTOM selection - Choose specific categories
  [0] Cancel

Your selection: _
```

**Custom Selection UI** (`ShowCategorySelectionMenuWithReader()` in `selective.go`):
```
Available categories:
  [1] [ ] PVE Cluster Configuration
      Proxmox VE cluster configuration and database
  [2] [ ] Network Configuration
      Network interfaces and routing
  ...

Commands:
  - Type number to toggle
  - 'a' = select all
  - 'n' = deselect all
  - 'c' = continue
  - '0' = cancel

Your selection: _
```

---

#### Phase 6: Safety Backup

**File**: `internal/orchestrator/restore_workflow_ui_backups_services.go` → `createSafetyBackup()`

```go
var safetyBackup *SafetyBackupResult
if len(normalCategories) > 0 {
    safetyBackup, err = CreateSafetyBackup(logger, normalCategories, destRoot)
    if err != nil {
        logger.Warning("Failed to create safety backup: %v", err)
        // Prompt user to continue or abort
        if !getUserConfirmation("yes") {
            return fmt.Errorf("restore aborted: safety backup failed")
        }
    }
}
```

**Implementation** (`CreateSafetyBackup()` in `backup_safety.go`):

```go
func CreateSafetyBackup(
    logger *logging.Logger,
    categories []Category,
    destRoot string,
) (*SafetyBackupResult, error) {
    // 1. Create backup archive
    timestamp := time.Now().Format("20060102_150405")
    backupPath := filepath.Join(
        "/tmp/proxsave",
        fmt.Sprintf("restore_backup_%s.tar.gz", timestamp),
    )

    // 2. Create TAR+GZIP writer
    file, _ := os.Create(backupPath)
    gzipWriter := gzip.NewWriter(file)
    tarWriter := tar.NewWriter(gzipWriter)

    // 3. For each category path
    for _, cat := range categories {
        for _, path := range cat.Paths {
            fullPath := filepath.Join(destRoot, strings.TrimPrefix(path, "./"))

            // Check if exists
            if _, err := os.Stat(fullPath); os.IsNotExist(err) {
                continue
            }

            // Backup file/directory recursively
            filepath.Walk(fullPath, func(p string, info os.FileInfo, err error) error {
                // Create TAR header
                header, _ := tar.FileInfoHeader(info, "")
                header.Name = strings.TrimPrefix(p, destRoot)

                // Write header
                tarWriter.WriteHeader(header)

                // Write file content (if regular file)
                if info.Mode().IsRegular() {
                    f, _ := os.Open(p)
                    io.Copy(tarWriter, f)
                    f.Close()
                }
                return nil
            })
        }
    }

    // 4. Close archive
    tarWriter.Close()
    gzipWriter.Close()
    file.Close()

    return &SafetyBackupResult{BackupPath: backupPath}, nil
}
```

---

#### Phase 7: Service Management (Cluster Restore Only)

**File**: `internal/orchestrator/restore_workflow_ui_backups_services.go` → `preparePVEClusterRestore()`

```go
needsClusterRestore := systemType == SystemTypePVE &&
                       hasCategoryID(normalCategories, "pve_cluster")

if needsClusterRestore {
    logger.Info("Preparing system for cluster database restore")
    logger.Info("Stopping PVE services and unmounting /etc/pve")

    // Stop services
    if err := stopPVEClusterServices(ctx, logger); err != nil {
        return err  // FAIL-FAST
    }

    // Defer restart (always executes)
    defer func() {
        if err := startPVEClusterServices(ctx, logger); err != nil {
            logger.Warning("Failed to restart PVE services: %v", err)
        }
    }()

    // Unmount /etc/pve
    if err := unmountEtcPVE(ctx, logger); err != nil {
        logger.Warning("Could not unmount /etc/pve: %v", err)
        // Continue anyway
    }
}
```

**Stop Services** (`internal/orchestrator/restore_services.go` → `stopPVEClusterServices()`):
```go
func stopPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
    commands := [][]string{
        {"systemctl", "stop", "pve-cluster"},
        {"systemctl", "stop", "pvedaemon"},
        {"systemctl", "stop", "pveproxy"},
        {"systemctl", "stop", "pvestatd"},
    }
    for _, cmd := range commands {
        if err := runCommand(ctx, logger, cmd[0], cmd[1:]...); err != nil {
            return fmt.Errorf("failed to stop %s: %w", cmd[2], err)
        }
    }
    return nil
}
```

**Start Services** (`internal/orchestrator/restore_services.go` → `startPVEClusterServices()`):
```go
func startPVEClusterServices(ctx context.Context, logger *logging.Logger) error {
    commands := [][]string{
        {"systemctl", "start", "pve-cluster"},
        {"systemctl", "start", "pvedaemon"},
        {"systemctl", "start", "pveproxy"},
        {"systemctl", "start", "pvestatd"},
    }
    for _, cmd := range commands {
        if err := runCommand(ctx, logger, cmd[0], cmd[1:]...); err != nil {
            return fmt.Errorf("failed to start %s: %w", cmd[2], err)
        }
    }
    return nil
}
```

**Unmount** (`internal/orchestrator/restore_services.go` → `unmountEtcPVE()`):
```go
func unmountEtcPVE(ctx context.Context, logger *logging.Logger) error {
    cmd := exec.CommandContext(ctx, "umount", "/etc/pve")
    output, err := cmd.CombinedOutput()
    msg := strings.TrimSpace(string(output))

    if err != nil {
        // "not mounted" is not an error
        if strings.Contains(msg, "not mounted") {
            logger.Info("Skipping umount (already unmounted)")
            return nil
        }
        return fmt.Errorf("umount failed: %s", msg)
    }

    logger.Info("Successfully unmounted /etc/pve")
    return nil
}
```

---

#### Phase 8 & 9: File Extraction

**Three-tier extraction.** `splitRestoreCategories` (`staging.go`) divides the selected
categories into three tiers: **export-only**, **staged** (sensitive), and **normal**.
`prepareAndRestoreSelectedPayloads` (`restore_workflow_ui_run.go`) then processes them in
a fixed order:

1. **Normal categories** are extracted directly to `destRoot` (`/` on a real restore) via
   `extractNormalCategories`.
2. **`/etc/fstab`** is smart-merged, never blindly overwritten (see Safety Mechanisms).
3. **Export-only categories** (`pve_config_export`, `pbs_config`, `proxsave_info`) are
   extracted read-only to an export dir (`proxmox-config-export-<ts>` under the base dir),
   never onto the live system.
4. **Cluster SAFE apply** runs when applicable.
5. **Staged (sensitive) categories** are extracted into a per-run stage dir
   (`/tmp/proxsave/restore-stage-<ts>_<seq>`) and then applied (Phase 10).

```go
// restore_workflow_ui_run.go: prepareAndRestoreSelectedPayloads (processing order)
interceptFilesystemCategory(...)       // fstab handling
extractNormalCategories(...)           // tier 1 -> destRoot
smartMergeFilesystemCategory(...)      // fstab smart merge
exportCategories(...)                  // tier 3 -> export dir (read-only)
runClusterSafeApply(...)               // cluster SAFE apply
stageAndApplySensitiveCategories(...)  // tier 2 -> stage dir, then Phase 10
```

Staged categories are extracted **strictly**:
`extractSelectiveArchiveStrict(..., failOnPartial=true)`. If any entry fails, the whole
staged apply is skipped and the live system is left untouched (BH-002), so a partial tree
is never applied to sensitive config. The plain (non-staged) tiers use
`extractSelectiveArchive`, a thin wrapper over `extractSelectiveArchiveStrict(..., false)`
that creates the detailed log under `/tmp/proxsave` and calls `extractArchiveNative`.

**Which categories are staged** (`isStagedCategoryID`, `staging.go`): `network`,
`datastore_pbs`, `pbs_jobs`, `pbs_remotes`, `pbs_host`, `pbs_tape`, `storage_pve`,
`pve_jobs`, `pve_notifications`, `pbs_notifications`, `pve_access_control`,
`pbs_access_control`, `accounts`, `pve_firewall`, `pve_ha`, `pve_sdn`. Everything else is
a normal category unless it is flagged `ExportOnly`.

When staging is unavailable (a non-real or test filesystem) the plan folds the staged
categories back into the normal tier, so they are extracted directly instead of
staged-and-applied (`restore_workflow_ui_plan.go`).

---

#### Phase 10: Staged Apply

After the sensitive categories are staged under `/tmp/proxsave/restore-stage-*`,
`applyStagedCategories` (`restore_workflow_ui_extract.go`) applies them. It first runs the
**PBS datastore mount guards** as a pre-step (see Safety Mechanisms), then a fixed ordered
list of steps. Each step is individually gated on whether its category was selected,
re-checks context cancellation between steps, and degrades a non-fatal error to a warning
(only an abort or input error stops the apply):

1. **PBS staged config apply** (`maybeApplyPBSConfigsFromStage`, `pbs_staged_apply.go`): datastores/S3, remotes, sync/verify/prune jobs, node + traffic control, tape.
2. **PVE staged config apply** (`maybeApplyPVEConfigsFromStage`, `pve_staged_apply.go`): `storage_pve`, `pve_jobs`.
3. **PVE SDN staged apply** (`maybeApplyPVESDNFromStage`, `restore_sdn.go`).
4. **Access control staged apply** (`applyAccessControlFromStage` -> `maybeApplyAccessControlWithUI`): PBS and PVE ACLs, with a rollback/commit UI. In a cluster backup it skips the 1:1 PVE apply in SAFE mode and skips entirely in RECOVERY mode (`config.db` owns that state). Requires root and a real filesystem.
5. **System accounts staged apply** (`maybeApplyAccountsFromStage`, `restore_accounts.go`): the anti-lockout merge below.
6. **Notifications staged apply** (`maybeApplyNotificationsFromStage`, `restore_notifications.go`): PVE and PBS notification endpoints/matchers.

**PBS apply model (interactive).** On PBS hosts the apply offers **Merge (existing PBS)**
vs **Clean 1:1 (fresh PBS install)**. Merge creates/updates only (no deletions); Clean 1:1
attempts a 1:1 reconciliation (may remove objects not in the backup) via
`proxmox-backup-manager`, and may fall back to writing staged `*.cfg` files into
`/etc/proxmox-backup` (Clean 1:1 only). API coverage: `pbs_host` (node + traffic control),
`datastore_pbs` (datastores + S3), `pbs_remotes`, `pbs_jobs` (sync/verify/prune),
`pbs_notifications`. Other PBS categories remain file-based.

**Anti-lockout account merge** (`applyAccountsFromStage`). Accounts are **merged**, not
overwritten. ProxSave keeps every current host line and imports only regular backup
accounts: a backup user is imported only if it is non-root, non-NIS, has a valid name,
`uid >= 1000` (`systemAccountIDThreshold`), and no primary-gid escalation. It reads the
current `/etc/passwd`, `/etc/group`, and `/etc/shadow` first and refuses to rewrite the
auth DB if any is missing (no host baseline). A missing shadow line becomes a locked
placeholder (`:*:::::::`). Existing host groups only gain imported members; new groups
need `gid >= 1000`. All four files (`passwd`/`shadow`/`group`/`gshadow`) are written
all-or-nothing with the current contents as rollback. `/etc/sudoers` is replaced only if
the staged copy passes `visudo -c`.

**PBS raw-config skips** (`shouldSkipProxmoxSystemRestore`, `restore_archive_paths.go`).
These are never restored raw (they are recreated through the API instead):
`etc/proxmox-backup/domains.cfg`, `etc/proxmox-backup/user.cfg`,
`etc/proxmox-backup/acl.cfg`, plus the runtime locks `var/lib/proxmox-backup/.clusterlock`
and anything under `var/lib/proxmox-backup/lock/`.

## Category System

### Category Definition Structure

```go
type Category struct {
    ID          string       // Unique identifier for code
    Name        string       // Display name for users
    Description string       // User-friendly explanation
    Type        CategoryType // PVE, PBS, or Common
    Paths       []string     // Archive paths to match
    IsAvailable bool         // Set by analysis phase
    ExportOnly  bool         // Never restore to system
}
```

### Category Types

```go
const (
    CategoryTypePVE    CategoryType = "pve"    // PVE-specific
    CategoryTypePBS    CategoryType = "pbs"    // PBS-specific
    CategoryTypeCommon CategoryType = "common" // Both systems
)
```

### Path Matching Algorithm

**File**: `internal/orchestrator/categories.go` (`PathMatchesCategory()`)

```go
func PathMatchesCategory(filePath string, category Category) bool {
    // Step 1: Normalize file path
    normalized := filePath
    if !strings.HasPrefix(normalized, "./") &&
       !strings.HasPrefix(normalized, "../") {
        normalized = "./" + normalized
    }

    // Step 2: Check against each category path
    for _, catPath := range category.Paths {
        // Exact match
        if normalized == catPath {
            return true
        }

        // Directory prefix match
        if strings.HasSuffix(catPath, "/") {
            // Handle with or without trailing slash
            dirPath := strings.TrimSuffix(catPath, "/")

            // Exact directory match
            if normalized == dirPath {
                return true
            }

            // Prefix match (file under directory)
            if strings.HasPrefix(normalized, catPath) {
                return true
            }
        }
    }

    return false
}
```

**Examples**:

| Archive Path | Category Path | Match? | Reason |
|--------------|---------------|--------|--------|
| `./etc/network/interfaces` | `./etc/network/` | yes | Prefix match |
| `./etc/network/interfaces` | `./etc/network/interfaces` | yes | Exact match |
| `./etc/hostname` | `./etc/hostname` | yes | Exact match |
| `./etc/hostname` | `./etc/network/` | no | No match |
| `./var/lib/pve-cluster/config.db` | `./var/lib/pve-cluster/` | yes | Prefix match |
| `etc/network/interfaces` | `./etc/network/` | yes | Normalized to `./` |

### Adding New Categories

**Step 1**: Define category in `categories.go`

```go
{
    ID:          "my_custom",
    Name:        "My Custom Category",
    Description: "Description of what this category contains",
    Type:        CategoryTypeCommon,  // or PVE/PBS specific
    Paths: []string{
        "./path/to/files/",
        "./specific/file",
    },
    ExportOnly: false,  // true if should never restore to /
},
```

**Step 2**: Add to mode definitions (if applicable)

```go
func GetStorageModeCategories(systemType string) []Category {
    // Add your category ID here if it should be in Storage mode
}
```

**Step 3**: Test category matching

```bash
# Create test backup with your files
# Run restore in Custom mode
# Verify category appears and files extract correctly
```

---

## Service Management

### Lifecycle Management Pattern

**Go defer pattern** ensures cleanup even on errors:

```go
func RunRestoreWorkflow(...) error {
    // ... setup ...

    if needsClusterRestore {
        // Stop services
        stopPVEClusterServices(ctx, logger)

        // Schedule restart (ALWAYS executes)
        defer func() {
            startPVEClusterServices(ctx, logger)
        }()

        // Unmount filesystem
        unmountEtcPVE(ctx, logger)
    }

    // ... restore operations ...
    // Even if restore fails, defer will restart services
}
```

### Service Dependencies

**PVE Service Dependency Graph**:

```
pve-cluster (pmxcfs)
    ↓ (provides /etc/pve via FUSE)
pvedaemon
    ↓ (provides API)
pveproxy
    ↓ (provides web interface)
pvestatd
    (provides statistics)

Stop order:  pve-cluster → pvedaemon → pveproxy → pvestatd
Start order: pve-cluster → pvedaemon → pveproxy → pvestatd
```

**PBS Service Dependency Graph**:

```
proxmox-backup-proxy
    ↓ (provides web interface and API)
proxmox-backup
    (provides backup/restore operations)

Stop order:  proxmox-backup-proxy → proxmox-backup
Start order: proxmox-backup → proxmox-backup-proxy
```

**PBS Service Management Code**:
```go
func stopPBSServices(ctx context.Context, logger *logging.Logger) error {
    commands := [][]string{
        {"systemctl", "stop", "proxmox-backup-proxy"},
        {"systemctl", "stop", "proxmox-backup"},
    }
    // ... execute with error collection
}

func startPBSServices(ctx context.Context, logger *logging.Logger) error {
    commands := [][]string{
        {"systemctl", "start", "proxmox-backup"},
        {"systemctl", "start", "proxmox-backup-proxy"},
    }
    // ... execute with error collection
}
```

**PBS Service Trigger**: PBS services are stopped when any PBS-specific category is selected:
```go
func shouldStopPBSServices(categories []Category) bool {
    for _, cat := range categories {
        if cat.Type == CategoryTypePBS {
            return true
        }
    }
    return false
}
```

**API apply note**: When ProxSave applies PBS staged categories via API (`proxmox-backup-manager`), it may start PBS services again during the **staged apply** phase (even if services were stopped earlier for safe file extraction).

### Error Handling Philosophy

**Stop Phase**: **FAIL-FAST**
```go
if err := stopPVEClusterServices(ctx, logger); err != nil {
    return err  // Abort restore completely
}
```
**Reason**: Cannot safely restore if services still running

**Start Phase**: **WARN-ONLY**
```go
defer func() {
    if err := startPVEClusterServices(ctx, logger); err != nil {
        logger.Warning("Failed to restart: %v", err)
        // Continue anyway - restore already completed
    }
}()
```
**Reason**: Restore already done, aborting doesn't help

---

## Cluster SAFE/RECOVERY Mode

### Standalone vs Cluster Detection

The `ClusterMode` field in the backup manifest determines restore behavior:

| Manifest Value | Detection | Prompt Shown | Restore Behavior |
|----------------|-----------|--------------|------------------|
| `"standalone"` or empty | Standalone | NO | Direct database restore |
| `"cluster"` | Cluster | YES | SAFE or RECOVERY choice |

**ClusterMode is set during backup** in `backup_run_helpers.go` (`standaloneClusterMode()`):
```go
func standaloneClusterMode(collector *backup.Collector) string {
    if collector.IsClusteredPVE() {
        return "cluster"
    }
    return "standalone"
}
```

### Detection Logic

The workflow detects cluster backups via the manifest's `ClusterMode` field:

```go
if systemType == SystemTypePVE &&
   strings.EqualFold(strings.TrimSpace(candidate.Manifest.ClusterMode), "cluster") &&
   hasCategoryID(selectedCategories, "pve_cluster") {
    // Cluster backup detected, prompt for SAFE vs RECOVERY
}
```

**For standalone backups**: This condition is FALSE, so:
- No SAFE/RECOVERY prompt is shown
- `pve_cluster` remains in `normalCategories`
- Database is restored directly (same as RECOVERY mode)

**For cluster backups**: This condition is TRUE, so:
- SAFE/RECOVERY prompt is shown
- User chooses restore strategy
- SAFE mode redirects to export + pvesh API

### SAFE Mode Implementation

When SAFE mode is selected:

```go
if choice == 1 { // SAFE mode
    clusterSafeMode = true
    // Redirect pve_cluster from normal to export-only
    normalCategories, exportCategories = redirectClusterCategoryToExport(normalCategories, exportCategories)
}
```

**`redirectClusterCategoryToExport()`**:
```go
func redirectClusterCategoryToExport(normal []Category, export []Category) ([]Category, []Category) {
    filtered := make([]Category, 0, len(normal))
    for _, cat := range normal {
        if cat.ID == "pve_cluster" {
            export = append(export, cat) // Move to export
            continue
        }
        filtered = append(filtered, cat)
    }
    return filtered, export
}
```

### pvesh SAFE Apply

After extraction in SAFE mode, `runSafeClusterApply()` offers API-based restoration (primarily VM/CT configs). When the user selects the `storage_pve` category, storage.cfg + datacenter.cfg are applied later via the staged restore pipeline and SAFE apply will skip prompting for them.

**Key Functions**:
- `scanVMConfigs()`: Scans `<export>/etc/pve/nodes/<node>/qemu-server/` and `lxc/`
- `applyVMConfigs()`: Applies each config via `pvesh set /nodes/<node>/<type>/<vmid>/config`
- `applyStorageCfg()`: Parses storage.cfg blocks and applies via `pvesh set /cluster/storage/<id>`
- `runPvesh()`: Executes pvesh commands with logging

**Flow**:
```go
func runSafeClusterApply(ctx context.Context, reader *bufio.Reader, exportRoot string, logger *logging.Logger) error {
    // 1. Scan and apply VM/CT configs
    vmEntries, _ := scanVMConfigs(exportRoot, currentNode)
    if len(vmEntries) > 0 && promptYesNo("Apply all VM/CT configs via pvesh?") {
        applyVMConfigs(ctx, vmEntries, logger)
    }

    // 2. Apply storage.cfg
    storageCfg := filepath.Join(exportRoot, "etc/pve/storage.cfg")
    if fileExists(storageCfg) && promptYesNo("Apply storage.cfg via pvesh?") {
        applyStorageCfg(ctx, storageCfg, logger)
    }

    // 3. Apply datacenter.cfg
    dcCfg := filepath.Join(exportRoot, "etc/pve/datacenter.cfg")
    if fileExists(dcCfg) && promptYesNo("Apply datacenter.cfg via pvesh?") {
        runPvesh(ctx, logger, []string{"set", "/cluster/config", "-conf", dcCfg})
    }
}
```

---

## Extraction Engine

### Archive Format Support

**Decompression** (`internal/orchestrator/restore_decompression.go` → `createDecompressionReader()`):

```go
func createDecompressionReader(ctx context.Context, file *os.File, archivePath string) (io.ReadCloser, error) {
    switch {
    case strings.HasSuffix(archivePath, ".tar.gz"),
         strings.HasSuffix(archivePath, ".tgz"):
        return gzip.NewReader(file)  // Native Go

    case strings.HasSuffix(archivePath, ".tar.xz"):
        return createXZReader(ctx, file)  // External: xz command

    case strings.HasSuffix(archivePath, ".tar.zst"),
         strings.HasSuffix(archivePath, ".tar.zstd"):
        return createZstdReader(ctx, file)  // External: zstd command

    case strings.HasSuffix(archivePath, ".tar.bz2"):
        return createBzip2Reader(ctx, file)  // External: bzip2 command

    case strings.HasSuffix(archivePath, ".tar.lzma"):
        return createLzmaReader(ctx, file)  // External: lzma command

    case strings.HasSuffix(archivePath, ".tar"):
        return io.NopCloser(file), nil  // No decompression

    default:
        return nil, fmt.Errorf("unsupported format: %s", archivePath)
    }
}
```

### Selective Extraction Logic

**File**: `internal/orchestrator/restore_archive_extract.go` → `extractArchiveNative()`

```go
func extractArchiveNative(ctx context.Context, opts restoreArchiveOptions) error {
    // 1. Open archive with decompression
    file, _ := restoreFS.Open(opts.archivePath)
    reader, _ := createDecompressionReader(ctx, file, opts.archivePath)
    tarReader := tar.NewReader(reader)

    // 2. Iterate through TAR entries
    for {
        header, err := tarReader.Next()
        if err == io.EOF {
            break
        }

        // 3. Category filtering
        shouldExtract := false
        for _, cat := range opts.categories {
            if PathMatchesCategory(header.Name, cat) {
                shouldExtract = true
                break
            }
        }
        if !shouldExtract {
            filesSkipped++
            continue
        }

        // 4. Security checks: the real code resolves and validates the target via
        //    sanitizeRestoreEntryTargetWithFS() (restore_archive_paths.go), which
        //    rejects path traversal and symlink escapes outside destRoot.
        target, _, err := sanitizeRestoreEntryTargetWithFS(restoreFS, opts.destRoot, header.Name)
        if err != nil {
            return fmt.Errorf("illegal path: %s", header.Name)
        }

        // 5. /etc/pve hard guard (exact match or under /etc/pve/)
        if opts.destRoot == "/" && (target == "/etc/pve" || strings.HasPrefix(target, "/etc/pve/")) {
            opts.logger.Warning("Skipping %s (writes to /etc/pve prohibited)", target)
            continue
        }

        // 6. Extract based on type (extractTypedTarEntry passes the cleaned
        //    destRoot to symlink/hardlink so path escapes can be validated).
        switch header.Typeflag {
        case tar.TypeDir:
            extractDirectory(target, header, logger)
        case tar.TypeReg:
            extractRegularFile(tarReader, target, header, logger)
        case tar.TypeSymlink:
            extractSymlink(target, header, cleanDestRoot, logger)
        case tar.TypeLink:
            // BH-002: in selective mode a hardlink whose Linkname is outside the
            // selected categories is refused, so an in-category name can never alias
            // an out-of-category inode (for example /etc/shadow).
            extractHardlink(target, header, cleanDestRoot)
        }

        filesExtracted++
    }

    // BH-002: on the staged path (failOnPartialExtraction=true) any failed entry makes
    // the whole extraction return an error, so a partial tree is never applied.
    return nil
}
```

The real loop is `processRestoreArchiveEntries`, and the per-entry `/etc/pve` hard guard
plus the PBS raw-config skips live in `shouldSkipRestoreEntryTarget` (only when
`cleanDestRoot == "/"`). After the loop, `extractArchiveNative` also runs dedup symlink
materialization (below) before the fail-on-partial check.

### File Type Handling

Extraction never writes onto the live target. A regular file is written to a **sibling
temp file**, has its metadata set on the open file descriptor, and is then **atomically
renamed** over the target, so a truncated archive entry or a crash can only affect the
temp (removed on error), never the real file. The sibling temp name pattern is
`.proxsave-tmp-*` (`restoreTempPattern`); the rename is atomic because the temp is created
in the target's own directory (same filesystem). This replaces the older
`os.Create(target)` + `io.Copy` model, which wrote directly onto the live file and could
leave it truncated on a partial copy.

**Regular files** (`restore_archive_entries.go` -> `extractRegularFile`):
```go
func extractRegularFile(tarReader *tar.Reader, target string, header *tar.Header, logger *logging.Logger) (retErr error) {
    // Sibling temp in the target's own directory
    outFile, _ := restoreFS.CreateTemp(filepath.Dir(target), restoreTempPattern) // ".proxsave-tmp-*"
    tmpPath := outFile.Name()
    defer func() { if tmpPath != "" { _ = restoreFS.Remove(tmpPath) } }() // cleanup unless renamed

    io.Copy(outFile, tarReader)
    atomicFileChown(outFile, header.Uid, header.Gid) // fchown on the FD, best-effort
    atomicFileChmod(outFile, mode)                   // fchmod on the FD, hard error
    outFile.Close()

    restoreFS.Rename(tmpPath, target) // atomic replace
    tmpPath = ""                      // rename done, skip cleanup
    setTimestamps(target, header)     // atime/mtime on the final path
    return nil
}
```

Ownership and permissions are set on the **open descriptor** (`fchown`/`fchmod`, via the
`atomicFileChown`/`atomicFileChmod` helpers in `fs_atomic.go`) rather than by path, so a
logical or test filesystem root never leaks to a host path. The order is always
write -> fchown -> fchmod -> close -> rename -> timestamps.

**Directories** (`extractDirectory`): `MkdirAll(target, 0o700)` (owner-accessible), then
`Open` the directory and apply `fchown`/`fchmod` on the directory descriptor (mode masked
with `&0o7777`), then set timestamps. Chown is best-effort; a chmod failure is a hard
error.

**Symlinks** (`extractSymlink(target, header, destRoot, logger)`): the link target is
validated **before** creation with `resolvePathRelativeToBaseWithinRootFS` and again
**after** creation (readlink + re-resolve). If it resolves outside `destRoot` the symlink
is removed and the entry fails (`symlink target escapes root before/after creation`).
Ownership uses `Lchown`.

**Hard links** (`extractHardlink(target, header, destRoot)`): empty and absolute link
targets are rejected outright, then the target is resolved and validated with
`resolvePathWithinRootFS` (`hardlink target escapes root`) before the link is created. No
chown is applied to hard links.

Config files applied outside tar extraction (staged apply, network, and so on) use a
separate two-phase helper in `fs_atomic.go` (`prepareAtomicTempFile` +
`commitAtomicTempFile`, wrapped by `writeFileAtomic` / `writeFilesAtomic`) with `fsync`
plus a parent-directory `fsync` for durability. Note that helper uses a distinct temp
pattern (`%s.proxsave.tmp.%d`), not `restoreTempPattern`.

### Dedup symlink materialization (issue #70)

Deduplicated backups store repeated files once and record the duplicates as symlinks.
After the tar loop, `materializeDedupSymlinks` (`restore_archive_extract.go`) reads the
dedup manifest (always extracted regardless of the selected categories) and, for each
recorded duplicate still present as a symlink, rebuilds it into a **regular file from the
archive bytes** (never from the live target, never by deleting the symlink), using the
same atomic sibling-temp-plus-rename write. The manifest is streamed once (bounded
memory). If the pass does not complete it is **kept for retry** and, on the staged path,
surfaces an error (BH-002); on success it is deleted. A genuinely missing canonical (a
corrupt backup) is left as a symlink and does not block cleanup.

### Timestamp Preservation

`setTimestamps` sets atime and mtime on the final path with nanosecond precision via
`restoreFS.UtimesNano`. `ctime` (change time) cannot be set from userspace, so
`header.ChangeTime` is not restorable.

---

## Safety Mechanisms

### 1. Path Traversal Prevention

**Security Check** (`internal/orchestrator/restore_archive_paths.go` → `sanitizeRestoreEntryTargetWithFS()`):

```go
// Simplified illustration of the containment check. The real implementation is
// ensureRestoreTargetWithinRoot() in restore_archive_paths.go, which returns an
// error (not a bool) and is invoked by sanitizeRestoreEntryTargetWithFS().
func targetWithinRoot(target string, destRoot string) bool {
    cleanTarget := filepath.Clean(target)
    cleanDestRoot := filepath.Clean(destRoot)

    // Add trailing separator to prevent partial matches
    safePrefix := cleanDestRoot
    if !strings.HasSuffix(safePrefix, string(os.PathSeparator)) {
        safePrefix += string(os.PathSeparator)
    }

    // Check if target is under destRoot
    return strings.HasPrefix(cleanTarget, safePrefix) ||
           cleanTarget == cleanDestRoot
}
```

**Examples**:

| Target | DestRoot | Secure? |
|--------|----------|---------|
| `/var/lib/pve-cluster/config.db` | `/` | yes |
| `/../etc/passwd` | `/` | no |
| `/tmp/../etc/passwd` | `/` | no |
| `/opt/backup/file` | `/opt/backup` | yes |

### 2. /etc/pve Hard Guard

**Absolute Block** (`internal/orchestrator/restore_archive_entries.go` → `shouldSkipRestoreEntryTarget()`):

```go
// only when restoring to the real system root
if cleanDestRoot != string(os.PathSeparator) {
    return false, nil
}
// exact /etc/pve or anything under /etc/pve/ (NOT /etc/pvexyz)
if target == "/etc/pve" || strings.HasPrefix(target, "/etc/pve/") {
    logger.Warning("Skipping restore to %s (writes to /etc/pve are prohibited)", target)
    return true, nil  // skip this entry, don't error
}
```

**Applies only when**:
- Restoring to system root (`/`)
- Target is exactly `/etc/pve` or under `/etc/pve/`

**Does NOT apply**:
- Export-only extraction (different `destRoot`)

### 3. Smart `/etc/fstab` Merge + Device Remap

When restoring to the real system root (`/`), ProxSave avoids blindly overwriting `/etc/fstab`. Instead, it can run a **Smart Merge** workflow:

- Extracts the backup copy of `/etc/fstab` into a temporary directory.
- Compares it against the current system `/etc/fstab`.
- Proposes only **safe candidates**:
  - Network mounts (NFS/CIFS style entries)
  - Data mounts that use stable references (`UUID=`/`LABEL=`/`PARTUUID=` or `/dev/disk/by-*`) that exist on the restore host

**Device remap** (newer backups):
- If the backup contains ProxSave inventory (`var/lib/proxsave-info/commands/system/{blkid.txt,lsblk_json.json,lsblk.txt}` or PBS datastore inventory),
  ProxSave can remap unstable device paths from the backup (e.g. `/dev/sdb1`) to stable references (`UUID=`/`PARTUUID=`/`LABEL=`) **when the stable reference exists on the restore host**.
- This reduces the risk of mounting the wrong disk after a reinstall where `/dev/sdX` ordering changes.
- Note: backups taken from an **unprivileged container/rootless** environment may not include usable block-device inventory (for example `blkid` output can be empty/skipped). In that case, automated device remap is limited/unavailable and `/etc/fstab` entries may require manual review during restore.

**Normalization**:
- Entries written by the merge are normalized to include `nofail` (and `_netdev` for network mounts) to prevent offline storage from blocking boot/restore.

### 4. PBS Datastore Mount Guards (Offline Storage)

For PBS datastores whose paths live under typical mount roots (for example `/mnt/...`), ProxSave aims for a "restore even if offline" behavior:

- PBS datastore definitions are applied even when the underlying storage is offline/not mounted, so PBS shows them as **unavailable** rather than silently dropping them.
- When a mountpoint used by a datastore currently resolves to the root filesystem (mount missing), ProxSave applies a **read-only bind-mount guard** on the mount root.
- Guards prevent PBS from writing into `/` if the storage is missing at restore time.
- **Bind-mount guard:** when the real storage is mounted later it stacks on top of the read-only guard and the datastore becomes available again; the guard is then shadowed underneath and is discarded by a reboot or by `--cleanup-guards`.
- **If the bind mount cannot be created** (rare; e.g. a locked-down/containerized mount namespace), ProxSave does **not** set a persistent flag; it logs a loud warning that the mountpoint is unguarded and proceeds. (Older versions set a `chattr +i` immutable flag here; that flag survived reboots and could silently re-block the mountpoint once the storage was later unmounted, so it was removed.) ProxSave's own directory recreation on the mountpoint is still skipped by the storage-mount preflight, and the config-only restore never extracts into datastore mountpoints, so only *external* writers are unblocked while the storage stays offline.
- At restore start, if persistent `chattr +i` flags from an older version are still recorded, ProxSave warns and points to `--cleanup-guards`.

Optional maintenance:
- `proxsave --cleanup-guards` (preview with `--dry-run`) unmounts guard bind mounts **and** clears any **legacy** `chattr +i` immutable flags recorded by older versions, but only on mountpoints that are **not currently mounted** (clearing a live mount would touch the wrong inode). It prints a summary (unmounted / hidden-remaining / immutable-cleared / immutable-pending) and keeps the guard directory and its index until nothing is pending.
- To clear a legacy immutable flag on a mountpoint whose storage is already mounted: unmount it, run `--cleanup-guards` again (or `chattr -i <mountpoint>`), then remount.
- If you deleted `/var/lib/proxsave/guards` manually and a mountpoint is still read-only, ProxSave no longer has a record to clear: check with `lsattr -d <mountpoint>` and clear it yourself with `chattr -i <mountpoint>` while the storage is unmounted.

#### PVE Storage Mount Guards (Offline Storage)

For PVE storages that use mountpoints (notably `nfs`, `cifs`, `cephfs`, `glusterfs`, and `dir` storages on dedicated mountpoints), ProxSave applies the same "restore even if offline" safety model:

- Network storages use `/mnt/pve/<storageid>`. ProxSave attempts `pvesm activate <storageid>` with a short timeout.
- If the mountpoint still resolves to the root filesystem afterwards (mount missing/offline), ProxSave applies a **read-only bind-mount guard** on the mountpoint. If the bind mount cannot be created it logs a warning and proceeds unguarded (no persistent flag is set; see the PBS section above).
- For `dir` storages, guards are only applied when the storage `path` can be associated with a mountpoint present in `/etc/fstab` (to avoid guarding local root filesystem paths).

This prevents accidental writes into the root filesystem when storage is offline at restore time. When the real mount comes back it stacks on top of a bind-mount guard and normal operation resumes. See the cleanup notes under PBS Datastore Mount Guards above.

### 5. Root Privilege Check

**Pre-Extraction Check** (`internal/orchestrator/restore_archive.go` → `extractPlainArchive()`, `extractSelectiveArchiveStrict()`):

```go
// For system-path restoration on the REAL filesystem only
if destRoot == "/" && isRealRestoreFS(restoreFS) && os.Geteuid() != 0 {
    return fmt.Errorf("restore to %s requires root privileges", destRoot)
}
```

The `isRealRestoreFS` gate means the root check fires only against the real OS
filesystem, not against a test or in-memory FS.

### 6. Checksum Verification

**Archive integrity check** (`verifyStagedArchiveIntegrity()` / `resolveIntegrityExpectationValues()` in `decrypt_integrity.go`):

```go
// Verify checksum if available
if checksumFile exists {
    expectedChecksum := readChecksumFile(checksumFile)
    actualChecksum := calculateSHA256(archivePath)

    if expectedChecksum != actualChecksum {
        return fmt.Errorf("checksum mismatch")
    }

    logger.Info("✓ Checksum verified successfully")
} else if manifest.SHA256 != "" {
    // Use manifest checksum
    actualChecksum := calculateSHA256(archivePath)
    if manifest.SHA256 != actualChecksum {
        return fmt.Errorf("checksum mismatch")
    }
} else {
    logger.Warning("No checksum available for verification")
}
```

### 7. Interactive Confirmation Gates

**Multiple abort points**:

1. **Backup selection**: Type `0`
2. **Mode selection**: Type `0`
3. **Category selection**: Type `0`
4. **Restore plan**: Type `cancel` or `0`
5. **Safety backup failure**: Type `no`
6. **Compatibility warning**: Type `no`
7. **Any time**: Press Ctrl+C

**Confirmation Pattern** (`ConfirmRestoreOperationWithReader()` in `selective.go`):

```go
func ConfirmRestoreOperationWithReader(ctx context.Context, reader *bufio.Reader, logger *logging.Logger) (bool, error) {
    fmt.Print(`Type "RESTORE" (exact case) to proceed, or "cancel"/"0" to abort: `)

    response, _ := reader.ReadString('\n')
    response = strings.TrimSpace(response)

    if response == "RESTORE" {
        return true, nil
    }

    return false, nil
}
```

### 8. Secure Temp-Root Guard (issue #54)

Before ProxSave uses the shared workspace root `/tmp/proxsave` (for backup, decrypt, and
restore), `ensureSecureTempRoot` (`temp_registry.go`) validates it and creates it `0o700`
if missing. It **rejects** the root when it is a symlink, is not a directory, is group- or
world-writable (`perm & 0o022 != 0`), or is not owned by root or the effective uid. This
stops an attacker from pre-planting `/tmp/proxsave` as a symlink or a writable dir to
hijack restore temp files.

### 9. Registry-Backed Orphan Cleanup (issue #55)

Temp workspaces are tracked in a small registry (`TempDirRegistry`, default
`/var/run/proxsave/temp-dirs.json`, flock-guarded, atomically written). Each workspace
gets a `.proxsave-marker` file written **before** it is registered.
`CleanupOrphaned(maxAge)` removes entries whose process is gone or that are older than
`maxAge` (24h). Before deleting anything it calls `workspacePathIsRemovable`, which
returns true only for a real, non-symlink directory **strictly under** `/tmp/proxsave`
that carries the `.proxsave-marker`. An entry that fails the check is dropped from the
registry without touching the filesystem, so a poisoned registry or a hostile
`PROXMOX_TEMP_REGISTRY_PATH` can never make ProxSave delete an arbitrary path.

---

## Error Handling

### Error Types

```go
var (
    ErrRestoreAborted  = errors.New("restore aborted by user")
    ErrDecryptAborted  = errors.New("decryption aborted by user")
)
```

### Error Categories

**1. User Aborts** (Expected, Clean Exit):
```go
if errors.Is(err, ErrRestoreAborted) {
    logging.Info("Restore workflow aborted by user")
    return exitCodeInterrupted
}
```

**2. System Errors** (Unexpected, Error Exit):
```go
if err != nil {
    logging.Error("Restore workflow failed: %v", err)
    return exitCodeGenericError
}
```

**3. Partial Failures** (Warning, Continue):
```go
if filesFailed > 0 {
    logger.Warning("Restored %d files; %d failed", filesExtracted, filesFailed)
    // Continue, don't abort
}
```

### Context Cancellation

**Interrupt Handling** (`main.go`):

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Setup signal handler
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

go func() {
    <-sigCh
    logging.Info("Received interrupt signal, cancelling operations...")
    cancel()
}()
```

**Context Propagation**:
```go
func RunRestoreWorkflow(ctx context.Context, ...) error {
    // Check cancellation
    select {
    case <-ctx.Done():
        return ctx.Err()
    default:
    }

    // Pass to sub-operations
    err := extractArchiveNative(ctx, ...)
}
```

### Cleanup on Error

**Defer Pattern** ensures cleanup:

```go
func RunRestoreWorkflow(...) error {
    // Prepare backup
    prepared, err := prepareDecryptedBackup(...)
    if err != nil {
        return err
    }

    // Schedule cleanup (ALWAYS executes)
    defer prepared.Cleanup()

    // ... restore operations ...
    // Even if restore fails, cleanup executes
}
```

**Cleanup Functions**:
- Remove temporary decrypted files
- Restart services (if stopped)
- Close log files
- Remove incomplete extractions (future enhancement)

---

## Extension Guide

### Adding a New Restore Mode

**Step 1**: Define mode constant

```go
// File: internal/orchestrator/selective.go
const (
    RestoreModeFull    RestoreMode = 1
    RestoreModeStorage RestoreMode = 2
    RestoreModeBase    RestoreMode = 3
    RestoreModeCustom  RestoreMode = 4
    RestoreModeMyNew   RestoreMode = 5  // ← Add here
)
```

**Step 2**: Add to menu

```go
// File: internal/orchestrator/selective.go (the menu rendered behind RestoreWorkflowUI.SelectRestoreMode)
func ShowRestoreModeMenuWithReader(ctx context.Context, reader *bufio.Reader, logger *logging.Logger, systemType SystemType) (RestoreMode, error) {
    fmt.Println("Select restore mode:")
    fmt.Println("  [1] FULL restore")
    fmt.Println("  [2] STORAGE only")
    fmt.Println("  [3] SYSTEM BASE only")
    fmt.Println("  [4] CUSTOM selection")
    fmt.Println("  [5] MY NEW MODE")  // ← Add here
    // ...
}
```

**Step 3**: Implement category selection

```go
// File: internal/orchestrator/selective.go
func GetCategoriesForMode(mode RestoreMode, ...) []Category {
    switch mode {
    // ... existing cases ...
    case RestoreModeMyNew:
        return GetMyNewModeCategories(systemType)
    }
}

func GetMyNewModeCategories(systemType string) []Category {
    // Return list of category IDs for this mode
    return []Category{
        // ... category selection logic ...
    }
}
```

### Adding Pre/Post-Restore Hooks

**Architecture**:

```go
// File: internal/orchestrator/restore.go

type RestoreHook interface {
    PreRestore(ctx context.Context, categories []Category) error
    PostRestore(ctx context.Context, categories []Category) error
}

func RunRestoreWorkflow(..., hooks []RestoreHook) error {
    // ... selection and preparation ...

    // Call pre-restore hooks
    for _, hook := range hooks {
        if err := hook.PreRestore(ctx, selectedCategories); err != nil {
            return fmt.Errorf("pre-restore hook failed: %w", err)
        }
    }

    // ... extraction ...

    // Call post-restore hooks
    for _, hook := range hooks {
        if err := hook.PostRestore(ctx, selectedCategories); err != nil {
            logger.Warning("Post-restore hook failed: %v", err)
        }
    }
}
```

**Example Hook**:

```go
type NetworkConfigHook struct{}

func (h *NetworkConfigHook) PreRestore(ctx context.Context, categories []Category) error {
    // Check if network category is being restored
    for _, cat := range categories {
        if cat.ID == "network" {
            // Warn about network disruption
            fmt.Println("⚠ WARNING: Network configuration will be changed")
            fmt.Println("   You may lose connection during restore")
            return askConfirmation()
        }
    }
    return nil
}

func (h *NetworkConfigHook) PostRestore(ctx context.Context, categories []Category) error {
    for _, cat := range categories {
        if cat.ID == "network" {
            // Restart networking
            return exec.Command("systemctl", "restart", "networking").Run()
        }
    }
    return nil
}
```

### Custom Archive Formats

**Architecture**:

```go
// File: internal/orchestrator/restore.go

type ArchiveReader interface {
    Open(path string) error
    Next() (*ArchiveEntry, error)
    Extract(entry *ArchiveEntry, dest string) error
    Close() error
}

type ArchiveEntry struct {
    Name     string
    Size     int64
    Mode     os.FileMode
    ModTime  time.Time
    IsDir    bool
    LinkName string
}

func extractArchive(ctx context.Context, reader ArchiveReader, ...) error {
    for {
        entry, err := reader.Next()
        if err == io.EOF {
            break
        }

        // ... filtering and security checks ...

        if err := reader.Extract(entry, destPath); err != nil {
            return err
        }
    }
}
```

---

## Performance Considerations

### Archive Scanning

**Current**: Full archive scan for category analysis
```
Time Complexity: O(n) where n = number of files in archive
Space Complexity: O(n) for path list
```

**Optimization**: Stop early if all categories found
```go
func AnalyzeBackupCategories(...) ([]Category, error) {
    allFound := false
    for {
        header, err := tarReader.Next()
        // ...

        // Check if all categories now found
        if !allFound && allCategoriesFound(categories) {
            allFound = true
            break  // Stop scanning
        }
    }
}
```

### Multi-tier Extraction

The archive is opened once per tier that has selected categories (normal, export-only,
and staged), plus one more streaming pass for dedup materialization when a dedup manifest
is present. Each pass filters TAR entries by category, so a tier with no selected
categories is skipped entirely.

```
Normal tier   -> destRoot (/)                     (skipped if empty)
Export tier   -> proxmox-config-export-<ts>       (skipped if empty)
Staged tier   -> /tmp/proxsave/restore-stage-*    (skipped if empty), then applied
Dedup pass    -> streams the archive to rebuild deduplicated symlinks (issue #70)
```

Trading a few extra reads for isolation is deliberate: the staged tier is extracted
strictly and applied separately so sensitive config is never written half-applied
(BH-002), and the export tier is kept off the live system entirely.

### Memory Usage

**Current**: Stream-based extraction (low memory)
```
Memory: ~10-50 MB (TAR buffers + decompression)
```

**No need for optimization** - already efficient.

---

## Testing Strategies

### Unit Tests

**Category Matching**:
```go
func TestPathMatchesCategory(t *testing.T) {
    tests := []struct {
        path     string
        category Category
        expected bool
    }{
        {"./etc/network/interfaces", networkCategory, true},
        {"./etc/hostname", networkCategory, false},
        // ... more cases ...
    }

    for _, tt := range tests {
        result := PathMatchesCategory(tt.path, tt.category)
        if result != tt.expected {
            t.Errorf("PathMatchesCategory(%s) = %v; want %v",
                     tt.path, result, tt.expected)
        }
    }
}
```

### Integration Tests

**Full Restore Workflow**:
```bash
#!/bin/bash
# Test full restore workflow

# 1. Create test backup
proxsave

# 2. Modify system files
echo "test" > /etc/hostname

# 3. Run restore (with test responses)
echo -e "1\n1\n1\nRESTORE\n" | proxsave --restore

# 4. Verify restoration
if grep -q "original-hostname" /etc/hostname; then
    echo "✓ Restore successful"
else
    echo "✗ Restore failed"
    exit 1
fi
```

### Mocking External Dependencies

**Service Management**:
```go
type ServiceManager interface {
    Stop(service string) error
    Start(service string) error
}

type MockServiceManager struct {
    StopCalled  []string
    StartCalled []string
}

func (m *MockServiceManager) Stop(service string) error {
    m.StopCalled = append(m.StopCalled, service)
    return nil
}
```

---

## Debugging Guide

### Enable Verbose Logging

```bash
# Set log level to debug
proxsave --restore --log-level=debug
```

### Review Detailed Logs

```bash
# Restore log (name is restore_<timestamp>_<seq>.log, seq is a per-process counter)
cat /tmp/proxsave/restore_20251120_143052_1.log

# Service logs
journalctl -u pve-cluster --since "10 minutes ago"
journalctl -u pvedaemon --since "10 minutes ago"
```

### Trace Category Matching

Add debug logging:
```go
func PathMatchesCategory(filePath string, category Category) bool {
    logger.Debug("Checking %s against category %s", filePath, category.ID)
    // ... matching logic ...
    logger.Debug("  Result: %v", match)
    return match
}
```

### Inspect Archive Contents

```bash
# List archive without extracting
tar -tzf backup.tar.gz | less

# Extract specific file for inspection
tar -xzf backup.tar.gz ./etc/pve/storage.cfg -O | less
```

---

## Summary

The restore system is built on these technical foundations:

- **Modular architecture** with clear separation of concerns
- **Category-based abstraction** for flexible file selection
- **Two-pass extraction** for normal vs export-only files
- **Service lifecycle management** with defer pattern
- **Multiple safety layers** (backups, confirmations, guards)
- **Stream-based processing** for memory efficiency
- **Comprehensive error handling** with graceful degradation

**Total Implementation**:
- **~3,500 lines** across 8 core files
- **15+ categories** with 100+ file paths
- **4 restore modes** plus custom selection
- **11-phase workflow** with comprehensive logging

**Related Documentation**:
- [RESTORE_GUIDE.md](RESTORE_GUIDE.md) - Complete user guide
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md) - Visual workflow diagrams
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md) - Disaster recovery procedures
