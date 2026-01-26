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
│ decrypt.go │categories│restore.go│backup_safety │
│            │     .go  │selective │      .go     │
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
File Extraction
  ├─ Normal categories → /
  ├─ Export categories → export dir
  └─ Log all operations
  ↓
Post-Restore Tasks
  ├─ Recreate directories
  ├─ Check ZFS pools
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
| `internal/orchestrator/restore.go` | Main orchestration | `RunRestoreWorkflow()` |
| `internal/orchestrator/categories.go` | Category definitions | `AllCategories()`, `PathMatchesCategory()` |
| `internal/orchestrator/selective.go` | Category selection UI | `SelectRestoreMode()`, `ShowRestorePlan()` |
| `internal/orchestrator/decrypt.go` | Decryption workflow | `prepareDecryptedBackup()` |
| `internal/orchestrator/compatibility.go` | System validation | `ValidateCompatibility()` |
| `internal/orchestrator/backup_safety.go` | Safety backups | `CreateSafetyBackup()` |
| `internal/orchestrator/directory_recreation.go` | Storage setup | `RecreateDirectoriesFromConfig()` |

### File: cmd/proxsave/main.go

**Lines 562-578**: Entry point for restore flag

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

### File: internal/orchestrator/restore.go

**Main function**: `RunRestoreWorkflow()` (Lines 26-241)

**Signature**:
```go
func RunRestoreWorkflow(
    ctx context.Context,
    cfg *config.Config,
    logger *logging.Logger,
    version string,
) error
```

**Key Sections**:

1. **Preparation** (Lines 28-66):
   - Decrypt backup if needed
   - Detect system type
   - Validate compatibility
   - Analyze categories

2. **Mode & Category Selection** (Lines 68-91):
   - User selects restore mode (Full/Storage/Base/Custom)
   - Interactive category selection for Custom mode
   - Build category list

3. **Cluster SAFE/RECOVERY Prompt** (Lines 93-116):
   - Detect if backup is from cluster node (`manifest.ClusterMode`)
   - Prompt user: SAFE (export+API) vs RECOVERY (full restore)
   - Redirect pve_cluster to export-only if SAFE mode selected
   - `promptClusterRestoreMode()` function

4. **Category Split & Plan** (Lines 118-137):
   - Split normal vs export-only categories
   - `splitExportCategories()`, `redirectClusterCategoryToExport()`
   - Show restore plan and confirm

5. **Safety Backup** (Lines 139-156):
   - Backup files to be overwritten
   - Handle backup failures

6. **PVE Service Management** (Lines 158-179):
   - Detect cluster restore need (RECOVERY mode)
   - Stop PVE services: pve-cluster, pvedaemon, pveproxy, pvestatd
   - Unmount /etc/pve
   - Defer restart

7. **PBS Service Management** (Lines 181-204):
   - Detect PBS-specific category restore need
   - Stop PBS services: proxmox-backup-proxy, proxmox-backup
   - Prompt to continue if stop fails
   - Defer restart

8. **File Extraction** (Lines 206-239):
   - Extract normal categories to /
   - Extract export categories to timestamped directory
   - Handle extraction errors

9. **pvesh SAFE Apply** (Lines 241-248):
   - If SAFE cluster mode selected
   - `runSafeClusterApply()` function
   - Apply VM/CT configs, storage.cfg, datacenter.cfg via API

10. **Post-Restore** (Lines 250-303):
    - Recreate storage/datastore directories
    - Check ZFS pools (PBS only)
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

1. **`AllCategories()`** (Lines 16-162):
   - Returns complete list of 15+ categories
   - Hardcoded category definitions
   - Each category includes ID, name, description, paths

2. **`PathMatchesCategory()`** (Lines 263-292):
   - Check if archive path belongs to category
   - Handles exact matches and directory prefixes
   - Path normalization

3. **`GetCategoriesForMode()`** (Lines 283-316):
   - Return categories for restore mode
   - Filters export-only categories
   - Mode-specific category lists

