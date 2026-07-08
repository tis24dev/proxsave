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
proxsave [FLAGS] [OPTIONS]
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
proxsave --install

# CLI mode - text prompts only
proxsave --install --cli
```

**Note**: The `--cli` flag **only works** with the 5 commands listed above. All other commands always use CLI mode (no TUI alternative exists).

---

## Basic Operations

### Run Backup

```bash
# Run backup with default config
proxsave

# Use custom config file
proxsave --config /path/to/config.env
proxsave -c /path/to/config.env

# Dry-run mode (test without changes)
proxsave --dry-run
proxsave -n

# Show version
proxsave --version
proxsave -v

# Show help
proxsave --help
proxsave -h
```

### Flag Reference

| Flag | Short | Description |
|------|-------|-------------|
| `--config <path>` | `-c` | Path to configuration file (default: `configs/backup.env`) |
| `--dry-run` | `-n` | Test mode - no actual changes made |
| `--version` | `-v` | Display version information |
| `--help` | `-h` | Show help message |
| `--daemon` | | Run as the resident backup daemon (schedules + supervises runs, reports to healthchecks). Invoked by `proxsave-daemon.service`; not run by hand. See [docs/DAEMON.md](DAEMON.md). |
| `--daemon-setup` | | Switch this install to daemon mode: install+enable the service and remove the cron entry. |
| `--daemon-remove` | | Revert to the cron scheduler, disable the service, and stop future upgrades from reinstalling the daemon. |

---

## Installation & Setup

### Installation Wizard

```bash
# Interactive installation wizard (TUI mode - default)
proxsave --install

# Interactive installation wizard (CLI mode - for debugging)
proxsave --install --cli

# Clean reinstall (wipes install dir except build/env/identity, then runs wizard)
proxsave --new-install

# Clean reinstall with CLI mode
proxsave --new-install --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
proxsave --install

# CLI mode - text prompts (for debugging)
proxsave --install --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.

**Existing configuration**:
- If the configuration file already exists, **both TUI and CLI** now offer the same choices:
  - **Overwrite** (start from embedded template)
  - **Edit existing** (use current file as base and pre-fill wizard fields)
  - **Keep existing & continue** (leave file untouched and skip configuration wizard)
  - **Cancel** (abort installation)
- In **Keep existing & continue** mode, config-dependent post-steps are skipped (encryption setup, post-install audit, Telegram pairing), while finalization steps still run (docs install, symlink/cron finalization, permissions normalization).

**Wizard workflow**:
1. Generates/updates the configuration file (`configs/backup.env` by default)
2. Optionally configures secondary storage (`SECONDARY_PATH` required if enabled; `SECONDARY_LOG_PATH` optional; invalid secondary paths are re-prompted/rejected; disabling secondary storage clears both saved secondary paths)
3. Optionally configures cloud storage (rclone)
4. Optionally enables firewall rules collection (`BACKUP_FIREWALL_RULES=false` by default)
5. Optionally sets up notifications (Telegram, Email; Email asks for a delivery method and defaults to `EMAIL_DELIVERY_METHOD=relay` with `EMAIL_FALLBACK_SENDMAIL=true`)
6. Optionally configures encryption (AGE setup)
7. Optionally selects a cron time (HH:MM, default `02:00`) for the `proxsave` cron entry in both CLI and TUI install flows
8. Optionally runs a post-install dry-run audit and offers to disable unused collectors (actionable hints like `set BACKUP_*=false to disable`)
9. (If Telegram centralized mode is enabled and config + Server ID resolve successfully) Shows Server ID and offers pairing verification (retry/skip supported); otherwise install continues and logs why pairing was skipped
10. Finalizes installation (symlinks, cron migration, permission checks)

**Install log**: The installer writes a session log under `/tmp/proxsave/install-*.log` (includes audit results and Telegram pairing outcome).

### Configuration Upgrade

```bash
# Upgrade configuration file from embedded template
proxsave --upgrade-config

# Preview configuration upgrade (dry-run)
proxsave --upgrade-config-dry-run
```

**`--upgrade-config` use case**: After installing a new binary version, this command merges your current configuration with the latest embedded template, preserving your values while adding new options.

**Upgrade process**:
1. Reads current `configs/backup.env`
2. Extracts embedded template from binary
3. Merges your values with new template
4. Backs up old config (`backup.env.bak-YYYYMMDD-HHMMSS`)
5. Writes updated configuration
6. Reports added/removed variables

