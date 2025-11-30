package orchestrator

import (
	"strings"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
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
) *backup.ArchiverConfig {
	return &backup.ArchiverConfig{
		Compression:        compressionType,
		CompressionLevel:   compressionLevel,
		CompressionThreads: compressionThreads,
		CompressionMode:    compressionMode,
		DryRun:             dryRun,
		EncryptArchive:     encryptArchive,
		AgeRecipients:      ageRecipients,
	}
}

// InitializeBackupStats builds the initial BackupStats snapshot without side effects.
func InitializeBackupStats(
	hostname string,
	pType types.ProxmoxType,
	proxmoxVersion string,
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
	stats := &BackupStats{
		Hostname:                 hostname,
		ProxmoxType:              pType,
		ProxmoxVersion:           proxmoxVersion,
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
		stats.SecondaryPath = cfg.SecondaryPath
		stats.CloudPath = cfg.CloudRemote
		stats.MaxLocalBackups = cfg.MaxLocalBackups
		stats.MaxSecondaryBackups = cfg.MaxSecondaryBackups
		stats.MaxCloudBackups = cfg.MaxCloudBackups
		if stats.LocalPath == "" {
			stats.LocalPath = cfg.BackupPath
		}
	}

	if stats.SecondaryEnabled {
		stats.SecondaryStatus = "ok"
	} else {
		stats.SecondaryStatus = "disabled"
	}

	if stats.CloudEnabled {
		stats.CloudStatus = "ok"
	} else {
		stats.CloudStatus = "disabled"
	}

	return stats
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
