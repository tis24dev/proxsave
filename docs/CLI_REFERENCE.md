# Command-Line Reference

Use the [dashboard](DASHBOARD.md#when-the-dashboard-opens) for normal interactive operation. This reference covers explicit operations, automation, interface fallback and diagnostic invocations.

<!-- site-region: cli-reference:start -->

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
`--daemon-remove`, none of which have ever honoured it, and with `--restore`, which cannot run
without modifying the system (`DRY_RUN=true` refuses a restore too). The run stops with the
reason printed. Modifiers (`--config`, `--log-level`, `--cli`) do combine with the mode they apply
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

## Quick Reference

### All Flags

The **Dashboard** column names the menu row that does the same thing at a terminal; a dash
means the flag has no menu row.

| Flag | Short | Dashboard | Description |
|------|-------|-----------|-------------|
| `--help` | `-h` | - | Show help message |
| `--version` | `-v` | - | Display version information |
| `--config <path>` | `-c` | - | Path to configuration file. Passing it suppresses the dashboard, like any other flag |
| `--dry-run` | `-n` | - | Simulate a backup without creating or uploading archives; diagnostic logs are still written. Refused with `--upgrade`, `--upgrade-finalize`, `--daemon`, `--daemon-setup`, `--daemon-remove` and `--restore` |
| `--log-level <level>` | `-l` | - | Set log level (debug\|info\|warning\|error\|critical) |
| `--cli` | - | - | Force CLI mode instead of TUI (only for: --install, --new-install, --newkey, --decrypt, --restore) |
| `--install` | - | Install > Edit install | Interactive installation wizard |
| `--new-install` | - | Install > Wipe install | Wipe the install dir, keeping only `build/`, `daemon_state/`, `env/`, `guards/`, `identity/` and `restore/`, then run the wizard. Deletes local backups and `configs/backup.env` with stock paths |
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
| `--cleanup-guards` | - | Cleanup guards | Remove leftover ProxSave mount guards under `<BASE_DIR>/guards` and the legacy `/var/lib/proxsave/guards` (use with `--dry-run` to preview) |
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

## Basic Operations

### Run Backup

```bash
proxsave --backup
proxsave --backup --config /path/to/backup.env
proxsave --dry-run --log-level debug
```

A relative configuration path is resolved against the installation directory, not the current directory. Any argument bypasses the dashboard. Bare invocation without interactive terminal eligibility also runs the normal backup path.

Dry-run writes diagnostic logs but skips archive creation and upload; it is not a restore preview. `DRY_RUN=true` also refuses restore. See [backup verification](BACKUP_GUIDE.md#run-and-verify-your-first-backup) for the complete operational checks.

## Installation & Setup

### Installation Wizard

```bash
proxsave --install
proxsave --install --cli
```

Use [installation](INSTALL.md#fast-install) for bootstrap and wizard behavior. `--new-install` is a destructive reset, preserving only `build`, `daemon_state`, `env`, `guards`, `identity` and `restore`. With stock paths it deletes local archives, logs and `configs/backup.env`. Copy and verify required files independently before invoking it; it is not a migration command.

### Configuration Upgrade

```bash
proxsave --upgrade-config-dry-run
proxsave --upgrade-config
```

The first invocation reports the merge plan without writing configuration. The second applies it. Supported and custom values are preserved, missing keys are added, and retired keys are removed. Validation failure restores the timestamped config backup. Atomic replacement replaces a symlink itself rather than updating its target. See [merge effects](INSTALL.md#what-gets-updated) for the complete pruning list and scheduler boundaries.

### Binary Upgrade

```bash
proxsave --upgrade
proxsave --upgrade y
```

Appending `y` auto-confirms and suppresses interactive release notes. The currently installed binary discovers, downloads, verifies and replaces the release; versions from 0.36.0 delegate finalization to the new binary. Configuration merging is included. See [upgrade procedure](INSTALL.md#upgrading-proxsave-binary) for permissions, failure handling, daemon alignment and the external installer fallback. After an unattended upgrade, `proxsave --show-whatsnew` acknowledges unseen notes.

## Encryption & Decryption

### Generate AGE Key

```bash
proxsave --newkey
proxsave --age-newkey
proxsave --newkey --cli
```

These invoke the same recipient setup. They support existing recipients, passphrase derivation or deriving the public recipient from a private key, and multiple recipients with deduplication. The setup stores public recipients, not the passphrase or supplied private key. See [encryption setup](ENCRYPTION.md).

### Decrypt Backup

```bash
proxsave --decrypt
proxsave --decrypt --cli
```

This interactive selector uses configured local, secondary or cloud sources. Its output is a plaintext bundle, not necessarily the inner compressed archive. Inspect the outer bundle before choosing an inner archive. See [decryption](ENCRYPTION.md#decrypting-backups) for the complete procedure and plaintext handling.

## Restore Operations

### Restore from Backup

```bash
proxsave --restore
proxsave --restore --cli
```

Both run the same interactive restore logic. The flag does not imply unattended confirmation. Restore overwrites selected system files and has no dry-run mode; test on a disposable host and review the plan before confirming. Mode, category compatibility, export-only behavior and cluster SAFE/RECOVERY are separate choices. Use the [restore guide](RESTORE_GUIDE.md) and [cluster recovery guide](CLUSTER_RECOVERY.md) for their prerequisites and scoped safety rules.

### Cleanup Mount Guards (Optional)

```bash
proxsave --cleanup-guards --dry-run --log-level debug
proxsave --cleanup-guards
```

Preview first; applying cleanup requires root. Cleanup removes recorded bind-mount guards and legacy immutable flags only where the actual storage is not mounted. A live storage mount can hide the guard; unmount it safely and retry. Exit `17` means guards remain or the remaining count could not be confirmed. A bind guard clears on reboot, while a legacy immutable flag persists. See [restore guidance](RESTORE_GUIDE.md) before changing storage mount state.

## Logging

### Set Log Level

`proxsave --log-level debug` bypasses the dashboard and runs the normal backup path with debug logging, subject to configuration and runtime checks. It does not open a debug dashboard. Add `--dry-run` when you need diagnostic simulation rather than a real backup.

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
- **File**: `LOG_PATH/backup-<resolved-hostname>-YYYYMMDD-HHMMSS.log`

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

The complete flag table is maintained under [All Flags](#all-flags).

---

## Support & Diagnostics

```bash
proxsave --support
proxsave --restore --support
proxsave --daemon-status --log-level debug
```

Support mode forces debug logging and asks for the GitHub nickname, existing issue number and consent. Once consent is given, it sends the run log automatically at completion; there is no final review screen. A compiled maintainer recipient and working local sendmail are required. It does not use the normal notification relay. Registered secrets are redacted, but arbitrary diagnostics can still contain sensitive information. To review before sharing, use debug logging without support mode and submit only the reviewed log. See [support preparation](TROUBLESHOOTING.md).

### Diagnostics with no flag

Telegram pairing, Healthchecks verification and Post-install checks are dashboard screens. Daemon Restart also has no dedicated flag; the external service command is `systemctl restart proxsave-daemon.service`. Daemon status inspects personal-script readiness without executing scripts. Debug adds UID and path evidence; its exit verdict remains daemon health and binary alignment.

## Command Examples

```bash
# Explicit backup with a separate profile
proxsave --backup -c /etc/proxsave/production.env

# Diagnostic simulation, still writes logs
proxsave --dry-run -l debug

# Normal backup path with debug output, bypasses the dashboard
proxsave --log-level debug

# Read-only daemon status
proxsave --daemon-status
```

### Separate collection profile on a disposable host

A fixture or mounted-root profile is not a sandbox. `SYSTEM_ROOT_PREFIX` affects collection only and is not an environment override. Use the separate configuration and isolation prerequisites in [the disposable-host example](EXAMPLES.md#example-9-test-in-a-chrootfixture): independent backup/log/lock paths and disabled external destinations and notifications. Run only on a disposable compatible host.

```bash
proxsave -c /opt/proxsave/configs/snapshot.env --dry-run --log-level debug
proxsave -c /opt/proxsave/configs/snapshot.env --backup
```

Review the diagnostic log before the second invocation. Both bypass the dashboard; the second creates a real backup under that profile. Neither redirects upgrade, restore, security checks or service management into the fixture root.

## Scheduling with Cron

Scheduler installation, ownership and custom-cadence rules are maintained in the [scheduler guide](DAEMON.md). Do not add cron backups while the daemon is active. The explicit command used in an external schedule is `/usr/local/bin/proxsave --backup`; install/reinstall and daemon removal can replace recognized ProxSave cron entries with the configured schedule. Review ownership before editing a custom cadence.

## Related Documentation

Use [configuration](CONFIGURATION.md#editing-the-configuration-from-the-dashboard) for key definitions, [dashboard navigation](DASHBOARD.md#the-menu) for interactive actions and [troubleshooting](TROUBLESHOOTING.md) for failure diagnosis.

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
# (a restore is refused while DRY_RUN is true, from the environment or from backup.env)
DRY_RUN=true proxsave --backup

# BASE_DIR is not an override; it is detected from the installed executable.
# BASE_DIR in the environment or backup.env is deprecated and ignored.

# PBS restore behavior
# Selected interactively during `--restore` on PBS hosts (Merge vs Clean 1:1).

# Set debug level
DEBUG_LEVEL=extreme proxsave --log-level debug

# Disable colors
USE_COLOR=false proxsave --backup
```

The examples use an explicit operation because environment assignments alone do not suppress the dashboard.

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

<!-- site-region: cli-reference:end -->
