package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/types"
	"github.com/tis24dev/proxmox-backup/pkg/utils"
)

var (
	multiValueKeys = map[string]bool{
		"BACKUP_EXCLUDE_PATTERNS": true,
		"CUSTOM_BACKUP_PATHS":     true,
		"BACKUP_BLACKLIST":        true,
		"AGE_RECIPIENT":           true,
	}

	blockValueKeys = map[string]bool{
		"CUSTOM_BACKUP_PATHS": true,
		"BACKUP_BLACKLIST":    true,
	}
)

// Config contiene tutta la configurazione del sistema di backup
type Config struct {
	// General settings
	BackupEnabled            bool
	DebugLevel               types.LogLevel
	UseColor                 bool
	ColorizeStepLogs         bool
	EnableGoBackup           bool
	ProfilingEnabled         bool
	BaseDir                  string
	DryRun                   bool
	DisableNetworkPreflight  bool
	SecurityCheckEnabled     bool
	AbortOnSecurityIssues    bool
	AutoUpdateHashes         bool
	AutoFixPermissions       bool
	ContinueOnSecurityIssues bool
	SuspiciousProcesses      []string
	SafeBracketProcesses     []string
	SafeKernelProcesses      []string
	BackupUser               string
	BackupGroup              string
	SetBackupPermissions     bool

	// Compression settings
	CompressionType    types.CompressionType
	CompressionLevel   int
	CompressionThreads int
	CompressionMode    string

	// Safety settings
	MinDiskPrimaryGB   float64
	MinDiskSecondaryGB float64
	MinDiskCloudGB     float64
	SafetyFactor       float64

	// Optimization settings
	EnableSmartChunking    bool
	EnableDeduplication    bool
	EnablePrefilter        bool
	ChunkSizeMB            int
	ChunkThresholdMB       int
	PrefilterMaxFileSizeMB int

	// Paths
	BackupPath       string
	LogPath          string
	SecondaryLogPath string
	CloudLogPath     string
	LockPath         string
	SecureAccount    string
	ConfigPath       string

	// Storage settings
	SecondaryEnabled      bool
	SecondaryPath         string
	CloudEnabled          bool
	CloudRemote           string
	CloudRemotePath       string
	CloudUploadMode       string
	CloudParallelJobs     int
	CloudParallelVerify   bool
	CloudWriteHealthCheck bool

	// Rclone settings with comprehensible timeout names
	// RcloneTimeoutConnection: timeout for checking if remote is accessible (default: 30s)
	// RcloneTimeoutOperation: timeout for full upload/download operations (default: 300s)
	RcloneTimeoutConnection int
	RcloneTimeoutOperation  int
	RcloneBandwidthLimit    string
	RcloneTransfers         int
	RcloneRetries           int
	RcloneVerifyMethod      string // "primary" or "alternative"
	RcloneFlags             []string

	// Retention settings (applied to both backups and logs)
	LocalRetentionDays     int
	SecondaryRetentionDays int
	CloudRetentionDays     int
	MaxLocalBackups        int
	MaxSecondaryBackups    int
	MaxCloudBackups        int

	// Retention policy selector ("simple" or "gfs")
	RetentionPolicy string

	// GFS (Grandfather-Father-Son) retention settings
	// If ANY of these is > 0, GFS retention is enabled (overrides simple retention)
	RetentionDaily   int // Keep backups from last N days (0 = disabled)
	RetentionWeekly  int // Keep N weekly backups, one per week (0 = disabled)
	RetentionMonthly int // Keep N monthly backups, one per month (0 = disabled)
	RetentionYearly  int // Keep N yearly backups, one per year (0 = keep all yearly)

	// Batch deletion settings (cloud storage)
	CloudBatchSize  int // Number of files to delete per batch (default: 20)
	CloudBatchPause int // Pause in seconds between batches (default: 1)

	// Bundle settings for associated files
	BundleAssociatedFiles bool // Bundle .tar.xz + .sha256 + .metadata into single archive
	EncryptArchive        bool
	AgeRecipients         []string
	AgeRecipientFile      string

	// Telegram Notifications
	TelegramEnabled       bool
	TelegramBotType       string // "personal" or "centralized"
	TelegramBotToken      string // For personal mode
	TelegramChatID        string // For personal mode
	TelegramServerAPIHost string // For centralized mode
	ServerID              string // Server identifier for centralized mode

	// Email Notifications
	EmailEnabled          bool
	EmailDeliveryMethod   string // "relay" or "sendmail"
	EmailFallbackSendmail bool
	EmailRecipient        string // Single recipient, empty = auto-detect
	EmailFrom             string

	// Gotify Notifications
	GotifyEnabled         bool
	GotifyServerURL       string
	GotifyToken           string
	GotifyPrioritySuccess int
	GotifyPriorityWarning int
	GotifyPriorityFailure int

	// Cloud Relay Configuration (hardcoded for compatibility)
	CloudflareWorkerURL   string
	CloudflareWorkerToken string
	CloudflareHMACSecret  string
	WorkerTimeout         int // seconds
	WorkerMaxRetries      int
	WorkerRetryDelay      int // seconds

	// Webhook Notifications
	WebhookEnabled       bool
	WebhookEndpointNames []string // List of endpoint names to configure
	WebhookDefaultFormat string   // Default format for all endpoints
	WebhookTimeout       int      // Timeout in seconds
	WebhookMaxRetries    int      // Max retry attempts
	WebhookRetryDelay    int      // Delay between retries in seconds

	// Metrics
	MetricsEnabled bool
	MetricsPath    string

	// Security features
	CheckNetworkSecurity bool
	CheckFirewall        bool
	CheckOpenPorts       bool
	SuspiciousPorts      []int
	PortWhitelist        []string

	// Collector options
	ExcludePatterns []string

	// PVE-specific collection options
	BackupVMConfigs         bool
	BackupClusterConfig     bool
	BackupPVEFirewall       bool
	BackupVZDumpConfig      bool
	BackupPVEACL            bool
	BackupPVEJobs           bool
	BackupPVESchedules      bool
	BackupPVEReplication    bool
	BackupPVEBackupFiles    bool
	BackupSmallPVEBackups   bool
	MaxPVEBackupSizeBytes   int64
	PVEBackupIncludePattern string
	BackupCephConfig        bool
	CephConfigPath          string

	// PBS-specific collection options
	BackupDatastoreConfigs   bool
	BackupUserConfigs        bool
	BackupRemoteConfigs      bool
	BackupSyncJobs           bool
	BackupVerificationJobs   bool
	BackupTapeConfigs        bool
	BackupPruneSchedules     bool
	BackupPxarFiles          bool
	PxarDatastoreConcurrency int
	PxarIntraConcurrency     int
	PxarScanFanoutLevel      int
	PxarScanMaxRoots         int
	PxarStopOnCap            bool
	PxarEnumWorkers          int
	PxarEnumBudgetMs         int
	PxarFileIncludePatterns  []string
	PxarFileExcludePatterns  []string

	// System collection options
	BackupNetworkConfigs    bool
	BackupAptSources        bool
	BackupCronJobs          bool
	BackupSystemdServices   bool
	BackupSSLCerts          bool
	BackupSysctlConfig      bool
	BackupKernelModules     bool
	BackupFirewallRules     bool
	BackupInstalledPackages bool
	BackupScriptDir         bool
	BackupCriticalFiles     bool
	BackupSSHKeys           bool
	BackupZFSConfig         bool
	BackupRootHome          bool
	BackupScriptRepository  bool
	BackupUserHomes         bool
	BackupConfigFile        bool
	PVEConfigPath           string
	PBSConfigPath           string
	PVEClusterPath          string
	CorosyncConfigPath      string
	VzdumpConfigPath        string
	PBSDatastorePaths       []string

	CustomBackupPaths []string
	BackupBlacklist   []string

	// PBS Authentication (auto-detected, no manual input required)
	PBSRepository  string // Auto-detected from environment or generated
	PBSPassword    string // Auto-detected API token secret
	PBSFingerprint string // Auto-detected from PBS certificate

	// raw configuration map
	raw map[string]string
}