> **Keep `backup.env` a regular file.** The config upgrade (`--upgrade`, `--upgrade-config`) writes the new configuration atomically (temp file + rename), so if `configs/backup.env` is a **symlink** it is replaced by a regular file and the symlink target is left unchanged. For a centrally managed configuration, deploy a regular `backup.env` (for example copied or templated by your config-management tool) instead of symlinking it.

### Binary Upgrade

```bash
# Upgrade binary to latest version
proxsave --upgrade

# Non-interactive upgrade (auto-confirm)
proxsave --upgrade y

# Full upgrade including configuration
proxsave --upgrade
proxsave --upgrade-config
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
8. Updates the `proxsave` symlink in `/usr/local/bin/` (and removes the legacy `proxmox-backup` symlink if present)
9. Upgrades the configuration file (adds any new keys from the template to `backup.env`, preserving your existing and custom values, after backing up the current file) and fixes file permissions. The cron schedule is left untouched (re-run `--install` to change it).

**Post-upgrade steps**:
1. New config template keys are merged into `backup.env` automatically (existing and custom values preserved; previous file backed up)
2. Run `--upgrade-config` only to re-run that merge without upgrading the binary
3. Test functionality with dry-run: `proxsave --dry-run`
4. Verify backups continue to work as expected
5. Check cron schedule was preserved: `crontab -l`

**Important notes**:
- **Internet required**: Must be able to reach GitHub releases
- **Configuration kept current**: `--upgrade` merges new template keys into `backup.env`, preserving your existing and custom values and backing up the previous file first; it never changes or removes values you set
- **Platform support**: Linux only (amd64)
- **Incompatible flags**: Cannot use with `--install` or `--new-install`
- **Automatic maintenance**: Symlinks and permissions are updated automatically; the cron schedule is left untouched (re-run `--install` to change it)
- **Safe replacement**: Old binary is replaced atomically (no backup created)
- **Standalone config upgrade**: `--upgrade` already merges new template keys; use `--upgrade-config` to run that merge without upgrading the binary

See also: [upgrading configuration](#configuration-upgrade)

### Flag Reference

| Flag | Description |
|------|-------------|
| `--install` | Interactive installation wizard |
| `--new-install` | Wipe install directory (preserve build/env/identity) then launch wizard |
| `--upgrade` | Download and install latest ProxSave binary from GitHub releases |
| `--upgrade-config` | Merge current config with latest template |
| `--upgrade-config-dry-run` | Preview config upgrade without changes |

---

## Encryption & Decryption

### Generate Encryption Keys

```bash
# Generate new AGE encryption key (TUI mode - default)
proxsave --newkey
proxsave --age-newkey  # Alias

# Generate new AGE encryption key (CLI mode - for debugging)
proxsave --newkey --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
proxsave --newkey

# CLI mode - text prompts (for debugging or when TUI rendering is unavailable)
proxsave --newkey --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.

**`--newkey` workflow**:
1. Uses the configured `AGE_RECIPIENT_FILE` when present; otherwise falls back to `${BASE_DIR}/identity/age/recipient.txt`
2. Prompts for one of:
   - **Existing public recipient**: paste an `age1...` recipient
   - **Passphrase-derived**: enter a passphrase (proxsave derives the recipient; the passphrase is **not stored**)
   - **Private key-derived**: paste an `AGE-SECRET-KEY-...` key (not stored; proxsave stores only the derived public recipient)
3. Writes/overwrites the recipient file after confirmation

**Note**: Both CLI and TUI `--newkey` flows support adding multiple recipients and de-duplicate repeated entries before saving.

**For complete encryption guide**, see: **[Encryption Guide](ENCRYPTION.md)**

### Decrypt Backup

```bash
# Decrypt existing backup archive (TUI mode - default)
proxsave --decrypt

# Decrypt existing backup archive (CLI mode - for debugging)
proxsave --decrypt --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
proxsave --decrypt

# CLI mode - text prompts (for debugging)
proxsave --decrypt --cli
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
proxsave --restore

# Restore data from backup to system (CLI mode - for debugging)
proxsave --restore --cli
```

**Interface modes**:
```bash
# TUI mode (default) - terminal interface
proxsave --restore

# CLI mode - text prompts (for debugging)
proxsave --restore --cli
```

**Use `--cli` when**: TUI rendering issues occur or advanced debugging is needed.
**Note**: CLI and TUI run the same workflow logic; `--cli` only changes the interface (prompts/progress rendering), not the restore/decrypt behavior.

