# Proxmox VE Cluster Recovery Guide

Advanced disaster recovery procedures for Proxmox VE cluster database restoration.

## Table of Contents

- [Overview](#overview)
- [Understanding PVE Cluster Architecture](#understanding-pve-cluster-architecture)
- [Recovery Scenarios](#recovery-scenarios)
- [Pre-Recovery Checklist](#pre-recovery-checklist)
- [Scenario 1: Single-Node Recovery](#scenario-1-single-node-recovery)
- [Scenario 2: Complete Cluster Rebuild](#scenario-2-complete-cluster-rebuild)
- [Scenario 3: Multi-Node Cluster with Failed Master](#scenario-3-multi-node-cluster-with-failed-master)
- [Scenario 4: Migration to New Hardware](#scenario-4-migration-to-new-hardware)
- [Scenario 5: Hostname Changed](#scenario-5-hostname-changed)
- [Post-Recovery Verification](#post-recovery-verification)
- [Common Issues and Solutions](#common-issues-and-solutions)
- [Emergency Recovery Procedures](#emergency-recovery-procedures)

---

## Overview

This guide covers **advanced cluster database recovery** using proxsave's restore functionality. These procedures should be performed by experienced administrators familiar with Proxmox VE clustering.

### When to Use This Guide

- **Complete node failure** requiring cluster database restoration
- **Cluster corruption** preventing normal operation
- **Hardware migration** of cluster node
- **Disaster recovery** from backup after catastrophic failure

### What This Guide Does NOT Cover

- Normal PVE backup/restore operations (use `vzdump`)
- VM/CT recovery (use Proxmox Backup Server or `pve-zsync`)
- Simple configuration changes (use web UI or `/etc/pve`)
- Initial cluster creation (use `pvecm create`)

---

## Understanding PVE Cluster Architecture

### Cluster Filesystem Components

```
┌─────────────────────────────────────────────────────────────┐
│                  Proxmox VE Cluster Stack                    │
└─────────────────────────────────────────────────────────────┘

Application Layer:
  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐
  │  pveproxy   │  │  pvedaemon  │  │  pvestatd   │
  │(Web UI/API) │  │ (API backend│  │ (Statistics)│
  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘
         │                │                │
         └────────────────┼────────────────┘
                          ↓
Cluster Filesystem Layer:
  ┌────────────────────────────────────────────────┐
  │           /etc/pve (FUSE Mount)                │
  │  - storage.cfg, datacenter.cfg, user.cfg       │
  │  - qemu-server/*.conf, lxc/*.conf              │
  │  - corosync.conf, etc.                         │
  └──────────────────┬─────────────────────────────┘
                     ↓ (managed by)
  ┌────────────────────────────────────────────────┐
  │          pmxcfs (Cluster Filesystem Daemon)    │
  │  - FUSE filesystem implementation              │
  │  - Corosync integration                        │
  │  - SQLite database backend                     │
  └──────────────────┬─────────────────────────────┘
                     ↓ (reads/writes)
Database Layer:
  ┌────────────────────────────────────────────────┐
  │     /var/lib/pve-cluster/config.db (SQLite)    │
  │  - Actual storage of cluster configuration     │
  │  - Local file on each node                     │
  │  - Synchronized via corosync                   │
  └────────────────────────────────────────────────┘
                     ↓ (synced by)
Communication Layer:
  ┌────────────────────────────────────────────────┐
  │              Corosync                          │
  │  - Cluster communication                       │
  │  - Quorum management                           │
  │  - Config.db synchronization                   │
  └────────────────────────────────────────────────┘
```

### Key Concepts

**config.db**:
- SQLite database at `/var/lib/pve-cluster/config.db`
- Contains entire cluster configuration
- Replicated to all nodes via corosync
- **This is what we restore**

**pmxcfs**:
- Daemon that mounts `/etc/pve` as FUSE filesystem
- Translates filesystem operations to config.db queries
- Provides consistent view across cluster

**/etc/pve**:
- **NOT** a real directory (it's a FUSE mount)
- View into config.db
- **Cannot be restored directly** (that's why export-only)
- Automatically populated when pmxcfs starts with restored config.db

**Corosync**:
- Synchronizes config.db changes across nodes
- Manages quorum (who can make changes)
- Configuration in `/etc/pve/corosync.conf`

### Why Special Handling Is Required

**Problem**: You cannot simply copy files to `/etc/pve`:
```bash
# ✗ This will NOT work:
cp backup/etc/pve/storage.cfg /etc/pve/storage.cfg
# Writes go to FUSE, not permanent storage
# Lost on pmxcfs restart
```

**Solution**: Restore `/var/lib/pve-cluster/config.db`:
```bash
# ✓ This works (with services stopped):
# 1. Stop pmxcfs and related services
# 2. Restore config.db from backup
# 3. Restart pmxcfs
# 4. /etc/pve automatically repopulated from restored config.db
```

---

## Recovery Scenarios

### Decision Tree

```
┌─────────────────────────────────────────────────┐
│ What is your situation?                         │
└─────────────────────────────────────────────────┘
                      │
         ┌────────────┴────────────┐
         │                         │
    Single-Node?            Multi-Node Cluster?
         │                         │
         │                    ┌────┴────┐
         │                    │         │
         │              All Nodes    Only One
         │               Failed?     Node Failed?
         │                    │         │
         ↓                    ↓         ↓
   Scenario 1           Scenario 2   Scenario 3
   (Simple)            (Complete    (Partial
                        Rebuild)     Recovery)
```

| Scenario | Description | Complexity | Data Loss Risk |
|----------|-------------|------------|----------------|
| 1. Single-Node | Standalone PVE node restore | Low | Low |
| 2. Complete Rebuild | All cluster nodes failed | High | Medium |
| 3. Failed Master | One node failed in multi-node | Medium | Low |
| 4. Hardware Migration | Move cluster to new hardware | Medium | Low |
| 5. Hostname Changed | Node hostname doesn't match backup | Medium | Low |

---

## Pre-Recovery Checklist

### Before Starting ANY Recovery

**□ 1. Verify Backup Integrity**
```bash
# List available backups
ls -lh /opt/proxsave/backup/*.bundle.tar

# Check backup date matches expected
# Verify encryption status
# Ensure you have decryption key/passphrase
```

**□ 2. Document Current State**
```bash
# If system partially working:
pvecm status > /root/pre-recovery-cluster-status.txt
pvesm status > /root/pre-recovery-storage-status.txt
qm list > /root/pre-recovery-vm-list.txt
pct list > /root/pre-recovery-ct-list.txt
```

**□ 3. Backup Current State** (even if broken)
```bash
# Manual backup of current config.db
tar -czf /root/current-config-db-backup.tar.gz /var/lib/pve-cluster/

# Backup current /etc/pve (if mounted)
rsync -av /etc/pve/ /root/current-etc-pve-backup/
```

**□ 4. Check Network Connectivity**
```bash
# Verify network is working
ping -c 3 8.8.8.8

# Check DNS
nslookup google.com

# Verify hostname resolution
hostname -f
```

**□ 5. Stop All VMs/CTs** (if possible)
```bash
# Stop all VMs
for vmid in $(qm list | awk 'NR>1 {print $1}'); do
    qm stop $vmid
done

# Stop all containers
for ctid in $(pct list | awk 'NR>1 {print $1}'); do
    pct stop $ctid
done
```

**□ 6. Isolate Node** (for multi-node clusters)
```bash
# Stop cluster communication
systemctl stop corosync

# Or disconnect from cluster network
# ip link set <cluster-interface> down
```

**□ 7. Verify Disk Space**
```bash
# Check free space on /
df -h /

# Check free space on /tmp
df -h /tmp

# Ensure at least 1GB free on both
```

**□ 8. Have Rollback Plan Ready**
```bash
# Document rollback procedure
echo "1. Restore from /root/current-config-db-backup.tar.gz" > /root/ROLLBACK.txt
echo "2. systemctl restart pve-cluster pvedaemon pveproxy" >> /root/ROLLBACK.txt
echo "3. pvecm status to verify" >> /root/ROLLBACK.txt
```

---

## Scenario 1: Single-Node Recovery

### Situation

- Standalone Proxmox VE node (no cluster)
- Node failed, needs cluster database restored
- No other nodes to coordinate with

### Complexity: ★☆☆☆☆ (Low)

### Prerequisites

- ✅ Backup available with `pve_cluster` category
- ✅ Root access to node
- ✅ Network connectivity working
- ✅ Hostname matches backup (or willing to change it)

### Procedure

#### Step 1: Pre-Recovery Verification

```bash
# 1. Verify you're on the target system
hostname
# Expected: Should match backup hostname

# 2. Check PVE installed
dpkg -l | grep proxmox-ve

# 3. Verify no cluster membership
pvecm status
# Expected: "cluster not ready - no quorum?" or similar
```

#### Step 2: Run Restore Workflow

```bash
# Navigate to proxsave directory
cd /opt/proxsave

# Run restore
./build/proxsave --restore
```

#### Step 3: Interactive Selection

```
Select backup source:
  [1] Primary backup path
→ Select: 1

Available backups:
  [1] backup-pve01-20251120-143052.bundle.tar
→ Select: 1

Backup is encrypted with AGE.
Enter AGE passphrase: ********
→ Enter your passphrase

Select restore mode:
  [1] FULL restore
  [2] STORAGE only    ← Recommended for cluster recovery
  [3] SYSTEM BASE only
  [4] CUSTOM selection
→ Select: 2 (STORAGE only)

RESTORE PLAN:
  • PVE Cluster Configuration
  • PVE Storage Configuration
  • PVE Backup Jobs
  • ZFS Configuration

Type "RESTORE" to proceed: RESTORE
→ Type: RESTORE
```

#### Step 4: Automated Process

```
Creating safety backup...
✓ Safety backup: /tmp/proxsave/restore_backup_20251120_143052.tar.gz

Preparing system for cluster database restore:
  Stopping pve-cluster... ✓
  Stopping pvedaemon... ✓
  Stopping pveproxy... ✓
  Stopping pvestatd... ✓
  Unmounting /etc/pve... ✓

Extracting selected categories...
✓ Restored 47 files/directories

Recreating storage directories...
✓ Storage directories recreated

Restarting services...
✓ pve-cluster started
✓ pvedaemon started
✓ pveproxy started
✓ pvestatd started

Restore completed successfully.
```

#### Step 5: Verification

```bash
# 1. Check services running
systemctl status pve-cluster pvedaemon pveproxy pvestatd
# Expected: All "active (running)"

# 2. Verify /etc/pve mounted
mount | grep pve
# Expected: /etc/pve type fuse.pmxcfs (...)

ls -la /etc/pve/
# Expected: storage.cfg, datacenter.cfg, nodes/, etc.

# 3. Check cluster status
pvecm status
# Expected: Single node cluster, quorate

# 4. Verify storage accessible
pvesm status
# Expected: All storage shows "active"

# 5. Check API working
pvesh get /version
# Expected: Version info displayed

# 6. Access web interface
# https://<node-ip>:8006
# Expected: Login page appears, can login
```

#### Step 6: Post-Recovery Tasks

```bash
# 1. Verify VM/CT configurations present
ls -la /etc/pve/qemu-server/
ls -la /etc/pve/lxc/

# 2. Start VMs/CTs if needed
# qm start <vmid>
# pct start <ctid>

# 3. Remove safety backup (after thorough verification)
rm /tmp/proxsave/restore_backup_*.tar.gz

# 4. Update backups schedule if needed
cat /etc/pve/vzdump.cron
```

### Success Criteria

✅ All services running
✅ `/etc/pve` mounted and populated
✅ `pvecm status` shows healthy single-node cluster
✅ Storage accessible
✅ Web interface working
✅ VM/CT configs visible

---

## Scenario 2: Complete Cluster Rebuild

### Situation

- All nodes in cluster have failed
- Need to rebuild entire cluster from backup
- Want to restore cluster configuration

### Complexity: ★★★★☆ (High)

### Prerequisites

- ✅ Complete backup of one cluster node
- ✅ Fresh PVE installed on all nodes (or clean nodes)
- ✅ Nodes have correct hostnames
- ✅ Network connectivity between nodes
- ✅ Time synchronization working (NTP)

### Procedure

#### Phase 1: Restore Primary Node

**On PRIMARY NODE** (choose one to be master):

```bash
# 1. Verify hostname matches backup
hostname
# If different, change it:
# hostnamectl set-hostname <backup-hostname>
# reboot

# 2. Run restore (STORAGE or FULL mode)
cd /opt/proxsave
./build/proxsave --restore
# Select: [2] STORAGE only
# This restores cluster config, storage, ZFS

# 3. Verify primary node working
pvecm status
# Should show single-node cluster

# 4. Check corosync configuration
cat /etc/pve/corosync.conf
# Note: May reference old dead nodes
```

#### Phase 2: Clean Corosync Configuration

**On PRIMARY NODE**:

```bash
# 1. Edit corosync.conf to remove dead nodes
vi /etc/pve/corosync.conf

# Example before:
# nodelist {
#   node {
#     name: pve01
#     nodeid: 1
#     quorum_votes: 1
#     ring0_addr: 192.168.1.101
#   }
#   node {
#     name: pve02   ← Dead node
#     nodeid: 2
#     quorum_votes: 1
#     ring0_addr: 192.168.1.102
#   }
# }

# Example after (if rebuilding fresh):
# nodelist {
#   node {
#     name: pve01
#     nodeid: 1
#     quorum_votes: 1
#     ring0_addr: 192.168.1.101
#   }
# }

# 2. Update expected votes for single node
# Update quorum section:
# quorum {
#   provider: corosync_votequorum
#   expected_votes: 1   ← Change to 1
# }

# 3. Restart corosync and cluster
systemctl restart corosync
systemctl restart pve-cluster pvedaemon pveproxy

# 4. Set expected votes
pvecm expected 1

# 5. Verify primary healthy
pvecm status
# Should show: Quorate, 1 node
```

#### Phase 3: Add Secondary Nodes

**On SECONDARY NODES** (pve02, pve03, etc.):

```bash
# 1. Ensure fresh PVE installation
# - No existing cluster membership
# - No data in /etc/pve or /var/lib/pve-cluster/

# 2. Set correct hostname
hostnamectl set-hostname pve02  # Or pve03, etc.

# 3. Configure network
vi /etc/network/interfaces
# Ensure IP matches cluster network

# 4. Test connectivity to primary
ping 192.168.1.101  # Primary node IP

# 5. Join cluster
pvecm add 192.168.1.101  # Primary node IP
# Enter root password of primary node

# Expected output:
# Establishing API connection with host '192.168.1.101'
# Login succeeded!
# Node successfully joined cluster
```

**On PRIMARY NODE** (after each secondary joins):

```bash
# Verify node joined
pvecm nodes
# Should show all nodes

pvecm status
# Should show correct node count and quorum
```

#### Phase 4: Restore Storage on Secondary Nodes

**On EACH SECONDARY NODE**:

```bash
# Check if storage directories exist
pvesm status
# Some storage may show as "inactive" if paths don't exist

# Create missing storage directories (if needed)
mkdir -p /mnt/backup
mkdir -p /mnt/backup/dump
# ... etc, based on storage.cfg

# Or run directory recreation from backup
# (if you restored on secondary nodes too)
```

#### Phase 5: Verify Cluster Health

**On ANY NODE**:

```bash
# 1. Check cluster status
pvecm status
# Expected:
# - Nodes: <correct count>
# - Quorate: Yes
# - All nodes online

# 2. Check corosync
corosync-quorumtool -s
# Expected: All nodes visible, quorum achieved

# 3. Verify storage
pvesm status
# Expected: All storage "active" on appropriate nodes

# 4. Check logs for errors
journalctl -u corosync --since "30 minutes ago"
journalctl -u pve-cluster --since "30 minutes ago"
# Expected: No errors, clean communication

# 5. Verify HA (if used)
ha-manager status
# Expected: Service running, nodes visible
```

#### Phase 6: Restore VMs/CTs

**VM/CT configs are in restored /etc/pve**, but disk images need restoration:

```bash
# 1. Check VM configs present
qm config 100
pct config 200

# 2. Restore VM/CT disk images
# Option A: From Proxmox Backup Server
# pbs-restore <backup-id> <vmid>

# Option B: From vzdump backups
# qmrestore /path/to/vzdump-qemu-100.vma.zst 100

# Option C: From ZFS replication
# zfs recv <pool>/<dataset> < backup.zfs
```

### Success Criteria

✅ All nodes show in `pvecm nodes`
✅ Cluster is quorate
✅ Corosync communication working
✅ Storage accessible on all nodes
✅ `/etc/pve` syncing across nodes (test by editing file)
✅ Web interface accessible on all nodes
✅ VMs/CTs can be migrated between nodes

---

## Scenario 3: Multi-Node Cluster with Failed Master

### Situation

- Multi-node cluster running
- One node (with backup) failed completely
- Other nodes still operational
- Need to restore failed node

### Complexity: ★★★☆☆ (Medium)

### Prerequisites

- ✅ Other cluster nodes still running
- ✅ Cluster has quorum without failed node
- ✅ Backup of failed node available
- ✅ Fresh PVE installed on replacement hardware

### Procedure

#### Step 1: Remove Dead Node from Cluster

**On ANY WORKING NODE**:

```bash
# 1. Check current cluster state
pvecm nodes
# Note the dead node's name (pve02)

# 2. Remove dead node from cluster
pvecm delnode pve02
# Expected: Node removed from cluster

# 3. Verify removal
pvecm nodes
# Dead node should not appear

# 4. Update expected votes if needed
pvecm expected <new-number-of-nodes>
```

#### Step 2: Prepare Replacement Node

**On REPLACEMENT NODE**:

```bash
# 1. Install fresh Proxmox VE
# (Standard installation)

# 2. Set hostname to DIFFERENT name than dead node
# (Using same hostname causes conflicts)
hostnamectl set-hostname pve02-new

# 3. Configure network
# Ensure connectivity to cluster network
```

#### Step 3: Join Replacement Node to Cluster

**On REPLACEMENT NODE**:

```bash
# Join existing cluster (do NOT restore cluster config yet)
pvecm add <working-node-ip>
# Enter root password

# Expected: Node joins cluster, gets current config from existing nodes
```

#### Step 4: Verify Basic Cluster Functionality

**On REPLACEMENT NODE**:

```bash
# 1. Check cluster status
pvecm status
# Expected: Shows as part of cluster, quorate

# 2. Verify /etc/pve syncing
ls -la /etc/pve/
# Expected: Sees existing cluster configuration

# 3. Check storage
pvesm status
# Expected: Sees cluster storage
```

#### Step 5: Selective Restoration (If Needed)

**On REPLACEMENT NODE** (optional - if you need node-specific configs):

```bash
# Use CUSTOM mode to restore only specific categories
cd /opt/proxsave
./build/proxsave --restore

# Select: [4] CUSTOM selection
# Toggle:
#   - [ ] PVE Cluster Configuration  ← DO NOT select (already from cluster)
#   - [X] Network Configuration      ← Select if needed
#   - [X] SSH Configuration           ← Select if needed
#   - [X] Custom Scripts              ← Select if needed
#   - [X] ZFS Configuration           ← Select if node had ZFS

# Type "RESTORE" to proceed
```

**Important**: Do NOT restore `pve_cluster` category - node is already in working cluster!

#### Step 6: Migrate VMs/CTs to Replacement Node

**On ANY CLUSTER NODE**:

```bash
# Migrate VMs to replacement node
qm migrate <vmid> pve02-new --online
# Or offline migration:
# qm migrate <vmid> pve02-new

# Migrate containers
pct migrate <ctid> pve02-new --restart
```

#### Step 7: Cleanup

**On REPLACEMENT NODE** (after verification):

```bash
# If you want to rename node back to original name
# (Only if all VMs/CTs migrated off):

# 1. Leave cluster
pvecm delnode pve02-new

# 2. Change hostname
hostnamectl set-hostname pve02
reboot

# 3. Re-join cluster
pvecm add <working-node-ip>
```

### Success Criteria

✅ Replacement node shows in `pvecm nodes`
✅ Cluster is quorate
✅ Storage accessible on replacement node
✅ Can migrate VMs/CTs to/from replacement node
✅ No errors in `journalctl -u corosync`

---

## Scenario 4: Migration to New Hardware

### Situation

- Migrating existing node to new hardware
- Want to preserve cluster configuration
- Clean installation on new hardware

### Complexity: ★★★☆☆ (Medium)

### Prerequisites

- ✅ Backup from old hardware
- ✅ New hardware with PVE installed
- ✅ Network connectivity configured
- ✅ Old hardware shut down (to avoid conflicts)

### Procedure

#### Step 1: Preparation

**On OLD HARDWARE** (before shutdown):

```bash
# 1. Create fresh backup
cd /opt/proxsave
./build/proxsave

# 2. Document configuration
pvecm status > /root/old-cluster-status.txt
pvesm status > /root/old-storage-status.txt
ip addr show > /root/old-network-config.txt

# 3. Copy backup to new hardware
scp /opt/proxsave/backup/*.bundle.tar root@new-hardware:/root/

# 4. Shut down (but keep around for emergencies)
shutdown -h now
```

**On NEW HARDWARE**:

```bash
# 1. Install Proxmox VE
# (Fresh installation)

# 2. Set SAME hostname as old hardware
hostnamectl set-hostname <old-hostname>

# 3. Configure SAME IP address
vi /etc/network/interfaces
# Set same IP as old hardware

# 4. Reboot to apply network config
reboot
```

#### Step 2: Restore on New Hardware

```bash
# 1. Copy proxsave tool to new hardware
# scp -r /opt/proxsave root@new-hardware:/opt/

# 2. Place backup in expected location
mkdir -p /opt/proxsave/backup
mv /root/*.bundle.tar /opt/proxsave/backup/

# 3. Run restore
cd /opt/proxsave
./build/proxsave --restore

# Select: [2] STORAGE only (or FULL)
# Type "RESTORE" to confirm
```

#### Step 3: Verify Basic Functionality

```bash
# 1. Check services
systemctl status pve-cluster pvedaemon pveproxy

# 2. Verify /etc/pve
ls -la /etc/pve/

# 3. Check cluster status
pvecm status
# Expected: Single-node cluster (if standalone)
# Or: Part of multi-node cluster (if rejoining)

# 4. Verify storage
pvesm status
```

#### Step 4: Hardware-Specific Adjustments

**Storage Paths**:
```bash
# If storage paths different on new hardware:
vi /etc/pve/storage.cfg
# Update paths to match new hardware

# Create missing directories
mkdir -p /new/path/to/storage
mkdir -p /new/path/to/storage/dump
```

**Network Interfaces**:
```bash
# If interface names different:
vi /etc/network/interfaces
# Update interface names (eth0 vs ens18, etc.)

# Restart networking
systemctl restart networking
```

**ZFS Pools**:
```bash
# If ZFS pool names different:
# 1. Import with new name
zpool import <old-name> <new-name>

# 2. Update storage.cfg
vi /etc/pve/storage.cfg
# Update pool: <old-name> to pool: <new-name>

# 3. Update systemd unit if needed
systemctl enable zfs-import@<new-name>.service
```

#### Step 5: Rejoin Cluster (if multi-node)

**If node was part of multi-node cluster**:

```bash
# On other nodes: Remove old node first
pvecm delnode <old-hostname>

# On new hardware: Join cluster
pvecm add <existing-node-ip>
```

#### Step 6: Restore VM/CT Disk Images

```bash
# VMs and CTs configs are restored, but disk images need restoration:

# Option 1: Copy from old hardware disks
# (If you can mount old disks)
zfs send old-pool/vm-100-disk-0 | zfs recv new-pool/vm-100-disk-0

# Option 2: Restore from backup server
# qmrestore /path/to/backup.vma.zst 100

# Option 3: Create new VMs, attach restored disks
```

### Success Criteria

✅ Services running on new hardware
✅ Cluster configuration restored
✅ Storage accessible (with path adjustments)
✅ Network connectivity working
✅ Web interface accessible
✅ Can create/manage VMs/CTs

---

## Scenario 5: Hostname Changed

### Situation

- Need to restore backup to system with different hostname
- Backup from `pve01`, restoring to `pve02`
- May cause cluster conflicts

### Complexity: ★★☆☆☆ (Medium)

### Option A: Change Hostname to Match Backup

**Recommended if**: You want exact restoration

```bash
# 1. Change hostname before restore
hostnamectl set-hostname <backup-hostname>

# 2. Update /etc/hosts
vi /etc/hosts
# Update hostname references

# 3. Reboot
reboot

# 4. Run restore normally
cd /opt/proxsave
./build/proxsave --restore
```

### Option B: Update Configuration After Restore

**Recommended if**: You want to keep new hostname

#### Step 1: Restore with Mismatched Hostname

```bash
# Run restore (will work despite hostname mismatch)
cd /opt/proxsave
./build/proxsave --restore

# Select: [2] STORAGE only
# Continue even though hostname differs
```

#### Step 2: Fix Corosync Configuration

```bash
# 1. Edit corosync.conf
vi /etc/pve/corosync.conf

# Find nodelist section:
# nodelist {
#   node {
#     name: pve01      ← Change this
#     nodeid: 1
#     quorum_votes: 1
#     ring0_addr: 192.168.1.101
#   }
# }

# Update to new hostname:
# nodelist {
#   node {
#     name: pve02      ← New hostname
#     nodeid: 1
#     quorum_votes: 1
#     ring0_addr: 192.168.1.101  ← Update IP if needed
#   }
# }

# 2. Restart services
systemctl restart corosync
systemctl restart pve-cluster pvedaemon pveproxy
```

#### Step 3: Update Node Directory

```bash
# 1. Check /etc/pve/nodes/
ls -la /etc/pve/nodes/
# Will show old hostname directory

# 2. The cluster filesystem should auto-create new hostname directory
# But you may need to migrate configs:

# 3. Copy node-specific configs (if needed)
cp -r /etc/pve/nodes/pve01/* /etc/pve/nodes/pve02/

# 4. Verify new directory
ls -la /etc/pve/nodes/pve02/
```

#### Step 4: Update Certificates

```bash
# Regenerate certificates for new hostname
pvecm updatecerts

# Restart services
systemctl restart pvedaemon pveproxy
```

#### Step 5: Remove Old Node References

```bash
# If single-node cluster, remove old node:
pvecm delnode pve01

# Verify
pvecm nodes
# Should show only new hostname
```

### Success Criteria

✅ `pvecm status` shows new hostname
✅ Corosync.conf references new hostname
✅ Certificates match new hostname
✅ `/etc/pve/nodes/<new-hostname>/` exists
✅ Web interface accessible

---

## Post-Recovery Verification

### Comprehensive Health Check

Run these commands after ANY recovery scenario:

#### 1. Service Status

```bash
# Check all PVE services
systemctl status pve-cluster pvedaemon pveproxy pvestatd
# Expected: All "active (running)"

# Check for failed services
systemctl --failed
# Expected: No failed units
```

#### 2. Cluster Status

```bash
# Detailed cluster status
pvecm status

# Expected output:
# Cluster information
# -------------------
# Name:             <cluster-name>
# Config Version:   X
# Transport:        knet
# Secure auth:      on
#
# Quorum information
# ------------------
# Date:             ...
# Quorum provider:  corosync_votequorum
# Nodes:            X
# Expected votes:   X
# Total votes:      X
# Quorum:           X
# Flags:            Quorate ← MUST show "Quorate"

# Check nodes
pvecm nodes
# Expected: All nodes listed, online
```

#### 3. Filesystem Check

```bash
# Verify /etc/pve mounted
mount | grep pve
# Expected: /etc/pve type fuse.pmxcfs (rw,...)

# Check contents
ls -la /etc/pve/
# Expected: All config files present
# - storage.cfg
# - datacenter.cfg
# - user.cfg
# - nodes/
# - qemu-server/
# - lxc/

# Verify file sync (multi-node clusters)
# On node 1: echo "test" > /etc/pve/test.txt
# On node 2: cat /etc/pve/test.txt
# Expected: File visible on all nodes instantly
```

#### 4. Storage Verification

```bash
# Check storage status
pvesm status
# Expected: All storage "active"

# Check storage paths exist
for storage in $(pvesm status | awk 'NR>1 {print $1}'); do
    echo "Checking $storage:"
    pvesm path $storage
done

# Verify ZFS pools (if applicable)
zpool status
# Expected: All pools "ONLINE"

zpool list
# Expected: Correct capacity, no errors
```

#### 5. Network Verification

```bash
# Check cluster network
ip addr show
# Verify cluster interface has correct IP

# Test connectivity to other nodes (multi-node)
for node in pve02 pve03; do
    ping -c 3 $node
done

# Check corosync communication
corosync-cmapctl | grep members
# Expected: All nodes listed
```

#### 6. API and Web Interface

```bash
# Test API
pvesh get /version
# Expected: Version info

pvesh get /nodes
# Expected: Node list with status "online"

pvesh get /storage
# Expected: Storage list

# Test web interface
curl -k https://localhost:8006
# Expected: HTML response (login page)

# Full login test
# Browse to: https://<node-ip>:8006
# Login with: root@pam
# Verify: Dashboard loads, shows correct node/cluster info
```

#### 7. VM/CT Configuration

```bash
# List VMs
qm list
# Expected: All VMs listed (may show "unknown" if disk images not restored)

# Check specific VM config
qm config 100
# Expected: Full config displayed

# List containers
pct list
# Expected: All CTs listed

# Check specific CT config
pct config 200
# Expected: Full config displayed
```

#### 8. Log Analysis

```bash
# Check for errors in cluster log
journalctl -u pve-cluster --since "1 hour ago" | grep -i error
# Expected: No critical errors

# Check corosync logs
journalctl -u corosync --since "1 hour ago" | grep -i error
# Expected: No critical errors

# Check pvedaemon logs
journalctl -u pvedaemon --since "1 hour ago" | grep -i error
# Expected: No critical errors
```

#### 9. HA Status (if using HA)

```bash
# Check HA manager
ha-manager status
# Expected: Service running

# List HA resources
ha-manager config
# Expected: All HA resources listed

# Check HA group configuration
cat /etc/pve/ha/groups.cfg
cat /etc/pve/ha/resources.cfg
```

#### 10. Backup Jobs

```bash
# Check backup schedules
cat /etc/pve/vzdump.cron
# Expected: Backup jobs listed

# Check backup job configurations
cat /etc/pve/jobs.cfg
# Expected: Backup jobs configured

# Test manual backup
vzdump 100 --mode snapshot --storage local
# Expected: Backup completes successfully
```

### Automated Verification Script

Save as `/root/verify-cluster-recovery.sh`:

```bash
#!/bin/bash
# Cluster Recovery Verification Script

echo "=== Proxmox Cluster Recovery Verification ==="
echo "Started: $(date)"
echo

# 1. Services
echo "1. Checking services..."
systemctl is-active pve-cluster >/dev/null 2>&1 && echo "  ✓ pve-cluster running" || echo "  ✗ pve-cluster FAILED"
systemctl is-active pvedaemon >/dev/null 2>&1 && echo "  ✓ pvedaemon running" || echo "  ✗ pvedaemon FAILED"
systemctl is-active pveproxy >/dev/null 2>&1 && echo "  ✓ pveproxy running" || echo "  ✗ pveproxy FAILED"
echo

# 2. Cluster Status
echo "2. Checking cluster..."
if pvecm status | grep -q "Quorate.*Yes"; then
    echo "  ✓ Cluster is quorate"
else
    echo "  ✗ Cluster NOT quorate"
fi
echo

# 3. Filesystem
echo "3. Checking /etc/pve..."
if mount | grep -q "/etc/pve type fuse.pmxcfs"; then
    echo "  ✓ /etc/pve mounted"
else
    echo "  ✗ /etc/pve NOT mounted"
fi
echo

# 4. Storage
echo "4. Checking storage..."
if pvesm status >/dev/null 2>&1; then
    echo "  ✓ Storage accessible"
    INACTIVE=$(pvesm status | grep -c "inactive" || true)
    if [ "$INACTIVE" -gt 0 ]; then
        echo "  ⚠ $INACTIVE storage(s) inactive"
    fi
else
    echo "  ✗ Storage check FAILED"
fi
echo

# 5. API
echo "5. Checking API..."
if pvesh get /version >/dev/null 2>&1; then
    echo "  ✓ API responding"
else
    echo "  ✗ API NOT responding"
fi
echo

# 6. Summary
echo "=== Summary ==="
FAILED=$(systemctl --failed --no-legend | wc -l)
if [ "$FAILED" -eq 0 ]; then
    echo "  ✓ No failed services"
else
    echo "  ✗ $FAILED failed service(s)"
    systemctl --failed --no-legend
fi

echo
echo "Completed: $(date)"
```

Usage:
```bash
chmod +x /root/verify-cluster-recovery.sh
/root/verify-cluster-recovery.sh
```

---

## Common Issues and Solutions

### Issue: Services Won't Start

**Symptoms**:
```bash
systemctl status pve-cluster
# Output: failed (code=exited, status=1/FAILURE)
```

**Diagnosis**:
```bash
journalctl -xe -u pve-cluster
# Look for specific error messages
```

**Common Causes & Solutions**:

**1. config.db corrupted**:
```bash
# Rollback to safety backup
systemctl stop pve-cluster
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /
systemctl start pve-cluster
```

**2. /etc/pve still mounted**:
```bash
umount -f /etc/pve
systemctl restart pve-cluster
```

**3. Permissions wrong**:
```bash
chown -R root:www-data /var/lib/pve-cluster
chmod 0640 /var/lib/pve-cluster/config.db
systemctl restart pve-cluster
```

---

### Issue: Lost Quorum

**Symptoms**:
```bash
pvecm status
# Output: "not quorate"
```

**For Single-Node Cluster**:
```bash
pvecm expected 1
pvecm status
# Should now show "quorate"
```

**For Multi-Node Cluster**:
```bash
# Check how many nodes are online
pvecm nodes

# Set expected votes to number of online nodes
pvecm expected <number-of-online-nodes>

# Example: 3-node cluster, 2 nodes online:
pvecm expected 2
```

---

### Issue: /etc/pve Not Syncing

**Symptoms**:
- Changes on one node don't appear on others
- File modifications don't persist

**Solutions**:

**1. Check corosync communication**:
```bash
corosync-cfgtool -s
# Should show all nodes

corosync-quorumtool -l
# Should show all nodes with votes
```

**2. Restart cluster services**:
```bash
systemctl restart corosync
systemctl restart pve-cluster
```

**3. Check network connectivity**:
```bash
# Test cluster network
for node in pve02 pve03; do
    ping -c 3 $node
done

# Check firewall rules
iptables -L -n | grep -E "(5404|5405)"
# Corosync ports should not be blocked
```

**4. Force config sync**:
```bash
# On working node:
systemctl restart pve-cluster

# On problem node:
systemctl stop pve-cluster
rm -f /var/lib/pve-cluster/.pmxcfs.lockfile
systemctl start pve-cluster
```

---

### Issue: Storage Not Accessible

**Symptoms**:
```bash
pvesm status
# Shows storage as "inactive"
```

**Solutions**:

**1. Check storage paths**:
```bash
# For directory storage
ls -la /mnt/backup
# If doesn't exist: mkdir -p /mnt/backup

# For NFS
mount | grep nfs
# If not mounted: mount -t nfs server:/export /mnt/nfs
```

**2. Check ZFS pools**:
```bash
zpool status
# If not imported:
zpool import <pool-name>
systemctl start zfs-import@<pool-name>.service
```

**3. Fix permissions**:
```bash
chown root:root /mnt/storage
chmod 755 /mnt/storage
```

**4. Verify storage.cfg**:
```bash
cat /etc/pve/storage.cfg
# Check paths are correct
# Check disabled: 0 (not disabled)
```

---

### Issue: Web Interface Not Accessible

**Symptoms**:
- Cannot access https://node-ip:8006
- Connection refused or timeout

**Solutions**:

**1. Check pveproxy service**:
```bash
systemctl status pveproxy
# If not running:
systemctl start pveproxy
```

**2. Check listening port**:
```bash
ss -tlnp | grep 8006
# Should show pveproxy listening
```

**3. Check firewall**:
```bash
iptables -L INPUT -n -v | grep 8006
# If blocked, allow:
iptables -I INPUT -p tcp --dport 8006 -j ACCEPT
```

**4. Check certificates**:
```bash
pvecm updatecerts
systemctl restart pveproxy
```

**5. Test locally**:
```bash
curl -k https://localhost:8006
# Should return HTML
```

---

## Emergency Recovery Procedures

### Emergency: Complete System Corruption

**Situation**: Everything broken, cluster services won't start, /etc/pve won't mount

#### Nuclear Option 1: Force Restore from Safety Backup

```bash
# 1. Stop everything
systemctl stop pve-cluster pvedaemon pveproxy pvestatd
killall pmxcfs 2>/dev/null

# 2. Force unmount /etc/pve
umount -f /etc/pve 2>/dev/null
fusermount -uz /etc/pve 2>/dev/null

# 3. Restore from safety backup
tar -xzf /tmp/proxsave/restore_backup_*.tar.gz -C /

# 4. Restart services
systemctl start pve-cluster
sleep 5
systemctl start pvedaemon pveproxy pvestatd

# 5. Verify
pvecm status
```

#### Nuclear Option 2: Manual config.db Restoration

```bash
# 1. Stop and disable services
systemctl stop pve-cluster pvedaemon pveproxy pvestatd
systemctl disable pve-cluster

# 2. Force unmount
umount -f /etc/pve 2>/dev/null

# 3. Backup current (broken) config
mv /var/lib/pve-cluster /var/lib/pve-cluster.broken

# 4. Manually extract config.db from backup
mkdir -p /var/lib/pve-cluster
tar -xzf /path/to/backup.bundle.tar --strip-components=3 \
    -C /var/lib/pve-cluster \
    ./var/lib/pve-cluster/

# 5. Fix permissions
chown -R root:www-data /var/lib/pve-cluster
chmod 0640 /var/lib/pve-cluster/config.db

# 6. Re-enable and start
systemctl enable pve-cluster
systemctl start pve-cluster
sleep 5
systemctl start pvedaemon pveproxy pvestatd
```

#### Nuclear Option 3: Fresh Start with Config Import

```bash
# 1. Backup everything
tar -czf /root/emergency-backup-$(date +%s).tar.gz \
    /etc/pve/ /var/lib/pve-cluster/ 2>/dev/null || true

# 2. Remove cluster completely
pvecm delnode $(hostname)  # If multi-node
apt remove --purge proxmox-ve pve-cluster pvedaemon pveproxy

# 3. Reinstall cluster components
apt install proxmox-ve

# 4. Restore selective configs manually
# Use export-only /etc/pve contents from backup
# Copy configs one by one after reviewing

# 5. Recreate cluster (if multi-node)
pvecm create <cluster-name>  # On first node
pvecm add <first-node-ip>     # On other nodes
```

---

### Emergency: Split-Brain Scenario

**Situation**: Multi-node cluster with conflicting state

**Symptoms**:
- Different nodes show different quorum status
- Some nodes can't see others
- Corosync shows duplicate node IDs

**Resolution**:

```bash
# On ALL nodes:

# 1. Stop cluster communication
systemctl stop corosync pve-cluster

# 2. Choose ONE node as "source of truth" (primary)

# On PRIMARY node:
# 3. Start services
systemctl start corosync pve-cluster
pvecm expected 1

# On SECONDARY nodes:
# 4. Remove old cluster data
rm -rf /etc/pve /etc/corosync
rm -rf /var/lib/pve-cluster/*

# 5. Rejoin cluster
pvecm add <primary-node-ip>

# 6. Verify all nodes synced
pvecm status  # On all nodes
# Should show consistent state
```

---

## Appendix

### Quick Reference Commands

```bash
# Cluster Status
pvecm status              # Overall cluster status
pvecm nodes               # List cluster nodes
pvecm expected <N>        # Set expected votes

# Corosync
corosync-cfgtool -s       # Corosync status
corosync-quorumtool -s    # Quorum status
corosync-quorumtool -l    # Quorum list

# Services
systemctl restart pve-cluster pvedaemon pveproxy
systemctl status pve-cluster
journalctl -u pve-cluster --since "1 hour ago"

# Filesystem
mount | grep pve          # Check /etc/pve mount
ls -la /etc/pve/          # List cluster configs
umount -f /etc/pve        # Force unmount

# Storage
pvesm status              # Storage status
pvesm scan zfs            # Scan ZFS pools
pvesm scan nfs <server>   # Scan NFS exports

# HA
ha-manager status         # HA service status
ha-manager config         # HA resources

# Backup
vzdump --all              # Backup all VMs/CTs
```

### Useful File Locations

```bash
# Cluster Configuration
/etc/pve/                           # FUSE mount (cluster config view)
/var/lib/pve-cluster/config.db      # Actual cluster database
/etc/pve/corosync.conf              # Corosync configuration
/etc/pve/storage.cfg                # Storage definitions
/etc/pve/datacenter.cfg             # Datacenter settings

# Node-Specific
/etc/pve/nodes/<hostname>/          # Node-specific configs
/etc/pve/priv/                      # Private keys and certificates

# VM/CT Configs
/etc/pve/qemu-server/<vmid>.conf    # VM configurations
/etc/pve/lxc/<ctid>.conf            # Container configurations

# Logs
/var/log/pve/tasks/                 # Task logs
journalctl -u pve-cluster           # Cluster service logs
journalctl -u corosync              # Corosync logs
```

### Support Resources

- **Proxmox VE Documentation**: https://pve.proxmox.com/wiki/
- **Proxmox Forum**: https://forum.proxmox.com/
- **Cluster Manager**: https://pve.proxmox.com/wiki/Cluster_Manager
- **Emergency Recovery**: https://pve.proxmox.com/wiki/Recover_From_Corrupted_Configurations

---

## Final Notes

**Remember**:
- ✅ Always test recovery procedures on non-production systems first
- ✅ Keep multiple backup copies in different locations
- ✅ Document your specific cluster setup and customizations
- ✅ Verify backups regularly (don't wait for disaster)
- ✅ Practice recovery scenarios at least annually
- ✅ Keep this guide accessible (print or offline copy)

**When in Doubt**:
1. Stop and document current state
2. Create additional backups
3. Test procedure on non-production system
4. Ask for help on Proxmox forums
5. Better to be slow and safe than fast and corrupted

Good luck with your recovery! 🍀
