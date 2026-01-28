# Troubleshooting Guide

Complete troubleshooting guide for Proxsave with common issues, solutions, and debugging procedures.

## Table of Contents

- [Overview](#overview)
- [Common Issues](#common-issues)
  - [Build Failures](#1-build-failures)
  - [Configuration Issues](#2-configuration-issues)
  - [Cloud Storage Issues](#3-cloud-storage-issues)
  - [Encryption Issues](#4-encryption-issues)
  - [Disk Space Issues](#5-disk-space-issues)
  - [Email Notification Issues](#6-email-notification-issues)
  - [Restore Issues](#7-restore-issues)
- [Debug Procedures](#debug-procedures)
- [Getting Help](#getting-help)
- [Related Documentation](#related-documentation)

---

## Overview

This guide covers the most common issues encountered when using Proxsave, along with step-by-step solutions and debugging procedures.

**Before troubleshooting**:
1. Check you're running the latest version: `./build/proxsave --version`
2. Try dry-run mode first: `./build/proxsave --dry-run --log-level debug`
3. Review logs in `LOG_PATH/backup-$(hostname)-*.log`

---

## Common Issues

### 1. Build Failures

#### Error: `go: cannot find main module`

**Cause**: Not in project root directory, or `go.mod` missing.

**Solution**:
```bash
cd /opt/proxsave  # Ensure you're in project root
go mod init github.com/tis24dev/proxsave
go mod tidy
make build
```

**Verification**:
```bash
ls -la go.mod go.sum
# Both files should exist
```

---

#### Error: `package xxx not found`

**Cause**: Missing dependencies.

**Solution**:
```bash
go mod tidy  # Download dependencies
make build
```

**Alternative (clean rebuild)**:
```bash
rm -rf go.sum vendor/
go mod tidy
go mod vendor  # Optional: vendor dependencies
make build
```

---

#### Error: `build fails with permission denied`

**Cause**: Insufficient permissions on build directory.

**Solution**:
```bash
# Fix build directory permissions
chmod 755 /opt/proxsave/build
chown $(whoami):$(whoami) /opt/proxsave/build

# Rebuild
make clean
make build
```

---

### 2. Configuration Issues

#### Error: `Configuration file not found: configs/backup.env`

**Cause**: Configuration file doesn't exist or is in wrong location.

**Solution**:
```bash
# Run installer to create config
./build/proxsave --install
# For a clean reinstall (keeps env/ and identity/), run:
# ./build/proxsave --new-install

# Or copy template manually
cp internal/config/templates/backup.env configs/backup.env
nano configs/backup.env
```

**Using custom path**:
```bash
# Specify custom config location
./build/proxsave --config /etc/pbs/prod.env
```

---

#### Error: `Security check failed: Permission denied`

**Cause**: Incorrect file/directory permissions.

**Solution**:
```bash
# Fix permissions manually
chmod 700 /opt/proxsave/backup
chmod 700 /opt/proxsave/log
chmod 600 /opt/proxsave/configs/backup.env

# Or enable auto-fix in config
nano configs/backup.env
AUTO_FIX_PERMISSIONS=true
```

**Recommended permissions**:
```
/opt/proxsave/           755 (drwxr-xr-x)
├── backup/                    700 (drwx------)
├── log/                       700 (drwx------)
├── configs/
│   └── backup.env             600 (-rw-------)
├── identity/                  700 (drwx------)
│   └── age/
│       ├── recipient.txt      600 (-rw-------)
└── build/
    └── proxsave               755 (-rwxr-xr-x)
```

---

#### Error: `Invalid configuration value for COMPRESSION_TYPE`

**Cause**: Typo or unsupported compression algorithm.

**Solution**:
```bash
# Check valid values
nano configs/backup.env
COMPRESSION_TYPE=xz    # Valid: xz, zstd, gzip, bzip2, lz4
```

**Test configuration**:
```bash
./build/proxsave --dry-run --log-level debug
# Check for configuration validation errors
```

---

### 3. Cloud Storage Issues

#### Error: `rclone not found in PATH`

**Cause**: rclone not installed or not in PATH.

**Solution**:
```bash
# Install rclone
curl https://rclone.org/install.sh | sudo bash

# Verify
rclone version
which rclone
```

**Manual installation**:
```bash
wget https://downloads.rclone.org/rclone-current-linux-amd64.zip
unzip rclone-current-linux-amd64.zip
sudo cp rclone-*/rclone /usr/local/bin/
sudo chmod 755 /usr/local/bin/rclone
```

---

#### Error: `Cloud remote gdrive not accessible: couldn't find configuration section`

**Cause**: rclone remote not configured.

**Solution**:
```bash
# Configure rclone remote
rclone config
# n > gdrive > drive > ... > y > q

# Test remote
rclone listremotes
# Should show: gdrive:

# Test access
rclone lsf gdrive:
```

**Verify remote in config**:
```bash
# Check rclone config
rclone config show gdrive

# Verify backup.env points to correct remote
grep CLOUD_REMOTE configs/backup.env
grep CLOUD_REMOTE_PATH configs/backup.env
# Should match:
#   CLOUD_REMOTE=gdrive
#   CLOUD_REMOTE_PATH=/pbs-backups
```

---

#### Error: `401 unauthorized`

**Cause**: Expired OAuth token or invalid credentials.

**Solution for Google Drive**:
```bash
# Reconnect OAuth
rclone config reconnect gdrive

# Or reconfigure from scratch
rclone config delete gdrive
rclone config  # Create new
```

**Solution for S3/B2**:
```bash
# Regenerate API keys from provider console
# Delete old remote and create new with fresh keys
rclone config delete s3backup
rclone config  # Create new with updated credentials
```

---

#### Error: `connection timeout (30s)`

**Cause**: Slow network or firewall blocking connection.

**Solution**:
```bash
# Increase connection timeout
nano configs/backup.env
RCLONE_TIMEOUT_CONNECTION=60

# Test connectivity
ping -c 4 google.com
curl -I https://www.googleapis.com
```

**Firewall check**:
```bash
# Test HTTPS connectivity
curl -v https://www.googleapis.com 2>&1 | grep -i "connected\|timeout"

# Check firewall rules (if applicable)
iptables -L -n | grep -i drop
```

---

#### Restore/Decrypt: stuck on “Scanning backup path…” or timeout (cloud/rclone)

**Cause**: ProxSave scans cloud backups by listing the remote (`rclone lsf`) and inspecting each candidate by reading the manifest/metadata (`rclone cat`). Each rclone call is protected by `RCLONE_TIMEOUT_CONNECTION` (the timer resets per command). On slow remotes or very large directories this can time out.

**Solution**:
```bash
# Increase scan timeout
nano configs/backup.env
RCLONE_TIMEOUT_CONNECTION=120

# Ensure you selected the remote directory that contains the backups (scan is non-recursive),
# then re-run restore with debug logs (restore log path is printed on start)
./build/proxsave --restore --log-level debug

# Or use support mode to capture full diagnostics
./build/proxsave --restore --support
```

If it still fails, run the equivalent manual checks:
```bash
rclone lsf <remote:path>
rclone cat <remote:path>/<backup>.bundle.tar | head
```

---

#### Error: `operation timeout (300s exceeded)`

**Cause**: Large backup file + slow upload speed.

**Solution 1: Increase timeout**:
```bash
nano configs/backup.env
RCLONE_TIMEOUT_OPERATION=900  # 15 minutes
```

**Solution 2: Reduce backup size**:
```bash
# Use faster compression
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
COMPRESSION_MODE=fast

# Or reduce backup scope
BACKUP_CLUSTER_CONFIG=false
BACKUP_ROOT_HOME=false
```

> Note: `BACKUP_CLUSTER_CONFIG=false` also skips cluster runtime collection (`pvecm status`, `pvecm nodes`, HA status), which helps avoid non-critical cluster warnings on standalone nodes.

---

#### Error: `429 Too Many Requests` (API rate limiting)

**Cause**: Exceeding cloud provider API rate limits.

**Solution**:
```bash
# Reduce parallel transfers
nano configs/backup.env
RCLONE_TRANSFERS=2
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=3  # Wait 3 seconds between batches
```

**Provider-specific tuning**:

**Google Drive**:
```bash
RCLONE_TRANSFERS=2-4
CLOUD_BATCH_SIZE=10
CLOUD_BATCH_PAUSE=2
```

**Backblaze B2**:
```bash
RCLONE_TRANSFERS=2
CLOUD_BATCH_SIZE=20
CLOUD_BATCH_PAUSE=2
```

---

#### Error: `directory not found` or `403 forbidden` during connectivity check

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

**Verify permissions**:
```bash
# Test write access
echo "test" > /tmp/test.txt
rclone copy /tmp/test.txt gdrive:pbs-backups/
rclone deletefile gdrive:pbs-backups/test.txt
rm /tmp/test.txt
```

---

### 4. Encryption Issues

#### Error: `Encryption setup requires interaction but terminal unavailable`

**Cause**: Trying to run encryption wizard in non-interactive environment (cron, systemd).

**Solution 1: Pre-generate keys**:
```bash
# Run key generation interactively first
./build/proxsave --newkey

# Then run backup (uses existing keys)
./build/proxsave
```

**Solution 2: Set recipient directly**:
```bash
# Generate key manually
age-keygen -o configs/age-keys.txt

# Extract public key
grep "# public key:" configs/age-keys.txt | cut -d: -f2 | tr -d ' '

# Add to config
nano configs/backup.env
AGE_RECIPIENT="age1abc123def456..."
```

---

#### Error: `Failed to decrypt: incorrect passphrase`

**Cause**: Wrong passphrase, wrong key, or corrupted backup.

**Solution**:
- **Verify passphrase** is correct (case-sensitive, no trailing spaces)
- If using private key, paste full `AGE-SECRET-KEY-1...` string (not public key!)
- Try alternative recipients if multiple were configured
- **No recovery possible** if passphrase/key is lost

**Test decryption manually**:
```bash
# With private key file
age --decrypt -i configs/age-keys.txt backup.tar.xz.age > test.tar.xz

# With passphrase
age --decrypt backup.tar.xz.age > test.tar.xz
# (prompts for passphrase)
```

**Verify backup integrity**:
```bash
# Check SHA256 checksum
sha256sum -c backup.*.sha256

# If checksum fails, backup is corrupted
```

---

#### Error: `age: no identity matched any of the recipients`

**Cause**: Using wrong private key (doesn't match any recipient used during encryption).

**Solution**:
```bash
# Check which public key was used during encryption
# (stored in backup.env at backup time)

# Verify your private key matches
age-keygen -y configs/age-keys.txt
# Output should match AGE_RECIPIENT in backup.env

# If mismatch, find correct private key or use passphrase
```

---

### 5. Disk Space Issues

#### Error: `Insufficient disk space: 0.5 GB available, 1 GB required`

**Cause**: Not enough free space for backup creation.

**Solution 1: Clean old backups**:
```bash
# Check disk usage
df -h /opt/proxsave

# List backups by size
ls -lh /opt/proxsave/backup/

# Clean old backups manually
rm /opt/proxsave/backup/backup.*.tar.xz
```

**Solution 2: Adjust retention**:
```bash
nano configs/backup.env
MAX_LOCAL_BACKUPS=5  # Keep fewer backups

# Or use GFS with aggressive pruning
RETENTION_POLICY=gfs
RETENTION_DAILY=3
RETENTION_WEEKLY=2
RETENTION_MONTHLY=6
RETENTION_YEARLY=2
```

**Solution 3: Reduce backup size**:
```bash
# Disable large collectors
BACKUP_ROOT_HOME=false
BACKUP_CRITICAL_FILES=false

# Use faster/smaller compression
COMPRESSION_TYPE=zstd
COMPRESSION_LEVEL=3
```

---

### 6. Email Notification Issues

#### Symptom: No email notifications received

First, confirm which delivery method you are using:

```bash
# configs/backup.env
EMAIL_DELIVERY_METHOD=relay   # cloud relay (outbound HTTPS)
# or
EMAIL_DELIVERY_METHOD=sendmail # /usr/sbin/sendmail (local MTA required)
# or
EMAIL_DELIVERY_METHOD=pmf     # Proxmox Notifications via proxmox-mail-forward
```

If Email is enabled but you don't see it being dispatched, ensure `EMAIL_DELIVERY_METHOD` is exactly one of: `relay`, `sendmail`, `pmf` (typos will skip Email with a warning like: `Email: enabled but not initialized (...)`).

##### If `EMAIL_DELIVERY_METHOD=relay`

- Ensure outbound HTTPS works from the node (the relay needs network access).
- Ensure the recipient is configured:
  - Set `EMAIL_RECIPIENT=...`, or
  - Leave it empty and set an email for `root@pam` inside Proxmox (auto-detect).
- Relay blocks `root@…` recipients; use a real non-root mailbox for `EMAIL_RECIPIENT`.
- If `EMAIL_FALLBACK_SENDMAIL=true`, ProxSave will fall back to `EMAIL_DELIVERY_METHOD=pmf` when the relay fails.
- Check the proxsave logs for `email-relay` warnings/errors.

##### If `EMAIL_DELIVERY_METHOD=sendmail`

This mode uses `/usr/sbin/sendmail`, so your node must have a working local MTA (e.g. postfix).

- Ensure a recipient is available:
  - Set `EMAIL_RECIPIENT=...`, or
  - Leave it empty and set an email for `root@pam` inside Proxmox (auto-detect).
- Verify `sendmail` exists:
  ```bash
  test -x /usr/sbin/sendmail && echo "sendmail OK" || echo "sendmail not found"
  ```
- Check your MTA status and queue (`systemctl status postfix`, `mailq`, `/var/log/mail.log`).

##### If `EMAIL_DELIVERY_METHOD=pmf`

This mode uses Proxmox Notifications via `proxmox-mail-forward` (final recipients are configured in Proxmox, not in proxsave).

- `EMAIL_RECIPIENT` is optional in this mode and is only used for the `To:` header.
- Verify `proxmox-mail-forward` exists:
  ```bash
  test -x /usr/libexec/proxmox-mail-forward && echo "proxmox-mail-forward OK" || echo "proxmox-mail-forward not found"
  ```
- Verify Proxmox Notifications configuration in the UI (`Datacenter -> Notifications`).

---

#### Error: `Backup path full` warnings but backup succeeds

**Cause**: Warning threshold triggered, but backup still fits.

**Solution**:
```bash
# Adjust warning threshold
nano configs/backup.env
MIN_DISK_SPACE_PRIMARY_GB=5  # Lower threshold

# Or increase disk space
# Add more storage or clean unnecessary files
```

---
### 7. Restore Issues

#### Restore drops SSH / IP changes during network restore

**Symptoms**:
- SSH/Web UI disconnects during restore when the `network` category is applied live
- You see a `NETWORK ROLLBACK` block in the footer (especially after Ctrl+C)

**Explanation**:
- Live network apply can change IP/routes immediately.
- ProxSave protects access by arming a rollback timer that can revert network-related files automatically if `COMMIT` is not received in time.

**What to do**:
- Prefer running restore from the **local console/IPMI**, not over SSH.
- If the footer says **ARMED**, reconnect using the **pre-apply IP** once rollback runs.
- If it says **EXECUTED**, reconnect using the **pre-apply IP** (rollback already ran).
- If it says **DISARMED/CLEARED**, reconnect using the **post-apply IP** (new config remains active).
- Check the rollback log path printed in the footer for details.

#### Error during network preflight: `addr_add_dry_run() got an unexpected keyword argument 'nodad'`

**Symptoms**:
- Restore networking preflight fails when running `ifup -n -a`
- Log contains: `NetlinkListenerWithCache.addr_add_dry_run() got an unexpected keyword argument 'nodad'`

**Cause**:
- A Proxmox-packaged `ifupdown2` version may ship a Python signature mismatch between `addr_add()` and `addr_add_dry_run()` (dry-run path), which crashes `ifup -n` when `nodad` is used.

**What ProxSave does**:
- During restore, ProxSave can apply a guarded hotfix (only when needed) by patching `/usr/share/ifupdown2/lib/nlcache.py` and writing a timestamped `.bak.*` backup first.

**Recovery / rollback**:
- To revert the hotfix, restore the `.bak.*` copy back onto `nlcache.py`, or upgrade `ifupdown2` when Proxmox publishes a fixed build.

---

## Debug Procedures

### Enable Debug Logging

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

**Debug levels**:

| Level | Detail | Use Case |
|-------|--------|----------|
| `standard` | Basic operations | Normal production |
| `advanced` | Command execution, file ops | Troubleshooting |
| `extreme` | Full verbose, all internals | Deep debugging |

---

### Test rclone Manually

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

**Check rclone configuration**:
```bash
# List remotes
rclone listremotes

# Show remote details
rclone config show gdrive

# Test connectivity
rclone about gdrive:
```

---

### Verify Configuration Loading

```bash
# Check parsed configuration
grep -E "^CLOUD_|^RCLONE_" /opt/proxsave/configs/backup.env

# Test with dry-run
./build/proxsave --dry-run --log-level debug
# Check output for loaded config values
```

**Expected dry-run output**:
```
[DRY-RUN] Configuration loaded from: configs/backup.env
[DRY-RUN] CLOUD_ENABLED: true
[DRY-RUN] CLOUD_REMOTE: gdrive:pbs-backups
[DRY-RUN] Would create backup...
```

---

### Analyze Log Files

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

**Log analysis patterns**:
```bash
# Check backup duration
grep "Backup completed" /opt/proxsave/log/backup-*.log

# Check compression ratio
grep "Compression" /opt/proxsave/log/backup-*.log

# Check upload speed
grep -i "upload.*speed\|transfer.*rate" /opt/proxsave/log/backup-*.log

# Check retention operations
grep "Retention" /opt/proxsave/log/backup-*.log
```

---

### Test Individual Components

#### Test Compression

```bash
# Test xz compression
echo "test data" > /tmp/test.txt
xz -zv -6 /tmp/test.txt
ls -lh /tmp/test.txt.xz

# Test zstd compression
echo "test data" > /tmp/test2.txt
zstd -v -3 /tmp/test2.txt
ls -lh /tmp/test2.txt.zst
```

#### Test Encryption

```bash
# Generate test key
age-keygen -o /tmp/test-key.txt

# Encrypt test file
echo "sensitive data" | age -r $(grep "public key:" /tmp/test-key.txt | cut -d: -f2) > /tmp/encrypted.age

# Decrypt test file
age --decrypt -i /tmp/test-key.txt /tmp/encrypted.age
```

#### Test Backup Archive Creation

```bash
# Create test TAR archive
tar -czf /tmp/test-backup.tar.gz /etc/hostname /etc/hosts
tar -tzf /tmp/test-backup.tar.gz
```

---

## Getting Help

### Check Documentation

Before reporting issues, review:

- **[README](../README.md)** - Project overview
- **[Configuration Guide](CONFIGURATION.md)** - All config variables
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone troubleshooting
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption issues
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore troubleshooting

---

### Enable Verbose Logging

```bash
# Capture full debug output
./build/proxsave --log-level debug 2>&1 | tee /tmp/pbs-debug.log
```

---

### Report Issues

If problem persists:

**1. Gather information**:
```bash
./build/proxsave --version
rclone version
go version
uname -a
```

**2. Collect logs**:
```bash
tar -czf /tmp/pbs-debug.tar.gz \
    /opt/proxsave/log/backup-*.log \
    /tmp/pbs-debug.log
```

**3. Sanitize config** (remove credentials):
```bash
cp configs/backup.env /tmp/backup.env.sanitized
nano /tmp/backup.env.sanitized
# Remove: TELEGRAM_BOT_TOKEN, TELEGRAM_CHAT_ID, EMAIL_*, WEBHOOK_*_URL, AGE_RECIPIENT
```

**4. Create GitHub issue**:
- **Repository**: https://github.com/tis24dev/proxsave/issues
- **Include**:
  - Version information
  - Sanitized configuration
  - Relevant log excerpts
  - Steps to reproduce
- **Describe**:
  - Expected behavior
  - Actual behavior
  - Environment details

**Issue template**:
```markdown
**Version**:
./build/proxsave --version output here

**Environment**:
- OS: Proxmox VE 8.x / PBS 3.x / Debian 12
- Go version:
- rclone version:

**Configuration** (sanitized):
```bash
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive
CLOUD_REMOTE_PATH=/pbs-backups
COMPRESSION_TYPE=xz
# ... other relevant settings
```

**Issue description**:
Brief description of the problem...

**Steps to reproduce**:
1. Configure X
2. Run Y
3. Observe error Z

**Expected behavior**:
What should happen...

**Actual behavior**:
What actually happens...

**Logs** (relevant excerpts):
```
[ERROR] Cloud upload failed: connection timeout
...
```

**Additional context**:
Any other relevant information...
```

---

### Use Support Mode

For complex issues requiring developer assistance:

```bash
# Run in support mode (sends debug log to developer)
./build/proxsave --support
```

**Support mode workflow**:
1. Requests GitHub username and issue number
2. Runs backup with DEBUG logging
3. Emails log to `github-support@tis24.it`
4. Requires existing GitHub issue for tracking

**Note**: Logs may contain file paths and hostnames. Credentials are never logged.

---

## Related Documentation

### Configuration & Setup
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference
- **[CLI Reference](CLI_REFERENCE.md)** - All command-line flags
- **[Migration Guide](MIGRATION_GUIDE.md)** - Bash to Go migration

### Operations
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone configuration and troubleshooting
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption setup
- **[Restore Guide](RESTORE_GUIDE.md)** - Restore operations

### Reference
- **[Examples](EXAMPLES.md)** - Real-world scenarios
- **[Developer Guide](DEVELOPER_GUIDE.md)** - Contributing and development

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Quick Diagnostic Checklist

Use this checklist for rapid troubleshooting:

```bash
# 1. Check binary exists and is executable
ls -lh /opt/proxsave/build/proxsave
# Should show: -rwxr-xr-x ... proxsave

# 2. Check configuration file exists
ls -lh /opt/proxsave/configs/backup.env
# Should show: -rw------- ... backup.env

# 3. Test configuration loading
./build/proxsave --dry-run
# Should NOT error on config parsing

# 4. Check disk space
df -h /opt/proxsave
# Should have >2GB free

# 5. Check permissions
ls -la /opt/proxsave/backup /opt/proxsave/log
# backup: drwx------
# log: drwx------

# 6. Test rclone (if cloud enabled)
rclone listremotes
rclone lsf gdrive:
# Should list remote without errors

# 7. Check latest log
tail -50 /opt/proxsave/log/backup-*.log
# Review for obvious errors

# 8. Run debug mode
./build/proxsave --dry-run --log-level debug 2>&1 | less
# Review detailed output for issues
```

---

## FAQ - Common Questions

**Q: Backup succeeds but cloud upload fails - is this a problem?**
A: No. Cloud uploads are non-critical. Local backup succeeded, which is the primary goal. Review cloud configuration and retry.

**Q: How do I test my configuration without running a real backup?**
A: Use `--dry-run` mode: `./build/proxsave --dry-run --log-level debug`

**Q: Logs show warnings about deprecated variables?**
A: Update your configuration: `./build/proxsave --upgrade-config`

**Q: Can I run backup while another backup is in progress?**
A: No. Use a lock file (`BACKUP_PATH/.backup.lock`) to prevent concurrent runs.

**Q: How do I recover from a failed backup?**
A: Delete the incomplete backup file and re-run. The system automatically handles cleanup.

**Q: Encryption is slow - how can I speed it up?**
A: AGE encryption is streaming and shouldn't significantly impact speed. Check compression settings instead.

**Q: Cloud upload is very slow - how can I speed it up?**
A: Increase `RCLONE_TRANSFERS`, use `parallel` mode, check network bandwidth, try different compression.
