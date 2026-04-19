# Proxsave Documentation Index

This directory contains the authoritative project documentation.

The repository root `README.md` intentionally remains minimal. Use the documents
below for the current operational and technical behavior.

## User Guides

- [INSTALL.md](INSTALL.md): installation, reinstall, and upgrade flows
- [CONFIGURATION.md](CONFIGURATION.md): complete `backup.env` reference
- [CLI_REFERENCE.md](CLI_REFERENCE.md): commands, flags, and workflow phases
- [EXAMPLES.md](EXAMPLES.md): ready-to-use configuration examples
- [RESTORE_GUIDE.md](RESTORE_GUIDE.md): full restore guide and category behavior
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md): operational diagnostics and fixes

## Architecture & Developer Docs

- [DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md): contributor setup and development workflow
- [COLLECTOR_ARCHITECTURE.md](COLLECTOR_ARCHITECTURE.md): collector recipes, bricks, and `dual`
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md): restore internals and orchestration details
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md): visual restore workflow diagrams

## Supporting References

- [BACKUP_ENV_MAPPING.md](BACKUP_ENV_MAPPING.md): legacy Bash to Go env mapping
- [CLOUD_STORAGE.md](CLOUD_STORAGE.md): cloud/rclone behavior
- [ENCRYPTION.md](ENCRYPTION.md): archive encryption and decrypt/restore flow
- [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md): attestation verification
- [MIGRATION_GUIDE.md](MIGRATION_GUIDE.md): migration from the Bash implementation
- [LEGACY_BASH.md](LEGACY_BASH.md): legacy Bash notes and compatibility
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md): PVE cluster disaster recovery
- [RELEASE-PROCESS.md](RELEASE-PROCESS.md): release engineering notes