4. **`GetStorageModeCategories()`** (Lines 322-344):
   - PVE: cluster, storage, jobs, zfs
   - PBS: config, datastore, jobs, zfs

5. **`GetBaseModeCategories()`** (Lines 346-359):
   - Common categories only
   - Network, SSL, SSH, services

**SSH Category Coverage**

- `./etc/ssh/` → sshd configuration, host keys, authorized_keys
- `./root/.ssh/` → root private/public keys and known_hosts
  (these are the paths matched by the `ssh` category during restore)

### File: internal/orchestrator/selective.go

**Purpose**: Interactive category selection UI

**Key Functions**:

1. **`SelectRestoreMode()`** (Lines 124-167):
   - Display mode menu
   - Get user selection
   - Return RestoreMode enum

2. **`SelectCategoriesInteractive()`** (Lines 169-281):
   - Display checkbox menu
   - Toggle category selection
   - Commands: number, 'a', 'n', 'c', '0'

3. **`ShowRestorePlan()`** (Lines 336-391):
   - Display selected categories
   - Show file paths to be restored
   - Display warnings

4. **`ConfirmRestorePlan()`** (Lines 393-417):
   - User must type "RESTORE"
   - Case-sensitive
   - Returns error if not confirmed

### File: internal/orchestrator/decrypt.go

**Purpose**: Handle backup decryption

**Key Functions**:

1. **`prepareDecryptedBackup()`** (Lines 484-496):
   - Entry point for decryption workflow
   - Delegates to selection and decryption

2. **`SelectAndPrepareBackup()`** (Lines 166-203):
   - Display configured paths
   - User selects location
   - Scans for backups

3. **`DiscoverBackups()`** (Lines 234-308):
   - Find .bundle.tar files
   - Parse manifests
   - Sort by creation date

4. **`SelectSpecificBackup()`** (Lines 344-377):
   - Display backup list with metadata
   - User selects by number

5. **`DecryptIfNeeded()`** (Lines 399-482):
   - Check encryption status
   - Prompt for key/passphrase
   - Decrypt to /tmp
   - Verify checksum

---

## Execution Flow

### Detailed Phase Breakdown

#### Phase 1: Initialization

**File**: `cmd/proxsave/main.go:562-578`

```go
if args.Restore {
    // Call orchestrator
    err := orchestrator.RunRestoreWorkflow(ctx, cfg, logger, version)
}
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

**File**: `internal/orchestrator/restore.go:28-55`

```go
prepared, err := prepareDecryptedBackup(ctx, cfg, logger)
if err != nil {
    return err
}
// cleanup deferred
defer func() {
    if prepared.CleanupFunc != nil {
        prepared.CleanupFunc()
    }
}()
```

**Sub-phases**:

1. **Path Selection** (`decrypt.go:166-203`):
   ```go
   Select backup source:
     [1] Primary: /opt/proxsave/backup
     [2] Secondary: /mnt/secondary/backups
     [3] Cloud: /mnt/cloud-backups
   ```

2. **Backup Discovery** (`decrypt.go:234-308`):
   - Scan for `.bundle.tar` files
   - Parse JSON manifests
   - Extract metadata (date, encryption, version)

3. **Backup Selection** (`decrypt.go:344-377`):
   - Display sorted list (newest first)
   - User selects by index

4. **Decryption** (`decrypt.go:399-482`):
   - Check if encrypted (manifest)
   - Prompt for AGE key/passphrase
   - Decrypt to `/tmp/proxsave/proxmox-decrypt-<random>/`
   - Verify SHA256 checksum

**Data Structure**:
```go
type PreparedBackup struct {
    ArchivePath  string        // Path to plaintext archive
    Manifest     *Manifest     // Parsed metadata
    CleanupFunc  func()        // Cleanup temporary files
}
```

---

#### Phase 3: System Detection & Compatibility

**File**: `internal/orchestrator/restore.go:58-72`

```go
systemType := DetectSystemType(logger)
logger.Info("Current system type: %s", systemType)

