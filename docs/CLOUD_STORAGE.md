# Cloud Storage with rclone

<!-- site-region: cloud-backups:start -->

## Set up and recover cloud backups

Cloud storage copies ProxSave's configuration archives through rclone. It is an
additional recovery destination, not a VM or container backup service. Keep remote
credentials and AGE recovery material independently of the protected node. A credential
copy inside the same remote cannot be the only way to regain access to that remote.

### Prepare the remote

Install rclone on the backup host and configure it under the account that runs
ProxSave, normally root. Provider account creation, authentication and any provider
cold-storage retrieval happen outside the dashboard.

```bash
rclone version
rclone config
rclone listremotes
rclone config file
```

The configuration wizard creates a named remote such as `gdrive` or `s3backup`.
Use your provider's storage type, permissions and authentication requirements. Keep
the resulting configuration private, normally mode `0600`, and check its parent
directory permissions. See [provider examples](#configuring-rclone) and
[credential protection](#securing-configuration) for the detailed reference.

Verify access to the intended directory, substituting your actual remote and path:

```bash
rclone lsf 'gdrive:proxsave/node1/backup'
```

A successful listing proves read/list access, not upload and deletion access. A
restricted token may need a write test instead; see [restricted token issues](#restricted-api-token-issues).
Use a dedicated prefix for each host. Retention scans recursively and limits deletion
by backup ownership, but separate prefixes also keep listings and recovery choices clear.

### Connect ProxSave to the remote

Run `proxsave` without arguments on an interactive terminal and choose
**Maintenance** > **Install** > **Edit install**. Enable `Cloud backups (rclone)`,
then fill `Rclone backup remote` and `Rclone log remote`.
The wizard requires both text fields while enabled. It does not configure rclone
itself. Turning the toggle off blanks both keys.

Advanced paths, retention and upload settings require a hand edit of the active
`configs/backup.env`. A straightforward example is:

```bash
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=proxsave/node1/backup
CLOUD_LOG_PATH=proxsave/node1/log
```

`CLOUD_REMOTE` identifies the remote name. `CLOUD_REMOTE_PATH` supplies the path inside
it. The shorthand `CLOUD_REMOTE=gdrive:proxsave/node1/backup` also works with an empty
path setting. A base path in `CLOUD_REMOTE` and another in `CLOUD_REMOTE_PATH` are
combined, so review both before running. `CLOUD_LOG_PATH` can be a path on the same
remote or an explicit `otherremote:path`; blanking it by hand disables cloud log upload.
A blank log path is valid at runtime even though the wizard requires one.

See [remote path formats](#recommended-remote-path-formats-important) for examples and
the [configuration reference](#configuration-reference) for all supported tuning keys.
Choose [retention](STORAGE.md#set-backup-retention) deliberately before storing backups.

### Verify a saved backup

Choose **Backup** in the dashboard. Check remote initialization, upload and verification
results. A dry run checks accessibility and skips storage dispatch; it cannot prove
that an archive was uploaded. Automation and dry-run flags belong in
[CLI_REFERENCE.md](CLI_REFERENCE.md).

List the remote after the run and confirm the exact named artifact exists. With default
bundling there is one `.bundle.tar` per backup. With bundling disabled retain the
archive and its matching metadata, manifest and checksum files as a complete set.
If log upload is configured, check its separate path too.

Choose **Tools** > **Decrypt** for an encrypted archive inspection test or **Tools** >
**Restore** to select the cloud source and review a recovery plan. A successful listing
or upload is not proof that your key can decrypt the backup. Test a full restore on an
appropriate isolated recovery host.

Upload verification may compare size only when the backend exposes no native SHA256.
`CLOUD_VERIFY_DOWNLOAD=true` enables download-and-hash verification in that case and
uses additional bandwidth. Upload workers and rclone transfer settings do not make
one `copyto` object upload into multiple independent file jobs. Review the
[performance reference](#performance-tuning) before changing concurrency or rate limits.

### Recover after losing the original host

1. Install ProxSave and rclone on the replacement host using the
   [installation guide](INSTALL.md#fast-install).
2. Recover the independent rclone configuration or authenticate again through
   `rclone config`. Restore private permissions and verify remote access.
3. Configure the correct remote and backup path, then choose **Tools** > **Restore**
   and select the cloud source. Select the exact complete backup, not a log or sidecar.
4. Verify and decrypt it, choose categories and review the target-specific restore
   plan. Follow [restore safety and verification](RESTORE_GUIDE.md#restore-modes).

If you need an external manual download, use a private directory and the exact bundle
name from the listing:

```bash
umask 077
RECOVERY_DIR=$(mktemp -d /root/proxsave-recovery.XXXXXX)
rclone copyto 'gdrive:proxsave/node1/backup/EXACT-BACKUP.bundle.tar' "$RECOVERY_DIR/backup.bundle.tar"
```

Make the downloaded copy available through the configured primary or secondary archive
path before selecting it in the dashboard. Do not copy a fully extracted configuration
tree over `/`; that bypasses restore safeguards. Provider archive tiers that require
rehydration must be made readable through the provider before this workflow can fetch them.

### If cloud storage fails

Check the remote name, combined path, account used by ProxSave, token permissions,
provider quota, connectivity and operation timeout. Distinguish failure to upload from
failure to verify or rotate an already saved copy. Keep the successful local copy.
The [troubleshooting reference](#troubleshooting) lists specific errors and token tests.
For diagnostic backup logging see [troubleshooting](TROUBLESHOOTING.md#diagnose-backup-and-restore-failures):
the debug invocation starts a backup and bypasses the dashboard.

<!-- site-region: cloud-backups:end -->

## Implementation appendix

## Architecture

Proxsave uses a **3-tier storage system**:

```text
┌─────────────────────────────────────────────────────────────┐
│                    BACKUP ORCHESTRATOR                      │
└─────────────────────────────────────────────────────────────┘
                            │
            ┌───────────────┼───────────────┐
            │               │               │
            ▼               ▼               ▼
┌───────────────────┐ ┌───────────────┐ ┌─────────────────┐
│  LOCAL STORAGE    │ │   SECONDARY   │ │ CLOUD STORAGE   │
│   (Primary)       │ │   STORAGE     │ │   (rclone)      │
├───────────────────┤ ├───────────────┤ ├─────────────────┤
│ Critical: YES     │ │ Critical: NO  │ │ Critical: NO    │
│ Required: YES     │ │ Optional: YES │ │ Optional: YES   │
│ Failure: ABORT    │ │ Failure: WARN │ │ Failure: WARN   │
└───────────────────┘ └───────────────┘ └─────────────────┘
         │                    │                   │
         ▼                    ▼                   ▼
  /opt/backup/         /mnt/secondary/      gdrive:backups/
```

**Design principle**: Cloud storage is **NON-CRITICAL**. Upload failures log warnings but don't abort the backup.

### Execution Flow

```text
1. Create backup locally           Critical (must succeed)
   └─> BACKUP_PATH/

2. Copy to secondary storage       ○ Optional (warn on failure)
   └─> SECONDARY_PATH/

3. Upload to cloud (rclone)        ○ Optional (warn on failure)
   ├─> One bundle per backup by default, so one upload
   ├─> Verification: size + SHA256 (size-only fallback if backend lacks native hash)
   └─> Retry: 3 attempts with backoff

4. Apply retention policies        Per-tier retention
   ├─> Local: MAX_LOCAL_BACKUPS or GFS
   ├─> Secondary: MAX_SECONDARY_BACKUPS or GFS
   └─> Cloud: MAX_CLOUD_BACKUPS or GFS
```

### Cloud layout (bundle vs raw)

By default (`BUNDLE_ASSOCIATED_FILES=true`) each backup is packed into a single
`<archive>.bundle.tar` before upload, so the cloud remote receives **one file per
backup**. The bundle contains the raw archive plus its `.metadata` and `.sha256`
sidecars (and `.metadata.sha256` when present); the `.manifest.json` is **not** inside
the bundle.

Set `BUNDLE_ASSOCIATED_FILES=false` to upload the **raw** archive plus separate
sidecars: `<archive>`, `<archive>.sha256`, `<archive>.manifest.json`,
`<archive>.metadata`, and `<archive>.metadata.sha256` (missing ones are skipped). In
this raw layout the `<archive>.manifest.json` is the **authoritative metadata** that
keeps the backup discoverable and verifiable during restore/decrypt cloud scans, so do
not delete it (PS-BH-002).

### How ProxSave invokes rclone

ProxSave never runs a free-form rclone command line. Every call is built from a fixed
**subcommand allowlist**: `copyto`, `delete`, `deletefile`, `hashsum`, `ls`, `lsf`,
`lsl`, `mkdir`, `touch` (anything else is rejected). Uploads use **`copyto`** (not
`copy`), with `--bwlimit` and `--transfers` when set. Progress flags are deliberately
omitted: these runs are headless, so there is no TTY to draw on. Connectivity checks
use `lsf` (plus `mkdir`, or `touch` + `deletefile` for the write test) and add
`--max-depth 1`; the log count adds `--files-only`; verification uses `lsl`/`ls` plus
`hashsum sha256`, with `--download` when `CLOUD_VERIFY_DOWNLOAD=true`. Timeouts are
enforced with Go context deadlines rather than rclone flags, so ProxSave does not add
`--config`, `--timeout`, or `--contimeout`. `RCLONE_FLAGS`, if set, is appended to
every one of these commands (see the Configuration Reference).

This describes the **backup** path. The restore and decrypt cloud scan builds its
rclone calls separately: it uses `cat` to read manifests and `copyto --progress` to
download, neither of which goes through the allowlist, and **none of them receive
`RCLONE_FLAGS`**. If your remote needs a flag to work at all, a custom `--config` path
or a provider option, backups will succeed and restores will not. Put such settings in
the rclone remote definition itself, not in `RCLONE_FLAGS`. The manual `rclone`
commands shown later in this guide are for you to run by hand and are not subject to
this allowlist.

---



## Detailed reference

The task above is the operator procedure. The following material preserves detailed behavior, limits and implementation context.

## Supported Cloud Providers

| Provider | rclone Type | Use Case | Free Tier |
|----------|-------------|----------|-----------|
| **Google Drive** | `drive` | Small/medium businesses, easy OAuth | 15GB |
| **Amazon S3** | `s3` | Enterprise, scalable, highly available | Account-dependent; see [AWS Free Tier](https://aws.amazon.com/free/) |
| **Backblaze B2** | `b2` | Cost-effective archival | See [B2 pricing and allowances](https://www.backblaze.com/cloud-storage/pricing) |
| **Microsoft OneDrive** | `onedrive` | Microsoft 365 integration | 5GB |
| **Dropbox** | `dropbox` | Simple, limited free space | 2GB |
| **MinIO** | `s3` | Self-hosted S3-compatible | Unlimited (self-hosted) |
| **Wasabi** | `s3` | S3-compatible, no egress fees | None |
| **Cloudflare R2** | `s3` | Zero egress fees, S3-compatible | 10GB |
| **SFTP/FTP** | `sftp`/`ftp` | Generic remote server | N/A |

**Note**: Any of the 40+ rclone backends are supported. See [rclone.org](https://rclone.org/) for complete list.

---

## Configuring rclone

### Interactive Configuration

```text
# Launch interactive wizard
rclone config

# Create new remote
n                          # New remote
<remote-name>              # e.g., "gdrive", "s3backup"
<storage-type>             # e.g., "drive", "s3", "b2"
# ... follow provider-specific prompts ...
y                          # Confirm
q                          # Quit
```

### Example 1: Google Drive

```bash
rclone config

n                          # New remote
gdrive                     # Remote name
drive                      # Storage type (Google Drive)
                          # Client ID (press enter for default)
                          # Client Secret (press enter for default)
1                          # Scope: Full access
                          # Root folder ID (press enter)
                          # Service account (press enter for no)
n                          # Advanced config? No
y                          # Auto config (opens browser for OAuth)
# [Authorize in browser]
y                          # Confirm
q                          # Quit
```

**Google Drive notes**:
- Uses OAuth2 (requires browser for first auth)
- API limit: ~1000 requests per 100 seconds
- Tuning: `CLOUD_BATCH_SIZE=10`, `CLOUD_BATCH_PAUSE=2`

**Test remote**:
```bash
rclone mkdir gdrive:pbs-backups
echo "test" > /tmp/test.txt
rclone copy /tmp/test.txt gdrive:pbs-backups/
rclone ls gdrive:pbs-backups/
# Should show: test.txt
rclone deletefile gdrive:pbs-backups/test.txt
```

### Example 2: Amazon S3

```bash
rclone config

n                          # New remote
s3backup                   # Remote name
s3                         # Storage type
1                          # Provider: AWS
1                          # Credentials: IAM
# Or enter manually:
# AKIAIOSFODNN7EXAMPLE      # Access key ID
# wJalrXUtn...EXAMPLEKEY    # Secret access key
eu-central-1               # Region
                          # Endpoint (default AWS)
                          # Location constraint (auto)
                          # ACL (default)
n                          # Advanced config? No
y                          # Confirm
q                          # Quit
```

**S3 notes**:
- Requires Access Key ID + Secret Access Key
- Choose region close to your location
- High reliability (99.999999999% durability)
- Consider S3 Standard (hot data) or Glacier (cold storage)

### Example 3: MinIO (Self-hosted)

```bash
rclone config

n                          # New remote
minio                      # Remote name
s3                         # Storage type (S3 compatible)
5                          # Provider: Minio
false                      # Get credentials from runtime? No
minioadmin                 # Access key (default MinIO)
minioadmin                 # Secret key (default MinIO)
                          # Region (empty for MinIO)
https://minio.example.com  # Endpoint
                          # Location constraint (empty)
n                          # Advanced config? No
y                          # Confirm
q                          # Quit
```

**MinIO notes**:
- S3-compatible, self-hosted
- Full control over data and costs
- Requires MinIO server setup
- Use HTTPS for security

### Example 4: Backblaze B2

```bash
rclone config

n                          # New remote
b2                         # Remote name
b2                         # Storage type
001234567890abcdef         # Account ID
K001abcdefghijklmnopqrs    # Application Key
                          # Hard delete? No (default)
n                          # Advanced config? No
y                          # Confirm
q                          # Quit
```

**B2 notes**:
- Check [current B2 pricing](https://www.backblaze.com/cloud-storage/pricing) for storage charges and allowances
- Download allowances depend on the plan and stored volume; see [transaction and egress pricing](https://www.backblaze.com/cloud-storage/transaction-pricing)
- Lower API rate limit than S3
- Ideal for long-term archival

### Verify Configuration

`rclone config show` prints sensitive configuration, including credentials or tokens.
Use [config redacted](https://rclone.org/commands/rclone_config_redacted/) for diagnostics
and review its output before sharing it.

```bash
# List configured remotes
rclone listremotes
# Output: gdrive:, s3backup:, minio:, b2:

# Show a redacted configuration (rclone 1.64 or newer)
rclone config redacted gdrive

# Test connectivity
rclone lsf gdrive:
# Empty output or directory list = working
# Error = configuration issue
```

---

## Securing Configuration

### File Permissions

```bash
# Check config file location
rclone config file
# Output: /root/.config/rclone/rclone.conf

# Set secure permissions
chmod 600 ~/.config/rclone/rclone.conf
chown root:root ~/.config/rclone/rclone.conf
```

### Backup rclone Configuration

**IMPORTANT**: The rclone configuration file contains credentials. Include it in your backups:

```bash
# Add to backup.env
CUSTOM_BACKUP_PATHS="
/root/.config/rclone/rclone.conf
/opt/proxsave/configs/backup.env
"
```

This ensures rclone config is backed up with your system, enabling disaster recovery.

---

## Performance Tuning

Two things to know before you tune.

Every upload is a single-file `rclone copyto`, including raw sidecars. Increasing
`RCLONE_TRANSFERS` does not create concurrent file transfers inside that invocation.
With the default bundle layout there is one backup object. With
`BUNDLE_ASSOCIATED_FILES=false`, `CLOUD_UPLOAD_MODE` and `CLOUD_PARALLEL_MAX_JOBS`
control ProxSave workers that launch separate uploads. For a large object, check
`RCLONE_BANDWIDTH_LIMIT` and the backend's supported chunk and concurrency options
in the rclone remote definition. The examples below are starting settings, not
measured performance guarantees.

Integer keys are parsed strictly and fall back to their default without a warning if
parsing fails, so write a number: `RCLONE_TRANSFERS=16`, never `8-16`. A range silently
becomes the default.

### By Network Type

#### Fast Network (Fiber, LAN, Datacenter)

```bash
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=4
RCLONE_TRANSFERS=8
RCLONE_BANDWIDTH_LIMIT=
RCLONE_TIMEOUT_OPERATION=300
```

#### Slow Network (ADSL, 4G, Satellite)

```bash
CLOUD_UPLOAD_MODE=sequential
CLOUD_PARALLEL_MAX_JOBS=1
RCLONE_TRANSFERS=2
RCLONE_BANDWIDTH_LIMIT=2M
RCLONE_TIMEOUT_OPERATION=1800
RCLONE_RETRIES=5
```

#### Shared Network (Office, Multi-tenant)

```bash
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=2
RCLONE_TRANSFERS=4
RCLONE_BANDWIDTH_LIMIT=5M
RCLONE_TIMEOUT_OPERATION=600
```

### By Cloud Provider

#### Google Drive

```bash
RCLONE_TIMEOUT_CONNECTION=60
RCLONE_TRANSFERS=4
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=2
```

**Characteristics**:
- API limit: ~1000 requests/100s
- Requires OAuth2 authentication
- Good for small/medium deployments

#### Amazon S3 / Wasabi

```bash
RCLONE_TIMEOUT_CONNECTION=30
RCLONE_TRANSFERS=16
CLOUD_BATCH_SIZE=100
CLOUD_BATCH_PAUSE=1
```

**Characteristics**:
- High API limits
- Excellent scalability
- Low latency

#### Backblaze B2

```bash
RCLONE_TIMEOUT_CONNECTION=45
RCLONE_TRANSFERS=4
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=2
```

**Characteristics**:
- Lower API limits than S3
- Cost-effective for archival
- 10GB free tier

#### MinIO (Self-hosted LAN)

> A LAN remote still needs the host to have outbound internet. Every run does a network
> preflight (it dials `1.1.1.1:443`, then `8.8.8.8:53`) and, if neither answers, disables
> cloud storage for that run and logs a warning naming the reason, however reachable
> your MinIO box is. On an air-gapped or egress-filtered host set
> `DISABLE_NETWORK_PREFLIGHT=true`; see [CONFIGURATION.md](CONFIGURATION.md).

```bash
RCLONE_TIMEOUT_CONNECTION=10
RCLONE_TRANSFERS=8
CLOUD_BATCH_SIZE=100
CLOUD_BATCH_PAUSE=0
```

**Characteristics**:
- No API limits
- Full LAN speed
- Self-hosted control

### Upload Mode Comparison

| Mode | Use Case | Pros | Cons |
|------|----------|------|------|
| **Parallel** | Fast networks, high-capacity providers | Faster uploads, better throughput | More RAM, higher API usage |
| **Sequential** | Slow networks, rate-limited APIs | Lower memory, API-friendly | Slower total time |

**Default**: `parallel` with `CLOUD_PARALLEL_MAX_JOBS=2` (balanced)

---

## Troubleshooting

### Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| `rclone not found in PATH` | Not installed | `curl https://rclone.org/install.sh \| sudo bash` |
| `couldn't find configuration section 'gdrive'` | Remote not configured | `rclone config` to create remote |
| `401 unauthorized` | Credentials expired | `rclone config reconnect gdrive` or regenerate keys |
| `connection timeout (30s)` | Slow network | Increase `RCLONE_TIMEOUT_CONNECTION=60` |
| `Timed out while scanning ... (rclone)` | Slow remote / huge directory | Increase `RCLONE_TIMEOUT_CONNECTION` and ensure the remote path points to the directory that contains the backups (scan is non-recursive) |
| `operation timeout (300s exceeded)` | Large file + slow network | Increase `RCLONE_TIMEOUT_OPERATION=900` |
| `429 Too Many Requests` | API rate limiting | Reduce ProxSave upload workers for raw files; for retention, reduce `CLOUD_BATCH_SIZE` and increase `CLOUD_BATCH_PAUSE` |
| `directory not found` | Path doesn't exist | `rclone mkdir gdrive:pbs-backups` |
| `403 Forbidden` | Insufficient permissions | Check bucket/remote ACL/IAM |
| `507 Insufficient Storage` | Quota exceeded | Reduce retention, increase quota, or change provider |

### Restricted API Token Issues

**Error**: `directory not found` or `403 forbidden` during connectivity check

**Cause**: API token lacks list/about permissions. Common with:
- **Cloudflare R2** restricted tokens
- **S3-compatible providers** with minimal permissions
- **Backblaze B2**, **Wasabi** write-only tokens

**Solution**:
```bash
# Use write test instead of list test
nano configs/backup.env
CLOUD_WRITE_HEALTHCHECK=true
```

This creates a temporary test file (`.pbs-backup-healthcheck-<timestamp>`) and deletes it, requiring only write/delete permissions instead of list operations.

**Alternative**: Grant list permissions to your API token if possible.

### Debug Procedures

#### Enable Debug Logging

`proxsave --log-level debug` starts a backup with diagnostic logging. Because it has an argument, it bypasses the dashboard; it does not open a debug dashboard. It may create and upload a real backup.

```bash
# Run with debug level
proxsave --log-level debug

# Or set in config
nano configs/backup.env
DEBUG_LEVEL=extreme

# Logs include:
# - Detailed command execution
# - rclone stdout/stderr
# - File operations
# - Retry attempts
```

#### Verify Configuration Loading

```bash
# Check parsed configuration
grep -E "^CLOUD_|^RCLONE_" /opt/proxsave/configs/backup.env

# Dry-run diagnostic invocations are documented in CLI_REFERENCE.md.
```

#### Analyze Log Files

```bash
# Find latest log
ls -lt /opt/proxsave/log/

# View log
cat /opt/proxsave/log/backup-$(hostname)-*.log

# Filter errors
grep -i "error\|fail\|warning" /opt/proxsave/log/backup-*.log

# Filter cloud issues
grep -i "cloud.*error\|cloud.*fail\|cloud.*warning" /opt/proxsave/log/backup-*.log
```

---

## FAQ

**Q: Can I use multiple cloud providers?**
A: ProxSave supports one `CLOUD_REMOTE`. An rclone union combines backends, but its default create policy chooses one upstream; it does not create an independent copy on every provider. Replication needs an explicit policy and a review of read, delete and failure behavior. See [rclone union policies](https://rclone.org/union/).

**Q: Can I use a network address like "192.168.0.10/folder" for SECONDARY_PATH?**
A: **No**. `SECONDARY_PATH` and `BACKUP_PATH` require **absolute local filesystem paths**. For network shares, mount them first using NFS/CIFS/SMB, then use the local mount point path (e.g., `/mnt/nas-backup`).

If you want to use a direct network address without mounting, configure it as `CLOUD_REMOTE` using rclone with an S3-compatible backend (like MinIO) or appropriate protocol.

Example comparison:
- Invalid: `SECONDARY_PATH=192.168.0.10:/backup`
- Invalid: `SECONDARY_PATH=//server/share`
- Mounted filesystem: Mount first: `sudo mount 192.168.0.10:/backup /mnt/backup`, then `SECONDARY_PATH=/mnt/backup`
- rclone alternative: Use `CLOUD_REMOTE=minio` with `CLOUD_REMOTE_PATH=/backup` (requires rclone configuration for MinIO/S3 on LAN)

**Q: Do cloud logs consume too much space?**
A: Logs follow backup retention automatically. To disable cloud log upload: `CLOUD_LOG_PATH=""` (empty).

**Q: Does cloud upload slow down backups?**
A: Yes. The local backup completes first (critical), but the upload runs inside the same run, so it delays completion. There is no upload-only mode and no way to hand the upload to a separate job: the run owns it. On a host scheduled by the resident daemon this matters twice over, because the upload counts against `MAX_RUN_DURATION` (default `1h`), and a run that overruns it is killed and reported as a hang rather than finishing slowly. Size that key for the slowest upload you expect, and remember that `RCLONE_BANDWIDTH_LIMIT` makes the run longer, not shorter.

**Q: Can I backup directly to cloud only (no local)?**
A: No, local storage is mandatory (critical). Cloud is always secondary/tertiary. Philosophy: fast local backup to slow cloud archival.

**Q: How much RAM does rclone use?**
A: Memory use depends on backend buffers, chunk sizes and the number of ProxSave upload workers. Each worker invokes a single-file `copyto`; multiplying a per-file estimate by `RCLONE_TRANSFERS` does not describe this path. Measure a representative run and reduce `CLOUD_PARALLEL_MAX_JOBS` for raw uploads or backend buffering when needed.

**Q: Can I test upload without creating backup?**
A: Yes, use existing file:
```bash
rclone copy /opt/proxsave/backup/existing-backup.tar.xz gdrive:pbs-backups/ --dry-run
# Remove --dry-run for real upload
```

**Q: Cloudflare R2 / Backblaze B2 / restricted API token - connectivity check fails?**
A: Set `CLOUD_WRITE_HEALTHCHECK=true` in `configs/backup.env`. This uses write test instead of list operations, compatible with minimal API token permissions (write/delete only).

---

## Quick Reference

### Common rclone Commands

```bash
# List remotes
rclone listremotes

# Show redacted remote config; review before sharing
rclone config redacted gdrive

# List files (long format)
rclone lsl gdrive:pbs-backups/

# List files (short format)
rclone lsf gdrive:pbs-backups/

# Check quota
rclone about gdrive:

# Copy local to remote
rclone copy /local/file.txt gdrive:pbs-backups/

# Copy remote to local
rclone copy gdrive:pbs-backups/file.txt /local/

# Sync (WARNING: deletes non-matching files)
rclone sync /local/dir/ gdrive:pbs-backups/

# Create directory
rclone mkdir gdrive:pbs-backups/subdir

# Delete file
rclone deletefile gdrive:pbs-backups/file.txt

# Delete directory (recursive)
rclone purge gdrive:pbs-backups/old/

# Verify integrity
rclone check /local/dir/ gdrive:pbs-backups/ --checksum
```

### Environment Variables Quick List

```bash
# Essential
CLOUD_ENABLED=true
CLOUD_REMOTE=GoogleDrive
CLOUD_REMOTE_PATH=/proxsave/backup
MAX_CLOUD_BACKUPS=30

# Upload tuning
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=2
RCLONE_TRANSFERS=4
RCLONE_BANDWIDTH_LIMIT=5M

# Timeouts
RCLONE_TIMEOUT_CONNECTION=30
RCLONE_TIMEOUT_OPERATION=300

# Retry & batch
RCLONE_RETRIES=3
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=1
```

---

**For official rclone documentation**, see: https://rclone.org/

### Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `CLOUD_ENABLED` | `false` | Enable cloud storage |
| `CLOUD_REMOTE` | _(empty)_ | rclone remote **name** from `rclone config` (legacy `remote:path` still supported), or an absolute local directory such as a mounted share (`/mnt/cloud`). **Required** when `CLOUD_ENABLED=true`: leaving it empty is a hard configuration error, and the run aborts with exit `2` before anything is backed up, locally included. Set both keys together, or neither. |
| `CLOUD_REMOTE_PATH` | _(empty)_ | Folder path/prefix inside the remote (e.g., `/proxsave/backup`) |
| `CLOUD_LOG_PATH` | _(empty)_ | Optional log folder (recommended: path-only on the same remote; use `otherremote:/path` only when using a different remote) |
| `CLOUD_UPLOAD_MODE` | `parallel` | `parallel` or `sequential`. Inert under the default bundle layout: there is only one file to upload, so nothing runs concurrently either way |
| `CLOUD_PARALLEL_MAX_JOBS` | `2` | Max concurrent uploads. Only has an effect with `BUNDLE_ASSOCIATED_FILES=false`, which is what creates more than one upload |
| `CLOUD_PARALLEL_VERIFICATION` | `true` | Also verify each sidecar file. Only reachable with `BUNDLE_ASSOCIATED_FILES=false`; the setting is honoured in sequential mode too |
| `CLOUD_VERIFY_CHECKSUM` | `true` | Compare remote SHA256 to the local checksum after upload; size-only fallback when the backend has no native hash |
| `CLOUD_VERIFY_DOWNLOAD` | `false` | When the backend lacks native SHA256, download the object and hash it locally (uses bandwidth) |
| `CLOUD_WRITE_HEALTHCHECK` | `false` | Use write test for connectivity check |
| `RCLONE_TIMEOUT_CONNECTION` | `30` | Seconds. On the backup path this is the budget for the **whole** accessibility check, up to 3 attempts with 2s and 4s backoffs between them and up to 3 rclone calls each, not per command. The restore/decrypt cloud scan does apply it per command. Raise it if a slow remote makes the preflight give up |
| `RCLONE_TIMEOUT_OPERATION` | `300` | Per-operation upload timeout (seconds). `0` means **unbounded** uploads (no per-op deadline). Management/query ops (list, delete, retention) are always bounded: they use this value when it is > 0, otherwise a built-in 300s floor. So raising it also raises the management ceiling, and a positive value below 300 also shortens management ops. |
| `RCLONE_BANDWIDTH_LIMIT` | _(empty)_, template ships `10M` | Upload rate limit (e.g., `5M` = 5 MB/s) |
| `RCLONE_TRANSFERS` | `4`, template ships `16` | Passed as rclone `--transfers`; each ProxSave upload is a single-file `copyto`, so this does not set ProxSave upload concurrency |
| `RCLONE_RETRIES` | `3` | Retry attempts on failure |
| `RCLONE_VERIFY_METHOD` | `primary` | How the remote object is located for verification: `primary` (`rclone lsl`) or `alternative` (`rclone ls`). The SHA256 comparison (see `CLOUD_VERIFY_CHECKSUM`) runs on top of either. |
| `CLOUD_BATCH_SIZE` | `20` | Files per batch (deletion) |
| `CLOUD_BATCH_PAUSE` | `1` | Seconds between batches |
| `MAX_CLOUD_BACKUPS` | `30`, template ships `15` | Simple retention (ignored if GFS enabled) |
| `RCLONE_FLAGS` | _(empty)_ | Extra global rclone flags, split on whitespace and injected verbatim into **every backup-path** rclone command (right after the subcommand). The restore and decrypt cloud scan builds its rclone calls separately and does **not** receive them: see [How ProxSave invokes rclone](#how-proxsave-invokes-rclone). No shell quoting or validation, so keep each flag a single token (e.g. `--fast-list --checkers 8`). |
| `BUNDLE_ASSOCIATED_FILES` | `true` | Bundle the archive and its sidecars into one `.bundle.tar` before upload (the default cloud layout). Set `false` to upload the raw archive plus separate sidecars. See [Cloud layout](#cloud-layout-bundle-vs-raw). |

**Legacy env-var aliases.** For backward compatibility ProxSave also accepts these
older names. **When both are present the winner is not consistent**, so never leave
both in the file:

| Canonical | Legacy alias | Which wins if both are set |
|---|---|---|
| `CLOUD_ENABLED` | `ENABLE_CLOUD_BACKUP` | the legacy key, even with an empty value |
| `CLOUD_REMOTE` | `RCLONE_REMOTE` | the legacy key |
| `MAX_CLOUD_BACKUPS` | `CLOUD_RETENTION_DAYS` | the canonical key |
| `RCLONE_TIMEOUT_CONNECTION` | `CLOUD_CONNECTIVITY_TIMEOUT` | the canonical key |

So adding `CLOUD_REMOTE=NewRemote` to a config that still carries
`RCLONE_REMOTE=OldRemote` keeps uploading to `OldRemote`. Prefer the canonical names in
new configs, and delete the legacy line rather than leaving it alongside.

For complete configuration reference, see: **[Configuration Guide](CONFIGURATION.md)**

### Recommended Remote Path Formats (Important)

ProxSave supports both "new style" (path-only) and "legacy style" (`remote:path`) values, but using a consistent format avoids confusion.

**Recommended:**
- `CLOUD_REMOTE` should be just the **remote name** (no `:`), e.g. `nextcloud` or `GoogleDrive`.
- `CLOUD_REMOTE_PATH` should be a **path inside the remote** (no remote prefix). Use **no trailing slash**. A leading `/` is accepted
  and dropped: the path is always relative to the remote's root, which for an `sftp` remote is the login user's home directory.
  For a folder on this host, set `CLOUD_REMOTE` to its absolute path instead (for example `CLOUD_REMOTE=/mnt/backup`).
- `CLOUD_LOG_PATH` should be a **folder path** for logs. When logs are stored on the **same remote**, prefer **path-only** here too (no remote prefix). Use `otherremote:/path` only if logs must go to a different remote than `CLOUD_REMOTE`.

**Examples (same remote):**
```bash
CLOUD_REMOTE=nextcloud-katerasrael
CLOUD_REMOTE_PATH=B+K/BACKUP/marcellus
CLOUD_LOG_PATH=B+K/BACKUP/marcellus/logs
```

**Examples (different remotes for backups vs logs):**
```bash
CLOUD_REMOTE=nextcloud-backups
CLOUD_REMOTE_PATH=proxsave/backup/host1
CLOUD_LOG_PATH=nextcloud-logs:proxsave/log/host1
```

### Understanding CLOUD_REMOTE vs CLOUD_REMOTE_PATH

**How CLOUD_REMOTE and CLOUD_REMOTE_PATH work together**

1. **Recommended (remote name + full path in `CLOUD_REMOTE_PATH`)**
   - `CLOUD_REMOTE=GoogleDrive`
   - `CLOUD_REMOTE_PATH=/proxsave/backup/server1`
   to backups in: `GoogleDrive:/proxsave/backup/server1`

2. **Legacy compatibility (remote already contains a base path)**
   - `CLOUD_REMOTE=GoogleDrive:/proxsave/backup`
   - `CLOUD_REMOTE_PATH=server1` *(optional extra suffix)*
   to backups in: `GoogleDrive:/proxsave/backup/server1`

In both cases ProxSave combines the base path and the optional prefix into a single
path inside the remote, and uses that consistently for:
- **uploads** (cloud backend);
- **cloud retention**;
- **restore / decrypt menus** (entry "Cloud backups (rclone)").
  - Restore/decrypt cloud scanning applies `RCLONE_TIMEOUT_CONNECTION` per rclone command (the timer resets on each `lsf`/manifest read).

You can choose the style you prefer; they are equivalent from the tool's point of view.

3. **Local directory (an absolute path, for example a mounted share)**
   - `CLOUD_REMOTE=/mnt/cloud`
   - `CLOUD_REMOTE_PATH=server1` *(optional)*
   to backups in: `/mnt/cloud/server1`; with `CLOUD_LOG_PATH=/proxsave/log` the logs go to `/mnt/cloud/proxsave/log`.
   The copy still goes through rclone. ProxSave creates the directory when it is missing (not in a dry run) and gives the backups the same owner and mode as on the secondary path.

**When to use CLOUD_REMOTE_PATH**:
- Organizing multiple servers' backups: `server1/`, `server2/`
- Separating environments: `production/`, `staging/`
- Version control: `v1/`, `v2/`

> **Give every host its own prefix.** Retention lists the remote **recursively**, with no depth limit, then keeps only the archives this host owns. The owner is the hostname recorded in the manifest, or the host token the filename carries (`<host>-backup-<timestamp>`) when no manifest can be read. A host answers to the name the kernel reports and to the name it stamps into its own archives (`hostname -f`, so usually the FQDN), and to nothing else: `pve.siteA.example` and `pve.siteB.example` stay two machines, and so do `pve` and `pve.siteB.example` on a host whose FQDN does not resolve, unless the archives record this host's server identity: then they are this host's whatever name they carry. Archives left out of scope are never deleted and are not reported (a run with `--log-level debug` lists them). If `hostname -f` stops resolving the way it did when the archives were written, archives without this host's server identity stop rotating and the run says so (`  Named <name>, not rotated: <N> backups`). A per-host prefix is still worth having on its own merits: smaller listings, counts in the log that match what you expect, and restore menus that show only this host's backups. Leave `CLOUD_REMOTE_PATH` empty only when the remote, or the sub-path in `CLOUD_REMOTE`, belongs to this host alone.

---


## Earlier guide entry points

## Configure proxsave

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.

## Disaster Recovery

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.

## Overview

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.

## Prerequisites

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.

## Related Documentation

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.

## Testing

See the complete [operator procedure](#set-up-and-recover-cloud-backups). Detailed settings and implementation material remain in the reference sections above.
