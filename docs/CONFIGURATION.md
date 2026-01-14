# Configuration Reference

Complete reference for all 200+ configuration variables in `configs/backup.env`.

## Table of Contents

- [Configuration File Location](#configuration-file-location)
- [General Settings](#general-settings)
- [Security Settings](#security-settings)
- [Disk Space](#disk-space)
- [Storage Paths](#storage-paths)
- [Compression Settings](#compression-settings)
- [Advanced Optimizations](#advanced-optimizations)
- [Network Preflight](#network-preflight)
- [Collection Exclusions](#collection-exclusions)
- [Secondary Storage](#secondary-storage)
- [Cloud Storage (rclone)](#cloud-storage-rclone)
- [Storage Comparison](#storage-comparison)
- [rclone Settings](#rclone-settings)
- [Batch Deletion (Cloud)](#batch-deletion-cloud)
- [Retention Policies](#retention-policies)
- [Encryption & Bundling](#encryption--bundling)
- [Notifications](#notifications)
- [Metrics - Prometheus](#metrics---prometheus)
- [Collector Options](#collector-options)
- [Custom Paths & Blacklist](#custom-paths--blacklist)

---

## Configuration File Location

**Default**: `/opt/proxsave/configs/backup.env`
**Custom**: Specify with `--config` flag

```bash
# Use custom config file
./build/proxsave --config /path/to/my-backup.env
```

---

## General Settings

```bash
# Enable/disable backup system
BACKUP_ENABLED=true                # true | false

# Enable Go pipeline (vs legacy Bash)
ENABLE_GO_BACKUP=true              # true | false

# Colored output in terminal
USE_COLOR=true                     # true | false

# Colorize "Step N/8" lines in logs
COLORIZE_STEP_LOGS=true            # true | false (requires USE_COLOR=true)

# Debug level
DEBUG_LEVEL=standard               # standard | advanced | extreme

# Dry-run mode (test without changes)
DRY_RUN=false                      # true | false

# Enable/disable always-on pprof profiling (CPU + heap)
PROFILING_ENABLED=true             # true | false (profiles written under LOG_PATH)
```

### DEBUG_LEVEL Details

| Level | Description |
|-------|-------------|
| `standard` | Basic operation logging |
| `advanced` | Detailed command execution, file operations |
| `extreme` | Full verbose output including rclone/compression internals |

---

## Security Settings

```bash
# Security preflight check
SECURITY_CHECK_ENABLED=true                     # true | false

# Auto-update file hashes
AUTO_UPDATE_HASHES=true                         # true | false

# Auto-fix permissions
AUTO_FIX_PERMISSIONS=true                       # true | false

# Block backup on security issues
CONTINUE_ON_SECURITY_ISSUES=false               # false = block, true = warn

# Network security checks
CHECK_NETWORK_SECURITY=false                    # true | false
CHECK_FIREWALL=false                            # true | false
CHECK_OPEN_PORTS=false                          # true | false

# Suspicious port list (space-separated)
SUSPICIOUS_PORTS="6666 6665 1337 31337 4444 5555 4242 6324 8888 2222 3389 5900"

# Port whitelist (format: service:port)
PORT_WHITELIST=                                 # e.g., "sshd:22,nginx:443"

# Suspicious process names (comma-separated)
# NOTE: Your values are ADDED to the built-in defaults (not replaced)
# Built-in defaults: ncat, cryptominer, xmrig, kdevtmpfsi, kinsing, minerd, mr.sh
SUSPICIOUS_PROCESSES="ncat,cryptominer,xmrig,kdevtmpfsi,kinsing,minerd,mr.sh"

# Safe process names (won't trigger alerts)
# NOTE: Your values are ADDED to the built-in defaults (not replaced)
# Supports exact match, prefix with *, or regex: patterns (case-insensitive)
# Built-in defaults for SAFE_BRACKET_PROCESSES: sshd:, systemd, cron, rsyslogd, dbus-daemon, zvol_tq*, arc_*, dbu_*, dbuf_*, l2arc_feed, lockd, nfsd*, nfsv4 callback*
SAFE_BRACKET_PROCESSES="sshd:,systemd,cron,rsyslogd,dbus-daemon"

# Built-in defaults for SAFE_KERNEL_PROCESSES: ksgxd, hwrng, usb-storage, vdev_autotrim, card1-crtc0, card1-crtc1, card1-crtc2, kvm-pit*, and various regex patterns
SAFE_KERNEL_PROCESSES="ksgxd,hwrng,usb-storage,vdev_autotrim,card1-crtc0,card1-crtc1,card1-crtc2,kvm-pit,regex:^card[0-9]+-crtc[0-9]+$,regex:^drbd_[wrs]_.+,regex:^kvm-pit/[0-9]+$,regex:^kmmpd-drbd[0-9]+$"

# Skip permission checks (use only for testing)
SKIP_PERMISSION_CHECK=false                     # true | false

# Permission management (Bash-compatible behavior)
BACKUP_USER=backup                              # System user for backup/log directory ownership
BACKUP_GROUP=backup                             # System group for backup/log directory ownership
SET_BACKUP_PERMISSIONS=false                    # true = apply chown/chmod on backup/log directories
```

### Security Check Behavior

- Verifies file permissions (0700 for directories, 0600 for sensitive files)
- Checks for suspicious open ports
- Scans for suspicious processes
- Validates file hashes to detect tampering
- **If `CONTINUE_ON_SECURITY_ISSUES=false`**: Backup aborts on any issue
- **If `CONTINUE_ON_SECURITY_ISSUES=true`**: Issues logged as warnings, backup continues

#### Process List Merge Behavior

All security process lists use an **additive merge strategy**:

- **`SUSPICIOUS_PROCESSES`**: Your configured values are **added** to built-in defaults
- **`SAFE_BRACKET_PROCESSES`**: Your configured values are **added** to built-in defaults
- **`SAFE_KERNEL_PROCESSES`**: Your configured values are **added** to built-in defaults

**Example**: If you configure `SUSPICIOUS_PROCESSES="mymalware,suspicious-app"`, the final list will include:
- Built-in defaults: `ncat, cryptominer, xmrig, kdevtmpfsi, kinsing, minerd, mr.sh`
- Your additions: `mymalware, suspicious-app`
- Final result: All of the above combined (duplicates automatically removed)

This means you don't need to repeat the default values - just add your custom entries.

### Permission Management

When `SET_BACKUP_PERMISSIONS=true`, the system applies Bash-compatible ownership and permissions:

**Ownership (chown)**:
- Recursively changes owner:group for:
  - `BACKUP_PATH` (primary backup directory)
  - `LOG_PATH` (primary log directory)
  - `SECONDARY_PATH` (if configured)
  - `SECONDARY_LOG_PATH` (if configured)
- Uses `BACKUP_USER:BACKUP_GROUP` as the target owner
- Does NOT touch binary files, config files, or system paths

**Permissions (chmod)**:
- Applies mode `0750` (rwxr-x---) to directories only
- Files keep their existing permissions (unchanged)
- Conservative and safe approach

**Requirements**:
- Both `BACKUP_USER` and `BACKUP_GROUP` must be set
- User and group must already exist on the system
- **Does NOT create users or groups** (unlike legacy Bash version)

**Error handling**:
- Non-fatal: All failures logged as warnings
- Backup continues even if permission changes fail
- User/group not found: logs warning and skips operation

**Use cases**:
- Migration from legacy Bash version
- Multi-user environments requiring specific ownership
- Shared backup storage with group access
- NFS/CIFS mounts requiring specific ownership

**Example**:
```bash
# Create dedicated backup user/group first
groupadd backup
useradd -r -g backup -s /bin/false backup

# Configure ownership
BACKUP_USER=backup
BACKUP_GROUP=backup
SET_BACKUP_PERMISSIONS=true

# Result: All backup/log directories owned by backup:backup with mode 0750
```

---

## Disk Space

```bash
# Minimum free space required (GB)
MIN_DISK_SPACE_PRIMARY_GB=1        # Primary storage
MIN_DISK_SPACE_SECONDARY_GB=1      # Secondary storage
MIN_DISK_SPACE_CLOUD_GB=1          # Cloud storage (not enforced for remote)
```

**Behavior**: Backup aborts if available space < minimum threshold.

---

## Storage Paths

```bash
# Base directory for all operations
BASE_DIR=/opt/proxsave

# Lock file directory
LOCK_PATH=${BASE_DIR}/lock

# Credentials directory
SECURE_ACCOUNT=${BASE_DIR}/secure_account

# Primary backup storage
BACKUP_PATH=${BASE_DIR}/backup

# Primary log storage
LOG_PATH=${BASE_DIR}/log
```

**Path resolution**: `${BASE_DIR}` expands automatically. Use absolute paths or relative to `BASE_DIR`.

---

## Compression Settings

```bash
# Compression algorithm
COMPRESSION_TYPE=xz                # none | gzip | pigz | bzip2 | xz | lzma | zstd

# Compression level
COMPRESSION_LEVEL=9                # Range depends on algorithm (see table below)

# Compression threads (0 = auto-detect CPU cores)
COMPRESSION_THREADS=0              # 0 = auto, >0 = fixed thread count

# Compression mode
COMPRESSION_MODE=ultra             # fast | standard | maximum | ultra
```

### Compression Algorithm Details

| Algorithm | Level Range | Notes |
|-----------|-------------|-------|
| `none` | 0 | No compression |
| `gzip` | 1-9 | Single-threaded, widely compatible |
| `pigz` | 1-9 | Parallel gzip, faster on multi-core |
| `bzip2` | 1-9 | Higher compression, slower |
| `xz` | 0-9 | Excellent compression, supports `--extreme` |
| `lzma` | 0-9 | Similar to xz |
| `zstd` | 1-22 | Fast, good compression (>19 uses `--ultra`) |

### Compression Modes

| Mode | Description |
|------|-------------|
| `fast` | Lower levels, faster execution |
| `standard` | Balanced |
| `maximum` | Level 9 for gzip/bzip2/xz, level 19 for zstd |
| `ultra` | Adds `--extreme` for xz/lzma, level 22 for zstd |

### Examples

**Fast backup** (large files, quick compression):
```bash
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
COMPRESSION_MODE=fast
```

**Maximum compression** (archival, storage limited):
```bash
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=9
COMPRESSION_MODE=ultra
COMPRESSION_THREADS=0  # Use all CPU cores
```

**No compression** (already compressed data):
```bash
COMPRESSION_TYPE=none
```

---

## Advanced Optimizations

```bash
# Enable smart chunking for large files
ENABLE_SMART_CHUNKING=true         # true | false

# Enable deduplication
ENABLE_DEDUPLICATION=true          # true | false

# Enable prefiltering
ENABLE_PREFILTER=true              # true | false

# Chunking threshold (MB)
CHUNK_THRESHOLD_MB=50              # Files >50MB are chunked

# Chunk size (MB)
CHUNK_SIZE_MB=10                   # Each chunk is 10MB

# Prefilter max file size (MB)
PREFILTER_MAX_FILE_SIZE_MB=8       # Skip prefilter for files >8MB
```

### What These Do

- **Smart chunking**: Splits large files for parallel processing
- **Deduplication**: Detects duplicate data blocks (reduces storage)
- **Prefilter**: Analyzes small files before compression (optimizes algorithm selection)

---

## Network Preflight

```bash
# Skip network connectivity checks
DISABLE_NETWORK_PREFLIGHT=false    # false = check, true = skip

# Use case: Offline environments without Telegram/email/cloud
```

**Behavior**:
- **false (default)**: Verifies connectivity before using network features
- **true**: Skips checks (operations may fail later if network unavailable)

---

## Collection Exclusions

```bash
# Glob patterns to exclude (space or comma separated)
BACKUP_EXCLUDE_PATTERNS="*/cache/**, /var/tmp/**, *.log"
```

### Pattern Syntax

- `*`: Match any file
- `**`: Match any directory recursively
- Example: `*/cache/**` excludes all `cache/` subdirectories

---

## Secondary Storage

```bash
# Enable secondary storage
SECONDARY_ENABLED=false            # true | false

# Secondary backup path
SECONDARY_PATH=/mnt/secondary/backup

# Secondary log path
SECONDARY_LOG_PATH=/mnt/secondary/log
```

### Use Case

Additional local storage for redundant backup copies - mounted NAS, USB drives, local disks.

### IMPORTANT PATH REQUIREMENTS

- `SECONDARY_PATH` **must be a filesystem-mounted path** (e.g., `/mnt/nas-backup`, `/media/usb-drive`)
- `SECONDARY_PATH` **CANNOT** be a network address (e.g., `192.168.0.10/folder`, `//server/share`)
- Network shares **must be mounted first** using standard Linux mounting (NFS/CIFS/SMB)

### Network Storage Setup

For NAS or network storage:

**1. First, mount the network share**:
```bash
# NFS example
sudo mount 192.168.0.10:/backup /mnt/nas-backup

# CIFS/SMB example
sudo mount -t cifs //192.168.0.10/backup /mnt/nas-backup -o credentials=/root/.smbcreds

# To make it permanent, add to /etc/fstab
```

**2. Then configure SECONDARY_PATH**:
```bash
SECONDARY_PATH=/mnt/nas-backup  # ✓ Correct - uses mounted path
```

### What NOT to Do

```bash
SECONDARY_PATH=192.168.0.10/backup       # ✗ WRONG - network address
SECONDARY_PATH=//server/share            # ✗ WRONG - UNC path
SECONDARY_PATH=\\192.168.0.10\backup    # ✗ WRONG - Windows path
```

**For direct network access without mounting:** Use `CLOUD_REMOTE` with rclone instead (see [Cloud Storage](#cloud-storage-rclone) section).

### Behavior

- Secondary storage is **non-critical** (failures log warnings, don't abort backup)
- Files copied via native Go (no dependency on rclone)
- Same retention policy as primary storage

---

## Cloud Storage (rclone)

```bash
# Enable cloud storage
CLOUD_ENABLED=false                # true | false

# rclone remote (recommended: remote NAME + path via CLOUD_REMOTE_PATH)
CLOUD_REMOTE=GoogleDrive                   # remote name from `rclone config`
CLOUD_REMOTE_PATH=/proxsave/backup         # folder path inside the remote

# Cloud log path (same remote, optional)
CLOUD_LOG_PATH=/proxsave/log               # leave empty to disable log uploads

# Legacy compatibility (still supported):
# CLOUD_REMOTE=GoogleDrive:/proxsave/backup        # legacy combined syntax
# CLOUD_REMOTE_PATH=server1                        # extra suffix if needed
# CLOUD_LOG_PATH=GoogleDrive:/proxsave/log         # legacy explicit remote for logs

# Upload mode
CLOUD_UPLOAD_MODE=parallel         # sequential | parallel

# Parallel worker count
CLOUD_PARALLEL_MAX_JOBS=2          # Workers for associated files

# Verify files in parallel
CLOUD_PARALLEL_VERIFICATION=true   # true | false

# Preflight connectivity check
CLOUD_WRITE_HEALTHCHECK=false      # true | false (auto-fallback mode vs force write test)
```

### Connectivity Check Modes

| Setting | Mode | Behavior |
|---------|------|----------|
| `false` (default) | **Auto mode** (RECOMMENDED) | Tries fast `rclone lsf` list check first, automatically falls back to write test if permission errors detected (403/401). Best-of-both-worlds approach. |
| `true` | **Force write test** | Skips list check entirely, only uses write test (creates/deletes temp file `.pbs-backup-healthcheck-<timestamp>`). Use if you want to skip list operations completely. |

### How Auto-Fallback Works (when `false`)

1. First attempt: Fast `rclone lsf` to check remote accessibility
2. If permission error detected (403/401/access denied): Automatically tries write test
3. If write test succeeds: Continues with backup (warns about list permission limitation)
4. If both fail: Returns error with clear troubleshooting hints

### When Auto-Fallback Helps

Automatic with `false`:
- ✅ **Cloudflare R2** with restricted API tokens (no list permissions)
- ✅ **S3-compatible providers** with minimal token permissions
- ✅ **Backblaze B2**, **Wasabi** write-only tokens
- ✅ **First-time setup** with uncertain token permissions

### When to Set CLOUD_WRITE_HEALTHCHECK=true

- You know your token lacks list permissions (skip failed list attempt)
- Slightly faster checks by avoiding initial list attempt
- Network with very slow list operations

### Performance

- Auto mode (`false`): ~1-2s on success, ~3-4s if fallback needed (one-time on first run)
- Force write (`true`): ~2-3s always (slightly slower than list, faster than auto-fallback)

### Remote Format

**Format**: `<remote-name>:<path>`
- `remote-name`: Configured in `rclone config`
- `:` (colon): Required separator
- `path`: Directory inside remote (optional for root)

**Examples**:
- `gdrive:pbs-backups` → Google Drive, folder "pbs-backups"
- `s3:my-bucket/backups` → S3 bucket, subfolder "backups"
- `minio:/pbs` → MinIO, absolute path "/pbs"

### Upload Modes

| Mode | Description |
|------|-------------|
| `sequential` | Upload files one at a time (lower memory, predictable) |
| `parallel` | Upload main file sequentially, then associated files (.sha256, .metadata, .bundle) in parallel (faster, uses more memory) |

---

## Storage Comparison

Quick comparison to help you choose the right storage configuration:

| Feature | SECONDARY_PATH | CLOUD_REMOTE (rclone) |
|---------|----------------|----------------------|
| **Path Type** | Filesystem-mounted paths only | Network addresses via rclone |
| **Valid Examples** | `/mnt/nas-backup`<br>`/media/usb-drive`<br>`/backup/secondary` | `CLOUD_REMOTE=GoogleDrive` + `CLOUD_REMOTE_PATH=/backups`<br>`CLOUD_REMOTE=b2` + `CLOUD_REMOTE_PATH=/pbs-prod`<br>`CLOUD_REMOTE=minio` + `CLOUD_REMOTE_PATH=/pbs` |
| **Invalid Examples** | ❌ `192.168.0.10/folder`<br>❌ `//server/share`<br>❌ `\\192.168.0.10\backup` | N/A (handles network directly) |
| **Network Storage** | Must mount first via NFS/CIFS/SMB | Direct access via rclone config |
| **Setup Complexity** | Simple (native Go copy) | Moderate (requires rclone config) |
| **Dependencies** | None | Requires rclone installed |
| **Speed** | Fast (local filesystem I/O) | Depends on network/cloud |
| **Use Case** | - Local USB drives<br>- Pre-mounted NAS shares<br>- Additional local disks | - Cloud storage (GDrive, S3, B2)<br>- LAN servers (MinIO, S3)<br>- Remote storage without mounting |
| **Failure Behavior** | Non-critical (warns, continues) | Non-critical (warns, continues) |
| **Setup Example** | `sudo mount 192.168.0.10:/share /mnt/nas`<br>`SECONDARY_PATH=/mnt/nas` | `rclone config` (create "minio" remote)<br>`CLOUD_REMOTE=minio` + `CLOUD_REMOTE_PATH=/backups` |

### Decision Guide

- **Use SECONDARY_PATH if**: You have local storage (USB drive) OR willing to mount network shares to filesystem
- **Use CLOUD_REMOTE if**: You want direct network access (no mounting) OR using cloud providers OR using S3-compatible storage

**See** [docs/CLOUD_STORAGE.md](CLOUD_STORAGE.md) **for complete rclone setup guide.**

---

## rclone Settings

```bash
# Connection timeout (seconds)
RCLONE_TIMEOUT_CONNECTION=30       # Remote accessibility check

# Operation timeout (seconds)
RCLONE_TIMEOUT_OPERATION=300       # Upload/download operations (5 minutes default)

# Bandwidth limit
RCLONE_BANDWIDTH_LIMIT=            # Empty = unlimited, "10M" = 10 MB/s

# Parallel transfers inside rclone
RCLONE_TRANSFERS=16                # Number of simultaneous file transfers

# Retry attempts
RCLONE_RETRIES=3                   # Retry count for failed operations

# Verification method
RCLONE_VERIFY_METHOD=primary       # primary | alternative

# Additional rclone flags
RCLONE_FLAGS="--checkers=4 --stats=0 --drive-use-trash=false --drive-pacer-min-sleep=10ms --drive-pacer-burst=100"
```

### Timeout Tuning

- **CONNECTION**: Short timeout for quick accessibility check (default 30s)
- **OPERATION**: Long timeout for large file uploads (increase for slow networks)

### Bandwidth Limit Format

- `""` = Unlimited
- `"10M"` = 10 MB/s
- `"512K"` = 512 KB/s

### Verification Methods

- **primary**: Uses `rclone lsl <file>` (fast, direct)
- **alternative**: Uses `rclone ls <directory>` then searches (slower, compatible with all remotes)

---

## Batch Deletion (Cloud)

```bash
# Files per batch
CLOUD_BATCH_SIZE=20                # Delete max 20 files per batch

# Pause between batches (seconds)
CLOUD_BATCH_PAUSE=1                # Wait 1 second between batches
```

### Purpose

Avoid API rate limiting during retention cleanup.

### Example

Deleting 50 files with `BATCH_SIZE=20`, `BATCH_PAUSE=1`:
- Batch 1: Delete files 1-20, pause 1s
- Batch 2: Delete files 21-40, pause 1s
- Batch 3: Delete files 41-50, done

### Provider Tuning

| Provider | Recommended BATCH_SIZE | Recommended BATCH_PAUSE |
|----------|----------------------|------------------------|
| Google Drive | 10-15 | 2-3 |
| S3/Wasabi | 50-100 | 1 |
| Backblaze B2 | 20-30 | 2 |
| MinIO (self-hosted) | 100+ | 0 |

---

## Retention Policies

Two mutually exclusive strategies:

### 1. Simple Retention (Count-Based)

```bash
# Retention policy mode
RETENTION_POLICY=simple            # simple | gfs

# Keep N most recent backups
MAX_LOCAL_BACKUPS=15               # Primary storage
MAX_SECONDARY_BACKUPS=15           # Secondary storage
MAX_CLOUD_BACKUPS=15               # Cloud storage
```

**Behavior**:
- Keeps N most recent backups
- Deletes all older backups
- Simple, predictable
- Good for frequent backups with limited storage

**Example**: With `MAX_LOCAL_BACKUPS=30` and daily backups, keeps last 30 days.

### 2. GFS Retention (Grandfather-Father-Son)

```bash
# Retention policy mode
RETENTION_POLICY=gfs               # Activates GFS mode

# GFS tiers
RETENTION_DAILY=7                  # Keep last 7 daily backups (minimum accepted is 1; 0 treated as 1)
RETENTION_WEEKLY=4                 # Keep 4 weekly backups (1 per ISO week)
RETENTION_MONTHLY=12               # Keep 12 monthly backups (1 per month)
RETENTION_YEARLY=3                 # Keep 3 yearly backups (1 per year)
```

### GFS Algorithm

| Tier | Selection Criteria | Example (7/4/12/3) |
|------|-------------------|-------------------|
| Daily | Most recent N backups | Last 7 backups (2025-11-17, 11-16, ..., 11-11) |
| Weekly | 1 per ISO week, excluding daily | Weeks 46, 45, 44, 43 (1 backup per week) |
| Monthly | 1 per month, excluding daily/weekly | Nov 2025, Oct 2025, ..., Dec 2024 |
| Yearly | 1 per year, excluding daily/weekly/monthly | 2025, 2024, 2023 |

### Benefits

- Better historical coverage than simple count
- Automatic time distribution
- ISO 8601 week numbering (standard)
- Efficient storage (fewer total backups)

### Example Output

```
GFS classification → daily: 7/7, weekly: 4/4, monthly: 12/12, yearly: 2/3, to_delete: 15
Deleting old backup: pbs-backup-20220115-120000.tar.xz (created: 2022-01-15 12:00:00)
Cloud storage retention applied: deleted 15 backups (logs deleted: 15), 26 backups remaining
```

### Storage Comparison

- **Simple**: `MAX_CLOUD_BACKUPS=1095` for 3 years daily = 1095 backups
- **GFS**: `DAILY=7, WEEKLY=4, MONTHLY=12, YEARLY=3` = ~26 backups (97% storage reduction!)

---

## Encryption & Bundling

```bash
# Bundle associated files into single .tar
BUNDLE_ASSOCIATED_FILES=true       # true | false

# Encrypt archive with AGE
ENCRYPT_ARCHIVE=false              # true | false

# AGE public key recipient (inline)
AGE_RECIPIENT=                     # e.g., "age1..."

# AGE recipient file path
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt
```

### Bundle Format

**Format**: `<name>.tar.<algo>.age.bundle.tar`

**Contents**:
- Main archive (`.tar.xz.age`)
- Checksum (`.sha256`)
- Metadata (`.metadata`)

### Encryption

- Uses AGE (age-encryption.org)
- Streaming encryption (no plaintext on disk)
- Supports multiple recipients
- Passphrase or key-based

**See** [docs/ENCRYPTION.md](ENCRYPTION.md) **for complete workflow.**

---

## Notifications

### Telegram

```bash
# Enable Telegram notifications
TELEGRAM_ENABLED=false             # true | false

# Bot type
BOT_TELEGRAM_TYPE=centralized      # centralized | personal

# Personal mode settings
TELEGRAM_BOT_TOKEN=                # Bot token (from @BotFather)
TELEGRAM_CHAT_ID=                  # Chat ID (your user ID or group ID)
```

**Bot types**:
- **centralized**: Uses organization-wide bot (configured server-side)
- **personal**: Uses your own bot (requires `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`)

**Setup personal bot**:
1. Message @BotFather on Telegram: `/newbot`
2. Copy token to `TELEGRAM_BOT_TOKEN`
3. Message @userinfobot: `/start` (get your chat ID)
4. Copy ID to `TELEGRAM_CHAT_ID`

### Email

```bash
# Enable email notifications
EMAIL_ENABLED=false                # true | false

# Delivery method
EMAIL_DELIVERY_METHOD=relay        # relay | sendmail | pmf

# Fallback to pmf (proxmox-mail-forward) if relay fails
EMAIL_FALLBACK_SENDMAIL=true       # true | false

# Recipient
# - relay/sendmail: required (empty = auto-detect from Proxmox root@pam)
# - pmf: optional (used only for the To: header)
EMAIL_RECIPIENT=                   # e.g., "admin@example.com"

# From address (used by sendmail/pmf; relay may ignore/override it)
EMAIL_FROM=no-reply@proxmox.tis24.it
```

**Delivery methods**:
- **relay**: Uses cloud relay (outbound HTTPS)
- **sendmail**: Uses `/usr/sbin/sendmail` (requires a working local MTA, e.g. postfix)
- **pmf**: Uses Proxmox Notifications via `proxmox-mail-forward`

**Notes**:
- Allowed values for `EMAIL_DELIVERY_METHOD` are: `relay`, `sendmail`, `pmf` (invalid values will skip Email with a warning).
- `EMAIL_FALLBACK_SENDMAIL` is a historical name (kept for compatibility). When `EMAIL_DELIVERY_METHOD=relay`, it enables fallback to **pmf** (it will not fall back to `/usr/sbin/sendmail`).
- `relay` requires a real mailbox recipient and blocks `root@…` recipients; set `EMAIL_RECIPIENT` to a non-root mailbox if needed.
- `sendmail` requires a recipient and uses `/usr/sbin/sendmail`; ProxSave can auto-detect `root@pam` email from Proxmox if `EMAIL_RECIPIENT` is empty.
- With `pmf`, final delivery recipients are determined by Proxmox Notifications targets/matchers. `EMAIL_RECIPIENT` is only used for the `To:` header and may be empty.

### Gotify

```bash
# Enable Gotify notifications
GOTIFY_ENABLED=false               # true | false

# Gotify server URL
GOTIFY_SERVER_URL=                 # e.g., "https://gotify.example.com"

# Application token
GOTIFY_TOKEN=                      # From Gotify Apps page

# Priority levels
GOTIFY_PRIORITY_SUCCESS=2          # Success notifications
GOTIFY_PRIORITY_WARNING=5          # Warning notifications
GOTIFY_PRIORITY_FAILURE=8          # Failure notifications
```

**Setup**:
1. Install Gotify server (https://gotify.net)
2. Create application in Gotify
3. Copy app token to `GOTIFY_TOKEN`

### Webhook

```bash
# Enable webhook notifications
WEBHOOK_ENABLED=false              # true | false

# Comma-separated endpoint names
WEBHOOK_ENDPOINTS=                 # e.g., "discord_alerts,teams_ops"

# Default payload format
WEBHOOK_FORMAT=generic             # discord | slack | teams | generic

# Request timeout (seconds)
WEBHOOK_TIMEOUT=30

# Retry configuration
WEBHOOK_MAX_RETRIES=3
WEBHOOK_RETRY_DELAY=2              # Seconds between retries
```

**Per-endpoint configuration** (example for endpoint named `discord_alerts`):

```bash
# URL
WEBHOOK_DISCORD_ALERTS_URL=https://discord.com/api/webhooks/XXXX/YYY

# Payload format
WEBHOOK_DISCORD_ALERTS_FORMAT=discord  # discord | slack | teams | generic

# HTTP method
WEBHOOK_DISCORD_ALERTS_METHOD=POST     # POST | GET | HEAD

# Custom headers (comma-separated)
WEBHOOK_DISCORD_ALERTS_HEADERS="X-Custom-Token:abc123,X-Another:value"

# Authentication type
WEBHOOK_DISCORD_ALERTS_AUTH_TYPE=none  # none | bearer | basic | hmac

# Authentication credentials
WEBHOOK_DISCORD_ALERTS_AUTH_TOKEN=     # Bearer token
WEBHOOK_DISCORD_ALERTS_AUTH_USER=      # Basic auth username
WEBHOOK_DISCORD_ALERTS_AUTH_PASS=      # Basic auth password
WEBHOOK_DISCORD_ALERTS_AUTH_SECRET=    # HMAC secret key
```

**Supported formats**:
- **discord**: Discord webhook JSON format
- **slack**: Slack incoming webhook format
- **teams**: Microsoft Teams connector format
- **generic**: Simple JSON `{"status": "...", "message": "..."}`

---

## Metrics - Prometheus

```bash
# Enable Prometheus metrics export
METRICS_ENABLED=false              # true | false

# Metrics export path (textfile collector format)
METRICS_PATH=${BASE_DIR}/metrics   # Empty = /var/lib/prometheus/node-exporter
```

> ℹ️ Metrics export is available only for the Go pipeline (`ENABLE_GO_BACKUP=true`).

**Output**: Creates `proxmox_backup.prom` in `METRICS_PATH` with:
- Backup duration and start/end timestamps
- Archive size and raw bytes collected
- Files collected/failed and success/failure status
- Storage usage counters per location (local/secondary/cloud)

**Integration**: Point Prometheus node_exporter to `METRICS_PATH`.

---

## Collector Options

### PVE-Specific

```bash
# Cluster configuration
BACKUP_CLUSTER_CONFIG=true         # /etc/pve/cluster files

# PVE firewall rules
BACKUP_PVE_FIREWALL=true           # PVE firewall configuration

# vzdump configuration
BACKUP_VZDUMP_CONFIG=true          # /etc/vzdump.conf

# Access control lists
BACKUP_PVE_ACL=true                # User permissions

# Scheduled jobs
BACKUP_PVE_JOBS=true               # Backup jobs configuration
BACKUP_PVE_SCHEDULES=true          # Cron schedules

# Replication
BACKUP_PVE_REPLICATION=true        # VM/CT replication config

# PVE backup files
BACKUP_PVE_BACKUP_FILES=true       # Include backup files from /var/lib/vz/dump
BACKUP_SMALL_PVE_BACKUPS=false     # Include small backups only
MAX_PVE_BACKUP_SIZE=100M           # Max size for "small" backups
PVE_BACKUP_INCLUDE_PATTERN=        # Glob patterns to include

# Ceph configuration
BACKUP_CEPH_CONFIG=false           # Ceph cluster config
CEPH_CONFIG_PATH=/etc/ceph         # Ceph config directory

# VM/CT configurations
BACKUP_VM_CONFIGS=true             # VM/CT config files
```

### PBS-Specific

```bash
# PBS datastore configs
BACKUP_DATASTORE_CONFIGS=true      # Datastore definitions

# User and permissions
BACKUP_USER_CONFIGS=true           # PBS users and tokens

# Remote configurations
BACKUP_REMOTE_CONFIGS=true         # Remote PBS servers

# Sync jobs
BACKUP_SYNC_JOBS=true              # Datastore sync jobs

# Verification jobs
BACKUP_VERIFICATION_JOBS=true      # Backup verification schedules

# Tape backup
BACKUP_TAPE_CONFIGS=true           # Tape library configuration

# Prune schedules
BACKUP_PRUNE_SCHEDULES=true        # Retention prune schedules

# PXAR metadata scanning
PXAR_SCAN_ENABLE=false             # Enable PXAR file metadata collection
PXAR_SCAN_DS_CONCURRENCY=3         # Datastores scanned in parallel
PXAR_SCAN_INTRA_CONCURRENCY=4      # Workers per datastore
PXAR_SCAN_FANOUT_LEVEL=2           # Directory depth for fan-out
PXAR_SCAN_MAX_ROOTS=2048           # Max worker roots per datastore
PXAR_STOP_ON_CAP=false             # Stop enumeration at max roots
PXAR_ENUM_READDIR_WORKERS=4        # Parallel ReadDir workers
PXAR_ENUM_BUDGET_MS=0              # Time budget for enumeration (0=disabled)
PXAR_FILE_INCLUDE_PATTERN=         # Include patterns (default: *.pxar, catalog.pxar*)
PXAR_FILE_EXCLUDE_PATTERN=         # Exclude patterns (e.g., *.tmp, *.lock)
```

**PXAR scanning**: Collects metadata from Proxmox Backup Server .pxar archives.

### Override Collection Paths

```bash
# PVE paths
PVE_CONFIG_PATH=/etc/pve
PVE_CLUSTER_PATH=/var/lib/pve-cluster
COROSYNC_CONFIG_PATH=${PVE_CONFIG_PATH}/corosync.conf
VZDUMP_CONFIG_PATH=/etc/vzdump.conf

# PBS datastore paths (comma/space separated)
PBS_DATASTORE_PATH=                # e.g., "/mnt/pbs1,/mnt/pbs2"

# System root override (testing/chroot)
SYSTEM_ROOT_PREFIX=                # Optional alternate root for system collection. Empty or "/" = real root.
# Use this to point the collector at a chroot/test fixture without touching the host FS.
```

**Use case**: Working with mounted snapshots or mirrors at non-standard paths.

### System Collectors

```bash
# Network configuration
BACKUP_NETWORK_CONFIGS=true        # /etc/network/interfaces, /etc/hosts
# Also captures /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg
# and /etc/dnsmasq.d/lxc-vmbr1.conf for LXC bridge overrides

# APT sources
BACKUP_APT_SOURCES=true            # /etc/apt/sources.list*

# Cron jobs
BACKUP_CRON_JOBS=true              # /etc/crontab, /etc/cron.*

# Systemd services
BACKUP_SYSTEMD_SERVICES=true       # /etc/systemd/system

# SSL certificates
BACKUP_SSL_CERTS=true              # /etc/ssl/certs, /etc/pve/local/pve-ssl.*

# Sysctl configuration
BACKUP_SYSCTL_CONFIG=true          # /etc/sysctl.conf, /etc/sysctl.d/

# Kernel modules
BACKUP_KERNEL_MODULES=true         # /etc/modules, /etc/modprobe.d/

# Firewall rules
BACKUP_FIREWALL_RULES=false        # iptables, nftables

# Installed packages
BACKUP_INSTALLED_PACKAGES=true     # dpkg -l, apt-mark showmanual

# Custom script directory
BACKUP_SCRIPT_DIR=true             # /opt/proxsave directory

# Critical system files
BACKUP_CRITICAL_FILES=true         # /etc/fstab, /etc/hostname, /etc/resolv.conf

# SSH keys
BACKUP_SSH_KEYS=true               # /root/.ssh, /etc/ssh

# ZFS configuration
BACKUP_ZFS_CONFIG=true             # /etc/zfs, /etc/hostid, zpool cache & properties

# Root home directory
BACKUP_ROOT_HOME=true              # /root (excluding .cache, .local/share/Trash)

# Backup script repository
BACKUP_SCRIPT_REPOSITORY=false     # Include .git directory

# Backup configuration file
BACKUP_CONFIG_FILE=true            # Include this backup.env configuration file in the backup
```

**Note**: `BACKUP_CONFIG_FILE=true` automatically includes the `configs/backup.env` file in the backup archive. This is highly recommended for disaster recovery, as it allows you to restore your exact backup configuration along with the system files. If you have sensitive credentials in `backup.env`, ensure your backups are encrypted (`ENCRYPT_ARCHIVE=true`).

---

## Custom Paths & Blacklist

```bash
# Custom paths to include (one per line)
CUSTOM_BACKUP_PATHS="
# /root/.config/rclone/rclone.conf
# /srv/custom-config.yaml
# /etc/custom/tool.conf
"

# Paths to exclude (one per line)
BACKUP_BLACKLIST="
# /root/.cache
# /root/*_tmp
"
```

**Format**: Bash-style heredoc, one path per line, `#` for comments.

---

## Related Documentation

- [README.md](../README.md) - Main documentation
- [CLOUD_STORAGE.md](CLOUD_STORAGE.md) - Complete rclone setup guide
- [ENCRYPTION.md](ENCRYPTION.md) - AGE encryption workflow
- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Command-line reference
- [EXAMPLES.md](EXAMPLES.md) - Practical configuration examples