**`--restore` workflow** (16 phases):
1. Scans configured storage locations (local/secondary/cloud)
2. Lists available backups with metadata (encrypted or unencrypted)
3. If encrypted, prompts for decryption key/passphrase and decrypts
4. Detects the current host role (`pve`, `pbs`, `dual`, or `unknown`)
5. Validates compatibility using capability overlap and backup targets
   - exact match: proceed normally
   - partial match: continue with warning, then filter categories automatically
   - no overlap: warn strongly before continuing
6. Analyzes backup categories
7. Presents restore mode selection:
   - **Full Restore**: all compatible categories
   - **Storage Restore**: storage/datastore-focused categories
   - **Base System Restore**: network, SSH, system files
   - **Custom Restore**: select specific categories
8. For cluster backups: prompts for **SAFE** (export+API) or **RECOVERY** (full restore) mode
9. Shows detailed restore plan with selected categories
10. Requires confirmation: type `RESTORE` to proceed
11. Creates safety backup of existing files
12. Stops services if needed (PVE: pve-cluster, pvedaemon, pveproxy, pvestatd; PBS: proxmox-backup-proxy, proxmox-backup)
13. Extracts selected categories to system root (`/`)
14. Exports export-only categories to separate directory
15. For SAFE cluster mode: offers to apply configs via `pvesh` API
16. Recreates storage/datastore directories, checks ZFS pools, restarts services, and displays completion summary

**Compatibility model**:
- `dual` backups persist explicit targets (`pve`, `pbs`)
- restoring a `dual` backup to a single-role host is allowed
- ProxSave restores only categories compatible with the current host role
- `common` categories remain available across roles

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

During some restores (notably PBS datastores and PVE network storages on mountpoints under `/mnt`), ProxSave may apply a **read-only bind-mount guard** over a mountpoint to prevent accidental writes to `/` when the underlying storage is offline/not mounted yet. If the bind mount cannot be created, ProxSave logs a warning and proceeds unguarded — it no longer sets a persistent `chattr +i` immutable flag (older versions did; that flag survived reboots and could silently re-block the mountpoint when the storage was later unmounted).

`--cleanup-guards` unmounts bind-mount guards **and** clears any **legacy** `chattr +i` immutable flags left by older versions. For safety it only acts on mountpoints that are **not currently mounted** (a real mount on top shadows the guard; clearing it then would touch the wrong inode), prints a summary (unmounted / hidden-remaining / immutable-cleared / immutable-pending), and keeps the guard directory until nothing is pending.

```bash
# Preview (no changes)
proxsave --cleanup-guards --dry-run --log-level debug

# Apply cleanup (requires root)
proxsave --cleanup-guards
```

Notes:
- Bringing the storage back online is enough to *use* it again (a real mount stacks on top of the guard automatically); `--cleanup-guards` just removes the leftover guard. A bind-mount guard also clears on reboot. A legacy `chattr +i` flag does **not** clear on reboot — it persists until cleared.
- To clear a legacy flag while the storage is mounted: unmount it, run `--cleanup-guards` again (or `chattr -i <mountpoint>`), then remount.
- If you deleted `/var/lib/proxsave/guards` manually and a mountpoint is still read-only, ProxSave has no record left: check `lsattr -d <mountpoint>` and run `chattr -i <mountpoint>` while the storage is unmounted.

## Logging

### Set Log Level

```bash
# Set log level
proxsave --log-level debug
proxsave -l info    # debug|info|warning|error|critical
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

### Log Labels (PHASE/STEP/SKIP)

Some log lines use a label to make the output easier to scan:

| Label | Level | Meaning |
|-------|-------|---------|
| `PHASE` | `info` | High-level workflow phase marker |
| `STEP` | `info` | A notable step within a phase |
| `SKIP` | `info` | Optional item intentionally skipped or not applicable |

**Common `SKIP` examples**:
- A feature is disabled by configuration.
- A non-critical CLI tool is not installed.
- Running in an **unprivileged container/rootless** environment where low-level inventory commands are expected to fail (for example `dmidecode` or `blkid`). In this case, ProxSave still attempts the collection, but logs a `SKIP` (not a `WARNING`) when the failure matches known “missing privileges” patterns.
  - For `blkid`, the skip reason also includes a restore hint: `/etc/fstab` remap may be limited.

### Flag Reference

| Flag | Short | Description |
|------|-------|-------------|
| `--log-level <level>` | `-l` | Set log level: debug\|info\|warning\|error\|critical |

---

## Support & Diagnostics

### Support Mode

```bash
# Run in support mode: force DEBUG logging and send log to developer
proxsave --support
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
proxsave

