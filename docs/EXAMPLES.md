# Practical Examples

Real-world configuration examples for Proxsave covering common deployment scenarios.

## Table of Contents

- [Overview](#overview)
- [Example 1: Basic Local Backup](#example-1-basic-local-backup)
- [Example 2: Local + Secondary Storage](#example-2-local--secondary-storage)
- [Example 3: Cloud Backup with Google Drive](#example-3-cloud-backup-with-google-drive)
- [Example 4: Encrypted Backup with AGE](#example-4-encrypted-backup-with-age)
- [Example 5: Backblaze B2 with Bandwidth Limiting](#example-5-backblaze-b2-with-bandwidth-limiting)
- [Example 6: MinIO Self-Hosted with High Performance](#example-6-minio-self-hosted-with-high-performance)
- [Example 7: Multi-Notification Setup](#example-7-multi-notification-setup)
- [Example 8: Complete Production Setup](#example-8-complete-production-setup)
- [Example 9: Test in a Chroot/Fixture](#example-9-test-in-a-chrootfixture)
- [Related Documentation](#related-documentation)

---

## Overview

This guide provides complete, copy-paste ready configuration examples for common deployment scenarios. Each example includes:

- **Scenario description**: What problem it solves
- **Complete configuration**: Ready-to-use `backup.env` settings
- **Step-by-step setup**: Commands to execute
- **Cron scheduling**: Automation examples
- **Expected results**: What to expect after setup

**How to use these examples**:
1. Choose the example closest to your use case
2. Copy the configuration to `configs/backup.env`
3. Adjust paths, credentials, and parameters for your environment
4. Test with `--dry-run` before production use
5. Schedule via cron or systemd

---

## Example 1: Basic Local Backup

**Scenario**: Single server, local backup only, simple retention.

**Use case**:
- Standalone Proxmox VE or PBS server
- No cloud storage requirements
- Simple daily backups
- Fast local SSD/NVMe storage

### Configuration

```bash
# configs/backup.env
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxsave/backup
LOG_PATH=/opt/proxsave/log

# Compression
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
COMPRESSION_MODE=standard

# Retention: Keep 15 backups
MAX_LOCAL_BACKUPS=15

# Run backup
./build/proxsave
```

### Setup Steps

```bash
# 1. Install
./build/proxsave --install
# (use --new-install to wipe everything except build/, env/, and identity/ before installing)

# 2. Edit configuration
nano configs/backup.env
# (paste configuration above)

# 3. Test
./build/proxsave --dry-run

# 4. Run first backup
./build/proxsave
```

### Cron Schedule

```bash
# Daily backup at 2 AM
crontab -e

0 2 * * * /opt/proxsave/build/proxsave >> /var/log/pbs-backup.log 2>&1
```

### Expected Results

```
Backup directory: /opt/proxsave/backup/
- <hostname>-backup-20240115-020000.tar.xz.bundle.tar
```

If you disable bundling (`BUNDLE_ASSOCIATED_FILES=false`), proxsave keeps the raw archive plus sidecar files (`.sha256`, `.metadata`, `.manifest.json`).

**Retention**: Automatically keeps latest 15 backups, deletes older ones.

**Backup size**: Typically 500MB-2GB depending on system configuration.

---

## Example 2: Local + Secondary Storage

**Scenario**: Local SSD + secondary NAS, different retention.

**Use case**:
- Local SSD for fast backup creation
- NAS/network storage for longer retention
- Different retention policies per tier
- Cost optimization (expensive SSD, cheap NAS)

### Configuration

```bash
# configs/backup.env
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxsave/backup
LOG_PATH=/opt/proxsave/log

# Secondary storage (NAS)
SECONDARY_ENABLED=true
SECONDARY_PATH=/mnt/nas/pbs-backup
SECONDARY_LOG_PATH=/mnt/nas/pbs-log

# Retention
MAX_LOCAL_BACKUPS=7        # 1 week local (SSD expensive)
MAX_SECONDARY_BACKUPS=30   # 1 month secondary (NAS cheap)

# Run backup
./build/proxsave
```

### Setup Steps

```bash
# 1. Mount NAS (example with NFS)
sudo mkdir -p /mnt/nas
sudo mount -t nfs 192.168.1.100:/backup /mnt/nas

# Make mount persistent
echo "192.168.1.100:/backup /mnt/nas nfs defaults 0 0" | sudo tee -a /etc/fstab

# 2. Create backup directories
mkdir -p /mnt/nas/pbs-backup /mnt/nas/pbs-log
chmod 700 /mnt/nas/pbs-backup /mnt/nas/pbs-log

# 3. Test write access
touch /mnt/nas/pbs-backup/test.txt && rm /mnt/nas/pbs-backup/test.txt

# 4. Configure and run
./build/proxsave --install
# (use --new-install if you want to reset the install dir first, keeping env/identity)
# (paste configuration above)
./build/proxsave --dry-run
./build/proxsave
```

### Expected Results

```
Local (SSD):
/opt/proxsave/backup/
- 7 backups retained (latest week)

Secondary (NAS):
/mnt/nas/pbs-backup/
- 30 backups retained (latest month)
```

**Cost benefit**: Keep only 1 week locally (saves SSD space), 1 month on cheap NAS storage.

---

## Example 3: Cloud Backup with Google Drive

**Scenario**: Small business, daily backups, GFS retention, Google Drive.

**Use case**:
- Small/medium business
- 15GB free Google Drive tier
- GFS retention for compliance
- Daily automated backups

### Setup Steps

#### Step 1: Configure rclone

```bash
rclone config
# n > gdrive > drive > [OAuth] > y > q
rclone mkdir gdrive:pbs-backups
rclone mkdir gdrive:pbs-logs
```

#### Step 2: Configure backup.env

```bash
# configs/backup.env

# Cloud storage
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=/pbs-backups
CLOUD_LOG_PATH=/pbs-logs
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=3
CLOUD_PARALLEL_VERIFICATION=true

# Google Drive tuning
RCLONE_TIMEOUT_CONNECTION=60
RCLONE_TIMEOUT_OPERATION=600
RCLONE_TRANSFERS=4
RCLONE_RETRIES=3
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=2

# GFS retention (3-year coverage)
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=12
RETENTION_YEARLY=3
```

#### Step 3: Test and Run

```bash
# Dry-run
./build/proxsave --dry-run

# Real backup
./build/proxsave

# Verify
rclone ls gdrive:pbs-backups/
rclone ls gdrive:/pbs-logs/
```

### Expected Results

**GFS retention distribution**:
- 7 daily backups (last week)
- 4 weekly backups (last month)
- 12 monthly backups (last year)
- 3 yearly backups

**Total**: ~26 backups across 3 years

**Storage calculation** (example):
- Backup size: 500 MB
- Total: 26 × 500 MB = 13 GB (fits in 15GB free tier)

---

## Example 4: Encrypted Backup with AGE

**Scenario**: Sensitive data, encryption required, cloud storage.

**Use case**:
- Sensitive personal/business data
- Compliance requirements (GDPR, HIPAA)
- Cloud storage with zero-trust model
- Secure key management

### Setup Steps

#### Step 1: Generate Encryption Key

```bash
./build/proxsave --newkey

# Wizard:
# [2] Generate from personal passphrase
# Enter passphrase: **************** (min 12 chars, strong)
# Confirm: ****************
# ✓ AGE recipient generated and saved
```

#### Step 2: Configure backup.env

```bash
# configs/backup.env

# Encryption
ENCRYPT_ARCHIVE=true
AGE_RECIPIENT_FILE=/opt/proxsave/identity/age/recipient.txt

# Bundle (recommended with encryption)
BUNDLE_ASSOCIATED_FILES=true

# Cloud storage
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=/pbs-encrypted
MAX_CLOUD_BACKUPS=30
```

#### Step 3: Run Backup

```bash
./build/proxsave

# Result (with bundling enabled): pve01-backup-20240115-020000.tar.xz.age.bundle.tar
```

#### Step 4: Decrypt (when needed)

```bash
./build/proxsave --decrypt

# Select backup: [1]
# Destination: /tmp/decrypt
# Enter passphrase: ****************
# ✓ Decryption successful
```

### Expected Results

**Encrypted backup**:
```
pve01-backup-20240115-020000.tar.xz.age.bundle.tar     # Encrypted bundle (archive + metadata + checksum)
```

**Security features**:
- ✅ Streaming encryption (no plaintext on disk)
- ✅ ChaCha20-Poly1305 AEAD
- ✅ Passphrase-based or key-based
- ✅ Compatible with standard AGE tools

---

## Example 5: Backblaze B2 with Bandwidth Limiting

**Scenario**: Remote archival, slow network, cost optimization.

**Use case**:
- Long-term archival (7-year retention)
- Slow office network (don't saturate during work hours)
- Cost-effective cloud storage
- Nighttime backup window

### Setup Steps

#### Step 1: Configure Backblaze B2

```bash
# Create B2 account (10GB free)
# Create bucket: pbs-backups (Private)
# Create Application Key (copy Key ID and Application Key)

rclone config
# n > b2 > b2 > <Key-ID> > <App-Key> > n > n > y > q
```

#### Step 2: Configure backup.env

```bash
# configs/backup.env

# Cloud storage
CLOUD_ENABLED=true
CLOUD_REMOTE=b2
CLOUD_REMOTE_PATH=/pbs-backups
CLOUD_LOG_PATH=/pbs-backups/logs
CLOUD_UPLOAD_MODE=sequential

# Slow network tuning
RCLONE_TIMEOUT_CONNECTION=45
RCLONE_TIMEOUT_OPERATION=1800     # 30 minutes
RCLONE_BANDWIDTH_LIMIT=5M         # 5 MB/s (don't saturate office network)
RCLONE_TRANSFERS=2
RCLONE_RETRIES=5

# Batch deletion (B2 rate limiting)
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=2

# GFS retention (long-term archival)
RETENTION_POLICY=gfs
RETENTION_DAILY=14
RETENTION_WEEKLY=8
RETENTION_MONTHLY=24
RETENTION_YEARLY=5

# Result: ~51 backups distributed over 5 years
# Cost: 51 × 0.5GB = 25.5GB × $0.005 = $0.13/month
```

#### Step 3: Schedule Nightly Backup

```bash
# Cron: 2 AM daily
crontab -e
0 2 * * * /opt/proxsave/build/proxsave
```

**Why nightly?**: Slow upload doesn't impact office hours, B2 free egress 1GB/day.

### Expected Results

**Cost calculation** (example):
- Backup size: 500 MB
- GFS retention: 51 backups
- Total storage: 25.5 GB
- B2 cost: $0.005/GB/month = **$0.13/month**

**Upload time** (example):
- Backup size: 500 MB
- Bandwidth: 5 MB/s
- Upload time: ~100 seconds (1.5 minutes)

---

## Example 6: MinIO Self-Hosted with High Performance

**Scenario**: LAN-based MinIO server, fast storage, hourly backups.

**Use case**:
- Self-hosted S3-compatible storage (MinIO)
- Local network (gigabit LAN)
- Hourly backup frequency
- High-performance requirements

### Setup Steps

#### Step 1: Configure MinIO

```bash
# Assuming MinIO running at https://minio.local:9000
# Create bucket via MinIO Console or mc client
mc mb minio-local/pbs-backups
mc mb minio-local/pbs-logs

rclone config
# n > minio > s3 > Minio > minioadmin > minioadmin > (empty region) > https://minio.local:9000 > y > q
```

#### Step 2: Configure backup.env

```bash
# configs/backup.env

# Cloud storage (MinIO LAN)
CLOUD_ENABLED=true
CLOUD_REMOTE=minio
CLOUD_REMOTE_PATH=/pbs-backups/server1    # Organize by server
CLOUD_LOG_PATH=/pbs-logs
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=4
CLOUD_WRITE_HEALTHCHECK=true       # Test write access

# LAN performance tuning
RCLONE_TIMEOUT_CONNECTION=10
RCLONE_TIMEOUT_OPERATION=300
RCLONE_BANDWIDTH_LIMIT=            # Unlimited (LAN)
RCLONE_TRANSFERS=8                 # Highly parallel
RCLONE_RETRIES=2

# Batch deletion (no API limits)
CLOUD_BATCH_SIZE=100
CLOUD_BATCH_PAUSE=0

# Simple retention (168 hours = 1 week)
MAX_CLOUD_BACKUPS=168
```

#### Step 3: Hourly Backup

```bash
# Cron: every hour
crontab -e
0 * * * * /opt/proxsave/build/proxsave
```

### Expected Results

**Performance**:
- Backup creation: 2-3 minutes
- Upload to MinIO (LAN): 10-30 seconds
- Total time: ~3-4 minutes

**Retention**:
- 168 hourly backups (1 week)
- Automatic hourly rotation

**Advantages**:
- ✅ Full control over data
- ✅ No cloud costs
- ✅ LAN speed (gigabit)
- ✅ S3-compatible API

---

## Example 7: Multi-Notification Setup

**Scenario**: Telegram + Email + Webhook (Discord) notifications.

**Use case**:
- Multiple notification channels
- Team awareness
- Critical alerts via multiple paths
- Redundant notification delivery

### Configuration

```bash
# configs/backup.env

# Telegram
TELEGRAM_ENABLED=true
BOT_TELEGRAM_TYPE=personal
TELEGRAM_BOT_TOKEN=123456789:ABCdefGHIjklMNOpqrsTUVwxyz
TELEGRAM_CHAT_ID=987654321

# Email
EMAIL_ENABLED=true
EMAIL_DELIVERY_METHOD=relay
EMAIL_RECIPIENT=admin@example.com
EMAIL_FROM=noreply@proxmox.example.com

# Webhook (Discord)
WEBHOOK_ENABLED=true
WEBHOOK_ENDPOINTS=discord_alerts
WEBHOOK_DISCORD_ALERTS_URL=https://discord.com/api/webhooks/XXXX/YYYY
WEBHOOK_DISCORD_ALERTS_FORMAT=discord
WEBHOOK_DISCORD_ALERTS_METHOD=POST

# Run backup
./build/proxsave
# Result: Notifications sent to Telegram, Email, and Discord
```

### Setup Steps

#### 1. Telegram Bot Setup

```bash
# Create bot via @BotFather on Telegram
# Send message: /newbot
# Get token: 123456789:ABC...

# Get chat ID:
# Send message to bot
# Visit: https://api.telegram.org/bot<TOKEN>/getUpdates
# Copy chat.id value
```

#### 2. Email Configuration

```bash
# Option A: Cloud relay (outbound HTTPS)
# - Set EMAIL_DELIVERY_METHOD=relay and configure EMAIL_RECIPIENT (or leave empty for root@pam auto-detect)
# - Relay blocks root@… recipients; use a real non-root mailbox for EMAIL_RECIPIENT
# - No local SMTP/MTA setup required on the node
# - Optional: set EMAIL_FALLBACK_SENDMAIL=true to fall back to EMAIL_DELIVERY_METHOD=pmf when the relay fails

# Option B: Local sendmail (/usr/sbin/sendmail)
# - Set EMAIL_DELIVERY_METHOD=sendmail
# - Requires a working local MTA (e.g. postfix) on the node
# - EMAIL_RECIPIENT is required (or auto-detected from Proxmox root@pam if configured)

# Option C: Proxmox Notifications via proxmox-mail-forward
# - Set EMAIL_DELIVERY_METHOD=pmf
# - Ensure Proxmox Notifications targets/matchers are configured
# - EMAIL_RECIPIENT is optional (only used for the To: header)
# - Optional quick check (runs the forwarder directly; run as root):
printf "To: root\nSubject: proxsave test\n\nHello from proxsave\n" | sudo /usr/libexec/proxmox-mail-forward
```

#### 3. Discord Webhook

```bash
# In Discord:
# Server Settings > Integrations > Webhooks > New Webhook
# Copy webhook URL
```

### Expected Results

**On successful backup**:
- ✅ Telegram message with summary
- ✅ Email with detailed report
- ✅ Discord embed with stats

**On failure**:
- ❌ Telegram alert with error
- ❌ Email with failure details
- ❌ Discord mention with logs

---

## Example 8: Complete Production Setup

**Scenario**: Enterprise setup with all features enabled.

**Use case**:
- Production environment
- Maximum reliability
- All collectors enabled
- Multi-tier storage
- Encryption
- Notifications
- Metrics export

### Configuration

```bash
# configs/backup.env

# General
BACKUP_ENABLED=true
USE_COLOR=true
DEBUG_LEVEL=standard

# Security
SECURITY_CHECK_ENABLED=true
AUTO_UPDATE_HASHES=true
AUTO_FIX_PERMISSIONS=true
CONTINUE_ON_SECURITY_ISSUES=false

# Compression (balanced)
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
COMPRESSION_MODE=standard
COMPRESSION_THREADS=0

# Primary storage
BACKUP_PATH=/opt/proxsave/backup
LOG_PATH=/opt/proxsave/log

# Secondary storage (NAS)
SECONDARY_ENABLED=true
SECONDARY_PATH=/mnt/nas/pbs-backup
SECONDARY_LOG_PATH=/mnt/nas/pbs-log

# Cloud storage (S3)
CLOUD_ENABLED=true
CLOUD_REMOTE=s3
CLOUD_REMOTE_PATH=/company-backups/datacenter1/pbs1
CLOUD_LOG_PATH=/company-backups/logs
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=4
RCLONE_TRANSFERS=8
RCLONE_RETRIES=3

# GFS retention (7-year compliance)
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=8
RETENTION_MONTHLY=24
RETENTION_YEARLY=7

# Encryption
ENCRYPT_ARCHIVE=true
BUNDLE_ASSOCIATED_FILES=true
AGE_RECIPIENT_FILE=/opt/proxsave/identity/age/recipient.txt

# Notifications
TELEGRAM_ENABLED=true
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...
EMAIL_ENABLED=true
EMAIL_RECIPIENT=ops@example.com
GOTIFY_ENABLED=true
GOTIFY_SERVER_URL=https://gotify.example.com
GOTIFY_TOKEN=...

# Metrics
METRICS_ENABLED=true
METRICS_PATH=/var/lib/prometheus/node-exporter

# Collectors (all enabled)
BACKUP_CLUSTER_CONFIG=true
BACKUP_PVE_FIREWALL=true
BACKUP_DATASTORE_CONFIGS=true
BACKUP_USER_CONFIGS=true
BACKUP_NETWORK_CONFIGS=true
BACKUP_APT_SOURCES=true
BACKUP_CRON_JOBS=true
BACKUP_SYSTEMD_SERVICES=true
BACKUP_SSL_CERTS=true
BACKUP_CRITICAL_FILES=true
BACKUP_SSH_KEYS=true
BACKUP_ZFS_CONFIG=true
BACKUP_ROOT_HOME=true

# Custom paths
CUSTOM_BACKUP_PATHS="
/root/.config/rclone/rclone.conf
/opt/proxsave/configs/backup.env
/etc/custom/app.conf
"

# Run backup
./build/proxsave
```

### Cron Schedule

```bash
# Daily backup at 2 AM
crontab -e
0 2 * * * /opt/proxsave/build/proxsave >> /var/log/pbs-backup-cron.log 2>&1
```

### Expected Results

**Storage distribution**:
- ✅ Encrypted backup on local SSD
- ✅ Copy to secondary NAS
- ✅ Upload to S3 cloud
- ✅ GFS retention (7-year compliance)

**Notifications**:
- ✅ Telegram alert
- ✅ Email report
- ✅ Gotify push notification

**Metrics**:
- ✅ Prometheus metrics exported
- ✅ Grafana dashboard integration

**Backup includes**:
- PVE cluster config
- PBS datastores
- Network configs
- SSL certificates
- SSH keys
- ZFS configuration
- Custom application configs

---

## Related Documentation

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone setup for all examples
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption details

### Operations
- **[CLI Reference](CLI_REFERENCE.md)** - All command-line flags
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common issues and solutions

### Reference
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore from any example backup
- **[Migration Guide](MIGRATION_GUIDE.md)** - Migrate from Bash version

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Quick Comparison Matrix

| Example | Local | NAS | Cloud | Encryption | GFS | Complexity |
|---------|-------|-----|-------|------------|-----|------------|
| **1. Basic Local** | ✅ | ❌ | ❌ | ❌ | ❌ | ⭐ Simple |
| **2. Local + NAS** | ✅ | ✅ | ❌ | ❌ | ❌ | ⭐⭐ Easy |
| **3. Google Drive** | ✅ | ❌ | ✅ | ❌ | ✅ | ⭐⭐ Easy |
| **4. Encrypted** | ✅ | ❌ | ✅ | ✅ | ❌ | ⭐⭐⭐ Moderate |
| **5. Backblaze B2** | ✅ | ❌ | ✅ | ❌ | ✅ | ⭐⭐⭐ Moderate |
| **6. MinIO** | ✅ | ❌ | ✅ | ❌ | ❌ | ⭐⭐⭐ Moderate |
| **7. Multi-Notify** | ✅ | ❌ | ❌ | ❌ | ❌ | ⭐⭐ Easy |
| **8. Production** | ✅ | ✅ | ✅ | ✅ | ✅ | ⭐⭐⭐⭐ Advanced |

---

## Customization Tips

### Mix and Match

All examples can be combined. For instance:

**Example: Local + NAS + Encrypted + Google Drive + GFS**:
```bash
# From Example 2: Local + NAS
SECONDARY_ENABLED=true
SECONDARY_PATH=/mnt/nas/pbs-backup

# From Example 4: Encryption
ENCRYPT_ARCHIVE=true
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt

# From Example 3: Google Drive + GFS
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=/pbs-backups
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=12
RETENTION_YEARLY=3
```

### Performance Tuning

Adjust compression for your use case:

**Fast (low CPU)**:
```bash
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
COMPRESSION_MODE=fast
```

**Balanced** (default):
```bash
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
COMPRESSION_MODE=standard
```

**Maximum compression** (high CPU):
```bash
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=9
COMPRESSION_MODE=slow
```

### Cost Optimization

**Minimize cloud costs**:
```bash
# Use GFS for efficient long-term retention
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=6
RETENTION_YEARLY=2

# Aggressive compression
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=9

# Disable log uploads
CLOUD_LOG_PATH=

# Result: ~19 backups instead of 365 (5% storage cost)
```

---

## Example 9: Test in a Chroot/Fixture

**Scenario**: Run collection against an alternate system root (chroot, mounted snapshot, test fixture) without touching the live filesystem.

**Use case**:
- CI/test backups in an isolated environment
- Offline analysis of a mounted image/snapshot
- Running inside a container that mounts a different root

### Configuration

```bash
# configs/backup.env
SYSTEM_ROOT_PREFIX=/mnt/snapshot-root   # points to the alternate root
BACKUP_ENABLED=true
ENABLE_GO_BACKUP=true
# /etc, /var, /root, /home are resolved under the prefix
```

### Setup Steps

```bash
# 1) Mount or prepare the alternate root
mount /dev/vg0/snap /mnt/snapshot-root   # example

# 2) Run a dry-run
SYSTEM_ROOT_PREFIX=/mnt/snapshot-root ./build/proxsave --dry-run

# 3) Run the actual backup (optional)
SYSTEM_ROOT_PREFIX=/mnt/snapshot-root ./build/proxsave
```

### Expected Results
- Collected files reflect the contents of `/mnt/snapshot-root/etc`, `/var`, `/root`, `/home`, etc.
- No writes to the node's live filesystem.

---

## Next Steps

1. **Choose an example** closest to your use case
2. **Copy configuration** to `configs/backup.env`
3. **Customize** paths, credentials, retention
4. **Test** with `--dry-run` mode
5. **Run** first backup manually
6. **Verify** results in storage locations
7. **Schedule** via cron or systemd
8. **Monitor** logs and notifications

**For detailed configuration options**, see: **[Configuration Guide](CONFIGURATION.md)**
