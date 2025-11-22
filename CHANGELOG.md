# Changelog

All notable changes to the Proxmox Backup Go project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

#### Retention Policies - GFS (Grandfather-Father-Son) System
- **Intelligent time-distributed backup retention** as alternative to simple count-based retention
- **Explicit policy selection**: GFS mode activates when `RETENTION_POLICY=gfs` (simple retention remains the default)
- **Four retention categories**:
  - `RETENTION_DAILY`: Keep last N backups (daily tier, minimum accepted is 1; 0 is treated as 1)
  - `RETENTION_WEEKLY`: Keep N weekly backups (1 per ISO week)
  - `RETENTION_MONTHLY`: Keep N monthly backups (1 per month)
  - `RETENTION_YEARLY`: Keep N yearly backups (1 per year, 0 = unlimited)
- **ISO 8601 week numbering** for consistent weekly backup classification
- **Per-destination policies**: Local, secondary, and cloud can use different retention strategies
- **Predictive logging**: Shows classification results and deletion count before execution
- **Uniform logging format**: Both Simple and GFS modes now display multi-line status with consistent formatting
- **Real-time classification display**: Storage initialization shows current vs. limit for each category

#### Enhanced Storage Messages
- **Unified format for Simple and GFS retention modes**:
  ```
  ✓ Local storage initialized (present 25 backups)
    Policy: GFS (daily=7, weekly=4, monthly=12, yearly=3)
    Total: 25/-
    Daily: 7/7
    Weekly: 4/4
    Monthly: 12/12
    Yearly: 2/3
  ```
- **Detailed retention application logging**:
  ```
  GFS classification → daily: 7/7, weekly: 4/4, monthly: 12/12, yearly: 2/3, to_delete: 0
  ```
- **Simple retention gets same multi-line format**:
  ```
  ✓ Local storage initialized (present 20 backups)
    Policy: simple (keep 15 newest)

  Simple retention → current: 26, limit: 15, to_delete: 11
  ```

### Changed
- **Storage initialization messages**: Replaced misleading "retention 0 backups" with detailed categorical breakdown for GFS mode
- **Retention logging**: Changed from `daily=%d` to `daily: %d/%d` format showing current/limit for all categories
- **Configuration file**: Added comprehensive GFS documentation in `configs/backup.env` template

### Technical Details
- New file: `internal/storage/retention.go` with GFS classification algorithm
- Updated: `internal/config/config.go` with GFS-related fields (`RetentionDaily`, `RetentionWeekly`, `RetentionMonthly`, `RetentionYearly`)
- Updated: `internal/storage/local.go`, `secondary.go`, `cloud.go` with GFS support
- Updated: `cmd/proxmox-backup/main.go` with enhanced `formatStorageInitSummary()` function
- Environment variable override support for all retention settings

## [0.1.0-dev] - 2025-11-14

### Completed Phases

#### Phase 5.2 - Webhook Notifications ✅
- Multi-endpoint webhook notifier (Discord, Slack, Teams, generic JSON)
- Fine-grained environment configuration per endpoint
- Structured payload builder with template support
- Cloud relay integration with sensitive data masking
- Extensive debug logging

#### Phase 5.1 - Notifications ✅
- Telegram notifications (personal/centralized modes)
- Email notifications (relay/sendmail with fallback)
- HTML email templates
- Auto-detection of recipients
- Non-blocking error handling
- Security preflight checks

#### Phase 4.2 - Storage Operations ✅
- Multi-storage support (local, secondary, cloud)
- Cloud integration (rclone)
- Filesystem detection and compatibility checks
- Bundle creation (tar with compression=0)
- Simple retention policies (count-based)

#### Phase 4.1 - Collection ✅
- Data collection (PVE, PBS, System)
- Archive creation & compression
- Integrity verification (SHA256)
- PXAR metadata sampling
- Custom backup paths and blacklists

#### Phase 3 - Environment & Core ✅
- Proxmox type detection (PVE vs PBS)
- Environment validation
- Directory setup and permissions

#### Phase 2 - Hybrid Orchestrator ✅
- Go main entry point
- CLI argument parsing with cobra
- Signal handling (SIGINT, SIGTERM)

#### Phase 1 - Core Infrastructure ✅
- Configuration system with .env support
- Logging framework (structured logging)
- Error handling and exit codes
- Basic utilities

#### Phase 0 - Initial Setup ✅
- Go project structure
- Build system (Makefile)
- Git workflow

### Known Issues
- Metrics (Prometheus) not yet implemented (Phase 5.3)
- YAML configuration format planned but not implemented

---

## Version History

- **0.1.0-dev**: Current development version
- **[Unreleased]**: Features in development

---

**Last Updated**: 2025-11-14
