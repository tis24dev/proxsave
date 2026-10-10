# ProxSave Installation Guide

Install ProxSave, build it from source, maintain an existing installation, or replace a historical installation. After setup, use the [dashboard](DASHBOARD.md#when-the-dashboard-opens) for normal operations.

<!-- site-region: install-proxsave:start -->

## Fast Install

### Direct Install

Run the installer as root on a Linux amd64 Proxmox host. The release installer requires outbound access to GitHub and OpenSSL for signature verification. Other architectures require a [source build](#manual-installation).

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

The script downloads the latest stable release into `/opt/proxsave`. Before installing it, it verifies the pinned ECDSA P-256 signature on `SHA256SUMS`, then checks the archive checksum. Missing or invalid signatures stop installation; there is no checksum-only fallback. See [release verification](PROVENANCE_VERIFICATION.md#release-signature-sha256sumssig) for independent checks.

The installer opens the configuration wizard, installs `/usr/local/bin/proxsave`, and finalizes the selected scheduler. Fresh installations default to the resident daemon. Cloud copies require a separately configured rclone remote; see [cloud storage](CLOUD_STORAGE.md).

For a terminal that cannot render the TUI, use the text prompts:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --cli
```

### Open the dashboard

After installation, run:

```bash
proxsave
```

Use an interactive terminal with both stdin and stdout attached and a nonempty `TERM` other than `dumb`. Bare invocation without these conditions runs a backup directly. Adding any argument also bypasses the dashboard. See [invocation rules](DASHBOARD.md#when-the-dashboard-opens).

To change the wizard settings later, choose **Install > Edit install**, then **Edit existing**. The form covers destinations, notifications, encryption and scheduling. Advanced settings require editing `backup.env`; the [configuration entry guide](CONFIGURATION.md#editing-the-configuration-from-the-dashboard) identifies the exact fields.

### First Backup Workflow

Choose **Backup** in the dashboard. Review the outcome, saved log path and destination results before relying on the installation. Follow the [first backup guide](BACKUP_GUIDE.md#run-and-verify-your-first-backup) for scope, archive verification and a recovery test. An installation or diagnostic check alone does not prove that a restorable backup exists.

### Reinstall safely

**Install > Edit install** keeps the existing installation and lets you edit, overwrite or retain the configuration. **Install > Wipe install** is destructive: it preserves only `build`, `daemon_state`, `env`, `guards`, `identity` and `restore` under the installation directory. With stock paths it deletes local archives in `backup`, run logs in `log`, and `configs/backup.env`.

Copy required archives, logs and configuration to an independent destination before choosing Wipe install. The wipe requires confirmation and defaults to No. Verify the retained copies before proceeding. Wiping is not a configuration reset or a migration tool. For automation flags, use the [CLI installation reference](CLI_REFERENCE.md#installation--setup).

<!-- site-region: install-proxsave:end -->

<!-- site-region: upgrade-proxsave:start -->

## Upgrading ProxSave Binary

### Requirements

An upgrade needs an existing readable configuration, access to GitHub releases, enough disk space, and permissions to replace the binary, normalize ownership and manage the scheduler. Run maintenance as root. Published releases are Linux amd64; upgrading a source build does not rebuild it for another architecture.

Keep an independent copy of your configuration and required backup archives before maintenance. Binary replacement and configuration merging are separate operations, and neither converts historical backup formats.

For an installation from the historical Bash version, follow [legacy migration](#legacy-migration) before choosing an upgrade route. Updating the current configuration template does not translate old Bash settings automatically.

A wipe reinstall is a separate operation that deletes files under the installation directory, including default backup and log paths. Keep independent recovery copies and review the [installation choices](#fast-install) before using it.

### Upgrade from the dashboard

1. Run `proxsave` on an interactive terminal and choose **Upgrade > Check upgrade**.
2. The check runs automatically. `NO UPGRADE (<version>)` means you are current. If a release is available, read the version, release URL and notes before choosing **Run upgrade**. `CHECK FAILED` means the release check failed, not that no upgrade exists.
3. Read the streamed outcome. A successful dashboard upgrade restarts an active daemon once, verifies it, and relaunches the dashboard using the newly installed binary. If restart is deferred because a backup is active or configuration cannot be read, the existing daemon keeps its old binary. Check **Daemon > Status** before considering maintenance complete.
4. Choose **Upgrade > Check config** to review the installed template against `backup.env`. Apply the merge when offered. This is useful separately from a binary upgrade, which also merges configuration during finalization.
5. Use **Post-install > Check** to inspect diagnostics, then follow the [first backup verification](BACKUP_GUIDE.md#run-and-verify-your-first-backup) with **Backup**.

### What Gets Updated

The upgrade verifies the release signature and archive checksum, atomically replaces the executable, refreshes the entrypoint and support documents, normalizes permissions, merges the embedded configuration template and reconciles the scheduler.

Supported and custom configuration values are preserved. Missing keys are added, while retired keys are pruned with a warning: `BASE_DIR`, `CRON_SCHEDULE`, `CRON_HOUR`, `CRON_MINUTE`, `ENABLE_SMART_CHUNKING`, `CHUNK_SIZE_MB`, `CHUNK_THRESHOLD_MB`, `BACKUP_USER_HOMES` and `DAEMON_OPT_OUT`. Matching is by the complete key, case-insensitively. A copy is saved as `backup.env.backup.<timestamp>` before writing; failed merged-config validation restores that copy.

Keep `backup.env` a regular file. Atomic replacement replaces a configuration symlink itself and leaves its target unchanged. For central configuration management, deploy a regular file rather than a symlink.

The scheduler already recorded by `SCHEDULER_MODE` is retained. A daemon is installed automatically only when the upgrade had to introduce that key. To change the engine or time, use **Install > Edit install** and review the [scheduler guide](DAEMON.md).

### The two upgrade paths

The built-in dashboard upgrade performs release discovery, download, verification and binary replacement using the currently installed binary. Fixes to those steps cannot help the same upgrade that first installs them. Since 0.36.0, that binary delegates finalization to the freshly installed binary; older upgrading binaries finalize with their own code.

The external installer verifies and replaces the binary itself, then invokes the new binary to finalize:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

Use this fallback when the built-in upgrade cannot complete or release notes require newer installation logic. The script always targets `/opt/proxsave` and the latest stable release; it can replace a beta with stable and is unsuitable for maintaining a different installation directory without first reviewing that boundary.

### Quick Upgrade

For unattended invocation and auto-confirmation semantics, use the [binary upgrade flags](CLI_REFERENCE.md#binary-upgrade). The dashboard is the normal interactive route.

### Full Upgrade Workflow

The complete operator sequence is Check upgrade, review and Run upgrade, Check config, Daemon Status, Post-install Check, and a verified Backup. Keep configuration merge and daemon alignment visible in the outcome rather than treating binary replacement as completion.

### Troubleshooting

If the check or download fails, inspect the reported URL, connectivity, disk space and saved log. If the binary is updated but daemon status reports a version mismatch, use **Daemon > Restart** after the running backup has finished. If configuration is missing or unreadable, repair it through **Install > Edit install** before retrying. Use the [failure guide](TROUBLESHOOTING.md) for debug-log preparation.

<!-- site-region: upgrade-proxsave:end -->

<!-- site-region: install-from-source:start -->

## Manual Installation

Build from source for development or an architecture without a published release. This requires network access for the repository, Go toolchain and dependencies; it is not an offline installation.

### Prerequisites

Install Git, make, and a Go toolchain satisfying `go.mod` (currently Go 1.26.9). Choose the Go download for your host architecture and verify it according to the Go project's instructions. Install rclone only if you plan to use cloud storage. Do not extract a new Go toolchain over an existing `/usr/local/go` tree.

### Building from Source

Use an empty installation directory. Do not clone over an existing ProxSave installation or its archives.

```bash
mkdir /opt/proxsave
cd /opt/proxsave
git clone --branch main https://github.com/tis24dev/proxsave.git .
go mod download
make build
./build/proxsave --version
```

`make build` sets version metadata through `internal/version.Version`, `Commit` and `Date`. A local build without the compiled support recipient cannot send maintainer support email. Build output is `build/proxsave`.

### Interactive Installation Wizard

For this first invocation, run the newly built binary as root:

```bash
./build/proxsave --install
```

Use `./build/proxsave --install --cli` if the TUI cannot render. After finalization, use bare `proxsave` to open the dashboard. Review the saved configuration and scheduler outcome before running your [first backup](BACKUP_GUIDE.md#run-and-verify-your-first-backup).

If the configuration file already exists, **both TUI and CLI** ask whether to:
- **Overwrite** (start from the embedded template)
- **Edit existing** (use the current file as base and pre-fill the wizard fields)
- **Keep existing & continue** (leave the file untouched and skip the configuration wizard)
- **Cancel** (exit installation)

In **Keep existing & continue** mode, config-dependent post-steps are skipped:
- AGE setup
- Post-install check wizard
- Telegram pairing wizard
- Backup monitoring (healthchecks) verification, and the self-mode ping-URL form with it

Final install steps still run:
- Support docs installation
- Symlink and scheduler finalization (installs the daemon service or the cron entry for the chosen engine)
- Permission normalization

**Wizard prompts:**

1. **Configuration file path** (taken from `--config`, not asked in the wizard): default `configs/backup.env`, shown in the install banner. An absolute path is used as-is; a relative path resolves against the detected install directory (`BASE_DIR`), not the current directory.
2. **Secondary storage**: Optional path for backup/log copies; disabling it clears both saved secondary paths from `backup.env`
3. **Cloud storage (rclone)**: Optional rclone configuration (supports `CLOUD_REMOTE` as a remote name (recommended) or legacy `remote:path`; `CLOUD_LOG_PATH` supports path-only (recommended) or `otherremote:/path`)
4. **Firewall rules**: Optional firewall rules collection toggle (`BACKUP_FIREWALL_RULES=false` by default; supports iptables/nftables)
5. **Notifications**: Enable Telegram (centralized) and Email notifications; Email asks for a delivery mode and defaults to `relay` with `sendmail` failover. Use `pmf` only when you want Proxmox Notifications via `proxmox-mail-forward`.
6. **Encryption**: AGE encryption setup (runs sub-wizard immediately if enabled)
7. **Scheduler engine**: choose the ProxSave local daemon or system cron. Fresh installs and Overwrite default to the daemon (a resident systemd service with a hang watchdog and healthchecks); editing an existing config keeps its current engine. See [DAEMON.md](DAEMON.md).
8. **Healthchecks** (daemon only): with the daemon engine, choose the monitoring mode: `Off`, `ProxSave HC Server` (centralized, zero setup, the default), or `Your own server` (self). Self mode opens a follow-up screen to paste your ping URLs, then a verification screen. Centralized mode goes straight to the verification screen, which also hands you the way into your monitoring portal: a single-use link until you set a portal password, the portal address and your sign-in identity afterwards. With the cron engine this choice is dimmed and forced off. See [HEALTHCHECKS.md](HEALTHCHECKS.md).
9. **Notify level** (daemon with monitoring only): which runs notify you, `Every run`, `Warnings and failures` or `Failures only` (`NOTIFY_ON`). With monitoring off or the cron engine it is not asked and `always` is written. See [CONFIGURATION.md](CONFIGURATION.md#which-runs-get-notified-notify_on).
10. **Frequency**: how often the backup runs, `Daily` (default), `Weekly` or `Monthly` (`SCHEDULER_FREQUENCY`).
11. **Weekday** (weekly only): the day of the weekly backup, default Monday (`SCHEDULER_WEEKDAY`).
12. **Day of month (1-28)** (monthly only): the day of the monthly backup, default 1 (`SCHEDULER_MONTHDAY`). Days 29-31 are not accepted, so no month is skipped.
13. **Run at (HH:MM)**: the backup time (default `02:00`, `SCHEDULER_TIME`). Frequency, day and time are used by whichever engine you chose: the daemon's schedule, or the cron entry the install writes. When `backup.env` records no schedule yet, the install takes it from an existing proxsave cron line; see [DAEMON.md](DAEMON.md#the-schedule-is-inherited-not-reset).
14. **Post-install check (optional)**: Runs a diagnostic simulation, writes logs without creating or uploading archives, and shows actionable warnings like `set BACKUP_*=false to disable`, allowing you to disable unused collectors and reduce WARNING noise
15. **Telegram pairing (optional)**: If Telegram centralized mode is enabled and the installer can load a valid config plus a Server ID, it shows your Server ID and lets you verify pairing with the bot (retry/skip supported). Otherwise installation continues and logs why pairing was skipped.


### Scheduling and the daemon

The selected scheduler is finalized during installation. The resident daemon is the default and is required for Healthchecks transmissions. Cron is an opt-out for schedules the daemon cannot express. Review [scheduling](DAEMON.md) and [monitoring](HEALTHCHECKS.md) for their complete setup and verification procedures.

`BASE_DIR` is detected from the executable. An active `BASE_DIR` assignment is ignored and removed by configuration upgrades. On architectures without published releases, continue maintaining your source build: the binary upgrade downloads Linux amd64 rather than compiling your source.

<!-- site-region: install-from-source:end -->

<!-- site-region: legacy-migration:start -->

## Legacy migration

Current ProxSave has no automatic legacy migration command. Configuration template merging adds current keys and prunes retired ones; it does not translate a historical installation or guarantee that an old archive can be restored.

### Preserve the old installation

1. Identify the historical installation directory, executable or scripts, configuration, archive destinations and scheduler. Record its cron entries, timers or service units before changing them.
2. Copy the original files and required archives to an independent destination. Keep the historical restore tooling and any required keys with those copies. Do not wipe the old installation or overwrite its configuration.
3. Choose the new installation location. The release installer always uses `/opt/proxsave`; if the old installation already occupies that directory, preserve it independently before proceeding. Avoid running old and new schedulers against the same destinations.

### Map settings to the current template

Install the current version following [Fast Install](#fast-install). In the dashboard choose **Install > Edit install** for the supported wizard fields. Map advanced values manually against the [configuration key reference](CONFIGURATION.md#general-settings), using names, units and accepted values from the current template rather than copying the old file wholesale.

Do not carry forward `BASE_DIR` or `CRON_*` assignments. The executable determines the installation directory, while `SCHEDULER_MODE`, `SCHEDULER_FREQUENCY`, its applicable day and `SCHEDULER_TIME` define the current schedule. Retired chunking and user-home toggles are not supported. Recipient configuration and private recovery keys are different: preserve the private keys independently, and verify the current public recipient before encrypting new backups.

### Transfer scheduler ownership and verify

Disable the historical scheduler before enabling the new one. For a historical cron command that the current installer does not recognize, remove or disable that entry manually; do not assume current finalization discovers every old script name. Choose the new engine in **Install > Edit install**, then inspect **Daemon > Status** for a daemon installation. For cron, inspect the actual crontab and confirm there is only one intended schedule. Only the resident daemon transmits Healthchecks monitoring.

Run **Post-install > Check**, review warnings, then create and verify a new **Backup** using the [first backup guide](BACKUP_GUIDE.md#run-and-verify-your-first-backup). Test restore on a disposable compatible host. Inspect historical archives separately with the original tooling where necessary; a successful current backup does not establish historical format compatibility.

Retain the old files until both a new backup and a recovery test have succeeded. If verification fails, stop the new schedule before reinstating the old one, preserving both sets of archives and logs for diagnosis.

<!-- site-region: legacy-migration:end -->
