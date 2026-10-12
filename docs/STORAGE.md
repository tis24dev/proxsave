# Backup destinations, retention and native PBS

<!-- site-region: backup-destinations:start -->

## Choose and verify backup destinations

ProxSave collects host configuration into a working tree. Primary, secondary and rclone
storage receive an archive of that tree, normally with its metadata and checksum in a
single `.bundle.tar`. Native PBS receives the tree as a `proxsave.pxar` snapshot instead.
These are configuration backups, not a replacement for VM, container or PBS datastore
payload backups. See [backup scope](BACKUP_GUIDE.md#what-proxsave-backs-up).

| Destination | What you configure | Recovery source |
| --- | --- | --- |
| Primary filesystem | `BACKUP_PATH` | Primary archive source in Restore or Decrypt |
| Secondary filesystem or mounted NAS | `SECONDARY_ENABLED`, `SECONDARY_PATH` | Secondary archive source |
| rclone remote | `CLOUD_ENABLED`, remote and path | Cloud archive source |
| Native PBS | PVE PBS storage definition and `PBS_TARGET_STORAGE` | External PBS client or PBS interface |

### Configure primary and secondary storage

Run `proxsave` without arguments on an interactive terminal, then choose
**Maintenance** > **Install** > **Edit install**. Review the installation paths and
enable `Secondary storage` if needed. Fill in `Secondary backup path` and optionally
`Secondary log path`. The backup path is required when enabled; the log path can be
empty. Other storage and retention settings are hand edits of `configs/backup.env`.

The primary archive directory normally resolves to `${BASE_DIR}/backup` through
`BACKUP_PATH`. The primary copy provides a convenient local recovery source, but a
copy on the protected host alone cannot survive loss of that host or disk.

A secondary path is an absolute local filesystem path. For NAS storage, mount the
NFS or SMB share outside ProxSave before configuring it. Check the mount itself,
read/write access under the backup account and persistence after reboot. Creating a
directory at the intended mount point does not establish that the NAS is mounted.
Use rclone if you need remote access without a filesystem mount; see
[cloud backups](CLOUD_STORAGE.md#set-up-and-recover-cloud-backups).

```bash
# Example: inspect an existing NAS mount, using your actual path.
findmnt --target /mnt/nas-backup
```

Do not place secondary backups inside the primary backup directory. Ensure paths have
private permissions and enough space. The disk thresholds are
`MIN_DISK_SPACE_PRIMARY_GB`, `MIN_DISK_SPACE_SECONDARY_GB`,
`MIN_DISK_SPACE_CLOUD_GB` and `MIN_DISK_SPACE_PBS_GB`. They are checks, not a reservation
of space for the run. Refer to [configuration](CONFIGURATION.md) for path validation,
timeouts, permission repair and required-space checks.

### Prove a destination works

1. Choose **Backup** in the dashboard and inspect the initialization and storage
   results for every enabled destination. An enabled destination that could not
   initialize is not proof of a saved copy.
2. Confirm the named bundle exists on primary and secondary storage. With
   `BUNDLE_ASSOCIATED_FILES=false`, keep the archive and matching `.metadata`,
   `.sha256` and `.manifest.json` sidecars together.
3. Choose **Tools** > **Decrypt** for an encrypted archive inspection test, or
   **Tools** > **Restore** to select the saved archive and review the restore plan.
   Do not apply configuration to a production host merely to check that a backup is
   listed. Use a suitable isolated recovery host for a full restore test.
4. Keep remote credentials and decryption material independently accessible. A
   credential stored only inside the backup it unlocks cannot bootstrap recovery.

For retention, continue with [retention policies](#set-backup-retention). For native
PBS, use the [dedicated procedure](#create-native-pbs-backups); its snapshot is not an
archive listed by the current Restore or Decrypt source selector.
Command-line equivalents belong in [CLI_REFERENCE.md](CLI_REFERENCE.md).

<!-- site-region: backup-destinations:end -->

<!-- site-region: backup-retention:start -->

## Set backup retention

Retention deletes old backups after a usable copy has been saved to that destination.
Plan it against the number and age of recovery points you need, not just free space.
Changes apply to subsequent runs; reducing a limit can delete older recovery points.
Keep an independent copy before tightening a policy you have not tested.

There is no dedicated retention form in the dashboard. Edit the active
`configs/backup.env`, then use **Backup** to run the new policy and inspect the retention
result. For daemon scheduling changes, use **Daemon** > **Status** and **Restart** as
needed; see [scheduling](DAEMON.md#schedule-and-supervise-backups).

### Keep the newest backups

`RETENTION_POLICY=simple` keeps a count of newest eligible backups at each destination.
The `MAX_*_BACKUPS` values are counts, not days, even though older aliases and internal
field names use the word `days`. Two backups in one day consume two slots.

```bash
RETENTION_POLICY=simple
MAX_LOCAL_BACKUPS=15
MAX_SECONDARY_BACKUPS=15
MAX_CLOUD_BACKUPS=15
MAX_PBS_TARGET_BACKUPS=15
```

These values are an example matching the current shipped template. The compiled
fallbacks when keys are absent are 7 local, 14 secondary, 30 cloud and 15 native PBS.
For simple retention, `0` disables deletion for that destination. Logs follow the
backup retention policy; cloud deletion batching is controlled separately by
`CLOUD_BATCH_SIZE` and `CLOUD_BATCH_PAUSE`, not upload-worker settings.

### Spread recovery points over time

`RETENTION_POLICY=gfs` uses shared daily, weekly, monthly and yearly tiers across
enabled destinations. A recovery point selected for more than one tier is kept once.
The effective daily tier is at least one, including when `RETENTION_DAILY` is empty
or `0`; GFS cannot be disabled by setting every tier to zero.

```bash
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=12
RETENTION_YEARLY=3
```

Daily points cover the recent window. Weekly points represent past ISO weeks,
monthly points past months and yearly points past years. This is a distribution
policy, not a promise that a fixed number of files will always be present: missing
runs cannot create missing recovery points. Do not add tier counts and interpret the
sum as a guaranteed minimum.

### Verify retention and understand skipped entries

Choose **Backup**, inspect the reported policy, kept tiers and deletion result, then
list the remaining recovery points at each destination. Verify a retained old point
is recoverable as well as the newest point.

Archive retention scopes ownership using backup identity and available metadata.
Unverifiable entries without a completion manifest/checksum, or without a reliable
timestamp, are left untouched and do not consume keep slots. Such entries can explain
why more files remain than the configured limit. Never interpret a filename alone as
proof that it is safe to delete another host's backup.

Native PBS retention applies to this host's `host/proxsave-<hostname>` group in the
configured namespace. Protected snapshots remain protected. A successful upload with
failed pruning is a saved backup with a retention warning; it does not reclaim space.
Pruning removes snapshot references. PBS garbage collection and datastore maintenance
remain separate PBS administration tasks, so pruning does not guarantee immediate disk
space recovery. Avoid competing PBS server prune jobs unless their policy is deliberate.

See the [configuration key reference](CONFIGURATION.md) for supported values and the
[native PBS procedure](#create-native-pbs-backups) for pruning permissions.

<!-- site-region: backup-retention:end -->

<!-- site-region: native-pbs-backups:start -->

## Create native PBS backups

Native PBS storage sends the collected configuration tree directly to Proxmox Backup
Server as `proxsave.pxar`, under `host/proxsave-<hostname>`. It does not upload the AGE
archive or its `.bundle.tar`. Primary, secondary and cloud archive copies can remain
enabled alongside it and use their own recovery workflow.

### Prepare PBS and the PVE storage

This destination requires a Proxmox VE host with a resolved hostname and
`proxmox-backup-client` available in `PATH`. A standalone PBS host is not an eligible
sender for this destination. ProxSave can still back up a PBS host's configuration
through its ordinary archive destinations.

Before enabling the destination:

1. On PBS create or select the datastore and, if used, create the namespace. The
   namespace must already exist; ProxSave checks access and does not create it.
2. Set up an account or API token with access to the datastore and namespace for
   listing, backup and recovery. Retention additionally needs `Datastore.Prune` or
   `Datastore.Modify`. If token privilege separation is enabled, check the token's
   effective permissions as well as the user account's permissions.
3. In the Proxmox VE interface add or verify a storage of type **Proxmox Backup Server**.
   Record its storage ID, server, port if nondefault, datastore, namespace, username
   or token ID, and certificate fingerprint. Confirm the storage is enabled.
4. Configure its credentials and encryption through PVE. ProxSave reads the existing
   storage definition from `/etc/pve/storage.cfg` and credentials from
   `/etc/pve/priv/storage/<storage-id>.pw`; it has no independent PBS credential form.
5. Preserve credentials, the chosen repository/namespace and any encryption key
   outside the node. Test that this independent recovery material is readable.

ProxSave's dashboard has no native PBS setup form. Edit the active `configs/backup.env`:

```bash
PBS_TARGET_ENABLED=true
PBS_TARGET_STORAGE=pbs-config-backups
MAX_PBS_TARGET_BACKUPS=15
```

Replace `pbs-config-backups` with the actual PVE storage ID, not a datastore name,
filesystem path or rclone remote. Review `MIN_DISK_SPACE_PBS_GB` and the shared
[retention policy](#set-backup-retention). The current template disables this destination
until explicitly configured.

### Check the actual encryption key

The storage's existing `/etc/pve/priv/storage/<storage-id>.enc` controls native PBS
encryption. When it exists, ProxSave checks it and supplies it as `--keyfile`; it also
supplies `<storage-id>.master.pem` when that master public key exists. A key that requires a passphrase or whose fingerprint does not match the
storage definition prevents initialization when the client can inspect it. If key
inspection itself fails or returns unreadable output, the key is still passed to the
upload; that upload may fail, but it never silently switches to plaintext. Native
uploads run with closed stdin, so interactive key passphrases are unsupported.

Without the `.enc` file, ProxSave explicitly uses `--crypt-mode none`, even if
`ENCRYPT_ARCHIVE=true` and AGE recipients are configured. Conversely an existing PBS
key is used regardless of the AGE archive toggle. **AGE private identities and AGE
passphrases do not decrypt PBS snapshots.** Retain the PBS encryption key independently;
a master public key alone is not recovery material. If you rely on master-key recovery,
keep the corresponding private master key and validate that recovery separately.

### Run and verify the snapshot

1. Run `proxsave` without arguments and choose **Backup**.
2. Check `PBS storage: initialized` and the datastore access result. A wrong storage
   type, disabled definition, missing password, invalid key, inaccessible datastore
   or missing namespace prevents initialization. An enabled uninitialized block
   remains visible and later reports that its backup was not saved.
3. In the storage result confirm the exact snapshot name and `PBS Storage: backup saved`.
   ProxSave lists the uploaded snapshot and checks its reported encryption mode before
   treating it as usable. Review exclusion-rule warnings: `.pxarexclude` files in the
   collected tree affect what the PBS client includes.
4. In PBS verify the snapshot in the intended namespace and group, inspect
   `proxsave.pxar`, and review the attached run log. Review retention separately: a
   pruning permission error can leave a usable snapshot with a warning.
5. Test recovery to a private directory on another suitable host before relying on
   this destination as the only surviving recovery copy.

Snapshots use 64 KiB chunks. Client versions supporting it use metadata change
detection; older clients omit that option. PBS chunk reuse belongs to this native
format; archive destinations do not gain PBS chunk deduplication by storing a bundle.

### Recover a native PBS snapshot

The current dashboard **Tools** > **Restore** and **Tools** > **Decrypt** select primary,
secondary or rclone archives. They do not browse native PBS snapshots or convert a
`pxar` snapshot into a ProxSave restore bundle. Native recovery therefore uses PBS
outside ProxSave.

Prepare a recovery host with `proxmox-backup-client`, independently recovered access
credentials and, for encrypted snapshots, the original PBS key or a tested master-key
recovery path. Use the same repository and namespace as the backup. Confirm the TLS
fingerprint against a trusted record before providing credentials.

This example runs in a root shell. Replace the repository, namespace and exact
snapshot timestamp with values from your independent record and PBS listing. Keep
credentials out of command arguments and shell history:

```bash
umask 077
export PBS_REPOSITORY='backup-user@pbs!token@pbs.example:datastore'
read -r -s -p 'PBS password or token secret: ' PBS_PASSWORD
printf '\n'
export PBS_PASSWORD
export PBS_FINGERPRINT='TRUSTED-CERTIFICATE-FINGERPRINT'
proxmox-backup-client snapshot list --repository "$PBS_REPOSITORY" --ns config-backups
install -d -m 700 /root/proxsave-pbs-recovery
proxmox-backup-client restore \
  'host/proxsave-node.example/2026-10-10T02:00:00Z' proxsave.pxar \
  /root/proxsave-pbs-recovery \
  --repository "$PBS_REPOSITORY" --ns config-backups \
  --keyfile /root/recovered-pbs-storage.enc
unset PBS_PASSWORD
```

Omit `--ns` for the root namespace. Omit `--keyfile` only for a snapshot known to be
unencrypted. Ensure the destination is an empty private inspection directory; do not
use `/` as an extraction destination. Inspect the recovered tree and ensure expected
configuration files are present. PBS recovery authentication and key handling are
external client tasks; check the installed client's help if its options differ.

Do not copy the entire tree onto a live host. Applying recovered cluster, network,
boot or storage configuration needs the same target compatibility and service safety
planning described in the [restore guide](RESTORE_GUIDE.md#restore-modes) and
[cluster recovery guide](CLUSTER_RECOVERY.md). Keep an archive destination enabled
when you need ProxSave's category-based restore workflow. A tested native snapshot
recovery and a tested archive restore prove different recovery paths.

<!-- site-region: native-pbs-backups:end -->
