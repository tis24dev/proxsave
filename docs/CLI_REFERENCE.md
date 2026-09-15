# Command-Line Reference

Complete reference for all ProxSave command-line options and flags.

**This page is the automation surface, not the everyday one.** On a normal host you drive
ProxSave from the interactive dashboard: run `proxsave` with no arguments on a terminal and
it opens, with installing, configuring, upgrading, running a backup, restoring, the daemon
and the support report all a keypress away. See [DASHBOARD.md](DASHBOARD.md). Reach for the
flags below when there is no terminal to open the dashboard on: headless hosts, cron entries,
provisioning scripts, remote automation, and recovery when the TUI cannot run.

## Table of Contents

- [Dashboard first, flags for automation](#dashboard-first-flags-for-automation)
- [Overview](#overview)
- [Interface Modes](#interface-modes)
- [Basic Operations](#basic-operations)
- [Installation & Setup](#installation--setup)
- [Encryption & Decryption](#encryption--decryption)
- [Restore Operations](#restore-operations)
- [Logging](#logging)
- [Support & Diagnostics](#support--diagnostics)
- [Command Examples](#command-examples)
- [Scheduling with Cron](#scheduling-with-cron)
- [Related Documentation](#related-documentation)
- [Quick Reference](#quick-reference)
- [Environment Variables](#environment-variables)
- [Exit Codes](#exit-codes)

---

## Dashboard first, flags for automation

`proxsave` with **no arguments at all**, on an interactive terminal, opens the dashboard.
That is the route to use, and the route to give an operator, unless something prevents it.

The gate is narrow, and knowing why matters as soon as you script ProxSave:

- **No arguments at all.** A single flag is enough to skip the dashboard, including a
  harmless one: `proxsave -c /etc/proxsave/prod.env` runs a backup with that config, it does
  not open the menu.
- **A real terminal.** stdin and stdout must both be TTYs, and `TERM` must be set and not
  `dumb`. cron, systemd, pipes and `ssh` without a pty all fail this and run the backup
  directly, which is exactly what a scheduler wants.

Most flags on this page have a dashboard row that runs the same code: some rows set the flag
internally and fall through to the identical flow, the rest call the same function in-session.

| Dashboard menu path | Flag |
|---------------------|------|
| Backup | `--backup` |
| Restore | `--restore` |
| Decrypt | `--decrypt` |
| New key | `--newkey` |
| Install > Edit install | `--install` |
| Install > Wipe install | `--new-install` |
| Upgrade > Check upgrade | `--upgrade` |
| Upgrade > Check config | `--upgrade-config` (its read-only check step is `--upgrade-config-dry-run`) |
| Daemon > Install | `--daemon-setup` |
| Daemon > Disable | `--daemon-remove` |
| Daemon > Status | `--daemon-status` |
| Cleanup guards | `--cleanup-guards` |
| Support | `--support` |
| (opens by itself once per release) | `--show-whatsnew` |

Four dashboard rows have no flag at all. **Telegram**, **Healthchecks** and **Post-install**
are diagnostic check screens that exist only inside the dashboard, and **Daemon > Restart**
restarts the running service without changing the scheduler engine, which no flag does.
Going the other way, `--config`, `--dry-run`, `--log-level` and `--cli`
are run modifiers with no menu row, and `--daemon` is what `proxsave-daemon.service`
executes, never something you type.

**Scheduling**: on a fresh install the resident daemon (`proxsave-daemon.service`) is the
scheduler, and it is the only thing that transmits healthcheck pings; a backup started by
hand or from the dashboard hands its outcome to the daemon to report. Cron is the
legacy/opt-out engine, kept for the schedules the daemon cannot express. See
[DAEMON.md](DAEMON.md), [HEALTHCHECKS.md](HEALTHCHECKS.md) and
[Scheduling with Cron](#scheduling-with-cron) below.

---

## Overview

The binary `/opt/proxsave/build/proxsave`, reachable as `proxsave` through the
`/usr/local/bin/proxsave` symlink the installer writes, supports multiple operation modes
through command-line flags.

**Command structure**:
```bash
proxsave [FLAGS] [OPTIONS]
```

**Mode flags do not all combine.** Each one selects an operation mode, and ProxSave refuses
the combinations that would contradict each other rather than picking one silently: only one
of `--daemon`, `--daemon-setup`, `--daemon-remove`, `--daemon-status` at a time and none of
them with another mode; `--install` not with `--new-install` or `--upgrade`;
`--cleanup-guards` and `--support` each with their own short list of incompatible modes; and
`--dry-run` refused with `--upgrade`, `--upgrade-finalize`, `--daemon`, `--daemon-setup` and
`--daemon-remove`, none of which have ever honoured it. The run stops with the reason
printed. Modifiers (`--config`, `--log-level`, `--cli`) do combine with the mode they apply
to.

**Configuration precedence** (highest to lowest):
1. Command-line flags
2. Environment variables
3. Configuration file (default `configs/backup.env`, resolved under the detected install directory, typically `/opt/proxsave/configs/backup.env`)
4. Default values

---

## Interface Modes

Some interactive commands support two interface modes. All five are reachable from the
dashboard too (Install > Edit install, Install > Wipe install, New key, Decrypt, Restore), so
this only matters when you invoke them by flag.

### TUI Mode (Default)
- **Full Terminal UI**: Interactive menus, forms, and visual feedback
- **Commands**: `--install`, `--new-install`, `--newkey`, `--decrypt`, `--restore`
- **Best for**: Normal interactive use on local terminals
- **Automatic fallback**: these five drop to CLI mode on their own when stdout is not a real
  terminal, so a redirected or piped invocation does not need `--cli` to avoid AltScreen
  escapes in the captured output

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

An operator at a terminal runs a backup from the dashboard's **Backup** row. The forms below
are for cron entries, scripts and headless hosts.

```bash
# Run the backup now (explicit; always skips the interactive dashboard)
proxsave --backup

# Bare invocation: runs the backup when non-interactive (cron, pipe, systemd),
# opens the interactive dashboard on an interactive terminal
proxsave

# Use custom config file. NOTE: passing any flag, this one included, suppresses the
# dashboard, so on a terminal these run the backup instead of opening the menu.
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
| `--config <path>` | `-c` | Path to configuration file (default `configs/backup.env`, resolved under the install dir, e.g. `/opt/proxsave/configs/backup.env`). An absolute path is used as-is; a relative path is joined onto the install dir, not the current directory. |
| `--dry-run` | `-n` | Test mode - no actual changes made. Refused with `--upgrade` and `--upgrade-finalize`: neither has ever honoured it, so the combination is an error rather than a silent full upgrade. Refused with `--daemon`, `--daemon-setup` and `--daemon-remove` for the same reason |
| `--version` | `-v` | Display version information |
| `--help` | `-h` | Show help message |
| `--backup` | | Run the backup now and skip the interactive dashboard. This is the default behavior when proxsave runs non-interactively (cron, pipe, systemd). Dashboard: **Backup**. |
| `--daemon` | | Run as the resident backup daemon (schedules + supervises runs, reports to healthchecks). Invoked by `proxsave-daemon.service`; not run by hand, and it has no dashboard row for that reason. See [docs/DAEMON.md](DAEMON.md) and [docs/HEALTHCHECKS.md](HEALTHCHECKS.md). |
| `--daemon-setup` | | Switch this install to daemon mode: install+enable the service and remove the cron entry. Dashboard: **Daemon > Install** (shown while the host is on cron). |
| `--daemon-remove` | | Revert to the cron scheduler, disable the service, and block future upgrades from reinstalling the daemon. Dashboard: **Daemon > Disable** (shown while the daemon is installed). |
| `--daemon-status` | | Read-only daemon status (scheduler/service state, version/alignment, and personal pre/post script readiness) and exit. `--log-level debug` adds daemon-UID and path-component evidence without executing scripts. Exit code remains based only on daemon health: `0` when running and aligned, non-zero otherwise. Dashboard: **Daemon > Status** (same screen, no exit code). |
| `--show-whatsnew` | | Show the release-notes screen once and exit, then mark it seen. `--upgrade` calls it for you on an interactive terminal, but not under `--upgrade y`; run it by hand after an unattended upgrade to stop the "unseen release notes" warning. Dashboard: opens by itself before the first menu, once per release. |

---

## Installation & Setup

### Installation Wizard

The very first install is the one thing the dashboard cannot do, since there is no binary to
open it with: bootstrap with the installer one-liner in [INSTALL.md](INSTALL.md). Everything
after that is a dashboard row: **Install > Edit install** re-runs the wizard, **Install >
Wipe install** resets first. The flags below are the same two flows for headless hosts and
provisioning scripts.

```bash
# Interactive installation wizard (TUI mode - default)
proxsave --install

# Interactive installation wizard (CLI mode - for debugging)
proxsave --install --cli

# Clean reinstall: wipes the install dir except build/, env/ and identity/, then runs
# the wizard. With stock paths that deletes local backup archives and configs/backup.env.
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
- In **Keep existing & continue** mode, config-dependent post-steps are skipped (encryption setup, post-install audit, Telegram pairing), while finalization steps still run (docs install, symlink and scheduler finalization, permissions normalization).

**Wizard workflow**:
1. Generates/updates the configuration file (`configs/backup.env` by default)
2. Optionally configures secondary storage (`SECONDARY_PATH` required if enabled; `SECONDARY_LOG_PATH` optional; invalid secondary paths are re-prompted/rejected; disabling secondary storage clears both saved secondary paths)
3. Optionally configures cloud storage (rclone)
4. Optionally enables firewall rules collection (`BACKUP_FIREWALL_RULES=false` by default)
5. Optionally sets up notifications (Telegram, Email; Email asks for a delivery mode and defaults to `EMAIL_DELIVERY_METHOD=relay` with `EMAIL_FALLBACK_SENDMAIL=true`)
6. Optionally configures encryption (AGE setup)
7. Optionally selects a daily run time (HH:MM, default `02:00`). On fresh installs the scheduler defaults to the resident daemon; cron is offered as the alternative engine (see [INSTALL.md](INSTALL.md) and [DAEMON.md](DAEMON.md))
8. Optionally runs a post-install dry-run audit and offers to disable unused collectors (actionable hints like `set BACKUP_*=false to disable`)
9. (If Telegram centralized mode is enabled and config + Server ID resolve successfully) Shows Server ID and offers pairing verification (retry/skip supported); otherwise install continues and logs why pairing was skipped
10. Finalizes installation (symlinks, scheduler setup for the chosen engine, permission checks)

**Install log**: The installer writes a session log under `/tmp/proxsave/install-*.log` (includes audit results and Telegram pairing outcome).

### Configuration Upgrade

Dashboard: **Upgrade > Check config** runs the read-only plan below, lists the variables it
would add, and only offers `Apply` when there is something to add. Both steps call the same
functions as the two flags here.

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
4. Backs up old config (`backup.env.backup.YYYYMMDD_HHMMSS`, next to the config file)
5. Writes updated configuration
6. Reports added keys, preserved values, and any merge warnings

**Nothing is ever removed from your configuration.** Keys you set that are not in the
template (including keys that differ from a template key only by upper/lower case) are
preserved in place with their original value and casing, and are reported as such. The
upgrade only adds keys the template has and your config lacks.

If the merged configuration fails validation, the backup from step 4 is restored
automatically and the command reports the error, so a failed upgrade leaves your
configuration as it was.

> **Keep `backup.env` a regular file.** The config upgrade (`--upgrade`, `--upgrade-config`) writes the new configuration atomically (temp file + rename), so if `configs/backup.env` is a **symlink** it is replaced by a regular file and the symlink target is left unchanged. For a centrally managed configuration, deploy a regular `backup.env` (for example copied or templated by your config-management tool) instead of symlinking it.

### Binary Upgrade

At a terminal, upgrade from the dashboard: **Upgrade > Check upgrade** runs the same
`--upgrade` code in-session, showing the available version and the release notes before you
commit to it. The flag forms below are for unattended upgrades and headless hosts.

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

**Which binary executes the upgrade.** `proxsave --upgrade` is run by the binary you already
have. It is the old binary that fetches the release list, downloads the archive, verifies the
signature and checksum, and replaces itself, and that cannot move: a freshly downloaded
binary is untrusted until the installed one has verified it, so it can never be the party
that verifies itself. A fix to the download-and-verify half therefore only takes effect on
the upgrade *after* the one that ships it. From 0.36.0 on, the rest of the upgrade (the
config merge, docs and symlinks, the scheduler reconciliation, permissions, the footer) is
handed to the binary that was just installed via the internal `--upgrade-finalize`, so that
half is already the new release's code; the decision to hand it over lives in the old binary,
so a host coming from a release older than 0.36.0 still finalizes with the old code.

The externally fetched installer is the way around it. It downloads and verifies the release
itself, swaps the new binary in, and only then runs it with `--upgrade --localfile`, so
everything after the download is the new release's code:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

Use it when the built-in upgrade cannot help itself: the release notes say the upgrade or
migration logic changed, you are coming from a release older than 0.36.0, or a built-in
upgrade failed part-way. It installs into `/opt/proxsave` unconditionally (the path is fixed
in the script) and always installs the latest **stable** release. See
[INSTALL.md](INSTALL.md) for the full comparison.

**Upgrade workflow**:
1. Validates configuration file exists
2. Queries GitHub API for latest release version
3. Downloads binary archive, SHA256SUMS and SHA256SUMS.sig from GitHub
4. Creates temporary directory for download
5. Verifies authenticity first (the `SHA256SUMS.sig` ECDSA signature against the release key pinned in the binary), then archive integrity against that authenticated `SHA256SUMS`. Both are mandatory: an unsigned or mismatched release aborts the upgrade, there is no checksum-only fallback
6. Extracts binary from tar.gz archive
7. Atomically replaces current binary (write to .tmp, then rename)
8. Updates the `proxsave` symlink in `/usr/local/bin/` (and removes the legacy `proxmox-backup` symlink if present)
9. Upgrades the configuration file (adds any new keys from the template to `backup.env`, preserving your existing and custom values, after backing up the current file) and fixes file permissions. After a successful binary install, the resident daemon (`proxsave-daemon.service`) is installed only on a host that has never recorded a scheduler engine, i.e. one where this upgrade's config merge had to add `SCHEDULER_MODE`; any host that already carries the key keeps the engine it records. The daemon runs once daily at `SCHEDULER_TIME` (default `02:00`) and does not carry over your crontab schedule, so a hand-edited cron time or any non-daily cadence (hourly, weekly, several times a day) is dropped. Run `--daemon-remove` to stay on cron if you need a non-daily schedule.

**Post-upgrade steps**:
1. New config template keys are merged into `backup.env` automatically (existing and custom values preserved; previous file backed up)
2. Run `--upgrade-config` only to re-run that merge without upgrading the binary
3. Test functionality with dry-run: `proxsave --dry-run`
4. Verify backups continue to work as expected
5. Check the scheduler: `proxsave --daemon-status` for daemon installs, or `crontab -l` on cron installs

**Important notes**:
- **Internet required**: Must be able to reach GitHub releases
- **Configuration kept current**: `--upgrade` merges new template keys into `backup.env`, preserving your existing and custom values and backing up the previous file first; it never changes or removes values you set
- **Platform support**: Linux only (amd64)
- **Incompatible flags**: Cannot use with `--install` or `--new-install`
- **Automatic maintenance**: Symlinks and permissions are updated automatically. The daemon is installed only on a host that has never recorded a scheduler engine; re-run `--install` to change the run time or engine
- **Safe replacement**: Old binary is replaced atomically (no backup created)
- **Standalone config upgrade**: `--upgrade` already merges new template keys; use `--upgrade-config` to run that merge without upgrading the binary

See also: [upgrading configuration](#configuration-upgrade)

### Flag Reference

| Flag | Description |
|------|-------------|
| `--install` | Interactive installation wizard. Dashboard: **Install > Edit install** |
| `--new-install` | Wipe the install directory, keeping only `build/`, `env/` and `identity/`, then launch the wizard. With stock paths this deletes your local backup archives and `configs/backup.env`. Dashboard: **Install > Wipe install** |
| `--upgrade` | Download and install latest ProxSave binary from GitHub releases. Dashboard: **Upgrade > Check upgrade** |
| `--upgrade-config` | Merge current config with latest template. Dashboard: **Upgrade > Check config**, whose `Apply` runs this |
| `--upgrade-config-dry-run` | Preview config upgrade without changes. Dashboard: the check step of **Upgrade > Check config**, which runs it before offering `Apply` |

`--upgrade` and `install.sh` drive a second ProxSave process with six more flags
(`--localfile`, `--upgrade-config-json` and the four `--upgrade-finalize*` ones). They are
documented once, under [Internal Flags](#internal-flags).

---

## Encryption & Decryption

### Generate Encryption Keys

Dashboard: **New key** runs this same flow.

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

Dashboard: **Decrypt** runs this same flow.

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
| `--newkey` | `--age-newkey` | Generate new AGE encryption key. Dashboard: **New key** |
| `--decrypt` | - | Decrypt existing backup archive. Dashboard: **Decrypt** |

---

## Restore Operations

### Restore from Backup

Dashboard: **Restore** runs this same workflow. The flag is what you use on a host recovered
far enough to run the binary but not to paint a TUI, or when the restore is driven from a
script.

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

**WARNING**: Restore operations overwrite files in-place. **Always test in a VM or snapshot your system first!**

**For complete restore workflows**, see:
- **[Restore Guide](RESTORE_GUIDE.md)** - Complete user guide with all restore modes
- **[Restore Technical](RESTORE_TECHNICAL.md)** - Technical implementation details
- **[Cluster Recovery](CLUSTER_RECOVERY.md)** - Disaster recovery procedures

### Flag Reference

| Flag | Description |
|------|-------------|
| `--restore` | Run interactive restore workflow (select bundle, decrypt if needed, apply to system). Dashboard: **Restore** |
| `--cleanup-guards` | Cleanup ProxSave mount guards under `/var/lib/proxsave/guards` (useful after restores with offline mountpoints; use with `--dry-run` to preview). Dashboard: **Cleanup guards**, which previews first and only then offers `Apply` |

---

### Cleanup Mount Guards (Optional)

During some restores (notably PBS datastores and PVE network storages on mountpoints under `/mnt`), ProxSave may apply a **read-only bind-mount guard** over a mountpoint to prevent accidental writes to `/` when the underlying storage is offline/not mounted yet. If the bind mount cannot be created, ProxSave logs a warning and proceeds unguarded, and no longer sets a persistent `chattr +i` immutable flag (older versions did; that flag survived reboots and could silently re-block the mountpoint when the storage was later unmounted).

`--cleanup-guards` unmounts bind-mount guards **and** clears any **legacy** `chattr +i` immutable flags left by older versions. For safety it only acts on mountpoints that are **not currently mounted** (a real mount on top shadows the guard; clearing it then would touch the wrong inode), prints a summary (unmounted / hidden-remaining / immutable-cleared / immutable-pending), and keeps the guard directory until nothing is pending.

Dashboard: **Cleanup guards** does both steps in one screen, running the read-only check
first and offering `Apply` only when guards are found. It calls the same routine as the flag.

```bash
# Preview (no changes)
proxsave --cleanup-guards --dry-run --log-level debug

# Apply cleanup (requires root)
proxsave --cleanup-guards
```

Notes:
- Bringing the storage back online is enough to *use* it again (a real mount stacks on top of the guard automatically); `--cleanup-guards` just removes the leftover guard. A bind-mount guard also clears on reboot. A legacy `chattr +i` flag does **not** clear on reboot; it persists until cleared.
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

The level threshold mutes the **console only** for warnings and above: a warning or
error raised below the chosen level is still counted (footer, exit code) and still
written to the log file, so the artifact shipped with notifications keeps the
evidence. Levels below warning are filtered everywhere, as before.

**`--log-level` vs `DEBUG_LEVEL`**:
- `DEBUG_LEVEL` (config) sets the base log level: `standard` resolves to `info`, `advanced` and `extreme` both resolve to `debug`. Default is `info`.
- `--log-level` (CLI flag) overrides `DEBUG_LEVEL` for that run.
- `--support` forces `debug`, overriding both.

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
- Running in an **unprivileged container/rootless** environment where low-level inventory commands are expected to fail (for example `dmidecode` or `blkid`). In this case, ProxSave still attempts the collection, but logs a `SKIP` (not a `WARNING`) when the failure matches known "missing privileges" patterns.
  - For `blkid`, the skip reason also includes a restore hint: `/etc/fstab` remap may be limited.

### Flag Reference

| Flag | Short | Description |
|------|-------|-------------|
| `--log-level <level>` | `-l` | Set log level: debug\|info\|warning\|error\|critical |

---

## Support & Diagnostics

### Support Mode

Dashboard: **Support** collects the consent and the GitHub metadata in a form and then runs
the same support backup, streamed in the frame. The flag is the headless equivalent, which
asks for the same two answers on stdin.

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
6. Emails the log to the maintainer address baked into the build, with the GitHub username and issue number in the subject
7. Returns log file path for user review

**Requirements**:
- Existing GitHub issue for tracking
- A build with the maintainer recipient compiled in (`EMAIL_SUPPORT`). The recipient is injected at build time, not hardcoded; a build without it (for example a local dev build) skips the email and logs a warning.
- Working local mail delivery on the node (`/usr/sbin/sendmail` via Postfix/Exim/Sendmail). Support mode always hands the email to the local MTA; it does not use the notification relay.

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
| `--support` | Run in support mode (force DEBUG logging and email log to developer). Available for the standard backup run and `--restore`. Dashboard: **Support** |

### Diagnostics with no flag

Three checks exist only inside the dashboard and have no command-line equivalent:
**Telegram** (verify the relay pairing), **Healthchecks** (verify monitoring and show the
portal details) and **Post-install** (re-run the post-install audit). If you need them on a
headless host, open the dashboard over an interactive SSH session; there is no flag that
runs them. `--daemon-status` is the one diagnostic that does have a flag, and it is the one
built to be read by a script.

---

## Command Examples

These are the scripted forms. Most of them are a dashboard row at a terminal; see
[Dashboard first, flags for automation](#dashboard-first-flags-for-automation) for the
mapping.

### Standard Operations

```bash
# Run a backup now (bare `proxsave` opens the dashboard on a TTY)
proxsave --backup

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
# Re-run the install wizard against the current configuration
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

**Cron is the opt-out engine, not the recommended one.** The resident daemon is what a fresh
install schedules with, and it is the only thing that transmits healthcheck pings: on a cron
host, monitoring stays silent (see [HEALTHCHECKS.md](HEALTHCHECKS.md)). Put a host back on
the daemon from the dashboard's **Daemon > Install** row, or with `--daemon-setup`. The rest
of this section is for the hosts that stay on cron because they need a cadence the daemon
cannot express.

> On fresh installs ProxSave schedules backups through the **resident daemon** (`proxsave-daemon.service`) by default; see [DAEMON.md](DAEMON.md). The daemon runs once daily, so every schedule below (hourly, every 6 hours, weekly, several times a day) requires the daemon-less **cron** engine. Do not add a cron entry while the daemon is active, or the backup runs twice.
>
> **ProxSave owns your crontab, so a hand-written schedule does not survive.** `--install`, `--new-install` and `--daemon-remove` each rewrite it: they delete **every** cron line whose command is named `proxsave` or `proxmox-backup`, not only the one they wrote themselves, and append a single daily entry at `SCHEDULER_TIME`. The deletion happens in both scheduler modes; whether the appended line stays depends on where the run ends. `--daemon-remove` ends on cron, so it keeps it. `--install` and `--new-install` write that line first and then, when the selected (or already configured) mode is `daemon`, drop it again while enabling the unit, so a daemon installation ends with no proxsave cron entry, unless the unit install itself fails and the host stays on cron with the line it just wrote. A custom cadence from this section is therefore silently downgraded to daily by a cron reinstall, and removed outright by a daemon one. `--upgrade` is the exception: it only repoints legacy paths and leaves the schedule alone.
>
> The practical order is: run `proxsave --daemon-remove` first, which switches to cron, writes the daily line for you, and records `SCHEDULER_MODE=cron`, **then** edit that line to the cadence you want. Adding a second entry afterwards leaves two, and both will fire.
>
> That record is also what makes the line survive. `--upgrade` installs the daemon only on a host that has never recorded a scheduler engine, i.e. one where the upgrade's own config merge had to add `SCHEDULER_MODE`. Any host whose `backup.env` already carries the key is left as it is, so a hand-edited cadence on a 0.30 or later install survives every upgrade. `--daemon-setup` still removes it, because there you asked for the daemon.

### Cron Setup

Write the entry the way ProxSave writes its own, `<schedule> /usr/local/bin/proxsave
--backup`: every install and upgrade repoints that symlink at the current binary, and
`--backup` pins the non-interactive behaviour, so the run can never land on the dashboard
even if something in the chain allocates a pty.

```bash
# Edit crontab
crontab -e

# Daily backup at 2 AM
0 2 * * * /usr/local/bin/proxsave --backup >> /var/log/pbs-backup.log 2>&1

# Hourly backup
0 * * * * /usr/local/bin/proxsave --backup

# Weekly backup (Sunday 3 AM)
0 3 * * 0 /usr/local/bin/proxsave --backup
```

### Cron Cadences

| Frequency | Cron Expression | Use Case |
|-----------|----------------|----------|
| **Hourly** | `0 * * * *` | High-change environments, critical systems |
| **Every 6 hours** | `0 */6 * * *` | Moderate-change environments |
| **Daily (2 AM)** | `0 2 * * *` | Standard production; this is the one cadence the daemon already covers, so prefer the daemon for it |
| **Daily (off-hours)** | `0 22 * * *` | After business hours |
| **Weekly** | `0 3 * * 0` | Low-change environments, archival |

### Advanced Cron Patterns

```bash
# Weekday backups only (Mon-Fri, 2 AM)
0 2 * * 1-5 /usr/local/bin/proxsave --backup

# Multiple daily backups (8 AM, 2 PM, 10 PM)
0 8,14,22 * * * /usr/local/bin/proxsave --backup

# First day of month (monthly report)
0 3 1 * * /usr/local/bin/proxsave --backup --log-level info

# With custom config
0 2 * * * /usr/local/bin/proxsave --backup -c /etc/pbs-prod.env
```

### Logging Best Practices

```bash
# Separate cron log file
0 2 * * * /usr/local/bin/proxsave --backup >> /var/log/pbs-cron.log 2>&1

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

### The everyday interface
- **[Dashboard](DASHBOARD.md)** - The interactive menu these flags mirror
- **[Resident daemon](DAEMON.md)** - The default scheduler, and how to move a host on or off it
- **[Installation](INSTALL.md)** - Bootstrap install and the two upgrade paths

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

The **Dashboard** column names the menu row that does the same thing at a terminal; a dash
means the flag has no menu row.

| Flag | Short | Dashboard | Description |
|------|-------|-----------|-------------|
| `--help` | `-h` | - | Show help message |
| `--version` | `-v` | - | Display version information |
| `--config <path>` | `-c` | - | Path to configuration file. Passing it suppresses the dashboard, like any other flag |
| `--dry-run` | `-n` | - | Test mode - no actual changes. Refused with `--upgrade`, `--upgrade-finalize`, `--daemon`, `--daemon-setup` and `--daemon-remove` |
| `--log-level <level>` | `-l` | - | Set log level (debug\|info\|warning\|error\|critical) |
| `--cli` | - | - | Force CLI mode instead of TUI (only for: --install, --new-install, --newkey, --decrypt, --restore) |
| `--install` | - | Install > Edit install | Interactive installation wizard |
| `--new-install` | - | Install > Wipe install | Wipe the install dir, keeping only `build/`, `env/` and `identity/`, then run the wizard. Deletes local backups and `configs/backup.env` with stock paths |
| `--upgrade` | - | Upgrade > Check upgrade | Download and install latest binary from GitHub releases. Executed by the OLD binary; from 0.36.0 on it hands the finalize to the new one |
| `--upgrade-config` | - | Upgrade > Check config | Upgrade config from embedded template |
| `--upgrade-config-dry-run` | - | Upgrade > Check config (check step) | Preview config upgrade |
| `--newkey` | - | New key | Generate new AGE encryption key |
| `--age-newkey` | - | New key | Alias for `--newkey` |
| `--decrypt` | - | Decrypt | Decrypt existing backup |
| `--restore` | - | Restore | Restore from backup to system |
| `--backup` | - | Backup | Run the backup now and skip the interactive dashboard (default when non-interactive, e.g. cron) |
| `--daemon` | - | - | Run as the resident backup daemon (installed as `proxsave-daemon.service`; not run by hand) |
| `--daemon-setup` | - | Daemon > Install | Switch this install to daemon mode (install+enable the service, remove the cron entry) |
| `--daemon-remove` | - | Daemon > Disable | Revert to the cron scheduler, disable the service, and block future upgrades from reinstalling the daemon |
| `--daemon-status` | - | Daemon > Status | Read-only daemon and personal-script status; add `--log-level debug` for UID/path evidence. Scripts are not executed; exit is `0` only when the daemon is running and aligned |
| `--show-whatsnew` | - | (opens by itself) | Show the release-notes screen once and exit, then mark it seen |
| `--cleanup-guards` | - | Cleanup guards | Remove leftover ProxSave mount guards under `/var/lib/proxsave/guards` (use with `--dry-run` to preview) |
| `--support` | - | Support | Run in support mode (force DEBUG logging and email log). Available for the standard backup run and `--restore` |

### Internal Flags

These exist so one ProxSave process can drive another. They are documented for completeness
and for reading a debug log; do not put them in a script of your own.

| Flag | Used by | Description |
|------|---------|-------------|
| `--localfile` | `install.sh --upgrade`, `upgrade-beta.sh` | Only with `--upgrade`: skip the release check and the download, and finalize using the binary already on disk, which those scripts have already swapped in themselves. Rejected on its own: `--localfile` without `--upgrade` is an error |
| `--upgrade-config-json` | `--upgrade` | Upgrade the config and print a JSON summary to stdout |
| `--upgrade-finalize` | `--upgrade` (installed version 0.36.0 or newer) | Run only the post-install finalize phase (config merge, docs/symlinks, daemon migrate and restart, permissions, footer, release notes) and exit. `--upgrade` re-invokes the freshly installed binary with it, so the finalize policy that runs belongs to the release being installed rather than to the one being replaced. Run by hand it restarts the daemon and prints an upgrade footer for a version nobody installed |
| `--upgrade-finalize-version <version>` | `--upgrade-finalize` | The version the caller installed, for the footer the child prints. The child cannot read it from its own build info on the `--localfile` path, where the on-disk binary may predate the tag |
| `--upgrade-finalize-skip-whatsnew` | `--upgrade-finalize` | Do not open the release-notes screen. `--upgrade` forwards it when nobody is there to close it (`--upgrade y`) or when the dashboard owns the presentation |
| `--upgrade-finalize-skip-daemon-restart` | `--upgrade-finalize` | Do not restart the resident daemon, because the caller restarts it itself. `--upgrade` forwards this when the dashboard is driving, since the suppression lives in a package variable a child process cannot see. Without it the daemon is restarted twice |

### Common Command Patterns

For scripts, cron and headless hosts. At a terminal, `proxsave` with no arguments opens the
dashboard and every pattern here except the modifiers is a row on it.

```bash
# Run a backup now (bare `proxsave` opens the dashboard on a TTY)
proxsave --backup

# Test before running
proxsave --dry-run --log-level debug

# Re-run the install wizard
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
proxsave --backup          # Run encrypted backup
proxsave --decrypt         # Decrypt when needed

# Restore workflow (test in VM first!)
proxsave --restore

# Troubleshooting
proxsave --dry-run --log-level debug
proxsave --support
```

---

## Environment Variables

While most configuration is in `configs/backup.env`, some settings can also be set in the environment for a single run.

**Only a fixed allowlist of keys is honoured.** ProxSave reads a hardcoded list of about a hundred `backup.env` names out of the environment; every other key in the shipped template, roughly half of them, is ignored. There is no warning and no log line when that happens: the run silently uses the value from the file. The three PBS auth keys (`PBS_REPOSITORY`, `PBS_PASSWORD`, `PBS_FINGERPRINT`) are handled separately and the environment wins over the file for them. For anything else not on the list, the file is the only way to set it.

Two consequences worth knowing:

- Keys that look obviously overridable often are not. `SYSTEM_ROOT_PREFIX` and `HOST_BACKUP_MODE` are two examples, so `SYSTEM_ROOT_PREFIX=/mnt/snapshot proxsave` does not back up the mounted root, it backs up the live one.
- An empty value is treated as absent, so a key cannot be cleared from the environment: `GOTIFY_TOKEN= proxsave` leaves the file's token in place.

The ones below are on the list and are the ones worth using:

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

**Priority**: for a key on the allowlist, environment variable > configuration file > default. One exception: if the file still carries the **legacy alias** of that key (see Legacy key names in [CONFIGURATION.md](CONFIGURATION.md)), the legacy line in the file wins over the environment, because the allowlist only carries the canonical name. For every other key the environment is not consulted at all. `BASE_DIR` is always runtime-detected and is not overridable from either place.

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
| `16` | backup skipped | No backup was performed, for a benign reason: another backup already held the lock, or `BACKUP_ENABLED=false`. Not a failure |
| `17` | guards still in place | `--cleanup-guards` only. The cleanup itself ran fine, but the storage is still locked: guard mounts or immutable flags are left behind (typically hidden under a live mount), or the remaining count could not be confirmed. Also returned by `--cleanup-guards --dry-run` when it finds guards. Not a failure: unmount the datastore and retry |
| `130` | interrupted | The run was cancelled with Ctrl+C (128 plus SIGINT) |

**Note**: `1`, `16`, `17` and `130` are the non-zero codes that do not mean something
went wrong: `1` is also what a run that succeeded with warnings returns, `16` means no
backup was performed for a benign reason, `17` means a guard cleanup ran fine but the
storage is still locked, and `130` means the run was cancelled by hand. Only `2` through
`15` are unambiguous failures. A wrapper of the form `proxsave --backup || alert` will
page you on warning-only runs, every time two runs overlap, and on anything you Ctrl+C,
unless it excludes them. For `--cleanup-guards`, `17` is the one to act on but not to
report as a bug, and `1` means the opposite of what it means elsewhere: there it is the
cleanup itself failing, which is a different remedy.

**Note**: `--log-level` does not change any exit code. Since 0.34.0 the threshold is a
console filter only (see Log levels above), so a warning raised under `--log-level error`
is still counted and still promotes an otherwise clean run to `1`. In earlier releases the
same warning was dropped before the counters and the run exited `0`, so a wrapper that
relied on a high `--log-level` to keep exit codes quiet will start reporting `1` on the
same hosts. Filter on the code, not on the log level.

**Note**: Cloud storage is non-critical. A cloud upload failure does **not** abort the
run with a storage error (`5`): the local backup is kept, but the failure is recorded as a
warning, so the run finishes with a non-zero exit code (`1`, generic error), not `0`.
