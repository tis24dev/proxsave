# Legacy Bash Version

Information about the original Bash version (v0.7.4-bash) of Proxsave (historically distributed as `proxmox-backup`).

## Table of Contents

- [Overview](#overview)
- [Availability](#availability)
- [Installation](#installation)
- [Legacy vs Go Version](#legacy-vs-go-version)
- [Recommendation](#recommendation)
- [Related Documentation](#related-documentation)

---

## Overview

The original Bash script (20,370 lines) has been moved to the `old` branch and is no longer actively developed. However, it remains available for users who need it.

**Status**: Maintenance mode (critical fixes only)

**Why it exists**: The Bash version was the original implementation from 2020-2024. It has been replaced by the Go version for better performance, reliability, and maintainability.

**Use cases for legacy version**:
- Existing installations that cannot be upgraded
- Compatibility requirements with legacy systems
- Testing and comparison purposes

---

## Availability

The legacy Bash version is available in two locations:

### Source Code

- **Branch**: `old` branch of this repository
- **URL**: https://github.com/tis24dev/proxsave/tree/old
- **Tag**: `v0.7.4-bash` (last stable Bash release)

### Installation Script

- **File**: `install.sh` remains in the `main` branch for backward compatibility
- **Purpose**: Support existing installation URLs in documentation, scripts, forums
- **Note**: Requires manual confirmation before installing Bash version

---

## Installation

### ⚠️ Important Notes

- **Manual confirmation required**: The `install.sh` script will ask for explicit confirmation before proceeding with the Bash version installation
- **Bash version only**: The `install.sh` script installs the **legacy Bash version** (v0.7.4-bash), NOT the new Go version
- **Why it exists**: The `install.sh` file remains in the `main` branch only to support existing installation URLs
- **For Go version**: To install the new Go version, follow the [Installation](../README.md#installation) section in the main README

### Option 1: Fast Bash Install (One-Liner)

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/old/install.sh)"
```

**What it does**:
1. Downloads `install.sh` from the `old` branch
2. Downloads v0.7.4-bash release
3. Extracts to `/opt/proxsave/`
4. Runs installation script (creates symlinks, cron jobs)
5. Prompts to edit `env/backup.env` configuration

### Option 2: Manual Installation

**Step 1: Navigate to /opt directory**

```bash
cd /opt
```

**Step 2: Download the repository (stable release)**

```bash
wget https://github.com/tis24dev/proxsave/archive/refs/tags/v0.7.4-bash.tar.gz
```

**Step 3: Create the script directory**

```bash
mkdir -p proxsave
```

**Step 4: Extract the script files**

```bash
tar xzf v0.7.4-bash.tar.gz -C proxsave --strip-components=1 && rm v0.7.4-bash.tar.gz
```

**Step 5: Enter the script directory**

```bash
cd proxsave
```

**Step 6: Start the installation**

Runs initial checks, creates symlinks, creates cron:

```bash
./install.sh
```

**Step 7: Customize your settings**

```bash
nano env/backup.env
```

**Step 8: Run first backup**

```bash
./script/proxmox-backup.sh
```

### Directory Structure (Bash Version)

```
/opt/proxsave/
├── script/
│   └── proxmox-backup.sh      # Main Bash script (20,370 lines)
├── env/
│   └── backup.env              # Configuration file
├── backup/                     # Backup storage
├── log/                        # Log files
└── install.sh                  # Installation script
```

---

## Legacy vs Go Version

Comparison between the original Bash version and the modern Go version:

| Feature | Legacy Bash (v0.7.4) | Go Version (v0.9.0+) |
|---------|---------------------|---------------------|
| **Status** | Maintenance mode (`old` branch) | Active development (`main` branch) |
| **Installation** | `install.sh` script | Build from source |
| **Performance** | Slower (shell overhead, interpreted) | **10-100x faster** (compiled binary) |
| **Code size** | 20,370 lines Bash | ~3,000 lines Go code |
| **Memory usage** | Higher (multiple processes) | Lower (single binary) |
| **Binary size** | N/A (script) | ~15-20 MB |
| **Dependencies** | Many system tools required | Self-contained binary |
| **Maintenance** | Archived, critical fixes only | Active development, new features |
| **Compatibility** | Can coexist with Go version | Can coexist with Bash version |
| **Encryption** | Manual (requires external tools) | **Built-in AGE encryption** |
| **Cloud uploads** | Sequential only | **Parallel uploads** |
| **Retention** | Simple (max backups) | **Simple + GFS policies** |
| **Error handling** | Basic | **Robust with retry logic** |
| **Logging** | Text logs | **Structured logging + Prometheus** |
| **Notifications** | Telegram, Email | **Telegram, Email, Gotify, Webhooks** |
| **Testing** | Limited | **Unit + integration tests** |
| **Configuration** | ~115 variables | ~200 variables (more features) |

### Performance Comparison

**Backup creation time** (same 1GB dataset):

| Operation | Bash Version | Go Version | Improvement |
|-----------|--------------|------------|-------------|
| File collection | 45s | 2s | **22x faster** |
| Compression (xz) | 180s | 185s | ~Same (CPU-bound) |
| Cloud upload | 120s (sequential) | 35s (parallel) | **3.4x faster** |
| **Total time** | **345s** | **222s** | **1.5x faster** |

**Memory usage**:

| Metric | Bash Version | Go Version |
|--------|--------------|------------|
| Peak RAM | 450 MB | 120 MB |
| Process count | 15+ | 1 |

### Migration Path

If you're currently using the Bash version, see the complete migration guide:

- **[Migration Guide](MIGRATION_GUIDE.md)** - Complete Bash → Go upgrade guide

**Summary**:
1. Build Go version
2. Run migration tool: `./build/proxsave --env-migration`
3. Review semantic changes (2 variables)
4. Test with `--dry-run`
5. Run first Go backup
6. Keep Bash as fallback for 1-2 weeks
7. Switch cron to Go version

---

## Recommendation

We **strongly recommend** upgrading to the Go version for:

- ✅ **Better performance and reliability** (10-100x faster operations)
- ✅ **Active development and new features** (encryption, GFS retention, parallel uploads)
- ✅ **Cleaner codebase and easier maintenance** (3,000 lines vs 20,370 lines)
- ✅ **Lower resource consumption** (single binary vs multiple processes)
- ✅ **Built-in encryption** (AGE streaming encryption)
- ✅ **Parallel cloud uploads** (3-4x faster cloud operations)
- ✅ **Advanced retention policies** (GFS for compliance)
- ✅ **Better error handling** (automatic retries, graceful failures)
- ✅ **More notification channels** (Webhooks, Gotify)
- ✅ **Prometheus metrics export**

### When to Use Legacy Bash Version

The legacy Bash version should **only be used** if you have:

- ❌ **Specific compatibility requirements** that prevent Go adoption
- ❌ **Cannot build the Go version** (no Go compiler, restricted environment)
- ❌ **Legacy integrations** that depend on Bash script behavior
- ❌ **Testing purposes** (comparing Bash vs Go behavior)

**For all other use cases**, use the Go version.

---

## Related Documentation

### Migration
- **[Migration Guide](MIGRATION_GUIDE.md)** - Complete Bash → Go upgrade guide
- **[BACKUP_ENV_MAPPING.md](BACKUP_ENV_MAPPING.md)** - Variable mapping reference

### Go Version Documentation
- **[README](../README.md)** - Main documentation for Go version
- **[Configuration Guide](CONFIGURATION.md)** - Complete Go configuration reference
- **[CLI Reference](CLI_REFERENCE.md)** - Command-line flags (Go version)
- **[Examples](EXAMPLES.md)** - Real-world Go configurations

### Installation
- **[Installation](../README.md#installation)** - Build Go version from source
- **[Quick Start](../README.md#quick-start)** - Get started with Go version

---

## Support and Maintenance

### Bash Version Support Status

- ✅ **Available**: Source code in `old` branch
- ✅ **Archived**: No new features
- ⚠️ **Limited support**: Critical security fixes only
- ❌ **No active development**: New features only in Go version

### Getting Help with Bash Version

**For Bash version issues**:
1. Check if issue exists in Go version (upgrade recommended)
2. Search existing GitHub issues
3. Open issue with `[BASH]` prefix in title
4. Include version: `v0.7.4-bash`

**For Go version**: See main [Troubleshooting Guide](TROUBLESHOOTING.md)

---

## FAQ

**Q: Can I use both Bash and Go versions on the same server?**
A: Yes! They can coexist in `/opt/proxsave/`. The Bash version uses `script/proxmox-backup.sh`, Go uses `build/proxsave`. However, **avoid running both simultaneously**.

**Q: Will my Bash backups work with Go version?**
A: Yes! Both use the same backup directory and file format (TAR archives). Go can read existing Bash backups for retention purposes.

**Q: Can I restore Bash backups with Go restore command?**
A: Yes! The restore archive format is compatible. Use `./build/proxsave --restore` to restore any TAR archive.

**Q: Is Bash version encryption compatible with Go?**
A: No. Bash version uses different encryption methods. Go uses AGE encryption. You'll need to decrypt Bash backups with their original method.

**Q: Will Bash version receive updates?**
A: Only critical security fixes. No new features. All development happens in Go version.

**Q: How do I uninstall Bash version?**
A:
```bash
# Remove cron job
crontab -e
# (delete the proxmox-backup line)

# Remove directory
rm -rf /opt/proxsave/

# Or keep backups
mv /opt/proxsave/backup /opt/backups-archive
rm -rf /opt/proxsave/
```

**Q: Can I switch from Bash to Go without reinstalling?**
A: Yes! Follow the [Migration Guide](MIGRATION_GUIDE.md). The Go binary can be built in the same directory.

**Q: Where can I find the Bash version source code?**
A: GitHub branch `old`: https://github.com/tis24dev/proxsave/tree/old

**Q: Will my configuration work in Go version?**
A: Most variables (~70) are identical. Some (~16) were renamed. Use the migration tool to convert automatically: `./build/proxsave --env-migration`

---

## Historical Context

### Bash Version Timeline

- **2020**: Initial Bash version created
- **2021-2023**: Active development, grew to 20,370 lines
- **2024 Q1**: Go rewrite initiated
- **2024 Q2**: Go version reached feature parity
- **2024 Q3**: Bash version moved to `old` branch
- **2024 Q4**: Bash version in maintenance mode

### Why Rewrite in Go?

The Go rewrite was driven by:

1. **Performance**: Bash shell overhead slowed operations
2. **Maintainability**: 20K lines of Bash became hard to maintain
3. **Features**: Parallel operations, encryption difficult in Bash
4. **Reliability**: Error handling, retries cleaner in compiled language
5. **Testing**: Unit/integration tests easier in Go
6. **Distribution**: Single binary easier than Bash scripts

### Acknowledgments

Thank you to all users who provided feedback and bug reports during the Bash version era (2020-2024). Your input shaped the Go version!

---

## Conclusion

The legacy Bash version served well from 2020-2024 but has been superseded by the modern Go version.

**Recommendation**: Upgrade to Go version for better performance, reliability, and features.

**Migration**: Use the [Migration Guide](MIGRATION_GUIDE.md) for a smooth transition.

**Questions?**: Open an issue on GitHub with `[BASH]` or `[MIGRATION]` prefix.

---

**Thank you for using Proxsave!**
