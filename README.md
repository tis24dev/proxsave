<div align="center">

# ProxSave
Proxmox PBS & PVE System Files Backup

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.25+-success.svg?logo=go)](https://go.dev/)
[![codecov](https://codecov.io/gh/tis24dev/proxsave/branch/dev/graph/badge.svg)](https://codecov.io/gh/tis24dev/proxsave)
[![GoSec](https://img.shields.io/github/actions/workflow/status/tis24dev/proxsave/security-ultimate.yml?label=GoSec&logo=go)](https://github.com/tis24dev/proxsave/actions/workflows/security-ultimate.yml)
[![CodeQL](https://img.shields.io/github/actions/workflow/status/tis24dev/proxsave/codeql.yml?label=CodeQL&logo=github)](https://github.com/tis24dev/proxsave/actions/workflows/codeql.yml)
[![Dependabot](https://img.shields.io/badge/Dependabot-enabled-success?logo=dependabot)](https://github.com/tis24dev/proxsave/network/updates)
[![Proxmox](https://img.shields.io/badge/Proxmox-PVE%20%7C%20PBS-E57000.svg)](https://www.proxmox.com/)
[![rclone](https://img.shields.io/badge/rclone-1.60+-136C9E.svg)](https://rclone.org/)
[![💖 Sponsor](https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github)](https://github.com/sponsors/tis24dev)
[![☕ Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee)](https://buymeacoffee.com/tis24dev)
</div>

## About the Project

ProxSave is a project created by enthusiasts, with the aim of simplifying recovery in critical moments.

Restoring a PVE or PBS server after a disaster (or even just a migration) is always a process that requires skill, time, and patience, **ProxSave** allows you to save your entire environment and restore it at any time, allowing you to prepare the new installation to accommodate your personal data with as few manual changes as possible.

**ProxSave** allows you to save and restore, integrating advanced features: automatic backups, multi-path saves, intelligent retention, encryption of backups, integrated email and Telegram notifications without configuration, and compatibility with webhooks, Gotify, and Prometheus.

For more information, take a look at our landing page at [proxsave.dev](https://proxsave.dev).

## Installation

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

or: if you need a fully clean reinstall use: (preserves `env/` and `identity/`)
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --new-install
```
You can find the installation script source [here](./install.sh).

> [!NOTE]
> Please refer to the [docs](./docs/INSTALL.md) for more information about the installation.

## Guide

You can find the guide files for the various functions [here](./docs/).

## Support

Contact us at [GitHub Issues](https://github.com/tis24dev/proxsave/issues).

## Donations
To stay completely free and open-source, with no feature behind the paywall and evolve the project, we need your help. If you like ProxSave, please consider donating to help us fund the project's future development.

[GitHub](https://github.com/sponsors/tis24dev)
[Buy me a coffe](https://buymeacoffee.com/tis24dev)

Thank you so much!

## Recognitions
<a href="https://www.xda-developers.com/i-use-this-free-tool-with-proxmox-backup-server/"
   target="_blank" rel="noopener noreferrer">
  <img
    src="https://img.shields.io/badge/XDA%20Developers-Article-blue?logo=android"
    width="300"
  />
</a>

## Repo Activity

![Alt](https://repobeats.axiom.co/api/embed/53ea60503d80f77590f52ac0e983b2b8af47e20a.svg "Repobeats analytics image")

## Star History

<a href="https://www.star-history.com/#tis24dev/proxmox-backup&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=tis24dev/proxmox-backup&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=tis24dev/proxmox-backup&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=tis24dev/proxmox-backup&type=date&legend=top-left" />
 </picture>
</a>