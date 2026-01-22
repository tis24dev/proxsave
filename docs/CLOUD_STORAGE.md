# Cloud Storage with rclone

Complete guide to configuring rclone for cloud backup storage.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Supported Cloud Providers](#supported-cloud-providers)
- [Configuring rclone](#configuring-rclone)
- [Securing Configuration](#securing-configuration)
- [Configure proxsave](#configure-proxsave)
- [Performance Tuning](#performance-tuning)
- [Testing](#testing)
- [Troubleshooting](#troubleshooting)
- [Disaster Recovery](#disaster-recovery)
- [Related Documentation](#related-documentation)

---

## Overview

Proxsave integrates with [rclone](https://rclone.org/) to provide seamless cloud backup storage across 40+ cloud providers. Cloud storage serves as a **non-critical tertiary backup layer** for long-term archival and disaster recovery.

**Key capabilities**:
- **Multi-provider support**: Google Drive, S3, Backblaze B2, OneDrive, MinIO, and 40+ more
- **Non-blocking uploads**: Local backup completes first, cloud upload happens asynchronously
- **Automatic retry logic**: Configurable retry attempts with exponential backoff
- **Bandwidth management**: Upload rate limiting for shared networks
- **Parallel/sequential modes**: Optimize for network speed and API limits
- **GFS retention**: Advanced retention policies applied to cloud backups
- **Verification**: SHA256 checksum validation after upload

---

## Architecture

Proxsave uses a **3-tier storage system**:

```
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

```
1. Create backup locally           ✓ Critical (must succeed)
   └─> BACKUP_PATH/

2. Copy to secondary storage       ○ Optional (warn on failure)
   └─> SECONDARY_PATH/

3. Upload to cloud (rclone)        ○ Optional (warn on failure)
   ├─> Parallel mode: 2-4 jobs concurrently
   ├─> Sequential mode: One at a time
   ├─> Verification: SHA256 checksum
   └─> Retry: 3 attempts with backoff

4. Apply retention policies        ✓ Per-tier retention
   ├─> Local: MAX_LOCAL_BACKUPS or GFS
   ├─> Secondary: MAX_SECONDARY_BACKUPS or GFS
   └─> Cloud: MAX_CLOUD_BACKUPS or GFS
```

---

## Prerequisites

### Install rclone

```bash
# Verify installation
which rclone
rclone version

# Install via official script (recommended)
curl https://rclone.org/install.sh | sudo bash

# Or via package manager
# Debian/Ubuntu
sudo apt-get update && sudo apt-get install rclone

# CentOS/RHEL
sudo yum install rclone

# Or manual download
wget https://downloads.rclone.org/rclone-current-linux-amd64.zip
unzip rclone-current-linux-amd64.zip
sudo cp rclone-*/rclone /usr/local/bin/
sudo chmod 755 /usr/local/bin/rclone

# Verify
rclone version  # Should show v1.50+
```

**Minimum version**: rclone v1.50+
**Recommended version**: Latest stable (v1.65+)

---

## Supported Cloud Providers

| Provider | rclone Type | Use Case | Free Tier |
|----------|-------------|----------|-----------|
| **Google Drive** | `drive` | Small/medium businesses, easy OAuth | 15GB |
| **Amazon S3** | `s3` | Enterprise, scalable, highly available | 5GB (12 months) |
| **Backblaze B2** | `b2` | Cost-effective archival | 10GB + 1GB/day egress |
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

```bash
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
- Cost-effective: $0.005/GB/month (vs S3 $0.023)
- 10GB free storage + 1GB/day free download
- Lower API rate limit than S3
- Ideal for long-term archival

### Verify Configuration

```bash
# List configured remotes
rclone listremotes
# Output: gdrive:, s3backup:, minio:, b2:

# Show configuration (no passwords)
rclone config show gdrive

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

## Configure proxsave

### Minimal Configuration

```bash
# Edit backup.env
nano /opt/proxsave/configs/backup.env

# Enable cloud storage
CLOUD_ENABLED=true
# rclone remote NAME (from `rclone config`)
CLOUD_REMOTE=GoogleDrive
# Full path (or prefix) inside the remote
CLOUD_REMOTE_PATH=/proxsave/backup

# Retention
MAX_CLOUD_BACKUPS=30
```

This is sufficient to start! Other options use sensible defaults.

### Recommended Production Configuration

```bash
# Cloud storage
CLOUD_ENABLED=true
CLOUD_REMOTE=GoogleDrive
CLOUD_REMOTE_PATH=/proxsave/backup   # Folder path inside the remote
CLOUD_LOG_PATH=/proxsave/log         # Optional: log folder inside the same remote

# Upload mode
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=2
CLOUD_PARALLEL_VERIFICATION=true

# Timeouts
RCLONE_TIMEOUT_CONNECTION=30
RCLONE_TIMEOUT_OPERATION=300          # 5 minutes

# Bandwidth
RCLONE_BANDWIDTH_LIMIT=               # Empty = unlimited
RCLONE_TRANSFERS=4

# Retry & verification
RCLONE_RETRIES=3
RCLONE_VERIFY_METHOD=primary

# Batch deletion
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=1

# GFS retention
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=12
RETENTION_YEARLY=3
```

### Configuration Reference

| Variable | Default | Description |
|----------|---------|-------------|
| `CLOUD_ENABLED` | `false` | Enable cloud storage |
| `CLOUD_REMOTE` | _(required)_ | rclone remote **name** from `rclone config` (legacy `remote:path` still supported) |
| `CLOUD_REMOTE_PATH` | _(empty)_ | Folder path/prefix inside the remote (e.g., `/proxsave/backup`) |
| `CLOUD_LOG_PATH` | _(empty)_ | Optional log folder (recommended: path-only on the same remote; use `otherremote:/path` only when using a different remote) |
| `CLOUD_UPLOAD_MODE` | `parallel` | `parallel` or `sequential` |
| `CLOUD_PARALLEL_MAX_JOBS` | `2` | Max concurrent uploads (parallel mode) |
| `CLOUD_PARALLEL_VERIFICATION` | `true` | Verify checksums after upload |
| `CLOUD_WRITE_HEALTHCHECK` | `false` | Use write test for connectivity check |
| `RCLONE_TIMEOUT_CONNECTION` | `30` | Connection timeout (seconds). Also used by restore/decrypt when scanning cloud backups (list + manifest read). |
| `RCLONE_TIMEOUT_OPERATION` | `300` | Operation timeout (seconds) |
| `RCLONE_BANDWIDTH_LIMIT` | _(empty)_ | Upload rate limit (e.g., `5M` = 5 MB/s) |
| `RCLONE_TRANSFERS` | `4` | Number of parallel transfers |
| `RCLONE_RETRIES` | `3` | Retry attempts on failure |
| `RCLONE_VERIFY_METHOD` | `primary` | Verification method |
| `CLOUD_BATCH_SIZE` | `20` | Files per batch (deletion) |
| `CLOUD_BATCH_PAUSE` | `1` | Seconds between batches |
| `MAX_CLOUD_BACKUPS` | `30` | Simple retention (ignored if GFS enabled) |

For complete configuration reference, see: **[Configuration Guide](CONFIGURATION.md)**

### Recommended Remote Path Formats (Important)

ProxSave supports both “new style” (path-only) and “legacy style” (`remote:path`) values, but using a consistent format avoids confusion.

**Recommended:**
- `CLOUD_REMOTE` should be just the **remote name** (no `:`), e.g. `nextcloud` or `GoogleDrive`.
- `CLOUD_REMOTE_PATH` should be a **path inside the remote** (no remote prefix). Use **no trailing slash**. A leading `/` is accepted.
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
   → backups in: `GoogleDrive:/proxsave/backup/server1`

2. **Legacy compatibility (remote already contains a base path)**  
   - `CLOUD_REMOTE=GoogleDrive:/proxsave/backup`  
   - `CLOUD_REMOTE_PATH=server1` *(optional extra suffix)*  
   → backups in: `GoogleDrive:/proxsave/backup/server1`

In both cases ProxSave combines the base path and the optional prefix into a single
path inside the remote, and uses that consistently for:
- **uploads** (cloud backend);
- **cloud retention**;
- **restore / decrypt menus** (entry “Cloud backups (rclone)”).
  - Restore/decrypt cloud scanning is protected by `RCLONE_TIMEOUT_CONNECTION` (increase it on slow remotes or very large directories).

You can choose the style you prefer; they are equivalent from the tool’s point of view.

**When to use CLOUD_REMOTE_PATH**:
- Organizing multiple servers' backups: `server1/`, `server2/`
- Separating environments: `production/`, `staging/`
- Version control: `v1/`, `v2/`
- Or leave empty to store in remote root

---

## Performance Tuning

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
RCLONE_TRANSFERS=8-16
CLOUD_BATCH_SIZE=50-100
CLOUD_BATCH_PAUSE=1
```

**Characteristics**:
- High API limits
- Excellent scalability
- Low latency

#### Backblaze B2

```bash
RCLONE_TIMEOUT_CONNECTION=45
RCLONE_TRANSFERS=2-4
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=2
```

**Characteristics**:
- Lower API limits than S3
- Cost-effective for archival
- 10GB free tier

#### MinIO (Self-hosted LAN)

```bash
RCLONE_TIMEOUT_CONNECTION=10
RCLONE_TRANSFERS=8+
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

## Testing

### Dry-Run Test

```bash
# Build
cd /opt/proxsave
make build

# Dry-run test
DRY_RUN=true ./build/proxsave

# Check output:
# ✓ "Cloud storage initialized: gdrive:pbs-backups"
# ✓ "Cloud remote gdrive is accessible"
# ✓ "[DRY-RUN] Would upload backup to cloud storage"
```

### Real Backup Test

```bash
# Real backup
./build/proxsave

# Verify upload
rclone ls gdrive:pbs-backups/
# Should show backup files

# Verify logs
rclone ls gdrive:/pbs-logs/
# Should show log files
```

### Manual rclone Test

```bash
# Test upload
echo "test" > /tmp/test.txt
rclone copy /tmp/test.txt gdrive:pbs-backups/ --verbose

# Verify
rclone lsl gdrive:pbs-backups/test.txt

# Test download
rclone copy gdrive:pbs-backups/test.txt /tmp/test-download.txt
cat /tmp/test-download.txt

# Cleanup
rclone deletefile gdrive:pbs-backups/test.txt
rm /tmp/test*.txt
```

---

## Troubleshooting

### Common Errors

| Error | Cause | Solution |
|-------|-------|----------|
| `rclone not found in PATH` | Not installed | `curl https://rclone.org/install.sh \| sudo bash` |
| `couldn't find configuration section 'gdrive'` | Remote not configured | `rclone config` → create remote |
| `401 unauthorized` | Credentials expired | `rclone config reconnect gdrive` or regenerate keys |
| `connection timeout (30s)` | Slow network | Increase `RCLONE_TIMEOUT_CONNECTION=60` |
| `Timed out while scanning ... (rclone)` | Slow remote / huge directory | Increase `RCLONE_TIMEOUT_CONNECTION` and re-try restore/decrypt scan |
| `operation timeout (300s exceeded)` | Large file + slow network | Increase `RCLONE_TIMEOUT_OPERATION=900` |
| `429 Too Many Requests` | API rate limiting | Reduce `RCLONE_TRANSFERS=2`, increase `CLOUD_BATCH_PAUSE=3` |
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

```bash
# Run with debug level
./build/proxsave --log-level debug

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

# Test with dry-run
./build/proxsave --dry-run --log-level debug
# Check output for loaded config values
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

## Disaster Recovery

### Backup Configuration

```bash
# Save critical configs
tar -czf /tmp/pbs-config-backup.tar.gz \
    /root/.config/rclone/rclone.conf \
    /opt/proxsave/configs/backup.env

# Upload to cloud (manual)
rclone copy /tmp/pbs-config-backup.tar.gz gdrive:/pbs-disaster-recovery/

# Or automate via backup.env
CUSTOM_BACKUP_PATHS="
/root/.config/rclone/rclone.conf
/opt/proxsave/configs/
"
```

### Recovery Procedure

Complete step-by-step recovery process:

**Step 1: Setup New Server**

```bash
apt-get update && apt-get install rclone
```

**Step 2: Restore rclone Config**

```bash
# Option A: From separate backup
rclone copy gdrive:/pbs-disaster-recovery/rclone.conf /root/.config/rclone/

# Option B: Reconfigure manually
rclone config
```

**Step 3: Verify Access**

```bash
rclone ls gdrive:pbs-backups/
```

**Step 4: Download Latest Backup**

```bash
LATEST=$(rclone lsf gdrive:pbs-backups/ --format "t;p" | sort -r | head -1 | cut -d';' -f2)
echo "Latest: $LATEST"
rclone copy "gdrive:pbs-backups/$LATEST" /tmp/recovery/
```

**Step 5: Extract Bundle** (if using bundle format)

```bash
cd /tmp/recovery
tar -xf *.bundle.tar
```

**Step 6: Verify Checksum**

```bash
sha256sum -c *.sha256
```

**Step 7: Decrypt** (if encrypted)

```bash
age --decrypt -i /path/to/key.txt -o backup.tar.xz backup.tar.xz.age
```

**Step 8: Extract Archive**

```bash
tar -xJf backup.tar.xz -C /restore/
```

**Step 9: Restore Files**

```bash
# Review extracted files first
ls -la /restore/

# Restore selectively (recommended)
cp -a /restore/etc/pve/* /etc/pve/
cp -a /restore/etc/proxmox-backup/* /etc/proxmox-backup/

# Or restore all (use with caution)
cp -a /restore/* /
```

**For complete restore workflows**, see: **[Restore Guide](RESTORE_GUIDE.md)**

---

## Related Documentation

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference including all cloud/rclone settings
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption for cloud-stored backups

### Restore Operations
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete restore workflows from cloud backups
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical implementation details

### Reference
- **[Examples](EXAMPLES.md)** - Real-world cloud backup scenarios
- **[Troubleshooting](TROUBLESHOOTING.md)** - Cloud storage troubleshooting
- **[CLI Reference](CLI_REFERENCE.md)** - Command-line flags

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## FAQ

**Q: Can I use multiple cloud providers?**
A: No, currently only one `CLOUD_REMOTE` is supported. Workaround: Use `rclone union` to combine multiple backends.

**Q: Can I use a network address like "192.168.0.10/folder" for SECONDARY_PATH?**
A: **No**. `SECONDARY_PATH` and `BACKUP_PATH` require **filesystem-mounted paths only**. Network shares must be mounted first using NFS/CIFS/SMB mount commands, then you use the local mount point path (e.g., `/mnt/nas-backup`).

If you want to use a direct network address without mounting, configure it as `CLOUD_REMOTE` using rclone with an S3-compatible backend (like MinIO) or appropriate protocol.

Example comparison:
- ✗ WRONG: `SECONDARY_PATH=192.168.0.10:/backup`
- ✗ WRONG: `SECONDARY_PATH=//server/share`
- ✓ RIGHT: Mount first: `sudo mount 192.168.0.10:/backup /mnt/backup`, then `SECONDARY_PATH=/mnt/backup`
- ✓ ALTERNATIVE: Use `CLOUD_REMOTE=minio` with `CLOUD_REMOTE_PATH=/backup` (requires rclone configuration for MinIO/S3 on LAN)

**Q: Do cloud logs consume too much space?**
A: Logs follow backup retention automatically. To disable cloud log upload: `CLOUD_LOG_PATH=""` (empty).

**Q: Does cloud upload slow down backups?**
A: Local backup completes first (critical). Cloud upload happens after but delays backup completion. For very slow clouds, consider separate cron job for upload.

**Q: Can I backup directly to cloud only (no local)?**
A: No, local storage is mandatory (critical). Cloud is always secondary/tertiary. Philosophy: fast local backup → slow cloud archival.

**Q: How much RAM does rclone use?**
A: Depends on `RCLONE_TRANSFERS`. Each transfer uses ~10-50MB. With `RCLONE_TRANSFERS=8` → ~80-400MB. For low-RAM systems: `RCLONE_TRANSFERS=2`.

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

# Show remote config
rclone config show gdrive

# List files (long format)
rclone lsl gdrive:pbs-backups/

# List files (short format)
rclone lsf gdrive:pbs-backups/

# Check quota
rclone about gdrive:

# Copy local → remote
rclone copy /local/file.txt gdrive:pbs-backups/

# Copy remote → local
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
