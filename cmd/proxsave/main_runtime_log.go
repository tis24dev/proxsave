// Package main contains the proxsave command entrypoint.
package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func logRunContext(rt *appRuntime) {
	logRunDryRunStatus(rt)
	baseDirSource := runBaseDirSource(rt)
	logging.Info("Environment: %s %s", rt.envInfo.Type, rt.envInfo.Version)
	logUserNamespaceContext(rt.logger, rt.unprivilegedInfo)
	logging.Info("Backup enabled: %v", rt.cfg.BackupEnabled)
	logging.Info("Debug level: %s", rt.logLevel.String())
	logging.Info("Compression: %s (level %d, mode %s)", rt.cfg.CompressionType, rt.cfg.CompressionLevel, rt.cfg.CompressionMode)
	logging.Info("Base directory: %s (%s)", rt.cfg.BaseDir, baseDirSource)
	logging.Info("Configuration file: %s (%s)", rt.args.ConfigPath, runConfigPathSource(rt))
}

func logRunDryRunStatus(rt *appRuntime) {
	if !rt.dryRun {
		return
	}
	if rt.args.DryRun {
		logging.Info("DRY RUN MODE: No actual changes will be made (enabled via --dry-run flag)")
		return
	}
	logging.Info("DRY RUN MODE: No actual changes will be made (enabled via DRY_RUN config)")
}

func runBaseDirSource(rt *appRuntime) string {
	if rawBaseDir, ok := rt.cfg.Get("BASE_DIR"); ok && strings.TrimSpace(rawBaseDir) != "" {
		return "configured in backup.env"
	}
	if rt.initialEnvBaseDir != "" {
		return "from environment (BASE_DIR)"
	}
	if rt.autoBaseDirFound {
		return "auto-detected from executable path"
	}
	return "default fallback"
}

func runConfigPathSource(rt *appRuntime) string {
	if rt.args.ConfigPathSource != "" {
		return rt.args.ConfigPathSource
	}
	return "configured path"
}