// LoadConfig legge il file di configurazione backup.env
func LoadConfig(configPath string) (*Config, error) {
	if !utils.FileExists(configPath) {
		return nil, fmt.Errorf("configuration file not found: %s", configPath)
	}

	rawValues, err := parseEnvFile(configPath)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ConfigPath: configPath,
		raw:        rawValues,
	}

	// Override with environment variables (env vars take precedence over file)
	cfg.loadEnvOverrides()

	// Parse configuration
	if err := cfg.parse(); err != nil {
		return nil, fmt.Errorf("error parsing configuration: %w", err)
	}

	return cfg, nil
}

// loadEnvOverrides checks for environment variables and overrides config file values
// This allows environment variables to take precedence over file configuration
func (c *Config) loadEnvOverrides() {
	// List of all configuration keys that can be overridden by environment variables
	envKeys := []string{
		"BACKUP_ENABLED", "DRY_RUN", "DEBUG_LEVEL", "USE_COLOR", "COLORIZE_STEP_LOGS",
		"PROFILING_ENABLED",
		"COMPRESSION_TYPE", "COMPRESSION_LEVEL", "COMPRESSION_THREADS", "COMPRESSION_MODE",
		"ENABLE_SMART_CHUNKING", "ENABLE_DEDUPLICATION", "ENABLE_PREFILTER",
		"CHUNK_SIZE_MB", "CHUNK_THRESHOLD_MB", "PREFILTER_MAX_FILE_SIZE_MB",
		"BACKUP_PATH", "LOG_PATH", "LOCK_PATH", "SECURE_ACCOUNT",
		"SECONDARY_ENABLED", "SECONDARY_PATH", "SECONDARY_LOG_PATH",
		"CLOUD_ENABLED", "CLOUD_REMOTE", "CLOUD_REMOTE_PATH", "CLOUD_LOG_PATH",
		"CLOUD_UPLOAD_MODE", "CLOUD_PARALLEL_MAX_JOBS", "CLOUD_PARALLEL_VERIFICATION",
		"CLOUD_WRITE_HEALTHCHECK",
		"RCLONE_TIMEOUT_CONNECTION", "RCLONE_TIMEOUT_OPERATION",
		"RCLONE_BANDWIDTH_LIMIT", "RCLONE_TRANSFERS", "RCLONE_RETRIES", "RCLONE_VERIFY_METHOD",
		"RCLONE_FLAGS",
		"CLOUD_BATCH_SIZE", "CLOUD_BATCH_PAUSE",
		"MAX_LOCAL_BACKUPS", "MAX_SECONDARY_BACKUPS", "MAX_CLOUD_BACKUPS",
		"RETENTION_DAILY", "RETENTION_WEEKLY", "RETENTION_MONTHLY", "RETENTION_YEARLY",
		"BUNDLE_ASSOCIATED_FILES", "ENCRYPT_ARCHIVE", "AGE_RECIPIENT", "AGE_RECIPIENT_FILE",
		"TELEGRAM_ENABLED", "BOT_TELEGRAM_TYPE", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID",
		"EMAIL_ENABLED", "EMAIL_DELIVERY_METHOD", "EMAIL_FALLBACK_SENDMAIL",
		"EMAIL_RECIPIENT", "EMAIL_FROM",
		"WEBHOOK_ENABLED", "WEBHOOK_ENDPOINTS", "WEBHOOK_FORMAT", "WEBHOOK_TIMEOUT",
		"WEBHOOK_MAX_RETRIES", "WEBHOOK_RETRY_DELAY",
		"METRICS_ENABLED", "METRICS_PATH",
		"SECURITY_CHECK_ENABLED", "AUTO_UPDATE_HASHES", "AUTO_FIX_PERMISSIONS",
		"CONTINUE_ON_SECURITY_ISSUES", "CHECK_NETWORK_SECURITY", "CHECK_FIREWALL",
		"CHECK_OPEN_PORTS", "SUSPICIOUS_PORTS", "PORT_WHITELIST",
		"SUSPICIOUS_PROCESSES", "SAFE_BRACKET_PROCESSES", "SAFE_KERNEL_PROCESSES",
		"MIN_DISK_SPACE_PRIMARY_GB", "MIN_DISK_SPACE_SECONDARY_GB", "MIN_DISK_SPACE_CLOUD_GB",
		"DISABLE_NETWORK_PREFLIGHT", "BACKUP_EXCLUDE_PATTERNS",
		"SKIP_PERMISSION_CHECK", "BACKUP_CONFIG_FILE",
		"BACKUP_USER", "BACKUP_GROUP", "SET_BACKUP_PERMISSIONS",
	}

	for _, key := range envKeys {
		if envValue := os.Getenv(key); envValue != "" {
			c.raw[key] = envValue
		}
	}
}

