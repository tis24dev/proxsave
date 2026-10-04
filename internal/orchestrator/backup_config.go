package orchestrator

import (
	"strings"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/block"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// BuildArchiverConfig builds a pure ArchiverConfig from the provided inputs.
func BuildArchiverConfig(
	compressionType types.CompressionType,
	compressionLevel int,
	compressionThreads int,
	compressionMode string,
	dryRun bool,
	encryptArchive bool,
	ageRecipients []age.Recipient,
	excludePatterns []string,
) *backup.ArchiverConfig {
	return &backup.ArchiverConfig{
		Compression:        compressionType,
		CompressionLevel:   compressionLevel,
		CompressionThreads: compressionThreads,
		CompressionMode:    compressionMode,
		DryRun:             dryRun,
		EncryptArchive:     encryptArchive,
		AgeRecipients:      ageRecipients,
		ExcludePatterns:    append([]string(nil), excludePatterns...),
	}
}

// InitializeBackupStats builds the initial BackupStats snapshot without side effects.
func InitializeBackupStats(
	hostname string,
	envInfo *environment.EnvironmentInfo,
	version string,
	startTime time.Time,
	cfg *config.Config,
	compressionType types.CompressionType,
	compressionMode string,
	compressionLevel int,
	compressionThreads int,
	backupPath string,
	serverID, serverMAC string,
) *BackupStats {
	pType := types.ProxmoxUnknown
	proxmoxVersion := "unknown"
	pveVersion := ""
	pbsVersion := ""
	targets := []string(nil)
	if envInfo != nil {
		pType = envInfo.Type
		proxmoxVersion = envInfo.Version
		pveVersion = envInfo.PVEVersion
		pbsVersion = envInfo.PBSVersion
		targets = append(targets, envInfo.Type.Targets()...)
	}
	stats := &BackupStats{
		Hostname:                 hostname,
		ProxmoxType:              pType,
		ProxmoxTargets:           targets,
		ProxmoxVersion:           proxmoxVersion,
		PVEVersion:               pveVersion,
		PBSVersion:               pbsVersion,
		Timestamp:                startTime,
		Version:                  version,
		ScriptVersion:            version,
		StartTime:                startTime,
		RequestedCompression:     compressionType,
		RequestedCompressionMode: compressionMode,
		Compression:              compressionType,
		CompressionLevel:         compressionLevel,
		CompressionMode:          compressionMode,
		CompressionThreads:       compressionThreads,
		LocalPath:                backupPath,
		EmailStatus:              "unknown",
		TelegramStatus:           describeTelegramStatus(cfg),
		ServerID:                 serverID,
		ServerMAC:                serverMAC,
		ExitCode:                 types.ExitSuccess.Int(),
	}

	if cfg != nil {
		stats.SecondaryEnabled = cfg.SecondaryEnabled
		stats.CloudEnabled = cfg.CloudEnabled
		stats.SecondaryStartupFailed = cfg.SecondaryStartupFailed && !cfg.SecondaryEnabled
		stats.CloudStartupFailed = cfg.CloudStartupFailed && !cfg.CloudEnabled
		stats.SecondaryPath = cfg.SecondaryPath
		stats.CloudPath = cfg.CloudRemote
		stats.MaxLocalBackups = cfg.MaxLocalBackups
		stats.MaxSecondaryBackups = cfg.MaxSecondaryBackups
		stats.MaxCloudBackups = cfg.MaxCloudBackups
		if stats.LocalPath == "" {
			stats.LocalPath = cfg.BackupPath
		}
	}

	// An enabled copy starts as "skipped", not "ok": nothing has been copied yet, and
	// StorageAdapter.Sync is the only writer of a real outcome (it sets "ok" the moment
	// it starts, then error or warning as they happen). A run that fails before the
	// copies, or whose critical local store fails first, never reaches Sync for them,
	// and the "ok" this used to start from reached every channel as a copy that worked.
	//
	// A copy that is on in backup.env and failed at startup is out of the run, and
	// nothing was saved there: "error", with a count never read (-1) and the retention
	// form of the configuration, so its summary reads "?/<M>", "?/-" or "?/?".
	switch {
	case stats.SecondaryEnabled:
		stats.SecondaryStatus = "skipped"
	case stats.SecondaryStartupFailed:
		stats.SecondaryStatus = "error"
		stats.SecondaryBackups = -1
		rc := configuredRetention(cfg, storage.LocationSecondary)
		stats.SecondaryRetentionPolicy = rc.Policy
		if rc.Policy == "gfs" {
			stats.SecondaryGFSDaily, stats.SecondaryGFSWeekly, stats.SecondaryGFSMonthly, stats.SecondaryGFSYearly = rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly
		}
	default:
		stats.SecondaryStatus = "disabled"
	}

	switch {
	case stats.CloudEnabled:
		stats.CloudStatus = "skipped"
	case stats.CloudStartupFailed:
		stats.CloudStatus = "error"
		stats.CloudBackups = -1
		rc := configuredRetention(cfg, storage.LocationCloud)
		stats.CloudRetentionPolicy = rc.Policy
		if rc.Policy == "gfs" {
			stats.CloudGFSDaily, stats.CloudGFSWeekly, stats.CloudGFSMonthly, stats.CloudGFSYearly = rc.Daily, rc.Weekly, rc.Monthly, rc.Yearly
		}
	default:
		stats.CloudStatus = "disabled"
	}

	// The PBS block writes its own outcome at step [7]; until then nothing was uploaded.
	// Without PBS the field stays nil and out of the serialized stats.
	if cfg != nil && cfg.PBSTargetEnabled {
		rc := block.PBSRetentionFromConfig(cfg)
		stats.PBSTarget = &block.Result{
			Name:            block.PBSName,
			Status:          block.StatusSkipped,
			Backups:         -1,
			MaxBackups:      rc.MaxBackups,
			RetentionPolicy: rc.Policy,
		}
	}

	return stats
}

// configuredRetention is the retention a location has in the configuration, with the
// GFS tiers as the retention pass would apply them (EffectiveGFSRetentionConfig, the
// silent half of NormalizeGFSRetentionConfig).
func configuredRetention(cfg *config.Config, location storage.BackupLocation) storage.RetentionConfig {
	rc := storage.NewRetentionConfigFromConfig(cfg, location)
	if rc.Policy == "gfs" {
		rc = storage.EffectiveGFSRetentionConfig(rc)
	}
	return rc
}

func describeTelegramStatus(cfg *config.Config) string {
	if cfg == nil || !cfg.TelegramEnabled {
		return "disabled"
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.TelegramBotType))
	if mode == "" {
		return "personal"
	}
	switch mode {
	case "personal", "centralized":
		return mode
	default:
		return mode
	}
}
