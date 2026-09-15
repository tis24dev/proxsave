# Practical Examples

Real-world configuration examples for ProxSave covering common deployment scenarios.

Everything below is reached the same way: run `proxsave` with no arguments on a terminal
and the interactive dashboard opens. That is where you install, edit the configuration,
run a backup, restore, upgrade, and manage the scheduler. The command-line flags shown
next to each example are the automation and recovery route, for cron jobs, scripts and
hosts where the dashboard cannot run.

## Table of Contents

- [Start with the dashboard](#start-with-the-dashboard)
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
- [HA-LXC Backup Appliance (HOST_BACKUP_MODE)](#ha-lxc-backup-appliance-host_backup_mode)
- [Example 10: Dual PVE+PBS Host](#example-10-dual-pvepbs-host)
- [Example 11: Resident daemon and healthchecks monitoring](#example-11-resident-daemon-and-healthchecks-monitoring)
- [Quick Comparison Matrix](#quick-comparison-matrix)
- [Customization Tips](#customization-tips)
- [Related Documentation](#related-documentation)

---

## Start with the dashboard

`proxsave`, typed bare on a terminal, opens the dashboard. It is a launcher: every row that
has a matching flag runs the same code as that flag, so nothing you do there behaves
differently from the command line. The rows marked `(dashboard only)` below have no flag.
The full walkthrough is in [DASHBOARD.md](DASHBOARD.md); this is the map from the tasks in
this guide to the rows that perform them.

| You want to | Dashboard route | Same thing from a script |
|---|---|---|
| Install or re-install | `Maintenance` -> `Install` -> `Edit install` | `proxsave --install` |
| Start over on a dirty install | `Maintenance` -> `Install` -> `Wipe install` | `proxsave --new-install` |
| Change the configuration | `Maintenance` -> `Install` -> `Edit install`, then `Edit existing` | edit `configs/backup.env` by hand |
| Run a backup now | `Backup` -> `Backup` | `proxsave --backup` |
| Restore a backup | `Tools` -> `Restore` | `proxsave --restore` |
| Decrypt an encrypted bundle | `Tools` -> `Decrypt` | `proxsave --decrypt` |
| Create the AGE encryption key | `Maintenance` -> `New key` | `proxsave --newkey` |
| Update the binary | `Maintenance` -> `Upgrade` -> `Check upgrade` | `proxsave --upgrade` |
| Add new template keys to `backup.env` | `Maintenance` -> `Upgrade` -> `Check config` | `proxsave --upgrade-config` |
| Switch the scheduler to the daemon | `Daemon` -> `Install` | `proxsave --daemon-setup` |
| Go back to cron | `Daemon` -> `Disable` | `proxsave --daemon-remove` |
| Check the scheduler state | `Daemon` -> `Status` | `proxsave --daemon-status` |
| Verify Telegram pairing | `Diagnostic Checks` -> `Telegram` | (dashboard only) |
| Verify backup monitoring | `Diagnostic Checks` -> `Healthchecks` | (dashboard only) |
| Re-run the post-install audit | `Diagnostic Checks` -> `Post-install` | (dashboard only) |
| Send a support report | `Recovery` -> `Support` | `proxsave --support` |
| Clear leftover restore mount guards | `Recovery` -> `Cleanup guards` | `proxsave --cleanup-guards` |

Two things to know before you rely on it:

- The dashboard opens only when `proxsave` is invoked completely bare and both stdin and
  stdout are real terminals. Any flag at all, `--config` included, skips it. Everything
  else, cron and the daemon included, runs the backup directly.
- It opens before the configuration is loaded, so it works on a host whose `backup.env`
  is missing or broken. That is the intended way to repair one.

### The configuration form

`Install` -> `Edit install` is the graphical editor for the settings most installs need.
Choose `Edit existing` when asked about the current file and the form is pre-filled from
it; `Overwrite` starts from the shipped template instead. The form covers secondary
storage, cloud storage, firewall-rule collection, Telegram, email and its delivery mode,
encryption, the scheduler engine, healthchecks monitoring, and the daily run time.
Everything else in `backup.env` is edited by hand; which block lives where is listed in
[CONFIGURATION.md](CONFIGURATION.md).

### Upgrading: which binary runs the upgrade

There are two upgrade routes and they are not equivalent.

**In place**, from the dashboard (`Maintenance` -> `Upgrade` -> `Check upgrade`) or with
`proxsave --upgrade`. Both run the same code in the binary already installed. That binary
owns the release check, the download, the signature and checksum verification, and the
replacement itself; the downloaded binary is untrusted until this one has verified it, so
it cannot be the party that verifies itself. The `backup.env` merge is then always run by
the freshly installed binary, and from release 0.36.0 onward the rest of the post-install
work (documentation refresh, symlinks, legacy cron repointing, daemon migration and
restart) is handed to it as well. On a host whose installed binary predates 0.36.0 there is
no such handover: the old release's finalize runs, so anything a newer release changed
there does not take effect on that upgrade.

**Externally fetched**, which downloads and swaps in the new binary first and only then
asks it to finish the job:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

Every step, the finalize included, is the new release's code. Use this route when the
installed binary is old, and when an in-place upgrade has misbehaved.

### When to reach for the flags instead

The flags stay fully supported, and they are the right tool in four cases: a headless or
serial-console host where no TTY is available, a cron job or a script, a run driven by
configuration management, and recovery on a host where the TUI will not start. Each
example below shows the flag form for exactly that reason.

---

## Overview

This guide provides complete, copy-paste ready configuration examples for common deployment scenarios. Each example includes:

- **Scenario description**: What problem it solves
- **Complete configuration**: Ready-to-use `backup.env` settings
- **Step-by-step setup**: The dashboard route, and the equivalent commands
- **Scheduling**: The daemon, or a cron entry where cron is the chosen engine
- **Expected results**: What to expect after setup

**How to use these examples**:
1. Choose the example closest to your use case
2. Open the dashboard (`proxsave`) and set what the form covers under `Install` -> `Edit install`
3. Put the remaining variables in `configs/backup.env`
4. Adjust paths, credentials, and parameters for your environment
5. Test with `DRY_RUN=true` or `proxsave --dry-run` before production use
6. Let the resident daemon schedule the run; it is already the engine on a fresh install,
   and `Daemon` -> `Install` switches a host that is still on cron

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
```

### Setup Steps

From the dashboard:

1. Run `proxsave` to open it.
2. `Maintenance` -> `Install` -> `Edit install`, and walk the configuration form.
3. Put `BACKUP_PATH`, `LOG_PATH`, the compression variables and `MAX_LOCAL_BACKUPS` in
   `configs/backup.env` by hand; the form does not cover them.
4. `Daemon` -> `Status` to confirm the resident service is the scheduler. A fresh install
   already selects it, so the group offers `Disable` and `Restart`; a host still on cron
   offers `Install` instead, and that is the row that switches it.
5. `Backup` -> `Backup` to run the first one now and watch the log stream on screen.

The same steps without a TTY:

```bash
# 1. Install
proxsave --install
# (use --new-install to wipe everything except build/, env/, and identity/ before installing)

# 2. Edit configuration
nano /opt/proxsave/configs/backup.env
# (paste configuration above)

# 3. Test
proxsave --dry-run

# 4. Run first backup
proxsave --backup
```

### Scheduling

A fresh install schedules the backup through the resident daemon
(`proxsave-daemon.service`), at `SCHEDULER_TIME`. Nothing more is needed. See
[Example 11](#example-11-resident-daemon-and-healthchecks-monitoring) and
[DAEMON.md](DAEMON.md).

Only if you have deliberately opted out of the daemon (`SCHEDULER_MODE=cron`) does a cron
entry apply. ProxSave writes that entry itself; hand-editing the crontab is the legacy
path:

```bash
# Daily backup at 2 AM, the canonical entry ProxSave writes
crontab -e

0 2 * * * /usr/local/bin/proxsave --backup >> /var/log/pbs-backup.log 2>&1
```

### Expected Results

```text
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
```

### Setup Steps

Mount the share first; ProxSave copies to a filesystem path and never speaks NFS or SMB
itself:

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
```

Then, in the dashboard: `Maintenance` -> `Install` -> `Edit install`, turn
**Secondary storage** on and fill in **Secondary backup path** and **Secondary log path**.
The form validates both before writing them. `MAX_LOCAL_BACKUPS` and
`MAX_SECONDARY_BACKUPS` are not on the form; set them in `configs/backup.env`. Finish with
`Backup` -> `Backup`.

The same without a TTY:

```bash
proxsave --install
# (use --new-install if you want to reset the install dir first, keeping build/, env/, and identity/)
# (paste configuration above)
proxsave --dry-run
proxsave --backup
```

### Expected Results

```text
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

#### Step 2: Configure the cloud target

In the dashboard, `Maintenance` -> `Install` -> `Edit install`, turn
**Cloud backups (rclone)** on and fill in **Rclone backup remote** and
**Rclone log remote**. Those three fields are all the form writes for cloud storage
(`CLOUD_ENABLED`, `CLOUD_REMOTE`, `CLOUD_LOG_PATH`). `CLOUD_REMOTE_PATH`, the upload mode,
the rclone tuning and the retention policy go in `configs/backup.env`:

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

Run the backup from the dashboard with `Backup` -> `Backup`; the log streams inside the
screen and `c` copies it. The scripted equivalent:

```bash
# Dry-run
proxsave --dry-run

# Real backup
proxsave --backup

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

In the dashboard: `Maintenance` -> `New key`. The wizard offers three sources for the AGE
recipient (an existing public key, a passphrase, or an existing private key). For the
passphrase route, enter a strong passphrase (minimum 12 characters, at least 3 character
classes) and confirm it; the recipient is generated and saved, and the passphrase itself is
not stored.

The same wizard from a script (a Charm TUI by default; add `--cli` for text prompts):

```bash
proxsave --newkey
```

#### Step 2: Configure backup.env

Turn **Backup encryption (AGE)** on in `Maintenance` -> `Install` -> `Edit install`; that
writes `ENCRYPT_ARCHIVE`. The recipient file path, bundling and the cloud target are
hand-edited:

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

`Backup` -> `Backup` in the dashboard, or `proxsave --backup` from a script.

```text
Result (with bundling enabled): pve01-backup-20240115-020000.tar.xz.age.bundle.tar
```

#### Step 4: Decrypt (when needed)

In the dashboard: `Tools` -> `Decrypt`. The flow asks for the backup to decrypt, then for
the AGE key or passphrase in a single secret field, then for the destination directory
(which defaults to the `decrypt/` folder under `BASE_DIR`). The decrypted archive is
written there as `<name>.decrypted.bundle.tar`.

The same flow from a script (a Charm TUI by default; add `--cli` for text prompts):

```bash
proxsave --decrypt
```

### Expected Results

**Encrypted backup**:
```text
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

Set the run time in the dashboard (`Maintenance` -> `Install` -> `Edit install`, the
**Run at (HH:MM)** field) and let the resident daemon own the schedule
(already the engine on a fresh install; `Daemon` -> `Install` switches a cron host). Both
write to `configs/backup.env`: `SCHEDULER_TIME=02:00` and
`SCHEDULER_MODE=daemon`.

Only on a host kept on cron does the crontab matter:

```bash
# Cron: 2 AM daily (SCHEDULER_MODE=cron only)
crontab -e
0 2 * * * /usr/local/bin/proxsave --backup
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

This is the one scenario in this guide that cron does better. The resident daemon runs the
backup once a day, at `SCHEDULER_TIME`; it has no sub-daily schedule. An hourly cadence
therefore means staying on cron: keep `SCHEDULER_MODE=cron` (or revert with
`Daemon` -> `Disable`) and write the entry yourself.

```bash
# Cron: every hour
crontab -e
0 * * * * /usr/local/bin/proxsave --backup
```

Note what that costs: the healthchecks monitoring described in
[Example 11](#example-11-resident-daemon-and-healthchecks-monitoring) only transmits under
the daemon, so an hourly cron host has no dead-man switch and no hang watchdog.

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

**Scenario**: Telegram + Email + Webhook (Discord + Pushover) notifications.

**Use case**:
- Multiple notification channels
- Team awareness
- Critical alerts via multiple paths
- Redundant notification delivery

### Configuration

The dashboard covers the two channels most hosts use: `Maintenance` -> `Install` ->
`Edit install` has a **Telegram notifications** toggle, an **Email notifications** toggle
and an **Email delivery mode** selector (relay, sendmail, pmf). Enabling Telegram there
also selects the centralized bot on a config that has no `BOT_TELEGRAM_TYPE` yet. Gotify
and webhooks have no form fields; they are configured in `backup.env` only. Once Telegram
is on, `Diagnostic Checks` -> `Telegram` verifies the pairing without running a backup.

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

# Webhook (Discord + Pushover)
WEBHOOK_ENABLED=true
WEBHOOK_ENDPOINTS=discord_alerts,pushover
WEBHOOK_DISCORD_ALERTS_URL=https://discord.com/api/webhooks/XXXX/YYYY
WEBHOOK_DISCORD_ALERTS_FORMAT=discord
WEBHOOK_DISCORD_ALERTS_METHOD=POST

# Pushover (push notifications to phone/desktop). Token + user key go in the
# JSON body, so AUTH_TYPE stays "none". PRIORITY accepts -2..1 (default 0).
WEBHOOK_PUSHOVER_URL=https://api.pushover.net/1/messages.json
WEBHOOK_PUSHOVER_FORMAT=pushover
WEBHOOK_PUSHOVER_METHOD=POST
WEBHOOK_PUSHOVER_AUTH_TYPE=none
WEBHOOK_PUSHOVER_AUTH_TOKEN=<pushover-application-token>
WEBHOOK_PUSHOVER_AUTH_USER=<pushover-user-or-group-key>
WEBHOOK_PUSHOVER_PRIORITY=0
```

Run the backup (`Backup` -> `Backup`, or `proxsave --backup`) and the report goes to
Telegram, Email, Discord and Pushover.

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
# Option A: Cloud relay (default, outbound HTTPS)
# - Set EMAIL_DELIVERY_METHOD=relay and configure EMAIL_RECIPIENT (or leave empty for root@pam auto-detect)
# - Relay blocks root@... recipients; use a real non-root mailbox for EMAIL_RECIPIENT
# - No local SMTP/MTA setup required on the node
# - Optional/default: set EMAIL_FALLBACK_SENDMAIL=true to fall back to local sendmail when the relay fails

# Option B: Local sendmail (/usr/sbin/sendmail)
# - Set EMAIL_DELIVERY_METHOD=sendmail
# - Requires a working local MTA (e.g. postfix) on the node
# - EMAIL_RECIPIENT is required (or auto-detected from Proxmox root@pam if configured)

# Option C: Proxmox Notifications (manual)
# - Set EMAIL_DELIVERY_METHOD=pmf
# - Configure SMTP/Sendmail targets and matchers in Proxmox Notifications
# - ProxSave does not need SMTP host/port/user/password
# - EMAIL_RECIPIENT is optional (only used for the To: header)
# - If PMF fails the relay is tried next regardless of EMAIL_FALLBACK_SENDMAIL, which only gates the final local-sendmail leg. Use EMAIL_DELIVERY_METHOD=sendmail to stay off the relay entirely
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
- ✅ Pushover push notification

**On failure**:
- ❌ Telegram alert with error
- ❌ Email with failure details
- ❌ Discord mention with logs
- ❌ Pushover push notification

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
```

### Building it from the dashboard

Six of the blocks above are on the configuration form
(`Maintenance` -> `Install` -> `Edit install`): secondary storage, cloud storage, Telegram,
email and its delivery mode, encryption, and the scheduler engine plus the daily run time.
The rest (compression, retention, security, metrics, collectors, custom paths) is
hand-edited in `configs/backup.env`.

Then, still in the dashboard:

1. `Maintenance` -> `New key` for the AGE recipient.
2. `Daemon` -> `Status` to confirm the resident service owns the schedule, or
   `Daemon` -> `Install` if the host is still on cron.
3. `Diagnostic Checks` -> `Telegram`, then `Healthchecks`, then `Post-install`, to confirm
   the install before you trust it.
4. `Backup` -> `Backup` for the first run.

### Scheduling

The resident daemon runs the backup daily at `SCHEDULER_TIME`, under a `MAX_RUN_DURATION`
watchdog, and reports to an external monitor. That is the recommended engine for a
production host. A cron entry applies only where the daemon was deliberately declined:

```bash
# Daily backup at 2 AM (SCHEDULER_MODE=cron only)
crontab -e
0 2 * * * /usr/local/bin/proxsave --backup >> /var/log/pbs-backup-cron.log 2>&1
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
# /etc, /var, /root, /home are resolved under the prefix
```

### Setup Steps

This is a flags-only scenario. The run needs a second configuration file selected with
`--config`, and any flag at all skips the dashboard, so there is no dashboard route for it.

`SYSTEM_ROOT_PREFIX` is **not** one of the keys ProxSave reads from the environment, so it has to go in a config file. Setting it on the command line is silently ignored and the run collects the live root instead, which is the one mistake here that produces a plausible-looking archive of the wrong system.

```bash
# 1) Mount or prepare the alternate root
mount /dev/vg0/snap /mnt/snapshot-root   # example

# 2) Put the prefix in a dedicated config file
cp /opt/proxsave/configs/backup.env /opt/proxsave/configs/snapshot.env
sed -i 's|^SYSTEM_ROOT_PREFIX=.*|SYSTEM_ROOT_PREFIX=/mnt/snapshot-root|' /opt/proxsave/configs/snapshot.env

# 3) Run a dry-run against it
proxsave -c /opt/proxsave/configs/snapshot.env --dry-run

# 4) Run the actual backup (optional)
proxsave -c /opt/proxsave/configs/snapshot.env --backup
```

Confirm the prefix took effect before running for real. On a Proxmox snapshot the run header names it (`Proxmox Type (prefix /mnt/snapshot-root): ...`); on a plain chroot or fixture that line is debug-only, so run the dry-run with `--log-level debug` and check the collected paths resolve under `/mnt/snapshot-root`. Collection lines at the default level name the category, not the path, so they cannot tell you which root was used.

### Expected Results
- Collected files reflect the contents of `/mnt/snapshot-root/etc`, `/var`, `/root`, `/home`, etc.
- No writes to the node's live filesystem.

---

## HA-LXC Backup Appliance (`HOST_BACKUP_MODE`)

**Scenario**: Run ProxSave inside a privileged LXC that backs up the Proxmox host, with the host filesystem bind-mounted read-only into the container (issue #255).

**Use case**:
- A dedicated, restartable backup appliance for a Proxmox VE or PBS host
- Keeping ProxSave and its dependencies out of the host root

### Setup Steps

```bash
# 1) On the Proxmox host, bind-mount the host root and pmxcfs read-only into the CT
pct set <CTID> -mp0 /,mp=/host,ro=1
pct set <CTID> -mp1 /etc/pve,mp=/host/etc/pve,ro=1

# 2) In the container's configs/backup.env
SYSTEM_ROOT_PREFIX=/host
HOST_BACKUP_MODE=true
BACKUP_ENABLED=true

# 3) Dry-run, then run
proxsave --dry-run
proxsave --backup
```

Once the container is configured, running `proxsave` bare inside it opens the dashboard as
usual, so the appliance is managed the same way as a host install.

### Expected Results
- Proxmox type is detected from the mounted host (`/host/etc/pve`, `/host/etc/proxmox-backup`), not the container.
- Absolute symlinks such as `/etc/ceph/ceph.conf -> /etc/pve/ceph.conf` resolve under the prefix, so Ceph configuration is collected.
- ZFS pool state is collected (shared kernel); namespace-scoped and cluster-daemon commands (udevadm, ethtool, pvesh, ceph) are skipped and their data comes from the host files.
- Symlink targets are stored verbatim, so the archive restores onto a real host unchanged.
- No writes to the host filesystem.

---

## Example 10: Dual PVE+PBS Host

**Scenario**: A single node runs both Proxmox VE and Proxmox Backup Server.

**Use case**:
- Lab or edge node with co-installed PVE + PBS
- Single backup run should include both product roles
- Restore must remain compatible with `dual`, `pve`, or `pbs` targets

### Configuration

```bash
# configs/backup.env
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxsave/backup
LOG_PATH=/opt/proxsave/log

# Common/system collection
BACKUP_NETWORK_CONFIGS=true
BACKUP_CRON_JOBS=true
BACKUP_SYSTEMD_SERVICES=true
BACKUP_ZFS_CONFIG=true

# PVE collection
BACKUP_VM_CONFIGS=true
BACKUP_CLUSTER_CONFIG=true
BACKUP_PVE_JOBS=true
BACKUP_PVE_REPLICATION=true
BACKUP_PVE_FIREWALL=true

# PBS collection
BACKUP_DATASTORE_CONFIGS=true
BACKUP_REMOTE_CONFIGS=true
BACKUP_SYNC_JOBS=true
BACKUP_VERIFICATION_JOBS=true
BACKUP_PBS_NOTIFICATIONS=true
BACKUP_PBS_NODE_CONFIG=true

# Recommended for dual labs: opt in to the PBS datastore inventories (off by default;
# metadata only, never datastore contents, and it walks every configured or discovered PBS datastore)
PXAR_SCAN_ENABLE=true
```

### Expected Behavior

- ProxSave auto-detects the host as `dual`
- One archive is produced for the run
- Metadata persists:
  - `BACKUP_TYPE=dual`
  - `BACKUP_TARGETS=pve,pbs`
- The backup contains:
  - PVE categories
  - PBS categories
  - one shared `common/system` payload

### Restore Notes

- Restore on a `dual` host: full `PVE + PBS + Common`
- Restore on a `pve` host: `PVE + Common`
- Restore on a `pbs` host: `PBS + Common`

The restore workflow filters categories automatically when the current host does
not support all backup targets.

---

## Example 11: Resident daemon and healthchecks monitoring

**Scenario**: You want the backup scheduled and supervised by a resident service with a
hang watchdog and an external dead-man switch, instead of a bare cron entry. This is the
normal engine and fresh installs default to it; cron is the opt-out. Use this to switch an
existing install or to inspect one.

### Switch to the daemon

In the dashboard, the `Daemon` group shows only the command that fits the current state.
On a host still on cron it offers `Install`; pick it and the switch runs in the screen,
with the result reported there. `Status` is always offered and is read-only.

The same operations from a script or a headless host:

```bash
# Install the systemd service, remove the proxsave cron entry, turn on centralized healthchecks
proxsave --daemon-setup

# Check status (exit 0 only when running, beating, and binary-aligned)
proxsave --daemon-status
```

Both routes run the same code. `--daemon-setup` writes `SCHEDULER_MODE=daemon` and `HEALTHCHECK_ENABLED=true` and starts
`proxsave-daemon.service`. The daemon runs the backup daily at `SCHEDULER_TIME`, under a
`MAX_RUN_DURATION` watchdog, and reports three fixed checks (alive, backup, updates) plus one
per notification channel to an external healthchecks monitor.

It also deletes every cron line whose command is named `proxsave` or `proxmox-backup`, and
tells you how many it removed, or that it found none. "None" on a host that was running on
cron means something else was scheduling the backup, typically a wrapper script whose command
has a different name: that entry survives and now runs alongside the daemon, and `--daemon-setup`
warns about it by name. The unattended `--upgrade` migration refuses outright in that case.
Read [DAEMON.md](DAEMON.md) before switching such a host.

### Inspect and revert

From the dashboard, on a host where the daemon is the active scheduler, the `Daemon` group
offers `Disable` (revert to cron), `Restart` (reload a rebuilt binary) and `Status`.
`Status` is the screen to read first: it reports the scheduler mode, the service state, the
running version and whether that version matches the binary on disk.

The equivalents, plus the systemd views the dashboard does not wrap:

```bash
systemctl status proxsave-daemon.service      # is it running?
journalctl -u proxsave-daemon.service -f      # follow its log
proxsave --daemon-remove                      # revert to a cron entry
```

`--daemon-remove` always writes a cron line at `SCHEDULER_TIME` and records `SCHEDULER_MODE=cron`,
which is what stops upgrades reinstalling the daemon: the key is present, so it is honoured. If the
host also schedules ProxSave through an entry ProxSave does not own, that entry is reported and left
alone, so such a host ends with two nightly backups and the run that loses the per-run lock exits
`16`. Withholding the line instead would leave a misidentified host with nothing scheduled at all,
silently, which is the worse of the two. The daemon-only healthchecks are switched back off
with the daemon, so a reverted host does not warn about a service that is no longer installed.

### Run your own script around each backup

The daemon can start a script of yours before the run and another after it, for example to
stop a service that must not be running while its data is copied and to start it again
afterwards:

```bash
PERSONAL_SCRIPT_PRE_RUN=/usr/local/bin/proxsave-pre.sh
PERSONAL_SCRIPT_POST_RUN=/usr/local/bin/proxsave-post.sh
```

```bash
#!/bin/sh
# /usr/local/bin/proxsave-pre.sh, chmod 700, owned by root
systemctl stop my-noisy-service
```

```bash
#!/bin/sh
# /usr/local/bin/proxsave-post.sh, chmod 700, owned by root
# Started after every outcome, so the service comes back even when the backup failed.
systemctl start my-noisy-service
```

These are your scripts: ProxSave starts them and says nothing about them. Their output is
discarded, their exit code is ignored, a failure never blocks or fails the backup, and each is
killed after 10 minutes (except on the abandoned-child unwind, see [DAEMON.md](DAEMON.md)). They run only under the daemon, and only around the run it schedules.
Write your own log line if you need a record. See [DAEMON.md](DAEMON.md) for the full contract.

See [DAEMON.md](DAEMON.md) for the daemon itself and [HEALTHCHECKS.md](HEALTHCHECKS.md)
for the monitoring modes (centralized vs self), the monitoring portal, and the full
configuration keys.

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

## Related Documentation

### Using ProxSave
- **[Dashboard](DASHBOARD.md)** - The interactive menu, screen by screen
- **[Daemon](DAEMON.md)** - The resident scheduler, its watchdog and its reporting
- **[Healthchecks](HEALTHCHECKS.md)** - Monitoring modes, the portal, and the checks the daemon reports

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone setup for all examples
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption details

### Operations
- **[CLI Reference](CLI_REFERENCE.md)** - All command-line flags
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common issues and solutions

### Reference
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore from any example backup

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Next Steps

1. **Choose an example** closest to your use case
2. **Open the dashboard** with `proxsave` and set what the configuration form covers
3. **Copy the rest** into `configs/backup.env`
4. **Customize** paths, credentials, retention
5. **Test** with `proxsave --dry-run`
6. **Run** the first backup from `Backup` -> `Backup`
7. **Verify** results in storage locations
8. **Schedule** with the resident daemon (`Daemon` -> `Install` on a cron host), or keep cron if you opted out
9. **Monitor** with `Diagnostic Checks` -> `Healthchecks`, plus logs and notifications

**For detailed configuration options**, see: **[Configuration Guide](CONFIGURATION.md)**