// parse interpreta i valori raw della configurazione
// Supporta sia il formato legacy che quello nuovo del backup.env
func (c *Config) parse() error {
	// General settings
	c.BackupEnabled = c.getBool("BACKUP_ENABLED", true)
	c.DryRun = c.getBool("DRY_RUN", false)
	c.ProfilingEnabled = c.getBool("PROFILING_ENABLED", true)

	// DEBUG_LEVEL: supporta sia numerico che string ("standard", "advanced", "extreme")
	c.DebugLevel = c.getLogLevel("DEBUG_LEVEL", types.LogLevelInfo)

	// USE_COLOR vs DISABLE_COLORS (invertito)
	if disableColors, ok := c.raw["DISABLE_COLORS"]; ok {
		c.UseColor = !utils.ParseBool(disableColors)
	} else {
		c.UseColor = c.getBool("USE_COLOR", true)
	}
	c.ColorizeStepLogs = c.getBool("COLORIZE_STEP_LOGS", true) && c.UseColor

	// Compression
	c.CompressionType = c.getCompressionType("COMPRESSION_TYPE", types.CompressionXZ)
	c.CompressionLevel = c.getInt("COMPRESSION_LEVEL", 6)
	c.CompressionThreads = c.getInt("COMPRESSION_THREADS", 0) // 0 = auto
	c.CompressionMode = strings.ToLower(c.getString("COMPRESSION_MODE", "standard"))
	if c.CompressionMode == "" {
		c.CompressionMode = "standard"
	}
	c.CompressionLevel = adjustLevelForMode(c.CompressionType, c.CompressionMode, c.CompressionLevel)

	// Optimizations
	c.EnableSmartChunking = c.getBool("ENABLE_SMART_CHUNKING", false)
	c.EnableDeduplication = c.getBool("ENABLE_DEDUPLICATION", false)
	c.EnablePrefilter = c.getBool("ENABLE_PREFILTER", false)
	c.ChunkSizeMB = c.getInt("CHUNK_SIZE_MB", 10)
	if c.ChunkSizeMB <= 0 {
		c.ChunkSizeMB = 10
	}
	c.ChunkThresholdMB = c.getInt("CHUNK_THRESHOLD_MB", 50)
	if c.ChunkThresholdMB <= 0 {
		c.ChunkThresholdMB = 50
	}
	c.PrefilterMaxFileSizeMB = c.getInt("PREFILTER_MAX_FILE_SIZE_MB", 8)
	if c.PrefilterMaxFileSizeMB <= 0 {
		c.PrefilterMaxFileSizeMB = 8
	}

	c.MinDiskPrimaryGB = sanitizeMinDisk(c.getFloat("MIN_DISK_SPACE_PRIMARY_GB", 10.0))
	c.MinDiskSecondaryGB = sanitizeMinDisk(c.getFloat("MIN_DISK_SPACE_SECONDARY_GB", c.MinDiskPrimaryGB))
	c.MinDiskCloudGB = sanitizeMinDisk(c.getFloat("MIN_DISK_SPACE_CLOUD_GB", c.MinDiskPrimaryGB))

	// Feature flags
	c.EnableGoBackup = c.getBoolWithFallback([]string{"ENABLE_GO_BACKUP", "ENABLE_GO_PIPELINE"}, true)
	// Preflight controls
	c.DisableNetworkPreflight = c.getBool("DISABLE_NETWORK_PREFLIGHT", false)

	// Base directory (compatibile con lo script Bash: se non specificato, usa env o default)
	envBaseDir := os.Getenv("BASE_DIR")
	c.BaseDir = c.getString("BASE_DIR", envBaseDir)
	if c.BaseDir == "" {
		c.BaseDir = "/opt/proxmox-backup"
	}
	_ = os.Setenv("BASE_DIR", c.BaseDir)

	// Security controls
	c.SecurityCheckEnabled = c.getBoolWithFallback([]string{"SECURITY_CHECK_ENABLED", "FULL_SECURITY_CHECK"}, true)
	c.AutoUpdateHashes = c.getBool("AUTO_UPDATE_HASHES", true)
	c.AutoFixPermissions = c.getBool("AUTO_FIX_PERMISSIONS", false)
	if val, ok := c.raw["CONTINUE_ON_SECURITY_ISSUES"]; ok {
		c.ContinueOnSecurityIssues = utils.ParseBool(val)
		c.AbortOnSecurityIssues = !c.ContinueOnSecurityIssues
	} else if val, ok := c.raw["ABORT_ON_SECURITY_ISSUES"]; ok {
		c.AbortOnSecurityIssues = utils.ParseBool(val)
		c.ContinueOnSecurityIssues = !c.AbortOnSecurityIssues
	} else {
		c.ContinueOnSecurityIssues = false
		c.AbortOnSecurityIssues = true
	}
	c.CheckNetworkSecurity = c.getBool("CHECK_NETWORK_SECURITY", false)
	c.CheckFirewall = c.getBool("CHECK_FIREWALL", false)
	c.CheckOpenPorts = c.getBool("CHECK_OPEN_PORTS", false)
	c.SuspiciousPorts = c.getIntList("SUSPICIOUS_PORTS", []int{6666, 6665, 1337, 31337, 4444, 5555, 4242, 6324, 8888, 2222, 3389, 5900})
	c.PortWhitelist = c.getStringSlice("PORT_WHITELIST", nil)
	defaultSuspicious := []string{
		"ncat", "cryptominer", "xmrig", "kdevtmpfsi", "kinsing", "minerd", "mr.sh",
	}
	userSuspicious := c.getStringSlice("SUSPICIOUS_PROCESSES", nil)
	c.SuspiciousProcesses = mergeStringSlices(defaultSuspicious, userSuspicious)

	defaultSafeBracket := []string{
		"sshd:", "systemd", "cron", "rsyslogd", "dbus-daemon",
		"zvol_tq*", "arc_*", "dbu_*", "dbuf_*", "l2arc_feed", "lockd", "nfsd*", "nfsv4 callback*",
	}
	userSafeBracket := c.getStringSlice("SAFE_BRACKET_PROCESSES", nil)
	c.SafeBracketProcesses = mergeStringSlices(defaultSafeBracket, userSafeBracket)

	defaultSafeKernel := []string{
		"ksgxd",
		"hwrng",
		"usb-storage",
		"vdev_autotrim",
		"card1-crtc0",
		"card1-crtc1",
		"card1-crtc2",
		"kvm-pit*",
		"psimon",
		"regex:^kvm-pit/[0-9]+$",
		"regex:^worker/.+-drbd_as_pm-.*",
	}
	userSafeKernel := c.getStringSlice("SAFE_KERNEL_PROCESSES", nil)
	c.SafeKernelProcesses = mergeStringSlices(defaultSafeKernel, userSafeKernel)

	c.BackupUser = strings.TrimSpace(c.getString("BACKUP_USER", ""))
	c.BackupGroup = strings.TrimSpace(c.getString("BACKUP_GROUP", ""))
	c.SetBackupPermissions = c.getBool("SET_BACKUP_PERMISSIONS", false)

	c.EncryptArchive = c.getBool("ENCRYPT_ARCHIVE", false)
	c.AgeRecipientFile = strings.TrimSpace(c.getString("AGE_RECIPIENT_FILE", ""))
	c.AgeRecipients = c.getStringSlice("AGE_RECIPIENT", nil)
	if len(c.AgeRecipients) == 0 {
		c.AgeRecipients = c.getStringSlice("AGE_RECIPIENTS", nil)
	}

	// Paths: supporta LOCAL_BACKUP_PATH o BACKUP_PATH
	c.BackupPath = c.getStringWithFallback([]string{"LOCAL_BACKUP_PATH", "BACKUP_PATH"}, filepath.Join(c.BaseDir, "backup"))
	c.LogPath = c.getStringWithFallback([]string{"LOCAL_LOG_PATH", "LOG_PATH"}, filepath.Join(c.BaseDir, "log"))
	c.SecondaryLogPath = c.getString("SECONDARY_LOG_PATH", "")
	c.CloudLogPath = c.getString("CLOUD_LOG_PATH", "")
	c.LockPath = c.getString("LOCK_PATH", filepath.Join(c.BaseDir, "lock"))
	c.SecureAccount = c.getString("SECURE_ACCOUNT", filepath.Join(c.BaseDir, "secure_account"))

	// Storage: supporta ENABLE_SECONDARY_BACKUP o SECONDARY_ENABLED
	c.SecondaryEnabled = c.getBoolWithFallback([]string{"ENABLE_SECONDARY_BACKUP", "SECONDARY_ENABLED"}, false)
	c.SecondaryPath = c.getStringWithFallback([]string{"SECONDARY_BACKUP_PATH", "SECONDARY_PATH"}, "")

	c.CloudEnabled = c.getBoolWithFallback([]string{"ENABLE_CLOUD_BACKUP", "CLOUD_ENABLED"}, false)
	c.CloudRemote = c.getStringWithFallback([]string{"RCLONE_REMOTE", "CLOUD_REMOTE"}, "")
	c.CloudRemotePath = strings.Trim(strings.TrimSpace(c.getString("CLOUD_REMOTE_PATH", "")), "/")
	mode := strings.ToLower(strings.TrimSpace(c.getString("CLOUD_UPLOAD_MODE", "")))
	if mode != "parallel" {
		mode = "sequential"
	}
	c.CloudUploadMode = mode
	c.CloudParallelJobs = c.getInt("CLOUD_PARALLEL_MAX_JOBS", 2)
	if c.CloudParallelJobs <= 0 {
		c.CloudParallelJobs = 1
	}
	c.CloudParallelVerify = c.getBool("CLOUD_PARALLEL_VERIFICATION", false)
	c.CloudWriteHealthCheck = c.getBool("CLOUD_WRITE_HEALTHCHECK", false)

	// Rclone settings with comprehensible timeout names
	c.RcloneTimeoutConnection = c.getIntWithFallback([]string{"RCLONE_TIMEOUT_CONNECTION", "CLOUD_CONNECTIVITY_TIMEOUT"}, 30)
	c.RcloneTimeoutOperation = c.getInt("RCLONE_TIMEOUT_OPERATION", 300)
	c.RcloneBandwidthLimit = c.getString("RCLONE_BANDWIDTH_LIMIT", "")
	c.RcloneTransfers = c.getInt("RCLONE_TRANSFERS", 4)
	c.RcloneRetries = c.getInt("RCLONE_RETRIES", 3)
	c.RcloneVerifyMethod = strings.ToLower(c.getString("RCLONE_VERIFY_METHOD", "primary"))
	if c.RcloneVerifyMethod == "" {
		c.RcloneVerifyMethod = "primary"
	}
	if rawFlags := strings.TrimSpace(c.getString("RCLONE_FLAGS", "")); rawFlags != "" {
		c.RcloneFlags = strings.Fields(rawFlags)
	}

	// Retention: supporta MAX_LOCAL_BACKUPS o LOCAL_RETENTION_DAYS
	// Applies to both backups and log files
	c.LocalRetentionDays = c.getIntWithFallback([]string{"MAX_LOCAL_BACKUPS", "LOCAL_RETENTION_DAYS"}, 7)
	c.SecondaryRetentionDays = c.getIntWithFallback([]string{"MAX_SECONDARY_BACKUPS", "SECONDARY_RETENTION_DAYS"}, 14)
	c.CloudRetentionDays = c.getIntWithFallback([]string{"MAX_CLOUD_BACKUPS", "CLOUD_RETENTION_DAYS"}, 30)
	c.MaxLocalBackups = c.LocalRetentionDays
	c.MaxSecondaryBackups = c.SecondaryRetentionDays
	c.MaxCloudBackups = c.CloudRetentionDays

	// GFS (Grandfather-Father-Son) retention policy
	// Tier limits; the active policy is selected via RETENTION_POLICY
	c.RetentionDaily = c.getInt("RETENTION_DAILY", 0)
	c.RetentionWeekly = c.getInt("RETENTION_WEEKLY", 0)
	c.RetentionMonthly = c.getInt("RETENTION_MONTHLY", 0)
	c.RetentionYearly = c.getInt("RETENTION_YEARLY", 0)

	// Retention policy selector
	// RETENTION_POLICY=simple (default) uses MAX_*_BACKUPS
	// RETENTION_POLICY=gfs uses RETENTION_* tiers
	policy := strings.ToLower(strings.TrimSpace(c.getString("RETENTION_POLICY", "simple")))
	switch policy {
	case "gfs":
		c.RetentionPolicy = "gfs"
	default:
		c.RetentionPolicy = "simple"
	}

	// Batch deletion settings for cloud storage (avoid API rate limits)
	c.CloudBatchSize = c.getInt("CLOUD_BATCH_SIZE", 20)
	if c.CloudBatchSize <= 0 {
		c.CloudBatchSize = 20
	}
	c.CloudBatchPause = c.getInt("CLOUD_BATCH_PAUSE", 1)
	if c.CloudBatchPause < 0 {
		c.CloudBatchPause = 1
	}

	// Bundle associated files into single archive
	c.BundleAssociatedFiles = c.getBool("BUNDLE_ASSOCIATED_FILES", true)

	c.SafetyFactor = 1.5

	// Telegram Notifications
	c.TelegramEnabled = c.getBool("TELEGRAM_ENABLED", false)
	c.TelegramBotType = c.getString("BOT_TELEGRAM_TYPE", "centralized")
	c.TelegramBotToken = c.getString("TELEGRAM_BOT_TOKEN", "")
	c.TelegramChatID = c.getString("TELEGRAM_CHAT_ID", "")
	c.TelegramServerAPIHost = "https://bot.tis24.it:1443"
	c.ServerID = ""

	// Email Notifications
	c.EmailEnabled = c.getBool("EMAIL_ENABLED", true)
	c.EmailDeliveryMethod = c.getString("EMAIL_DELIVERY_METHOD", "relay")
	c.EmailFallbackSendmail = c.getBool("EMAIL_FALLBACK_SENDMAIL", true)
	c.EmailRecipient = c.getString("EMAIL_RECIPIENT", "")
	c.EmailFrom = c.getString("EMAIL_FROM", "no-reply@proxmox.tis24.it")

	// Gotify Notifications
	c.GotifyEnabled = c.getBool("GOTIFY_ENABLED", false)
	c.GotifyServerURL = strings.TrimSpace(c.getString("GOTIFY_SERVER_URL", ""))
	c.GotifyToken = strings.TrimSpace(c.getString("GOTIFY_TOKEN", ""))
	c.GotifyPrioritySuccess = c.ensurePositiveInt("GOTIFY_PRIORITY_SUCCESS", 2)
	c.GotifyPriorityWarning = c.ensurePositiveInt("GOTIFY_PRIORITY_WARNING", 5)
	c.GotifyPriorityFailure = c.ensurePositiveInt("GOTIFY_PRIORITY_FAILURE", 8)

	// Cloud Relay Configuration (hardcoded for Bash compatibility)
	c.CloudflareWorkerURL = "https://relay-tis24.weathered-hill-5216.workers.dev/send"
	c.CloudflareWorkerToken = "v1_public_20251024"
	c.CloudflareHMACSecret = "4cc8946c15338082674d7213aee19069571e1afe60ad21b44be4d68260486fb2" // From wrangler.jsonc
	c.WorkerTimeout = 30
	c.WorkerMaxRetries = 2
	c.WorkerRetryDelay = 2

	// Webhook Notifications
	c.WebhookEnabled = c.getBool("WEBHOOK_ENABLED", false)
	c.WebhookDefaultFormat = c.getString("WEBHOOK_FORMAT", "generic")
	c.WebhookTimeout = c.getInt("WEBHOOK_TIMEOUT", 30)
	c.WebhookMaxRetries = c.getInt("WEBHOOK_MAX_RETRIES", 3)
	c.WebhookRetryDelay = c.getInt("WEBHOOK_RETRY_DELAY", 2)

	// Parse webhook endpoint names (comma-separated)
	endpointNames := c.getString("WEBHOOK_ENDPOINTS", "")
	if endpointNames != "" {
		c.WebhookEndpointNames = strings.Split(endpointNames, ",")
		// Trim whitespace from each name
		for i, name := range c.WebhookEndpointNames {
			c.WebhookEndpointNames[i] = strings.TrimSpace(name)
		}
	} else {
		c.WebhookEndpointNames = []string{}
	}

	// Metrics: supporta PROMETHEUS_ENABLED o METRICS_ENABLED
	c.MetricsEnabled = c.getBoolWithFallback([]string{"PROMETHEUS_ENABLED", "METRICS_ENABLED"}, false)
	rawMetricsPath := strings.TrimSpace(c.getStringWithFallback([]string{"METRICS_PATH", "PROMETHEUS_TEXTFILE_DIR"}, ""))
	if rawMetricsPath == "" {
		// Default to node_exporter textfile directory when not overridden
		c.MetricsPath = "/var/lib/prometheus/node-exporter"
	} else {
		c.MetricsPath = rawMetricsPath
	}

	if patterns := c.getStringSlice("BACKUP_EXCLUDE_PATTERNS", nil); patterns != nil {
		c.ExcludePatterns = patterns
	} else {
		c.ExcludePatterns = []string{}
	}

	// PVE-specific collection options
	c.BackupVMConfigs = c.getBool("BACKUP_VM_CONFIGS", true)
	c.BackupClusterConfig = c.getBool("BACKUP_CLUSTER_CONFIG", true)
	c.BackupPVEFirewall = c.getBool("BACKUP_PVE_FIREWALL", true)
	c.BackupVZDumpConfig = c.getBool("BACKUP_VZDUMP_CONFIG", true)
	c.BackupPVEACL = c.getBool("BACKUP_PVE_ACL", true)
	c.BackupPVEJobs = c.getBool("BACKUP_PVE_JOBS", true)
	c.BackupPVESchedules = c.getBool("BACKUP_PVE_SCHEDULES", true)
	c.BackupPVEReplication = c.getBool("BACKUP_PVE_REPLICATION", true)
	c.BackupPVEBackupFiles = c.getBool("BACKUP_PVE_BACKUP_FILES", true)
	c.BackupSmallPVEBackups = c.getBool("BACKUP_SMALL_PVE_BACKUPS", false)
	if rawSize := strings.TrimSpace(c.getString("MAX_PVE_BACKUP_SIZE", "")); rawSize != "" {
		sizeBytes, err := parseSizeToBytes(rawSize)
		if err != nil {
			return fmt.Errorf("invalid MAX_PVE_BACKUP_SIZE: %w", err)
		}
		c.MaxPVEBackupSizeBytes = sizeBytes
	}
	c.PVEBackupIncludePattern = strings.TrimSpace(c.getString("PVE_BACKUP_INCLUDE_PATTERN", ""))
	c.BackupCephConfig = c.getBool("BACKUP_CEPH_CONFIG", true)
	c.CephConfigPath = c.getString("CEPH_CONFIG_PATH", "/etc/ceph")
	c.PVEConfigPath = c.getString("PVE_CONFIG_PATH", "/etc/pve")
	c.PVEClusterPath = c.getString("PVE_CLUSTER_PATH", "/var/lib/pve-cluster")
	defaultCorosync := filepath.Join(c.PVEConfigPath, "corosync.conf")
	c.CorosyncConfigPath = c.getString("COROSYNC_CONFIG_PATH", defaultCorosync)
	c.VzdumpConfigPath = c.getString("VZDUMP_CONFIG_PATH", "/etc/vzdump.conf")
	c.PBSConfigPath = c.getString("PBS_CONFIG_PATH", "/etc/proxmox-backup")

	// PBS-specific collection options
	c.BackupDatastoreConfigs = c.getBool("BACKUP_DATASTORE_CONFIGS", true)
	c.BackupUserConfigs = c.getBool("BACKUP_USER_CONFIGS", true)
	c.BackupRemoteConfigs = c.getBoolWithFallback([]string{"BACKUP_REMOTE_CONFIGS", "BACKUP_REMOTE_CFG"}, true)
	c.BackupSyncJobs = c.getBool("BACKUP_SYNC_JOBS", true)
	c.BackupVerificationJobs = c.getBool("BACKUP_VERIFICATION_JOBS", true)
	c.BackupTapeConfigs = c.getBool("BACKUP_TAPE_CONFIGS", true)
	c.BackupPruneSchedules = c.getBool("BACKUP_PRUNE_SCHEDULES", true)
	// PXAR scan enable: prefer new key PXAR_SCAN_ENABLE, fallback to legacy BACKUP_PXAR_FILES
	c.BackupPxarFiles = c.getBoolWithFallback([]string{"PXAR_SCAN_ENABLE", "BACKUP_PXAR_FILES"}, true)
	c.PxarDatastoreConcurrency = c.getInt("PXAR_SCAN_DS_CONCURRENCY", 3)
	c.PxarIntraConcurrency = c.getInt("PXAR_SCAN_INTRA_CONCURRENCY", 4)
	c.PxarScanFanoutLevel = c.getInt("PXAR_SCAN_FANOUT_LEVEL", 2)
	c.PxarScanMaxRoots = c.getInt("PXAR_SCAN_MAX_ROOTS", 2048)
	c.PxarStopOnCap = c.getBool("PXAR_STOP_ON_CAP", false)
	c.PxarEnumWorkers = c.getInt("PXAR_ENUM_READDIR_WORKERS", 4)
	c.PxarEnumBudgetMs = c.getInt("PXAR_ENUM_BUDGET_MS", 0)
	c.PxarFileIncludePatterns = normalizeList(c.getStringSliceWithFallback([]string{"PXAR_FILE_INCLUDE_PATTERN", "PXAR_INCLUDE_PATTERN"}, nil))
	c.PxarFileExcludePatterns = normalizeList(c.getStringSlice("PXAR_FILE_EXCLUDE_PATTERN", nil))

	// System collection options
	c.BackupNetworkConfigs = c.getBoolWithFallback([]string{"BACKUP_NETWORK_CONFIGS", "BACKUP_NETWORK_CONFIG"}, true)
	c.BackupAptSources = c.getBool("BACKUP_APT_SOURCES", true)
	c.BackupCronJobs = c.getBoolWithFallback([]string{"BACKUP_CRON_JOBS", "BACKUP_CRONTABS"}, true)
	c.BackupSystemdServices = c.getBool("BACKUP_SYSTEMD_SERVICES", true)
	c.BackupSSLCerts = c.getBool("BACKUP_SSL_CERTS", true)
	c.BackupSysctlConfig = c.getBool("BACKUP_SYSCTL_CONFIG", true)
	c.BackupKernelModules = c.getBool("BACKUP_KERNEL_MODULES", true)
	c.BackupFirewallRules = c.getBool("BACKUP_FIREWALL_RULES", true)
	c.BackupInstalledPackages = c.getBool("BACKUP_INSTALLED_PACKAGES", true)
	c.BackupScriptDir = c.getBool("BACKUP_SCRIPT_DIR", true)
	c.BackupCriticalFiles = c.getBool("BACKUP_CRITICAL_FILES", true)
	c.BackupSSHKeys = c.getBool("BACKUP_SSH_KEYS", true)
	c.BackupZFSConfig = c.getBool("BACKUP_ZFS_CONFIG", true)
	c.BackupRootHome = c.getBool("BACKUP_ROOT_HOME", true)
	c.BackupScriptRepository = c.getBool("BACKUP_SCRIPT_REPOSITORY", true)
	c.BackupUserHomes = c.getBool("BACKUP_USER_HOMES", true)
	c.BackupConfigFile = c.getBool("BACKUP_CONFIG_FILE", true)
	c.PBSDatastorePaths = normalizeList(c.getStringSlice("PBS_DATASTORE_PATH", nil))

	c.CustomBackupPaths = normalizeList(c.getStringSlice("CUSTOM_BACKUP_PATHS", nil))
	c.BackupBlacklist = normalizeList(c.getStringSlice("BACKUP_BLACKLIST", nil))

	// Auto-detect PBS authentication (zero user input required)
	c.autoDetectPBSAuth()

	return nil
}

