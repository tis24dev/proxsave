## 📑 Table of Contents

- [🚀 Fast Install](#fast-instal)
  - [Direct Install](#direct-install)
  - [Migration](#migration)
  - [First Backup Workflow](#first-backup-workflow)
- [💾 Manual Installation](#installation)
  - [Prerequisites](#prerequisites)
  - [Building from Source](#building-from-source)
  - [Interactive Installation Wizard](#interactive-installation-wizard)
- [🔄 Upgrading from Bash Version](#upgrading-from-previous-bash-version-v074-bash-or-earlier)
  - [Migration Tools](#migration-tools)
  - [Upgrade Steps](#upgrade-steps)
- [📜 Legacy Bash Version](#legacy-bash-version-v074-bash)
  - [Installing the Legacy Bash Version](#installing-the-legacy-bash-version)
- [⌨️ Legacy vs Go Version](#legacy-vs-go-version)


## Fast Install

### Direct Install

1. Download & start Install
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)"
```

or: if you need a fully clean reinstall use: (preserves `env/` and `identity/`)
```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" _ --new-install
```

2. Run your first backup
```bash
./build/proxsave
```

### Migration
1. Run migration installation from bash with old env file
```bash
./build/proxsave --env-migration
```

2. Run your first backup again after migration
```bash
./build/proxsave
```

### First Backup Workflow

```bash
# Dry-run test (no actual changes)
./build/proxsave --dry-run

# Real backup
./build/proxsave

# View logs
tail -f log/backup-$(hostname)-*.log

# Check backup files
ls -lh backup/
```

---

## Manual Installation
> Allows you to compile your binary file from individual project files.

### Prerequisites

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
mkdir /opt/proxsave

# Navigate to project directory
cd /opt/proxsave

# Copy from github
git clone --branch main https://github.com/tis24dev/proxsave.git .

# Initialize Go module
go mod init github.com/tis24dev/proxsave

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
./build/prosave --install

# Or perform a clean reinstall (keeps env/ and identity/)
./build/proxsave --new-install
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

### Migration Tools

#### Option 1: Interactive Tool

Automatic tool based on variable mapping: BACKUP_ENV_MAPPING.md (we recommend checking after migration to ensure everything went smoothly)

```bash
./build/proxsave --env-migration
```

You can then manually add your custom variables by referring to the mapping guide.


#### Option 2: Migration Reference Guide

The project includes a complete environment variable mapping guide to help you migrate your configuration:

**?? [BACKUP_ENV_MAPPING.md](docs/BACKUP_ENV_MAPPING.md)** - Complete Bash ? Go variable mapping reference

This guide categorizes every variable:
- **SAME**: Variables with identical names (just copy them)
- **RENAMED ?**: Variables with new names but automatic fallback (old names still work)
- **SEMANTIC CHANGE ??**: Variables requiring value conversion (e.g., percentage ? GB)
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
   ./build/proxsave --env-migration-dry-run

   # Review the output, then execute real migration
   ./build/proxsave --env-migration

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
   ./build/proxsave --dry-run

   # Check the output for any warnings or errors
   ```

4. **Run a test backup**
   ```bash
   # First real backup
   ./build/proxsave

   # Verify results
   ls -lh backup/
   cat log/backup-*.log
   ```


### Key Migration Notes

**Automatic variable fallbacks** - These old Bash variable names still work in Go:
- `LOCAL_BACKUP_PATH` ? reads as `BACKUP_PATH`
- `ENABLE_CLOUD_BACKUP` ? reads as `CLOUD_ENABLED`
- `PROMETHEUS_ENABLED` ? reads as `METRICS_ENABLED`
- And 13 more (see mapping guide for complete list)

**Variables requiring conversion:**
- `STORAGE_WARNING_THRESHOLD_PRIMARY="90"` (% used) ? `MIN_DISK_SPACE_PRIMARY_GB="1"` (GB free)
- `CLOUD_BACKUP_PATH="/remote:path/folder"` (full path) ? `CLOUD_REMOTE_PATH="folder"` (prefix only)

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
- Enable debug logging: `./build/proxsave --dry-run --log-level debug`

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
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/old/install.sh)"
```

#### Option 2: Manual

Enter the /opt directory
```bash
cd /opt
```

Download the repository (stable release)
```bash
wget https://github.com/tis24dev/proxsave/archive/refs/tags/v0.7.4-bash.tar.gz
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

- ?? **Manual confirmation required**: The `install.sh` script will ask for explicit confirmation before proceeding with the Bash version installation
- ?? **Bash version only**: The `install.sh` script installs the **legacy Bash version** (v0.7.4-bash), NOT the new Go version
- ?? **Why it exists**: The `install.sh` file remains in the `main` branch only to support existing installation URLs that may be circulating in documentation, scripts, or forums
- ?? **For Go version**: To install the new Go version, follow the [Installation](#installation) section above (build from source)

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