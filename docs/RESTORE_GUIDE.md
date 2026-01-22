# Proxsave - Restore Guide

Complete guide for restoring Proxmox VE and Proxmox Backup Server configurations using the interactive restore workflow.

## Table of Contents

- [Quick Start](#quick-start)
- [Overview](#overview)
- [Category System](#category-system)
- [Restore Modes](#restore-modes)
- [Complete Workflow](#complete-workflow)
- [Cluster Database Restore](#cluster-database-restore)
- [Export-Only Categories](#export-only-categories)
- [VM/CT Configuration Restore](#vmct-configuration-restore)
- [Safety Features](#safety-features)
- [Troubleshooting](#troubleshooting)
- [FAQ](#faq)

---

## Quick Start

### Basic Restore

```bash
# Run the interactive restore workflow
./build/proxsave --restore

# Follow the prompts:
# 1. Select backup source location
# 2. Choose specific backup from list
# 3. Enter decryption passphrase (if encrypted)
# 4. Select restore mode
# 5. Review restore plan
# 6. Type "RESTORE" to confirm
# 7. Wait for completion
# 8. Verify services and cluster status
```

### Requirements

- **Root privileges**: Required for system path restoration
- **Sufficient disk space**: For decryption and safety backups
- **Service availability**: Target system services must be accessible
- **Network isolation**: For cluster restores, node should be isolated

---

## Overview

The `--restore` command provides an **interactive, category-based restoration system** that allows selective or full restoration of Proxmox configuration files from backup archives.

### Key Features

- **Category-based selection**: Granular control over what gets restored
- **4 restore modes**: Full, Storage, Base, or Custom selection
- **Safety backups**: Automatic backup before any changes
- **Encryption support**: AGE encryption with passphrase or key
- **Cluster-aware**: Special handling for PVE cluster database
- **Export-only protection**: Critical paths protected from direct writes
- **Comprehensive logging**: Detailed audit trail of all operations

### What Gets Restored

- System configurations (network, SSH, SSL, services)
- Proxmox-specific configs (cluster, storage, datastores)
- Custom scripts and cron jobs
- ZFS configurations and pool cache
- Backup jobs and scheduled tasks

### What Does NOT Get Restored

- VM/CT disk images (use Proxmox native tools)
- Application data (databases, user data)
- System packages (use apt/dpkg)
- Active cluster filesystem (`/etc/pve` - export-only)

---

## Category System

Restore operations are organized into **15+ categories** that group related configuration files.

### PVE-Specific Categories (6 categories)

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `pve_config_export` | PVE Config Export | **Export-only** copy of /etc/pve (never written to system) | `./etc/pve/` |
| `pve_cluster` | PVE Cluster Configuration | Cluster configuration and database | `./var/lib/pve-cluster/` |
| `storage_pve` | PVE Storage Configuration | Storage definitions | `./etc/vzdump.conf` |
| `pve_jobs` | PVE Backup Jobs | Scheduled backup jobs | `./etc/pve/jobs.cfg`<br>`./etc/pve/vzdump.cron` |
| `corosync` | Corosync Configuration | Cluster communication settings | `./etc/corosync/` |
| `ceph` | Ceph Configuration | Ceph storage cluster config | `./etc/ceph/` |

### PBS-Specific Categories (3 categories)

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `pbs_config` | PBS Configuration | Main PBS configuration | `./etc/proxmox-backup/` |
| `datastore_pbs` | PBS Datastore Configuration | Datastore definitions | `./etc/proxmox-backup/datastore.cfg` |
| `pbs_jobs` | PBS Jobs | Sync, verify, prune jobs | `./etc/proxmox-backup/sync.cfg`<br>`./etc/proxmox-backup/verification.cfg`<br>`./etc/proxmox-backup/prune.cfg` |

### Common Categories (7 categories)

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `network` | Network Configuration | Network interfaces and routing | `./etc/network/`<br>`./etc/hosts`<br>`./etc/hostname`<br>`./etc/resolv.conf`<br>`./etc/cloud/cloud.cfg.d/99-disable-network-config.cfg`<br>`./etc/dnsmasq.d/lxc-vmbr1.conf` |
| `ssl` | SSL Certificates | SSL/TLS certificates and keys | `./etc/proxmox-backup/proxy.pem` |
| `ssh` | SSH Configuration | SSH keys and authorized_keys | `./root/.ssh/`<br>`./etc/ssh/` |
| `scripts` | Custom Scripts | User scripts and tools | `./usr/local/bin/`<br>`./usr/local/sbin/` |
| `crontabs` | Scheduled Tasks | Cron jobs and systemd timers | `./etc/cron.d/`<br>`./etc/crontab`<br>`./var/spool/cron/` |
| `services` | System Services | Systemd service configs and udev rules | `./etc/systemd/system/`<br>`./etc/default/`<br>`./etc/udev/rules.d/` |
| `zfs` | ZFS Configuration | ZFS pool cache and configs | `./etc/zfs/`<br>`./etc/hostid` |

### Category Availability

Not all categories are available in every backup. The restore workflow:
1. Analyzes the backup archive
2. Detects which categories contain files
3. Displays only available categories for selection

---

## Restore Modes

Four predefined modes provide common restoration scenarios, plus custom selection for advanced users.

### 1. FULL Restore

**Description**: Restore everything from backup, including export-only categories (they are exported to a safe directory instead of being written directly)

**Use Cases**:
- Complete disaster recovery
- Migrating to new hardware
- Restoring after system failure

**Categories Included**: All available categories (export-only categories such as `pve_config_export` are extracted to the export directory for manual application)

**Command Flow**:
```
Select restore mode:
  [1] FULL restore ← Select this
```

---

### 2. STORAGE Only

**Description**: Restore cluster/storage configuration and scheduled jobs

**Use Cases**:
- Recovering storage definitions
- Restoring backup job schedules
- Fixing broken cluster database

**PVE Categories**:
- `pve_cluster` - Cluster configuration
- `storage_pve` - Storage definitions
- `pve_jobs` - Backup jobs
- `zfs` - ZFS configuration

**PBS Categories**:
- `pbs_config` - PBS configuration
- `datastore_pbs` - Datastore definitions
- `pbs_jobs` - Sync/verify/prune jobs
- `zfs` - ZFS configuration

**Command Flow**:
```
Select restore mode:
  [2] STORAGE only ← Select this
```

---

### 3. SYSTEM BASE Only

**Description**: Restore core system configurations (network, SSH, SSL, services and udev rules)

**Use Cases**:
- Restoring network configuration after manual changes
- Recovering SSH access
- Fixing SSL certificate issues
- Restoring systemd service customizations

**Categories Included**:
- `network` - Network interfaces, hostname, routing
- `ssl` - SSL/TLS certificates
- `ssh` - SSH daemon configuration (`/etc/ssh`) and SSH keys (root/home users)
- `services` - Systemd service configs and udev rules

**Command Flow**:
```
Select restore mode:
  [3] SYSTEM BASE only ← Select this
```

---

### 4. CUSTOM Selection

**Description**: Choose specific categories interactively

**Use Cases**:
- Selective restoration of specific components
- Restoring only network configuration
- Recovering only backup jobs
- Including export-only categories

**Interactive Menu**:
```
Available categories:
  [1] [ ] PVE Cluster Configuration
      Proxmox VE cluster configuration and database
  [2] [ ] Network Configuration
      Network interfaces and routing
  [3] [ ] SSL Certificates
      SSL/TLS certificates and keys
  ...

Commands:
  - Type number to toggle category
  - Type 'a' to select all
  - Type 'n' to deselect all
  - Type 'c' to continue
  - Type '0' to cancel
```

**Toggle Selection**:
```
Your selection: 1      # Toggle category 1
Your selection: 2      # Toggle category 2
Your selection: c      # Continue to restore plan
```

---

## Complete Workflow

The restore process follows a **14-phase workflow** with safety checks at each step.

### Workflow Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                    RESTORE WORKFLOW                             │
└─────────────────────────────────────────────────────────────────┘

Phase 1: Backup Selection
  ├─ Display configured paths (local/secondary/cloud)
  ├─ User selects search location
  ├─ Scan for .bundle.tar and raw archives
  └─ User selects specific backup

Phase 2: Decryption (if needed)
  ├─ Detect encryption (AGE)
  ├─ Prompt for key/passphrase
  ├─ Decrypt to /tmp/proxsave/
  └─ Verify SHA256 checksum

Phase 3: Compatibility Check
  ├─ Detect current system type (PVE/PBS/Unknown)
  ├─ Read backup type from manifest
  ├─ Validate compatibility
  └─ Warn if mismatch, require confirmation

Phase 4: Category Analysis
  ├─ Open and scan archive
  ├─ Check each category for file presence
  └─ Mark categories as available/unavailable

Phase 5: Mode Selection & Category Choice
  ├─ Display restore mode menu
  ├─ User selects mode (Full/Storage/Base/Custom)
  ├─ If Custom: Interactive category selection
  └─ Build final category list

Phase 6: Cluster Restore Mode (PVE Cluster Backups Only)
  ├─ SKIPPED if backup is from standalone node
  ├─ Detect if backup ClusterMode = "cluster"
  ├─ Prompt: SAFE (export+API) vs RECOVERY (full restore)
  ├─ SAFE: Redirect pve_cluster to export-only, apply via pvesh
  └─ RECOVERY: Proceed with direct database restore

Phase 7: Restore Plan & Confirmation
  ├─ Display detailed restore plan
  ├─ Show categories and file paths
  ├─ Show warnings
  └─ User types "RESTORE" to confirm

Phase 8: Safety Backup
  ├─ Create /tmp backup of files to be overwritten
  ├─ Preserve permissions, ownership, timestamps
  └─ Display rollback command

Phase 9: Service Management (PVE Cluster Restore)
  ├─ Detect if pve_cluster category selected (RECOVERY mode)
  ├─ Stop: pve-cluster, pvedaemon, pveproxy, pvestatd
  ├─ Unmount /etc/pve
  └─ Defer restart for after restore

Phase 10: Service Management (PBS Restore)
  ├─ Detect if PBS-specific categories selected
  ├─ Stop: proxmox-backup-proxy, proxmox-backup
  ├─ Prompt to continue if stop fails
  └─ Defer restart for after restore

Phase 11: File Extraction
  ├─ Extract normal categories to /
  ├─ Selective extraction based on category paths
  ├─ Preserve ownership, permissions, timestamps
  └─ Log all operations

Phase 12: Export-Only Extraction
  ├─ Extract export-only categories to timestamped directory
  ├─ Destination: <BASE_DIR>/proxmox-config-export-YYYYMMDD-HHMMSS/
  └─ Separate detailed log

Phase 13: pvesh SAFE Apply (Cluster SAFE Mode Only)
  ├─ Scan exported VM/CT configs
  ├─ Offer to apply via pvesh API
  ├─ Offer to apply storage.cfg via pvesh
  └─ Offer to apply datacenter.cfg via pvesh

Phase 14: Post-Restore Tasks
  ├─ Optional: Apply restored network config with rollback timer (requires COMMIT)
  ├─ Recreate storage/datastore directories
  ├─ Check ZFS pool status (PBS only)
  ├─ Restart PVE/PBS services (if stopped)
  └─ Display completion summary
```

### Phase-by-Phase Details

#### Phase 1: Backup Selection

**Interactive prompts**:
```
Select backup source:
  [1] Primary backup path: /opt/proxsave/backup
  [2] Secondary backup path: /mnt/secondary/backups
  [3] Cloud/local path: /mnt/cloud-backups
  [0] Cancel
```

**Backup list display**:
```
Available backups:
  [1] pve01-backup-20251120-143052.tar.xz.bundle.tar
      Created: 2025-11-20 14:30:52
      Encrypted: Yes (AGE)
      Tool Version: v1.2.0
      System: Proxmox Virtual Environment (PVE)

  [2] backup-pve01-20251119-020015.bundle.tar
      Created: 2025-11-19 02:00:15
      Encrypted: Yes (AGE)
      Tool Version: v1.2.0
      System: Proxmox Virtual Environment (PVE)
```

#### Phase 2: Decryption

**For AGE-encrypted backups**:
```
Backup is encrypted with AGE.

Decryption options:
  [1] Use AGE passphrase
  [2] Use AGE identity file (key)
  [0] Cancel

Enter AGE passphrase: ********
Decrypting... (this may take several minutes)
Decryption complete.
Verifying SHA256 checksum...
Checksum verified successfully.
```

#### Phase 3: Compatibility Check

**System detection**:
```
Current system type: Proxmox Virtual Environment (PVE)
Backup system type: Proxmox Virtual Environment (PVE)
✓ Systems are compatible
```

**Incompatibility warning**:
```
⚠ WARNING: Potential incompatibility detected!

Current system: Proxmox Backup Server (PBS)
Backup source: Proxmox Virtual Environment (PVE)

This backup may contain PVE-specific configurations that are not
compatible with PBS. Proceeding may result in system instability.

Type "yes" to continue anyway or "no" to abort:
```

#### Phase 6: Cluster Restore Mode (PVE Cluster Backups Only)

**This phase is SKIPPED for standalone backups** - the workflow proceeds directly to Phase 7.

When the backup was created from a **cluster node** (manifest `ClusterMode = "cluster"`) and `pve_cluster` category is selected:

```
Cluster backup detected. Choose how to restore the cluster database:
  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).
  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.
  [0] Exit

Choice: _
```

See [Cluster Restore Modes](#cluster-restore-modes-safe-vs-recovery) for detailed explanation.

#### Phase 7: Restore Plan

**Example display**:
```
═══════════════════════════════════════════════════════════════
RESTORE PLAN
═══════════════════════════════════════════════════════════════

Restore mode: STORAGE only (4 categories)
System type:  Proxmox Virtual Environment (PVE)

Categories to restore:
  1. PVE Cluster Configuration
     Proxmox VE cluster configuration and database
  2. PVE Storage Configuration
     Storage definitions and backup jobs
  3. PVE Backup Jobs
     Scheduled backup jobs
  4. ZFS Configuration
     ZFS pool cache and configs

Files/directories that will be restored:
  • /var/lib/pve-cluster/
  • /etc/vzdump.conf
  • /etc/pve/jobs.cfg
  • /etc/pve/vzdump.cron
  • /etc/zfs/
  • /etc/hostid

⚠ WARNING:
  • Existing files at these locations will be OVERWRITTEN
  • A safety backup will be created before restoration
  • Services may need to be restarted after restoration
  • PVE cluster services will be stopped during restore

Type "RESTORE" (exact case) to proceed, or "cancel"/"0" to abort:
```

#### Phase 8: Safety Backup

```
Creating safety backup of existing files...
Safety backup created successfully.
Safety backup location: /tmp/proxsave/restore_backup_20251120_143052.tar.gz

You can restore from this backup if needed using:
  tar -xzf /tmp/proxsave/restore_backup_20251120_143052.tar.gz -C /
```

#### Phase 9: Service Management (PVE Cluster)

**For cluster database restore (RECOVERY mode)**:
```
Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve

Stopping pve-cluster...
Stopping pvedaemon...
Stopping pveproxy...
Stopping pvestatd...
All PVE services stopped successfully.

Unmounting /etc/pve...
Successfully unmounted /etc/pve
```

#### Phase 10: Service Management (PBS)

**For PBS configuration restore**:
```
Preparing PBS system for restore: stopping proxmox-backup services

Stopping proxmox-backup-proxy...
Stopping proxmox-backup...
PBS services stopped successfully.
```

If service stop fails, you'll be prompted:
```
Unable to stop PBS services automatically: <error>
Continue restore with PBS services still running? (y/N): _
```

#### Phase 11 & 12: Extraction

```
Extracting selected categories from archive into /
Detailed restore log: /tmp/proxsave/restore_20251120_143052.log

Extracting: /var/lib/pve-cluster/config.db
Extracting: /var/lib/pve-cluster/.version
Extracting: /etc/vzdump.conf
...
Successfully restored 47 files/directories

Exporting 1 export-only category(ies) to: /opt/proxsave/proxmox-config-export-20251120-143052
Exported 23 files/directories
```

#### Phase 13: pvesh SAFE Apply (Cluster SAFE Mode Only)

When using Cluster SAFE mode, after extraction:
```
SAFE cluster restore: applying configs via pvesh (node=pve01)

Found 3 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
Applied VM/CT config 100 (webserver)
Applied VM/CT config 101 (database)
VM/CT apply completed: ok=2 failed=0

Storage configuration found: .../etc/pve/storage.cfg
Apply storage.cfg via pvesh? (y/N): y
Applied storage definition local
Storage apply completed: ok=1 failed=0
```

See [pvesh SAFE Apply](#pvesh-safe-apply-cluster-safe-mode) for detailed explanation.

#### Phase 14: Completion

```
═══════════════════════════════════════════════════════════════
RESTORE COMPLETED
═══════════════════════════════════════════════════════════════

Restore completed successfully.
Temporary decrypted bundle removed.
Detailed restore log: /tmp/proxsave/restore_20251120_143052.log
Export directory: /opt/proxsave/proxmox-config-export-20251120-143052/
Safety backup preserved at: /tmp/proxsave/restore_backup_20251120_143052.tar.gz
Remove it manually if restore was successful: rm /tmp/proxsave/restore_backup_20251120_143052.tar.gz

IMPORTANT: You may need to restart services for changes to take effect.
  PVE services were stopped/restarted during restore; verify status with: pvecm status
REBOOT RECOMMENDED: Reboot the node (or at least restart networking and core services) so hostname/IP and service changes from the restore are fully applied.

Recreating storage directories from /etc/pve/storage.cfg...
Created: /mnt/backup/dump/
Created: /mnt/backup/images/
Storage directories recreated successfully.
```

---

## PVE Restore: Standalone vs Cluster

The restore workflow behaves differently based on whether the backup originated from a **standalone PVE node** or a **cluster member**. This is critical for understanding what happens during restore.

### Detection

The system reads the `ClusterMode` field from the backup manifest:
- **Standalone**: `ClusterMode = "standalone"` or empty
- **Cluster**: `ClusterMode = "cluster"`

### Behavior Comparison

| Scenario | ClusterMode | SAFE/RECOVERY Prompt | Restore Behavior |
|----------|-------------|----------------------|------------------|
| **Standalone** | `standalone` | NOT shown | Database restored directly |
| **Cluster + SAFE** | `cluster` | Shown, choice 1 | Export only + pvesh API apply |
| **Cluster + RECOVERY** | `cluster` | Shown, choice 2 | Database restored directly |

### Standalone Restore

When restoring from a **standalone PVE backup**:

1. **No additional prompts** - The workflow proceeds directly
2. **Direct database restore** - `/var/lib/pve-cluster/config.db` is overwritten
3. **Automatic service management** - PVE services are stopped, database restored, services restarted
4. **No isolation required** - Single node has no split-brain risk

```
Detected system type: Proxmox Virtual Environment (PVE)
[Backup is from standalone node - no SAFE/RECOVERY prompt]

Preparing system for cluster database restore: stopping PVE services...
Stopping pve-cluster, pvedaemon, pveproxy, pvestatd...
Unmounting /etc/pve...
Extracting /var/lib/pve-cluster/config.db...
Restarting PVE services...
```

### Cluster Restore - SAFE Mode

When restoring from a **cluster backup** and selecting **SAFE mode** (option 1):

1. **Files are EXPORTED only** - NOT written to system paths
2. **Database is NOT modified** - Current cluster state preserved
3. **pvesh API apply** - User can selectively apply configurations:
   - VM/CT configs applied one by one
   - Storage definitions applied individually
   - Datacenter config applied if desired
4. **Non-destructive** - Safe for active clusters

```
Cluster backup detected. Choose how to restore:
  [1] SAFE: Export cluster files only (apply via API)  ← SELECTED
  [2] RECOVERY: Full database restore

Exporting 1 export-only category(ies) to: /opt/proxsave/proxmox-config-export-*/

SAFE cluster restore: applying configs via pvesh (node=pve01)
Found 5 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
Applied VM/CT config 100 (webserver)
Applied VM/CT config 101 (database)
...
```

### Cluster Restore - RECOVERY Mode

When restoring from a **cluster backup** and selecting **RECOVERY mode** (option 2):

1. **Same as Standalone** - Direct database restore
2. **WARNING displayed** - User must confirm node isolation
3. **Split-brain risk** - CRITICAL to isolate node before proceeding

```
Cluster backup detected. Choose how to restore:
  [1] SAFE: Export cluster files only
  [2] RECOVERY: Full database restore  ← SELECTED

WARNING: Selected RECOVERY cluster restore
Ensure other nodes are ISOLATED before proceeding!

Preparing system for cluster database restore...
[Same flow as Standalone]
```

### When to Use Each Mode

| Use Case | Recommended Mode |
|----------|------------------|
| Recovering a single standalone node | Standalone (automatic) |
| Recovering specific VM configs from cluster backup | Cluster SAFE |
| Recovering storage definitions from cluster backup | Cluster SAFE |
| Full disaster recovery, cluster destroyed | Cluster RECOVERY |
| Single surviving node after cluster failure | Cluster RECOVERY + isolate first |

---

## Cluster Database Restore

Restoring the PVE cluster database (`/var/lib/pve-cluster/config.db`) requires special handling due to the cluster filesystem architecture.

### Understanding PVE Cluster Filesystem

**Architecture**:
```
pmxcfs (daemon)
    ↓
reads/writes config.db (/var/lib/pve-cluster/config.db)
    ↓
presents as FUSE mount (/etc/pve)
    ↓
syncs via corosync (cluster communication)
```

**Critical Files**:
- `/var/lib/pve-cluster/config.db` - SQLite database (actual storage)
- `/etc/pve/` - FUSE mount (view into database)

**Why Special Handling Needed**:
- Cannot write to `/etc/pve` directly (it's a FUSE mount)
- Must stop pmxcfs to safely replace config.db
- Must ensure cluster synchronization after restore

### Cluster Restore Modes: SAFE vs RECOVERY

When the backup was created from a cluster node (detected via manifest `ClusterMode: cluster`) and you select the `pve_cluster` category, the restore workflow presents two options:

```
Cluster backup detected. Choose how to restore the cluster database:
  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).
  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.
  [0] Exit
```

#### Option 1: SAFE Mode (Recommended for Active Clusters)

**What it does**:
- Does **NOT** write to `/var/lib/pve-cluster/config.db`
- Exports all cluster files to a separate directory for review
- Offers to apply configurations via `pvesh` API (non-destructive)
- Preserves your current running cluster state

**When to use**:
- Cluster is still operational
- You want to selectively restore specific VMs or storage definitions
- You're recovering individual configurations, not the entire cluster
- You want to review changes before applying them

**Post-restore actions (SAFE mode)**:
After export, the workflow offers interactive options to apply configurations via `pvesh`:
1. **VM/CT configs**: Scans exported configs (under `/etc/pve/nodes/<node>/...`) and applies them via `pvesh set /nodes/<node>/qemu/<vmid>/config`
   - If the target node hostname differs from the hostname stored in the backup (common after hardware migration / reinstall), ProxSave detects the mismatch and prompts you to select the exported node directory to import from (instead of silently reporting “No VM/CT configs found”).
2. **Storage configuration**: Applies `storage.cfg` entries via `pvesh set /cluster/storage/<id>`
3. **Datacenter configuration**: Applies `datacenter.cfg` via `pvesh set /cluster/config`

Each action prompts for confirmation before execution.

#### Option 2: RECOVERY Mode (Full Cluster Restore)

**What it does**:
- Stops PVE cluster services (pve-cluster, pvedaemon, pveproxy, pvestatd)
- Unmounts `/etc/pve` FUSE filesystem
- Writes directly to `/var/lib/pve-cluster/config.db`
- Restarts services with restored configuration
- Avoids restoring files under `/etc/pve/*` while pmxcfs is stopped/unmounted (to prevent "shadowed" writes on the underlying disk). Those files are expected to come from the restored `config.db`.

**When to use**:
- Complete disaster recovery
- Cluster is completely offline or destroyed
- Node is isolated from other cluster members
- You have no working cluster to preserve

**CRITICAL WARNING**:
- Using RECOVERY mode on a node that is still part of an active cluster can cause split-brain
- Always isolate the node first (stop corosync, disconnect network)
- Other nodes may need to be removed and rejoined after restore

### Prerequisites

**CRITICAL: Before starting restore**:

1. **Isolate Node** (for multi-node clusters):
   ```bash
   # Stop cluster communication
   systemctl stop corosync

   # Or disconnect from cluster network
   # ip link set <cluster-interface> down
   ```

2. **Verify Node Isolation**:
   ```bash
   pvecm nodes
   # Should show only this node or error
   ```

3. **Document Current State**:
   ```bash
   pvecm status > /root/cluster-state-before-restore.txt
   pvesm status > /root/storage-state-before-restore.txt
   ```

4. **Backup Current State** (in addition to automatic safety backup):
   ```bash
   tar -czf /root/manual-cluster-backup.tar.gz /var/lib/pve-cluster/
   ```

### Automatic Service Management

When restoring the `pve_cluster` category, the workflow automatically:

**Stops services** (in order):
```
1. pve-cluster  → Stops pmxcfs, unmounts /etc/pve
2. pvedaemon    → Stops API daemon
3. pveproxy     → Stops web interface
4. pvestatd     → Stops statistics collection
```

**Unmounts filesystem**:
```bash
umount /etc/pve
```

**Restores files**:
- Extracts `/var/lib/pve-cluster/` directory
- Includes config.db and all related files
- Preserves permissions and ownership

**Restarts services** (in order):
```
1. pve-cluster  → Starts pmxcfs, reads restored config.db, remounts /etc/pve
2. pvedaemon    → Reads cluster config from /etc/pve
3. pveproxy     → Connects to pvedaemon
4. pvestatd     → Resumes statistics
```

### Service Stop/Restart Flow

```
┌─────────────────────────────────────────────────┐
│  CLUSTER DATABASE RESTORE SEQUENCE              │
└─────────────────────────────────────────────────┘

Before Restore:
  ┌─────────────┐
  │ pve-cluster │ ─┐
  │  (running)  │  │
  └─────────────┘  │
  ┌─────────────┐  │  All services
  │ pvedaemon   │ ─┤  running
  └─────────────┘  │  /etc/pve mounted
  ┌─────────────┐  │
  │ pveproxy    │ ─┤
  └─────────────┘  │
  ┌─────────────┐  │
  │ pvestatd    │ ─┘
  └─────────────┘

Stop Phase:
  systemctl stop pve-cluster  ← /etc/pve unmounted
  systemctl stop pvedaemon
  systemctl stop pveproxy
  systemctl stop pvestatd
  umount /etc/pve (if needed)

Restore Phase:
  Extract: /var/lib/pve-cluster/config.db
  Extract: /var/lib/pve-cluster/* (all files)

Restart Phase (deferred):
  systemctl start pve-cluster ← Reads restored config.db
                               ← Remounts /etc/pve
  systemctl start pvedaemon   ← Reads /etc/pve config
  systemctl start pveproxy
  systemctl start pvestatd

After Restore:
  ┌─────────────┐
  │ pve-cluster │ ─┐
  │  (running)  │  │
  └─────────────┘  │
  ┌─────────────┐  │  All services
  │ pvedaemon   │ ─┤  restarted
  └─────────────┘  │  /etc/pve remounted
  ┌─────────────┐  │  with RESTORED config
  │ pveproxy    │ ─┤
  └─────────────┘  │
  ┌─────────────┐  │
  │ pvestatd    │ ─┘
  └─────────────┘
```

### Post-Restore Verification

**Immediate Checks**:

1. **Verify Services Running**:
   ```bash
   systemctl status pve-cluster pvedaemon pveproxy pvestatd
   ```
   All should show: `active (running)`

2. **Verify /etc/pve Mounted**:
   ```bash
   mount | grep pve
   # Output: /etc/pve type fuse.pmxcfs (rw,nosuid,nodev,relatime,user_id=0,group_id=0)

   ls -la /etc/pve/
   # Should show: storage.cfg, datacenter.cfg, user.cfg, etc.
   ```

3. **Verify Cluster Status**:
   ```bash
   pvecm status
   ```
   Expected output:
   ```
   Cluster information
   -------------------
   Name:             pve-cluster
   Config Version:   X
   Transport:        knet
   Secure auth:      on

   Quorum information
   ------------------
   Date:             ...
   Quorum provider:  corosync_votequorum
   Nodes:            1  # For single-node
   Expected votes:   1
   Total votes:      1
   Quorum:           1
   Flags:            Quorate
   ```

4. **Verify API Access**:
   ```bash
   pvesh get /version
   # Should return PVE version info without errors

   pvesh get /cluster/resources
   # Should list cluster resources
   ```

5. **Verify Storage Configuration**:
   ```bash
   pvesm status
   # Should list all storage as 'active'
   ```

6. **Check Logs for Errors**:
   ```bash
   journalctl -u pve-cluster --since "5 minutes ago"
   journalctl -u pvedaemon --since "5 minutes ago"
   # Should show clean startup, no errors
   ```

### Multi-Node Cluster Considerations

**IMPORTANT**: Restoring config.db on one node of a multi-node cluster can cause cluster desynchronization.

**Recommended Approaches**:

**Option 1: Standalone Node Restore (Safest)**
```bash
# Before restore: Remove node from cluster
pvecm delnode <this-node-name>

# Perform restore
./build/proxsave --restore

# After restore: Rejoin cluster (if applicable)
# Or accept this node as new standalone cluster
```

**Option 2: Full Cluster Rebuild**
```bash
# On ALL nodes: Stop cluster communication
systemctl stop corosync

# On PRIMARY node: Perform restore
./build/proxsave --restore

# On PRIMARY node: Restart corosync
systemctl start corosync

# On OTHER nodes: Remove old cluster data and rejoin
rm -rf /etc/pve /var/lib/pve-cluster/*
pvecm add <primary-node-ip>
```

**Option 3: Isolated Node Recovery**
```bash
# Isolate node from cluster network
# (Disconnect network cable or firewall rules)

# Perform restore on isolated node
./build/proxsave --restore

# Test recovered configuration
# Verify all services working

# Decide: Keep standalone OR rejoin cluster
```

### Common Issues and Solutions

**Issue: Services fail to start after restore**

```bash
# Check detailed logs
journalctl -xe -u pve-cluster

# Common causes:
# - config.db corruption
# - Hostname mismatch
# - Certificate issues

# Solution: Restore from safety backup
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /
systemctl restart pve-cluster pvedaemon pveproxy pvestatd
```

**Issue: /etc/pve not mounting**

```bash
# Check if pmxcfs is running
systemctl status pve-cluster

# Try manual unmount and restart
umount -f /etc/pve
systemctl restart pve-cluster

# Check logs
journalctl -u pve-cluster --since "1 hour ago"
```

**Issue: Cluster shows wrong nodes**

```bash
# Edit corosync configuration
vi /etc/pve/corosync.conf
# Remove references to dead/wrong nodes

# Update cluster
pvecm updatecerts
pvecm delnode <wrong-node-name>

# Restart services
systemctl restart corosync pve-cluster
```

**Issue: Hostname changed since backup**

```bash
# Option 1: Change hostname to match backup
hostnamectl set-hostname <original-hostname>
reboot

# Option 2: Update cluster config for new hostname
# Edit /etc/pve/corosync.conf
# Change old hostname to new hostname
# Regenerate certificates
pvecm updatecerts
systemctl restart pve-cluster
```

---

## Export-Only Categories

Certain paths are too sensitive to restore directly and are extracted to a separate location for manual review.

### What is Export-Only?

**Export-only categories** contain files that:
- Cannot be safely written to their original location
- Require manual review before use
- May conflict with running services
- Are controlled by system daemons

### Export-Only Category: pve_config_export

**Category**: `pve_config_export`
**Path**: `./etc/pve/`
**Reason**: `/etc/pve` is a FUSE mount managed by pmxcfs

**Why Export-Only?**:
```
/etc/pve (FUSE mount)
    ↑
    Managed by pmxcfs daemon
    ↑
    Backend: /var/lib/pve-cluster/config.db

Writing directly to /etc/pve:
  ✗ Bypasses cluster synchronization
  ✗ Conflicts with FUSE filesystem
  ✗ May corrupt cluster state
  ✗ Changes don't persist (only in RAM)
```

**Code Protection**:
```
Hard guard in code prevents ANY write to /etc/pve when restoring to /
(see internal/orchestrator/restore.go:880-884)
```

### Export Process

**Two-Pass Extraction**:
```
Pass 1: Normal Categories
  ├─ Destination: / (system root)
  ├─ Categories: All non-export-only
  ├─ Safety backup: Created before extraction
  └─ Log: /tmp/proxsave/restore_TIMESTAMP.log

Pass 2: Export-Only Categories
  ├─ Destination: <BASE_DIR>/proxmox-config-export-YYYYMMDD-HHMMSS/
  ├─ Categories: Only export-only (pve_config_export)
  ├─ Safety backup: Not created (not overwriting system)
  └─ Log: Separate section in same log file
```

**Export Directory Structure**:
```
/opt/proxsave/proxmox-config-export-20251120-143052/
└── etc/
    └── pve/
        ├── datacenter.cfg
        ├── storage.cfg
        ├── user.cfg
        ├── corosync.conf
        ├── nodes/
        │   └── pve01/
        │       ├── pve-ssl.pem
        │       └── pve-ssl.key
        ├── qemu-server/
        │   ├── 100.conf
        │   └── 101.conf
        └── lxc/
            └── 200.conf
```

### Using Exported Files

**Purpose**: Reference and manual selective restoration

**Recommended Workflow**:

1. **Review Exported Files**:
   ```bash
   cd /opt/proxsave/proxmox-config-export-YYYYMMDD-HHMMSS/etc/pve/

   # Check cluster configuration
   cat datacenter.cfg
   cat storage.cfg
   cat user.cfg

   # List VMs/CTs
   ls qemu-server/
   ls lxc/
   ```

2. **Compare with Current System**:
   ```bash
   # Compare storage configuration
   diff /opt/proxsave/proxmox-config-export-*/etc/pve/storage.cfg \
        /etc/pve/storage.cfg

   # Compare user configuration
   diff /opt/proxsave/proxmox-config-export-*/etc/pve/user.cfg \
        /etc/pve/user.cfg
   ```

3. **Selective Manual Restoration**:
   ```bash
   # Example: Restore a specific VM config
   cp /opt/proxsave/proxmox-config-export-*/etc/pve/qemu-server/100.conf \
      /etc/pve/qemu-server/100.conf

   # Example: Restore user configuration
   cp /opt/proxsave/proxmox-config-export-*/etc/pve/user.cfg \
      /etc/pve/user.cfg

   # Note: These writes go through pmxcfs (FUSE), so they're safe
   ```

4. **Extract Configuration Values**:
   ```bash
   # Get specific storage definition
   grep -A 10 "dir: backup-storage" \
     /opt/proxsave/proxmox-config-export-*/etc/pve/storage.cfg

   # Get user list
   cat /opt/proxsave/proxmox-config-export-*/etc/pve/user.cfg | grep "user:"
   ```

### Export-Only in Custom Mode

**Visibility**:
- Export-only categories **NOT shown** in Full/Storage/Base modes
- **Only available** in Custom selection mode

**Selection**:
```
Available categories:
  [1] [ ] PVE Config Export
      Export-only copy of /etc/pve (never written to system paths)
      ↑ Clear description warns user
```

**Restore Plan Display**:
```
Export-only categories (will be extracted to separate directory):
  • PVE Config Export
    Destination: /opt/proxsave/proxmox-config-export-20251120-143052/
```

**Important**: ProxSave extracts export-only files to the separate directory shown above. The tool **does NOT automatically copy** these files to system paths. Any `cp` commands shown in this documentation are **manual examples** that you must execute yourself after reviewing the exported files. This design prevents accidental overwrites and gives you full control over what gets restored.

### Integration with Cluster Restore

**Correct Approach**: Use BOTH categories

```
Custom selection:
  [X] PVE Cluster Configuration ← Restores /var/lib/pve-cluster/config.db
  [X] PVE Config Export         ← Exports /etc/pve/ for reference

Result:
  1. config.db restored → New cluster configuration active
  2. /etc/pve/ exported → Old configuration available for comparison
  3. User can review differences and selectively copy needed files
```

**Why Both?**:
- `pve_cluster`: Restores the database (actual restore)
- `pve_config_export`: Provides reference copy of old /etc/pve (for comparison)

### pvesh SAFE Apply (Cluster SAFE Mode)

When using **SAFE cluster restore mode**, the workflow offers to apply exported configurations via the Proxmox VE API (`pvesh`). This allows you to restore individual configurations without replacing the entire cluster database.

**Available Actions**:

#### 1. VM/CT Configuration Apply

```
Found 5 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
```

For each VM/CT config found in the export:
- Reads config from `<export>/etc/pve/nodes/<node>/qemu-server/<vmid>.conf`
- Applies via: `pvesh set /nodes/<node>/qemu/<vmid>/config --filename <config>`
- Reports success/failure for each VM

**Note**: This creates or updates VM configurations in the cluster. Disk images are NOT affected.

#### 2. Storage Configuration Apply

```
Storage configuration found: /opt/proxsave/proxmox-config-export-*/etc/pve/storage.cfg
Apply storage.cfg via pvesh? (y/N): y
```

Parses `storage.cfg` and applies each storage definition:
- Each `storage: <name>` block is extracted
- Applied via: `pvesh set /cluster/storage/<name> -conf <block>`
- Existing storage with same name will be updated

**Note**: Storage directories are NOT created automatically. Run `pvesm status` to verify, then create missing directories manually.

#### 3. Datacenter Configuration Apply

```
Datacenter configuration found: /opt/proxsave/proxmox-config-export-*/etc/pve/datacenter.cfg
Apply datacenter.cfg via pvesh? (y/N): y
```

Applies datacenter-wide settings:
- Applied via: `pvesh set /cluster/config -conf <file>`
- Affects all cluster nodes

**Interactive Flow**:
```
SAFE cluster restore: applying configs via pvesh (node=pve01)

Found 3 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
Applied VM/CT config 100 (webserver)
Applied VM/CT config 101 (database)
Applied VM/CT config 102 (mailserver)
VM/CT apply completed: ok=3 failed=0

Storage configuration found: .../etc/pve/storage.cfg
Apply storage.cfg via pvesh? (y/N): y
Applied storage definition local
Applied storage definition backup-nfs
Storage apply completed: ok=2 failed=0

Datacenter configuration found: .../etc/pve/datacenter.cfg
Apply datacenter.cfg via pvesh? (y/N): n
Skipping datacenter.cfg apply
```

**Benefits of pvesh Apply**:
- Non-destructive: works with running cluster
- Selective: apply only what you need
- Auditable: each action logged
- Reversible: changes can be undone via GUI/API

---

## VM/CT Configuration Restore

Complete guide for restoring Virtual Machine and Container configurations from ProxSave backups.

### Overview

ProxSave **always backs up** all VM and CT configuration files from:
- `/etc/pve/qemu-server/*.conf` (QEMU VMs)
- `/etc/pve/lxc/*.conf` (LXC Containers)

These configurations are included in every backup and can be restored using **three different methods**.

**Important**: VM/CT **disk images are NOT backed up** by ProxSave. Only configuration files are included. Use Proxmox native backup tools (vzdump) for disk images.

---

### Three Restoration Methods

| Method | Use Case | Safety | Complexity |
|--------|----------|--------|------------|
| **pvesh SAFE Apply** | Active cluster, selective restore | High | Low |
| **Manual Copy** | Review before applying, single VM restore | Medium | Low |
| **Full Cluster Restore** | Disaster recovery, complete rebuild | Low | High |

---

### Method 1: pvesh SAFE Apply (Recommended)

**Best for**: Active clusters where you want to restore specific VMs without touching the cluster database.

**How it works**:
1. During restore, select **Cluster SAFE mode**
2. Configurations are exported to temporary directory
3. Interactive prompt asks: "Apply all VM/CT configs via pvesh?"
4. Each config applied via Proxmox API: `pvesh set /nodes/<node>/qemu/<vmid>/config`

**Advantages**:
✅ Non-destructive (cluster database untouched)
✅ Works with running cluster
✅ Selective application (can skip specific VMs)
✅ Auditable and reversible
✅ No service interruption required

**Step-by-Step Procedure**:

1. **Run restore workflow**:
   ```bash
   ./build/proxsave --restore
   ```

2. **Select backup and decrypt** (standard workflow)

3. **When prompted for restore mode**, if backup is from cluster node:
   ```
   Backup marked as cluster node; enabling guarded restore options

   Cluster restore mode:
     [1] SAFE mode (export configs + API apply)
     [2] RECOVERY mode (restore cluster database)
     [0] Cancel

   Select: 1
   ```

4. **Select categories** (Custom mode only needed if you want to exclude components; FULL already includes the `pve_config_export` export)

5. **After extraction completes**, you'll see:
   ```
   SAFE cluster restore: applying configs via pvesh (node=pve01)

   Found 5 VM/CT configs for node pve01
   Apply all VM/CT configs via pvesh? (y/N): y
   ```

   **If the node name changed** (example: backup from `pve-old`, restore on `pve-new`), ProxSave prompts for the exported source node:
   ```
   SAFE cluster restore: applying configs via pvesh (node=pve-new)

   WARNING: VM/CT configs in this backup are stored under different node names.
   Current node: pve-new
   Select which exported node to import VM/CT configs from (they will be applied to the current node):
     [1] pve-old (qemu=12, lxc=3)
     [0] Skip VM/CT apply
   Choice: 1

   Found 15 VM/CT configs for exported node pve-old (will apply to current node pve-new)
   Apply all VM/CT configs via pvesh? (y/N): y
   ```

6. **Confirm and watch progress**:
   ```
   Applied VM/CT config 100 (webserver)
   Applied VM/CT config 101 (database)
   Applied VM/CT config 102 (mailserver)
   Applied VM/CT config 103 (proxy)
   Applied VM/CT config 104 (backup)
   VM/CT apply completed: ok=5 failed=0
   ```

**Verification**:
```bash
# Check VMs are visible in Proxmox
qm list

# Verify specific VM config
qm config 100

# Check via web interface
https://your-pve:8006
```

**Troubleshooting**:
- If pvesh apply fails: Check logs for API errors, verify VM IDs don't conflict
- If VM not visible: Refresh web interface, check node name matches
- If config incorrect: Edit via GUI or `qm set <vmid> <option>`

---

### Method 2: Manual Copy from Export Directory

**Best for**: Reviewing configs before applying, restoring single specific VM, comparing old vs current.

**How it works**:
1. Restore with `pve_config_export` category selected (or Cluster SAFE mode)
2. Configurations extracted to: `/opt/proxsave/proxmox-config-export-<timestamp>/`
3. Review exported files
4. Manually copy desired configs to `/etc/pve/`

**Advantages**:
✅ Full control over what gets restored
✅ Review before applying
✅ Compare with current configs
✅ Extract individual values without full restore

**Step-by-Step Procedure**:

1. **Run restore and select pve_config_export category**:
   ```bash
   ./build/proxsave --restore
   # Select "Custom" mode
   # Enable "PVE Config Export" category
   ```

2. **Locate exported files**:
   ```bash
   cd /opt/proxsave/proxmox-config-export-*/etc/pve/

   # List available VM configs
   ls qemu-server/
   # Output: 100.conf  101.conf  102.conf

   # List container configs
   ls lxc/
   # Output: 200.conf  201.conf
   ```

3. **Review configuration before applying**:
   ```bash
   # View VM config
   cat qemu-server/100.conf

   # Compare with current config (if exists)
   diff qemu-server/100.conf /etc/pve/qemu-server/100.conf
   ```

4. **Copy desired config to system**:
   ```bash
   # Copy specific VM config
   cp qemu-server/100.conf /etc/pve/qemu-server/100.conf

   # Copy container config
   cp lxc/200.conf /etc/pve/lxc/200.conf

   # Or restore all VMs at once
   cp qemu-server/*.conf /etc/pve/qemu-server/
   ```

   **Note**: Writing to `/etc/pve/` goes through pmxcfs FUSE filesystem, so it's safe and properly synchronized across cluster.

5. **Verify in Proxmox**:
   ```bash
   qm list              # List VMs
   pct list             # List containers
   qm config 100        # Check specific VM
   ```

**Extract Specific Values Without Full Restore**:
```bash
# Get VM memory setting
grep "^memory:" qemu-server/100.conf

# Get network configuration
grep "^net" qemu-server/100.conf

# Get all storage definitions
grep "^scsi\|^virtio\|^ide\|^sata" qemu-server/100.conf
```

**Troubleshooting**:
- If copy fails: Check `/etc/pve/` is mounted (`mount | grep pve`)
- If VM doesn't appear: Restart pve-cluster service
- If config malformed: Edit directly in GUI or with `qm set`

---

### Method 3: Full Cluster Database Restore (RECOVERY Mode)

**Best for**: Complete disaster recovery, new hardware installation, total cluster rebuild.

**How it works**:
- Restores entire `/var/lib/pve-cluster/config.db` database
- All cluster configuration restored at once (including all VM/CT configs)
- Requires stopping PVE services and unmounting `/etc/pve/`

**Advantages**:
✅ Complete restore of entire cluster state
✅ All VMs, users, storage, settings restored together
✅ Ideal for disaster recovery

**Disadvantages**:
⚠️ Service interruption required
⚠️ Overwrites current cluster state
⚠️ All-or-nothing (can't selectively restore single VM)
⚠️ Risk of cluster desynchronization in multi-node setups

**When to Use**:
- Bare-metal disaster recovery
- Migration to new hardware
- Complete cluster rebuild
- Single-node standalone system

**When NOT to Use**:
- Active multi-node cluster (use SAFE mode instead)
- Only need to restore specific VMs (use Manual Copy or pvesh SAFE)
- Want to preserve current cluster state

**Procedure**: See [Cluster Database Restore](#cluster-database-restore) section for complete workflow.

**Note**: This method is documented in detail in the "Cluster Database Restore" section and [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md).

---

### Decision Tree: Which Method Should I Use?

```
Are you restoring to an active multi-node cluster?
├─ YES → Use Method 1: pvesh SAFE Apply
│        (Non-destructive, no downtime)
│
└─ NO → Is this a complete disaster recovery?
    ├─ YES → Use Method 3: Full Cluster Database Restore
    │        (Restores everything at once)
    │
    └─ NO → Do you need to restore all VMs?
        ├─ YES → Use Method 1: pvesh SAFE Apply
        │        (Faster than manual copy)
        │
        └─ NO → Use Method 2: Manual Copy
                 (Review before applying)
```

**Quick Reference Table**:

| Scenario | Recommended Method | Reason |
|----------|-------------------|---------|
| Active cluster, add missing VMs | pvesh SAFE Apply | No downtime, selective |
| Single-node, restore specific VM | Manual Copy | Full control, review first |
| New hardware installation | Full Cluster Restore | Complete system rebuild |
| Migration from old server | Full Cluster Restore | Everything in one operation |
| Review configs before applying | Manual Copy | Inspect before committing |
| Restore many VMs quickly | pvesh SAFE Apply | Automated, less error-prone |
| Multi-node cluster recovery | Full Cluster Restore | Synchronized state |

---

### Important Notes

**VM/CT Disk Images**:
- ProxSave does **NOT backup disk images** (they're typically hundreds of GB)
- Only configuration files are backed up
- For disk restoration, use:
  - Proxmox vzdump backups
  - Storage-level replication
  - ZFS snapshots/replication
  - Manual disk copies

**After Restore**:
- VM/CT configs are restored but **VMs remain stopped**
- Disk images must exist at paths specified in config
- Storage referenced in config must be configured
- If disk paths changed, edit configs via GUI

**Configuration vs. Data**:
```
What ProxSave Backs Up:
✅ VM/CT configuration files (*.conf)
✅ Cluster settings
✅ Storage definitions
✅ User/permissions
✅ Network configuration
✅ Backup job definitions

What ProxSave Does NOT Back Up:
❌ VM/CT disk images (*.qcow2, *.raw, etc.)
❌ Running VM memory state
❌ Application data inside VMs
❌ Storage pool data
```

**Best Practice Recommendation**:
1. Use ProxSave for **configuration backup** (what it's designed for)
2. Use Proxmox vzdump for **VM disk backups**
3. Combine both for complete disaster recovery capability

---

## Safety Features

Multiple layers of protection prevent data loss and corruption during restore.

### 1. Safety Backup

**Automatic** backup before ANY changes are written.

**Location**: `/tmp/proxsave/restore_backup_YYYYMMDD_HHMMSS.tar.gz`

**Contents**:
- All files that will be overwritten by restore
- Full directory structures
- Preserved permissions, ownership, timestamps

**Rollback Command**:
```bash
tar -xzf /tmp/proxsave/restore_backup_20251120_143052.tar.gz -C /
```

**If Safety Backup Fails**:
```
Failed to create safety backup: <error>

Continue without safety backup? (yes/no): _
```
- User can choose to abort (safe) or continue (risky)

### 2. Interactive Confirmation

**Multiple abort points** throughout workflow:

```
Abort Points:
  [0] Cancel backup selection
  [0] Cancel restore mode selection
  [0] Cancel category selection
  [cancel] Cancel at restore plan confirmation
  [Ctrl+C] Cancel at any time
```

**Confirmation Requirement**:
```
Type "RESTORE" (exact case) to proceed, or "cancel"/"0" to abort: _
```
- Must type exact word "RESTORE"
- Case-sensitive
- Prevents accidental restoration

**Prompt timeouts (auto-skip)**:
- Some interactive prompts include a visible countdown (currently **90 seconds**) to avoid getting “stuck” waiting for input in remote/automated scenarios.
- If the user does not answer before the countdown reaches 0, ProxSave proceeds with a **safe default** (no destructive action) and logs the decision.

Current auto-skip prompts:
- **Smart `/etc/fstab` merge**: defaults to **Skip** (no changes).
- **Live network apply** (“Apply restored network configuration now…”): defaults to **No** (stays staged/on-disk only; no live reload).

### 3. Compatibility Validation

**System Type Detection**:
```
Current system: Proxmox Virtual Environment (PVE)
Backup source: Proxmox Virtual Environment (PVE)
✓ Compatible
```

**Incompatibility Warning**:
```
⚠ WARNING: Potential incompatibility detected!
Type "yes" to continue anyway or "no" to abort: _
```

### 4. Network Safe Apply (Optional)

If the **network** category is restored, ProxSave can optionally apply the
new network configuration immediately using a **transactional rollback timer**.

**Apply prompt auto-skip**:
- The “apply now” prompt includes a **90-second** countdown; if you do not answer in time, ProxSave defaults to **No** and skips the live reload.

**Important (console recommended)**:
- Run the live network apply/commit step from the **local console** (physical console, IPMI/iDRAC/iLO, Proxmox console, or hypervisor console), not from SSH.
- If the restored network config changes the management IP or routes, your SSH session will drop and you may be unable to type `COMMIT`.
- In that case, ProxSave will treat the lack of `COMMIT` as “not confirmed” and will restore the previous network settings (rollback).

**How it works**:
- On live restores (writing to `/`), ProxSave **stages** network files first under `/tmp/proxsave/restore-stage-*` and does **not** overwrite `/etc/network/*` during archive extraction.
- After extraction, ProxSave performs a prevention-first **staged install**: it writes the staged files to disk (no reload), runs safe NIC repair + preflight validation, and **rolls back automatically** if validation fails (leaving the staged copy for review).
- If rollback backup creation fails (or ProxSave is not running as root), ProxSave keeps network files staged and avoids writing to `/etc`.
- When you choose to apply live, ProxSave (re)validates and reloads networking inside the rollback timer window.
- ProxSave arms a local rollback job **before** applying changes
- Rollback restores **only network-related files** using a dedicated archive under `/tmp/proxsave/network_rollback_backup_*` (so it won’t undo other restored categories)
- Rollback also prunes network config files that were **created after** the backup (e.g. extra files under `/etc/network/interfaces.d/`), so rollback returns to the exact pre-restore state
- The user has **180 seconds** to type `COMMIT`
- If `COMMIT` is not received, ProxSave triggers the rollback and restores the pre-restore network configuration
- If the network-only rollback archive is not available, ProxSave prompts before falling back to the full safety backup (or skipping live apply)

This protects SSH/GUI access during network changes.

**Health checks**:
- After applying changes, ProxSave runs local checks (SSH route if available, default route, link state, IP addresses, gateway ping, DNS config/resolve, local web UI port)
- On PVE systems, additional checks are included for cluster networking: `/etc/pve` (pmxcfs) mount status, `pve-cluster` / `corosync` service state, and `pvecm status` quorum
- The result is shown to help decide whether to type `COMMIT`
- Diagnostics are saved under `/tmp/proxsave/network_apply_*` (snapshots `before.txt` / `after.txt` / `after_rollback.txt` when relevant, `health_before.txt` / `health_after.txt`, `preflight.txt`, `plan.txt`, and `ifquery_*`)

**NIC name repair**:
- If physical NIC names changed after reinstall (e.g. `eno1` → `enp3s0`), ProxSave attempts an automatic mapping using backup network inventory (permanent MAC / MAC / PCI path / udev IDs like `ID_PATH`, `ID_NET_NAME_PATH`, `ID_NET_NAME_SLOT`, `ID_SERIAL`)
- When a safe mapping is found, `/etc/network/interfaces` and `/etc/network/interfaces.d/*` are rewritten before applying the network config
- If you skip live network apply, ProxSave may still install the staged config to disk (no reload) after safe NIC repair + preflight; if validation fails, it rolls back and keeps the staged copy.
- If a mapping would overwrite an interface name that already exists on the current system, ProxSave prompts before applying it (conflict-safe)
- If persistent NIC naming rules are detected (custom udev `NAME=` rules or systemd `.link` files), ProxSave warns and prompts before applying NIC repair to avoid conflicts with user-intended naming
- A backup of the pre-repair files is stored under `/tmp/proxsave/nic_repair_*`

**Preflight validation**:
- After NIC repair, ProxSave runs a **gate** validation of the ifupdown configuration before reloading networking (e.g. `ifup -n -a` / `ifup --no-act -a` / `ifreload --syntax-check -a`)
- If validation fails, live apply is aborted and the validator output is saved under `/tmp/proxsave/network_apply_*/preflight.txt`
- Additionally (diagnostics-only), ProxSave can run `ifquery --check -a` **before and after apply** to show how the runtime state matches the target config. Its output is saved under `/tmp/proxsave/network_apply_*/ifquery_*`. Note that `ifquery --check` can show `[fail]` **before apply** even when the config is valid (because the running state still reflects the old config).
- On staged installs/applies, a failed preflight triggers an **automatic rollback of network files** (no prompt), returning to the pre-restore state and keeping the staged copy for review.

**Result reporting**:
- If you do not type `COMMIT`, ProxSave completes the restore with warnings and reports that the original network settings were restored (including the current IP, when detectable), plus the rollback log path.

#### Ctrl+C footer: `NETWORK ROLLBACK` status

If you interrupt ProxSave with **Ctrl+C** and a live network apply/rollback timer was involved, the CLI footer can print a `NETWORK ROLLBACK` block with a recommended reconnection IP and the rollback log path.

The status can be one of:
- **ARMED**: rollback is still pending and will execute automatically at the deadline (a short countdown may be shown).
- **DISARMED/CLEARED**: rollback will **not** run (the marker was removed before the deadline; this can happen if it was manually cleared/disarmed).
- **EXECUTED**: rollback already ran (marker removed after the deadline).

**Which IP should I use?**
- **ARMED**: prepare to reconnect using the **pre-apply IP** once rollback runs.
- **EXECUTED**: reconnect using the **pre-apply IP** (the system should be back on the previous network config).
- **DISARMED/CLEARED**: reconnect using the **post-apply IP** (the applied config remains active).

Notes:
- *Pre-apply IP* is derived from the `before.txt` snapshot in `/tmp/proxsave/network_apply_*` and may be `unknown` if it cannot be parsed.
- *Post-apply IP* is what ProxSave could observe on the management interface after applying the new config; it may include CIDR suffixes (for example `10.0.0.4/24`) or multiple addresses.

**Example outputs**

The `NETWORK ROLLBACK` block is printed just before the standard ProxSave footer (the footer color reflects the exit status, e.g. magenta on Ctrl+C).

Example 1 — **ARMED** (rollback pending, countdown shown for a few seconds):
```text
===========================================
NETWORK ROLLBACK

  Status: ARMED (will execute automatically)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /tmp/proxsave/network_rollback_20260122_153012.log

Connection will be temporarily interrupted during restore.
Remember to reconnect using the pre-apply IP: 192.168.1.100
  Remaining: 147s
===========================================

===========================================
ProxSave - Go - <build signature>
===========================================
```

Example 2 — **EXECUTED** (rollback already ran, no countdown):
```text
===========================================
NETWORK ROLLBACK

  Status: EXECUTED (marker removed)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /tmp/proxsave/network_rollback_20260122_153012.log

Rollback executed: reconnect using the pre-apply IP: 192.168.1.100
===========================================
```

Example 3 — **DISARMED/CLEARED** (rollback will not run, applied config remains active):
```text
===========================================
NETWORK ROLLBACK

  Status: DISARMED/CLEARED (marker removed before deadline)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /tmp/proxsave/network_rollback_20260122_153012.log

Rollback will NOT run: reconnect using the post-apply IP: 10.0.0.4/24
===========================================
```

### 5. Smart `/etc/fstab` Merge (Optional)

If the restore includes filesystem configuration (notably `/etc/fstab`), ProxSave can run a **smart merge** instead of blindly overwriting your current `fstab`.

**What it does**:
- Compares the current `/etc/fstab` with the backup copy.
- Keeps existing critical entries (for example, root and swap) when they already match the running system.
- Detects **safe mount candidates** from the backup (for example, additional NFS mounts) and offers to add them.

**Safety behavior**:
- The user is prompted before any change is written.
- The prompt includes a **90-second** countdown; if you do not answer in time, ProxSave defaults to **Skip** (no changes).

### 6. Hard Guards

**Path Traversal Prevention**:
- All extracted paths validated
- Paths outside destination root rejected
- Security: Prevents malicious archive escapes

**`/etc/pve` Write Block**:
```go
// Hard guard in code
if cleanDestRoot == "/" && strings.HasPrefix(target, "/etc/pve") {
    logger.Warning("Skipping restore to %s (writes to /etc/pve are prohibited)", target)
    return nil
}
```
- Absolute prevention of `/etc/pve` corruption

### 7. Service Management Fail-Fast

**Service Stop**: If ANY service fails to stop → ABORT entire restore

**Why?**:
- Prevents partial corruption
- Better to fail safely than corrupt database
- User can investigate and retry

### 8. Comprehensive Logging

**Detailed Log**: `/tmp/proxsave/restore_YYYYMMDD_HHMMSS.log`

**Contents**:
```
=== RESTORE LOG ===
Started: 2025-11-20 14:30:52

EXTRACTED FILES:
  /var/lib/pve-cluster/config.db (ownership: 0:0, mode: 0600)
  /var/lib/pve-cluster/.version (ownership: 0:0, mode: 0644)
  /etc/vzdump.conf (ownership: 0:0, mode: 0644)
  ...

SKIPPED FILES:
  ./opt/some-file (does not match any selected category)
  ...

SUMMARY:
  Files extracted: 47
  Files skipped: 1203
  Files failed: 0
  Duration: 12.3 seconds
```

**Usage**:
```bash
# Review what was restored
cat /tmp/proxsave/restore_20251120_143052.log

# Search for specific file
grep "storage.cfg" /tmp/proxsave/restore_20251120_143052.log

# Check for failures
grep "FAILED" /tmp/proxsave/restore_20251120_143052.log
```

### 9. Checksum Verification

**SHA256 Verification**:
- Backup checksum verified after decryption
- Ensures backup integrity
- Detects corruption or tampering

**Behavior**:
```
Verifying SHA256 checksum...
Expected: a1b2c3...
Actual:   a1b2c3...
✓ Checksum verified successfully.
```

### 10. Deferred Service Restart

**Go defer pattern** ensures services restart even if restore fails:

```
Services stopped → Defer restart scheduled → Restore → (Failure) → Deferred restart executes
```

**Prevents**: System left with services stopped after failed restore

---

## Troubleshooting

### General Issues

**Issue: "restore: permission denied"**

**Cause**: Not running as root

**Solution**:
```bash
sudo ./build/proxsave --restore
# Or
su -
./build/proxsave --restore
```

---

**Issue: "Failed to create safety backup"**

**Cause**: Insufficient disk space in `/tmp`

**Solution**:
```bash
# Check available space
df -h /tmp

# Clean up temporary files
rm -rf /tmp/proxsave/proxmox-decrypt-*
rm -f /tmp/proxsave/restore_backup_*.tar.gz

# Or expand /tmp (if tmpfs)
mount -o remount,size=10G /tmp
```

---

**Issue: "Backup not found in selected location"**

**Cause**: Incorrect backup path or file naming

**Solution**:
```bash
# Verify backup location
ls -la /opt/proxsave/backup/

# Check for .bundle.tar files
find /opt/proxsave/ -name "*.bundle.tar"

# Update backup.env if needed
vi /opt/proxsave/configs/backup.env
# Set correct BACKUP_PATH
```

---

### Decryption Issues

**Issue: "Decryption failed: incorrect passphrase"**

**Cause**: Wrong AGE passphrase entered

**Solution**:
```bash
# Retry with correct passphrase
# Or use AGE identity file instead
./build/proxsave --restore
# Select option [2] Use AGE identity file
```

---

**Issue: "AGE identity file not found"**

**Cause**: Default key file missing

**Solution**:
```bash
# Check for key file
ls -la /opt/proxsave/age/recipients

# If missing, use passphrase instead
# Or specify correct key file path when prompted
```

---

### Service Issues

**Issue: "Failed to stop pve-cluster: Unit not found"**

**Cause**: Not a PVE system or service not installed

**Solution**:
```bash
# This is normal on PBS systems
# Or check if PVE installed
dpkg -l | grep proxmox-ve

# If truly PVE but service missing, repair
apt install --reinstall pve-cluster
```

---

**Issue: "Services failed to restart after restore"**

**Cause**: config.db corruption or configuration error

**Solution**:
```bash
# Check service status
systemctl status pve-cluster
journalctl -xe -u pve-cluster

# Restore from safety backup
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /

# Restart services
systemctl restart pve-cluster pvedaemon pveproxy pvestatd

# If still failing, check logs
journalctl -u pve-cluster --since "10 minutes ago"
```

---

**Issue: "/etc/pve not mounting after restore"**

**Cause**: pmxcfs unable to mount FUSE filesystem

**Solution**:
```bash
# Check if already mounted
mount | grep pve

# Force unmount
umount -f /etc/pve

# Restart pve-cluster
systemctl restart pve-cluster

# Check logs
journalctl -u pve-cluster | tail -50

# If config.db corrupted, restore safety backup
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /
systemctl restart pve-cluster
```

---

### Cluster Issues

**Issue: "Cluster shows wrong nodes after restore"**

**Cause**: Restored old cluster configuration

**Solution**:
```bash
# Option 1: Remove wrong nodes
pvecm delnode <wrong-node-name>

# Option 2: Edit corosync.conf
vi /etc/pve/corosync.conf
# Remove references to dead nodes
# Update node list

pvecm updatecerts
systemctl restart corosync pve-cluster
```

---

**Issue: "Lost quorum after restore"**

**Cause**: Single-node cluster with wrong expected votes

**Solution**:
```bash
# Set expected votes to 1
pvecm expected 1

# Verify quorum
pvecm status

# Make permanent (edit corosync.conf)
vi /etc/pve/corosync.conf
# Update expected_votes in quorum section
```

---

**Issue: "Hostname mismatch in cluster config"**

**Cause**: Restored backup from different hostname

**Solution**:
```bash
# Option 1: Change hostname to match
hostnamectl set-hostname <original-hostname>
reboot

# Option 2: Update cluster config
vi /etc/pve/corosync.conf
# Change old hostname to new hostname
pvecm updatecerts
systemctl restart corosync pve-cluster
```

---

### Storage Issues

**Issue: "Storage not accessible after restore"**

**Cause**: Storage directories missing or permissions wrong

**Solution**:
```bash
# Check storage status
pvesm status

# Manually recreate storage directories
mkdir -p /mnt/backup/{dump,images,template}
chown root:root /mnt/backup -R

# Or run directory recreation manually
# (restore workflow does this automatically)
```

---

**Issue: "ZFS pools not importing after restore"**

**Cause**: ZFS pools not imported after config restore

**Solution**:
```bash
# List available pools
zpool import

# Import pool
zpool import <pool-name>

# Verify status
zpool status

# Enable auto-import
systemctl enable zfs-import@<pool-name>.service
```

---

### PBS-Specific Issues

**Issue: "Datastore not accessible"**

**Cause**: ZFS pool not mounted or directory missing

**Solution**:
```bash
# Check datastore configuration
cat /etc/proxmox-backup/datastore.cfg

# Check if ZFS pool mounted
zpool status
zfs list

# If ZFS, import pool
zpool import <pool-name>

# If directory-based datastore (non-ZFS), verify permissions for backup user
# NOTE:
# - On live restores, ProxSave stages PBS datastore/job configuration first under `/tmp/proxsave/restore-stage-*`
#   and applies it safely after checking the current system state.
# - If a datastore path looks like a mountpoint location (e.g. under `/mnt`) but resolves to the root filesystem,
#   ProxSave will **defer** that datastore definition (it will NOT be written to `datastore.cfg`), to avoid ending up
#   with a broken datastore entry that blocks re-creation on a new/empty disk. Deferred entries are saved under
#   `/tmp/proxsave/datastore.cfg.deferred.*` for manual review.
# - ProxSave may create missing datastore directories and fix `.lock`/ownership, but it will NOT format disks.
# - To avoid accidental writes to the wrong disk, ProxSave will skip datastore directory initialization if the
#   datastore path looks like a mountpoint location (e.g. under /mnt) but resolves to the root filesystem.
#   In that case, mount/import the datastore disk/pool first, then restart PBS (or re-run restore).
# - If the datastore path is not empty and contains unexpected files/directories, ProxSave will not touch it.
ls -ld /mnt/datastore /mnt/datastore/<DatastoreName> 2>/dev/null
namei -l /mnt/datastore/<DatastoreName> 2>/dev/null || true

# Common fix (adjust to your datastore path)
chown backup:backup /mnt/datastore && chmod 750 /mnt/datastore
chown -R backup:backup /mnt/datastore/<DatastoreName> && chmod 750 /mnt/datastore/<DatastoreName>
```

---

**Issue: "Bad Request (400) unable to read /etc/resolv.conf (No such file or directory)"**

**Cause**: `/etc/resolv.conf` is missing or a broken symlink. This can happen after a restore if a previous backup contained an invalid symlink (e.g. pointing to `../commands/resolv_conf.txt`), or if the target system uses `systemd-resolved` and the expected `/run/systemd/resolve/*` files are not present.

**Solution**:
```bash
ls -la /etc/resolv.conf
readlink /etc/resolv.conf 2>/dev/null || true

# If the link is broken or points to commands/resolv_conf.txt, replace it:
rm -f /etc/resolv.conf

if [ -e /run/systemd/resolve/resolv.conf ]; then
  ln -s /run/systemd/resolve/resolv.conf /etc/resolv.conf
elif [ -e /run/systemd/resolve/stub-resolv.conf ]; then
  ln -s /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf
else
  # Fallback: static DNS (adjust to your environment)
  printf "nameserver 1.1.1.1\nnameserver 8.8.8.8\noptions timeout:2 attempts:2\n" > /etc/resolv.conf
  chmod 644 /etc/resolv.conf
fi
```

Note: newer ProxSave versions attempt to auto-repair `/etc/resolv.conf` during restore when the `network` category is selected.

---

**Issue: "Bad Request (400) parsing /etc/proxmox-backup/datastore.cfg (expected section properties)"**

**Cause**: In PBS, properties inside a `datastore:` section must be indented. A malformed file (often from manual edits or very old configs) will prevent PBS from loading datastore config.

**Solution**:
```bash
# ProxSave will attempt to auto-normalize datastore.cfg during restore and store a backup under /tmp/proxsave/,
# but you can also fix it manually:
cp -a /etc/proxmox-backup/datastore.cfg /root/datastore.cfg.bak.$(date +%F_%H%M%S)

# Example of correct indentation:
# datastore: Data1
#     gc-schedule 0/2:00
#     path /mnt/datastore/Data1

editor /etc/proxmox-backup/datastore.cfg
systemctl restart proxmox-backup proxmox-backup-proxy
```

---

**Issue: "unable to read prune/verification job config ... syntax error (expected header)"**

**Cause**: PBS job config files (`/etc/proxmox-backup/prune.cfg`, `/etc/proxmox-backup/verification.cfg`) are empty or malformed. PBS expects a section header at the first non-comment line; an empty file can trigger parse errors.

**Restore behavior**:
- On live restores, ProxSave stages PBS job config files and will **remove** empty staged job configs instead of writing a 0-byte file (to avoid breaking PBS parsing).

**Manual fix**:
```bash
rm -f /etc/proxmox-backup/prune.cfg /etc/proxmox-backup/verification.cfg
systemctl restart proxmox-backup proxmox-backup-proxy
```

---

**Issue: "Datastore error: Is a directory (os error 21)"**

**Cause**: PBS expects a lock file at `<datastore-path>/.lock`. If `.lock` is a directory (common after manual fixes or incorrect initialization), PBS will fail to open it and the datastore becomes unavailable.

**Solution**:
```bash
P=/mnt/datastore/<DatastoreName>
ls -ld "$P/.lock"

# If .lock is a directory, replace it with a file:
rm -rf "$P/.lock" && touch "$P/.lock" && chown backup:backup "$P/.lock"

systemctl restart proxmox-backup proxmox-backup-proxy
```

---

## FAQ

### General Questions

**Q: Can I restore PVE backup to PBS system (or vice versa)?**

A: Not recommended. The restore workflow will warn about incompatibility. PVE and PBS have different configurations that are not interchangeable. However, **common categories** (network, SSH, SSL) can be safely restored cross-platform using Custom mode.

---

**Q: Can I automate restore operations?**

A: No. The restore workflow is intentionally interactive to prevent accidental data loss. All selections require user input and confirmation.

---

**Q: How long does a restore take?**

A: Depends on:
- Backup size (typically 10-500 MB)
- Encryption (decryption adds 1-5 minutes)
- Number of files (typically 1-10 minutes extraction)
- Storage speed

Typical full restore: **5-15 minutes**

---

**Q: Can I restore to a different server?**

A: Yes, with considerations:
- **Same system type** (PVE to PVE, PBS to PBS) recommended
- **Hostname** should match or be updated manually
- **Network configuration** may need adjustment
- **Storage paths** may need adjustment
- **Cluster membership** must be handled manually

---

**Q: What if I cancel during restore?**

A: Depends on when:
- **Before extraction**: No changes made, completely safe
- **During extraction**: Partial restore, use safety backup to rollback
- **After extraction**: Restore completed, services may be in mixed state

Use Ctrl+C carefully - wait for current file to finish.

---

### Safety & Recovery

**Q: How do I rollback a failed restore?**

A: Use the safety backup:
```bash
# Stop services (if cluster restore)
systemctl stop pve-cluster pvedaemon pveproxy pvestatd

# Extract safety backup
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /

# Restart services
systemctl restart pve-cluster pvedaemon pveproxy pvestatd

# Verify
pvecm status
pvesm status
```

---

**Q: Can I test restore without affecting production?**

A: Yes, two approaches:

**Approach 1: Test on separate system**
```bash
# Copy backup to test system
# Run restore on test system
# Validate configuration
# Apply learnings to production restore
```

**Approach 2: Decrypt-only mode**
```bash
# Decrypt without restoring
./build/proxsave --decrypt

# Manually inspect decrypted files
tar -tzf /path/to/decrypted.tar.gz | less

# Review specific files
tar -xzf /path/to/decrypted.tar.gz ./etc/pve/storage.cfg -O | less
```

---

**Q: What happens to VMs/CTs during cluster restore?**

A: **VMs/CTs themselves are NOT affected**:
- Their disk images remain untouched
- They continue running (unless services stopped on host)
- Only their **configuration** is restored
- VM configs in `/etc/pve/qemu-server/` are backed up and can be restored via:
  - **pvesh SAFE Apply** (automatic via API - recommended)
  - **Manual copy** from export directory
  - **Full cluster restore** (disaster recovery)
- See [VM/CT Configuration Restore](#vmct-configuration-restore) section for complete guide

**Recommended**: Stop all VMs/CTs before cluster restore for safety.

---

### Cluster-Specific

**Q: Can I restore cluster database on a multi-node cluster?**

A: **Risky and NOT recommended** without precautions:

**Safe approach**:
1. Isolate node from cluster
2. Restore on isolated node
3. Verify configuration
4. Decide: Keep standalone OR rejoin cluster

**Unsafe**: Restoring on active cluster node can cause split-brain.

---

**Q: How do I restore cluster database on new hardware?**

A: Full procedure:

```bash
# 1. Install Proxmox VE on new hardware
# 2. Configure same hostname as backup
hostnamectl set-hostname <original-hostname>

# 3. Run restore
./build/proxsave --restore
# Select: STORAGE mode or Custom (include pve_cluster)

# 4. Verify services
systemctl status pve-cluster pvedaemon pveproxy
pvecm status

# 5. Verify storage
pvesm status

# 6. Manually recreate any missing storage paths
# 7. Import ZFS pools if needed
zpool import <pool-name>

# 8. Access web interface and verify
https://<server-ip>:8006
```

---

**Q: What if backup hostname doesn't match current hostname?**

A: Two options:

**Option 1: Change hostname to match (easiest)**
```bash
hostnamectl set-hostname <backup-hostname>
reboot
# Then run restore
```

**Option 2: Update cluster config after restore**
```bash
# Restore as normal (hostname will mismatch)
./build/proxsave --restore

# Update corosync configuration
vi /etc/pve/corosync.conf
# Change old hostname to new hostname

# Regenerate certificates
pvecm updatecerts

# Restart services
systemctl restart corosync pve-cluster pvedaemon pveproxy
```

---

### Export-Only

**Q: Why can't I restore /etc/pve directly?**

A: `/etc/pve` is a **FUSE mount** managed by pmxcfs daemon:
- Writing directly bypasses cluster synchronization
- Changes don't persist (only in RAM)
- Can corrupt cluster state

**Correct approach**: Restore `/var/lib/pve-cluster/config.db` (the database backend)

---

**Q: How do I use exported /etc/pve files?**

A: For reference and selective manual restoration:

```bash
# Review exported files
ls /opt/proxsave/proxmox-config-export-*/etc/pve/

# Compare with current
diff /opt/proxsave/proxmox-config-export-*/etc/pve/storage.cfg \
     /etc/pve/storage.cfg

# Selectively copy needed files
cp /opt/proxsave/proxmox-config-export-*/etc/pve/qemu-server/100.conf \
   /etc/pve/qemu-server/100.conf
```

---

**Q: How do I restore individual VM/CT configuration files back to the system?**

A: **Three methods available**:

**Method 1: pvesh SAFE Apply (Recommended)**
```bash
# During restore, select Cluster SAFE mode
# Answer "yes" when prompted: "Apply all VM/CT configs via pvesh?"
# Configs applied automatically via API
```

**Method 2: Manual Copy**
```bash
# After restore with pve_config_export category:
cd /opt/proxsave/proxmox-config-export-*/etc/pve/
cp qemu-server/100.conf /etc/pve/qemu-server/100.conf
```

**Method 3: Full Cluster Restore**
- Restores entire cluster database including all VM configs
- Use for disaster recovery only

See [VM/CT Configuration Restore](#vmct-configuration-restore) for detailed procedures.

---

### Technical

**Q: Can I restore only specific files from a category?**

A: Not directly. Categories are the smallest granularity.

**Workaround**:
```bash
# Use --decrypt to create plaintext archive
./build/proxsave --decrypt

# Manually extract specific files
tar -xzf /path/to/decrypted.tar.gz ./specific/file/path
```

---

**Q: Does restore preserve file permissions and ownership?**

A: Yes, completely:
- **Ownership**: UID/GID preserved
- **Permissions**: Mode bits preserved
- **Timestamps**: mtime and atime preserved
- **ctime**: Cannot be set (kernel-managed)

---

**Q: What compression formats are supported?**

A: All standard formats:
- `.tar.gz`, `.tgz` - gzip (native Go)
- `.tar.xz` - xz (external command)
- `.tar.zst`, `.tar.zstd` - zstd (external command)
- `.tar.bz2` - bzip2 (external command)
- `.tar.lzma` - lzma (external command)
- `.tar` - uncompressed

---

**Q: Can I restore from cloud backup?**

A: Yes, in two ways:

1. **Directly from rclone remote (recommended)**  
   If you are already uploading with `CLOUD_ENABLED=true` and rclone:

   ```bash
   # Example backup.env
   CLOUD_ENABLED=true
   CLOUD_REMOTE=gdrive
   CLOUD_REMOTE_PATH=/pbs-backups/server1
   ```

   - During `--decrypt` or `--restore` (CLI or TUI), ProxSave will read the same
     `CLOUD_REMOTE` / `CLOUD_REMOTE_PATH` combination and show an entry:
       - `Cloud backups (rclone)`
   - When selected, the tool:
     - lists `.bundle.tar` bundles on the remote with `rclone lsf`;
     - reads metadata/manifest via `rclone cat` (without downloading everything);
     - when you pick a backup, downloads it to `/tmp/proxsave` and proceeds with decrypt/restore.
   - If scanning times out (slow remote / huge directory), increase `RCLONE_TIMEOUT_CONNECTION` and retry.

2. **From a local rclone mount (restore-only)**  
   If you prefer to mount the rclone backend as a local filesystem:

   ```bash
   # Mount cloud storage locally
   rclone mount remote:bucket /mnt/cloud &

   # Configure in backup.env (restore-only scenario)
   CLOUD_ENABLED=false                      # cloud upload disabled
   # Use BACKUP_PATH / SECONDARY_PATH or browse the mount directly
   ```

   In questo caso puoi:
   - copiare i bundle dal mount (`/mnt/cloud/...`) nella cartella di backup locale;
   - oppure indicare il path montato quando il tool chiede il percorso dei backup
     (CLI) o sfogliare la directory montata prima di lanciare ProxSave.

---

**Q: What encryption is supported?**

A: AGE encryption only:
- **Passphrase-based**: Scrypt derivation (N=32768, r=8, p=1)
- **Key-based**: X25519 identity files

---

**Q: Where are temporary files stored?**

A: All in `/tmp/proxsave/`:
- `proxmox-decrypt-*/` - Decryption workspace (deleted after restore)
- `restore_TIMESTAMP.log` - Detailed restore log (preserved)
- `restore_backup_TIMESTAMP.tar.gz` - Safety backup (preserved)

**Cleanup**:
```bash
# Remove safety backup after successful restore
rm /tmp/proxsave/restore_backup_*.tar.gz

# Remove old logs
find /tmp/proxsave/ -name "restore_*.log" -mtime +7 -delete
```

---

## Additional Resources

**Related Documentation**:
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md) - Technical architecture and internals
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md) - Visual workflow diagrams
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md) - Advanced cluster disaster recovery
- [README.md](../README.md) - Main project documentation

**Proxmox Documentation**:
- [Proxmox VE Cluster Manager](https://pve.proxmox.com/wiki/Cluster_Manager)
- [Proxmox Backup Server Documentation](https://pbs.proxmox.com/docs/)

**Support**:
- Project Issues: [GitHub Issues](https://github.com/your-repo/proxsave/issues)
- Proxmox Forum: [forum.proxmox.com](https://forum.proxmox.com/)

---

## Summary

The restore workflow provides a **safe, interactive, and flexible** system for recovering Proxmox configurations:

✅ **Category-based** granular control
✅ **4 restore modes** for common scenarios
✅ **Safety backups** before any changes
✅ **Cluster-aware** service management
✅ **Export-only protection** for sensitive paths
✅ **Comprehensive logging** for audit trails
✅ **Multiple abort points** for user control

**Remember**:
- Always verify backups before disaster strikes
- Test restore procedures on non-production systems
- Isolate cluster nodes before cluster database restore
- Keep safety backups until restore is fully verified
- Review exported /etc/pve files manually

**Most Important**: Read and understand this guide BEFORE you need to restore!