// Helper methods per ottenere valori tipizzati

func (c *Config) getString(key, defaultValue string) string {
	if val, ok := c.raw[key]; ok {
		return expandEnvVars(val)
	}
	return defaultValue
}

func (c *Config) getBool(key string, defaultValue bool) bool {
	if val, ok := c.raw[key]; ok {
		return utils.ParseBool(val)
	}
	return defaultValue
}

func (c *Config) getInt(key string, defaultValue int) int {
	if val, ok := c.raw[key]; ok {
		if intVal, err := strconv.Atoi(val); err == nil {
			return intVal
		}
	}
	return defaultValue
}

func (c *Config) ensurePositiveInt(key string, defaultValue int) int {
	value := c.getInt(key, defaultValue)
	if value <= 0 {
		return defaultValue
	}
	return value
}

func (c *Config) getIntList(key string, defaultValue []int) []int {
	val, ok := c.raw[key]
	if !ok || strings.TrimSpace(val) == "" {
		return append([]int(nil), defaultValue...)
	}

	parts := strings.FieldsFunc(val, func(r rune) bool {
		switch r {
		case ',', ';', '|', '\n', '\t', ' ':
			return true
		default:
			return false
		}
	})

	var result []int
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		part = strings.Trim(part, `"'`)
		if num, err := strconv.Atoi(part); err == nil {
			result = append(result, num)
		}
	}

	if len(result) == 0 && len(defaultValue) > 0 {
		return append([]int(nil), defaultValue...)
	}
	return result
}

