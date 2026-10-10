# Proxmox VE Cluster Recovery Guide

Advanced disaster recovery procedures for Proxmox VE cluster database restoration.

## Table of Contents

- [Choose a cluster recovery procedure](#recover-a-proxmox-cluster)
- [Overview](#overview)
- [Starting a restore](#starting-a-restore)
- [Understanding PVE Cluster Architecture](#understanding-pve-cluster-architecture)
- [Cluster restore modes: SAFE vs RECOVERY](#cluster-restore-modes-safe-vs-recovery)
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

<!-- site-region: cluster-recovery:start -->

## Recover a Proxmox cluster

Start by deciding whether the target is standalone, an isolated surviving member, a fresh replacement joining a healthy cluster, or the first node of a complete rebuild. Those are different recovery states. Configuration restoration does not recover VM/CT disks, shared storage data or PBS chunks, and a service starting does not prove correct membership or quorum.

### Establish the recovery boundary

Keep console or IPMI access, recover the complete backup and decryption credentials independently, and record the expected hostname, node membership and network/storage layout. Confirm which peers are alive before any destructive database operation. Isolate the intended recovery target and account for HA workloads and fencing. Never rename a live member, remove yourself with `pvecm delnode`, delete the pmxcfs tree or use a blind expected-votes override to make a restore proceed.

Run `proxsave` without arguments as root on an interactive console and choose **Tools > Restore**. The workflow verifies the archive, decrypts if needed, lists available categories and asks for scope. Direct entry for consoles where the dashboard cannot render is documented in [CLI reference](CLI_REFERENCE.md#restore-from-backup); it remains interactive.

### Choose the complete scenario before applying

| Situation | Canonical procedure | Boundary |
| --- | --- | --- |
| Failed standalone PVE host | [Single-Node Recovery](#scenario-1-single-node-recovery) | Confirm that the destination is truly standalone; an isolated member is not the same state. |
| Entire cluster lost | [Complete Cluster Rebuild](#scenario-2-complete-cluster-rebuild) | Recover the first isolated node deliberately, then rebuild membership through the reviewed joining procedure. Do not restore an independent database on each joined node. |
| Healthy peers remain and one node failed | [Multi-Node Cluster with Failed Master](#scenario-3-multi-node-cluster-with-failed-master) | Use a clean replacement and the healthy cluster's current configuration. Optional ProxSave restore is Custom and excludes `pve_cluster`; node-local network changes need console review. |
| New hardware | [Migration to New Hardware](#scenario-4-migration-to-new-hardware) | Choose standalone, isolated-member or clean-join recovery before selecting scope. Validate disk identity, mounts and device assignments on the destination. |
| Different destination hostname | [Hostname Changed](#scenario-5-hostname-changed) | Do not rename a live member. On a fresh or isolated destination, review SAFE exported-node selection and live guest ownership. |

Each linked runbook contains its prerequisites, external Proxmox steps, dashboard choices and verification. Follow it as a complete procedure rather than combining commands from different scenarios. The [pre-recovery checklist](#pre-recovery-checklist) is required preparation; official membership procedures remain external Proxmox operations.

### SAFE and RECOVERY are separate from scope

The mandatory cluster-mode selector appears when a selected `pve_cluster` payload is present. Full and Storage can reach it; Custom reaches it only with that category selected. System base does not select it. Full includes export-only categories, while Storage and System base exclude them. Include `pve_config_export` when you need backed-up guest definitions for SAFE application.

SAFE does not replace the archived `config.db` or stop the PVE cluster services. Confirmed storage, datacenter, pool, mapping and guest applies nevertheless change live state, sometimes across the cluster. Guest confirmation concerns the eligible batch from the chosen exported source node, not an individual selection for every VM. Source-node selection does not transfer ownership: cluster-wide guest inventory must be readable and unambiguous, and VMIDs owned by another node or with another guest type are skipped. A file fallback for an existing guest needs a recognized API schema refusal and verified stopped status; other API errors do not authorize it. Without `pvesh`, SAFE applies are skipped. Review [full SAFE behavior](#cluster-restore-modes-safe-vs-recovery) before accepting these changes.

RECOVERY overwrites `/var/lib/pve-cluster/` while pmxcfs is stopped. Use it only on an offline or isolated destination under the chosen runbook. A quorate cluster with more than one online node is refused before services stop or files are written. Unreadable quorum or node counts allow continuation only when corosync is confirmed inactive or failed; active, starting or unreadable corosync is refused. A node without either corosync configuration path is treated as standalone. Passing a software guard does not replace verifying the scenario and peer isolation.

### Service and partial-failure safeguards

RECOVERY stops HA first: `pve-ha-lrm`, then `pve-ha-crm`, before cluster/API services. The LRM receives one graceful stop and up to 180 seconds; it is never signalled to force progress because that can leave its watchdog armed and fence the host. If it cannot stop, the restore aborts and cancels the queued stop. Do not kill it manually to bypass this safeguard.

The workflow unmounts `/etc/pve`, extracts the database, then restarts cluster/API services before later restore stages. An unmount failure is a warning, not proof that the database is safe. Individual `/etc/pve` applies are skipped in RECOVERY because the database owns those areas; confirmed SAFE paths are separate live write paths. Restart warnings must be investigated, and HA must not be brought back while pmxcfs is unavailable.

Normal extraction, fstab merge and SAFE operations can change the host before later staged apply. An incomplete sensitive stage is discarded and its consumers are skipped; this is not a whole-restore transaction. Network/firewall/HA/access-control live applies have their own 180-second COMMIT rollback decisions. The fstab countdown expires to No even when matching root/swap makes Yes the Enter-key default. Read the logs and establish the actual applied scope before retrying.

### Verify and handle failures

Use [Post-Recovery Verification](#post-recovery-verification) to check service health, mounted pmxcfs, actual membership and quorum, cluster communication, storage, UI/API access and workload definitions. Confirm backing mounts and VM disks before starting workloads. Mount guards can make offline storage read-only; a failed bind guard is warning-only, and a real mount only shadows an existing guard. Use **Recovery > Cleanup guards** after storage is correctly available and inspect any remaining/pending legacy flags.

Keep the printed logs and persistent `<BASE_DIR>/restore/TIMESTAMP/` safety artifacts until the result is verified. They do not undo every API operation. Never blindly extract a safety archive over a live database: follow [manual rollback prerequisites](RESTORE_GUIDE.md#manual-rollback-prerequisites), including isolation, graceful HA shutdown and correct `config.db` recovery permissions. For refused guards or incomplete recovery, use [Common Issues and Solutions](#common-issues-and-solutions), the applicable runbook's failure handling, and [Prepare a support request](TROUBLESHOOTING.md#prepare-a-support-request).

<!-- site-region: cluster-recovery:end -->

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

## Starting a restore

Run `proxsave` without arguments as root on an interactive console, then choose **Tools > Restore**. The prompts below are the text representation of the same restore choices shown as TUI selectors. Both interfaces require operator answers. If the dashboard cannot render or you need a different configuration file, consult [CLI reference](CLI_REFERENCE.md) for direct entry and terminal options.

## Understanding PVE Cluster Architecture

### Cluster Filesystem Components

```text
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
- Writes through mounted pmxcfs are persisted in config.db and replicated. ProxSave blocks blind archive extraction there and uses confirmed API/pmxcfs applies (SAFE) or database replacement with services stopped (RECOVERY). See [Cluster restore modes](#cluster-restore-modes-safe-vs-recovery).
- Repopulated from config.db when pmxcfs restarts (the RECOVERY path)

**Corosync**:
- Synchronizes config.db changes across nodes
- Manages quorum (who can make changes)
- Configuration in `/etc/pve/corosync.conf`

### Why /etc/pve needs special handling

A write through mounted pmxcfs updates persistent cluster configuration. A copy into
an underlying directory while pmxcfs is unmounted does not update that database.
Confusing these two states can overwrite live configuration or leave files hidden
under the mount. See the [pmxcfs documentation](https://pve.proxmox.com/pve-docs/pmxcfs-plain.html).

ProxSave exports `/etc/pve` entries for review. SAFE offers confirmed configuration
applies against the running cluster; RECOVERY replaces the archived database only
after the required service shutdown. Choose the mode according to the cluster state.

---

## Cluster restore modes: SAFE vs RECOVERY

This is the single most important decision in a cluster restore, and ProxSave forces you to make it explicitly.

### When the prompt appears

Whenever a PVE restore includes the `pve_cluster` category and the backup actually contains cluster data, ProxSave shows a second, mandatory prompt right after you pick the restore scope. FULL and STORAGE both include `pve_cluster`, so they always reach it; CUSTOM reaches it only if you select `PVE Cluster Configuration`. SYSTEM BASE does not include `pve_cluster`, so it never shows this prompt. In the CLI it reads:

```text
Cluster backup detected. Choose how to restore the cluster database:
  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).
  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.
  [0] Exit
Choice:
```

In the TUI it is a selector titled `Cluster restore mode` with `SAFE`, `RECOVERY`, and `Exit` items carrying the same text. `Exit` (or Esc) aborts the whole restore.

### SAFE (configuration apply)

SAFE does not replace config.db with the archived database, does not stop `pve-cluster`/`pvedaemon`/`pveproxy`/`pvestatd`, and does not unmount `/etc/pve`. It extracts the cluster files to an export directory and re-applies them to the RUNNING cluster through `pvesh`/`pveum`:

- storage definitions from storage.cfg (`pvesh create /storage`, falling back to `pvesh set /storage/<id>` for definitions that already exist);
- datacenter options (datacenter.cfg written into pmxcfs, which replicates cluster-wide; the API has no whole-file endpoint);
- resource pools (`pveum pool add/modify` for definitions, then membership, with an optional allow-move guard when a pool lists guests);
- PCI, USB, and directory resource mappings;
- VM and CT configs: before any guest mutation, SAFE loads the cluster-wide inventory with `pvesh get /cluster/resources --type vm --output-format=json`. An unavailable, malformed, incomplete, or ambiguous inventory fails the whole selected guest batch closed. A VMID owned by another node, or present with a different guest type, is reported and skipped; SAFE never moves it. Existing guests on the current node are updated with `pvesh set` under `/nodes/<node>/qemu|lxc/<vmid>/config` (minus create-only keys); a recognized schema refusal allows file fallback only after `status/current` explicitly reports `stopped`. Other API errors do not qualify. With a running or unverified guest after a schema refusal, ProxSave may retry the API with refused keys removed. A VMID absent cluster-wide is registered on the current node by writing the conf into pmxcfs (config only - disks are not part of a config restore).

Use SAFE for confirmed applies without replacing the whole database. These operations still change live state, sometimes cluster-wide. Guest confirmation covers the eligible batch from the selected source node, not individual guests. SAFE needs `pvesh` on PATH; without it, it logs a skip and applies nothing.

If the backup's VM/CT configs are stored under a node name that does not match the current host (a hostname change), SAFE handles it for you: it warns, and either auto-selects the single exported node or asks which exported node to import the guest configs from. The chosen configs are applied to the current node. You do not need to copy `/etc/pve/nodes/...` by hand.

Selecting an exported node controls only which backed-up files are considered. It does not override live cluster ownership: if one of those VMIDs already belongs to another node, SAFE skips it rather than applying or moving it to the current node.

This applies only when the guest configs are actually in the export, which means the restore included `pve_config_export`. FULL includes it; CUSTOM includes it if you select it; STORAGE and SYSTEM BASE strip export-only categories, so in those modes SAFE applies no guest config and the node-name handling never runs.

### RECOVERY (the destructive, offline choice)

RECOVERY restores the entire cluster database by overwriting `/var/lib/pve-cluster/`. To do that safely it:

1. probes the quorum with `pvecm status` right after you pick RECOVERY, when the node has a `corosync.conf`, and refuses a quorate cluster with more than 1 node online (see below);
2. stops `pve-ha-lrm` and `pve-ha-crm`, then `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd`, in that order. Every service except `pve-ha-lrm` escalates to SIGKILL if it will not stop. `pve-ha-lrm` is never signalled: it gets one `systemctl stop --no-block` and up to 180 seconds to go inactive. If it is still active after that, ProxSave runs `systemctl start pve-ha-lrm`, which cancels the queued stop and leaves the LRM running, and the restore stops;
3. unmounts `/etc/pve` (a failure here is a warning, not fatal);
4. extracts `./var/lib/pve-cluster/` (config.db) directly to disk while pmxcfs is down;
5. restarts `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd`, then `pve-ha-crm` and `pve-ha-lrm`, right after that extraction and before the later steps (network apply, boot rebuild). If the restore fails before that point, they are restarted when the run ends. A unit that fails to start does not stop the others, except that `pve-ha-crm` and `pve-ha-lrm` are not started while `pve-cluster` is down (the LRM would arm the watchdog without pmxcfs). There is one attempt per restore, of three tries per unit; the restore goes on, and its closing advice names the units left down.

The HA services are stopped first because a running `pve-ha-lrm` keeps the node's watchdog open: with pmxcfs down for 60 seconds the watchdog expires and the node is hard-reset (fenced) in the middle of the restore. Stopped, the LRM freezes its HA resources and closes the watchdog cleanly, and the CRM releases its lock so the master moves to another node. After the restart the LRM resumes the resources it froze.

The LRM stop can be slow: it waits for the CRM master to acknowledge the freeze, and when the old master is a node that went down, its master lock only times out about 120 seconds after it died. On an isolated node whose old master was powered off, the stop took 93 seconds. A SIGKILL before the LRM closes its watchdog would leave the watchdog armed and fence the node, which is why `pve-ha-lrm` is only ever asked to stop and given 180 seconds.

The quorum probe exists because on a member of a quorate cluster the restored config.db does not survive: when `pve-cluster` starts again, pmxcfs syncs from the cluster leader and the leader's copy replaces it. What the probe does:

| `pvecm status` reports | Result |
|---|---|
| Quorate, more than 1 node online | The restore stops before anything is stopped or written: `Cluster RECOVERY refused - quorate cluster, N nodes online: its copy would replace the restored config.db` |
| Quorate, 1 node online | Proceeds |
| Not quorate | Proceeds |
| Cannot be read (pvecm is not installed, fails, times out, or prints no `Quorate:` line), or quorate with a `Nodes:` count that is not a number, and `systemctl is-active corosync` says `inactive` or `failed` | Proceeds, with the warning `Cluster RECOVERY - quorum unknown (<reason>), corosync inactive, proceeding` (or `failed`) |
| Same, with corosync in any other state (`active`, `activating`, ...) or a state that cannot be read | The restore stops before anything is stopped or written: `Cluster RECOVERY refused - quorum unknown (<reason>), corosync <state>: in a quorate cluster, its copy would replace the restored config.db` |

A node without `corosync.conf` (`/etc/pve/corosync.conf` or `/etc/corosync/corosync.conf`) is standalone: it proceeds with no probe and no message. pvecm also fails when pmxcfs is down (no `/etc/pve/corosync.conf`) while corosync is up and quorate with its peers; `pve-cluster` would then start again and sync from the leader. So a quorum that cannot be read lets the restore proceed only when systemctl shows corosync stopped, which is also the state after `systemctl stop corosync`.

While pmxcfs is down, ProxSave does not write individual `/etc/pve` files. The config areas that live under `/etc/pve` (storage, jobs, firewall, HA, SDN, access control, notifications) each skip their own apply step during a cluster RECOVERY, because config.db now owns them; a shadow-guard strips any `/etc/pve` path from the direct-extraction set as a backstop. Everything under `/etc/pve` comes back from the restored config.db once `/etc/pve` is remounted.

Use RECOVERY only on an OFFLINE or ISOLATED node. Restoring config.db on a node that is still talking to other cluster members can corrupt the cluster. ProxSave logs a warning when you pick it: `Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated`.

### Which one an earlier version of this guide described

Older versions of this guide showed the "stopping PVE services / unmounting /etc/pve / restart" sequence as the automatic result of choosing "STORAGE only". That sequence is RECOVERY only. Choosing STORAGE (or FULL) just brings you to the SAFE/RECOVERY prompt; SAFE does none of it.

### Offline storage: mount guards

If a datastore or storage mountpoint is offline during a restore (its device is not mounted, so the path resolves to the root filesystem), ProxSave bind-mounts a read-only guard over it from `<BASE_DIR>/guards` (`/opt/proxsave/guards` by default), so the restore cannot write onto the root disk and be shadowed later when the real storage mounts. The guard is a runtime bind mount: it is shadowed when the real storage mounts on top, and it is gone after a reboot. Current versions no longer set a persistent `chattr +i` flag. To clear leftover guards once storage is back online:

Choose **Recovery > Cleanup guards** in the dashboard. See [CLI reference](CLI_REFERENCE.md) for direct entry.

In the dashboard the same operation is `Recovery > Cleanup guards`: a read-only check
first (green when there is nothing to clean, yellow with a count when guards are
present), then `Apply` for the real removal.

`--cleanup-guards` also clears any legacy `chattr +i` immutable flags left by older versions.

### Network changes are applied with an auto-rollback

If your restore scope includes the network category (FULL, SYSTEM BASE, or a CUSTOM selection, not STORAGE), ProxSave remaps interface names by hardware identity and applies the config with an armed auto-rollback: it arms a 180-second timer, reloads networking, and reverts automatically unless you confirm with `COMMIT` in time. Run that step from the local console or IPMI, not over SSH, since it can change the active IP. The firewall, HA, and access-control applies use the same armed 180-second rollback.

### Incomplete stages and apply warnings

Sensitive configuration is applied only from a complete stage. If stage extraction fails, ProxSave discards it and skips staged consumers, including network installation, firewall, HA and notification repair. Normal file extraction, fstab changes and SAFE operations may already have changed the host. This guard does not make the whole restore transactional. Read the session log and verify each applied category before retrying or rolling back.

### The safety backup

Before overwriting anything, ProxSave writes a safety backup of the current configuration to `<BASE_DIR>/restore/<YYYYMMDD_HHMMSS>/restore_backup_<YYYYMMDD_HHMMSS>.tar.gz` (`/opt/proxsave/restore/...` by default) and keeps it, outside `/tmp` so that it survives the reboot the restore recommends. At the end it prints where it is:

```text
Safety backup preserved at: /opt/proxsave/restore/20251120_143052/restore_backup_20251120_143118.tar.gz
Safety backup - kept until removed, ProxSave never deletes it
```

If any staged step fails, the run ends with `Restore completed with warnings.` rather than aborting, and the safety backup preserves selected pre-restore files. It is not a complete reversal of API operations. Follow [manual rollback prerequisites](RESTORE_GUIDE.md#manual-rollback-prerequisites) before using it; never extract it blindly over a live cluster database.

---

## Recovery Scenarios

### Decision Tree

```text
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

### Complexity: Low

### Prerequisites

- Backup available with `pve_cluster` category
- Root access to node
- Network connectivity working
- Hostname matches backup (or willing to change it)

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

Open the dashboard and choose `Restore`:

Choose **Tools > Restore** in the dashboard. See [CLI reference](CLI_REFERENCE.md) for direct entry.

For direct entry when the dashboard cannot render, see [CLI reference](CLI_REFERENCE.md).

#### Step 3: Interactive Selection

The prompts below are the text rendering (`--cli`, or any non-interactive run). The TUI
asks exactly the same questions as selectors, in the same order.

```text
Select backup source:
  [1] Primary backup path
Select: 1

- backups:
  [1] backup-pve01-20251120-143052.bundle.tar
Select: 1

Backup is encrypted with AGE.
Enter AGE passphrase: ********

Select restore mode:
  [1] FULL restore - Restore everything from backup
  [2] STORAGE only - PVE cluster + storage + jobs + mounts
  [3] SYSTEM BASE only - Network + SSL + SSH + services + filesystem
  [4] CUSTOM selection - Choose specific categories
  [0] Cancel
Select: 2 (STORAGE only)
```

Because the backup contains cluster data, ProxSave now asks how to restore the cluster database (see [Cluster restore modes](#cluster-restore-modes-safe-vs-recovery)):

```text
Cluster backup detected. Choose how to restore the cluster database:
  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).
  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.
  [0] Exit
Choice: 2 (RECOVERY)
```

For a standalone node you are rebuilding from backup, RECOVERY is the right choice: the node is offline and you want its exact database back. On a node that is already up and running, choose SAFE instead. The STORAGE-mode restore plan on a PVE host is:

```text
RESTORE PLAN:
  - PVE Cluster Configuration
  - PVE Storage Configuration
  - PVE Backup Jobs
  - ZFS Configuration
  - Filesystem Configuration
  - Storage Stack (Mounts/Targets)

Type 'RESTORE' to proceed or 'cancel' to abort: RESTORE
```

#### Step 4: Automated Process (RECOVERY)

Because you chose RECOVERY, ProxSave restores config.db with the cluster services stopped. The real log is terse; the sequence is:

```text
Selected RECOVERY cluster restore: full cluster database will be restored; ensure other nodes are isolated

Creating Safety backup of current configuration...
Safety backup location: /opt/proxsave/restore/20251120_143052/restore_backup_20251120_143118.tar.gz

Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve

... extraction of the selected categories ...

Restore completed successfully.
Safety backup preserved at: /opt/proxsave/restore/20251120_143052/restore_backup_20251120_143118.tar.gz
Safety backup - kept until removed, ProxSave never deletes it
```

ProxSave stops `pve-ha-lrm`, `pve-ha-crm`, `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd`, unmounts `/etc/pve`, extracts `/var/lib/pve-cluster/` (config.db), then restarts `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd`, `pve-ha-crm`, `pve-ha-lrm` before the remaining steps of the restore. It does not print a per-service checkmark line for each one. No `/etc/pve` files are written directly: config.db owns them, so `/etc/pve` is repopulated from the restored database a moment after pmxcfs remounts, not by the file-extraction phase.

Had you chosen SAFE, this step would instead apply what the selected categories actually exported, through `pvesh` on the running cluster, without stopping PVE services or replacing config.db. Note what STORAGE mode does not carry: the VM/CT configs live in `pve_config_export`, which is export-only and is stripped from STORAGE, so none are applied. `storage.cfg` and `datacenter.cfg` belong to `storage_pve` and are still applied through `pvesh`, as are pools and resource mappings. Use FULL, or CUSTOM including `pve_config_export`, when you want the guest configs applied.

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
# A standalone installation may have no corosync.conf and no cluster status.
# For an actual one-member cluster, check its configured votes and quorum.

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

# 3. Remove this restore's safety backup (after thorough verification). TIMESTAMP is
#    its directory, from its "Safety backup preserved at:" line; the logs and the
#    safety backups of earlier restores stay.
rm /opt/proxsave/restore/TIMESTAMP/restore_backup_*.tar.gz

# 4. Update backups schedule if needed
cat /etc/pve/vzdump.cron
```

### Success Criteria

- All services running
- `/etc/pve` mounted and populated
- `pvecm status` shows healthy single-node cluster
- Storage accessible
- Web interface working
- VM/CT configs visible

---

## Scenario 2: Complete Cluster Rebuild

### Situation

- All nodes in cluster have failed
- Need to rebuild entire cluster from backup
- Want to restore cluster configuration

### Complexity: High

### Prerequisites

- Complete backup of one cluster node
- Fresh PVE installed on all nodes (or clean nodes)
- Nodes have correct hostnames
- Network connectivity between nodes
- Time synchronization working (NTP)

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
#    Run bare proxsave and choose Tools > Restore in the dashboard.
#    Direct entry options are in CLI_REFERENCE.md.
# Dashboard action: Tools > Restore
# Select: [2] STORAGE only
# At the cluster prompt choose RECOVERY: the primary is offline, so this
# restores the full config.db. SAFE would apply configs via the API without
# writing config.db, so the cluster/corosync state would not come back.

# 3. Verify primary node working
pvecm status
# Restored membership can still list the old peers; quorum is not automatic.

# 4. Check corosync configuration
cat /etc/pve/corosync.conf
# Note: May reference old dead nodes
```

#### Phase 2: Clean Corosync Configuration

A database from a multi-node cluster retains its membership. Recovering it on one
host does not establish quorum or turn it into a standalone installation.

Keep all former members powered off or isolated from both cluster communication and
shared workloads. Inspect `pvecm status`, the restored `corosync.conf`, node IDs and
expected votes. If membership changes or temporary quorum recovery are necessary,
follow the [official Proxmox node-removal and quorum procedure](https://pve.proxmox.com/pve-docs/pvecm-plain.html)
for that version and state. Reducing expected votes is not a general fix for a
network partition and must not allow competing members to write independently.

Proceed only after one authoritative cluster state is established and verified.
Do not delete the mounted `/etc/pve` tree to reset a node.

#### Phase 3: Add Secondary Nodes

**On SECONDARY NODES** (pve02, pve03, etc.):

```bash
# 1. Ensure fresh PVE installation
# - No existing cluster membership
# - No existing guests; preserve the fresh installation's own pmxcfs files

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

- All nodes show in `pvecm nodes`
- Cluster is quorate
- Corosync communication working
- Storage accessible on all nodes
- `/etc/pve` syncing across nodes (test by editing file)
- Web interface accessible on all nodes
- VMs/CTs can be migrated between nodes

---

## Scenario 3: Multi-Node Cluster with Failed Master

### Situation

- Multi-node cluster running
- One node (with backup) failed completely
- Other nodes still operational
- Need to restore failed node

### Complexity: Medium

### Prerequisites

- Other cluster nodes still running
- Cluster has quorum without failed node
- Backup of failed node available
- Fresh PVE installed on replacement hardware

### Procedure

#### Step 1: Remove Dead Node from Cluster

**On a surviving, quorate node**, after the failed node is powered off and prevented from rejoining:

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

# 4. Verify the surviving cluster still has its intended quorum
pvecm status
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

Run `proxsave` without arguments and choose **Tools > Restore**, then choose **Custom**. Leave **PVE Cluster Configuration** (`pve_cluster`) unselected. Consider only the node-local categories required for this replacement, such as network, SSL, SSH and system services, and inspect every selected path. Review and apply network changes from a local console or IPMI; they can replace the active address and disconnect SSH.

**Important**: Never run a RECOVERY cluster restore on a node that is still in the cluster; it overwrites config.db and can corrupt the cluster. On a member of a quorate cluster with other nodes online, ProxSave refuses RECOVERY (`Cluster RECOVERY refused - quorate cluster, N nodes online: ...`). The conservative choice is to leave `PVE Cluster Configuration` unselected here. If you do select it, ProxSave prompts SAFE vs RECOVERY, and on a live member SAFE is the applicable mode (its confirmed API applies change live configuration without replacing config.db).

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

Keep the replacement's chosen hostname after it has joined. Renaming a joined node
is not a `delnode`/rename/rejoin shortcut. Choose its final name before installation
and joining. If replacement under another name is required later, move or back up
its workloads, power it off, and remove it from a surviving cluster member using
the [official procedure](https://pve.proxmox.com/pve-docs/pvecm-plain.html).

### Success Criteria

- Replacement node shows in `pvecm nodes`
- Cluster is quorate
- Storage accessible on replacement node
- Can migrate VMs/CTs to/from replacement node
- No errors in `journalctl -u corosync`

---

## Scenario 4: Migration to New Hardware

### Situation

- Migrating existing node to new hardware
- Want to preserve cluster configuration
- Clean installation on new hardware

### Complexity: Medium

### Prerequisites

- Backup from old hardware
- New hardware with PVE installed
- Network connectivity configured
- Old hardware shut down (to avoid conflicts)

### Procedure

#### Step 1: Preparation

**On OLD HARDWARE** (before shutdown):

```bash
# 1. Create fresh backup
#    Dashboard equivalent: run bare `proxsave` and pick Backup.
# Dashboard action: Backup

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

Choose the recovery route first. If a multi-node cluster survives, use
[Scenario 3](#scenario-3-multi-node-cluster-with-failed-master): join a clean
replacement and restore node-local settings. Shutting down the old hardware alone
does not isolate the replacement from the other live members. The RECOVERY example
below applies only to a standalone recovery or an explicitly isolated full-cluster
recovery; do not use it before joining a surviving cluster.

```bash
# 1. Copy proxsave tool to new hardware
# scp -r /opt/proxsave root@new-hardware:/opt/

# 2. Place backup in expected location
mkdir -p /opt/proxsave/backup
mv /root/*.bundle.tar /opt/proxsave/backup/

# 3. Run bare proxsave and choose Tools > Restore in the dashboard.
#    Direct entry options are in CLI_REFERENCE.md.
# Dashboard action: Tools > Restore

# Select: [2] STORAGE only (or FULL)
# Choose RECOVERY only for the isolated recovery route described above.
# Do not restore an old cluster database before joining a surviving cluster.
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
# A standalone host need not have Corosync cluster status.
# A restored cluster member retains the backed-up membership; verify quorum separately.

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

A node with a restored cluster database or guest definitions is not a clean joining
node. Joining replaces `/etc/pve` with the surviving cluster's configuration, and
Proxmox requires no existing guests on the joining host.

If the cluster survives, follow Scenario 3 from a fresh installation. If you are
rebuilding the entire cluster, follow Scenario 2 and join fresh secondary nodes only
after the authoritative recovered node is ready. Preserve any recovered guest data
before changing routes; do not erase local configuration as a shortcut.

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

- Services running on new hardware
- Cluster configuration restored
- Storage accessible (with path adjustments)
- Network connectivity working
- Web interface accessible
- Can create/manage VMs/CTs

---

## Scenario 5: Hostname Changed

### Situation

- Need to restore backup to system with different hostname
- Backup from `pve01`, restoring to `pve02`
- May cause cluster conflicts

### Complexity: Medium

### Option A: Change Hostname to Match Backup

**Recommended if**: You are preparing a fresh, isolated replacement before it joins any cluster. Set its hostname and `/etc/hosts` before whole-database recovery; do not rename a live cluster member this way.

```bash
# 1. Change hostname before restore
hostnamectl set-hostname <backup-hostname>

# 2. Update /etc/hosts
vi /etc/hosts
# Update hostname references

# 3. Reboot
reboot

# 4. Run restore normally (dashboard: bare `proxsave` -> Restore)
# Dashboard action: Tools > Restore
```

### Option B: Update Configuration After Restore

Use the new name from installation onward. Prefer SAFE with FULL, or CUSTOM including
`pve_config_export`, to review the exported node and apply eligible guest settings
without importing the old cluster membership.

#### Step 1: Restore with Mismatched Hostname

Open **Tools > Restore** in the dashboard, select the configuration scope and choose SAFE when the
cluster payload is included. The workflow can ask which exported node to use when
the current hostname differs. STORAGE alone excludes the export-only guest payload.

#### Step 2: Fix Corosync Configuration

Do not turn a database restore into a rename by editing a node's name in place.
For a surviving cluster, join a clean replacement with its final name and use the
cluster's membership. For isolated recovery, review the version-specific Proxmox
membership procedure before bringing any old peers back.

#### Step 3: Update Node Directory

Inspect `/etc/pve/nodes/` and the cluster-wide guest inventory. SAFE skips VMIDs owned
by another node; it does not move them. Do not recursively copy an old node's entire
directory to the new name. Use the Proxmox guest migration/recovery procedure for
existing VMIDs, and review any manual single-guest change with its disks and ownership.

#### Step 4: Update Certificates

Verify `/etc/hostname`, `/etc/hosts`, DNS and the intended management address. Apply
the appropriate Proxmox certificate procedure after the final node identity is
established. WebAuthn users may need re-enrollment if the UI origin changes.

#### Step 5: Remove Old Node References

Remove an obsolete node only from a surviving cluster member after its workloads
are accounted for and the old machine cannot reconnect. Follow the
[official cluster procedure](https://pve.proxmox.com/pve-docs/pvecm-plain.html).

### Success Criteria

- The running hostname and management certificates match the intended identity
- Guest VMIDs have the intended single owner and their storage is available
- Cluster members agree on membership and quorum; a standalone host has no unintended cluster membership
- The web interface works and restore logs contain no unexplained skipped applies

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

# Inspect configured storage definitions and their paths
pvesh get /storage --output-format json
# For an actual volume ID, pvesm path resolves its filesystem path:
# pvesm path 'local:iso/example.iso'

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
systemctl is-active pve-cluster >/dev/null 2>&1 && echo "  Yes pve-cluster running" || echo "  No pve-cluster FAILED"
systemctl is-active pvedaemon >/dev/null 2>&1 && echo "  Yes pvedaemon running" || echo "  No pvedaemon FAILED"
systemctl is-active pveproxy >/dev/null 2>&1 && echo "  Yes pveproxy running" || echo "  No pveproxy FAILED"
echo

# 2. Cluster Status
echo "2. Checking cluster..."
if pvecm status | grep -q "Quorate.*Yes"; then
    echo "  Yes Cluster is quorate"
else
    echo "  No Cluster NOT quorate"
fi
echo

# 3. Filesystem
echo "3. Checking /etc/pve..."
if mount | grep -q "/etc/pve type fuse.pmxcfs"; then
    echo "  Yes /etc/pve mounted"
else
    echo "  No /etc/pve NOT mounted"
fi
echo

# 4. Storage
echo "4. Checking storage..."
if pvesm status >/dev/null 2>&1; then
    echo "  Yes Storage accessible"
    INACTIVE=$(pvesm status | grep -c "inactive" || true)
    if [ "$INACTIVE" -gt 0 ]; then
        echo "  Warning: $INACTIVE storage(s) inactive"
    fi
else
    echo "  No Storage check FAILED"
fi
echo

# 5. API
echo "5. Checking API..."
if pvesh get /version >/dev/null 2>&1; then
    echo "  Yes API responding"
else
    echo "  No API NOT responding"
fi
echo

# 6. Summary
echo "=== Summary ==="
FAILED=$(systemctl --failed --no-legend | wc -l)
if [ "$FAILED" -eq 0 ]; then
    echo "  Yes No failed services"
else
    echo "  No $FAILED failed service(s)"
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

Use the [manual rollback prerequisites](RESTORE_GUIDE.md#manual-rollback-prerequisites)
for the exact restore session. Do not extract a safety archive while pmxcfs is
running, and account for HA before stopping the cluster filesystem.

**2. /etc/pve still mounted**:

Inspect `findmnt /etc/pve` and the service log. Confirm the HA-aware shutdown before
unmounting; do not force-unmount a live cluster as a generic repair.

**3. Permissions wrong**:

ProxSave preserves backed-up ownership and modes. For a manual database recovery,
follow the [official pmxcfs procedure](https://pve.proxmox.com/pve-docs/pmxcfs-plain.html),
which requires `config.db` mode `0600`. Check that specific file and its expected
owner; do not recursively assign `/var/lib/pve-cluster` to `root:www-data`.

---

### Issue: Lost Quorum

**Symptoms**:
```bash
pvecm status
# Output: "not quorate"
```

Check the intended membership, online members, Corosync links and qdevice state
before changing votes. A standalone host and a one-member Corosync cluster are
different configurations. Loss of quorum during a partition protects shared state.

Do not set expected votes to the number of nodes you can currently see. Temporary
quorum overrides belong only to a documented recovery with the other writers
excluded. Follow the [official Proxmox quorum procedure](https://pve.proxmox.com/pve-docs/pvecm-plain.html)
and verify consistent membership before reconnecting recovered nodes.

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

**2. Inspect service logs**:
```bash
journalctl -u corosync -u pve-cluster --since "30 minutes ago"
```

A restart is not a substitute for resolving quorum or connectivity. If stopping
pmxcfs is necessary, follow the HA-aware shutdown prerequisites first.

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

**4. Do not force synchronization by deleting a lock**:

Check the service and quorum errors first. Removing `.pmxcfs.lockfile` does not
establish which cluster state is authoritative. Use the supported Proxmox recovery
procedure for the diagnosed failure.

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

#### Option 1: Review the Safety Backup

Start with the exact archive and logs from the failed restore. Follow the
[manual rollback prerequisites](RESTORE_GUIDE.md#manual-rollback-prerequisites),
including graceful HA shutdown before taking pmxcfs down. The safety archive is a
file-level recovery aid, not a complete undo of cluster-wide API operations.

#### Option 2: Recover config.db from a Verified Backup

Prefer ProxSave's RECOVERY workflow on an isolated replacement. If a manual database
recovery is necessary, verify and decrypt the complete backup first, unwrap its
bundle, and select the actual inner archive by name. Do this in a private working
directory before touching the live database. Use the
[official pmxcfs recovery procedure](https://pve.proxmox.com/pve-docs/pmxcfs-plain.html)
with the correct hostname, stopped services, unmounted `/etc/pve` and database mode
`0600`. Do not bypass a failed HA shutdown with `killall` or a forced unmount.

#### Option 3: Reinstall and Apply Selected Configuration

When the installation cannot be repaired, preserve guest data and the available
configuration evidence, then install Proxmox afresh. Select the surviving-cluster
or full-rebuild scenario before importing configuration. Purging the Proxmox
packages on a broken live node is not a documented substitute for that process.

---

### Emergency: Split-Brain Scenario

**Situation**: Multi-node cluster with conflicting state

**Symptoms**:
- Different nodes show different quorum status
- Some nodes can't see others
- Corosync shows duplicate node IDs

**Resolution**:

1. Use console access to establish the workload and storage state of every partition.
   Prevent competing nodes from running or writing the same guests.
2. Keep conflicting members isolated and preserve their configuration evidence.
   Choose an authoritative state based on the recovery plan, not merely the first
   node that starts.
3. Follow the [official Proxmox cluster recovery procedure](https://pve.proxmox.com/pve-docs/pvecm-plain.html)
   for membership and quorum. Stopping pmxcfs also requires the HA precautions in
   [manual rollback prerequisites](RESTORE_GUIDE.md#manual-rollback-prerequisites).
4. Recover the authoritative node, then reintroduce clean replacement nodes using
   the prerequisites in Scenario 2. Never delete a mounted `/etc/pve` tree to force
   a join, and do not lower quorum independently on competing partitions.
5. Verify agreed membership, guest ownership, storage and HA before restoring service.

---

## Appendix

### Quick Reference Commands

```bash
# Cluster Status
pvecm status              # Overall cluster status
pvecm nodes               # List cluster nodes
# Quorum overrides require the reviewed recovery procedure above

# Corosync
corosync-cfgtool -s       # Corosync status
corosync-quorumtool -s    # Quorum status
corosync-quorumtool -l    # Quorum list

# Services
systemctl status pve-cluster
journalctl -u pve-cluster --since "1 hour ago"

# Filesystem
mount | grep pve          # Check /etc/pve mount
ls -la /etc/pve/          # List cluster configs
findmnt /etc/pve          # Inspect the mount without changing it

# Storage
pvesm status              # Storage status
pvesm scan zfs            # Scan ZFS pools
pvesm scan nfs nfs.example.com  # Replace with your NFS server

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
- Always test recovery procedures on non-production systems first
- Keep multiple backup copies in different locations
- Document your specific cluster setup and customizations
- Verify backups regularly (don't wait for disaster)
- Practice recovery scenarios at least annually
- Keep this guide accessible (print or offline copy)

**When in Doubt**:
1. Stop and document current state
2. Create additional backups
3. Test procedure on non-production system
4. Ask for help on Proxmox forums
5. Better to be slow and safe than fast and corrupted.
