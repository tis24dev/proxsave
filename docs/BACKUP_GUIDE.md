# Host configuration backup

Use this guide to understand what ProxSave collects, run your first backup from the dashboard and check the result

- [What ProxSave backs up](#what-proxsave-backs-up)
- [Run and verify your first backup](#run-and-verify-your-first-backup)
- [Choose what to collect](#choose-what-to-collect)
- [How a backup runs](#how-a-backup-runs)

<!-- site-region: backup-scope:start -->

## What ProxSave backs up

ProxSave backs up the host configuration of Proxmox VE and Proxmox Backup Server. It collects configuration files and recovery information for restoring settings after a reinstall, host failure or hardware replacement

Collection runs on the installed host without requiring the host, VMs or containers to shut down. The collected files describe the system during the run; this is not a single atomic snapshot of every service. A restore is a separate operation that can change configuration and restart affected services

### Configuration coverage

| Area | Saved configuration and recovery information |
| --- | --- |
| Proxmox VE | VM and container configuration, storage definitions, jobs, permissions, firewall, HA, SDN, resource mappings and cluster configuration, including `/var/lib/pve-cluster/config.db` |
| Proxmox Backup Server | Datastore definitions, remotes, jobs, users, access rules, notifications, certificates and server settings |
| Linux host | Networking, mount configuration, storage stack, services, users, scheduled tasks, SSH material, kernel and boot settings |
| Additional files | Selected scripts, home-directory content and explicitly configured custom paths, subject to collection settings and exclusions |
| Recovery information | Inventories, command output and manifests that help inspect the original system |

Coverage depends on the detected host role, available files, enabled collectors and successful collection. On a host with both PVE and PBS installed, ProxSave can collect both roles in one archive with shared Linux configuration

VM disks, container filesystems and PBS datastore contents are outside the normal host-configuration backup. Protect workload data separately. A package inventory does not include installation packages, and an exported inventory is not necessarily something the restore workflow can apply

Custom paths and optional collection of existing PVE backup files can add files beyond the normal scope. They do not turn ProxSave into a VM backup scheduler or an image-based disk recovery tool

### Where backups can go

Archives can be kept locally, copied to mounted secondary storage such as a NAS, or sent through rclone. Native PBS integration stores a snapshot of the collected tree using a configured PVE PBS storage. Read [backup destinations](STORAGE.md#choose-and-verify-backup-destinations) to choose the appropriate format and recovery path

Start with [installation](INSTALL.md#fast-install), then [run and verify a backup](#run-and-verify-your-first-backup). For recovery, use the [restore guide](RESTORE_GUIDE.md#quick-start)

<!-- site-region: backup-scope:end -->

<!-- site-region: first-backup:start -->

## Run and verify your first backup

Before starting, complete installation, confirm the configured destinations are available and keep any decryption keys somewhere you can access after losing the host. A local archive on the same failed disk will not help recover that disk

### Start from the dashboard

1. Open an interactive terminal on the installed host and run `proxsave` without arguments
2. If release notes appear, read and acknowledge them to reach the dashboard
3. To review the installation settings, choose **Maintenance > Install > Edit install**. Use the [configuration reference](CONFIGURATION.md#editing-the-configuration-from-the-dashboard) for settings that the form does not expose
4. Choose **Diagnostic Checks > Post-install** to review the installation checks. These checks are useful preparation; they do not replace a real backup and recovery test
5. Choose **Backup** in the Backup group. The dashboard displays the run output and its final outcome

Opening the dashboard requires an interactive terminal. Bare `proxsave` starts a backup when those terminal checks fail, and passing command-line options also bypasses the dashboard. See [dashboard behavior](DASHBOARD.md#when-the-dashboard-opens) before using a pipe, a script or a remote session without a terminal

### Check the result

Read the collection summary and warnings before relying on the backup. Investigate missing files or an incomplete product role even when an archive was produced

Check the result for each destination you enabled. A successful local archive does not establish that the NAS, cloud copy or PBS snapshot succeeded. Confirm that the expected archive set or snapshot is accessible using the destination's own browser or management interface

Keep the archive and its associated metadata/checksum files together, or keep the complete bundle when bundling is enabled. Those files describe the backup and are used when discovering and verifying it for recovery. For encrypted backups, confirm that the required private key or passphrase recovery material is available independently of the backed-up host

Open **Daemon > Status** to inspect the active scheduler. If using healthchecks, open **Diagnostic Checks > Healthchecks** and verify the monitoring setup as described in the [monitoring guide](HEALTHCHECKS.md). Receiving a notification does not prove that a future missed backup will be detected

### Verify recovery separately

Before depending on the backup for disaster recovery, test the applicable recovery procedure on a disposable, compatible host. Follow the [restore guide](RESTORE_GUIDE.md#quick-start) and check the recovered settings there. Archive verification is not proof that every setting is suitable for different hardware or that workload data has been recovered

For problems, use [troubleshooting](TROUBLESHOOTING.md#common-issues). Keep diagnostic logs private until you have checked them for sensitive information

<!-- site-region: first-backup:end -->

<!-- site-region: backup-selection:start -->

## Choose what to collect

ProxSave chooses collectors for the detected PVE, PBS or combined host role, then collects common Linux configuration. Collection settings control optional parts of that process. Restore categories are a later selection from the files actually present in the backup

### Adjust the collection settings

Start in **Maintenance > Install > Edit install** for settings exposed by the configuration form. Other collection keys must be edited in the active `backup.env` file using the [configuration reference](CONFIGURATION.md#collector-options). Do not assume that every key has a dashboard control

Review related settings together. For example, disabling a dedicated credential or VM configuration collector does not guarantee that the same information is absent from a larger database or directory snapshot. PVE's `config.db` can still contain access-control secrets when the separate ACL files are excluded

Use encryption and appropriate access controls for archives that contain credentials. Read [encryption](ENCRYPTION.md#encrypt-backups-and-preserve-recovery-keys) before collecting material you do not want stored in plaintext at the destinations

### Include custom files

`CUSTOM_BACKUP_PATHS` adds explicitly selected files or directories. Paths are collected under their corresponding host-relative locations in the archive. Missing paths do not prove a successful copy, so inspect the collection log and the resulting content

Keep the list focused. Broad paths can collect large amounts of application data or secrets that are unrelated to host recovery. User homes and scripts also deserve review for sensitive material

Custom collection does not create a new automatic restore category. A custom file may match an existing category, but arbitrary application paths require separate inspection and a deliberate recovery procedure

### Exclusions and optional inventories

Use the documented blacklist and `BACKUP_EXCLUDE_PATTERNS` behavior to exclude unwanted files. Review the resulting backup instead of assuming that one exclusion removes every equivalent representation of a setting. The [collection exclusions](CONFIGURATION.md#collection-exclusions) and [custom paths](CONFIGURATION.md#custom-paths--blacklist) reference describes their scope

PBS datastore and PVE backup-file inventories provide recovery information. They are distinct from collecting live VM disks or PBS datastore payloads. Enable optional scans only when their results are useful and check their collection time on the actual host

### Verify a changed policy

After changing the collection policy, run **Backup** from the dashboard and inspect the collection warnings, output size and destination results. Retain a known usable backup while validating the new policy. Use a disposable recovery target to check that the settings you need can actually be recovered

<!-- site-region: backup-selection:end -->

<!-- site-region: backup-workflow:start -->

## How a backup runs

The dashboard and scheduled runs use the same backup workflow. Understanding its stages helps distinguish a collection problem from an archive, destination or notification problem

1. **Load and check the configuration.** ProxSave resolves its configuration, detects the host roles and performs the applicable environment, permission, dependency and destination checks
2. **Collect files and recovery information.** Role-specific collectors and common Linux collectors write into a temporary working tree. Some collectors record warnings and continue, so the summary matters as much as the presence of an archive
3. **Prepare the archive.** Configured optimizations run on the collected tree. ProxSave creates the archive with the selected compression and, when configured, AGE encryption
4. **Verify and finalize the archive set.** The stored archive is checked and its associated metadata is written. A failed archive creation is not treated as a completed archive
5. **Handle archive destinations.** Local retention and configured secondary/cloud copies are processed, with destination-specific results
6. **Handle native PBS.** When enabled, the PBS destination sends the collected tree through `proxmox-backup-client` and checks the resulting snapshot. This is a separate format from the AGE archive
7. **Report the run.** ProxSave records outcomes and attempts the enabled notifications. A notification failure does not undo an already saved backup; notification delivery can still extend the run time

### What the results mean

An archive can contain useful configuration while some requested files were unavailable. A combined PVE/PBS run can preserve one role when collection of the other fails, with the incomplete target recorded. Review these warnings rather than treating every produced archive as complete

Temporary collection includes plaintext, even when the final archive is encrypted. Encryption of the archive or PBS snapshot does not encrypt the host's collection workspace. See [encryption and staging](ENCRYPTION.md#plaintext-staging)

The resident daemon adds scheduling, supervision and health reporting around the backup process. Direct notification delivery and detection of a backup that never started are separate concerns. Use the [daemon guide](DAEMON.md) and [healthchecks guide](HEALTHCHECKS.md) to configure both

### Recovery uses the saved format

For local, secondary and rclone archive sets, choose **Tools > Restore** in the dashboard and follow the [restore workflow](RESTORE_GUIDE.md#quick-start). Native PBS snapshots require the PBS recovery path described in [native PBS backups](STORAGE.md#create-native-pbs-backups). Neither workflow restores VM disk contents that were never part of the backup

<!-- site-region: backup-workflow:end -->

## Implementation references

For contributors, [collector architecture](COLLECTOR_ARCHITECTURE.md) explains recipes and combined-role collection. The backup pipeline lives in [backup_run_phases.go](../internal/orchestrator/backup_run_phases.go), and [block_adapter.go](../internal/orchestrator/block_adapter.go) passes the collected tree to native destination blocks