func (c *Config) getLogLevel(key string, defaultValue types.LogLevel) types.LogLevel {
	if val, ok := c.raw[key]; ok {
		// Try numeric first
		if intVal, err := strconv.Atoi(val); err == nil {
			return types.LogLevel(intVal)
		}
		// Try string values: "standard", "advanced", "extreme"
		switch val {
		case "standard":
			return types.LogLevelInfo
		case "advanced":
			return types.LogLevelDebug
		case "extreme":
			return types.LogLevelDebug
		}
	}
	return defaultValue
}

func (c *Config) getCompressionType(key string, defaultValue types.CompressionType) types.CompressionType {
	if val, ok := c.raw[key]; ok {
		return types.CompressionType(val)
	}
	return defaultValue
}

func (c *Config) getStringSlice(key string, defaultValue []string) []string {
	val, ok := c.raw[key]
	if !ok {
		return defaultValue
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return []string{}
	}

	parts := strings.FieldsFunc(val, func(r rune) bool {
		switch r {
		case ',', ';', '|', '\n':
			return true
		default:
			return false
		}
	})

	var result []string
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			trimmed = strings.Trim(trimmed, `"'`)
			result = append(result, trimmed)
		}
	}

	if len(result) == 0 {
		return []string{}
	}
	return result
}

