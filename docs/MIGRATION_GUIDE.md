# Migration Guide: Bash to Go

Complete guide for upgrading from the Bash version (v0.7.4-bash or earlier) to the Go version (proxsave).

## Table of Contents

- [Overview](#overview)
- [Compatibility Overview](#compatibility-overview)
- [Migration Tools](#migration-tools)
- [Upgrade Steps](#upgrade-steps)
- [Key Migration Notes](#key-migration-notes)
- [Troubleshooting Migration](#troubleshooting-migration)
- [Related Documentation](#related-documentation)

---

## Overview

If you're currently using the legacy Bash version (historically called proxmox-backup, v0.7.4-bash or earlier), you can upgrade to the Go-based proxsave release with minimal effort. The Go version offers significant performance improvements while maintaining backward compatibility for most configuration variables.

**Key benefits of upgrading to Go**:
- ⚡ **10-100x faster** execution (compiled vs interpreted)
- 🛡️ **Enhanced security** with automatic permission fixes
- 🔐 **Built-in encryption** (AGE streaming encryption)
- ☁️ **Advanced cloud features** (parallel uploads, better retry logic)
- 📊 **Prometheus metrics** export
- 🔔 **More notification channels** (Gotify, Webhooks)
- 🎯 **GFS retention policies** for compliance
- 🧪 **Better testing** (dry-run mode, configuration validation)

---

## Compatibility Overview

The migration path is designed to be smooth and safe:

- ✅ **Both versions can coexist**: The Bash and Go versions can run in the same directory (`/opt/proxsave/`) as they use different internal paths and binary names
- ✅ **Most variables work unchanged**: ~70 configuration variables have identical names between Bash and Go
- ✅ **Automatic fallback support**: 16 renamed variables automatically read old Bash names via fallback mechanism
- ⚠️ **Some variables require manual conversion**: 2 variables have semantic changes (storage thresholds, cloud path format)
- ℹ️ **Legacy variables**: ~27 Bash-only variables are no longer used (replaced by improved internal logic)

### Variable Categories

| Category | Count | Description | Action Required |
|----------|-------|-------------|-----------------|
| **SAME** | ~70 | Identical names in both versions | ✅ Copy directly |
| **RENAMED ✅** | 16 | New names, but fallback reads old names | ✅ Copy directly (works with old names) |
| **SEMANTIC CHANGE ⚠️** | 2 | Different meaning or format | ⚠️ Manual conversion needed |
| **LEGACY** | ~27 | Bash-only, no longer used | ℹ️ Skip (not needed) |

**Total variables**: ~115 in Bash version → ~200 in Go version (many new features!)

---

## Migration Tools

### Option 1: Automatic Migration Tool (Recommended)

The fastest way to migrate is using the built-in migration tool:

```bash
# Step 1: Preview migration (recommended first step)
./build/proxsave --env-migration-dry-run

# Step 2: Execute real migration
./build/proxsave --env-migration
```

**What the tool does**:
- ✅ Automatically maps 70+ variables (SAME category)
- ✅ Converts 16 renamed variables (RENAMED category)
- ⚠️ Flags 2 variables for manual review (SEMANTIC CHANGE)
- ℹ️ Skips 27 legacy variables (LEGACY category)
- 💾 Creates backup of existing config (`backup.env.bak-YYYYMMDD-HHMMSS`)

**Example output**:
```
✓ Migrated 45 variables (SAME category)
✓ Converted 12 variables (RENAMED category)
⚠ Review required: 2 variables (SEMANTIC CHANGE)
  - STORAGE_WARNING_THRESHOLD_PRIMARY → MIN_DISK_SPACE_PRIMARY_GB
  - CLOUD_BACKUP_PATH → CLOUD_REMOTE_PATH
ℹ Skipped 18 legacy variables (LEGACY category)

Configuration written to: /opt/proxsave/configs/backup.env
Backup saved to: /opt/proxsave/configs/backup.env.bak-20251117-143022

⚠ IMPORTANT: Review SEMANTIC CHANGE variables before running backup!

Next step: ./build/proxsave --dry-run
```

### Option 2: Manual Migration with Reference Guide

For those who prefer manual control or want to understand the changes:

**📄 [BACKUP_ENV_MAPPING.md](BACKUP_ENV_MAPPING.md)** - Complete Bash → Go variable mapping reference

**Quick migration workflow**:
1. Open your Bash `backup.env`
2. Open your Go `backup.env`
3. Refer to `BACKUP_ENV_MAPPING.md` while copying your values
4. Most variables can be copied directly (SAME + RENAMED categories)
5. Pay attention to SEMANTIC CHANGE variables for manual conversion

**Mapping guide structure**:
```
BACKUP_ENV_MAPPING.md
├── SAME: Direct copy variables
├── RENAMED: Old name → New name (with fallback)
├── SEMANTIC CHANGE: Variables requiring conversion
└── LEGACY: Bash-only variables (no longer needed)
```

---

## Upgrade Steps

### Step 1: Build the Go Version

```bash
cd /opt/proxsave
make build

# Verify binary
ls -lh build/proxsave
./build/proxsave --version
```

### Step 2: Migrate Your Configuration

**Option A: Automatic migration** (recommended for existing users):

```bash
# Preview migration (safe, no changes)
./build/proxsave --env-migration-dry-run

# Review the output carefully
# Check which variables need manual review

# Execute migration
./build/proxsave --env-migration

# Manual review of SEMANTIC CHANGE variables
nano configs/backup.env
# Look for comments like: # REVIEW NEEDED: ...
```

**Option B: Manual migration** using mapping guide:

```bash
# Copy Bash config to Go config location
cp /opt/proxsave/env/backup.env /opt/proxsave/configs/backup.env

# Edit with BACKUP_ENV_MAPPING.md as reference
nano configs/backup.env

# Convert SEMANTIC CHANGE variables manually:
# - STORAGE_WARNING_THRESHOLD_PRIMARY="90" → MIN_DISK_SPACE_PRIMARY_GB="10"
# - CLOUD_BACKUP_PATH="/gdrive:backups/folder" → split into:
#   CLOUD_REMOTE="gdrive"
#   CLOUD_REMOTE_PATH="/backups/folder"

# Remove LEGACY variables (optional, they're ignored anyway)
```

### Step 3: Test the Configuration

```bash
# Dry-run to verify configuration
./build/proxsave --dry-run

# Check the output for any warnings or errors
# Look for:
# - "Configuration loaded from: configs/backup.env" ✓
# - "Security checks passed" ✓
# - "Cloud remote accessible" (if using cloud) ✓
# - Any WARNINGS about variables

# View parsed configuration
./build/proxsave --dry-run --log-level debug | grep -i "config\|warning"
```

### Step 4: Run a Test Backup

```bash
# First real backup with Go version
./build/proxsave

# Verify results
ls -lh backup/
# Typical file (bundling enabled by default): <hostname>-backup-YYYYMMDD-HHMMSS.tar.xz.bundle.tar

# Check log for any issues
cat log/backup-*.log
tail -100 log/backup-$(hostname)-*.log | grep -i "error\|warning\|complete"
```

### Step 5: Compare Results

```bash
# Compare Bash vs Go backup sizes
ls -lh backup/*.tar.*

# Verify the bundle contents (lists the embedded archive + metadata + checksum)
tar -tf backup/*-backup-*.bundle.tar | head -50

# Check cloud upload (if enabled)
rclone ls gdrive:pbs-backups/
```

### Step 6: Gradual Cutover (Optional)

The old Bash version remains functional and can be used as fallback during the transition period:

```bash
# Keep Bash version available
# /opt/proxsave/script/proxmox-backup.sh (Bash)
# /opt/proxsave/build/proxsave (Go)

# Test Go version in cron first
crontab -e
# Comment Bash line:
# 0 2 * * * /opt/proxsave/script/proxmox-backup.sh
# Add Go line:
0 2 * * * /opt/proxsave/build/proxsave

# Monitor for 1-2 weeks, then remove Bash cron completely
```

---

## Key Migration Notes

### Automatic Variable Fallbacks

These old Bash variable names **still work** in Go (automatic fallback):

| Old Bash Name | New Go Name | Status |
|---------------|-------------|--------|
| `LOCAL_BACKUP_PATH` | `BACKUP_PATH` | ✅ Auto-fallback |
| `ENABLE_CLOUD_BACKUP` | `CLOUD_ENABLED` | ✅ Auto-fallback |
| `PROMETHEUS_ENABLED` | `METRICS_ENABLED` | ✅ Auto-fallback |
| `PROMETHEUS_TEXTFILE_DIR` | `METRICS_PATH` | ✅ Auto-fallback |
| `TELEGRAM_ENABLE` | `TELEGRAM_ENABLED` | ✅ Auto-fallback |
| `EMAIL_ENABLE` | `EMAIL_ENABLED` | ✅ Auto-fallback |
| `GOTIFY_ENABLE` | `GOTIFY_ENABLED` | ✅ Auto-fallback |
| `WEBHOOK_ENABLE` | `WEBHOOK_ENABLED` | ✅ Auto-fallback |
| _... and 8 more_ | _See BACKUP_ENV_MAPPING.md_ | ✅ Auto-fallback |

**What this means**: You can keep using old variable names, and Go will automatically read them. However, **it's recommended to update to new names** for clarity and future compatibility.

For email notifications, if `EMAIL_ENABLED` is omitted entirely, the runtime default is `false`, matching the template.

### Variables Requiring Conversion

#### 1. Storage Thresholds (SEMANTIC CHANGE)

**Bash version** (percentage of disk used):
```bash
STORAGE_WARNING_THRESHOLD_PRIMARY="90"    # Warn when 90% disk used
STORAGE_WARNING_THRESHOLD_SECONDARY="95"
```

**Go version** (GB of free space):
```bash
MIN_DISK_SPACE_PRIMARY_GB="10"            # Warn when <10GB free
MIN_DISK_SPACE_SECONDARY_GB="5"
```

**Conversion formula**:
```
Disk size: 100GB
Bash: 90% used = 10GB free
Go: MIN_DISK_SPACE_PRIMARY_GB="10"
```

**Example conversions**:

| Disk Size | Bash (% used) | Go (GB free) |
|-----------|---------------|--------------|
| 50GB SSD | 90% | `5` |
| 100GB SSD | 90% | `10` |
| 500GB HDD | 95% | `25` |
| 1TB HDD | 95% | `50` |
| 2TB NAS | 98% | `40` |

#### 2. Cloud Path Format (SEMANTIC CHANGE)

**Bash version** (full path):
```bash
CLOUD_BACKUP_PATH="/gdrive:pbs-backups/folder"
```

**Go version** (remote name + path/prefix):
```bash
CLOUD_REMOTE="gdrive"                    # rclone remote name
CLOUD_REMOTE_PATH="/pbs-backups/folder"  # Full path/prefix inside that remote
# Optional: add extra suffixes (e.g., "/pbs-backups/folder/server1")
```

**Example conversions**:

| Bash `CLOUD_BACKUP_PATH` | Go `CLOUD_REMOTE` | Go `CLOUD_REMOTE_PATH` |
|--------------------------|-------------------|------------------------|
| `/gdrive:backups` | `gdrive` | `/backups` |
| `/gdrive:backups/pve1` | `gdrive` | `/backups/pve1` |
| `/s3:my-bucket/folder/subfolder` | `s3` | `/my-bucket/folder/subfolder` |
| `/b2:pbs/archives` | `b2` | `/pbs/archives` |

### Legacy Variables (No Longer Needed)

These Bash variables are **not needed** in Go (skip them during migration):

**Logging & Output**:
- `ENABLE_EMOJI_LOG` → Go uses `USE_COLOR` instead
- `LOG_COLOR_*` (all color codes) → Go handles colors internally
- `VERBOSE_LOGGING` → Go uses `DEBUG_LEVEL`

**Paths**:
- `BACKUP_ENV_PATH` → Go uses fixed `configs/backup.env`
- `SCRIPT_DIR` → Go binary is self-contained
- `BASE_DIR` → Go auto-detects it from the installed `proxsave` executable; active migrated values are ignored/removed

**Internal Logic**:
- `BACKUP_TIMESTAMP_FORMAT` → Go uses ISO 8601 internally
- `TEMP_ARCHIVE_PATH` → Go manages temporary files automatically
- Various `COLOR_*` variables → Go handles terminal colors

**~27 total legacy variables** - see BACKUP_ENV_MAPPING.md for complete list.

### New Go-Only Features Available

After migration, you can enable new features not available in Bash:

**GFS Retention Policies**:
```bash
RETENTION_POLICY=gfs
RETENTION_DAILY=7
RETENTION_WEEKLY=4
RETENTION_MONTHLY=12
RETENTION_YEARLY=3
```

**AGE Encryption**:
```bash
ENCRYPT_ARCHIVE=true
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt
```

**Parallel Cloud Uploads**:
```bash
CLOUD_UPLOAD_MODE=parallel
CLOUD_PARALLEL_MAX_JOBS=4
```

**Advanced Security Checks**:
```bash
SECURITY_CHECK_ENABLED=true
AUTO_FIX_PERMISSIONS=true
```

**Webhook Notifications**:
```bash
WEBHOOK_ENABLED=true
WEBHOOK_ENDPOINTS=discord_alerts
WEBHOOK_DISCORD_ALERTS_URL=https://discord.com/api/webhooks/...
WEBHOOK_DISCORD_ALERTS_FORMAT=discord
```

**Prometheus Metrics**:
```bash
METRICS_ENABLED=true
METRICS_PATH=/var/lib/prometheus/node-exporter
```

---

## Troubleshooting Migration

### Problem: "Configuration variable not recognized"

**Solution**: Check `BACKUP_ENV_MAPPING.md` to see if the variable was renamed or is now LEGACY.

**Example**:
```
Error: Unknown variable: ENABLE_EMOJI_LOG
```

This is a LEGACY variable (Bash-only). Remove it or comment it out in Go config.

---

### Problem: Storage threshold warnings incorrect

**Symptoms**:
- Warnings about disk space even though plenty is available
- Or no warnings when disk is almost full

**Solution**: Convert percentage-based thresholds to GB-based (SEMANTIC CHANGE variables).

**Example**:
```bash
# OLD (Bash): 90% used threshold on 100GB disk = warn at 10GB free
STORAGE_WARNING_THRESHOLD_PRIMARY="90"

# NEW (Go): Warn when <10GB free
MIN_DISK_SPACE_PRIMARY_GB="10"
```

---

### Problem: Cloud path not working

**Symptoms**:
- Cloud uploads fail with "path not found"
- rclone errors about invalid remote

**Solution**: Split `CLOUD_BACKUP_PATH` into `CLOUD_REMOTE` (remote name) and `CLOUD_REMOTE_PATH` (full path inside that remote).

**Example**:
```bash
# OLD (Bash):
CLOUD_BACKUP_PATH="/gdrive:pbs-backups/server1"

# NEW (Go):
CLOUD_REMOTE="gdrive"
CLOUD_REMOTE_PATH="/pbs-backups/server1"
```

---

### Problem: Notifications not working after migration

**Symptoms**:
- Telegram/Email not sending
- No errors in logs

**Solution**: Check for renamed variables with `_ENABLE` → `_ENABLED`.

**Example**:
```bash
# OLD (Bash):
TELEGRAM_ENABLE=true

# NEW (Go) - note the 'D' at the end:
TELEGRAM_ENABLED=true
```

**Note**: Automatic fallback handles the legacy `_ENABLE` aliases, but explicitly updating is cleaner. For email, leaving `EMAIL_ENABLED` unset now keeps notifications disabled by default.

---

### Problem: Backup size different from Bash version

**Symptoms**:
- Go backups are larger or smaller than Bash

**Explanation**: This is usually due to:
1. Different compression defaults
2. Additional collectors enabled/disabled
3. Bundling settings

**Solution**: Match compression settings:
```bash
# Check Bash settings
grep COMPRESSION /path/to/bash/backup.env

# Match in Go
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6
COMPRESSION_MODE=standard
```

---

### Still Having Issues?

1. **Review the complete mapping guide**: [BACKUP_ENV_MAPPING.md](BACKUP_ENV_MAPPING.md)
2. **Compare configs side-by-side**:
   ```bash
   diff -u /path/to/bash/backup.env configs/backup.env
   ```
3. **Enable debug logging**:
   ```bash
   ./build/proxsave --dry-run --log-level debug
   ```
4. **Check for SEMANTIC CHANGE variables**:
   ```bash
   grep "REVIEW NEEDED" configs/backup.env
   ```
5. **Consult other documentation**:
   - [Configuration Guide](CONFIGURATION.md) - All Go variables explained
   - [Troubleshooting Guide](TROUBLESHOOTING.md) - Common issues
   - [Examples](EXAMPLES.md) - Working configurations

---

## Related Documentation

### Migration Resources
- **BACKUP_ENV_MAPPING.md** - Complete Bash → Go variable mapping
- **[Legacy Bash Guide](LEGACY_BASH.md)** - Information about the Bash version

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete Go variable reference
- **[CLI Reference](CLI_REFERENCE.md)** - All command-line flags including `--env-migration`

### Operations
- **[Examples](EXAMPLES.md)** - Real-world Go configurations
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common issues and solutions

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Migration Checklist

Use this checklist to ensure a smooth migration:

```
Pre-Migration:
□ Backup current Bash configuration
□ Note current backup size and duration
□ Review BACKUP_ENV_MAPPING.md
□ Build Go binary (make build)

Migration:
□ Run --env-migration-dry-run (preview)
□ Review SEMANTIC CHANGE variables
□ Run --env-migration (execute)
□ Manually convert storage thresholds (% → GB)
□ Manually split cloud path (if using cloud)
□ Remove or comment LEGACY variables (optional)

Testing:
□ Run --dry-run (verify config loading)
□ Check for warnings in output
□ Run first backup with Go version
□ Compare backup size with Bash version
□ Verify all expected files in archive
□ Test cloud upload (if enabled)
□ Test notifications (if enabled)

Deployment:
□ Update cron job to use Go binary
□ Monitor first few scheduled backups
□ Keep Bash version as fallback (1-2 weeks)
□ Document any custom changes
□ Archive old Bash config for reference

Cleanup (after 1-2 weeks):
□ Remove Bash cron job
□ Archive Bash version (optional)
□ Update documentation/runbooks
□ Celebrate successful migration! 🎉
```

---

## FAQ

**Q: Can I run both Bash and Go versions in parallel?**
A: Yes! They can coexist in the same directory. However, **avoid running both simultaneously** to prevent conflicts. Use one or the other per cron schedule.

**Q: Will migration delete my Bash configuration?**
A: No. The migration tool **never deletes** your Bash config. It only reads it and creates a new Go config.

**Q: What happens if I use old variable names in Go config?**
A: For RENAMED variables (16 total), Go automatically falls back to read old names. However, it's recommended to update to new names for clarity.

**Q: How long does migration take?**
A: Automatic migration takes **seconds**. Manual migration depends on complexity but typically **10-30 minutes** for careful review.

**Q: Can I roll back to Bash version after migration?**
A: Yes! Your Bash installation remains untouched. Simply switch your cron job back to the Bash script.

**Q: Do I need to migrate everything at once?**
A: No. You can migrate incrementally:
1. Start with basic backup settings
2. Test thoroughly
3. Add cloud/encryption features later

**Q: Will my backup retention history be preserved?**
A: Yes! Both versions use the same `backup/` directory, so existing backups are retained and counted by Go retention logic.

**Q: Are encrypted backups from Bash compatible with Go?**
A: Go uses a different encryption system (AGE). Bash version backups need to be decrypted with their original method. New backups from Go will use AGE encryption.
