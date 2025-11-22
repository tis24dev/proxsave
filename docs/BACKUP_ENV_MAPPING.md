# backup.env – Legacy vs Go Mapping

This file documents the mapping between variables from the **old Bash `backup.env`**
(`reference/env/backup.env`) and the **new Go template**
(`internal/config/templates/backup.env` + `internal/config/config.go`).

> Note: where the name is identical, the variable is already compatible.

## Conventions

- `= SAME` → same name and same meaning.
- `= RENAMED(<new_name>) ✅` → the logic is the same but the recommended name in the new template is different. **The old name is still accepted** via automatic fallback in Go.
- `= SEMANTIC CHANGE ⚠️` → not only the name changes, but also the meaning or value format. **Requires manual conversion**.
- `= LEGACY` → used only in the reference Bash script; Go no longer reads it or has equivalent internal logic.

## Unchanged variables (same name and meaning)

AUTO_UPDATE_HASHES        = SAME
BACKUP_BLACKLIST          = SAME
BACKUP_CEPH_CONFIG        = SAME
BACKUP_CLUSTER_CONFIG     = SAME
BACKUP_CRITICAL_FILES     = SAME
BACKUP_INSTALLED_PACKAGES = SAME
BACKUP_PVE_BACKUP_FILES   = SAME
BACKUP_PVE_FIREWALL       = SAME
BACKUP_PVE_JOBS           = SAME
BACKUP_PVE_REPLICATION    = SAME
BACKUP_PVE_SCHEDULES      = SAME
BACKUP_PXAR_FILES         = SAME (you can also use PXAR_SCAN_ENABLE)
BACKUP_SCRIPT_DIR         = SAME
BACKUP_SMALL_PVE_BACKUPS  = SAME
BACKUP_VM_CONFIGS         = SAME
BACKUP_VZDUMP_CONFIG      = SAME
BACKUP_ZFS_CONFIG         = SAME
BACKUP_USER               = SAME (now used by Go for SET_BACKUP_PERMISSIONS)
BACKUP_GROUP              = SAME (now used by Go for SET_BACKUP_PERMISSIONS)
SET_BACKUP_PERMISSIONS    = SAME (enables chown/chmod on backup/log in Go too)
BOT_TELEGRAM_TYPE         = SAME
CEPH_CONFIG_PATH          = SAME
CHECK_FIREWALL            = SAME
CHECK_NETWORK_SECURITY    = SAME
CHECK_OPEN_PORTS          = SAME
CLOUD_LOG_PATH            = SAME
CLOUD_PARALLEL_MAX_JOBS   = SAME
CLOUD_PARALLEL_VERIFICATION = SAME
CLOUD_UPLOAD_MODE         = SAME
COMPRESSION_LEVEL         = SAME
COMPRESSION_MODE          = SAME
COMPRESSION_THREADS       = SAME
COMPRESSION_TYPE          = SAME
COROSYNC_CONFIG_PATH      = SAME
CUSTOM_BACKUP_PATHS       = SAME
DEBUG_LEVEL               = SAME
DISABLE_NETWORK_PREFLIGHT = SAME
EMAIL_DELIVERY_METHOD     = SAME
EMAIL_ENABLED             = SAME
EMAIL_FALLBACK_SENDMAIL   = SAME
EMAIL_FROM                = SAME
EMAIL_RECIPIENT           = SAME
ENABLE_DEDUPLICATION      = SAME
ENABLE_PREFILTER          = SAME
ENABLE_SMART_CHUNKING     = SAME
MAX_CLOUD_BACKUPS         = SAME
MAX_LOCAL_BACKUPS         = SAME
MAX_PVE_BACKUP_SIZE       = SAME
MAX_SECONDARY_BACKUPS     = SAME
PBS_DATASTORE_PATH        = SAME
PORT_WHITELIST            = SAME
PVE_BACKUP_INCLUDE_PATTERN = SAME
PVE_CLUSTER_PATH          = SAME
PVE_CONFIG_PATH           = SAME
PXAR_STOP_ON_CAP          = SAME
RCLONE_BANDWIDTH_LIMIT    = SAME
RCLONE_FLAGS              = SAME
SECONDARY_LOG_PATH        = SAME
SECURITY_CHECK_ENABLED    = SAME (you can also use FULL_SECURITY_CHECK)
SUSPICIOUS_PORTS          = SAME
TELEGRAM_BOT_TOKEN        = SAME
TELEGRAM_CHAT_ID          = SAME
TELEGRAM_ENABLED          = SAME
VZDUMP_CONFIG_PATH        = SAME
WEBHOOK_ENABLED           = SAME
WEBHOOK_ENDPOINTS         = SAME
WEBHOOK_FORMAT            = SAME
WEBHOOK_MAX_RETRIES       = SAME
WEBHOOK_RETRY_DELAY       = SAME
WEBHOOK_TIMEOUT           = SAME  

## Renamed variables / Supported aliases in Go

### Renamed with automatic fallback ✅
*The old name continues to work - gradual migration possible*