func mergeStringSlices(base, extra []string) []string {
	if len(base) == 0 && len(extra) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))

	for _, v := range base {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	for _, v := range extra {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	return out
}

// Helper methods with fallback support (try multiple keys)

// expandEnvVars expands environment variables and special variables like ${BASE_DIR}
func expandEnvVars(s string) string {
	// Expand ${VAR} and $VAR style variables
	result := os.Expand(s, func(key string) string {
		// Special handling for BASE_DIR
		if key == "BASE_DIR" {
			// Check if BASE_DIR is set in environment, otherwise use default
			if val := os.Getenv("BASE_DIR"); val != "" {
				return val
			}
			return "/opt/proxmox-backup"
		}
		return os.Getenv(key)
	})
	return result
}

func (c *Config) getStringWithFallback(keys []string, defaultValue string) string {
	for _, key := range keys {
		if val, ok := c.raw[key]; ok && val != "" {
			return expandEnvVars(val)
		}
	}
	return defaultValue
}

func (c *Config) getBoolWithFallback(keys []string, defaultValue bool) bool {
	for _, key := range keys {
		if val, ok := c.raw[key]; ok {
			return utils.ParseBool(val)
		}
	}
	return defaultValue
}

func (c *Config) getIntWithFallback(keys []string, defaultValue int) int {
	for _, key := range keys {
		if val, ok := c.raw[key]; ok {
			if intVal, err := strconv.Atoi(val); err == nil {
				return intVal
			}
		}
	}
	return defaultValue
}

func (c *Config) getStringSliceWithFallback(keys []string, defaultValue []string) []string {
	for _, key := range keys {
		if val, ok := c.raw[key]; ok {
			val = strings.TrimSpace(val)
			if val != "" {
				parts := strings.FieldsFunc(val, func(r rune) bool {
					switch r {
					case ',', ';', ':', '|', '\n':
						return true
					default:
						return false
					}
				})

				var result []string
				for _, part := range parts {
					if trimmed := strings.TrimSpace(part); trimmed != "" {
						trimmed = strings.Trim(trimmed, `"'`)
						result = append(result, trimmed)
					}
				}

				if len(result) > 0 {
					return result
				}
			}
		}
	}
	return defaultValue
}

func parseSizeToBytes(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}

	multiplier := float64(1)
	last := value[len(value)-1]
	switch last {
	case 'k', 'K':
		multiplier = 1024
		value = strings.TrimSpace(value[:len(value)-1])
	case 'm', 'M':
		multiplier = 1024 * 1024
		value = strings.TrimSpace(value[:len(value)-1])
	case 'g', 'G':
		multiplier = 1024 * 1024 * 1024
		value = strings.TrimSpace(value[:len(value)-1])
	case 't', 'T':
		multiplier = 1024 * 1024 * 1024 * 1024
		value = strings.TrimSpace(value[:len(value)-1])
	}

	if value == "" {
		return 0, fmt.Errorf("missing numeric value")
	}

	num, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if num < 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	bytes := int64(num * multiplier)
	if bytes < 0 {
		bytes = 0
	}
	return bytes, nil
}

