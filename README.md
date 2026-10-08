# ProxSave: Proxmox VE and PBS Host Backup and Disaster Recovery

[![Latest release](https://img.shields.io/github/v/release/tis24dev/proxsave)](https://github.com/tis24dev/proxsave/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)

ProxSave is a free, open-source tool for **online backups of host configuration** on **Proxmox Virtual Environment (PVE)** and **Proxmox Backup Server (PBS)**. It supports configuration restore and disaster recovery after a server failure, a Proxmox reinstall or a move to new hardware.

Configuration backups run **without shutting down the host, virtual machines or LXC containers**. The host and its workloads remain online during collection.

Backups can be kept locally, copied to a mounted NAS or another disk, uploaded to cloud storage, and **saved directly to a Proxmox Backup Server storage already configured in PVE**. Scheduled backups, encryption, retention policies, selective restore and external monitoring are included.

ProxSave is written in Go and released under the [MIT license](./LICENSE). It is an independent project, with no affiliation to Proxmox and no paid feature tier.

[Website: proxsave.dev](https://proxsave.dev) · [Documentation](./docs/README.md) · [Releases](https://github.com/tis24dev/proxsave/releases) · [Report an issue](https://github.com/tis24dev/proxsave/issues)

## Why back up the Proxmox host?

A working recovery plan needs both your workloads and the configuration of the server that runs them. After a fresh installation, there may be network bridges, storage definitions, users, permissions, firewall rules and backup jobs to recreate before your environment is usable again.

ProxSave keeps that host configuration available for recovery. Typical uses include:

- Recovering a Proxmox VE node after a boot disk or hardware failure.
- Rebuilding the configuration of a Proxmox Backup Server.
- Preparing for a reinstall or migration to replacement hardware.
- Restoring selected settings after an unwanted configuration change.
- Preserving the configuration of standalone nodes, cluster members and hosts running both PVE and PBS.

ProxSave complements **PBS and vzdump backups of virtual machines and containers**. It captures VM and LXC configuration, but does not create backups of their running disks or filesystems. It can also collect existing PVE backup files when enabled; those files still need to be created by your normal guest backup jobs. PBS datastore contents need their own protection as well.

## What ProxSave backs up

ProxSave detects the host's PVE and PBS roles and collects the relevant configuration alongside common Linux system settings. On a host running both products, it collects both roles in one backup run. An optional host-backup mode also supports collection from a host filesystem mounted read-only inside a suitably configured LXC appliance.

| Area | Configurable backup coverage |
| --- | --- |
| **Proxmox VE** | VM and LXC configuration, storage and datacenter settings, backup jobs, replication settings, users and permissions, firewall rules, HA and SDN configuration, PCI/USB resource mappings, Corosync and the cluster database (`/var/lib/pve-cluster/config.db`). |
| **Proxmox Backup Server** | Datastore definitions, S3 endpoint settings, remotes, sync, prune and verification jobs, users and access control, notifications, node settings, ACME configuration and tape configuration, including tape encryption keys. |
| **Networking** | Interface configuration, bridges, bonds, hostname and DNS settings, with network inventory and runtime reports for recovery. |
| **Storage and filesystems** | Mount configuration, ZFS configuration and pool inventory, LVM metadata, iSCSI, multipath, software RAID, encrypted-volume settings and automount configuration. |
| **System and access** | SSH configuration and keys, TLS certificates, system accounts, service definitions, cron jobs, package sources, kernel settings and boot parameters. |
| **Personal files and scripts** | Local scripts, root and user home directories, and additional paths you choose to include, with exclusions to control the backup's scope. |
| **Recovery information** | Hardware and package inventories, command outputs, storage reports and backup metadata that help explain how the original host was configured. |

Coverage depends on the host's role, available files and enabled collectors. Some captured information is provided for reference or manual review rather than applied automatically during restore.

## Backup destinations

A backup run creates a local archive. It can also save secondary and cloud copies and create a separate native PBS snapshot, so you can keep recovery material outside the host being protected.

| Destination | What it provides |
| --- | --- |
| **Local storage** | A ProxSave archive on a local filesystem path. |
| **Secondary storage** | Another archive copy on a mounted filesystem, such as an NFS or SMB share, NAS, external drive or second disk. |
| **Cloud and remote storage** | Archive copies through rclone, including S3-compatible storage, Backblaze B2, Google Drive, OneDrive and other supported backends. |
| **Proxmox Backup Server** | A native host snapshot of the collected files, sent directly to a PBS storage configured in Proxmox VE. |

### Native Proxmox Backup Server integration

Native PBS uploads require a PVE host, an enabled PBS storage in `/etc/pve/storage.cfg` and `proxmox-backup-client`. ProxSave reads that storage's connection details, datastore, namespace, credentials and encryption key from the PVE configuration and sends the collected file tree directly to PBS. The configured namespace, if used, must already exist.

These are native PBS file backups, with [PBS deduplication and incremental transfer of chunks](https://pbs.proxmox.com/docs/technical-overview.html). Each host has its own `host/proxsave-<hostname>` backup group containing a `proxsave.pxar` file archive. ProxSave checks that the uploaded snapshot exists and has the expected encryption mode, manages retention for that group and attaches the run log to the snapshot.

PBS encryption follows the key configured on the selected PVE storage. It is separate from the age encryption used for ProxSave archive copies. A storage without an encryption key receives unencrypted snapshots.

Recovery from native PBS snapshots uses PBS tools. ProxSave's built-in restore selector currently discovers its archive backups in local, secondary and rclone storage.

[PBS storage reference](./docs/CONFIGURATION.md#pbs-storage-proxmox-backup-server) · [Cloud storage](./docs/CLOUD_STORAGE.md)

## Restore and disaster recovery

ProxSave provides an interactive restore workflow with four choices: **full configuration**, **storage**, **base system** or **custom categories**. You can recover a broad set of settings or focus on a specific area, such as networking, storage definitions or backup jobs.

The workflow checks the backup's compatibility with the target host, presents a restore plan and creates safety copies of the configuration being replaced. Relevant PVE and PBS settings are staged and applied through the appropriate APIs or controlled file updates. Categories belonging to a product the target host does not run are exported for review.

For PVE clusters, **SAFE** mode preserves the running cluster database while recovering supported configuration; **RECOVERY** mode handles cluster database restoration. Network recovery includes interface-name repair, connectivity checks and a rollback timer. Staged PVE firewall, HA and access-control changes also have rollback protection.

Recovery logs, exports and safety backups remain available for later review.

ProxSave restores configuration onto an installed system. Restoring network or cluster settings may interrupt service, and boot changes require a reboot. VM disks, container filesystems, application state and installed packages remain part of your wider disaster recovery plan.

[Restore guide](./docs/RESTORE_GUIDE.md) · [Proxmox cluster recovery](./docs/CLUSTER_RECOVERY.md)

### IOMMU, VFIO and passthrough configuration

Backups preserve the configuration used for **PCI passthrough, GPU passthrough and USB passthrough**: guest device assignments, PVE resource mappings, IOMMU/VFIO kernel parameters, module options and driver blacklists. Coverage follows the enabled collectors.

When the relevant restore categories are selected, ProxSave restores `/etc/modules` and `/etc/modprobe.d/` automatically. The boot category merges saved kernel parameters into a recognized GRUB or systemd-boot configuration, preserves existing target values, and rebuilds the initramfs and bootloader when required. The source host's boot files and disk identifiers remain available for reference.

In PVE SAFE mode, the workflow can apply saved PCI/USB resource mappings through the Proxmox API before guest configurations, after confirmation.

On different hardware, PCI addresses, device IDs, USB paths and cluster node names may need manual changes. Firmware IOMMU settings, device isolation and hardware compatibility must be checked on the target host. ProxSave does not automatically adapt passthrough assignments to replacement devices.

[Boot parameter restore](./docs/RESTORE_GUIDE.md#11-kernel-command-line-merge-boot-category) · [PVE resource mapping restore](./docs/RESTORE_GUIDE.md#pvesh-safe-apply-cluster-safe-mode)

## Automatic backups and monitoring

The resident daemon schedules daily, weekly or monthly backups and supervises each run with a duration limit to detect hangs. Cron remains available as an alternative scheduler.

Retention can keep the newest backups or use **Grandfather-Father-Son (GFS)** rules for daily, weekly, monthly and yearly recovery points. Local, secondary, cloud and native PBS destinations support retention. Archive compression is configurable, including gzip, Zstandard and xz.

Backup reports can be delivered through **Telegram, email, Gotify and webhooks**. Email supports a relay, local sendmail or Proxmox Notifications integration. Notification failures are recorded separately from failures to create the backup.

With the daemon, external healthchecks monitoring can detect a missing backup, an unavailable host or a hung run even when the host cannot send a notification. It also tracks daemon liveness, release updates and notification-channel outcomes. You can use the ProxSave monitoring service or your own healthchecks instance. Prometheus metrics are available through the node_exporter textfile collector.

The interactive terminal dashboard brings backup, restore, configuration, diagnostics, upgrades and daemon management together. Command-line options support headless hosts and automation, and custom scripts can run before and after a backup.

[Scheduling](./docs/DAEMON.md) · [Monitoring](./docs/HEALTHCHECKS.md) · [Notifications](./docs/NOTIFICATIONS.md)

## Encryption and integrity

ProxSave archive backups can use **age encryption** with passphrase or key-based recipients. Compression and encryption are streamed during archive creation. SHA-256 checksums, archive verification and backup manifests support integrity checks and describe the host and roles a backup came from.

The installer verifies the release signature and checksum before installing the binary. Release provenance attestations are also available for independent verification.

Host backups can contain credentials and private keys. Encryption covers the saved archive; collected files are temporarily staged in plaintext on the host. The security and encryption guides explain the storage, permissions and recovery-key considerations.

[Encryption](./docs/ENCRYPTION.md) · [Security](./docs/SECURITY.md) · [Release verification](./docs/PROVENANCE_VERIFICATION.md)

## Installation

Run the official installer as root on your Proxmox VE or PBS host (Linux x86-64):

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

The installer downloads and verifies the latest release, installs the binary and opens the interactive setup. You can [read the installer source](./install.sh) before running it.

After installation, run `proxsave` to open the interactive dashboard.

See the [installation guide](./docs/INSTALL.md) for requirements, alternative installation methods, reinstalling and upgrading.

## Documentation

| Topic | Guide |
| --- | --- |
| All user and technical documentation | [Documentation index](./docs/README.md) |
| Dashboard and everyday operation | [Dashboard guide](./docs/DASHBOARD.md) |
| Backup settings, destinations and collectors | [Configuration reference](./docs/CONFIGURATION.md) |
| Recovery workflows and cluster procedures | [Restore guide](./docs/RESTORE_GUIDE.md) and [cluster recovery](./docs/CLUSTER_RECOVERY.md) |
| Backup scheduling and healthchecks | [Daemon](./docs/DAEMON.md) and [monitoring](./docs/HEALTHCHECKS.md) |
| Automation and practical configurations | [CLI reference](./docs/CLI_REFERENCE.md) and [examples](./docs/EXAMPLES.md) |
| Diagnosing problems | [Troubleshooting](./docs/TROUBLESHOOTING.md) |
| Architecture and development | [Collector architecture](./docs/COLLECTOR_ARCHITECTURE.md) and [developer guide](./docs/DEVELOPER_GUIDE.md) |

## Community and support

Bug reports, recovery feedback and contributions help improve ProxSave. For support, use [GitHub Issues](https://github.com/tis24dev/proxsave/issues) or contact the maintainer on [Telegram](https://t.me/tis24dev). The documentation covers diagnostics and the built-in support report.

Thanks to [@NukeThemTillTheyGlow](https://github.com/NukeThemTillTheyGlow) and [@marc6901](https://github.com/marc6901) for release testing and feedback, and to everyone who reports problems or contributes fixes.

To contribute, read [CONTRIBUTING.md](./CONTRIBUTING.md). To help fund development, you can [sponsor the project](https://github.com/sponsors/tis24dev) or [donate through PayPal](https://paypal.me/DNoventa).