ABORT_ON_SECURITY_ISSUES   = RENAMED(CONTINUE_ON_SECURITY_ISSUES) ✅ *with inverted logic*
BACKUP_CRONTABS            = RENAMED(BACKUP_CRON_JOBS) ✅
BACKUP_NETWORK_CONFIG      = RENAMED(BACKUP_NETWORK_CONFIGS) ✅
BACKUP_REMOTE_CFG          = RENAMED(BACKUP_REMOTE_CONFIGS) ✅
CLOUD_CONNECTIVITY_TIMEOUT = RENAMED(RCLONE_TIMEOUT_CONNECTION) ✅
DISABLE_COLORS             = RENAMED(USE_COLOR) ✅ *with inverted logic*
ENABLE_CLOUD_BACKUP        = RENAMED(CLOUD_ENABLED) ✅
ENABLE_SECONDARY_BACKUP    = RENAMED(SECONDARY_ENABLED) ✅
FULL_SECURITY_CHECK        = RENAMED(SECURITY_CHECK_ENABLED) ✅
LOCAL_BACKUP_PATH          = RENAMED(BACKUP_PATH) ✅
LOCAL_LOG_PATH             = RENAMED(LOG_PATH) ✅
PROMETHEUS_ENABLED         = RENAMED(METRICS_ENABLED) ✅
PROMETHEUS_TEXTFILE_DIR    = RENAMED(METRICS_PATH) ✅
PXAR_INCLUDE_PATTERN       = RENAMED(PXAR_FILE_INCLUDE_PATTERN) ✅
RCLONE_REMOTE              = RENAMED(CLOUD_REMOTE) ✅
SECONDARY_BACKUP_PATH      = RENAMED(SECONDARY_PATH) ✅

### Semantic changes ⚠️
*Require manual value conversion*

CLOUD_BACKUP_PATH = SEMANTIC CHANGE → CLOUD_REMOTE_PATH ⚠️
  **Bash**: `CLOUD_BACKUP_PATH="/proxmox-backup/backup"` (full path)
  **Go**: `CLOUD_REMOTE_PATH="proxmox-backup"` (prefix that combines with CLOUD_REMOTE)
  **Migration**: remove the remote name and trailing path from the bash variable

STORAGE_WARNING_THRESHOLD_SECONDARY = SEMANTIC CHANGE → MIN_DISK_SPACE_SECONDARY_GB ⚠️
  **Bash**: `STORAGE_WARNING_THRESHOLD_SECONDARY="90"` (90% used = warning)
  **Go**: `MIN_DISK_SPACE_SECONDARY_GB="10"` (minimum 10GB free required)
  **Migration**: manually calculate free GB based on your disk size  

## Bash-only variables / Not used by Go (LEGACY)

AUTO_DETECT_DATASTORES    = LEGACY (Bash only, auto-detect handled internally in Go)
BACKUP_COROSYNC_CONFIG    = LEGACY (Go always uses COROSYNC_CONFIG_PATH / cluster)
BACKUP_SMALL_PXAR         = LEGACY (in Go, PXAR tuning is more granular via PXAR_*_*)
CLOUD_BACKUP_REQUIRED     = LEGACY (secondary is always optional = warning only, non-blocking)
CLOUD_PARALLEL_UPLOAD_TIMEOUT = LEGACY (in Go, timeouts are RCLONE_TIMEOUT_*)
ENABLE_EMOJI_LOG          = LEGACY (log formatting handled internally in Go)
ENABLE_LOG_MANAGEMENT     = LEGACY (log management in Go via LogPath/retention)
MAX_CLOUD_LOGS            = LEGACY (Bash only; in Go log retention follows MAX_CLOUD_BACKUPS/CloudRetentionDays)
MAX_LOCAL_LOGS            = LEGACY (Bash only; in Go log retention follows MAX_LOCAL_BACKUPS/LocalRetentionDays)
MAX_PXAR_SIZE             = LEGACY (in Go there are PXAR_SCAN_MAX_ROOTS / budget, not the same semantics)
MAX_SECONDARY_LOGS        = LEGACY (Bash only; in Go log retention follows MAX_SECONDARY_BACKUPS/SecondaryRetentionDays)
MIN_BASH_VERSION          = LEGACY (specific only to Bash script)
MULTI_STORAGE_PARALLEL    = LEGACY (in Go there is parallel storage management, not controlled by this variable)
REMOVE_UNAUTHORIZED_FILES = LEGACY (in Go there is no hard delete flag; checks are more conservative)
SECONDARY_BACKUP_REQUIRED = LEGACY (secondary is always optional = warning only, non-blocking)
SKIP_CLOUD_VERIFICATION   = LEGACY (verifications always performed)
STORAGE_WARNING_THRESHOLD_PRIMARY = LEGACY / SEMANTIC CHANGE ⚠️
  **Bash**: `STORAGE_WARNING_THRESHOLD_PRIMARY="90"` (90% used = warning)
  **Go**: `MIN_DISK_SPACE_PRIMARY_GB="1"` (minimum 1GB free required)
  **Note**: Same semantic change as STORAGE_WARNING_THRESHOLD_SECONDARY (from % used to GB free)

YELLOW                    = LEGACY (Bash color codes, not used by Go)
PINK                      = LEGACY (Bash color codes, not used by Go)
RED                       = LEGACY (Bash color codes, not used by Go)
PURPLE                    = LEGACY (Bash color codes, not used by Go)
GRAY                      = LEGACY (Bash color codes, not used by Go)
GREEN                     = LEGACY (Bash color codes, not used by Go)
CYAN                      = LEGACY (Bash color codes, not used by Go)
BLUE                      = LEGACY (Bash color codes, not used by Go)
RESET                     = LEGACY (Bash color codes, not used by Go)
BOLD                      = LEGACY (Bash color codes, not used by Go)  
