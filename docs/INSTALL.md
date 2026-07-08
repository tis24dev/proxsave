# ProxSave Installation Guide

## 📑 Table of Contents

- [🚀 Fast Install](#fast-install)
  - [Direct Install](#direct-install)
  - [First Backup Workflow](#first-backup-workflow)
- [⬆️ Upgrading ProxSave Binary](#upgrading-proxsave-binary)
  - [Quick Upgrade](#quick-upgrade)
  - [What Gets Updated](#what-gets-updated)
  - [Full Upgrade Workflow](#full-upgrade-workflow)
- [💾 Manual Installation](#manual-installation)
  - [Prerequisites](#prerequisites)
  - [Building from Source](#building-from-source)
  - [Interactive Installation Wizard](#interactive-installation-wizard)

## Fast Install

### Direct Install

1. Download & start Install

   ```bash
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
   ```

   or: if you need a fully clean reinstall use: (preserves `build/`, `env/`, and `identity/`)

   ```bash
   bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --new-install
   ```

2. Run your first backup

   ```bash
   proxsave
   ```

> **Release integrity & authenticity.** `install.sh` and `proxsave --upgrade`
> verify every release before installing it: `SHA256SUMS` is checked against the
> project's pinned **ECDSA P-256** signature (`SHA256SUMS.sig`), then the archive
> is checked against `SHA256SUMS`. A missing or invalid signature aborts the
> install/upgrade — there is no fallback to checksum-only. `install.sh` requires
> `openssl` for this (preinstalled on Proxmox); the Go upgrade verifies it
> natively. To verify a download yourself, see
> [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md#release-signature-sha256sumssig).

### First Backup Workflow

```bash
# Dry-run test (no actual changes)
proxsave --dry-run

# Real backup
proxsave

# View logs
tail -f log/backup-$(hostname)-*.log

# Check backup files
ls -lh backup/
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

- ✅ Downloads latest binary from GitHub releases
- ✅ Verifies integrity with SHA256 checksums
- ✅ Atomically replaces current binary
- ✅ Updates the `/usr/local/bin/proxsave` symlink (and removes the legacy `proxmox-backup` symlink if present)
- ✅ Fixes file permissions
- ✅ Merges any new template keys into your `backup.env` (your existing and custom values are preserved, and the previous file is backed up first)
- ❌ **Does NOT touch** your cron schedule (re-run `--install` to change it)

### Full Upgrade Workflow

```bash
# 1. Upgrade binary
proxsave --upgrade

# 2. (Optional) Update configuration with new template variables
proxsave --upgrade-config

# 3. Test configuration
proxsave --dry-run

# 4. Verify cron schedule
crontab -l

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

# Or perform a clean reinstall (keeps build/, env/, and identity/)
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

Final install steps still run:
- Support docs installation
- Symlink and cron finalization
- Permission normalization

**Wizard prompts:**

1. **Configuration file path**: Default `configs/backup.env` (accepts absolute or relative paths within repo)
2. **Secondary storage**: Optional path for backup/log copies; disabling it clears both saved secondary paths from `backup.env`
3. **Cloud storage (rclone)**: Optional rclone configuration (supports `CLOUD_REMOTE` as a remote name (recommended) or legacy `remote:path`; `CLOUD_LOG_PATH` supports path-only (recommended) or `otherremote:/path`)
4. **Firewall rules**: Optional firewall rules collection toggle (`BACKUP_FIREWALL_RULES=false` by default; supports iptables/nftables)
5. **Notifications**: Enable Telegram (centralized) and Email notifications; Email asks for a delivery method and defaults to `relay` with `sendmail` failover. Use `pmf` only when you want Proxmox Notifications via `proxmox-mail-forward`.
6. **Encryption**: AGE encryption setup (runs sub-wizard immediately if enabled)
7. **Cron schedule**: Choose cron time (HH:MM, default `02:00`) for the `proxsave` cron entry in both CLI and TUI install modes
8. **Post-install check (optional)**: Runs `proxsave --dry-run` and shows actionable warnings like `set BACKUP_*=false to disable`, allowing you to disable unused collectors and reduce WARNING noise
9. **Telegram pairing (optional)**: If Telegram centralized mode is enabled and the installer can load a valid config plus a Server ID, it shows your Server ID and lets you verify pairing with the bot (retry/skip supported). Otherwise installation continues and logs why pairing was skipped.

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

`BASE_DIR` is detected from the installed `proxsave` executable. Do not add an active `BASE_DIR=...` line to `backup.env`; upgrades remove it and runtime ignores it if present.