# Dry-run with debug logging
proxsave --dry-run --log-level debug

# Use custom config
proxsave -c /etc/proxmox-backup/prod.env

# Generate encryption keys
proxsave --newkey

# Decrypt specific backup
proxsave --decrypt
# ... follow interactive prompts ...

# Full restore (DANGEROUS - test in VM first!)
proxsave --restore
# ... type RESTORE to confirm ...
```

### Installation & Setup

```bash
# First-time installation
proxsave --install

# Full reset + installation (preserves build/env/identity)
proxsave --new-install

# Upgrade binary to latest release
proxsave --upgrade

# Upgrade configuration after binary update
proxsave --upgrade-config

# Preview upgrade changes
proxsave --upgrade-config-dry-run

# Full upgrade workflow (binary + config)
proxsave --upgrade
proxsave --upgrade-config
proxsave --dry-run  # Verify everything works
```

### Troubleshooting

```bash
# Test configuration without running backup
proxsave --dry-run

# Debug mode with extreme verbosity
DEBUG_LEVEL=extreme proxsave --log-level debug

# Test encryption setup
proxsave --newkey

# Verify backup integrity
proxsave --decrypt --log-level debug

# Support mode for developer assistance
proxsave --support
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
# /etc/logrotate.d/proxsave
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
| `--newkey` | - | Generate new AGE encryption key |
| `--age-newkey` | - | Alias for `--newkey` |
| `--decrypt` | - | Decrypt existing backup |
| `--restore` | - | Restore from backup to system |
| `--support` | - | Run in support mode (force DEBUG logging and email log). Available for the standard backup run and `--restore` |

### Common Command Patterns

```bash
# Standard backup
proxsave

# Test before running
proxsave --dry-run --log-level debug

# First-time setup
proxsave --install

# Full reset (preserve build/env/identity) then setup
proxsave --new-install

# Upgrade binary to latest version
proxsave --upgrade

# After binary upgrade, optionally update config
proxsave --upgrade-config

# Use CLI mode instead of TUI (for debugging)
proxsave --install --cli
proxsave --new-install --cli
proxsave --newkey --cli
proxsave --decrypt --cli
proxsave --restore --cli

# Encryption workflow
proxsave --newkey          # Generate keys
proxsave                   # Run encrypted backup
proxsave --decrypt         # Decrypt when needed

# Restore workflow (test in VM first!)
proxsave --restore

# Troubleshooting
proxsave --dry-run --log-level debug
proxsave --support
```

---

## Environment Variables

While most configuration is in `configs/backup.env`, these environment variables can override settings:

```bash
# Config file location: there is no env var for this; use the -c / --config CLI flag
proxsave -c /etc/pbs/prod.env

# Dry-run mode: overridden via this environment variable
DRY_RUN=true proxsave

# BASE_DIR is not an override; it is detected from the installed executable.
# BASE_DIR in the environment or backup.env is deprecated and ignored.

# PBS restore behavior
# Selected interactively during `--restore` on PBS hosts (Merge vs Clean 1:1).

# Set debug level
DEBUG_LEVEL=extreme proxsave --log-level debug

# Disable colors
USE_COLOR=false proxsave
```

**Priority**: Environment variables > Configuration file > Defaults, except `BASE_DIR`, which is always runtime-detected.

---

## Exit Codes

| Code | Name | Meaning |
|------|------|---------|
| `0` | success | Execution completed successfully |
| `1` | generic error | Unspecified generic error |
| `2` | configuration error | Configuration error |
| `3` | environment error | Invalid or unsupported Proxmox environment |
| `4` | backup error | Error during the backup operation (generic) |
| `5` | storage error | Error during storage operations |
| `6` | network error | Network error (upload, notifications, etc.) |
| `7` | permission error | Permission error |
| `8` | verification error | Error during integrity verification |
| `9` | collection error | Error during collection of configuration files |
| `10` | archive error | Error while creating the archive |
| `11` | compression error | Error during compression |
| `12` | disk space error | Insufficient disk space |
| `13` | panic error | Unhandled panic caught |
| `14` | security error | Errors detected by the security check |
| `15` | encryption error | Error during encryption setup or processing |

**Note**: Cloud storage is non-critical. A cloud upload failure does **not** abort the
run with a storage error (`5`): the local backup is kept, but the failure is recorded as a
warning, so the run finishes with a non-zero exit code (`1`, generic error) — not `0`.
