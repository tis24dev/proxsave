# Proxsave - Restore Guide

Complete guide for restoring Proxmox VE, Proxmox Backup Server, and dual-role
PVE+PBS backups using the interactive restore workflow.

Restore is a **dashboard** action: run `proxsave` with no arguments on a terminal and
pick **Tools > Restore**. Direct restore entry and terminal options for rescue shells and headless hosts are documented in [CLI reference](CLI_REFERENCE.md).

## Table of Contents

- [Choose restore modes and categories](#choose-restore-modes-and-categories)
- [Restore a host](#restore-a-host)
- [Restore IOMMU, VFIO and passthrough settings](#restore-iommu-vfio-and-passthrough-settings)
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

### Normal path: the dashboard

Run `proxsave` as root with **no arguments** on a terminal and choose **Restore** in the
`Tools` group:

```text
─── Tools ───
  Restore           restore a backup onto this system
  Decrypt           convert an encrypted backup into a plaintext bundle
```

The dashboard is a launcher only: the `Restore` row sets exactly what `--restore` sets
and then runs the identical workflow, so everything in this guide applies to both. The
full menu is documented in [DASHBOARD.md](DASHBOARD.md).

The dashboard opens only when the invocation is **completely bare** (any flag, even
`--config`, skips it) and stdin and stdout are both real terminals with `TERM` set and
not `dumb`. Direct entry and terminal requirements are in [CLI reference](CLI_REFERENCE.md#restore-from-backup).

### Emergency, headless and recovery path: `--restore`

If the dashboard cannot open, use the interactive restore entry and terminal options in [CLI reference](CLI_REFERENCE.md). A text prompt still needs operator answers; it is not unattended recovery.

### The steps, either way

1. Select backup source location
2. Choose specific backup from list
3. Enter the AGE key or passphrase in a single field (if encrypted)
4. Select restore mode
5. Review restore plan
6. Confirm twice: type RESTORE (or press the RESTORE button), then confirm the overwrite
7. Wait for completion
8. Verify services and cluster status

### Requirements

- **Root privileges**: Required for system path restoration (restoring to `/` as a
  non-root user is refused outright)
- **Sufficient disk space**: For decryption and safety backups
- **Service availability**: Target system services must be accessible
- **Network isolation**: For cluster restores, node should be isolated
- **A real terminal**: For the dashboard. Without one, use the [direct restore entry](CLI_REFERENCE.md#restore-from-backup), which still requires answers

---





<!-- site-region: restore-selection:start -->

## Choose restore modes and categories

Choose a restore scope before accepting any overwrite. ProxSave restores host configuration from a complete backup set or bundle. It does not restore VM/CT disks, PBS chunks, partitions or filesystem contents outside the backed-up paths, and it does not format disks or import ZFS pools. Recover workload data through its own backup system.

### Prepare the selection

Run `proxsave` without arguments as root on an interactive console and choose **Tools > Restore**. Select the source location, choose the complete backup and supply its AGE key or passphrase if encrypted. Keep the archive and required sidecars together; if a cloud scan is empty, check the selected directory because scanning is non-recursive. ProxSave verifies the stored archive before decryption and analyzes which categories actually contain files. A missing key cannot be replaced by generating a new one.

Restore compares the backup's roles with the destination: PVE, PBS, dual or unknown. Matching roles permit their categories. A partial match filters live restores to shared roles; incompatible product configuration is exported for review. An unknown role produces a warning, and a disjoint known role needs explicit override. Review that warning instead of assuming an override makes the configuration compatible.

### Choose a mode

| Mode | Selection and limits |
| --- | --- |
| Full | All available compatible categories, including export-only material. Sensitive categories still use staged apply; Full does not write every archived file directly to the host. |
| Storage | The available role-specific storage, jobs and common mount/storage categories. On PVE it can include `pve_cluster`; on PBS it includes datastore and maintenance configuration. Export-only categories are excluded. |
| System base | Network, SSL, SSH, services and filesystem configuration. SSH covers `/root/.ssh/` and `/etc/ssh/`; `/home/` keys require `user_data`. This mode excludes `boot`, `pve_cluster` and export-only categories. |
| Custom | The available categories you explicitly select, including export-only categories when required. Use this for a limited node-local restore or a selected sensitive apply. |

Preset membership, exact category paths and role-specific differences are in [Restore Modes](#restore-modes) and the [Category System reference](#category-system). A category absent from the archive cannot be selected.

### Understand where changes go

Normal categories write their selected system paths. Export-only categories write a separate timestamped export directory for inspection. Staged categories first extract sensitive configuration into a private stage, then their own apply steps validate and install it through the relevant API or file workflow. Individual file replacement does not make the whole restore a transaction.

The category reference covers all 33 categories. For selection, these are the important boundaries:

| Area | Categories and decision |
| --- | --- |
| PVE database and guest exports | `pve_cluster` controls database handling; `pve_config_export` supplies exported `/etc/pve` configuration, including guest definitions when backed up. Full includes the export; Storage and System base do not. |
| PVE live configuration | `storage_pve`, `pve_jobs`, `pve_notifications`, `pve_access_control`, `pve_firewall`, `pve_ha`, `pve_sdn` have category-specific apply paths. `corosync` and `ceph` require a reviewed cluster/storage scenario. |
| PBS exports and integrations | `pbs_config` is export-only. `pbs_host`, `datastore_pbs`, `maintenance_pbs`, `pbs_jobs`, `pbs_remotes`, `pbs_notifications`, `pbs_access_control`, `pbs_tape` cover distinct host, datastore, job, credential and tape settings. Datastore definitions do not recover chunks. |
| Host identity and access | `network`, `ssl`, `ssh`, `accounts`, `user_data` can alter access or identity. Account databases merge while retaining current root/system accounts; validated `/etc/sudoers` is replaced, and `/etc/sudoers.d` is excluded. Access-control categories perform their own 1:1 apply and root@pam safeguard. |
| Mounts and drivers | `filesystem`, `storage_stack`, `zfs`, `services`, `boot` require destination disk, mount and hardware review. Boot parameters merge into the destination configuration; old boot files are exported. |
| Scripts and diagnostics | `scripts`, `crontabs`, `proxsave_info` cover custom tools, scheduled tasks and export-only diagnostics. Inspect any restored automation before enabling it. |

### Separate cluster mode from category mode

When selected PVE cluster data is present, choose SAFE or RECOVERY after the scope. SAFE avoids replacing the archived database but confirmed API and pmxcfs applies mutate live configuration, potentially cluster-wide. RECOVERY overwrites the cluster database and requires an offline or isolated target. It refuses a quorate cluster with more than one online node; unreadable quorum permits continuation only when corosync is confirmed inactive or failed. Use [Recover a Proxmox cluster](CLUSTER_RECOVERY.md#recover-a-proxmox-cluster) before making this decision.

### Review before accepting

Check the plan's selected categories, file paths, destination roles and warnings. For guest configuration through SAFE, confirm that the exported source node and eligible batch are the ones you intend. Cluster-wide inventory must be readable and unambiguous; guests owned elsewhere or with another type are skipped, never moved. A stopped-guest file fallback requires a recognized API schema refusal, not any API error.

Proceed to [Restore a host](#restore-a-host) only when those choices are correct. Both the RESTORE gate and overwrite confirmation are required. If the stage later fails, its consumers are skipped, but preceding normal extraction, fstab changes or SAFE operations may already have changed the host. Cancel before confirmation when the plan or compatibility warning is unclear. Direct entry and terminal options are in [CLI reference](CLI_REFERENCE.md#restore-from-backup).

<!-- site-region: restore-selection:end -->

<!-- site-region: restore-host:start -->

## Restore a host

This procedure restores selected Proxmox and operating-system configuration after a reinstall or configuration failure. It does not restore workload disks or PBS backup chunks. Use a compatible destination and a tested backup; reserve space for a decrypted working archive, export material and persistent safety artifacts.

### Prerequisites

- Keep root access through a local console or IPMI. Network, firewall, HA and access-control changes can disconnect SSH or alter running workloads.
- Install the appropriate PVE/PBS role and confirm the destination hostname, networking and storage plan. For cluster membership, hostname changes or database replacement, use the matching [cluster scenario](CLUSTER_RECOVERY.md#recover-a-proxmox-cluster) first. Never rename a live member as a recovery shortcut.
- Retrieve the complete archive or bundle and recover its AGE key or passphrase independently. Verify that the destination can read the backup source. Do not rely on restoring credentials that are needed to retrieve or decrypt that same backup.
- Identify which data disks, shares or ZFS pools are already mounted/imported and which are offline. ProxSave neither formats disks nor imports pools. Have a separate recovery plan for VM disks and PBS chunks.

### Run and review

1. Run `proxsave` without arguments as root on the console and choose **Tools > Restore**. For a console where the dashboard cannot open, follow [CLI restore entry](CLI_REFERENCE.md#restore-from-backup); text prompts still require answers.
2. Select the source and complete backup. For cloud storage, choose the directory containing the backup, because discovery is non-recursive. Supply the AGE key or passphrase if prompted. Stop on a failed integrity check or missing credential.
3. Choose the scope using [Choose restore modes and categories](#choose-restore-modes-and-categories). On a live replacement cluster member, use Custom and keep `pve_cluster` unselected; do not accept Full as a substitute for a deliberately limited plan.
4. If cluster payload is selected, decide SAFE or RECOVERY with the canonical cluster runbook. SAFE changes confirmed live configuration; RECOVERY manages database replacement only after its quorum and isolation guards. Do not proceed after a refused guard.
5. Review all categories, paths and warnings, then accept the RESTORE gate and separate overwrite confirmation. A successful safety backup is the preferred prerequisite for continuing. If creating it fails, cancel and correct space/permissions rather than assuming the restore can undo later changes.

### Follow the apply decisions

ProxSave extracts normal categories, exports protected categories and applies staged sensitive configuration. A failed or incomplete stage is discarded and its consumers are skipped, including later network installation, firewall, HA and notification repair. This does not undo changes from earlier normal extraction, filesystem merge or SAFE apply. Read the resulting per-category warnings before retrying.

The smart fstab merge proposes safe data/network mounts instead of copying the old file wholesale. Destination UUID/label references and available inventory influence device remapping. The prompt has a 90-second countdown that always answers No at expiry. A matching root/swap can make Yes the Enter-key default; it does not make the timeout accept the merge. Review each proposed mount. Details are in [Smart fstab Merge](#5-smart-etcfstab-merge-optional).

Network files may be installed even if the optional live reload is skipped. A confirmed live network apply arms a 180-second rollback and needs COMMIT before the timer expires. Firewall, HA and access-control applies have their own rollback gates. Keep console access, test connectivity and inspect what each gate actually controls; these timers do not reverse every earlier restore operation. See [Network Safe Apply](#4-network-safe-apply-optional).

During cluster RECOVERY, HA services stop before pmxcfs and PVE API services. The LRM receives a graceful stop and up to 180 seconds; it must never be killed to force progress because its watchdog can fence the host. PVE services restart after database extraction, before remaining restore steps. Verify quorum separately from service startup. PBS restart and staged configuration checks have their own warnings; successful extraction alone is not proof of a healthy service.

Mount-backed PVE storage and PBS datastores can receive temporary read-only bind guards when the backing filesystem is offline and the path resolves to `/`. A failed guard is warning-only and leaves that path unguarded. A real mount shadows a guard; it does not remove it. Bring the correct storage online and use **Recovery > Cleanup guards**, which checks first and offers Apply only when guards are found. Legacy immutable flags can remain pending while a real mount masks their directory. Do not create a datastore layout on the root disk merely to silence an unavailable-storage warning.

For boot or driver restoration, use [Restore IOMMU, VFIO and passthrough settings](#restore-iommu-vfio-and-passthrough-settings). The old host's boot files are never blindly copied over the destination boot configuration. Inspect boot rebuild warnings before rebooting.

### Verify before returning to service

Read the printed session and detailed restore logs, including extraction counts, stage failures and skipped applies. Retain `<BASE_DIR>/restore/TIMESTAMP/` safety/rollback artifacts, normally under `/opt/proxsave/restore/`, until recovery is verified. Temporary working stages are cleaned on normal exit; the persistent safety directory is retained.

Verify relevant PVE/PBS services, the mounted configuration, actual cluster membership/quorum, storage paths and the product UI/API. Check that each storage path resolves to its expected backing filesystem. Confirm SSH/network/firewall access and user permissions, then compare job schedules and notifications. Start guests only after their configuration, device assignments and disks are correct; restored definitions do not prove those resources exist.

If the result reports warnings, establish which operations succeeded before repeating anything. Use [diagnose backup and restore failures](TROUBLESHOOTING.md#diagnose-backup-and-restore-failures) for logs and triage, and the [cluster verification procedure](CLUSTER_RECOVERY.md#post-recovery-verification) when applicable. Exported guest handling and application methods remain in [VM/CT Configuration Restore](#vmct-configuration-restore).

### Roll back only the reviewed scope

The safety archive preserves selected pre-restore files, not a complete inverse of API changes. Never extract it blindly over a running cluster database. Follow [manual rollback prerequisites](#manual-rollback-prerequisites), including verified isolation, graceful HA shutdown, pmxcfs handling and file ownership/mode requirements. Network, firewall, HA and access control have separate rollback files and scope. If those prerequisites cannot be established, stop and prepare a reviewed [support request](TROUBLESHOOTING.md#prepare-a-support-request).

<!-- site-region: restore-host:end -->

## Overview

Restore is an **interactive, category-based restoration system** that allows selective
or full restoration of Proxmox configuration files from backup archives. It is reached
from the dashboard (**Tools > Restore**). For consoles where the dashboard cannot open, use the direct-entry instructions in [CLI reference](CLI_REFERENCE.md#restore-from-backup).

### How to Use the Restore Docs

The restore documentation is split on purpose:

- [RESTORE_GUIDE.md](RESTORE_GUIDE.md): operator workflow, modes, warnings, and practical examples
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md): implementation details, detection logic, and internal architecture
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md): visual companion for the main workflow and decision paths
- [DASHBOARD.md](DASHBOARD.md): the menu the restore workflow is normally started from

### Interactive UI: TUI by default, `--cli` for text prompts

Restore uses selectors, multi-select category lists and confirmation screens. Transcripts in the detailed examples show the equivalent text prompts. Both interfaces require operator answers. Direct entry and text-mode options are in [CLI reference](CLI_REFERENCE.md#restore-from-backup).

### Key Features

- **Category-based selection**: Granular control over what gets restored
- **4 restore modes**: Full, Storage, Base, or Custom selection
- **Safety backups**: Automatic backup before any changes
- **Encryption support**: AGE encryption with passphrase or key
- **Cluster-aware**: Special handling for PVE cluster database
- **Export-only protection**: Critical paths protected from direct writes
- **Comprehensive logging**: Detailed audit trail of all operations

### System Types and Compatibility

Restore decisions are now based on four host types:

- `pve`
- `pbs`
- `dual`
- `unknown`

Backups also persist explicit target roles. This means compatibility is no
longer a simple exact match:

- **Full compatibility**: current host and backup targets match exactly
- **Partial compatibility**: backup and host share at least one role
- **Incompatible**: backup and host share no role

Examples:

- `dual` backup on `dual` host: restore `PVE + PBS + Common`
- `dual` backup on `pve` host: restore `PVE + Common`
- `dual` backup on `pbs` host: restore `PBS + Common`
- `pve` backup on `dual` host: restore `PVE + Common`

When compatibility is partial, ProxSave automatically filters selectable
restore categories to the roles supported by the current host. On a host that
runs one role only, the categories of the other role are extracted to the
export directory instead of being written to the system, in every restore mode.

`unknown` hosts can still use export-oriented or common-only workflows, but
ProxSave warns because role-specific compatibility cannot be verified.

### What Gets Restored

- System configurations (network, SSH, SSL, services)
- Proxmox-specific configs (cluster, storage, datastores)
- Custom scripts and cron jobs
- ZFS configurations and pool cache
- Backup jobs and scheduled tasks
- Kernel parameters set on the backed-up host (IOMMU, VFIO, ...), merged into the restore host's boot configuration (`boot` category)

### What Does NOT Get Restored

- VM/CT disk images (use Proxmox native tools)
- Application data (databases and app state under `/var/lib/*`)
- System packages (use apt/dpkg)
- Direct tar extraction writes to the pmxcfs mount (`/etc/pve`) (ProxSave uses staged apply: API or controlled pmxcfs file apply)

---

## Category System

Restore operations are organized into categories that group related configuration
files. The code defines **33 categories** in total: **11 PVE**, **9 PBS**, and **13
Common**. A host only sees the categories relevant to it: a **PVE host sees 24** (11
PVE + 13 Common) and a **PBS host sees 22** (9 PBS + 13 Common); a `dual` host sees all
33.

### Category Handling Types

Each category is handled in one of three ways:

- **Normal**: extracted directly to `/` (system paths) after safety backup
- **Staged**: extracted to `/tmp/proxsave/restore-stage-*` and then applied in a controlled way (file copy/validation or API apply: `pvesh`/`pveum` on PVE, `proxmox-backup-manager` on PBS); when staged files are written to system paths, ProxSave uses **individual file replacement** (not a transaction across categories or API updates) and enforces the final permissions/ownership (including for any created parent directories; not left to `umask`)
- **Export-only**: extracted to an export directory for manual review (never written to system paths)

### PVE-Specific Categories (11 categories)

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `pve_config_export` | PVE Config Export | **Export-only** copy of /etc/pve (never written to system) | `./etc/pve/` |
| `pve_cluster` | PVE Cluster Configuration | Cluster configuration and database | `./var/lib/pve-cluster/` |
| `storage_pve` | PVE Storage Configuration | **Staged** storage definitions (applied via API) + VZDump config | `./etc/pve/storage.cfg`; `./etc/pve/datacenter.cfg`; `./etc/vzdump.conf` |
| `pve_jobs` | PVE Backup Jobs | **Staged** scheduled backup jobs (applied via API) | `./etc/pve/jobs.cfg`; `./etc/pve/vzdump.cron` |
| `pve_notifications` | PVE Notifications | **Staged** notification targets and matchers (applied via API) | `./etc/pve/notifications.cfg`; `./etc/pve/priv/notifications.cfg` |
| `pve_access_control` | PVE Access Control | **Staged** access control + secrets restored 1:1 via pmxcfs file apply (root@pam safety rail) | `./etc/pve/user.cfg`; `./etc/pve/domains.cfg` (when present); `./etc/pve/priv/shadow.cfg`; `./etc/pve/priv/token.cfg`; `./etc/pve/priv/tfa.cfg` |
| `pve_firewall` | PVE Firewall | **Staged** firewall rules and node host firewall (pmxcfs file apply + rollback timer) | `./etc/pve/firewall/`; `./etc/pve/nodes/*/host.fw` |
| `pve_ha` | PVE High Availability (HA) | **Staged** HA resources/groups/rules (pmxcfs file apply + rollback timer) | `./etc/pve/ha/resources.cfg`; `./etc/pve/ha/groups.cfg`; `./etc/pve/ha/rules.cfg` |
| `pve_sdn` | PVE SDN | **Staged** SDN definitions (pmxcfs file apply; definitions only) | `./etc/pve/sdn/`; `./etc/pve/sdn.cfg` |
| `corosync` | Corosync Configuration | Cluster communication settings | `./etc/corosync/` |
| `ceph` | Ceph Configuration | Ceph storage cluster config | `./etc/ceph/` |

### PBS-Specific Categories (9 categories)

**PBS staged apply behavior**: During restore on PBS, ProxSave prompts you to choose how to reconcile PBS objects:
- **Merge (existing PBS)**: intended for restoring onto an already operational PBS; applies supported PBS categories via `proxmox-backup-manager` without deleting existing objects that are not in the backup. This covers the API-applied categories only: see the paragraph below for what is written straight from the stage in both modes.
- **Clean 1:1 (fresh PBS install)**: intended for restoring onto a new, clean PBS; attempts to make supported PBS objects match the backup (may remove objects not in the backup).

API apply is automatic for supported PBS staged categories, and file-based fallback for those is offered only in **Clean 1:1** mode. A few PBS items are always written straight from the stage in both modes, because there is no stable API for them: the `acme/accounts/` directory (mirrored, so an account the backup does not carry is removed), `acme/plugins.cfg`, `metricserver.cfg`, `proxy.cfg`, and the whole `pbs_tape` category. `proxy.cfg` in particular carries the listen address and certificate settings, so Merge mode is not a guarantee that nothing outside the API touches `/etc/proxmox-backup`.

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `pbs_config` | PBS Config Export | **Export-only** copy of /etc/proxmox-backup (never written to system) | `./etc/proxmox-backup/` |
| `pbs_host` | PBS Host & Integrations | **Staged** node settings, ACME, proxy, metric servers and traffic control (API/file apply) | `./etc/proxmox-backup/node.cfg`; `./etc/proxmox-backup/proxy.cfg`; `./etc/proxmox-backup/acme/accounts/`; `./etc/proxmox-backup/acme/plugins.cfg`; `./etc/proxmox-backup/metricserver.cfg`; `./etc/proxmox-backup/traffic-control.cfg`; `./var/lib/proxsave-info/commands/pbs/node_config.json`; `./var/lib/proxsave-info/commands/pbs/acme_accounts.json`; `./var/lib/proxsave-info/commands/pbs/acme_plugins.json`; `./var/lib/proxsave-info/commands/pbs/acme_account_*_info.json`; `./var/lib/proxsave-info/commands/pbs/acme_plugin_*_config.json`; `./var/lib/proxsave-info/commands/pbs/traffic_control.json` |
| `datastore_pbs` | PBS Datastore Configuration | **Staged** datastore definitions (incl. S3 endpoints) (API/file apply) | `./etc/proxmox-backup/datastore.cfg`; `./etc/proxmox-backup/s3.cfg`; `./var/lib/proxsave-info/commands/pbs/datastore_list.json`; `./var/lib/proxsave-info/commands/pbs/datastore_*_status.json`; `./var/lib/proxsave-info/commands/pbs/s3_endpoints.json`; `./var/lib/proxsave-info/commands/pbs/s3_endpoint_*_buckets.json`; `./var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json`; Note: `PBS_DATASTORE_PATH` override scan roots are inventory context only and are not recreated as datastore definitions during restore. |
| `maintenance_pbs` | PBS Maintenance | Maintenance settings | `./etc/proxmox-backup/maintenance.cfg` |
| `pbs_jobs` | PBS Jobs | **Staged** sync/verify/prune jobs (API/file apply) | `./etc/proxmox-backup/sync.cfg`; `./etc/proxmox-backup/verification.cfg`; `./etc/proxmox-backup/prune.cfg`; `./var/lib/proxsave-info/commands/pbs/sync_jobs.json`; `./var/lib/proxsave-info/commands/pbs/verification_jobs.json`; `./var/lib/proxsave-info/commands/pbs/prune_jobs.json`; `./var/lib/proxsave-info/commands/pbs/gc_jobs.json` |
| `pbs_remotes` | PBS Remotes | **Staged** remotes for sync/verify (may include credentials) (API/file apply) | `./etc/proxmox-backup/remote.cfg`; `./var/lib/proxsave-info/commands/pbs/remote_list.json` |
| `pbs_notifications` | PBS Notifications | **Staged** notification targets and matchers (API/file apply) | `./etc/proxmox-backup/notifications.cfg`; `./etc/proxmox-backup/notifications-priv.cfg`; `./var/lib/proxsave-info/commands/pbs/notification_targets.json`; `./var/lib/proxsave-info/commands/pbs/notification_matchers.json`; `./var/lib/proxsave-info/commands/pbs/notification_endpoints_*.json`; Note: restore rebuilds targets and matchers from the two `.cfg` files only. The `notification_*.json` listings are captured as context and are never read back during restore. |
| `pbs_access_control` | PBS Access Control | **Staged** access control + secrets restored 1:1 (root@pam safety rail) | `./etc/proxmox-backup/user.cfg`; `./etc/proxmox-backup/domains.cfg`; `./etc/proxmox-backup/acl.cfg`; `./etc/proxmox-backup/token.cfg`; `./etc/proxmox-backup/shadow.json`; `./etc/proxmox-backup/token.shadow`; `./etc/proxmox-backup/tfa.json`; `./var/lib/proxsave-info/commands/pbs/user_list.json`; `./var/lib/proxsave-info/commands/pbs/realms_ldap.json`; `./var/lib/proxsave-info/commands/pbs/realms_ad.json`; `./var/lib/proxsave-info/commands/pbs/realms_openid.json`; `./var/lib/proxsave-info/commands/pbs/acl_list.json` |
| `pbs_tape` | PBS Tape Backup | **Staged** tape config, jobs and encryption keys | `./etc/proxmox-backup/tape.cfg`; `./etc/proxmox-backup/tape-job.cfg`; `./etc/proxmox-backup/media-pool.cfg`; `./etc/proxmox-backup/tape-encryption-keys.json`; `./var/lib/proxsave-info/commands/pbs/tape_drives.json`; `./var/lib/proxsave-info/commands/pbs/tape_changers.json`; `./var/lib/proxsave-info/commands/pbs/tape_pools.json` |

### Common Categories (13 categories)

| Category | Name | Description | Paths |
|----------|------|-------------|-------|
| `filesystem` | Filesystem Configuration | Mount points and filesystems (/etc/fstab) - WARNING: Critical for boot | `./etc/fstab` |
| `storage_stack` | Storage Stack (Mounts/Targets) | Storage stack configuration used by mounts (iSCSI/LVM/MDADM/multipath/autofs/crypttab) | `./etc/crypttab`; `./etc/iscsi/`; `./var/lib/iscsi/`; `./etc/multipath/`; `./etc/multipath.conf`; `./etc/mdadm/`; `./etc/lvm/backup/`; `./etc/lvm/archive/`; `./etc/autofs.conf`; `./etc/auto.master`; `./etc/auto.master.d/`; `./etc/auto.*`; `./etc/keys/`; `./etc/luks-keys/`; `./etc/cryptsetup-keys.d/` |
| `network` | Network Configuration | Network interfaces and routing | `./etc/network/`; `./etc/netplan/`; `./etc/systemd/network/`; `./etc/NetworkManager/system-connections/`; `./etc/hosts`; `./etc/hostname`; `./etc/resolv.conf`; `./etc/cloud/cloud.cfg.d/99-disable-network-config.cfg`; `./etc/dnsmasq.d/lxc-vmbr1.conf` |
| `ssl` | SSL Certificates | SSL/TLS certificates and keys | `./etc/ssl/`; `./etc/proxmox-backup/proxy.pem`; `./etc/proxmox-backup/proxy.key`; `./etc/proxmox-backup/ssl/` |
| `ssh` | SSH Configuration | SSH keys and authorized_keys | `./root/.ssh/`; `./etc/ssh/` |
| `scripts` | Custom Scripts | User scripts and tools | `./usr/local/bin/`; `./usr/local/sbin/` |
| `crontabs` | Scheduled Tasks | Cron jobs and systemd timers | `./etc/cron.d/`; `./etc/crontab`; `./var/spool/cron/`; `./etc/cron.daily/`; `./etc/cron.hourly/`; `./etc/cron.weekly/`; `./etc/cron.monthly/` |
| `services` | System Services | Systemd service configs and related system settings. `/etc/modprobe.d/zfs.conf` is **not written** when this host has its own and it differs: the PVE installer sets `zfs_arc_max` there to 10% of the RAM it found, so the old machine's limit would cap the ARC on more RAM or take memory from the guests on less. A warning gives both values and the backup's copy goes to the export directory. A host without a `zfs.conf` gets the backup's | `./etc/systemd/system/`; `./etc/default/`; `./etc/udev/rules.d/`; `./etc/apt/`; `./etc/logrotate.d/`; `./etc/timezone`; `./etc/sysctl.conf`; `./etc/sysctl.d/`; `./etc/modprobe.d/`; `./etc/modules`; `./etc/iptables/`; `./etc/nftables.conf`; `./etc/nftables.d/` |
| `accounts` | System Accounts & Auth (WARNING) | Local system accounts and sudo policy, applied with a **safe merge** for `passwd`/`group`/`shadow`/`gshadow` that preserves the current host root and system accounts. `/etc/sudoers` is **replaced wholesale** with the backed-up file once `visudo -c` passes, so sudo rules added since the backup are lost; `/etc/sudoers.d` is not part of the category | `./etc/passwd`; `./etc/group`; `./etc/shadow`; `./etc/gshadow`; `./etc/sudoers` |
| `user_data` | User Data (Home Directories) | Root and user home directories (/root and /home) | `./root/`; `./home/` |
| `zfs` | ZFS Configuration | ZFS pool cache and configs. `/etc/hostid` is **not written** when this host has ZFS pools imported under a different hostid, or when its pools cannot be listed: a pool records the hostid it was imported under, and an initramfs rebuilt with another one refuses to import it (for a root pool, the host stops at boot). A warning gives both values. With no pool imported it is written, so pools on disks moved from the old host import under their own hostid. The same rule keeps this host's `/etc/zfs/zpool.cache` and `/etc/zfs/zfs-list.cache/`: another host's cache makes `zfs-import-cache.service` fail at boot and leaves this host's data pools out (PVE brings back a pool that is a `zfspool` storage; a PBS datastore or a manually mounted pool stays out) | `./etc/zfs/`; `./etc/hostid` |
| `boot` | Boot Configuration (Kernel Command Line) | Kernel parameters of the backed-up host (IOMMU, VFIO, ...) **merged** into this host's boot configuration, then initramfs and bootloader rebuilt; see [Kernel Command Line Merge](#11-kernel-command-line-merge-boot-category). Not in BASE or STORAGE | `./var/lib/proxsave-info/commands/system/kernel_cmdline.txt` (the source); `./etc/default/grub`, `./etc/kernel/cmdline` (the live files the merge may write, listed so the safety backup covers them) |
| `proxsave_info` | ProxSave Diagnostics (Export Only) | **Export-only** ProxSave command outputs and inventory reports, and the backed-up host's boot files (GRUB, kernel command line, ESP list) (never written to system) | `./var/lib/proxsave-info/`; `./manifest.json`; `./etc/default/grub`; `./etc/default/grub.d/`; `./etc/kernel/cmdline`; `./etc/kernel/proxmox-boot-uuids` |

### Category Availability

Not all categories are available in every backup. The restore workflow:
1. Analyzes the backup archive
2. Detects which categories contain files
3. Displays only available categories for selection

### PVE Access Control 1:1 (pve_access_control)

On standalone PVE restores (non-cluster backups), ProxSave restores `pve_access_control` **1:1** by applying staged files directly to the pmxcfs mount (`/etc/pve`):
- `/etc/pve/user.cfg` (includes users/roles/groups/ACL)
- `/etc/pve/domains.cfg` (realms, when present)
- `/etc/pve/priv/{shadow,token,tfa}.cfg` (when present)

**Root safety rail**:
- `root@pam` is preserved from the fresh install (not imported from the backup).
- ProxSave forces `root@pam` to keep `Administrator` on `/` (propagate), to prevent lockout.

**Cluster backups**:
- In cluster SAFE mode (no `config.db` restore), ProxSave prompts you to either **skip** access control (recommended) or apply it **1:1 cluster-wide** (including secrets) with a rollback timer.

**TFA**:
- Restored 1:1. Default behavior is **warn** (do not disable users). Some methods (notably WebAuthn) may require re-enrollment if the hostname/FQDN/origin changes.
- For maximum 1:1, keep the same URL origin (same FQDN/hostname and port), restore `network` + `ssl`, and avoid accessing the UI via raw IP.
- In CUSTOM mode, if you select access control without `network`/`ssl`, ProxSave suggests adding them to maximize WebAuthn compatibility.

---

### PBS Access Control 1:1 (pbs_access_control)

ProxSave restores `pbs_access_control` by applying staged files to `/etc/proxmox-backup`:
- `/etc/proxmox-backup/{user,domains,token}.cfg` (when present)
- `/etc/proxmox-backup/acl.cfg`
- `/etc/proxmox-backup/{shadow.json,token.shadow,tfa.json}` (when present)

**Root safety rail**:
- `root@pam` is preserved from the fresh install (not imported from the backup).
- ProxSave forces `root@pam` to keep `Admin` on `/`, to prevent lockout.

**TFA**:
- Restored 1:1. Default behavior is **warn** (do not disable users). Some methods (notably WebAuthn) may require re-enrollment if the hostname/FQDN/origin changes.
- For maximum 1:1, keep the same URL origin (same FQDN/hostname and port), restore `network` + `ssl`, and avoid accessing the UI via raw IP.
- In CUSTOM mode, if you select access control without `network`/`ssl`, ProxSave suggests adding them to maximize WebAuthn compatibility.

---

## Restore Modes

Four predefined modes provide common restoration scenarios, plus custom selection for advanced users. The mode ids are `full`, `storage`, `base`, and `custom`.

> **Note on export-only categories.** FULL keeps the three export-only categories
> (`pve_config_export`, `pbs_config`, `proxsave_info`) and writes them to the export
> directory. The **STORAGE** and **SYSTEM BASE** presets strip export-only categories,
> so an export-only id listed in a preset is silently dropped and never restored.

### 1. FULL Restore

**Description**: Restore everything from backup, including export-only categories (they are exported to a safe directory instead of being written directly)

**Use Cases**:
- Complete disaster recovery
- Migrating to new hardware
- Restoring after system failure

**Categories Included**: All available categories
- **Normal** categories are restored to system paths
- **Staged** categories are extracted under `/tmp/proxsave/restore-stage-*` and applied automatically (API/file apply)
- **Export-only** categories (e.g. `pve_config_export`, `pbs_config`) are extracted to the export directory for manual review/application
- On a `dual` host, FULL restore can include PVE, PBS, and Common categories in
  the same run
- On a single-role host restoring a `dual` backup, the categories of the other
  role are extracted to the export directory instead of being written to the
  system

**Command Flow**:
```text
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
- `storage_pve` - Storage + datacenter + vzdump config (staged apply)
- `pve_jobs` - Backup jobs
- `filesystem` - /etc/fstab
- `storage_stack` - Storage stack config (mount prerequisites)
- `zfs` - ZFS configuration

**PBS Categories**:
- `datastore_pbs` - Datastore definitions (staged apply; API preferred, file fallback in Clean 1:1)
- `maintenance_pbs` - Maintenance settings
- `pbs_jobs` - Sync/verify/prune jobs (staged apply; API preferred, file fallback in Clean 1:1)
- `pbs_remotes` - Remotes for sync jobs (staged apply; API preferred, file fallback in Clean 1:1)
- `filesystem` - /etc/fstab
- `storage_stack` - Storage stack config (mount prerequisites)
- `zfs` - ZFS configuration

**Dual hosts**:
- STORAGE mode on a `dual` host includes the compatible storage-focused
  categories from both product roles plus the common storage categories

**Command Flow**:
```text
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
- `ssh` - SSH daemon configuration (`/etc/ssh`) and root SSH keys (`/root/.ssh/`); keys under `/home/` require the separate `user_data` category
- `services` - Systemd service configs and udev rules
- `filesystem` - /etc/fstab (Smart merge prompt)

**Command Flow**:
```text
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
```text
- categories:
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
```text
Your selection: 1      # Toggle category 1
Your selection: 2      # Toggle category 2
Your selection: c      # Continue to restore plan
```

---



## Complete Workflow

The restore process follows a phased workflow with safety checks at each step.
This section stays operator-focused. Internal decision rules and code-level
behavior live in [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md), while the visual
flow lives in [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md).

### Workflow Diagram

```text
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
  ├─ Detect current system type (PVE/PBS/DUAL/Unknown)
  ├─ Read backup type from manifest
  ├─ Validate compatibility (exact / partial / incompatible)
  └─ Filter to compatible categories when needed

Phase 4: Category Analysis
  ├─ Open and scan archive
  ├─ Check each category for file presence
  └─ Mark categories as available/unavailable

Phase 5: Mode Selection & Category Choice
  ├─ Display restore mode menu
  ├─ User selects mode (Full/Storage/Base/Custom)
  ├─ If Custom: Interactive category selection
  └─ Build final category list

Phase 6: Cluster Restore Mode (PVE backups carrying pve_cluster)
  ├─ SKIPPED if the archive has no pve_cluster payload, or the category is not selected
  ├─ Detect the pve_cluster payload in the archive (not the manifest ClusterMode)
  ├─ Prompt: SAFE (export+API) vs RECOVERY (full restore)
  ├─ SAFE: Redirect pve_cluster to export-only, apply via pvesh
  └─ RECOVERY: Probe the quorum (pvecm status); refuse a quorate cluster with more than 1 node online,
     otherwise proceed with direct database restore

Phase 7: Restore Plan & Confirmation
  ├─ Display detailed restore plan
  ├─ Show categories and file paths
  ├─ Show warnings
  └─ User types "RESTORE" to confirm

Phase 8: Safety Backup
  ├─ Create persistent <BASE_DIR>/restore/TIMESTAMP/ safety backup
  ├─ Preserve permissions, ownership, timestamps
  └─ Display retained safety archive; follow manual rollback prerequisites

Phase 9: Service Management (PVE Cluster Restore)
  ├─ Detect if pve_cluster category selected (RECOVERY mode)
  ├─ Stop: pve-ha-lrm, pve-ha-crm, then pve-cluster, pvedaemon, pveproxy, pvestatd
  ├─ Unmount /etc/pve
  └─ Restart right after the cluster database is written (not at the end of the run):
     pve-cluster, pvedaemon, pveproxy, pvestatd, then pve-ha-crm, pve-ha-lrm

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

Phase 13: SAFE Apply (Cluster SAFE Mode Only)
  ├─ Scan exported datacenter objects (mappings, pools, VM/CT configs)
  ├─ Offer to apply resource mappings via pvesh (`/cluster/mapping/*`)
  ├─ Offer to apply pools via pveum (`pveum pool add/modify`)
  ├─ Offer to apply VM/CT configs via pvesh API
  ├─ Offer to apply storage/datacenter via pvesh (only if `storage_pve` is NOT selected)
  └─ Otherwise handled by `storage_pve` staged restore

Phase 14: Post-Restore Tasks
  ├─ Optional: Apply restored network config with rollback timer (requires COMMIT)
  ├─ Recreate storage/datastore directories
  ├─ Check ZFS pool status when the `zfs` category was restored (including dual hosts)
  ├─ Restart PVE/PBS services (if stopped)
  └─ Display completion summary
```

### Phase-by-Phase Details

#### Phase 1: Backup Selection

**Interactive prompts**: the sources offered are built from the configuration, so a
disabled secondary or an unconfigured cloud remote simply does not appear. Each line is
`<label> (<path>)`:

```text
Select the backup source:
  [1] Local backups (/opt/proxsave/backup)
  [2] Secondary backups (/mnt/secondary/backups)
  [3] Cloud backups (rclone) (GoogleDrive:/proxsave/backup)
  [0] Exit
```

**Backup list display**: one line per backup, describing it from its manifest rather
than from its filename:

```text
- backups:
  [1] 2025-11-20 14:30:52 • Host pve01 • ENCRYPTED • Tool v1.2.0 • PVE v8.2.4 (standalone)
  [2] 2025-11-19 02:00:15 • Host pve01 • ENCRYPTED • Tool v1.2.0 • PVE v8.2.4 (standalone)
  [0] Exit
```

The fields are: creation time, host the backup came from, `ENCRYPTED` or `PLAIN`, the
ProxSave version that wrote it, and the backup's target roles with the Proxmox version
and the cluster mode. Missing manifest values render as `unknown date`, `unknown host`,
`Tool unknown` or `UNKNOWN`.

#### Phase 2: Decryption

**For AGE-encrypted backups**, ProxSave asks for the secret in a **single field**
that accepts either an AGE key or a passphrase (there is no separate "passphrase vs
identity file" sub-menu). In the TUI this is a masked input labeled `Decrypt key`; the
`--cli` prompt is:

```text
Enter decryption key or passphrase for pve01-backup-20251120-143052.tar.xz (0 = exit): ********
Decrypting... (this may take several minutes)
Decryption complete.
Verifying SHA256 checksum...
Checksum verified successfully.
```

#### Phase 3: Compatibility Check

**System detection**:
```text
Current system type: Proxmox Virtual Environment (PVE)
Backup system type: Proxmox Virtual Environment (PVE)
Yes Systems are compatible
```

**Partial-compatibility warning**:
```text
Warning: WARNING: Partial compatibility detected

Current system: Proxmox Virtual Environment (PVE)
Backup source: Proxmox VE + Proxmox Backup Server (DUAL)

ProxSave will continue with the categories compatible with the current host:
- PVE categories
- Common categories
```

**Incompatibility warning**:
```text
Warning: WARNING: Potential incompatibility detected!

Current system: Proxmox Backup Server (PBS)
Backup source: Proxmox Virtual Environment (PVE)

This backup may contain PVE-specific configurations that are not
compatible with PBS. Proceeding may result in system instability.

Type "yes" to continue anyway or "no" to abort:
```

This guide intentionally shows the operator-facing outcomes only. The exact
metadata precedence, host detection order, and capability-overlap rules are
documented in [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md#phase-3-system-detection--compatibility).

#### Phase 6: Cluster Restore Mode (PVE backups carrying pve_cluster)

This phase runs whenever the archive carries a `pve_cluster` payload (`/var/lib/pve-cluster/`) and that category is part of the restore. That is decided from the archive contents, not from the manifest, so a **standalone** PVE backup normally reaches it too: see [PVE Restore: Standalone vs Cluster](#pve-restore-standalone-vs-cluster). On a standalone node, RECOVERY is the answer that writes the database back.

When the payload is present and the `pve_cluster` category is selected:

```text
Cluster backup detected. Choose how to restore the cluster database:
  [1] SAFE: Do NOT write /var/lib/pve-cluster/config.db. Export cluster files only (manual/apply via API).
  [2] RECOVERY: Restore full cluster database (/var/lib/pve-cluster). Use only when cluster is offline/isolated.
  [0] Exit

Choice: _
```

When RECOVERY is chosen and the node has a `corosync.conf` (`/etc/pve/corosync.conf` or `/etc/corosync/corosync.conf`), ProxSave runs `pvecm status` before any restore step:

| `pvecm status` reports | Result |
|---|---|
| Quorate, more than 1 node online | The restore stops: `Cluster RECOVERY refused - quorate cluster, N nodes online: its copy would replace the restored config.db` |
| Quorate, 1 node online | Proceeds |
| Not quorate | Proceeds |
| Cannot be read (pvecm is not installed, fails, times out, or prints no `Quorate:` line), or quorate with a `Nodes:` count that is not a number, and `systemctl is-active corosync` says `inactive` or `failed` | Proceeds, with the warning `Cluster RECOVERY - quorum unknown (<reason>), corosync inactive, proceeding` (or `failed`) |
| Same, with corosync in any other state (`active`, `activating`, ...) or a state that cannot be read | The restore stops before anything is stopped or written: `Cluster RECOVERY refused - quorum unknown (<reason>), corosync <state>: in a quorate cluster, its copy would replace the restored config.db` |

A node without `corosync.conf` (standalone) proceeds without the probe. The refusal exists because on a member of a quorate cluster the restored `config.db` does not survive: when `pve-cluster` starts again, pmxcfs syncs from the cluster leader and the leader's copy replaces the restored one. pvecm also fails when pmxcfs is down (no `/etc/pve/corosync.conf`) while corosync is up and quorate with its peers; `pve-cluster` would then start again and sync from the leader. So a quorum that cannot be read lets the restore proceed only when systemctl shows corosync stopped, which is also the state after `systemctl stop corosync`. Isolate the node first (see [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md)), or use SAFE.

See [Cluster Restore Modes](#cluster-restore-modes-safe-vs-recovery) for detailed explanation.

#### Phase 7: Restore Plan

**Example display**:
```text
═══════════════════════════════════════════════════════════════
RESTORE PLAN
═══════════════════════════════════════════════════════════════

Restore mode: STORAGE only (cluster + storage + jobs + mounts)
System type:  Proxmox Virtual Environment (PVE)

Categories to restore:
  1. PVE Cluster Configuration
     Proxmox VE cluster configuration and database
	  2. PVE Storage Configuration
	     Storage definitions (applied via API) and VZDump configuration
  3. PVE Backup Jobs
     Scheduled backup job definitions
  4. Filesystem Configuration
     Mount points and filesystems (/etc/fstab) - WARNING: Critical for boot
  5. Storage Stack (Mounts/Targets)
     Storage stack configuration used by mounts (iSCSI/LVM/MDADM/multipath/autofs/crypttab)
  6. ZFS Configuration
     ZFS pool cache and configuration files

	Files/directories that will be restored:
	  • /var/lib/pve-cluster/
	  • /etc/pve/storage.cfg
	  • /etc/pve/datacenter.cfg
	  • /etc/vzdump.conf
	  • /etc/pve/jobs.cfg
	  • /etc/pve/vzdump.cron
	  • /etc/fstab
  • /etc/crypttab
  • /etc/iscsi/
  • /var/lib/iscsi/
  • /etc/multipath/
  • /etc/multipath.conf
  • /etc/mdadm/
  • /etc/lvm/backup/
  • /etc/lvm/archive/
  • /etc/autofs.conf
  • /etc/auto.master
  • /etc/auto.master.d/
  • /etc/auto.*
  • /etc/zfs/
  • /etc/hostid

Warning: WARNING:
  • Existing files at these locations will be OVERWRITTEN
  • A safety backup will be created before restoration
  • Services may need to be restarted after restoration
  • PVE cluster services will be stopped during restore

Type 'RESTORE' to proceed or 'cancel' to abort:
```

The file list names what the extraction writes over the system. Export-only categories (their files go to the export directory), the `boot` category (it merges kernel parameters into this host's own file) and the backed-up host's boot files (never written) are not in it.

Confirmation is **two stages**. After you type `RESTORE` (or, in the TUI, press the
`RESTORE` button), ProxSave asks a second, explicit overwrite question before touching
anything:

```text
This operation will overwrite existing configuration files on this system.

Proceed with overwrite? (yes/no):
```

In the TUI this second gate is a danger-styled confirm with `Overwrite and restore` /
`Cancel` buttons defaulting to Cancel. Only after both stages does extraction begin.

#### Phase 8: Safety Backup

```text
Creating safety backup of existing files...
Safety backup created successfully.
Safety backup location: /opt/proxsave/restore/20251120_143052/restore_backup_20251120_143118.tar.gz

Safety backup - holds the current versions of the files this restore overwrites (tar.gz, paths relative to /)
```

#### Phase 9: Service Management (PVE Cluster)

**For cluster database restore (RECOVERY mode)**:
```text
Preparing system for cluster database restore: stopping PVE services and unmounting /etc/pve

Stopping pve-ha-lrm...
Stopping pve-ha-crm...
Stopping pve-cluster...
Stopping pvedaemon...
Stopping pveproxy...
Stopping pvestatd...
All PVE services stopped successfully.

Unmounting /etc/pve...
Successfully unmounted /etc/pve
```

The HA services are stopped first: `pve-ha-lrm` keeps the node's watchdog open, and with `pve-cluster` down for 60 seconds that watchdog expires and the node is hard-reset (fenced). Stopping the LRM freezes its HA resources and closes the watchdog cleanly; stopping the CRM releases its lock so another node takes over as master.

`pve-ha-lrm` is never signalled: it gets one `systemctl stop --no-block` and up to 180 seconds to go inactive. If it is still active after that, ProxSave runs `systemctl start pve-ha-lrm`, which cancels the queued stop and leaves the LRM running, and the restore stops with `failed to stop PVE services (pve-ha-lrm)`. Its stop waits for the CRM master to acknowledge the freeze; when the old master is a node that went down, its lock only times out about 120 seconds after it died (measured: 93 seconds on an isolated node whose old master was powered off). A SIGKILL before the LRM closes its watchdog would fence the node. The other services keep the escalating stop (blocking stop, then SIGTERM, then SIGKILL).

The services are started again as soon as the cluster database has been written, before the later steps (network apply, boot rebuild): `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd`, then `pve-ha-crm` and `pve-ha-lrm`. If the restore fails before that point, they are started when the run ends. A unit that fails to start does not stop the others, except that `pve-ha-crm` and `pve-ha-lrm` are not started while `pve-cluster` is down (the LRM would arm the watchdog without pmxcfs). There is one attempt per restore, of three tries per unit; the restore goes on, and its closing advice names the units left down.

#### Phase 10: Service Management (PBS)

**For PBS configuration restore**:
```text
Preparing PBS system for restore: stopping proxmox-backup services

Stopping proxmox-backup-proxy...
Stopping proxmox-backup...
PBS services stopped successfully.
```

If service stop fails, you'll be prompted:
```text
Unable to stop PBS services automatically: <error>
Continue restore with PBS services still running? (y/N): _
```

#### Phase 11 & 12: Extraction

```text
Extracting selected categories from archive into /
Detailed restore log: /opt/proxsave/restore/20251120_143052/restore_20251120_143409_1.log

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
```text
SAFE cluster restore: applying configs (node=pve01)

Found 3 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
Applied VM/CT config 100 (webserver)
Applied VM/CT config 101 (database)
VM/CT apply completed: ok=2 failed=0

Storage configuration found: .../etc/pve/storage.cfg
Apply storage.cfg via pvesh? (y/N): y
Applied storage definition local
Storage apply completed: ok=1 unknown=0 failed=0
```

See [pvesh SAFE Apply](#pvesh-safe-apply-cluster-safe-mode) for detailed explanation.

#### Phase 14: Completion

```text
═══════════════════════════════════════════════════════════════
RESTORE COMPLETED
═══════════════════════════════════════════════════════════════

Restore completed successfully.
Temporary decrypted bundle removed.
Detailed restore log: /opt/proxsave/restore/20251120_143052/restore_20251120_143409_1.log
Export directory: /opt/proxsave/proxmox-config-export-20251120-143052/
Safety backup preserved at: /opt/proxsave/restore/20251120_143052/restore_backup_20251120_143118.tar.gz
Safety backup - kept until removed, ProxSave never deletes it

Services - some restored files take effect only when the services that read them restart
  PVE services - stopped and started again during this restore
REBOOT RECOMMENDED: Reboot the node (or at least restart networking and core services) so hostname/IP and service changes from the restore are fully applied.

Recreating storage directories from /etc/pve/storage.cfg...
Created: /mnt/backup/dump/
Created: /mnt/backup/images/
Storage directories recreated successfully.
```

---

## PVE Restore: Standalone vs Cluster

Use [cluster decisions and recovery scenarios](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery). SAFE applies live configuration; RECOVERY replaces the database only on an offline or isolated target.

### Detection

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Behavior Comparison

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Standalone Restore

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Cluster Restore - SAFE Mode

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Cluster Restore - RECOVERY Mode

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### When to Use Each Mode

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

## Cluster Database Restore

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Understanding PVE Cluster Filesystem

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Cluster Restore Modes: SAFE vs RECOVERY

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Prerequisites

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#pre-recovery-checklist).

### Automatic Service Management

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Service Stop/Restart Flow

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#cluster-restore-modes-safe-vs-recovery).

### Post-Restore Verification

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#post-recovery-verification).

### Multi-Node Cluster Considerations

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#pre-recovery-checklist).

### Common Issues and Solutions

See the [cluster recovery procedure](CLUSTER_RECOVERY.md#common-issues-and-solutions).

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
```text
/etc/pve (FUSE mount)
    ↑
    Managed by pmxcfs daemon
    ↑
    Backend: /var/lib/pve-cluster/config.db

Writes through mounted /etc/pve:
  Persist in config.db and replicate within the cluster
  Can change live storage, guest and access configuration
  Need review before replacing existing settings
```

**Code Protection**:
```text
The archive extraction guard skips direct writes under /etc/pve when restoring to /
(see internal/orchestrator/restore_archive_entries.go)
Confirmed API and pmxcfs applies use separate paths
```

### Export-Only Category: pbs_config

**Category**: `pbs_config`  
**Path**: `./etc/proxmox-backup/`  
**Reason**: The full PBS configuration directory contains high-risk and secret-bearing files; ProxSave restores specific subsets via staged categories (e.g. `pbs_access_control`, `pbs_notifications`, `pbs_remotes`) and leaves the full directory as export-only for manual review.

### Export Process

**Extraction Passes**:
```text
Pass 1: Normal Categories
  ├─ Destination: / (system root)
  ├─ Categories: All non-export-only
  ├─ Safety backup: Created before extraction
  └─ Log: /opt/proxsave/restore/TIMESTAMP/restore_<extraction timestamp>_<seq>.log

Pass 2: Export-Only Categories
  ├─ Destination: <BASE_DIR>/proxmox-config-export-YYYYMMDD-HHMMSS/
  ├─ Categories: Export-only (e.g. pve_config_export, pbs_config)
  ├─ Safety backup: Not created (not overwriting system)
  └─ Log: Its own file in the same directory, restore_<extraction timestamp>_<seq>.log

Pass 3: Staged Categories
  ├─ Destination: /tmp/proxsave/restore-stage-* (deleted when the restore ends)
  ├─ Categories: Sensitive staged apply (e.g. network, notifications, access control)
  └─ Apply: Written after extraction via safe file/API apply steps
```

**Export Directory Structure**:
```text
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
- In **FULL restore**, export-only categories are included automatically (but extracted to a separate export directory)
- In **STORAGE** and **SYSTEM BASE** modes, export-only categories are not included
- In **CUSTOM selection**, you can explicitly select export-only categories

**Selection**:
```text
- categories:
  [1] [ ] PVE Config Export
      Export-only copy of /etc/pve (never written to system paths)
      ↑ Clear description warns user
```

**Restore Plan Display**:
```text
Export-only categories (will be extracted to separate directory):
  • PVE Config Export
    Destination: /opt/proxsave/proxmox-config-export-20251120-143052/
```

**Important**: ProxSave extracts export-only files to the separate directory shown above. The tool **does NOT automatically copy** these files to system paths. Any `cp` commands shown in this documentation are **manual examples** that you must execute yourself after reviewing the exported files. This design prevents accidental overwrites and gives you full control over what gets restored.

### Integration with Cluster Restore

**Correct Approach**: Use BOTH categories

```text
Custom selection:
  [X] PVE Cluster Configuration ← Restores /var/lib/pve-cluster/config.db
  [X] PVE Config Export         ← Exports /etc/pve/ for reference

Result:
  1. config.db restored -> New cluster configuration active
  2. /etc/pve/ exported -> Old configuration available for comparison
  3. User can review differences and selectively copy needed files
```

**Why Both?**:
- `pve_cluster`: Restores the database (actual restore)
- `pve_config_export`: Provides reference copy of old /etc/pve (for comparison)

### pvesh SAFE Apply (Cluster SAFE Mode)

When using **SAFE cluster restore mode**, the workflow offers to apply exported configurations and datacenter-wide objects via the Proxmox VE tooling (`pvesh` / `pveum`). This allows you to restore individual configurations without replacing the entire cluster database.

**Available Actions**:

#### 1. Resource Mappings Apply (PCI/USB/Dir)

If your VM/CT configs use `mapping=<id>` (notably for PCI/USB passthrough), ProxSave can apply the cluster resource mappings first:
- Applied via `pvesh` to `/cluster/mapping/{pci,usb,dir}` (when present in the backup)
- Recommended to run **before** VM/CT apply to avoid missing-mapping errors

#### 2. Resource Pools Apply (Pools)

ProxSave can restore pool definitions and membership (merge semantics):
- Pools are parsed from the exported `user.cfg`
- Applied via `pveum pool add` / `pveum pool modify`
- Membership (VMIDs + storages) is applied later in the SAFE apply flow (after VM/CT configs and storage)

#### 3. VM/CT Configuration Apply

```text
Found 5 VM/CT configs for node pve01
Apply all VM/CT configs via pvesh? (y/N): y
```

For each VM/CT config found in the export:
- Reads config from `<export>/etc/pve/nodes/<node>/qemu-server/<vmid>.conf`
- Loads one cluster-wide inventory with `pvesh get /cluster/resources --type vm --output-format=json` before changing any guest
- Stops the entire selected guest batch without mutation if the inventory command fails or its JSON is malformed, incomplete, or contains duplicate VMIDs
- Skips a VMID already owned by another node; SAFE does not move guests between nodes
- Skips a VMID whose live type (`qemu` or `lxc`) differs from the staged config type
- Updates a matching guest on the current node with `pvesh set /nodes/<node>/<type>/<vmid>/config --<key>=<value> ...`, excluding create-only keys
- A recognized API schema refusal permits file fallback only if `status/current` explicitly reports `stopped`; other API failures do not qualify
- For a schema refusal with a running or unverified guest, ProxSave may retry the API update with refused keys removed; it does not write the staged file
- Registers a VMID that is absent cluster-wide by writing the staged conf into pmxcfs on the current node
- Reports success/failure for each VM/CT

**Note**: This registers or updates guest configurations only. Disk images are NOT affected.

#### 4. Storage Configuration Apply

```text
Storage configuration found: /opt/proxsave/proxmox-config-export-*/etc/pve/storage.cfg
Apply storage.cfg via pvesh? (y/N): y
```

Parses `storage.cfg` and applies each storage definition:
- Each `storage: <name>` block is extracted
- Applied via: `pvesh create /storage --storage=<id> --type=<type> --<key>=<value> ...`
- Blocks are keyed on the `<type>: <id>` header (`dir: local`, `nfs: backup`); the older `storage: <id>` form is still accepted
- If `pvesh create` fails (including when the definition already exists), ProxSave retries with `pvesh set /storage/<id>` and drops keys that the live set schema reports as create-only
- The storage is counted as failed only when both the create attempt and the set fallback fail

**Note**: Storage directories are NOT created automatically. Run `pvesm status` to verify, then create missing directories manually.

#### 5. Datacenter Configuration Apply

```text
Datacenter configuration found: /opt/proxsave/proxmox-config-export-*/etc/pve/datacenter.cfg
Apply datacenter.cfg? (y/N): y
```

Applies datacenter-wide settings:
- Applied via: file write into pmxcfs (`/etc/pve/datacenter.cfg`)
- Affects all cluster nodes (pmxcfs replicates the file cluster-wide)

**Interactive Flow**:
```text
SAFE cluster restore: applying configs (node=pve01)

Apply PVE resource mappings (pvesh)? (y/N): y
Applied pci mapping device1

Apply PVE resource pools (merge)? (y/N): y
Applied pool definition dev

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
Storage apply completed: ok=2 unknown=0 failed=0

Datacenter configuration found: .../etc/pve/datacenter.cfg
Apply datacenter.cfg? (y/N): n
Skipping datacenter.cfg apply

Pools apply (membership) completed: ok=1 failed=0
```

**Reading the storage summary**: `Storage apply completed: ok=N unknown=M failed=K` counts three
different outcomes, and `unknown` is neither of the other two.

| Count | What it means |
|---|---|
| `ok` | the definition was applied, or the restore read the live one and confirmed it already holds every staged value |
| `failed` | the restore established that a staged value is NOT in effect and could not put it back |
| `unknown` | nothing reached the node and the restore could not establish whether the staged values are already in effect |

A definition lands in `unknown` when the update schema refuses every staged key (they are
create-only on that storage type) AND the live definition could not be compared: `pvesh get
/storage/<id>` failed, or a refused key is absent from the live object or is not a plain scalar.
The run says so by name:

```text
Applied nothing for storage nas: every staged key refused, not comparable (server, export)
```

**Neither `unknown` nor `failed` halts the restore**, in either apply path. A non-zero count is
not a stop; the two paths differ only in what the run's outcome records:

- The SAFE cluster apply shown above logs the summary and the per-definition `WARNING`, then goes
  straight on to `datacenter.cfg`. Nothing in that flow returns an error for a storage count, so
  the log lines are the whole record.
- The staged apply (`PVE staged apply: storage.cfg applied (ok=N unknown=M failed=K)`) does return
  an error when `failed > 0`, but the caller does not treat it as an abort: it marks the run as
  completed WITH WARNINGS and keeps applying the remaining categories. Only a user abort or an
  inconsistent-state failure halts a restore.

Leaving `unknown` short of a failure is deliberate: nothing there was shown to be wrong, so
counting it as one would be a guess in the other direction. The consequence for the operator is
the same either way. **A restore that ended without an error is not the same as a restore that is
complete.** Check every definition the run named, with `pvesh get /storage/<id>
--output-format=json` against the `storage.cfg` in the export, and set by hand anything that
differs.

**Scope of pvesh Apply**:
- Applies confirmed configuration groups without wholesale database replacement
- Updates live state and can affect the entire cluster
- Logs individual results, including skips and partial failures
- Does not provide an automatic transaction rollback for every API change

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

**Best for**: Applying an eligible guest configuration batch on an active cluster without replacing its whole database. For a single guest, decline the batch and follow the reviewed manual procedure.

**How it works**:
1. During restore, select **Cluster SAFE mode**
2. Configurations are exported to temporary directory
3. Interactive prompt asks: "Apply all VM/CT configs via pvesh?"
4. Each config applied via Proxmox API: `pvesh set /nodes/<node>/qemu/<vmid>/config`

**Behavior**:
- Uses the running cluster and avoids a full database replacement
- Confirms all eligible guest configurations from the selected source node as one batch
- Checks cluster-wide VMID ownership and guest type before mutation
- Changes live configuration; review per-guest results and the possible effect on workloads

**Step-by-Step Procedure**:

1. **Open the restore workflow**: run `proxsave` bare and pick **Tools > Restore** in
   the dashboard. Direct entry options are in [CLI reference](CLI_REFERENCE.md):
   Choose **Tools > Restore** in the dashboard; direct entry is in [CLI reference](CLI_REFERENCE.md).

2. **Select backup and decrypt** (standard workflow)

3. **When prompted for restore mode**, if backup is from cluster node:
   ```text
   Backup marked as cluster node; enabling guarded restore options

   Cluster restore mode:
     [1] SAFE mode (export configs + API apply)
     [2] RECOVERY mode (restore cluster database)
     [0] Cancel

   Select: 1
   ```

4. **Select categories** (Custom mode only needed if you want to exclude components; FULL already includes the `pve_config_export` export)

5. **After extraction completes**, you'll see:
   ```text
   SAFE cluster restore: applying configs (node=pve01)

   Found 5 VM/CT configs for node pve01
   Apply all VM/CT configs via pvesh? (y/N): y
   ```

   **If the node name changed** (example: backup from `pve-old`, restore on `pve-new`), ProxSave prompts for the exported source node:
   ```text
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
   ```text
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
- Full control over what gets restored
- Review before applying
- Compare with current configs
- Extract individual values without full restore

**Step-by-Step Procedure**:

1. **Open the restore workflow and select the pve_config_export category**: run
   `proxsave` without arguments and pick **Tools > Restore**. For direct entry when the dashboard cannot render, see [CLI reference](CLI_REFERENCE.md#restore-from-backup). Then select `CUSTOM`
   mode and enable the `PVE Config Export` category.

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
- Complete restore of entire cluster state
- All VMs, users, storage, settings restored together
- Ideal for disaster recovery

**Disadvantages**:
- Service interruption required
- Overwrites current cluster state
- All-or-nothing (can't selectively restore single VM)
- Risk of cluster desynchronization in multi-node setups

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

```text
Are you restoring to an active multi-node cluster?
├─ YES -> Use Method 1: pvesh SAFE Apply
│        (Confirmed batch changes to live configuration)
│
└─ NO -> Is this a complete disaster recovery?
    ├─ YES -> Use Method 3: Full Cluster Database Restore
    │        (Restores everything at once)
    │
    └─ NO -> Do you need to restore all VMs?
        ├─ YES -> Use Method 1: pvesh SAFE Apply
        │        (Faster than manual copy)
        │
        └─ NO -> Use Method 2: Manual Copy
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
```text
What ProxSave Backs Up:
- VM/CT configuration files (*.conf)
- Cluster settings
- Storage definitions
- User/permissions
- Network configuration
- Backup job definitions

What ProxSave Does NOT Back Up:
Not available VM/CT disk images (*.qcow2, *.raw, etc.)
Not available Running VM memory state
Not available Application data inside VMs
Not available Storage pool data
```

**Best Practice Recommendation**:
1. Use ProxSave for **configuration backup** (what it's designed for)
2. Use Proxmox vzdump for **VM disk backups**
3. Combine both for complete disaster recovery capability

---

## Safety Features

Multiple layers of protection prevent data loss and corruption during restore.

### 1. Safety Backup

**Automatic** backup before any changes are written, on the selective restore path and on the full-restore fallback.

> The full-restore fallback runs when ProxSave cannot analyse the archive's categories: it announces `Backup category analysis failed; ProxSave will run a full restore (no selective modes)` and, after confirmation, extracts the whole archive onto `/`. It takes this safety backup first, but not the network, firewall, HA and access control rollback archives, since it runs none of the transactional applies they undo. On a PBS host it stops the PBS services when the plan includes PBS categories. It does not stop `pve-cluster` and does not write the cluster database (`/var/lib/pve-cluster/`), since it cannot tell whether the archive holds usable cluster data. Export-only categories, and on a single-role host the categories of the other product, are kept off the live system.

**Location**: `<BASE_DIR>/restore/YYYYMMDD_HHMMSS/restore_backup_YYYYMMDD_HHMMSS.tar.gz` (`/opt/proxsave/restore/...` by default) (outside `/tmp`, so it survives the reboot the restore recommends)

**Contents**:
- All files that will be overwritten by restore
- Full directory structures
- Preserved permissions, ownership, timestamps

**Rollback**: inspect the exact session archive and follow the
[manual rollback prerequisites](#manual-rollback-prerequisites). A generic
`tar -xzf ... -C /` command does not handle live services, pmxcfs or HA, and does not
undo every API change made later in the workflow.

**If Safety Backup Fails**:
```text
Failed to create safety backup: <error>

Continue without safety backup? (yes/no): _
```
- User can choose to abort (safe) or continue (risky)

### 2. Interactive Confirmation

**Multiple abort points** throughout workflow:

```text
Abort Points:
  [0] Cancel backup selection
  [0] Cancel restore mode selection
  [0] Cancel category selection
  [cancel] Cancel at restore plan confirmation
  [Ctrl+C] Cancel at any time
```

**Confirmation Requirement**:
```text
Type "RESTORE" (exact case) to proceed, or "cancel"/"0" to abort: _
```
- Must type exact word "RESTORE"
- Case-sensitive
- Prevents accidental restoration

**Prompt timeouts (auto-skip)**:
- Some interactive prompts include a visible countdown (currently **90 seconds**) to avoid getting "stuck" waiting for input in remote/automated scenarios.
- If the user does not answer before the countdown reaches 0, ProxSave proceeds with a **safe default** (no destructive action) and logs the decision.

Current auto-skip prompts:
- **Smart `/etc/fstab` merge**
- **Live network apply** ("Apply restored network configuration now...")
- **PVE access control apply** (cluster-wide)
- **PVE firewall apply**
- **PVE HA apply**

On timeout these prompts answer **No** to the requested operation. This does not undo earlier phases. In particular, skipping live network reload can still leave validated network files installed for the next restart; see [Network Safe Apply](#4-network-safe-apply-optional).

That is worth separating from the Enter-key default, which the countdown does not follow. The fstab prompt defaults to **Yes** when the backup root, and swap where comparable, match the current system, so pressing Enter applies the merge. Letting the countdown run out still answers No. In other words the fstab merge is never applied unattended, however well the systems match.

### 3. Compatibility Validation

Compatibility is evaluated with the same `pve | pbs | dual | unknown` model
described in [System Types and Compatibility](#system-types-and-compatibility).

Operator-visible behavior is:
- exact role match: proceed normally
- partial overlap: continue with warnings and automatic category filtering
- no overlap: warn before continuing
- unknown: warn because role-specific validation is incomplete

For the internal precedence rules and implementation path, see
[RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md#phase-3-system-detection--compatibility).

### 4. Network Safe Apply (Optional)

If the **network** category is restored, ProxSave can optionally apply the
new network configuration immediately using a **transactional rollback timer**.

**Apply prompt auto-skip**:
- The "apply now" prompt includes a **90-second** countdown; if you do not answer in time, ProxSave defaults to **No** and skips the live reload.

**Important (console recommended)**:
- Run the live network apply/commit step from the **local console** (physical console, IPMI/iDRAC/iLO, Proxmox console, or hypervisor console), not from SSH.
- If the restored network config changes the management IP or routes, your SSH session will drop and you may be unable to type `COMMIT`.
- In that case, ProxSave will treat the lack of `COMMIT` as "not confirmed" and will restore the previous network settings (rollback).

**How it works**:
- On live restores (writing to `/`), ProxSave **stages** network files first under `/tmp/proxsave/restore-stage-*` and does **not** overwrite `/etc/network/*` during archive extraction.
- After extraction, ProxSave performs a prevention-first **staged install**: it writes the staged files to disk (no reload), runs safe NIC repair + preflight validation, and **rolls back automatically** if validation fails (leaving the staged copy for review until the restore ends, when the staging directory is deleted).
- If rollback backup creation fails (or ProxSave is not running as root), ProxSave keeps network files staged and avoids writing to `/etc`.
- When you choose to apply live, ProxSave (re)validates and reloads networking inside the rollback timer window.
- ProxSave arms a local rollback job **before** applying changes
- Rollback restores **only network-related files** using a dedicated archive under `/opt/proxsave/restore/<timestamp>/network_rollback_backup_*` (so it won't undo other restored categories)
- Rollback also prunes network config files that were **created after** the backup (e.g. extra files under `/etc/network/interfaces.d/`), so rollback returns to the exact pre-restore state
- The user has **180 seconds** to type `COMMIT`
- If `COMMIT` is not received, ProxSave triggers the rollback and restores the pre-restore network configuration
- If the network-only rollback archive is not available, ProxSave prompts before falling back to the full safety backup (or skipping live apply)

This protects SSH/GUI access during network changes.

**Health checks**:
- After applying changes, ProxSave runs local checks (SSH route if available, default route, link state, IP addresses, gateway ping, DNS config/resolve, local web UI port)
- On PVE systems, additional checks are included for cluster networking: `/etc/pve` (pmxcfs) mount status, `pve-cluster` / `corosync` service state, and `pvecm status` quorum
- The result is shown to help decide whether to type `COMMIT`
- Diagnostics are saved under `/opt/proxsave/restore/<timestamp>/network_apply_*` (snapshots `before.txt` / `after.txt` / `after_rollback.txt` when relevant, `health_before.txt` / `health_after.txt`, `preflight.txt`, `plan.txt`, and `ifquery_*`)

**NIC name repair**:
- If physical NIC names changed after reinstall (e.g. `eno1` -> `enp3s0`), ProxSave attempts an automatic mapping using backup network inventory (permanent MAC / MAC / PCI path / udev IDs like `ID_PATH`, `ID_NET_NAME_PATH`, `ID_NET_NAME_SLOT`, `ID_SERIAL`)
- When a safe mapping is found, `/etc/network/interfaces` and `/etc/network/interfaces.d/*` are rewritten before applying the network config
- If you skip live network apply, ProxSave may still install the staged config to disk (no reload) after safe NIC repair + preflight; if validation fails, it rolls back and keeps the staged copy.
- If a mapping would overwrite an interface name that already exists on the current system, ProxSave prompts before applying it (conflict-safe)
- If persistent NIC naming rules are detected (custom udev `NAME=` rules or systemd `.link` files), ProxSave warns and prompts before applying NIC repair to avoid conflicts with user-intended naming
- A backup of the pre-repair files is stored under `/opt/proxsave/restore/<timestamp>/nic_repair_*`

**Preflight validation**:
- After NIC repair, ProxSave runs a **gate** validation of the ifupdown configuration before reloading networking (e.g. `ifup -n -a` / `ifup --no-act -a` / `ifreload --syntax-check -a`)
- If validation fails, live apply is aborted and the validator output is saved under `/opt/proxsave/restore/<timestamp>/network_apply_*/preflight.txt`
- Additionally (diagnostics-only), ProxSave can run `ifquery --check -a` **before and after apply** to show how the runtime state matches the target config. Its output is saved under `/opt/proxsave/restore/<timestamp>/network_apply_*/ifquery_*`. Note that `ifquery --check` can show `[fail]` **before apply** even when the config is valid (because the running state still reflects the old config).
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
- *Pre-apply IP* is derived from the `before.txt` snapshot in `/opt/proxsave/restore/<timestamp>/network_apply_*` and may be `unknown` if it cannot be parsed.
- *Post-apply IP* is what ProxSave could observe on the management interface after applying the new config; it may include CIDR suffixes (for example `10.0.0.4/24`) or multiple addresses.

**Example outputs**

The `NETWORK ROLLBACK` block is printed just before the standard ProxSave footer (the footer color reflects the exit status, e.g. magenta on Ctrl+C).

Example 1, **ARMED** (rollback pending, countdown shown for a few seconds):
```text
===========================================
NETWORK ROLLBACK

  Status: ARMED (will execute automatically)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /opt/proxsave/restore/20260122_153012/network_rollback_20260122_153012.log

Connection will be temporarily interrupted during restore.
Remember to reconnect using the pre-apply IP: 192.168.1.100
  Remaining: 147s
===========================================

===========================================
ProxSave - Go - <build signature>
===========================================
```

Example 2, **EXECUTED** (rollback already ran, no countdown):
```text
===========================================
NETWORK ROLLBACK

  Status: EXECUTED (marker removed)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /opt/proxsave/restore/20260122_153012/network_rollback_20260122_153012.log

Rollback executed: reconnect using the pre-apply IP: 192.168.1.100
===========================================
```

Example 3, **DISARMED/CLEARED** (rollback will not run, applied config remains active):
```text
===========================================
NETWORK ROLLBACK

  Status: DISARMED/CLEARED (marker removed before deadline)
  Pre-apply IP (from snapshot): 192.168.1.100
  Post-apply IP (observed): 10.0.0.4/24
  Rollback log: /opt/proxsave/restore/20260122_153012/network_rollback_20260122_153012.log

Rollback will NOT run: reconnect using the post-apply IP: 10.0.0.4/24
===========================================
```

### 5. Smart `/etc/fstab` Merge (Optional)

If the restore includes filesystem configuration (notably `/etc/fstab`), ProxSave can run a **smart merge** instead of blindly overwriting your current `fstab`.

**What it does**:
- Compares the current `/etc/fstab` with the backup copy.
- Keeps existing critical entries (for example, root and swap) when they already match the running system.
- Detects **safe mount candidates** from the backup (for example, additional NFS mounts) and offers to add them.
- If ProxSave inventory data is present in the backup, ProxSave can remap **unstable** `/dev/*` devices from the backup (for example `/dev/sdb1`) to stable `UUID=`/`PARTUUID=`/`LABEL=` references **on the restore host** (only when the stable reference exists on the system). Note: backups taken from an **unprivileged container/rootless** environment may not include usable block-device inventory, so automated remap can be limited/unavailable.
- Normalizes restored entries by adding `nofail` (and `_netdev` for network mounts) so offline storage does not block boot/restore.

**Safety behavior**:
- The user is prompted before any change is written.
- The **90-second** countdown always answers **No** when it expires. A matching root/swap can make **Yes** the Enter-key default, but never the timeout answer.

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
- Blocks direct archive extraction into `/etc/pve`; confirmed API and pmxcfs applies remain separate write paths

**PBS Datastore Mount Guards**:
- When restoring PBS datastore definitions, ProxSave can apply a temporary read-only bind-mount guard on mount roots that currently resolve to the root filesystem. If the bind mount cannot be created, it logs a warning and proceeds unguarded (no persistent `chattr +i` flag is set).
- Purpose: prevent accidental writes to `/` if a datastore mountpoint is missing/offline at restore time (PBS will show the datastore as unavailable until storage is mounted).
- Optional cleanup: dashboard **Recovery > Cleanup guards**. Its first screen is a read-only check before Apply. See **Clearing mount guards after the storage is back** below.

**PVE Storage Mount Guards**:
- When restoring PVE storage definitions (from `storage.cfg`), ProxSave applies the same "restore even if offline" strategy for mount-backed storage:
  - Network storages (`nfs`, `cifs`, `cephfs`, `glusterfs`) use mountpoints under `/mnt/pve/<storageid>`. ProxSave attempts `pvesm activate <storageid>`; if the mountpoint still resolves to the root filesystem, it applies a temporary read-only bind-mount guard (or, if the bind mount cannot be created, logs a warning and proceeds unguarded).
  - `dir` storages are guarded only when their `path` lives under a mountpoint restored via `/etc/fstab` (to avoid guarding local root filesystem paths).
- Purpose: prevent PVE from writing into `/mnt/pve/...` (or other mount roots) when the backing storage is offline at restore time.
- Optional cleanup: dashboard **Recovery > Cleanup guards**. Its first screen is a read-only check before Apply. See **Clearing mount guards after the storage is back** below.

**Clearing mount guards after the storage is back**:
- The normal route is the dashboard: run `proxsave` bare and pick **Recovery > Cleanup guards**. It is a two-step screen, a read-only check first (which offers Apply only when it actually finds guards), then the cleanup itself, reporting `DONE` or `PENDING`. For direct cleanup entry and preview flags, see [CLI reference](CLI_REFERENCE.md).
- Bringing the storage online again is enough to *use* it: a real mount stacks on top of a bind-mount guard automatically. The guard is not deleted, only shadowed; a reboot or a cleanup run removes the bind-mount leftover. A **legacy** `chattr +i` flag (set by older versions when a bind mount failed) leaves the directory immutable across reboots until it is cleared.
- The cleanup unmounts bind-mount guards **and** clears any **legacy** `chattr +i` immutable flags, but only on mountpoints that are **not currently mounted** (clearing a live mount would touch the wrong inode); it prints a summary of what was cleared vs left pending. The guard directory is kept until nothing is pending.
- To clear a legacy flag while the storage is mounted: unmount it, choose **Recovery > Cleanup guards** again (or `chattr -i <mountpoint>`), then remount.
- If you deleted the guard directory (`<BASE_DIR>/guards`, or `/var/lib/proxsave/guards` from an older version) manually and a mountpoint is still read-only, ProxSave has no record left to clear: check `lsattr -d <mountpoint>` and run `chattr -i <mountpoint>` while the storage is unmounted.

### 7. Service Management Fail-Fast

**Service Stop**: if a PVE cluster service fails to stop, the restore aborts. PBS is softer: if a PBS service will not stop you are asked `Continue restore with PBS services still running? (y/N)` and the restore proceeds if you say yes

**Why?**:
- Prevents partial corruption
- Better to fail safely than corrupt database
- User can investigate and retry

### 8. Comprehensive Logging

**Detailed Logs**: one file per extraction pass, `<BASE_DIR>/restore/YYYYMMDD_HHMMSS/restore_<extraction YYYYMMDD_HHMMSS>_<seq>.log`, next to the restore session log `restore-<host>-<timestamp>.log`. The directory is named when the restore starts and each file when its pass starts, so the two timestamps differ; `<seq>` numbers the extraction passes of the run in order, from `_1`.

**Contents**:
```text
=== PROXMOX RESTORE LOG ===
Date: 2025-11-20 14:34:09
Mode: CUSTOM selection
Selected categories: 2 categories
  - PVE Cluster Configuration (pve_cluster)
  - PVE Storage Configuration (storage_pve)
Archive: pve01-backup-20251119-020000.tar.xz

=== FILES RESTORED ===
RESTORED: ./var/lib/pve-cluster/config.db
RESTORED: ./etc/vzdump.conf
...

=== FILES SKIPPED ===
SKIPPED: ./opt/some-file (does not match any selected category)
...

=== SUMMARY ===
Total files extracted: 47
Total files skipped: 1203
Total files failed: 0
Total files in archive: 1250
```

**Usage**:
```bash
# Review what was restored
cat /opt/proxsave/restore/20251120_143052/restore_20251120_143409_1.log

# Search for specific file
grep "storage.cfg" /opt/proxsave/restore/20251120_143052/restore_20251120_143409_1.log

# Check for failures: the detailed log has the count, the session log names each file
grep "Total files failed" /opt/proxsave/restore/20251120_143052/restore_20251120_143409_1.log
grep -E "Failed to extract|Refusing hardlink" /opt/proxsave/restore/20251120_143052/restore-*.log
```

### 9. Checksum Verification

**SHA256 Verification**:
- The archive is verified **before** decryption, against the staged encrypted artifact, using its `.sha256` sidecar or the manifest checksum
- A checksum is also computed after decryption, but only to record it in the manifest copy; it is not compared against anything
- A backup with neither a `.sha256` nor a manifest checksum is **dropped during discovery**, so it never appears in the selectable list. If a backup you expect is missing, check that its checksum file is still next to it

**Behavior**:
```text
Verifying SHA256 checksum...
Expected: a1b2c3...
Actual:   a1b2c3...
Yes Checksum verified successfully.
```

### 10. Deferred Service Restart

**Go defer pattern** ensures services restart even if restore fails:

```text
Services stopped -> Defer restart scheduled -> Restore -> (Failure) -> Deferred restart executes
```

**Prevents**: System left with services stopped after failed restore

### 11. Kernel Command Line Merge (`boot` category)

See [Restore IOMMU, VFIO and passthrough settings](#restore-iommu-vfio-and-passthrough-settings) for the procedure, merge rules and bootloader limits.

## Troubleshooting

### General Issues

**Issue: "restore to / requires root privileges"**

**Cause**: Not running as root

**Solution**: become root first, then open the dashboard and pick **Tools > Restore**:
```bash
# Dashboard action: Tools > Restore
# Or
su -
# Dashboard action: Tools > Restore
```

Root privileges are also required for direct restore entry; see [CLI reference](CLI_REFERENCE.md).

---

**Issue: "Failed to create safety backup"**

**Cause**: Insufficient disk space on the filesystem holding `<BASE_DIR>/restore/`, or no write access to it (the restore runs as root)

**Solution**:
```bash
# Check available space on the filesystem holding BASE_DIR (/opt/proxsave by default)
df -h /opt/proxsave

# Safety backups of earlier restores, kept until you remove them
ls -la /opt/proxsave/restore/
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

The paths can also be set from the dashboard, **Maintenance > Install > Edit install**,
which re-runs the interactive setup over the existing `backup.env`.

---

### Decryption Issues

**Issue: "Provided key or passphrase does not match this archive."**

**Cause**: The secret was accepted as valid input but does not open this archive. This
is the catch-all failure: everything that does **not** start with `AGE-SECRET-KEY-` is
treated as a passphrase, so a mistyped passphrase, the passphrase of a different
install, and a **file path** typed into the field all land here.

**Solution**: the prompt loops, so just answer again with the right secret; `0` exits.
The single field takes **either** form:

- An AGE secret key, the `AGE-SECRET-KEY-...` string itself
- The passphrase the recipients were derived from

There is no identity-file option in the restore or decrypt flow: the field takes the key
material or the passphrase, never a path to a file holding it. `AGE_RECIPIENT_FILE`
(default `${BASE_DIR}/identity/age/recipient.txt`, so
`/opt/proxsave/identity/age/recipient.txt` with stock paths) holds the **public
recipients** used to encrypt; it cannot decrypt anything and is not what this prompt is
asking for.

In the TUI the field is titled `Decrypt key` and reads "Provide the AGE secret key or
passphrase used for `<backup name>`. Enter 0 to exit." Under `--cli` it is the single
line `Enter decryption key or passphrase for <backup name> (0 = exit):`.

If the secret is genuinely lost, the backup cannot be decrypted. Creating a new key
(dashboard **Maintenance > New key**, or **Maintenance > New key** in the dashboard) only changes what future
backups are encrypted with.

---

**Issue: "Invalid key or passphrase."**

**Cause**: The input begins with `AGE-SECRET-KEY-` but could not be parsed as an AGE
secret key: truncated, mistyped, or carrying stray characters from a copy and paste.
This message is specific to that case; a wrong passphrase reports the mismatch above
instead.

**Solution**: re-paste the whole key on a single line, with no line break and no quotes
around it. Surrounding whitespace is trimmed for you, and the key is upper-cased before
parsing, so a key pasted in lowercase is fine.

---

### Service Issues

**Issue: a PVE service unit is not installed**

**Behavior**: A cluster RECOVERY skips, both when stopping and when restarting, any of `pve-ha-lrm`, `pve-ha-crm`, `pve-cluster`, `pvedaemon`, `pveproxy`, `pvestatd` whose unit is not installed (`systemctl show -p LoadState --value <unit>` prints `not-found`). The skip is logged only at debug level, so the restore does not stop on it.

**Cause**: Not a PVE system, or the package providing the unit is not installed (for example `pve-ha-manager` for the HA services)

**Solution** (when the unit should be there):
```bash
# This is normal on PBS systems
# Or check if PVE installed
dpkg -l | grep proxmox-ve

# If truly PVE but service missing, repair
apt install --reinstall pve-cluster
```

---

**Issue: "Services failed to restart after restore"**

Inspect `systemctl status pve-cluster` and `journalctl -u pve-cluster` for the actual
failure. For a database rollback, follow the
[manual rollback prerequisites](#manual-rollback-prerequisites).

**Issue: "/etc/pve not mounting after restore"**

Check the mount and service state before attempting a repair. A database error and
a still-mounted filesystem are different problems. Do not force-unmount pmxcfs or
extract a safety archive onto `/` while the database is in use. The same
[rollback prerequisites](#manual-rollback-prerequisites) apply.

---

### Cluster Issues

**Issue: "Cluster shows wrong nodes after restore"**

Inspect the recovered membership and identify the authoritative cluster. Remove
obsolete members only from a surviving node after the old machines and workloads
are accounted for. Follow [Cluster Recovery](CLUSTER_RECOVERY.md); do not edit node
lists and restart pmxcfs without the HA and isolation prerequisites.

**Issue: "Lost quorum after restore"**

A recovered multi-node database can still expect its former peers. Check membership,
Corosync links and qdevice state. Do not lower votes to match the nodes currently
visible during a partition. Use the [Lost Quorum procedure](CLUSTER_RECOVERY.md#issue-lost-quorum)
to select the appropriate recovery path.

**Issue: "Hostname mismatch in cluster config"**

See [Hostname Changed](CLUSTER_RECOVERY.md#scenario-5-hostname-changed). Prepare the
final hostname before joining, or select the reviewed SAFE apply route; renaming a
live member and copying its node directory is not a generic recovery procedure.

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
# - ProxSave runs filesystem mount restore (Smart `/etc/fstab` merge) before applying PBS datastore configuration.
# - Datastore definitions are applied even if the underlying storage is offline/not mounted (PBS will show them as unavailable),
#   so you do not lose datastore entries after a restore.
# - If a datastore path looks like a mount-root location (e.g. under `/mnt`) but currently resolves to the root filesystem,
#   ProxSave applies a temporary read-only **bind-mount guard** on the mount root to prevent writes to `/` until the storage becomes available.
#   If the bind mount cannot be created, ProxSave logs a warning and proceeds unguarded (no persistent flag is set).
#   A bind-mount guard is shadowed when the real storage mounts on top (and is cleared by a reboot or --cleanup-guards).
#   Older versions set a chattr +i fallback that persisted across reboots; --cleanup-guards still clears any such legacy flags (or clear manually with chattr -i while unmounted).
# - If the datastore path is not empty and contains unexpected files/directories (not a PBS datastore), ProxSave will defer that datastore block
#   and save it under `/opt/proxsave/restore/<timestamp>/datastore.cfg.deferred.*` for manual review.
# - ProxSave does not format disks or import ZFS pools: mount/import the underlying storage first, then restart PBS.
ls -ld /mnt/datastore /mnt/datastore/<DatastoreName> 2>/dev/null
namei -l /mnt/datastore/<DatastoreName> 2>/dev/null || true

# If you need to remove ProxSave mount guards (optional / troubleshooting, run as root):
# Dashboard action: Recovery > Cleanup guards

# Common fix (adjust to your datastore path)
chown backup:backup /mnt/datastore && chmod 750 /mnt/datastore
chown -R backup:backup /mnt/datastore/<DatastoreName> && chmod 750 /mnt/datastore/<DatastoreName>
```

---

**Issue: "Bad Request (400) unable to read /etc/resolv.conf (No such file or directory)"**

**Cause**: `/etc/resolv.conf` is missing or a broken symlink. This can happen after a restore if a previous backup contained an invalid symlink (e.g. pointing to `../var/lib/proxsave-info/commands/system/resolv_conf.txt` or legacy `../commands/resolv_conf.txt`), or if the target system uses `systemd-resolved` and the expected `/run/systemd/resolve/*` files are not present.

**Solution**:
```bash
ls -la /etc/resolv.conf
readlink /etc/resolv.conf 2>/dev/null || true

# If the link is broken or points to a proxsave diagnostics file, replace it:
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
# ProxSave will attempt to auto-normalize datastore.cfg during restore and keep a copy of the original
# in /opt/proxsave/restore/<timestamp>/ (datastore.cfg.pre-normalize.*),
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

**Issue: "Bad Request (400) parsing /etc/proxmox-backup/datastore.cfg ... duplicate property 'gc-schedule'"**

**Cause**: `datastore.cfg` is malformed (multiple datastore definitions merged into a single block). This typically happens if the file lost its structure (header/order/indentation), leading PBS to interpret keys like `gc-schedule`, `notification-mode`, or `path` as duplicated **within the same datastore**.

**Restore behavior**:
- ProxSave detects this condition during staged apply.
- If `var/lib/proxsave-info/commands/pbs/pbs_datastore_inventory.json` is available in the backup, ProxSave will use its embedded snapshot of the original `datastore.cfg` to recover a valid configuration.
- Inventory entries that came only from `PBS_DATASTORE_PATH` scan roots are treated as diagnostic context and are excluded from regenerated `datastore.cfg`.
- If recovery is not possible, ProxSave will **leave the existing** `/etc/proxmox-backup/datastore.cfg` unchanged to avoid breaking PBS.

**Manual diagnosis**:
```bash
nl -ba /etc/proxmox-backup/datastore.cfg | sed -n '1,120p'

# Look for duplicate keys inside the same datastore block:
awk '
/^datastore: /{ds=$2; delete seen}
/^[[:space:]]*[A-Za-z0-9-]+[[:space:]]+/{key=$1; if(seen[key]++) printf "DUP datastore=%s key=%s line=%d: %s\n", ds, key, NR, $0}
' /etc/proxmox-backup/datastore.cfg
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

A: Pure cross-role restore (no role overlap) is not recommended; however
ProxSave supports restores when roles overlap. PVE and PBS have different
role-specific configurations. ProxSave now evaluates compatibility by
**role overlap**:

- `pve` ↔ `pbs`: only common categories are sensible
- `dual` -> `pve`: PVE + Common can be restored
- `dual` -> `pbs`: PBS + Common can be restored
- `pve` or `pbs` -> `dual`: the matching role + Common can be restored

When overlap exists, ProxSave continues with warnings and automatically filters
the selected categories to the roles supported by the current host.

---

**Q: Can I automate restore operations?**

A: No. The restore workflow is intentionally interactive to prevent accidental data loss. All selections require user input and confirmation. There is no flag, environment variable or config key that answers the prompts for you.

`--restore` exists so a host that cannot open the dashboard can still be restored by hand, not so a script can restore it. Backups are the automated half of ProxSave (the resident daemon, or cron); restores are not.

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
- **Dual-role to single-role** restores are allowed, but only matching role
  categories plus Common are applied
- **Hostname** should match or be updated manually
- **Network configuration** may need adjustment
- **Storage paths** may need adjustment
- **Cluster membership** must be handled manually

---

**Q: What if I cancel during restore?**

A: Depends on when:
- **Before final confirmation**: No restore payload has been applied
- **After service preparation or during extraction**: Cleanup attempts to restore service state; files may already have changed
- **After extraction**: Later applies may still be pending or partial; inspect the session log before deciding what to recover

Use Ctrl+C carefully - wait for current file to finish.

---

### Safety & Recovery

**Q: How do I rollback a failed restore?**

A: Start with the exact safety archive and log from that restore session. Its scope
is the files captured before the selected restore; it is not a full host snapshot
or a reversal of every API operation.

#### Manual rollback prerequisites

1. Use an out-of-band console and establish which node and backup are authoritative.
   For cluster database recovery, isolate obsolete peers and account for guests and
   shared storage before proceeding.
2. Inspect the exact safety archive from the session's `Safety backup preserved at:`
   line. List its contents with `tar -tzf /exact/path/to/restore_backup_TIMESTAMP.tar.gz`.
   Do not combine archives with a wildcard or extract the whole tree onto `/`.
3. If restoring `config.db`, gracefully stop `pve-ha-lrm`, then `pve-ha-crm`, before
   `pve-cluster` and the other PVE services. Verify successful shutdown and that
   `/etc/pve` is unmounted. Do not kill the HA LRM to force progress: that can trigger
   watchdog fencing. Stop if these conditions cannot be established.
4. Restore only the reviewed files needed for this failure. For `config.db`, follow
   the [official pmxcfs recovery procedure](https://pve.proxmox.com/pve-docs/pmxcfs-plain.html),
   including database mode `0600`, the correct hostname and `/etc/hosts`. ProxSave's
   normal RECOVERY workflow manages the service sequence when using a complete
   ProxSave backup; a safety tarball alone is not that complete backup format.
5. Restart `pve-cluster` and verify the mounted configuration, then restore the API
   services. Bring HA back only after the cluster and workload state are safe.
   Review networking, firewall and access-control changes separately using the
   session's logs and their own rollback files.

For normal recovery from a complete backup, prefer **Tools > Restore** in the dashboard and the
applicable [cluster scenario](CLUSTER_RECOVERY.md).

---

**Q: Can I test restore without affecting production?**

A: Yes, two approaches:

**Approach 1: Test on separate system**


**Approach 2: Decrypt-only mode**

The output is an uncompressed `.decrypted.bundle.tar` holding the inner archive and
sidecars. Unwrap it into a private inspection directory, then inspect the named
inner archive using its actual compression format. Follow
[Decrypting Backups](ENCRYPTION.md#decrypting-backups); the outer bundle does not
contain host paths directly. Decryption writes plaintext output but does not apply
host configuration.

Choose **Tools > Decrypt** in the dashboard to prepare a plaintext bundle for inspection:
```bash
# Decrypt without restoring
# Dashboard action: Tools > Decrypt

# Inspect the outer, uncompressed bundle at the path printed by ProxSave
tar -tf /path/to/backup.decrypted.bundle.tar
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

A: Follow [Migration to New Hardware](CLUSTER_RECOVERY.md#scenario-4-migration-to-new-hardware), including the standalone, isolated-member or clean-join decision before selecting SAFE or RECOVERY. VM disks and datastore content need their own recovery.

---

**Q: What if backup hostname doesn't match current hostname?**

A: Follow [Hostname Changed](CLUSTER_RECOVERY.md#scenario-5-hostname-changed). Never rename a live cluster member or edit corosync as a shortcut. On a fresh or isolated destination, SAFE can select an exported source node for guest configuration without moving live cluster ownership.

---

### Export-Only

**Q: Why can't I restore /etc/pve directly?**

A: `/etc/pve` is the filesystem interface to pmxcfs. Writes through the mounted
filesystem persist in `config.db` and replicate within the cluster. ProxSave blocks
blind archive extraction there because it can overwrite live cluster configuration.
Use confirmed SAFE applies for selected configuration groups, a reviewed manual
single-file change, or RECOVERY for whole-database replacement on an isolated host.

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

**Workaround**: use dashboard **Tools > Decrypt** or **Tools > Decrypt** in the dashboard, then
unwrap the resulting `.decrypted.bundle.tar` into a private directory. Select the
inner archive by its printed name and inspect or extract the desired entry there,
using the matching decompressor. See [Decrypting Backups](ENCRYPTION.md#decrypting-backups).
Review any extracted configuration before copying it to a live path.

---

**Q: Does restore preserve file permissions and ownership?**

A: Yes:
- **Extraction**: ProxSave preserves UID/GID, mode bits and timestamps (mtime/atime) for extracted entries.
- **Staged categories**: files are extracted under `/tmp/proxsave/restore-stage-*` and then applied to system paths using individual file replacement, not a transaction across categories or API updates; ProxSave explicitly applies mode bits (not left to `umask`) and preserves/derives ownership/group to match expected system defaults (important on PBS, where `proxmox-backup-proxy` runs as `backup`; ProxSave also repairs common `root:root` group regressions by inheriting the destination parent directory's group). On supported filesystems, staged writes also `fsync()` the temporary file and the destination directory to reduce the risk of incomplete writes after a crash/power loss.
- **ctime**: Cannot be set (kernel-managed).

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

   - In the restore and decrypt workflows (from the dashboard or from `--restore` /
     `--decrypt`, CLI or TUI), ProxSave will read the same
     `CLOUD_REMOTE` / `CLOUD_REMOTE_PATH` combination and show an entry:
       - `Cloud backups (rclone)`
   - When selected, the tool:
     - lists backup candidates on the remote with `rclone lsf` (`.bundle.tar` bundles and legacy `.metadata`+archive pairs);
     - reads the manifest/metadata via `rclone cat` (without downloading full archives; for bundles the manifest is at the beginning, so this is typically fast);
     - when you pick a backup, downloads it to `/tmp/proxsave` and proceeds with decrypt/restore.
   - Cloud scan applies `RCLONE_TIMEOUT_CONNECTION` per rclone command (the timer resets on each list/inspect step). If scanning times out (slow remote / huge directory), increase `RCLONE_TIMEOUT_CONNECTION` and retry. Also ensure the selected remote path points directly to the directory that contains the backups (scan is non-recursive).

2. **From a local rclone mount (restore-only)**  
   If you prefer to mount the rclone backend as a local filesystem:

   ```bash
   # Mount cloud storage locally
   rclone mount remote:bucket /mnt/cloud &

   # Configure in backup.env (restore-only scenario)
   CLOUD_ENABLED=false                      # cloud upload disabled
   # Use BACKUP_PATH / SECONDARY_PATH or browse the mount directly
   ```

   In this case you can:
   - copy the bundles from the mount (`/mnt/cloud/...`) into the local backup directory;
   - or provide the mounted path when the tool asks for the backup location
     (CLI) or browse the mounted directory before launching ProxSave.

---

**Q: What encryption is supported?**

A: AGE encryption only:
- **Passphrase-based**: Scrypt derivation (N=32768, r=8, p=1)
- **Key-based**: X25519 identity files

---

**Q: Where are temporary files stored?**

A: Temporary files are in `/tmp/proxsave/`:
- `proxmox-decrypt-*/` - Decryption workspace (deleted after restore)
- `restore-stage-*/` - Staged sensitive categories, in the clear (deleted when the restore ends, on success or failure)

What a restore keeps is in its own directory, `<BASE_DIR>/restore/TIMESTAMP/` (`/opt/proxsave/restore/TIMESTAMP/` by default, mode 0700, files 0600), which survives the reboot the restore recommends:
- `restore-<host>-<timestamp>.log` - Restore session log (preserved)
- `restore_TIMESTAMP_<seq>.log` - Detailed restore logs (preserved)
- `restore_backup_TIMESTAMP.tar.gz` - Safety backup (preserved)
- `network_rollback_backup_*`, `firewall_rollback_backup_*`, `ha_rollback_backup_*`, `pve_access_control_rollback_backup_*` - Rollback archives (preserved)
- `*_rollback_*.log` - Logs of the armed rollbacks (preserved; the rollback scripts and markers stay in `/tmp/proxsave/`)
- `nic_repair_*/` - Network files as restored, before a NIC name repair (preserved)
- `network_apply_*/` - Network apply diagnostics (preserved)
- `datastore.cfg.deferred.*` - PBS datastore definitions that were not applied (preserved)
- `datastore.cfg.pre-normalize.*` - PBS datastore.cfg as it was before ProxSave fixed its indentation (preserved)

**Cleanup**:
```bash
# Remove a restore's directory once that restore has settled
rm -r /opt/proxsave/restore/TIMESTAMP
```

---

<!-- site-region: restore-passthrough:start -->

## Restore IOMMU, VFIO and passthrough settings

Use this procedure after a reinstall or hardware change when recovering host passthrough configuration. Firmware IOMMU support, device IDs, PCI addresses, IOMMU groups and guest disk availability must be checked on the destination. ProxSave does not configure firmware or prove that an old passthrough assignment is safe on new hardware.

1. Keep console or IPMI access and a verified backup with the boot inventory and system service files. Leave affected guests stopped while reviewing device assignments.
2. Run `proxsave` without arguments as root and choose **Tools > Restore**. Select the backup and decrypt it if required.
3. Choose **Custom** and select `boot` and `services` when available. The services category also includes systemd, package, sysctl and firewall-related files; inspect the full plan before accepting it. Select `pve_config_export` if you need exported guest configuration for comparison. FULL includes export-only categories; STORAGE and SYSTEM BASE do not.
4. Review the plan and both overwrite confirmations. The boot merge keeps destination-specific root and memory settings as described below. `/etc/modprobe.d/` and `/etc/modules` are system-service files, so old driver bindings and blacklists still need review on different hardware.
5. Inspect the restore log for `Boot configuration -` messages and any rebuild warning. Compare exported boot files and guest device references with the destination. A skipped bootloader merge requires a manual edit following the destination's Proxmox bootloader procedure, not copying the old file wholesale.
6. Reboot only after checking the resulting settings and retaining recovery access. Verify the effective `/proc/cmdline`, IOMMU groups and device driver bindings before applying guest passthrough assignments and starting guests individually. Missing IOMMU groups, incorrect binding, a bootloader warning or an unavailable guest disk means recovery is incomplete.

A restore usually runs on a new machine, whose root device, pool name and ESPs differ from the backed-up host. The `boot` category therefore never writes the old host's boot files: GRUB settings (`/etc/default/grub`, `/etc/default/grub.d/`), `/etc/kernel/cmdline` and `/etc/kernel/proxmox-boot-uuids` are kept in the archive under `var/lib/proxsave-info/boot/` and only reach the export directory. None of these files is written to the live system, in any mode, including the full-restore fallback, not even when `CUSTOM_BACKUP_PATHS` names `/etc/default` or `/etc/kernel` and the archive also holds them at their natural paths: those copies go to the export directory with `proxsave_info`. The rest of `/etc/default` is restored by `services` as before.

**Source**: the backed-up host's effective kernel command line (`/proc/cmdline` at backup time), stored in `var/lib/proxsave-info/commands/system/kernel_cmdline.txt` by every backup.

**Merge rule**:
- Every parameter is carried except those that describe the backed-up host itself, and anything after `--` (arguments for init):
  - its root device and boot image: `root=`, `boot=`, `ro`, `rw`, `BOOT_IMAGE=`, `initrd=`;
  - how its root was mounted and where it resumed from: `rootflags=`, `rootfstype=`, `resume=`, `resume_offset=`. A `rootflags=` or `rootfstype=` of another filesystem makes this host's root mount fail, and the boot stops in the initramfs shell;
  - what was sized on its RAM or laid out on its memory map: `zfs.zfs_arc_max=` and `zfs.zfs_arc_min=` (the restore keeps this host's ARC limit), `hugepages=`, `hugepagesz=`, `default_hugepagesz=`, `sysctl.vm.nr_hugepages=`, `hugetlb_cma=`, `cma=`, `kernelcore=`, `movablecore=`, `mem=` and `memmap=` (a `memmap=` of another machine's firmware map can stop the boot);
  - `crashkernel=`: the kdump reservation. On GRUB hosts `kdump-tools` writes it through its own drop-in, sized for this host's RAM; without `kdump-tools` it only takes memory.
- A parameter this host already has is not added again. When both hosts set the same parameter with different values, this host's value stays (the kernel treats `-` and `_` in parameter names as the same character, so `vfio-pci.ids` and `vfio_pci.ids` are one parameter).

**Where the parameters are written** (Proxmox VE admin guide, *Host Bootloader*, *Editing the Kernel Commandline*):

| This host | File written | Rebuild |
|-----------|--------------|---------|
| GRUB, no `/etc/kernel/proxmox-boot-uuids`; `/etc/default/grub`, `/boot/grub/grub.cfg` and `update-grub` present | the value of `GRUB_CMDLINE_LINUX_DEFAULT` in `/etc/default/grub`, the rest of the file untouched | `update-initramfs -u -k all`, then `update-grub` |
| `proxmox-boot-tool status` reports every ESP as `uefi` (systemd-boot); `/etc/kernel/cmdline` is one line with `root=` | `/etc/kernel/cmdline` | `update-initramfs -u -k all`, then `proxmox-boot-tool refresh` |
| `proxmox-boot-tool status` reports every ESP as `grub` | the value of `GRUB_CMDLINE_LINUX_DEFAULT` in `/etc/default/grub` | `update-initramfs -u -k all`, then `proxmox-boot-tool refresh` |

Anything else is not recognized with certainty, and nothing is written: the ESPs disagree, `proxmox-boot-tool status` fails, `GRUB_CMDLINE_LINUX_DEFAULT` is not one plain assignment of a literal value, or a file in `/etc/default/grub.d/` sets it too. The restore logs a warning with the reason and the parameters the backup carries.

**Rebuild**: runs once, at the end of the restore, when the merge changed a file or the restore wrote under `/etc/modprobe.d`, `/etc/modules`, `/etc/hostid` or `/etc/zfs`, which the initramfs copies. On a bootloader not recognized with certainty only `update-initramfs` runs, and the bootloader is left as it is. A failed command is a warning with the command and its error; the next command still runs and the restore goes on.

**Safety backup**: the pre-restore `/etc/default/grub` and `/etc/kernel/cmdline` are in the safety backup, like every other file the restore may write.

**Log**: every step is in the restore log, on lines starting with `Boot configuration -`: the backed-up command line, the bootloader found, the parameters added (or not carried because this host sets them differently), and each command run.

For VM/CT configuration application, use [VM/CT Configuration Restore](#vmct-configuration-restore); for failures, use [diagnose failures](TROUBLESHOOTING.md#diagnose-backup-and-restore-failures). CLI entry options are in [CLI reference](CLI_REFERENCE.md).

<!-- site-region: restore-passthrough:end -->

## Additional Resources

**Related Documentation**:
- [DASHBOARD.md](DASHBOARD.md) - The menu restore is normally started from
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md) - Technical architecture and internals
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md) - Visual workflow diagrams
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md) - Advanced cluster disaster recovery
- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Every flag, including the ones used here
- [README.md](../README.md) - Main project documentation

**Proxmox Documentation**:
- [Proxmox VE Cluster Manager](https://pve.proxmox.com/wiki/Cluster_Manager)
- [Proxmox Backup Server Documentation](https://pbs.proxmox.com/docs/)

**Support**:
- Project Issues: [GitHub Issues](https://github.com/tis24dev/proxsave/issues)
- Proxmox Forum: [forum.proxmox.com](https://forum.proxmox.com/)

---

## Summary

The restore workflow provides a **safe, interactive, and flexible** system for recovering Proxmox configurations:

- **Category-based** granular control
- **4 restore modes** for common scenarios
- **Safety backups** before any changes
- **Cluster-aware** service management
- **Export-only protection** for sensitive paths
- **Comprehensive logging** for audit trails
- **Multiple abort points** for user control

**Remember**:
- Start from the dashboard: `proxsave` with no arguments, then **Tools > Restore**
- Keep `--restore` for the host that cannot open it: headless, rescue shell, no TTY
- Always verify backups before disaster strikes
- Test restore procedures on non-production systems
- Isolate cluster nodes before cluster database restore
- Keep safety backups until restore is fully verified
- Review exported /etc/pve files manually

**Most Important**: Read and understand this guide BEFORE you need to restore!