func (c *Config) getFloat(key string, defaultValue float64) float64 {
	if val, ok := c.raw[key]; ok {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultValue
}

func adjustLevelForMode(comp types.CompressionType, mode string, current int) int {
	switch mode {
	case "fast":
		return 1
	case "maximum":
		switch comp {
		case types.CompressionZstd:
			return 19
		case types.CompressionXZ, types.CompressionGzip, types.CompressionPigz,
			types.CompressionBzip2, types.CompressionLZMA:
			return 9
		default:
			return current
		}
	case "ultra":
		switch comp {
		case types.CompressionZstd:
			return 22
		case types.CompressionXZ, types.CompressionGzip, types.CompressionPigz,
			types.CompressionBzip2, types.CompressionLZMA:
			return 9
		default:
			return current
		}
	default:
		return current
	}
}

func sanitizeMinDisk(value float64) float64 {
	if value <= 0 {
		return 10.0
	}
	return value
}

// Get restituisce un valore raw dalla configurazione
func (c *Config) Get(key string) (string, bool) {
	val, ok := c.raw[key]
	return val, ok
}

// Set imposta un valore nella configurazione
func (c *Config) Set(key, value string) {
	c.raw[key] = value
}

func normalizeList(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	clean := make([]string, 0, len(values))
	for _, v := range values {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			clean = append(clean, trimmed)
		}
	}
	if len(clean) == 0 {
		return []string{}
	}
	return clean
}

// autoDetectPBSAuth automatically detects PBS authentication credentials
// from environment variables, config files, or system defaults.
// This function ensures ZERO manual user input is required.
func (c *Config) autoDetectPBSAuth() {
	// Priority 1: Check environment variables (set by systemd or shell)
	if repo := os.Getenv("PBS_REPOSITORY"); repo != "" {
		c.PBSRepository = repo
	}
	if pass := os.Getenv("PBS_PASSWORD"); pass != "" {
		c.PBSPassword = pass
	}
	if fp := os.Getenv("PBS_FINGERPRINT"); fp != "" {
		c.PBSFingerprint = fp
	}

	// Priority 2: Check config file for PBS credentials
	if c.PBSRepository == "" {
		c.PBSRepository = c.getString("PBS_REPOSITORY", "")
	}
	if c.PBSPassword == "" {
		c.PBSPassword = c.getString("PBS_PASSWORD", "")
	}
	if c.PBSFingerprint == "" {
		c.PBSFingerprint = c.getString("PBS_FINGERPRINT", "")
	}

	// Priority 3: Auto-detect from PBS system files
	if c.PBSFingerprint == "" {
		c.PBSFingerprint = autoDetectPBSFingerprint()
	}

	// Priority 4: Try to read API token from secure_account directory
	if c.PBSPassword == "" && c.PBSRepository == "" {
		token, secret := autoDetectPBSToken(c.SecureAccount)
		if token != "" && secret != "" {
			// Use the detected token
			c.PBSRepository = fmt.Sprintf("%s@localhost", token)
			c.PBSPassword = secret
		}
	}

	// Note: If all detection fails, PBS client commands will fallback gracefully
	// (see safeCmdOutputWithPBSAuth in collector.go)
}

