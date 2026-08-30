# ProxSave Installation Guide

## Table of Contents

- [Fast Install](#fast-install)
  - [Direct Install](#direct-install)
  - [First Backup Workflow](#first-backup-workflow)
- [Upgrading ProxSave Binary](#upgrading-proxsave-binary)
  - [Quick Upgrade](#quick-upgrade)
  - [What Gets Updated](#what-gets-updated)
  - [Full Upgrade Workflow](#full-upgrade-workflow)
- [Manual Installation](#manual-installation)
  - [Prerequisites](#prerequisites)
  - [Building from Source](#building-from-source)
  - [Interactive Installation Wizard](#interactive-installation-wizard)
  - [Scheduling and the daemon](#scheduling-and-the-daemon)

## Fast Install

### Direct Install

1. Download & start Install

   ```bash
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
   ```

   or, if you need a fully clean reinstall. This keeps `build/`, `env/` and `identity/` and **deletes everything else under the base directory**, which with stock paths means your local backup archives in `backup/`, the logs in `log/`, and `configs/backup.env`. Both installers ask for confirmation first, defaulting to no. Do not run it as a way to reset the configuration unless your backups also live on secondary or cloud storage:

   ```bash
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --new-install
   ```

   Append `--cli` to run the text-mode installer instead of the TUI (for example `... install.sh)" _ --cli`).

2. Run your first backup

   ```bash
   proxsave --backup
   ```

   Bare `proxsave` on a terminal opens the interactive dashboard instead, where `Backup` is the first entry. It runs the backup directly only when there is no terminal attached, which is what cron and the daemon do. See [DASHBOARD.md](DASHBOARD.md).

> **Release integrity & authenticity.** `install.sh` and `proxsave --upgrade`
> verify every release before installing it: `SHA256SUMS` is checked against the
> project's pinned **ECDSA P-256** signature (`SHA256SUMS.sig`), then the archive
> is checked against `SHA256SUMS`. A missing or invalid signature aborts the
> install/upgrade, with no fallback to checksum-only. `install.sh` requires
> `openssl` for this (preinstalled on Proxmox); the Go upgrade verifies it
> natively. To verify a download yourself, see
> [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md#release-signature-sha256sumssig).

### First Backup Workflow

```bash
# Dry-run test (no actual changes)
proxsave --dry-run

# Real backup
proxsave --backup

# View logs. The filename carries the FQDN, not the short hostname, and LOG_PATH
# defaults to /opt/proxsave/log, so glob the directory rather than guessing the name.
ls -t /opt/proxsave/log/backup-*.log | head -1 | xargs tail -f

# Check backup files
ls -lh /opt/proxsave/backup/
```

---

## Upgrading ProxSave Binary

ProxSave provides a built-in upgrade command to update your installation to the latest release from GitHub.

### Quick Upgrade

```bash
# Upgrade to latest version
proxsave --upgrade

# Non-interactive upgrade (auto-confirm)
proxsave --upgrade y

# Optionally update configuration template
proxsave --upgrade-config

# Verify everything works
proxsave --dry-run
```

### What Gets Updated

The `--upgrade` command:

- Downloads the latest binary from GitHub releases and verifies its signature and checksum before installing.
- Atomically replaces the current binary and updates the `/usr/local/bin/proxsave` symlink (removing the legacy `proxmox-backup` symlink if present).
- Fixes file permissions.
- Merges any new template keys into your `backup.env` (your existing and custom values are preserved, and the previous file is backed up first).
- Reconciles the scheduler: the resident daemon is installed only on a host that has never recorded a scheduler engine; a host whose `backup.env` already carries `SCHEDULER_MODE` keeps the engine it records. Re-run `--install` to change the run time or engine.

### Full Upgrade Workflow

```bash
# 1. Upgrade binary
proxsave --upgrade

# 2. (Optional) Update configuration with new template variables
proxsave --upgrade-config

# 3. Test configuration
proxsave --dry-run

# 4. Check the scheduler
proxsave --daemon-status   # daemon installs; use crontab -l on cron installs

# 5. Run a real backup to confirm
proxsave
```

### Requirements

- **Internet connection**: Must reach GitHub releases
- **Platform**: Linux (amd64)
- **Permissions**: Root/sudo access recommended

### Troubleshooting

If upgrade fails:

1. Check internet connectivity: `curl -I https://github.com`
2. Verify GitHub is reachable: `curl -I https://api.github.com`
3. Check disk space: `df -h /opt/proxsave`
4. Review logs for specific errors

For more details, see [CLI Reference - Binary Upgrade](CLI_REFERENCE.md#binary-upgrade).

---

## Manual Installation

> Allows you to compile your binary file from individual project files.

### Prerequisites

```bash
# Install Go (if building from source)
wget https://go.dev/dl/go1.25.11.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.25.11.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# Install rclone (for cloud storage)
curl https://rclone.org/install.sh | bash

# Install git
apt update && apt install -y git

# Install make
apt update && apt install -y make

# Verify installations
go version    # Should show go1.25.11+
rclone version  # Should show rclone v1.60+
git --version # Should show git 2.47.3+
make --version # Should show make 4.4.1+
```

### Building from Source

```bash
# Create folder
mkdir /opt/proxsave

# Navigate to project directory
cd /opt/proxsave

# Copy from github
git clone --branch main https://github.com/tis24dev/proxsave.git .

# Download dependencies
go mod tidy

# Build binary
make build

# Verify build
./build/proxsave --version
```

### Interactive Installation Wizard

The installation wizard creates your configuration file interactively:

```bash
./build/proxsave --install

# Or perform a clean reinstall. Keeps build/, env/ and identity/; deletes everything
# else under the base directory, local backup archives and configs/backup.env included.
./build/proxsave --new-install
```

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
5. **Notifications**: Enable Telegram (centralized) and Email notifications; Email asks for a delivery method and defaults to `relay` with `sendmail` failover. Use `pmf` only when you want Proxmox Notifications via `proxmox-mail-forward`.
6. **Encryption**: AGE encryption setup (runs sub-wizard immediately if enabled)
7. **Scheduler engine**: choose the ProxSave local daemon or system cron. Fresh installs and Overwrite default to the daemon (a resident systemd service with a hang watchdog and healthchecks); editing an existing config keeps its current engine. See [DAEMON.md](DAEMON.md).
8. **Healthchecks** (daemon only): with the daemon engine, choose the monitoring mode: `Off`, `ProxSave HC Server` (centralized, zero setup, the default), or `Your own server` (self). Self mode opens a follow-up screen to paste your ping URLs, then a verification screen. Centralized mode goes straight to the verification screen, which also hands you the way into your monitoring portal: a single-use link until you set a portal password, the portal address and your sign-in identity afterwards. With the cron engine this choice is dimmed and forced off. See [HEALTHCHECKS.md](HEALTHCHECKS.md).
9. **Run at (HH:MM)**: the daily backup time (default `02:00`), used by whichever engine you chose (the daemon's daily run, or the cron entry).
10. **Post-install check (optional)**: Runs `proxsave --dry-run` and shows actionable warnings like `set BACKUP_*=false to disable`, allowing you to disable unused collectors and reduce WARNING noise
11. **Telegram pairing (optional)**: If Telegram centralized mode is enabled and the installer can load a valid config plus a Server ID, it shows your Server ID and lets you verify pairing with the bot (retry/skip supported). Otherwise installation continues and logs why pairing was skipped.

#### Backup monitoring wizard (TUI)

When the daemon engine is selected with monitoring on, the installer opens a **Backup monitoring (healthchecks)** screen after the config has been written. Self mode gets a parameters screen first, where you paste the full ping URL of each check (alive and backup required, updates and the four per-channel URLs optional); those go into `backup.env`. The verification screen itself writes nothing.

**What you see:**
- A short explanation of what gets reported
- **Centralized only**: your monitoring portal, boxed. Which form it takes depends on whether you already have a portal password. Without one you get a **single-use link, valid about an hour**, with the instruction to open it and **set a password and configure alert channels**: choosing that password is what turns the link into an account you can return to. Once you have one, the box shows the portal address and the identity to sign in with instead, under `Sign in with the password you set.`
- **Status**: a state keyword plus a plain-language explanation
- **Sensors**: after a check, one colored line per monitored check with its state and last-ping age
- **Actions**: `Check` (repeatable up to the attempt cap), then `Continue` once verified or `Skip` to move on. A hard blocker removes `Check`, since another attempt cannot help

The screen is skipped, with the reason logged, when monitoring is off, the config cannot be loaded, self mode has no alive URL yet, or the host has no server identity. It is also skipped, along with every other config-dependent post-step, when you answer **Keep existing & continue** at the existing-configuration prompt.

Centralized mode needs no pairing and no API key: the credential is provisioned automatically. `PROVISIONING` simply means it has not completed yet.

**CLI mode:**
- With `--install --cli` the installer applies the same rules and prints the same information as plain text, portal details included in whichever of the two forms applies, then asks whether to run the check now, with a retry loop.

Status keywords, sensor states, and what to do about each are in [HEALTHCHECKS.md](HEALTHCHECKS.md).

#### Telegram pairing wizard (TUI)

If you enable Telegram notifications during `--install`, the installer opens an additional **Telegram Setup** screen only when all of these are true:
- `TELEGRAM_ENABLED=true`
- `BOT_TELEGRAM_TYPE=centralized` (or left empty, which defaults to centralized)
- `backup.env` loads successfully
- a Server ID can be resolved from `<BASE_DIR>/identity/.server_identity`

If any of those checks fail, installation continues without this screen and logs the skip reason (for example config load failure, personal mode, or missing server identity).

When shown, it does **not** modify your `backup.env`. It only:
- Computes/loads the **Server ID** and persists it (identity file)
- Guides you through pairing with the centralized bot
- Lets you verify pairing immediately (retry supported)

**What you see:**
- **Instructions**: steps to start the bot and send the Server ID
- **Server ID**: digits-only identifier + identity file path/persistence status
- **Status**: live feedback from the pairing check
- **Actions**:
  - `Check`: verify pairing (press again to retry)
  - `Continue`: available only after a successful check
  - `Skip`: leave without verification (in centralized mode, `ESC` behaves like Skip when not verified)

**Where the Server ID is stored:**
- `<BASE_DIR>/identity/.server_identity`

**If `Check` fails:**
- `403` / `409`: start the bot, send the Server ID, then try again
- `422`: the Server ID looks invalid; re-run the installer or regenerate the identity file
- Other errors: temporary server/network issue; retry or skip and pair later

**CLI mode:**
- With `--install --cli`, the installer follows the same eligibility rules, then prints the Server ID and asks whether to run the check now (with a retry loop).

**Features:**

- Input sanitization (no newlines/control characters)
- Template comment preservation
- Creates all necessary directories with proper permissions (0700)
- Immediate AGE key generation if encryption is enabled
- Optional post-install audit to disable unused collectors (keeps changes explicit; nothing is disabled silently)
- Optional Telegram pairing wizard (centralized mode, valid config, Server ID available) that displays Server ID and verifies the bot registration (retry/skip supported)
- Install session log under `/tmp/proxsave/install-*.log` (includes audit results and Telegram pairing outcome)

After completion, edit `configs/backup.env` manually for advanced options.

### Scheduling and the daemon

ProxSave runs the backup from the resident daemon or from cron, chosen in the wizard above. Switch or inspect the scheduler at any time with `proxsave --daemon-setup`, `proxsave --daemon-remove`, and `proxsave --daemon-status`. See [DAEMON.md](DAEMON.md).

`BASE_DIR` is detected from the installed `proxsave` executable. Do not add an active `BASE_DIR=...` line to `backup.env`; upgrades remove it and runtime ignores it if present.
