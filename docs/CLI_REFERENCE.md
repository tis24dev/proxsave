# Command-Line Reference

Complete reference for all Proxsave command-line options and flags.

## Table of Contents

- [Overview](#overview)
- [Basic Operations](#basic-operations)
- [Installation & Setup](#installation--setup)
- [Encryption & Decryption](#encryption--decryption)
- [Restore Operations](#restore-operations)
- [Logging](#logging)
- [Support & Diagnostics](#support--diagnostics)
- [Command Examples](#command-examples)
- [Scheduling with Cron](#scheduling-with-cron)
- [Related Documentation](#related-documentation)

---

## Overview

The binary `/opt/proxsave/build/proxsave` supports multiple operation modes through command-line flags. All flags can be combined for flexible workflows.

**Command structure**:
```bash
./build/proxsave [FLAGS] [OPTIONS]
```

**Configuration precedence** (highest to lowest):
1. Command-line flags
2. Environment variables
3. Configuration file (`configs/backup.env`)
4. Default values

---

## Interface Modes

Some interactive commands support two interface modes:

### TUI Mode (Default)
- **Full Terminal UI**: Interactive menus, forms, and visual feedback
- **Commands**: `--install`, `--new-install`, `--newkey`, `--decrypt`, `--restore`
- **Best for**: Normal interactive use on local terminals

### CLI Mode (--cli flag)
- **Text-based prompts**: Simple stdin/stdout interaction
- **Activated by**: Adding `--cli` flag to TUI-enabled commands
- **Best for**:
  - Troubleshooting TUI rendering issues
  - Advanced debugging scenarios
  - SSH sessions with limited terminal support
  - Non-standard terminal emulators

**Example**:
```bash
# TUI mode (default) - full terminal interface
./build/proxsave --install

# CLI mode - text prompts only
./build/proxsave --install --cli
```

**Note**: The `--cli` flag **only works** with the 5 commands listed above. All other commands always use CLI mode (no TUI alternative exists).

---

## Basic Operations

### Run Backup

```bash
# Run backup with default config
./build/proxsave

# Use custom config file
./build/proxsave --config /path/to/config.env
./build/proxsave -c /path/to/config.env

# Dry-run mode (test without changes)
./build/proxsave --dry-run
./build/proxsave -n

# Show version
./build/proxsave --version
./build/proxsave -v

# Show help
./build/proxsave --help
./build/proxsave -h
```

### Flag Reference

| Flag | Short | Description |
|------|-------|-------------|
| `--config <path>` | `-c` | Path to configuration file (default: `configs/backup.env`) |
| `--dry-run` | `-n` | Test mode - no actual changes made |
| `--version` | `-v` | Display version information |
| `--help` | `-h` | Show help message |

---

## Installation & Setup

### Installation Wizard

```bash
# Interactive installation wizard (TUI mode - default)
./build/proxsave --install

# Interactive installation wizard (CLI mode - for debugging)
./build/proxsave --install --cli

# Clean reinstall (wipes install dir except build/env/identity, then runs wizard)
./build/proxsave --new-install

# Clean reinstall with CLI mode
./build/proxsave --new-install --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
./build/proxsave --install

# CLI mode - text prompts (for debugging)
./build/proxsave --install --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.

**Wizard workflow**:
1. Generates/updates the configuration file (`configs/backup.env` by default)
2. Optionally configures secondary storage
3. Optionally configures cloud storage (rclone)
4. Optionally enables firewall rules collection (`BACKUP_FIREWALL_RULES=false` by default)
5. Optionally sets up notifications (Telegram, Email; Email defaults to `EMAIL_DELIVERY_METHOD=relay`)
6. Optionally configures encryption (AGE setup)
7. Finalizes installation (symlinks, cron migration, permission checks)

### Configuration Upgrade

```bash
# Upgrade configuration file from embedded template
./build/proxsave --upgrade-config

# Preview configuration upgrade (dry-run)
./build/proxsave --upgrade-config-dry-run
```

**`--upgrade-config` use case**: After installing a new binary version, this command merges your current configuration with the latest embedded template, preserving your values while adding new options.

**Upgrade process**:
1. Reads current `configs/backup.env`
2. Extracts embedded template from binary
3. Merges your values with new template
4. Backs up old config (`backup.env.bak-YYYYMMDD-HHMMSS`)
5. Writes updated configuration
6. Reports added/removed variables

### Binary Upgrade

```bash
# Upgrade binary to latest version
./build/proxsave --upgrade

# Full upgrade including configuration
./build/proxsave --upgrade
./build/proxsave --upgrade-config
```

**`--upgrade` use case**: Update ProxSave binary to the latest version from GitHub releases while preserving your configuration and backup data. The upgrade process is safe and atomic, with checksum verification and automatic permission fixes.

**Upgrade workflow**:
1. Validates configuration file exists
2. Queries GitHub API for latest release version
3. Downloads binary archive and SHA256SUMS from GitHub
4. Creates temporary directory for download
5. Verifies archive integrity using SHA256 checksum
6. Extracts binary from tar.gz archive
7. Atomically replaces current binary (write to .tmp, then rename)
8. Updates symlinks in `/usr/local/bin/` (proxsave, proxmox-backup)
9. Cleans up legacy Bash script symlinks
10. Migrates cron entries (legacy entries are replaced, existing ones using the Go binary are preserved) and fixes file permissions

**Post-upgrade steps**:
1. Configuration file automatically compatible with new version
2. Optionally run `--upgrade-config` to merge new config template variables
3. Test functionality with dry-run: `./build/proxsave --dry-run`
4. Verify backups continue to work as expected
5. Check cron schedule was preserved: `crontab -l`

**Important notes**:
- **Internet required**: Must be able to reach GitHub releases
- **No configuration changes**: `backup.env` is never modified during `--upgrade`
- **Platform support**: Linux only (amd64, arm64)
- **Incompatible flags**: Cannot use with `--install` or `--new-install`
- **Automatic maintenance**: Symlinks, cron (without touching entries already pointing to proxsave/proxmox-backup), and permissions updated automatically
- **Safe replacement**: Old binary is replaced atomically (no backup created)
- **Separate config upgrade**: Use `--upgrade-config` separately to update configuration

See also: [upgrading configuration](#configuration-upgrade)

### Configuration Migration

```bash
# Migrate legacy Bash backup.env to Go configuration (pure migration)
./build/proxsave --env-migration --old-env /opt/proxsave/env/backup.env

# Or let the wizard prompt for the legacy path
./build/proxsave --env-migration

# Preview migration without making changes (dry-run)
./build/proxsave --env-migration-dry-run --old-env /opt/proxsave/env/backup.env

# Or with interactive prompt
./build/proxsave --env-migration-dry-run
```

**`--env-migration` use case**: Pure configuration migration from a legacy Bash `backup.env` to the Go configuration file, using migration rules to translate variable names and semantics.

**Migration workflow**:
1. Prompts for the legacy Bash `backup.env` path (or uses `--old-env` flag if provided)
2. Generates the Go `configs/backup.env` from the embedded template
3. Reads and parses the legacy Bash configuration file
4. Maps variables using migration rules:
   - **SAME**: Variables copied directly (e.g., `BACKUP_ENABLED`, `COMPRESSION_TYPE`)
   - **RENAMED**: Old names automatically converted to new names (e.g., `LOCAL_BACKUP_PATH` → `BACKUP_PATH`)
   - **SEMANTIC CHANGE**: Variables flagged for manual review (e.g., `STORAGE_WARNING_THRESHOLD_*`)
   - **LEGACY**: Bash-only variables skipped (e.g., `ENABLE_EMOJI_LOG`, color codes)
5. Backs up any existing Go configuration (timestamped: `backup.env.bak-YYYYMMDD-HHMMSS`)
6. Writes the new Go configuration with migrated values
7. Reloads/validates the migrated config and prints warnings for manual review

**`--env-migration-dry-run` use case**: Preview mode that shows exactly what would be migrated without making any changes to your system. **Recommended as first step** before running `--env-migration`.

**Dry-run behavior**:
- ✅ Reads and parses the legacy Bash configuration
- ✅ Shows complete migration summary with statistics
- ✅ Lists all SEMANTIC CHANGE variables requiring manual review
- ✅ Displays the mapping for each category (SAME, RENAMED, LEGACY)
- ❌ Does NOT create or modify any files
- ❌ Does NOT run the installer
- ❌ Does NOT create configuration backups

**Why use dry-run first**:
1. **Verify variable mapping** before committing changes
2. **Identify SEMANTIC CHANGE variables** that need attention
3. **Review what gets skipped** (LEGACY category)
4. **Safe exploration** - no risk of breaking existing config

**What gets migrated**:
- ✅ ~70 unchanged variables (SAME category)
- ✅ 16 renamed variables with automatic conversion (RENAMED category)
- ⚠️ 2 variables flagged for manual review (SEMANTIC CHANGE - storage thresholds, cloud path)
- ❌ ~27 legacy variables skipped (LEGACY category - no longer needed)

**Post-migration steps**:
1. Review `configs/backup.env` for SEMANTIC CHANGE warnings
2. Manually convert storage thresholds: `%` used → `GB` free
3. Verify cloud path format: full path → prefix only
4. Test with dry-run: `./build/proxsave --dry-run`
5. Check output for configuration warnings

**Example dry-run output** (`--env-migration-dry-run`):
```
[DRY-RUN] Reading legacy Bash configuration: /opt/proxsave/env/backup.env
[DRY-RUN] Parsing 89 variables from legacy file...

[DRY-RUN] Migration summary:
✓ Would migrate 45 variables (SAME category)
✓ Would convert 12 variables (RENAMED category)
⚠ Manual review required: 2 variables (SEMANTIC CHANGE)
  - STORAGE_WARNING_THRESHOLD_PRIMARY → MIN_DISK_SPACE_PRIMARY_GB
    Bash: "90" (90% used) → Go: needs GB value (e.g., "10")
  - CLOUD_BACKUP_PATH → CLOUD_REMOTE_PATH
    Bash: "/gdrive:backups/folder" → Go: "backups/folder" (prefix only)
ℹ Would skip 18 legacy variables (LEGACY category)

[DRY-RUN] No files created or modified (preview mode)

✓ Dry-run complete. Run without --dry-run to execute migration.
```

**Example real migration output** (`--env-migration`):
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
See migration documentation for conversion details.

Next step: ./build/proxsave --dry-run
```

### Flag Reference

| Flag | Description |
|------|-------------|
| `--install` | Interactive installation wizard |
| `--new-install` | Wipe install directory (preserve build/env/identity) then launch wizard |
| `--upgrade` | Download and install latest ProxSave binary from GitHub releases |
| `--upgrade-config` | Merge current config with latest template |
| `--upgrade-config-dry-run` | Preview config upgrade without changes |
| `--env-migration` | Migrate legacy Bash config to Go |
| `--env-migration-dry-run` | Preview migration without changes |
| `--old-env <path>` | Path to legacy Bash backup.env (used with `--env-migration`) |

---

## Encryption & Decryption

### Generate Encryption Keys

```bash
# Generate new AGE encryption key (TUI mode - default)
./build/proxsave --newkey
./build/proxsave --age-newkey  # Alias

# Generate new AGE encryption key (CLI mode - for debugging)
./build/proxsave --newkey --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
./build/proxsave --newkey

# CLI mode - text prompts (for debugging)
./build/proxsave --newkey --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.

**`--newkey` workflow**:
1. Uses the default recipient file: `${BASE_DIR}/identity/age/recipient.txt` (same as `AGE_RECIPIENT_FILE` in the template)
2. Prompts for one of:
   - **Existing public recipient**: paste an `age1...` recipient
   - **Passphrase-derived**: enter a passphrase (proxsave derives the recipient; the passphrase is **not stored**)
   - **Private key-derived**: paste an `AGE-SECRET-KEY-...` key (not stored; proxsave stores only the derived public recipient)
3. Writes/overwrites the recipient file after confirmation

**Note**: In `--cli` mode (text prompts), you can add multiple recipients. The default TUI flow saves a single recipient; you can always add more by editing the recipient file (one per line).

**For complete encryption guide**, see: **[Encryption Guide](ENCRYPTION.md)**

### Decrypt Backup

```bash
# Decrypt existing backup archive (TUI mode - default)
./build/proxsave --decrypt

# Decrypt existing backup archive (CLI mode - for debugging)
./build/proxsave --decrypt --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
./build/proxsave --decrypt

# CLI mode - text prompts (for debugging)
./build/proxsave --decrypt --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.

**`--decrypt` workflow**:
1. Scans configured storage locations (local/secondary/cloud)
2. Lists available backups with metadata
3. Prompts for destination folder (default `./decrypt`)
4. Requests passphrase or AGE private key (`AGE-SECRET-KEY-...`)
5. Decrypts backup to temporary location
6. Creates a decrypted bundle and moves it to the destination directory

**Output**: Decrypted bundle (e.g., `pve01-backup-20240115-023000.tar.xz.decrypted.bundle.tar`)

### Flag Reference

| Flag | Alias | Description |
|------|-------|-------------|
| `--newkey` | `--age-newkey` | Generate new AGE encryption key |
| `--decrypt` | - | Decrypt existing backup archive |

---

## Restore Operations

### Restore from Backup

```bash
# Restore data from backup to system (TUI mode - default)
./build/proxsave --restore

# Restore data from backup to system (CLI mode - for debugging)
./build/proxsave --restore --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
./build/proxsave --restore

# CLI mode - text prompts (for debugging)
./build/proxsave --restore --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.
**Note**: CLI and TUI run the same workflow logic; `--cli` only changes the interface (prompts/progress rendering), not the restore/decrypt behavior.

**`--restore` workflow** (14 phases):
1. Scans configured storage locations (local/secondary/cloud)
2. Lists available backups with metadata (encrypted or unencrypted)
3. If encrypted, prompts for decryption key/passphrase and decrypts
4. Validates system compatibility (PVE/PBS mismatch warning)
5. Analyzes backup categories
6. Presents restore mode selection:
   - **Full Restore**: All categories
   - **Storage Restore**: PVE/PBS-specific configs
   - **Base System Restore**: Network, SSH, system files
   - **Custom Restore**: Select specific categories
7. For cluster backups: prompts for **SAFE** (export+API) or **RECOVERY** (full restore) mode
8. Shows detailed restore plan with selected categories
9. Requires confirmation: type `RESTORE` to proceed
10. Creates safety backup of existing files
11. Stops services if needed (PVE: pve-cluster, pvedaemon, pveproxy, pvestatd; PBS: proxmox-backup-proxy, proxmox-backup)
12. Extracts selected categories to system root (`/`)
13. Exports export-only categories to separate directory
14. For SAFE cluster mode: offers to apply configs via `pvesh` API
15. Recreates storage/datastore directories, checks ZFS pools
16. Restarts services and displays completion summary

**⚠️ WARNING**: Restore operations overwrite files in-place. **Always test in a VM or snapshot your system first!**

**For complete restore workflows**, see:
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete user guide with all restore modes
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical implementation details
- **[Cluster Recovery](CLUSTER_RECOVERY.md)** - Disaster recovery procedures

### Flag Reference

| Flag | Description |
|------|-------------|
| `--restore` | Run interactive restore workflow (select bundle, decrypt if needed, apply to system) |
| `--cleanup-guards` | Cleanup ProxSave mount guards under `/var/lib/proxsave/guards` (useful after restores with offline mountpoints; use with `--dry-run` to preview) |

---

### Cleanup Mount Guards (Optional)

During some restores (notably PBS datastores on mountpoints under `/mnt`), ProxSave may apply **mount guards** to prevent accidental writes to `/` when the underlying storage is offline/not mounted yet.

If you want to remove those guards manually (optional):

```bash
# Preview (no changes)
./build/proxsave --cleanup-guards --dry-run --log-level debug

# Apply cleanup (requires root)
./build/proxsave --cleanup-guards
```

## Logging

### Set Log Level

```bash
# Set log level
./build/proxsave --log-level debug
./build/proxsave -l info    # debug|info|warning|error|critical
```

**Log level descriptions**:

| Level | Description | Use Case |
|-------|-------------|----------|
| `debug` | Verbose logging with detailed operations | Troubleshooting, development |
| `info` | Standard operational logging | Normal production use |
| `warning` | Warnings and errors only | Minimal logging |
| `error` | Errors only | Critical issues only |
| `critical` | Critical failures only | Emergency mode |

**Log output**:
- **Console**: Colored output (if `USE_COLOR=true`)
- **File**: `LOG_PATH/backup-$(hostname)-YYYYMMDD-HHMMSS.log`

**Debug level vs DEBUG_LEVEL**:
- `--log-level` (CLI flag): Controls logging verbosity
- `DEBUG_LEVEL` (config): Controls operation detail level (`standard`/`advanced`/`extreme`)

### Flag Reference

| Flag | Short | Description |
|------|-------|-------------|
| `--log-level <level>` | `-l` | Set log level: debug\|info\|warning\|error\|critical |

---

## Support & Diagnostics

### Support Mode

```bash
# Run in support mode: force DEBUG logging and send log to developer
./build/proxsave --support
```

**Support mode workflow**:
1. Displays consent notice about log sharing
2. Requests GitHub username
3. Requests GitHub issue number
4. Runs backup with **forced DEBUG logging** (overrides config)
5. Collects complete log file
6. Emails log to `github-support@tis24.it` with metadata
7. Returns log file path for user review

**Requirements**:
- Existing GitHub issue for tracking
- Working local mail delivery on the node (`/usr/sbin/sendmail` via Postfix/Exim/Sendmail)

**Privacy considerations**:
- Logs may contain sensitive information (paths, hostnames, file names)
- Credentials and keys are **never logged**
- Review log file before submitting if concerned

**When to use**:
- Persistent errors that need developer investigation
- Complex configuration issues
- Unexpected behavior requiring detailed diagnostics

### Flag Reference

| Flag | Description |
|------|-------------|
| `--support` | Run in support mode (force DEBUG logging and email log to developer). Available for the standard backup run and `--restore` |

---

## Command Examples

### Standard Operations

```bash
# Standard backup
./build/proxsave

# Dry-run with debug logging
./build/proxsave --dry-run --log-level debug

# Use custom config
./build/proxsave -c /etc/proxmox-backup/prod.env

# Generate encryption keys
./build/proxsave --newkey

# Decrypt specific backup
./build/proxsave --decrypt
# ... follow interactive prompts ...

# Full restore (DANGEROUS - test in VM first!)
./build/proxsave --restore
# ... type RESTORE to confirm ...
```

### Installation & Setup

```bash
# First-time installation
./build/proxsave --install

# Full reset + installation (preserves env/identity)
./build/proxsave --new-install

# Upgrade binary to latest release
./build/proxsave --upgrade

# Upgrade configuration after binary update
./build/proxsave --upgrade-config

# Preview upgrade changes
./build/proxsave --upgrade-config-dry-run

# Full upgrade workflow (binary + config)
./build/proxsave --upgrade
./build/proxsave --upgrade-config
./build/proxsave --dry-run  # Verify everything works

# Migrate from Bash version (preview)
./build/proxsave --env-migration-dry-run --old-env /opt/proxsave/env/backup.env

# Execute migration
./build/proxsave --env-migration --old-env /opt/proxsave/env/backup.env
```

### Troubleshooting

```bash
# Test configuration without running backup
./build/proxsave --dry-run

# Debug mode with extreme verbosity
DEBUG_LEVEL=extreme ./build/proxsave --log-level debug

# Test encryption setup
./build/proxsave --newkey

# Verify backup integrity
./build/proxsave --decrypt --log-level debug

# Support mode for developer assistance
./build/proxsave --support
```

---

## Scheduling with Cron

### Cron Setup

```bash
# Edit crontab
crontab -e

# Daily backup at 2 AM
0 2 * * * /opt/proxsave/build/proxsave >> /var/log/pbs-backup.log 2>&1

# Hourly backup
0 * * * * /opt/proxsave/build/proxsave

# Weekly backup (Sunday 3 AM)
0 3 * * 0 /opt/proxsave/build/proxsave
```

### Recommended Schedules

| Frequency | Cron Expression | Use Case |
|-----------|----------------|----------|
| **Hourly** | `0 * * * *` | High-change environments, critical systems |
| **Every 6 hours** | `0 */6 * * *` | Moderate-change environments |
| **Daily (2 AM)** | `0 2 * * *` | Standard production (recommended) |
| **Daily (off-hours)** | `0 22 * * *` | After business hours |
| **Weekly** | `0 3 * * 0` | Low-change environments, archival |

### Advanced Cron Patterns

```bash
# Weekday backups only (Mon-Fri, 2 AM)
0 2 * * 1-5 /opt/proxsave/build/proxsave

# Multiple daily backups (8 AM, 2 PM, 10 PM)
0 8,14,22 * * * /opt/proxsave/build/proxsave

# First day of month (monthly report)
0 3 1 * * /opt/proxsave/build/proxsave --log-level info

# With custom config
0 2 * * * /opt/proxsave/build/proxsave -c /etc/pbs-prod.env
```

### Logging Best Practices

```bash
# Separate cron log file
0 2 * * * /opt/proxsave/build/proxsave >> /var/log/pbs-cron.log 2>&1

# Rotate logs (logrotate config)
# /etc/logrotate.d/proxmox-backup
/var/log/pbs-cron.log {
    daily
    rotate 7
    compress
    missingok
    notifempty
}
```

---

## Related Documentation

### Configuration
- **[Configuration Guide](CONFIGURATION.md)** - Complete variable reference
- **[Migration Guide](MIGRATION_GUIDE.md)** - Bash to Go migration details

### Operations
- **[Encryption Guide](ENCRYPTION.md)** - AGE encryption setup and usage
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete restore workflows
- **[Cloud Storage Guide](CLOUD_STORAGE.md)** - rclone integration

### Reference
- **[Examples](EXAMPLES.md)** - Real-world usage examples
- **[Troubleshooting](TROUBLESHOOTING.md)** - Common issues and solutions
- **[Developer Guide](DEVELOPER_GUIDE.md)** - Contributing and development

### Main Documentation
- **[README](../README.md)** - Project overview and quick start

---

## Quick Reference

### All Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--help` | `-h` | Show help message |
| `--version` | `-v` | Display version information |
| `--config <path>` | `-c` | Path to configuration file |
| `--dry-run` | `-n` | Test mode - no actual changes |
| `--log-level <level>` | `-l` | Set log level (debug\|info\|warning\|error\|critical) |
| `--cli` | - | Force CLI mode instead of TUI (only for: --install, --new-install, --newkey, --decrypt, --restore) |
| `--install` | - | Interactive installation wizard |
| `--new-install` | - | Wipe install dir (preserve build/env/identity) then run wizard |
| `--upgrade` | - | Download and install latest binary from GitHub releases |
| `--upgrade-config` | - | Upgrade config from embedded template |
| `--upgrade-config-dry-run` | - | Preview config upgrade |
| `--env-migration` | - | Migrate legacy Bash config |
| `--env-migration-dry-run` | - | Preview migration |
| `--old-env <path>` | - | Path to legacy Bash backup.env |
| `--newkey` | - | Generate new AGE encryption key |
| `--age-newkey` | - | Alias for `--newkey` |
| `--decrypt` | - | Decrypt existing backup |
| `--restore` | - | Restore from backup to system |
| `--support` | - | Run in support mode (force DEBUG logging and email log). Available for the standard backup run and `--restore` |

### Common Command Patterns

```bash
# Standard backup
./build/proxsave

# Test before running
./build/proxsave --dry-run --log-level debug

# First-time setup
./build/proxsave --install

# Full reset (preserve env/identity) then setup
./build/proxsave --new-install

# Upgrade binary to latest version
./build/proxsave --upgrade

# After binary upgrade, optionally update config
./build/proxsave --upgrade-config

# Migrate from Bash (safe preview first)
./build/proxsave --env-migration-dry-run
./build/proxsave --env-migration

# Use CLI mode instead of TUI (for debugging)
./build/proxsave --install --cli
./build/proxsave --new-install --cli
./build/proxsave --newkey --cli
./build/proxsave --decrypt --cli
./build/proxsave --restore --cli

# Encryption workflow
./build/proxsave --newkey          # Generate keys
./build/proxsave                   # Run encrypted backup
./build/proxsave --decrypt         # Decrypt when needed

# Restore workflow (test in VM first!)
./build/proxsave --restore

# Troubleshooting
./build/proxsave --dry-run --log-level debug
./build/proxsave --support
```

---

## Environment Variables

While most configuration is in `configs/backup.env`, these environment variables can override settings:

```bash
# Override config file location
CONFIG_FILE=/etc/pbs/prod.env ./build/proxsave

# Force dry-run mode
DRY_RUN=true ./build/proxsave

# Set debug level
DEBUG_LEVEL=extreme ./build/proxsave --log-level debug

# Disable colors
USE_COLOR=false ./build/proxsave
```

**Priority**: Environment variables > Configuration file > Defaults

---

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Success |
| `1` | General error |
| `2` | Configuration error |
| `3` | Security check failed |
| `4` | Insufficient disk space |
| `5` | Backup creation failed |
| `6` | Upload failed (local backup succeeded) |
| `7` | Encryption/decryption failed |

**Note**: Cloud upload failures return exit code `0` (local backup succeeded), but log warnings.