// autoDetectPBSFingerprint tries to extract the SSL fingerprint from PBS certificate
func autoDetectPBSFingerprint() string {
	// Try to get fingerprint from PBS proxy certificate
	certPaths := []string{
		"/etc/proxmox-backup/proxy.pem",
		"/etc/pve/pve-root-ca.pem",
	}

	for _, certPath := range certPaths {
		if fp := extractFingerprintFromCert(certPath); fp != "" {
			return fp
		}
	}

	return ""
}

// extractFingerprintFromCert extracts SHA256 fingerprint from a certificate file
func extractFingerprintFromCert(certPath string) string {
	_ = certPath
	// This would require crypto/x509 parsing - for now return empty
	// The fingerprint is optional and commands will work without it on localhost
	return ""
}

// BuildWebhookConfig constructs a complete webhook configuration with all endpoints
func (c *Config) BuildWebhookConfig() *WebhookConfig {
	endpoints := []WebhookEndpoint{}

	// Parse each configured endpoint
	for _, name := range c.WebhookEndpointNames {
		if name == "" {
			continue
		}

		// Build environment variable prefix for this endpoint
		prefix := fmt.Sprintf("WEBHOOK_%s_", strings.ToUpper(strings.ReplaceAll(name, "-", "_")))

		// Parse endpoint configuration
		url := c.getString(prefix+"URL", "")
		if url == "" {
			continue // Skip endpoints without URL
		}

		format := c.getString(prefix+"FORMAT", c.WebhookDefaultFormat)
		method := c.getString(prefix+"METHOD", "POST")
		authType := c.getString(prefix+"AUTH_TYPE", "none")

		// Parse authentication
		auth := WebhookAuth{
			Type:   authType,
			Token:  c.getString(prefix+"AUTH_TOKEN", ""),
			User:   c.getString(prefix+"AUTH_USER", ""),
			Pass:   c.getString(prefix+"AUTH_PASS", ""),
			Secret: c.getString(prefix+"AUTH_SECRET", ""),
		}

		// Parse custom headers
		headers := make(map[string]string)
		headersStr := c.getString(prefix+"HEADERS", "")
		if headersStr != "" {
			// Format: "Key1:Value1,Key2:Value2"
			pairs := strings.Split(headersStr, ",")
			for _, pair := range pairs {
				parts := strings.SplitN(strings.TrimSpace(pair), ":", 2)
				if len(parts) == 2 {
					headers[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
				}
			}
		}

		endpoints = append(endpoints, WebhookEndpoint{
			Name:    name,
			URL:     url,
			Format:  format,
			Method:  method,
			Headers: headers,
			Auth:    auth,
		})
	}

	return &WebhookConfig{
		Enabled:       c.WebhookEnabled,
		Endpoints:     endpoints,
		DefaultFormat: c.WebhookDefaultFormat,
		Timeout:       c.WebhookTimeout,
		MaxRetries:    c.WebhookMaxRetries,
		RetryDelay:    c.WebhookRetryDelay,
	}
}

func parseEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open config file: %w", err)
	}
	defer file.Close()

	raw := make(map[string]string)
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}

		key, value, ok := utils.SplitKeyValue(line)
		if !ok {
			continue
		}

		if blockValueKeys[key] && trimmed == fmt.Sprintf("%s=\"", key) {
			var blockLines []string
			terminated := false
			for scanner.Scan() {
				next := strings.TrimRight(scanner.Text(), "\r")
				if strings.TrimSpace(next) == "\"" {
					terminated = true
					break
				}
				blockLines = append(blockLines, next)
			}
			if !terminated {
				return nil, fmt.Errorf("unterminated multi-line value for %s", key)
			}
			raw[key] = strings.Join(blockLines, "\n")
			continue
		}

		if multiValueKeys[key] {
			if existing, ok := raw[key]; ok && existing != "" {
				raw[key] = existing + "\n" + value
			} else {
				raw[key] = value
			}
		} else {
			raw[key] = value
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}
	return raw, nil
}

// WebhookConfig holds configuration for webhook notifications
type WebhookConfig struct {
	Enabled       bool
	Endpoints     []WebhookEndpoint
	DefaultFormat string
	Timeout       int
	MaxRetries    int
	RetryDelay    int
}

// WebhookEndpoint represents a single webhook endpoint configuration
type WebhookEndpoint struct {
	Name         string
	URL          string
	Format       string
	Method       string
	Headers      map[string]string
	Auth         WebhookAuth
	CustomFields map[string]interface{}
}

// WebhookAuth holds authentication configuration for a webhook
type WebhookAuth struct {
	Type   string
	Token  string
	User   string
	Pass   string
	Secret string
}

// autoDetectPBSToken tries to read API token from secure_account directory
func autoDetectPBSToken(secureAccountPath string) (token, secret string) {
	if secureAccountPath == "" {
		secureAccountPath = "/opt/proxmox-backup/secure_account"
	}

	// Try multiple possible token file locations
	tokenFiles := []string{
		filepath.Join(secureAccountPath, "pbs_token"),
		filepath.Join(secureAccountPath, "pbs_api_token"),
		"/root/.pbs-token", // Alternative location
	}

	for _, tokenFile := range tokenFiles {
		if utils.FileExists(tokenFile) {
			if data, err := os.ReadFile(tokenFile); err == nil {
				lines := strings.Split(string(data), "\n")
				for _, line := range lines {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					// Format: token_name=token_secret or just token_secret
					if strings.Contains(line, "=") {
						parts := strings.SplitN(line, "=", 2)
						if len(parts) == 2 {
							token = strings.TrimSpace(parts[0])
							secret = strings.TrimSpace(parts[1])
							return
						}
					} else {
						// Just the secret, use default token name
						secret = line
						token = "backup@pbs!go-client" // Default token
						return
					}
				}
			}
		}
	}

	return "", ""
}

// IsGFSRetentionEnabled returns true if GFS retention policy is configured.
// GFS is enabled only when RETENTION_POLICY is explicitly set to "gfs".
func (c *Config) IsGFSRetentionEnabled() bool {
	return strings.ToLower(strings.TrimSpace(c.RetentionPolicy)) == "gfs"
}

// GetRetentionPolicy returns the active retention policy type
// Returns "gfs" if GFS retention is enabled, "simple" otherwise
func (c *Config) GetRetentionPolicy() string {
	if c.IsGFSRetentionEnabled() {
		return "gfs"
	}
	return "simple"
}

// expandEnvVars expands environment variables and special variables like ${BASE_DIR}
