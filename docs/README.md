# Proxsave Documentation Index

This directory contains the authoritative project documentation.

Start with [DASHBOARD.md](DASHBOARD.md). The interactive dashboard, which opens when you run
`proxsave` with no arguments on a terminal, is the primary way to operate ProxSave: backup,
restore, configuration, upgrade, daemon management and the diagnostic checks all live there.
The command-line flags in [CLI_REFERENCE.md](CLI_REFERENCE.md) cover the automation, headless
and recovery routes.

The repository root `README.md` stays a short overview. Use the documents
below for the current operational and technical behavior.

## User Guides

- [DASHBOARD.md](DASHBOARD.md): the interactive dashboard, screen by screen (start here)
- [INSTALL.md](INSTALL.md): installation, reinstall, and upgrade flows
- [CONFIGURATION.md](CONFIGURATION.md): complete `backup.env` reference, and which settings the dashboard form covers
- [DAEMON.md](DAEMON.md): the resident daemon, the scheduler a new install gets, its watchdog and the engines
- [HEALTHCHECKS.md](HEALTHCHECKS.md): backup monitoring under the daemon, the monitoring portal, and self-hosted setups
- [RESTORE_GUIDE.md](RESTORE_GUIDE.md): full restore guide and category behavior
- [NOTIFICATIONS.md](NOTIFICATIONS.md): notification channels and the centralized bot relay
- [EXAMPLES.md](EXAMPLES.md): ready-to-use configuration examples
- [CLI_REFERENCE.md](CLI_REFERENCE.md): flags for automation, headless hosts, cron jobs and recovery
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md): operational diagnostics and fixes

## Architecture & Developer Docs

- [DEVELOPER_GUIDE.md](DEVELOPER_GUIDE.md): contributor setup and development workflow
- [COLLECTOR_ARCHITECTURE.md](COLLECTOR_ARCHITECTURE.md): collector recipes, bricks, and `dual`
- [DASHBOARD_TUI.md](DASHBOARD_TUI.md): internals of the dashboard and every graphical flow, components, and screen contracts
- [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md): restore internals and orchestration details
- [RESTORE_DIAGRAMS.md](RESTORE_DIAGRAMS.md): visual restore workflow diagrams
- [SECURITY.md](SECURITY.md): execution model, preflight checks, and secret handling
- [TEST_STRATEGY.md](TEST_STRATEGY.md): test conventions and coverage policy

## Supporting References

- [CLOUD_STORAGE.md](CLOUD_STORAGE.md): cloud/rclone behavior
- [ENCRYPTION.md](ENCRYPTION.md): archive encryption and decrypt/restore flow
- [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md): release signature and SLSA attestation verification
- [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md): PVE cluster disaster recovery
- [RELEASE-PROCESS.md](RELEASE-PROCESS.md): release engineering notes