if err := ValidateCompatibility(systemType, prepared.Manifest, reader); err != nil {
    logger.Warning("Compatibility check: %v", err)
    // Prompt user to continue or abort
}
```

**System Detection** (`compatibility.go:21-33`):
```go
func DetectSystemType(logger *logging.Logger) SystemType {
    // Check for PVE indicators
    if _, err := os.Stat("/etc/pve"); err == nil {
        if _, err := os.Stat("/usr/bin/qm"); err == nil {
            return SystemTypePVE
        }
    }

    // Check for PBS indicators
    if _, err := os.Stat("/etc/proxmox-backup"); err == nil {
        if _, err := os.Stat("/usr/sbin/proxmox-backup-proxy"); err == nil {
            return SystemTypePBS
        }
    }

    return SystemTypeUnknown
}
```

**Compatibility Check** (`compatibility.go:67-97`):
```go
func ValidateCompatibility(
    systemType SystemType,
    manifest *Manifest,
    reader *bufio.Reader,
) error {
    backupType := DetermineBackupSystemType(manifest)

    if systemType != SystemTypeUnknown &&
       backupType != SystemTypeUnknown &&
       systemType != backupType {
        // Prompt user: Type "yes" to continue
        if !getUserConfirmation(reader, "yes") {
            return ErrRestoreAborted
        }
    }
    return nil
}
```

---

#### Phase 4: Category Analysis

**File**: `internal/orchestrator/restore.go:75-89`

```go
availableCategories, err := AnalyzeBackupCategories(
    prepared.ArchivePath,
    logger,
)
```

**Implementation** (`selective.go:24-89`):

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
    categories := AllCategories()
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

**Path Matching** (`categories.go:263-292`):
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

**File**: `internal/orchestrator/restore.go:93-116`

```go
// Split categories
normalCategories, exportCategories := splitExportCategories(selectedCategories)

// Select restore mode
mode, err := SelectRestoreMode(systemType)

// Get categories for mode (or custom selection)
selectedCategories, err := GetCategoriesForModeOrCustom(
    mode, systemType, availableCategories,
)

// Show restore plan
ShowRestorePlan(selectedCategories, systemType, mode)

// Confirm
if err := ConfirmRestorePlan(); err != nil {
    return ErrRestoreAborted
}
```

**Mode Selection UI** (`selective.go:124-167`):
```
Select restore mode:
  [1] FULL restore - Restore everything from backup
  [2] STORAGE only - Cluster/storage + jobs
  [3] SYSTEM BASE only - Network + SSL + SSH + services
  [4] CUSTOM selection - Choose specific categories
  [0] Cancel

