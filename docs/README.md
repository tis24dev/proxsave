# ProxSave documentation

These guides describe host configuration backup and recovery for Proxmox VE and Proxmox Backup Server

After installation, open an interactive terminal and run `proxsave` without arguments to use the dashboard. The operator guides follow its menus. Command-line options for automation and headless use are kept in the [CLI reference](CLI_REFERENCE.md)

## User Guides

### Start and maintain

| Task | Guide |
| --- | --- |
| Understand what is protected | [Backup scope](BACKUP_GUIDE.md#what-proxsave-backs-up) |
| Install ProxSave | [Installation](INSTALL.md#fast-install) |
| Make and verify the first backup | [First backup](BACKUP_GUIDE.md#run-and-verify-your-first-backup) |
| Navigate the application | [Dashboard](DASHBOARD.md) |
| Update an existing installation | [Upgrade](INSTALL.md#upgrading-proxsave-binary) |
| Build from source | [Manual installation](INSTALL.md#manual-installation) |

### Backup and storage

| Task | Guide |
| --- | --- |
| Choose files and collectors | [Collection policy](BACKUP_GUIDE.md#choose-what-to-collect) |
| Understand the run stages | [Backup workflow](BACKUP_GUIDE.md#how-a-backup-runs) |
| Choose local, NAS, cloud or PBS storage | [Destinations](STORAGE.md#choose-and-verify-backup-destinations) |
| Configure retention | [Retention policy](STORAGE.md#set-backup-retention) |
| Save a host backup directly to PBS | [Native PBS backups](STORAGE.md#create-native-pbs-backups) |
| Configure rclone and remote paths | [Cloud storage](CLOUD_STORAGE.md) |
| Combine features for a particular host | [Practical examples](EXAMPLES.md) |

### Encryption and trust

| Task | Guide |
| --- | --- |
| Encrypt archives and retain recovery keys | [Encryption](ENCRYPTION.md) |
| Decrypt a bundle | [Decryption](ENCRYPTION.md#decrypting-backups) |
| Verify release signatures and attestations | [Release verification](PROVENANCE_VERIFICATION.md) |

### Scheduling and alerts

| Task | Guide |
| --- | --- |
| Schedule and supervise backup runs | [Resident daemon](DAEMON.md) |
| Detect missed or failed backups | [Healthchecks monitoring](HEALTHCHECKS.md) |
| Configure result messages | [Notifications](NOTIFICATIONS.md) |

### Restore and recovery

| Task | Guide |
| --- | --- |
| Plan and perform a host configuration restore | [Restore workflow](RESTORE_GUIDE.md#restore-a-host) |
| Understand what a restore mode includes | [Restore modes](RESTORE_GUIDE.md#choose-restore-modes-and-categories) |
| Recover cluster configuration | [Cluster recovery](CLUSTER_RECOVERY.md#recover-a-proxmox-cluster) |
| Recover boot and passthrough settings | [IOMMU, VFIO and passthrough](RESTORE_GUIDE.md#restore-iommu-vfio-and-passthrough-settings) |
| Retrieve archived cloud backups before restoring | [Cold-storage recovery](CLOUD_STORAGE.md#recover-archives-from-cold-object-storage) |

### Reference and help

| Task | Guide |
| --- | --- |
| Look up settings and defaults | [Configuration reference](CONFIGURATION.md) |
| Automate operations | [CLI reference](CLI_REFERENCE.md) |
| Diagnose failures and collect debug logs | [Troubleshooting](TROUBLESHOOTING.md#diagnose-backup-and-restore-failures) |
| Prepare a support request | [Getting help](TROUBLESHOOTING.md#prepare-a-support-request) |

## Architecture & Developer Docs

These references explain implementation and contribution work. Operator procedures live in the task guides above

- [Developer guide](DEVELOPER_GUIDE.md): build, test and contributor workflow
- [Collector architecture](COLLECTOR_ARCHITECTURE.md): role-specific recipes and shared collection
- [Dashboard architecture](DASHBOARD_TUI.md): screens, sessions and workflow integration
- [Restore internals](RESTORE_TECHNICAL.md): orchestration, category handling and diagrams
- [Security model](SECURITY.md): execution boundaries, preflight checks and secret handling
- [Test strategy](TEST_STRATEGY.md): testing conventions and UI-driver coverage
- [Release process](RELEASE-PROCESS.md): branches, review and release engineering

## Supporting References

[Restore diagram links](RESTORE_DIAGRAMS.md) remain available for existing references. Diagrams are maintained beside the corresponding explanations in the restore technical guide

The [website source map](publishing/site-map.json) identifies the task sections intended for GH Sync. Editorial review and preview checks are required before publishing a changed mapping; the file is not a record that an import has happened
