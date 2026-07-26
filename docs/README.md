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
- [DASHBOARD.md](DASHBOARD.md): the interactive dashboard, screen by screen
- [NOTIFICATIONS.md](NOTIFICATIONS.md): notification channels and the centralized bot relay
- [DAEMON.md](DAEMON.md): the resident daemon, its watchdog, and the scheduler engines
- [HEALTHCHECKS.md](HEALTHCHECKS.md): backup monitoring, the monitoring portal, and self-hosted setups
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md): operational diagnostics and fixes

## Architecture & Developer Docs

- [DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md): contributor setup and development workflow
- [COLLECTOR_ARCHITECTURE.md](COLLECTOR_ARCHITECTURE.md): collector recipes, bricks, and `dual`
- [DASHBOARD_TUI.md](DASHBOARD_TUI.md): dashboard internals, components, and screen contracts
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md): restore internals and orchestration details
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md): visual restore workflow diagrams
- [SECURITY.md](SECURITY.md): execution model, preflight checks, and secret handling
- [TEST_STRATEGY.md](TEST_STRATEGY.md): test conventions and coverage policy

## Supporting References

- [CLOUD_STORAGE.md](CLOUD_STORAGE.md): cloud/rclone behavior
- [ENCRYPTION.md](ENCRYPTION.md): archive encryption and decrypt/restore flow
- [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md): attestation verification
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md): PVE cluster disaster recovery
- [RELEASE-PROCESS.md](RELEASE-PROCESS.md): release engineering notes