Your selection: _
```

**Custom Selection UI** (`selective.go:169-281`):
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

**File**: `internal/orchestrator/restore.go:117-134`

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

**Implementation** (`backup_safety.go:24-104`):

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

**File**: `internal/orchestrator/restore.go:136-155`

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

**Stop Services** (`restore.go:308-321`):
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

**Start Services** (`restore.go:323-336`):
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

**Unmount** (`restore.go:338-356`):
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

**Two-Pass Extraction**:

**Pass 1: Normal Categories** (`restore.go:157-172`):
```go
if len(normalCategories) > 0 {
    destRoot := "/"
    logPath, err := extractSelectiveArchive(
        ctx,
        prepared.ArchivePath,
        destRoot,
        normalCategories,
        mode,
        logger,
    )
    if err != nil {
        logger.Warning("Restore completed with errors: %v", err)
    }
}
```

**Pass 2: Export Categories** (`restore.go:174-189`):
```go
if len(exportCategories) > 0 {
    exportRoot := exportDestRoot(cfg.BaseDir)
    logger.Info("Exporting /etc/pve contents to: %s", exportRoot)

    os.MkdirAll(exportRoot, 0o755)

    exportLog, err := extractSelectiveArchive(
        ctx,
        prepared.ArchivePath,
        exportRoot,
        exportCategories,
        RestoreModeCustom,
        logger,
    )
}
```

**Extraction Implementation** (`restore.go:582-618`):
```go
func extractSelectiveArchive(
    ctx context.Context,
    archivePath string,
    destRoot string,
    categories []Category,
    mode RestoreMode,
    logger *logging.Logger,
) (string, error) {
    // Create log file
    logPath := filepath.Join(
        "/tmp/proxsave",
        fmt.Sprintf("restore_%s.log", time.Now().Format("20060102_150405")),
    )
    logFile, _ := os.Create(logPath)
    defer logFile.Close()

    // Call native extraction
    err := extractArchiveNative(
        ctx,
        archivePath,
        destRoot,
        logger,
        categories,
        mode,
        logFile,
        logPath,
        nil, // skipFn (optional)
    )

    return logPath, err
}
```

---

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

**File**: `internal/orchestrator/categories.go:263-292`

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
| `./etc/network/interfaces` | `./etc/network/` | ✅ | Prefix match |
| `./etc/network/interfaces` | `./etc/network/interfaces` | ✅ | Exact match |
| `./etc/hostname` | `./etc/hostname` | ✅ | Exact match |
| `./etc/hostname` | `./etc/network/` | ❌ | No match |
| `./var/lib/pve-cluster/config.db` | `./var/lib/pve-cluster/` | ✅ | Prefix match |
| `etc/network/interfaces` | `./etc/network/` | ✅ | Normalized to `./` |

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

**ClusterMode is set during backup** in `bash.go`:
```go
if stats.IsPVEClusterNode {
    stats.ClusterMode = "cluster"
} else {
    stats.ClusterMode = "standalone"
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

**Decompression** (`restore.go:786-804`):

```go
func createDecompressionReader(file *os.File, archivePath string) (io.Reader, error) {
    switch {
    case strings.HasSuffix(archivePath, ".tar.gz"),
         strings.HasSuffix(archivePath, ".tgz"):
        return gzip.NewReader(file)  // Native Go

    case strings.HasSuffix(archivePath, ".tar.xz"):
        return createXZReader(file)  // External: xz command

    case strings.HasSuffix(archivePath, ".tar.zst"),
         strings.HasSuffix(archivePath, ".tar.zstd"):
        return createZstdReader(file)  // External: zstd command

    case strings.HasSuffix(archivePath, ".tar.bz2"):
        return createBzip2Reader(file)  // External: bzip2 command

    case strings.HasSuffix(archivePath, ".tar.lzma"):
        return createLzmaReader(file)  // External: lzma command

    case strings.HasSuffix(archivePath, ".tar"):
        return file, nil  // No decompression

    default:
        return nil, fmt.Errorf("unsupported format: %s", archivePath)
    }
}
```

### Selective Extraction Logic

**File**: `internal/orchestrator/restore.go:622-784`

```go
func extractArchiveNative(
    ctx context.Context,
    archivePath string,
    destRoot string,
    logger *logging.Logger,
    categories []Category,
    mode RestoreMode,
    logFile *os.File,
    logFilePath string,
    skipFn func(entryName string) bool,
) error {
    // 1. Open archive with decompression
    file, _ := os.Open(archivePath)
    reader, _ := createDecompressionReader(file, archivePath)
    tarReader := tar.NewReader(reader)

    // 2. Iterate through TAR entries
    for {
        header, err := tarReader.Next()
        if err == io.EOF {
            break
        }

        // 3. Category filtering (if selective mode)
        if selectiveMode {
            shouldExtract := false
            for _, cat := range categories {
                if PathMatchesCategory(header.Name, cat) {
                    shouldExtract = true
                    break
                }
            }

            if !shouldExtract {
                filesSkipped++
                continue
            }
        }

        // 4. Security checks
        target := filepath.Join(destRoot, header.Name)
        if !isSecurePath(target, destRoot) {
            return fmt.Errorf("illegal path: %s", header.Name)
        }

        // 5. /etc/pve hard guard
        if destRoot == "/" && strings.HasPrefix(target, "/etc/pve") {
            logger.Warning("Skipping %s (writes to /etc/pve prohibited)", target)
            continue
        }

        // 6. Extract based on type
        switch header.Typeflag {
        case tar.TypeDir:
            extractDirectory(target, header, logger)
        case tar.TypeReg:
            extractRegularFile(tarReader, target, header, logger)
        case tar.TypeSymlink:
            extractSymlink(target, header, logger)
        case tar.TypeLink:
            extractHardlink(target, header, logger)
        }

        filesExtracted++
    }

    return nil
}
```

### File Type Handling

**Directories** (`restore.go:906-927`):
```go
func extractDirectory(target string, header *tar.Header, logger *logging.Logger) error {
    // Create directory
    os.MkdirAll(target, os.FileMode(header.Mode))

    // Set ownership
    os.Chown(target, header.Uid, header.Gid)

    // Set permissions
    os.Chmod(target, os.FileMode(header.Mode))

    // Set timestamps
    setTimestamps(target, header)

    return nil
}
```

**Regular Files** (`restore.go:930-967`):
```go
func extractRegularFile(
    tarReader *tar.Reader,
    target string,
    header *tar.Header,
    logger *logging.Logger,
) error {
    // Ensure parent directory exists
    os.MkdirAll(filepath.Dir(target), 0755)

    // Create file
    outFile, _ := os.Create(target)
    defer outFile.Close()

    // Copy content
    io.Copy(outFile, tarReader)

    // Set ownership
    os.Chown(target, header.Uid, header.Gid)

    // Set permissions
    os.Chmod(target, os.FileMode(header.Mode))

    // Set timestamps
    setTimestamps(target, header)

    return nil
}
```

**Symlinks** (`restore.go:970-989`):
```go
func extractSymlink(target string, header *tar.Header, logger *logging.Logger) error {
    // Ensure parent directory
    os.MkdirAll(filepath.Dir(target), 0755)

    // Remove existing
    os.Remove(target)

    // Create symlink
    os.Symlink(header.Linkname, target)

    // Set ownership (use Lchown to not follow symlink)
    syscall.Lchown(target, header.Uid, header.Gid)

    return nil
}
```

**Hard Links** (`restore.go:992-1002`):
```go
func extractHardlink(target string, header *tar.Header, logger *logging.Logger) error {
    // Ensure parent directory
    os.MkdirAll(filepath.Dir(target), 0755)

    // Resolve link target
    linkTarget := filepath.Join(filepath.Dir(target), header.Linkname)

    // Create hard link
    os.Link(linkTarget, target)

    return nil
}
```

### Timestamp Preservation

**File**: `internal/orchestrator/restore.go:1004-1025`

```go
func setTimestamps(target string, header *tar.Header) error {
    // Extract times from header
    atime := header.AccessTime
    mtime := header.ModTime

    // Use syscall for precise control
    return syscall.UtimesNano(target, []syscall.Timespec{
        {Sec: atime.Unix(), Nsec: int64(atime.Nanosecond())},
        {Sec: mtime.Unix(), Nsec: int64(mtime.Nanosecond())},
    })
}
```

**Note**: `ctime` (change time) cannot be set by userspace - it's kernel-managed.

---

## Safety Mechanisms

### 1. Path Traversal Prevention

**Security Check** (`restore.go:869-878`):

```go
func isSecurePath(target string, destRoot string) bool {
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
| `/var/lib/pve-cluster/config.db` | `/` | ✅ |
| `/../etc/passwd` | `/` | ❌ |
| `/tmp/../etc/passwd` | `/` | ❌ |
| `/opt/backup/file` | `/opt/backup` | ✅ |

### 2. /etc/pve Hard Guard

**Absolute Block** (`restore.go:880-884`):

```go
if cleanDestRoot == string(os.PathSeparator) &&
   strings.HasPrefix(target, "/etc/pve") {
    logger.Warning("Skipping restore to %s (prohibited)", target)
    return nil  // Skip, don't error
}
```

**Applies only when**:
- Restoring to system root (`/`)
- Target path is under `/etc/pve`

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

**Normalization**:
- Entries written by the merge are normalized to include `nofail` (and `_netdev` for network mounts) to prevent offline storage from blocking boot/restore.

### 4. PBS Datastore Mount Guards (Offline Storage)

For PBS datastores whose paths live under typical mount roots (for example `/mnt/...`), ProxSave aims for a “restore even if offline” behavior:

- PBS datastore definitions are applied even when the underlying storage is offline/not mounted, so PBS shows them as **unavailable** rather than silently dropping them.
- When a mountpoint used by a datastore currently resolves to the root filesystem (mount missing), ProxSave applies a **temporary mount guard** on the mount root:
  - Preferred: read-only bind-mount guard
  - Fallback: `chattr +i` on the mountpoint directory
- Guards prevent PBS from writing into `/` if the storage is missing at restore time. When the real storage is mounted later, it overlays the guard and the datastore becomes available again.

Optional maintenance:
- `proxsave --cleanup-guards` removes guard bind mounts and the guard directory when they are still visible on mountpoints.

#### PVE Storage Mount Guards (Offline Storage)

For PVE storages that use mountpoints (notably `nfs`, `cifs`, `cephfs`, `glusterfs`, and `dir` storages on dedicated mountpoints), ProxSave applies the same “restore even if offline” safety model:

- Network storages use `/mnt/pve/<storageid>`. ProxSave attempts `pvesm activate <storageid>` with a short timeout.
- If the mountpoint still resolves to the root filesystem afterwards (mount missing/offline), ProxSave applies a **temporary mount guard** on the mountpoint:
  - Preferred: read-only bind-mount guard
  - Fallback: `chattr +i` on the mountpoint directory
- For `dir` storages, guards are only applied when the storage `path` can be associated with a mountpoint present in `/etc/fstab` (to avoid guarding local root filesystem paths).

This prevents accidental writes into the root filesystem when storage is offline at restore time. When the real mount comes back, it overlays the guard and normal operation resumes.

### 5. Root Privilege Check

**Pre-Extraction Check** (`restore.go:568-570`, `588-590`):

```go
// For system path restoration
if destRoot == "/" && os.Geteuid() != 0 {
    return fmt.Errorf("restore to %s requires root privileges", destRoot)
}
```

### 6. Checksum Verification

**After Decryption** (`decrypt.go:272-289`):

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

**Confirmation Pattern** (`selective.go:393-417`):

```go
func ConfirmRestorePlan(reader *bufio.Reader) error {
    fmt.Print(`Type "RESTORE" (exact case) to proceed, or "cancel"/"0" to abort: `)

    response, _ := reader.ReadString('\n')
    response = strings.TrimSpace(response)

    if response == "RESTORE" {
        return nil
    }

    return ErrRestoreAborted
}
```

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
    defer func() {
        if prepared.CleanupFunc != nil {
            prepared.CleanupFunc()
        }
    }()

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
// File: internal/orchestrator/selective.go
func SelectRestoreMode(systemType SystemType) (RestoreMode, error) {
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
// File: internal/orchestrator/categories.go
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

### Two-Pass Extraction

**Current**: Archive read twice if export-only categories exist
```
Pass 1: Normal categories
Pass 2: Export-only categories
```

**Optimization**: Single-pass with dual writers
```go
func extractArchiveSinglePass(...) error {
    normalWriter := createTarWriter("/")
    exportWriter := createTarWriter(exportDir)

    for {
        header, _ := tarReader.Next()

        if isExportOnly(header) {
            exportWriter.Write(header, content)
        } else {
            normalWriter.Write(header, content)
        }
    }
}
```

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
./build/proxsave

# 2. Modify system files
echo "test" > /etc/hostname

# 3. Run restore (with test responses)
echo -e "1\n1\n1\nRESTORE\n" | ./build/proxsave --restore

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
./build/proxsave --restore --log-level=debug
```

### Review Detailed Logs

```bash
# Restore log
cat /tmp/proxsave/restore_20251120_143052.log

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
