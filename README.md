<div align="center">

# ProxSave
Proxmox PBS & PVE System Files Backup

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.25+-success.svg?logo=go)](https://go.dev/)
[![codecov](https://codecov.io/gh/tis24dev/proxsave/branch/dev/graph/badge.svg)](https://codecov.io/gh/tis24dev/proxsave)
[![Go Lint](https://github.com/tis24dev/proxsave/actions/workflows/lint.yml/badge.svg?branch=dev)](https://github.com/tis24dev/proxsave/actions/workflows/lint.yml)
[![GoSec](https://img.shields.io/github/actions/workflow/status/tis24dev/proxsave/security-ultimate.yml?label=GoSec&logo=go)](https://github.com/tis24dev/proxsave/actions/workflows/security-ultimate.yml)
[![CodeQL](https://img.shields.io/github/actions/workflow/status/tis24dev/proxsave/codeql.yml?label=CodeQL&logo=github)](https://github.com/tis24dev/proxsave/actions/workflows/codeql.yml)
[![Dependabot](https://img.shields.io/badge/Dependabot-enabled-success?logo=dependabot)](https://github.com/tis24dev/proxsave/network/updates)
[![Proxmox](https://img.shields.io/badge/Proxmox-PVE%20%7C%20PBS-E57000.svg)](https://www.proxmox.com/)
[![rclone](https://img.shields.io/badge/rclone-1.60+-136C9E.svg)](https://rclone.org/)
[![💖 Sponsor](https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github)](https://github.com/sponsors/tis24dev)
[![☕ Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee)](https://github.com/sponsors/tis24dev)
[![💸 Donate](https://img.shields.io/badge/Donate-PayPal-blue?logo=paypal)](https://paypal.me/DNoventa)
</div>

## About the Project

ProxSave is a project created by enthusiasts, with the aim of simplifying recovery in critical moments.

Restoring a PVE or PBS server after a disaster (or even just a migration) is always a process that requires skill, time, and patience, **ProxSave** allows you to save your entire environment and restore it at any time, allowing you to prepare the new installation to accommodate your personal data with as few manual changes as possible.

**ProxSave** allows you to save and restore, integrating advanced features: automatic backups, multi-path saves, intelligent retention, encryption of backups, integrated Telegram and email notifications (cloud relay or Proxmox Notifications), and compatibility with webhooks, Gotify, and Prometheus.

For more information, take a look at our landing page at [proxsave.dev](https://proxsave.dev).

## Installation

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

or: if you need a fully clean reinstall use: (preserves `build/`, `env/`, and `identity/`)
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --new-install
```
You can find the installation script source [here](./install.sh).

The installer ends in the interactive setup, which writes your `configs/backup.env`. Everything after that is done from the dashboard: run `proxsave` on the host and it opens.

🔒 The installer and `proxsave --upgrade` verify each release's signature before installing, for releases that publish `SHA256SUMS.sig`, so only authentic, untampered builds ever run ([details](./docs/PROVENANCE_VERIFICATION.md#release-signature-sha256sumssig)).

> [!NOTE]
> Please refer to the [docs](./docs/INSTALL.md) for more information about the installation.

## The Dashboard

`proxsave`, run with no arguments on a terminal, opens the interactive dashboard. This is the normal way to use ProxSave: backing up, restoring, editing the configuration, upgrading, running the diagnostic checks and managing the daemon are all reachable there, and every entry that has a matching command-line flag runs that flag's code. The three `Diagnostic Checks` entries and `Daemon` > `Restart` have no flag and exist only in the dashboard.

```bash
proxsave
```

| Group | Entry | What it does |
|-------|-------|--------------|
| Backup | `Backup` | starts a backup with the current configuration, streamed inside the dashboard |
| Tools | `Restore` | restores a backup onto this system |
| Tools | `Decrypt` | converts an encrypted backup into a plaintext bundle |
| Maintenance | `New key` | creates a new AGE encryption key |
| Maintenance | `Install` | `Edit install` re-runs the interactive setup (this is how you change the configuration); `Wipe install` resets the install directory first, keeping `build/`, `env/` and `identity/` |
| Maintenance | `Upgrade` | `Check upgrade` updates the binary to a newer release, merging new template variables into `backup.env` as part of the same run; `Check config` runs that merge on its own |
| Diagnostic Checks | `Telegram`, `Healthchecks`, `Post-install` | verify the Telegram relay pairing, show the monitoring portal details, re-run the post-install audit |
| Daemon | `Install`, `Disable`, `Restart`, `Status` | switch the scheduler to the resident daemon or back to cron, restart it, show its state. The group is context aware: `Install` appears on a cron install, `Disable` and `Restart` when the daemon is the active scheduler, `Status` always |
| Recovery | `Cleanup guards` | removes leftover restore mount guards |
| Recovery | `Support` | runs a support backup and emails the debug log to the maintainer |

The dashboard opens only when `proxsave` is invoked completely bare (any flag, even `--config`, skips it) and stdin and stdout are both real terminals with `TERM` set to something other than `dumb`. Everything else, cron included, runs the backup directly, and a dashboard that is abandoned or that fails to render exits without doing anything instead of falling through into a backup.

Screen by screen: [DASHBOARD.md](./docs/DASHBOARD.md).

## Scheduling

The setup offers two scheduler engines and a fresh installation defaults to the **resident daemon** (`proxsave-daemon.service`): it runs the backup itself at `SCHEDULER_TIME`, supervises it under the `MAX_RUN_DURATION` hang watchdog, and reports liveness and outcome to healthchecks monitoring. Monitoring only transmits under the daemon, which is the sole pinger; choosing cron turns it off.

System cron is the legacy engine and stays fully supported: `SCHEDULER_MODE=cron` keeps the crontab entry instead. Once the variable is recorded in `backup.env`, an upgrade leaves the engine as it stands and never switches it.

Switch between the two from the dashboard's Daemon group, or with `--daemon-setup` / `--daemon-remove` on a headless host.

Details: [DAEMON.md](./docs/DAEMON.md) and [HEALTHCHECKS.md](./docs/HEALTHCHECKS.md).

## Upgrading

From the dashboard: `Upgrade` > `Check upgrade` downloads and installs a newer release, and the `backup.env` merge is part of that run. `Check config` runs that merge on its own, for when the binary is already current.

Headless, the same upgrade is `proxsave --upgrade` (append `y` to auto-confirm).

### External Upgrade
An in-place `proxsave --upgrade` is started by the binary already installed, so the release check, the download, the signature and checksum verification and the install itself are all executed by the OLD code. From 0.36.0 on, the installed binary hands the post-install finalize (the configuration merge, the docs and symlink refresh, the daemon migration and restart) to the freshly installed release, so that half runs the new code; a binary older than that finalizes with its own code, and a fix shipped in the new release cannot help that upgrade.

To run the whole upgrade with the new code, fetch the installer instead:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

It downloads, verifies and installs the release itself, then calls the new binary with `--upgrade --localfile` to finalize.

## Command Line

The flags stay fully supported, for headless hosts, cron jobs, scripts and recovery when the dashboard cannot run. They are the automation route, not the everyday one.

| Flag | What it does |
|------|--------------|
| `--backup` | runs the backup now, skipping the dashboard. A non-interactive invocation does this anyway |
| `--restore` | runs the restore workflow (select bundle, optionally decrypt, apply to system) |
| `--decrypt` | converts encrypted bundles into plaintext bundles |
| `--newkey`, `--age-newkey` | resets the AGE recipients and runs the interactive key setup |
| `--support` | forces debug logging and emails the log to the maintainer. Available for a standard backup run and for `--restore` |
| `--install` | runs the interactive installer (generate or edit `backup.env`) |
| `--new-install` | resets the installation directory, preserving `build/`, `env/` and `identity/`, then runs the installer |
| `--upgrade [y]` | downloads and installs the latest release, then upgrades `backup.env`. `y` auto-confirms |
| `--localfile` | with `--upgrade`: skip the release check and download and finalize against the binary already on disk |
| `--upgrade-config` | adds missing variables to `backup.env` from the embedded template, preserving existing and custom ones |
| `--upgrade-config-dry-run` | plans that merge without writing, reporting missing and custom variables |
| `--daemon` | runs as the resident daemon. This is what `proxsave-daemon.service` starts |
| `--daemon-setup` | switches this install to daemon mode: installs and enables the service, removes the cron entry |
| `--daemon-remove` | reverts to cron and prevents future upgrades from reinstalling the daemon |
| `--daemon-status` | prints scheduler mode, service state, running version and binary alignment |
| `--cleanup-guards` | removes leftover guard bind mounts and directories. Combine with `--dry-run` to preview |
| `--show-whatsnew` | shows the release notes screen once and exits |
| `-c`, `--config <path>` | configuration file to use (default `configs/backup.env` under the install directory) |
| `-l`, `--log-level <level>` | `debug`, `info`, `warning`, `error` or `critical` |
| `-n`, `--dry-run` | runs without making actual changes |
| `--cli` | uses plain CLI prompts instead of the TUI, for `--install`, `--new-install`, `--newkey`, `--decrypt` and `--restore` |
| `-v`, `--version` | shows version and build information |
| `-h`, `--help` | shows the help message |

A few more flags exist (`--upgrade-config-json`, `--upgrade-finalize` and its companions) purely as internal plumbing for `--upgrade`; they are not meant to be run by hand.

Full reference: [CLI_REFERENCE.md](./docs/CLI_REFERENCE.md).

## Guide

You can find the guide files for the various functions [here](./docs/README.md).

## Support

Every report or issue is important to us. There are various channels you can use to report a problem.

The fastest report is the dashboard's `Support` entry, under Recovery: it runs a backup with debug logging and emails that log to the maintainer. On a headless host, `proxsave --support` does the same.

It is important that you provide as much information as possible with each report.
You will often find these details listed. They are important, so please do not forget to include them:


```bash
example
===========================================
  Version: 0.11.2
  Build Signature: 60d0d998f* (2025-12-02T14:46:14+01:00) hash=eeb72ef6b8b6ad89
===========================================
```

Every run prints that block in its header. The dashboard shows the version in its own header and the build signature in its footer; `proxsave --version` prints the version with the build commit and date.

<a href="https://github.com/tis24dev/proxsave/issues" target="_blank" rel="noopener noreferrer"><img src="https://img.shields.io/badge/GitHub-Issues-orange?logo=github" style="height:25px;"/></a>
<a href="https://t.me/tis24dev" target="_blank" rel="noopener noreferrer"><img src="https://img.shields.io/badge/Telegram-@tis24dev-red?logo=telegram" style="height:25px;"/></a>

## Donations
To stay completely free and open-source, with no feature behind the paywall and evolve the project, we need your help. If you like ProxSave, please consider donating to help us fund the project's future development.

<a href="https://github.com/sponsors/tis24dev" target="_blank" rel="noopener noreferrer"><img src="https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github" style="height:25px;"/></a>
<a href="https://github.com/sponsors/tis24dev" target="_blank" rel="noopener noreferrer"><img src="https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee" style="height:25px;"/></a>

Thank you so much!

## Recognitions
<a href="https://www.xda-developers.com/i-use-this-free-tool-with-proxmox-backup-server/" target="_blank" rel="noopener noreferrer"><img src="https://img.shields.io/badge/XDA%20Developers-Article-blue?logo=android" style="height:25px;"/></a>

## Release Testing & Feedback
A special thanks to the community members who help by testing releases and reporting issues. 💙

<table align="left">
  <tr>
    <td align="center" width="160">
      <a href="https://github.com/NukeThemTillTheyGlow">
        <img src="https://github.com/NukeThemTillTheyGlow.png?size=96" width="56" alt="@NukeThemTillTheyGlow" />
      </a>
      <br />
      <a href="https://github.com/NukeThemTillTheyGlow"><sub><b>@NukeThemTillTheyGlow</b></sub></a>
      <br />
      <sub>release testing</sub>
    </td>
    <td align="center" width="160">
      <a href="https://github.com/marc6901">
        <img src="https://github.com/marc6901.png?size=96" width="56" alt="@marc6901" />
      </a>
      <br />
      <a href="https://github.com/marc6901"><sub><b>@marc6901</b></sub></a>
      <br />
      <sub>release testing</sub>
    </td>
  </tr>
</table>

<br clear="all" />

## Repo Activity
![Alt](https://repobeats.axiom.co/api/embed/d9565d6d1ed8222a5da5fedf25c18a9c8beab382.svg "Repobeats analytics image")
