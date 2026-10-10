# Practical examples

These scenarios combine the task guides into a host configuration backup policy. Start with [your first backup](BACKUP_GUIDE.md#run-and-verify-your-first-backup) if you have not yet verified a run

Open the dashboard with `proxsave` on an interactive terminal. Use **Maintenance > Install > Edit install** for settings exposed by its form; edit other settings in the active configuration file according to the [configuration reference](CONFIGURATION.md). The values below are examples, not installation defaults

## Start with the dashboard

Choose **Backup** to run the configured backup, **Tools > Restore** to recover settings, and **Daemon > Status** to inspect scheduling. Review [the dashboard guide](DASHBOARD.md) for the actual menu and terminal requirements. Automation and alternate configuration files are covered separately in the [CLI reference](CLI_REFERENCE.md)

## Overview

| Scenario | Purpose |
| --- | --- |
| [Multiple destinations](#example-8-complete-production-setup) | Combine local/NAS/cloud copies, encryption and result monitoring |
| [Alternate system root](#example-9-test-in-a-chrootfixture) | Collect a mounted test tree on an isolated disposable host |
| [Host backup appliance](#ha-lxc-backup-appliance-host_backup_mode) | Collect a host filesystem mounted into an LXC |
| [Dual-role host](#example-10-dual-pvepbs-host) | Protect a host with PVE and PBS installed together |
| [Scheduled monitoring](#example-11-resident-daemon-and-healthchecks-monitoring) | Combine recurring backups with missing-run detection |

## Example 8: Complete Production Setup

This example combines multiple destinations, AGE encryption and retention. Adapt it to the host and test every destination; enabling more features does not by itself prove that a backup is recoverable

### Prerequisites for the combined policy

- A working ProxSave installation and a verified initial backup
- A secondary filesystem mounted at the intended NAS path before the run
- An rclone remote configured for the account that runs ProxSave
- AGE recovery material stored independently of this host
- Enough destination capacity for the proposed retention policy

Configure the basic destination and encryption choices in **Maintenance > Install > Edit install**. Create or configure recipients through **Maintenance > New key**, following the [encryption guide](ENCRYPTION.md). Do not rotate keys without retaining the identities needed for existing archives

The following configuration illustrates the additional policy values. Replace existing assignments in the active configuration; do not append a second set of competing keys or aliases

```ini
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxsave/backup
LOG_PATH=/opt/proxsave/log

SECONDARY_ENABLED=true
SECONDARY_PATH=/mnt/nas/proxsave/backup
SECONDARY_LOG_PATH=/mnt/nas/proxsave/log

CLOUD_ENABLED=true
CLOUD_REMOTE=s3
CLOUD_REMOTE_PATH=/company-backups/host1
CLOUD_LOG_PATH=/company-backups/logs/host1

COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
COMPRESSION_MODE=standard

RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=8
RETENTION_MONTHLY=12
RETENTION_YEARLY=3

ENCRYPT_ARCHIVE=true
BUNDLE_ASSOCIATED_FILES=true
AGE_RECIPIENT_FILE=/opt/proxsave/identity/age/recipient.txt
```

The NAS path must be the intended mounted filesystem, not an empty directory left behind by a missing mount. The `s3` name is an already configured rclone remote. Follow [cloud setup](CLOUD_STORAGE.md) rather than treating the name as credentials

The retention values describe a proposed policy, not a compliance guarantee. They cannot retain historical generations that have never been created. Review the [retention behavior](STORAGE.md#set-backup-retention), destination overrides and available capacity before reducing or replacing an existing policy

If you use a personal Telegram bot, explicitly select `BOT_TELEGRAM_TYPE=personal` together with its token and chat ID. Use the [notification guide](NOTIFICATIONS.md) to configure and test each chosen channel. Keep secrets out of shared configuration examples

For metrics, set `METRICS_ENABLED` and `METRICS_PATH` according to the [metrics reference](CONFIGURATION.md#metrics---prometheus). Writing metrics files does not configure a Prometheus scrape job or create a Grafana dashboard

### Check the combined result

1. Open **Daemon > Status** and confirm which scheduler owns the installation
2. Review **Diagnostic Checks > Post-install**, then the Telegram or Healthchecks checks for services you have configured
3. Run **Backup** and examine the collection summary and the result of every enabled destination
4. Confirm that copied archive sets can be accessed independently of this host, including any credentials needed to retrieve them
5. Test decryption and a compatible recovery procedure on a disposable host before relying on this policy

Native PBS can be added using [its dedicated procedure](STORAGE.md#create-native-pbs-backups). It stores a different format and has separate key and recovery requirements

## Example 9: Test in a Chroot/Fixture

`SYSTEM_ROOT_PREFIX` redirects collection to another root. It does not sandbox ProxSave or redirect logs, runtime state, output paths or retention. Use a separate disposable test host or isolated test installation, not a production host with a different input path

Prepare the alternate filesystem using the host's storage tools, then create a dedicated configuration from the installation's current configuration. Set the prefix in that file; it is not an environment override

```ini
SYSTEM_ROOT_PREFIX=/mnt/snapshot-root
BACKUP_ENABLED=true
BACKUP_PATH=/var/lib/proxsave-fixture/backup
LOG_PATH=/var/lib/proxsave-fixture/log
SECONDARY_ENABLED=false
SECONDARY_PATH=
SECONDARY_LOG_PATH=
CLOUD_ENABLED=false
PBS_TARGET_ENABLED=false
TELEGRAM_ENABLED=false
EMAIL_ENABLED=false
GOTIFY_ENABLED=false
WEBHOOK_ENABLED=false
METRICS_ENABLED=false
HEALTHCHECK_ENABLED=false
AUTO_UPDATE_HASHES=false
AUTO_FIX_PERMISSIONS=false
SET_BACKUP_PERMISSIONS=false
```

Keep outputs outside the mounted input tree. Check aliases and supported environment overrides against the [configuration reference](CONFIGURATION.md), and remove custom scripts or other test settings that could affect live services

Selecting this separate configuration is an advanced command-line task. Use the [alternate-configuration instructions](CLI_REFERENCE.md#separate-collection-profile-on-a-disposable-host) and inspect debug output to confirm that collection reads the intended root before executing a real backup. The dry run still writes diagnostic logs

Expected results are files from the mounted input and archives/logs under the test output paths. Temporary files and runtime state still belong to the executing installation. Restoring from such an archive is a separate operation with its own target and safety requirements

## HA-LXC Backup Appliance (`HOST_BACKUP_MODE`)

This scenario runs ProxSave in a privileged LXC with the host filesystem exposed beneath `/host`. Configure the container mounts in Proxmox first: the intended host root and the PVE configuration mount must be visible at the correct prefixed paths and mounted read-only. This is external container administration, not a ProxSave dashboard operation

Use these settings inside the container's ProxSave installation:

```ini
SYSTEM_ROOT_PREFIX=/host
HOST_BACKUP_MODE=true
BACKUP_ENABLED=true
```

Then open `proxsave` in the container's interactive terminal and choose **Backup**. Review the detected role and collection results before relying on the appliance

With a prefix and host-backup mode, detection uses the mounted host filesystem. Commands whose namespace or daemon context would describe the container instead of the host are skipped, and some information comes from files. Not all host mounts or runtime information are necessarily visible in the container

Keep archive destinations, logs and runtime files in deliberate container-accessible locations. Read-only input mounts protect those input paths; the prefix itself is not a general isolation boundary. Do not run Restore in the container expecting it to restore the mounted host automatically

## Example 10: Dual PVE+PBS Host

ProxSave detects hosts where both products are installed and can collect their configurations together. The backup has one shared Linux payload and role-specific PVE/PBS content. Existing configuration directories alone are not a reliable substitute for installed-role detection

Use **Maintenance > Install > Edit install** for the exposed settings and review the collector keys in the configuration reference. This example enables representative role-specific collectors:

```ini
BACKUP_VM_CONFIGS=true
BACKUP_CLUSTER_CONFIG=true
BACKUP_PVE_JOBS=true
BACKUP_PVE_REPLICATION=true
BACKUP_PVE_FIREWALL=true
BACKUP_DATASTORE_CONFIGS=true
BACKUP_REMOTE_CONFIGS=true
BACKUP_SYNC_JOBS=true
BACKUP_VERIFICATION_JOBS=true
BACKUP_PBS_NOTIFICATIONS=true
BACKUP_PBS_NODE_CONFIG=true
BACKUP_NETWORK_CONFIGS=true
BACKUP_CRON_JOBS=true
BACKUP_SYSTEMD_SERVICES=true
BACKUP_ZFS_CONFIG=true
```

Run **Backup** and inspect both role summaries. If one role fails collection, the other may still be saved with the incomplete target recorded. Both role collectors failing is an error. Do not treat the presence of one archive as proof that both roles completed

Metadata identifies a combined backup as `dual` with PVE and PBS targets. During restore, available categories depend on payload and target capabilities: a PVE-only target cannot apply PBS-only settings just because both roles were in the source archive

Optional datastore inventories remain metadata. Enabling `PXAR_SCAN_ENABLE` does not include PBS datastore contents in the configuration backup

## Example 11: Resident daemon and healthchecks monitoring

Use this combination when you need scheduled execution and an alert if backups fail or stop arriving

1. Open **Daemon > Status** and inspect the configured scheduler and running binary
2. If the host uses cron and you intend to switch, choose **Daemon > Install** and review the result. Follow the [daemon migration guidance](DAEMON.md) for existing wrapper scripts or other schedulers
3. Configure the schedule through the installation form and the documented settings. Use **Daemon > Restart** when configuration or binary changes require it
4. Configure monitoring using the [healthchecks guide](HEALTHCHECKS.md), then open **Diagnostic Checks > Healthchecks** to inspect it
5. Run **Backup**, confirm the result reaches the monitor, and verify the intended alert delivery

A received backup message and a working missing-run monitor are different checks. Avoid leaving an independent cron wrapper or external timer running alongside the daemon schedule

## Example 1: Basic Local Backup

The complete procedure now lives in [Run and verify your first backup](BACKUP_GUIDE.md#run-and-verify-your-first-backup)

## Example 2: Local + Secondary Storage

Use [backup destinations](STORAGE.md#choose-and-verify-backup-destinations) for local and pre-mounted secondary storage, including the checks needed before relying on a NAS copy

## Example 3: Cloud Backup with Google Drive

Use [cloud storage](CLOUD_STORAGE.md) to configure an rclone remote for the executing account, select its path and verify a copied archive set

## Example 4: Encrypted Backup with AGE

Use the [encryption guide](ENCRYPTION.md) to configure recipients, retain recovery material and test decryption

## Example 5: Backblaze B2 with Bandwidth Limiting

Provider setup and rclone tuning belong in [cloud storage](CLOUD_STORAGE.md). Review the provider's current limits separately; a concurrency setting does not imply multiple workers within one uploaded file

## Example 6: MinIO Self-Hosted with High Performance

Use [cloud storage](CLOUD_STORAGE.md) for an S3-compatible remote and tune it against observed transfer behavior. A self-hosted endpoint is not automatically faster, and one copy operation is not replication to several independent destinations

## Example 7: Multi-Notification Setup

Use the [notification guide](NOTIFICATIONS.md) for channels and delivery checks, and [healthchecks](HEALTHCHECKS.md) for missing-run monitoring

## Quick Comparison Matrix

| Choice | Read first |
| --- | --- |
| Local/NAS/cloud/PBS | [Storage](STORAGE.md#choose-and-verify-backup-destinations) |
| Simple or GFS retention | [Retention](STORAGE.md#set-backup-retention) |
| Archive encryption and recovery keys | [Encryption](ENCRYPTION.md) |
| Scheduler and monitoring | [Daemon](DAEMON.md) and [healthchecks](HEALTHCHECKS.md) |
| Recovery planning | [Restore](RESTORE_GUIDE.md#quick-start) |

## Customization Tips

Change one part of a working policy at a time, run a backup from the dashboard and inspect the result. Keep a known usable backup while testing. Performance and archive size depend on the files actually collected, compression, destination and host resources

## Related Documentation

Use the [documentation index](README.md) to find the canonical task guide or the [configuration reference](CONFIGURATION.md) for individual keys

## Next Steps

Verify a recovery on a disposable compatible system and keep the access credentials and recovery material available independently of the original host
