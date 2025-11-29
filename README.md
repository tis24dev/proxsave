# 🔄 Proxmox Backup PBS & PVE System Files - GO

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go](https://img.shields.io/badge/Go-1.25+-success.svg?logo=go)](https://go.dev/)
[![codecov](https://codecov.io/gh/tis24dev/proxmox-backup/branch/dev/graph/badge.svg)](https://codecov.io/gh/tis24dev/proxmox-backup)
[![GoSec](https://img.shields.io/github/actions/workflow/status/tis24dev/proxmox-backup/security-ultimate.yml?label=GoSec&logo=go)](https://github.com/tis24dev/proxmox-backup/actions/workflows/security-ultimate.yml)
[![CodeQL](https://img.shields.io/github/actions/workflow/status/tis24dev/proxmox-backup/codeql.yml?label=CodeQL&logo=github)](https://github.com/tis24dev/proxmox-backup/actions/workflows/codeql.yml)
[![Dependabot](https://img.shields.io/badge/Dependabot-enabled-success?logo=dependabot)](https://github.com/tis24dev/proxmox-backup/network/updates)
[![Proxmox](https://img.shields.io/badge/Proxmox-PVE%20%7C%20PBS-E57000.svg)](https://www.proxmox.com/)
[![rclone](https://img.shields.io/badge/rclone-1.60+-136C9E.svg)](https://rclone.org/)
[![💖 Sponsor](https://img.shields.io/badge/Sponsor-GitHub%20Sponsors-pink?logo=github)](https://github.com/sponsors/tis24dev)
[![☕ Buy Me a Coffee](https://img.shields.io/badge/Buy%20Me%20a%20Coffee-tis24dev-yellow?logo=buymeacoffee)](https://buymeacoffee.com/tis24dev)

**Professional backup system for Proxmox Virtual Environment (PVE) and Proxmox Backup Server (PBS) configuration and critical files** - Rewritten in Go with advanced compression, multi-storage support, cloud integration, intelligent retention, and comprehensive monitoring.

> **Complete guide for installing, configuring, and using proxmox-backup**
>
> Version: 0.9.0 | Last Updated: 2025-11-17

---

### New features!!

Advanced AGE encryptio - Gotify and Webhook channels for notifications

Intelligent backup rotation - Intelligent deletion of logs associated with specific backups

---

## 📑 Table of Contents

- [🎯 Introduction](#introduction)
- [🚀 Quick Start](#quick-start)
  - [1 Minute Setup](#1-minute-setup)
- [💾 Manual Installation](#installation)
- [🔄 Upgrading from Bash Version](#upgrading-from-previous-bash-version-v074-bash-or-earlier)
- [📜 Legacy Bash Version](#legacy-bash-version-v074-bash)
- [⌨️ Command-Line Reference](#command-line-reference)
- [⚙️ Configuration Reference](#configuration-reference)
- [☁️ Cloud Storage](#cloud-storage)
- [🔐 Encryption](#encryption)
- [📝 Practical Examples](#practical-examples)
- [🔧 Troubleshooting](#troubleshooting)
- [🤝 Contributing](#contributing)
- [📚 Documentation](#documentation)
- [📄 License](#license)
- [🔐 Build Provenance](#build-provenance)
- [🔄 Restore Operations](#restore-operations)
- [✨ Conclusion](#conclusion)

---

## Introduction

**proxmox-backup** is a comprehensive backup solution for Proxmox VE/PBS environments, rewritten in Go from a 20,370-line Bash codebase. It provides intelligent backup management with support for local, secondary, and cloud storage destinations.

### Key Features

✅ **Multi-tier Storage**: Local (critical) + Secondary (optional) + Cloud (optional)
✅ **Intelligent Retention**: Simple count-based or GFS (Grandfather-Father-Son) time-distributed
✅ **Cloud Integration**: Full rclone support for 40+ cloud providers
✅ **Encryption**: Streaming AGE encryption with no plaintext on disk
✅ **Compression**: Multiple algorithms (gzip, xz, zstd) with configurable levels
✅ **Notifications**: Telegram, Email, Gotify, and Webhook support
✅ **Advanced Features**: Parallel uploads, retry logic, batch deletion, metrics export

### System Requirements

- **OS:** Linux (Debian, Proxmox VE tested)  
- **Go:** Version 1.25+ (required for building from source)  
- **rclone:** Version 1.60+ (recommended for full cloud provider support)  
- **Disk Space:** Minimum 1 GB for local storage  
- **Network:** Internet access (required for cloud backups & notifications)


---

## Quick Start

### 1-Minute Setup

1. Download & start Install
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxmox-backup/main/install.sh)"
```

or: if you need a fully clean reinstall use: (preserves `env/` and `identity/`)
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxmox-backup/main/install.sh)" --new-install
```

2. Run your first backup
```bash
./build/proxmox-backup
```

3. OPTIONAL - Run migration installation from bash with old env file
```bash
./build/proxmox-backup --env-migration
```

4. OPTIONAL - Run your first backup again after migration
```bash
./build/proxmox-backup
```

5. Check results
```bash
ls -lh backup/
```
```bash
cat log/backup-*.log
```

### Interactive Session Logs

Interactive commands such as `--install`, `--new-install`, `--restore`, and standalone `--decrypt` automatically create a real-time session log under `/tmp/proxmox-backup/` (for example `install-myhost-20250101-120000.log`). These files mirror the wizard output so you can review every prompt even when running unattended.

### First Backup Workflow

```bash
# Dry-run test (no actual changes)
./build/proxmox-backup --dry-run

# Real backup
./build/proxmox-backup

# View logs
tail -f log/backup-$(hostname)-*.log

# Check backup files
ls -lh backup/
```

---

## Installation

### Prerequisites (ONLY IF YOU WANT BUILD YOUR BINARY)

```bash
# Install Go (if building from source)
wget https://go.dev/dl/go1.25.4.linux-amd64.tar.gz
tar -C /usr/local -xzf go1.25.4.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# Install rclone (for cloud storage)
curl https://rclone.org/install.sh | bash

# Install git
apt update && apt install -y git

# Install make
apt update && apt install -y make

# Verify installations
go version    # Should show go1.25+
rclone version  # Should show rclone v1.50+
git --version # Should show git 2.47.3+
make --version # Should show make 4.4.1+
```

### Building from Source

```bash
# Create folder
mkdir /opt/proxmox-backup

# Navigate to project directory
cd /opt/proxmox-backup

# Copy from github
git clone --branch main https://github.com/tis24dev/proxmox-backup.git .

# Initialize Go module
go mod init github.com/tis24dev/proxmox-backup

# Download dependencies
go mod tidy

# Build binary
make build

# Verify build
./build/proxmox-backup --version
```

### Interactive Installation Wizard

The installation wizard creates your configuration file interactively:

```bash
./build/proxmox-backup --install

# Or perform a clean reinstall (keeps env/ and identity/)
./build/proxmox-backup --new-install
```

**Wizard prompts:**

1. **Configuration file path**: Default `configs/backup.env` (accepts absolute or relative paths within repo)
2. **Secondary storage**: Optional path for backup/log copies
3. **Cloud storage**: Optional rclone remote configuration
4. **Notifications**: Enable Telegram (centralized) and email relay
5. **Encryption**: AGE encryption setup (runs sub-wizard immediately if enabled)

**Features:**
- Input sanitization (no newlines/control characters)
- Template comment preservation
- Creates all necessary directories with proper permissions (0700)
- Immediate AGE key generation if encryption is enabled

After completion, edit `configs/backup.env` manually for advanced options.

---

## Upgrading from Previous Bash Version (v0.7.4-bash or Earlier)

If you're currently using the Bash version of proxmox-backup (v0.7.4-bash or earlier), you can upgrade to the Go version with minimal effort. The Go version offers significant performance improvements while maintaining backward compatibility for most configuration variables.

### Compatibility Overview

- ✅ **Both versions can coexist**: The Bash and Go versions can run in the same directory (`/opt/proxmox-backup/`) as they use different internal paths and binary names
- ✅ **Most variables work unchanged**: ~70 configuration variables have identical names between Bash and Go
- ✅ **Automatic fallback support**: 16 renamed variables automatically read old Bash names via fallback mechanism
- ⚠️ **Some variables require manual conversion**: 2 variables have semantic changes (storage thresholds, cloud path format)
- ℹ️ **Legacy variables**: ~27 Bash-only variables are no longer used (replaced by improved internal logic)

### Migration Tools

#### Option 1: Interactive Tool

Automatic tool based on variable mapping: BACKUP_ENV_MAPPING.md (we recommend checking after migration to ensure everything went smoothly)

```bash
./build/proxmox-backup --env-migration
```

You can then manually add your custom variables by referring to the mapping guide.


#### Option 2: Migration Reference Guide (Recommended)

The project includes a complete environment variable mapping guide to help you migrate your configuration:

**📄 [BACKUP_ENV_MAPPING.md](docs/BACKUP_ENV_MAPPING.md)** - Complete Bash → Go variable mapping reference

This guide categorizes every variable:
- **SAME**: Variables with identical names (just copy them)
- **RENAMED ✅**: Variables with new names but automatic fallback (old names still work)
- **SEMANTIC CHANGE ⚠️**: Variables requiring value conversion (e.g., percentage → GB)
- **LEGACY**: Bash-only variables no longer needed in Go

**Quick migration workflow:**
1. Open your Bash `backup.env`
1. Open your Go `backup.env`
3. Refer to `BACKUP_ENV_MAPPING.md` while copying your values
4. Most variables can be copied directly (SAME + RENAMED categories)
5. Pay attention to SEMANTIC CHANGE variables for manual conversion


### Upgrade Steps

1. **Run 1 minute Setup or Full manually Setup**

2. **Migrate your configuration**

   **Option A: Automatic migration (recommended for existing users)**
   ```bash
   # Step 1: Preview migration (recommended first step)
   ./build/proxmox-backup --env-migration-dry-run

   # Review the output, then execute real migration
   ./build/proxmox-backup --env-migration

   # The tool will:
   # - Automatically map 70+ variables (SAME category)
   # - Convert 16 renamed variables (RENAMED category)
   # - Flag 2 variables for manual review (SEMANTIC CHANGE)
   # - Skip 27 legacy variables (LEGACY category)
   # - Create backup of existing config
   ```

   **Option B: Manual migration using mapping guide**
   ```bash
   # Edit with your Bash settings, using BACKUP_ENV_MAPPING.md as reference
   nano configs/backup.env
   ```

3. **Test the configuration**
   ```bash
   # Dry-run to verify configuration
   ./build/proxmox-backup --dry-run

   # Check the output for any warnings or errors
   ```

4. **Run a test backup**
   ```bash
   # First real backup
   ./build/proxmox-backup

   # Verify results
   ls -lh backup/
   cat log/backup-*.log
   ```


### Key Migration Notes

**Automatic variable fallbacks** - These old Bash variable names still work in Go:
- `LOCAL_BACKUP_PATH` → reads as `BACKUP_PATH`
- `ENABLE_CLOUD_BACKUP` → reads as `CLOUD_ENABLED`
- `PROMETHEUS_ENABLED` → reads as `METRICS_ENABLED`
- And 13 more (see mapping guide for complete list)

**Variables requiring conversion:**
- `STORAGE_WARNING_THRESHOLD_PRIMARY="90"` (% used) → `MIN_DISK_SPACE_PRIMARY_GB="1"` (GB free)
- `CLOUD_BACKUP_PATH="/remote:path/folder"` (full path) → `CLOUD_REMOTE_PATH="folder"` (prefix only)

**New Go-only features available:**
- GFS retention policies (`RETENTION_POLICY=gfs`)
- AGE encryption (`ENCRYPT_ARCHIVE=true`)
- Parallel cloud uploads (`CLOUD_UPLOAD_MODE=parallel`)
- Advanced security checks with auto-fix
- Gotify and webhook notifications
- Prometheus metrics export

### Troubleshooting Migration

**Problem**: "Configuration variable not recognized"
- **Solution**: Check `BACKUP_ENV_MAPPING.md` to see if the variable was renamed or is now LEGACY

**Problem**: Storage threshold warnings incorrect
- **Solution**: Convert percentage-based thresholds to GB-based (SEMANTIC CHANGE variables)

**Problem**: Cloud path not working
- **Solution**: Split `CLOUD_BACKUP_PATH` into `CLOUD_REMOTE` (remote:path) and `CLOUD_REMOTE_PATH` (prefix)

**Still having issues?**
- Review the complete mapping guide: [BACKUP_ENV_MAPPING.md](docs/BACKUP_ENV_MAPPING.md)
- Compare your Bash config with the Go template side-by-side
- Enable debug logging: `./build/proxmox-backup --dry-run --log-level debug`

---

## Legacy Bash Version (v0.7.4-bash)

The original Bash script (20,370 lines) has been moved to the `old` branch and is no longer actively developed. However, it remains available for users who need it.

### Availability

- **Source code**: Available in the `old` branch of this repository
- **Installation script**: The `install.sh` file remains in the `main` branch for backward compatibility

### Installing the Legacy Bash Version

The legacy Bash version can still be installed using the original installation command:

#### Option 1: Fast Bash Legacy Install or Update or Reinstall
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxmox-backup/old/install.sh)"
```

#### Option 2: Manual

Enter the /opt directory
```bash
cd /opt
```

Download the repository (stable release)
```bash
wget https://github.com/tis24dev/proxmox-backup/archive/refs/tags/v0.7.4-bash.tar.gz
```

Create the script directory
```bash
mkdir proxmox-backup
```

Extract the script files into the newly created directory, then delete the archive
```bash
tar xzf v0.7.4-bash.tar.gz -C proxmox-backup --strip-components=1 && rm v0.7.4-bash.tar.gz
```

Enter the script directory
```bash
cd proxmox-backup
```

Start the installation (runs initial checks, creates symlinks, creates cron)
```bash
./install.sh
```

Customize your settings
```bash
nano env/backup.env
```

Run first backup
```bash
./script/proxmox-backup.sh

```


**Important Notes:**

- ⚠️ **Manual confirmation required**: The `install.sh` script will ask for explicit confirmation before proceeding with the Bash version installation
- ⚠️ **Bash version only**: The `install.sh` script installs the **legacy Bash version** (v0.7.4-bash), NOT the new Go version
- 📌 **Why it exists**: The `install.sh` file remains in the `main` branch only to support existing installation URLs that may be circulating in documentation, scripts, or forums
- 🔄 **For Go version**: To install the new Go version, follow the [Installation](#installation) section above (build from source)

### Legacy vs Go Version

| Feature | Legacy Bash (v0.7.4) | Go Version (v0.9.0+) |
|---------|---------------------|---------------------|
| **Status** | Maintenance mode (old branch) | Active development (main branch) |
| **Installation** | `install.sh` script | Build from source |
| **Performance** | Slower (shell overhead) | 10-20x faster (compiled) |
| **Code size** | 20,370 lines | ~3,000 lines Go code |
| **Memory usage** | Higher (multiple processes) | Lower (single binary) |
| **Maintenance** | Archived, critical fixes only | Active development |
| **Compatibility** | Can coexist with Go version | Can coexist with Bash version |

### Recommendation

We **strongly recommend** upgrading to the Go version for:
- ✅ Better performance and reliability
- ✅ Active development and new features
- ✅ Cleaner codebase and easier maintenance
- ✅ Lower resource consumption

The legacy Bash version should only be used if you have specific compatibility requirements or cannot build the Go version.

---

## Command-Line Reference

Quick reference for command-line options. For complete details, see **[CLI Reference](docs/CLI_REFERENCE.md)**.

### Common Commands

```bash
# Run backup
./build/proxmox-backup

# Dry-run (test without changes)
./build/proxmox-backup --dry-run

# Installation wizard
./build/proxmox-backup --install

# Full reset + installation (preserves env/identity)
./build/proxmox-backup --new-install

# Migrate from Bash version
./build/proxmox-backup --env-migration

# Generate encryption keys
./build/proxmox-backup --newkey

# Decrypt backup
./build/proxmox-backup --decrypt

# Restore from backup
./build/proxmox-backup --restore

# Use CLI mode instead of TUI (for debugging)
./build/proxmox-backup --install --cli
./build/proxmox-backup --new-install --cli
./build/proxmox-backup --newkey --cli
./build/proxmox-backup --decrypt --cli
./build/proxmox-backup --restore --cli
```

### All Flags

| Flag | Description |
|------|-------------|
| `--config <path>` | Use custom config file |
| `--dry-run` | Test mode (no changes) |
| `--cli` | Force CLI mode instead of TUI (only for: --install, --new-install, --newkey, --decrypt, --restore) |
| `--install` | Interactive installation wizard |
| `--new-install` | Wipe install dir (keep env/identity) then run wizard |
| `--env-migration` | Migrate Bash → Go config |
| `--upgrade-config` | Upgrade config to latest template |
| `--newkey` | Generate AGE encryption key |
| `--decrypt` | Decrypt backup archive |
| `--restore` | Restore from backup to system |
| `--log-level <level>` | Set logging verbosity |
| `--support` | Run with DEBUG logging for developer |
| `--version` | Show version information |
| `--help` | Show help message |

**For complete reference**, see: **[CLI Reference](docs/CLI_REFERENCE.md)**

---

## Configuration Reference

The configuration file `configs/backup.env` contains 200+ variables organized into categories.

**For complete configuration reference**, see: **[Configuration Guide](docs/CONFIGURATION.md)**

### Quick Configuration Example

**File location**: `/opt/proxmox-backup/configs/backup.env`

```bash
# Essential settings
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxmox-backup/backup
LOG_PATH=/opt/proxmox-backup/log

# Compression
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=6

# Retention
MAX_LOCAL_BACKUPS=15

# Cloud storage (optional)
CLOUD_ENABLED=false
CLOUD_REMOTE=gdrive:pbs-backups

# Encryption (optional)
AGE_ENABLED=false
AGE_RECIPIENT_FILE=configs/recipient.txt
```

### Configuration Categories

The configuration file includes 200+ variables across these categories:

- **General**: Basic settings, debug levels, profiling
- **Security**: Permission checks, suspicious process detection, firewall monitoring
- **Storage Paths**: Local, secondary, and cloud storage locations
- **Compression**: Algorithm selection (xz, zstd, gzip, bzip2, lz4) and performance tuning
- **Retention Policies**: Simple (max backups) or GFS (Grandfather-Father-Son)
- **Encryption**: AGE encryption with passphrase or key-based recipients
- **Notifications**: Telegram, Email, Gotify, Webhook integrations
- **Collectors**: PVE cluster configs, PBS datastores, system files, custom paths
- **Cloud Storage**: rclone integration with parallel uploads and retry logic
- **Metrics**: Prometheus metrics export

**Example essential variables**:

```bash
# Basic setup
BACKUP_ENABLED=true
BACKUP_PATH=/opt/proxmox-backup/backup
COMPRESSION_TYPE=xz
MAX_LOCAL_BACKUPS=15

# Cloud storage (optional)
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive:pbs-backups

# Encryption (optional)
AGE_ENABLED=true
AGE_RECIPIENT_FILE=configs/recipient.txt
```

**For detailed configuration of all 200+ variables, see**: **[Configuration Guide](docs/CONFIGURATION.md)**

---

## Cloud Storage

Cloud storage integration via rclone supports 40+ providers (Google Drive, S3, Backblaze B2, OneDrive, MinIO, etc.).

**Quick setup**:

```bash
# 1. Configure rclone
rclone config

# 2. Enable in backup.env
CLOUD_ENABLED=true
CLOUD_REMOTE=gdrive:pbs-backups
MAX_CLOUD_BACKUPS=30
```

**For complete cloud storage guide**, see: **[Cloud Storage Guide](docs/CLOUD_STORAGE.md)**

---

## Encryption

AGE encryption provides streaming encryption for backups with passphrase or key-based recipients.

**Quick setup**:

```bash
# 1. Generate encryption key
./build/proxmox-backup --newkey

# 2. Enable in backup.env
AGE_ENABLED=true
AGE_RECIPIENT_FILE=configs/recipient.txt

# 3. Run encrypted backup
./build/proxmox-backup
```

**For complete encryption guide**, see: **[Encryption Guide](docs/ENCRYPTION.md)**

---

## Practical Examples

Real-world configuration examples for common deployment scenarios.

**Available examples**:
1. **Basic Local Backup** - Single server, local storage only
2. **Local + Secondary Storage** - Local SSD + NAS with different retention
3. **Cloud Backup with Google Drive** - GFS retention, Google Drive
4. **Encrypted Backup with AGE** - Sensitive data, encryption required
5. **Backblaze B2 with Bandwidth Limiting** - Long-term archival, slow network
6. **MinIO Self-Hosted** - LAN-based, high performance, hourly backups
7. **Multi-Notification Setup** - Telegram + Email + Webhook
8. **Complete Production Setup** - Enterprise setup with all features

**For complete examples**, see: **[Examples](docs/EXAMPLES.md)**

> **Testing in a chroot/fixture?**
> Set `SYSTEM_ROOT_PREFIX=/tmp/fake-root` in `backup.env` to collect system/PVE/PBS configs from an alternate root without touching the live host. Useful for CI or offline validation of snapshots.

---

## Troubleshooting

Common issues and solutions.

**Common problems**:
- Build failures (Go modules, dependencies)
- Configuration issues (file not found, invalid values)
- Cloud storage (rclone not found, authentication, timeouts)
- Encryption (passphrase incorrect, key mismatch)
- Disk space (insufficient space, retention issues)

**For complete troubleshooting guide**, see: **[Troubleshooting Guide](docs/TROUBLESHOOTING.md)

---

## Contributing

We welcome contributions! For details on development setup, coding guidelines, and how to contribute, see: **[Developer Guide](docs/DEVELOPER_GUIDE.md)**

**Quick links**:
- Report bugs or request features: [GitHub Issues](https://github.com/tis24dev/proxmox-backup/issues)
- Submit code: Fork, create branch, submit PR
- Improve documentation: Fix typos, add examples
- Star the repo to show support!

---

## Documentation

Complete documentation is available in the `docs/` directory:

| Document | Description |
|----------|-------------|
| **[Configuration Guide](docs/CONFIGURATION.md)** | Complete reference for all 200+ variables |
| **[CLI Reference](docs/CLI_REFERENCE.md)** | All command-line flags and options |
| **[Encryption Guide](docs/ENCRYPTION.md)** | AGE encryption setup and usage |
| **[Cloud Storage Guide](docs/CLOUD_STORAGE.md)** | rclone integration for 40+ providers |
| **[Restore Guide](docs/RESTORE_GUIDE.md)** | Complete restore workflows |
| **[Restore Technical](docs/RESTORE_TECHNICAL.md)** | Technical architecture details |
| **[Cluster Recovery](docs/CLUSTER_RECOVERY.md)** | Disaster recovery procedures |
| **[Restore Diagrams](docs/RESTORE_DIAGRAMS.md)** | Visual workflow diagrams |
| **[Examples](docs/EXAMPLES.md)** | Real-world configuration examples |
| **[Troubleshooting](docs/TROUBLESHOOTING.md)** | Common issues and solutions |
| **[Migration Guide](docs/MIGRATION_GUIDE.md)** | Bash → Go upgrade guide |
| **[Developer Guide](docs/DEVELOPER_GUIDE.md)** | Contributing and development |
| **[Legacy Bash](docs/LEGACY_BASH.md)** | Information about Bash version |
| **[Provenance Verification](docs/PROVENANCE_VERIFICATION.md)** | Build attestation verification guide |

---

## License

This project is licensed under the **MIT License** - see the [LICENSE](LICENSE) file for details.

### Third-Party Licenses

This project uses third-party libraries licensed under BSD-3-Clause and MIT licenses. For complete attribution and license texts, see [NOTICE](NOTICE).

---

## Build Provenance

All release binaries include cryptographically signed build provenance attestations that prove the binary was built from this repository using GitHub Actions.

### What Are Attestations?

Build provenance attestations provide cryptographic proof that:
- ✓ The binary was built from this exact repository and commit
- ✓ The binary was built using the official GitHub Actions workflow
- ✓ The binary has not been tampered with after the build
- ✓ The build process is traceable and verifiable via public transparency log

This protects you from supply chain attacks, unauthorized builds, and tampered binaries.

### Quick Verification

Before using a downloaded binary, verify its authenticity:

```bash
# 1. Download the binary for your platform
wget https://github.com/tis24dev/proxmox-backup/releases/download/v0.9.1/proxmox-backup-linux-amd64

# 2. Verify the attestation (requires GitHub CLI)
gh attestation verify proxmox-backup-linux-amd64 --repo tis24dev/proxmox-backup

# 3. If verification succeeds, you can safely use the binary
chmod +x proxmox-backup-linux-amd64
./proxmox-backup-linux-amd64 --version
```

**Expected output:**
```
✓ Verification succeeded!

sha256:abc123... was attested by:
REPO                        PREDICATE_TYPE                  WORKFLOW
tis24dev/proxmox-backup     https://slsa.dev/provenance/v1  .github/workflows/release.yml@refs/tags/v0.9.1
```

### Prerequisites

To verify attestations, you need GitHub CLI (`gh`):

```bash
# Linux (Debian/Ubuntu)
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
sudo apt update && sudo apt install gh

# macOS
brew install gh

# Windows
winget install --id GitHub.cli
```

### Detailed Verification Guide

For complete instructions including:
- Multiple verification methods (single binary, all artifacts, JSON output, offline)
- Platform-specific examples (Linux, macOS, Windows)
- Troubleshooting common issues
- Security considerations and trust model
- Integration with automated scripts

See the complete guide: **[Provenance Verification Guide](docs/PROVENANCE_VERIFICATION.md)**

### Migration from GPG Signing

Previous releases (v0.9.0 and earlier) used GPG signatures. Starting from v0.9.1, we use GitHub's native attestation system which provides:
- **Easier verification**: Single `gh` command instead of GPG key management
- **Better transparency**: Public immutable log of all attestations
- **Modern security**: OIDC-based keyless signing with Sigstore
- **SLSA compliance**: Industry-standard provenance metadata

Old GPG signatures: `gpg --verify SHA256SUMS.asc SHA256SUMS`
New attestations: `gh attestation verify <binary> --repo tis24dev/proxmox-backup`

### Security Scanning

This project uses automated security scanning to ensure code quality and detect vulnerabilities:

**Static Analysis Security Testing (SAST):**
- **GoSec**: Go-specific security scanner that checks for common vulnerabilities (SQL injection, command injection, weak crypto, etc.)
  - Runs on: Every push, pull request, and weekly schedule
  - Results: Available in GitHub Security tab

- **CodeQL**: Deep semantic analysis for complex security issues and code quality
  - Runs on: Every push, pull request, and weekly schedule
  - Queries: `security-extended` + `security-and-quality`
  - Results: Available in GitHub Security tab

**Why This Matters:**
- Proactive detection of security vulnerabilities before they reach production
- Continuous monitoring for newly discovered vulnerability patterns
- Automated security review as part of CI/CD pipeline
- Complementary to build provenance attestations (code quality + artifact integrity)

View security scan results: [GitHub Security Tab](https://github.com/tis24dev/proxmox-backup/security)

**Supply Chain Security:**
- **Dependabot**: Automated dependency updates with security vulnerability detection
  - Schedule: Weekly (Monday 02:00 UTC)
  - Auto-merge: Enabled for patch/minor security updates
  - Manual review: Required only for major version updates

- **Dependency Review**: Blocks PRs introducing vulnerable or malicious dependencies
  - Triggers: On all PRs modifying go.mod/go.sum
  - Blocks: Critical CVE + unapproved licenses (GPL, AGPL)
  - Warns: High/Medium/Low CVE (doesn't block)

**Zero-Touch Operation:**
- Security patches auto-merged within 24-48h of release
- Critical CVEs block deployment automatically
- Manual intervention required only for breaking changes (~1-2 times/year)

---

## Restore Operations

⚠️ **Note**: This section has been moved to dedicated documentation.

**For complete restore documentation**, see:
- **[Restore Guide](docs/RESTORE_GUIDE.md)** - Complete user guide with all restore modes
- **[Restore Technical](docs/RESTORE_TECHNICAL.md)** - Technical architecture and internals
- **[Cluster Recovery](docs/CLUSTER_RECOVERY.md)** - Advanced disaster recovery procedures
- **[Restore Diagrams](docs/RESTORE_DIAGRAMS.md)** - Visual workflow diagrams

### Quick Restore Summary

**Interactive restore workflow**:
1. Run `./build/proxmox-backup --restore`
2. Select backup source (local/secondary/cloud)
3. Decrypt if encrypted (provide passphrase/key)
4. Choose restore mode (Full/Storage/Base/Custom)
5. Select categories to restore
6. Confirm and execute

**Restore modes**:
- **Full**: All categories except export-only
- **Storage**: PVE/PBS-specific configs
- **Base**: Network, SSH, system files
- **Custom**: Select specific categories

---

## Conclusion

This README provides a quick overview of Proxmox Backup Go. For detailed information, see the documentation files in the `docs/` directory.

**Next steps**:
1. **Install**: Follow [Installation](#installation) section
2. **Configure**: Edit `configs/backup.env`
3. **Test**: Run `--dry-run`
4. **Schedule**: Set up cron jobs
5. **Monitor**: Check logs and notifications

**For complete documentation**, see: **[Documentation](#documentation)** section above.

---

Thank you for using Proxmox Backup Go! 🎉
