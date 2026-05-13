// Package main contains the proxsave command entrypoint.
package main

import "github.com/tis24dev/proxsave/internal/logging"

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
	logIgnoredBaseDirOverrides(rt)
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
	if rt.autoBaseDirFound {
		return "auto-detected from executable path"
	}
	return "default fallback"
}

func logIgnoredBaseDirOverrides(rt *appRuntime) {
	if rt == nil || rt.cfg == nil {
		return
	}
	if val, ok := rt.cfg.IgnoredBaseDirConfig(); ok {
		logging.Warning("Ignoring deprecated BASE_DIR from backup.env (%q); using detected base directory %s", val, rt.cfg.BaseDir)
	}
	if val, ok := rt.cfg.IgnoredBaseDirEnv(); ok {
		logging.Warning("Ignoring deprecated BASE_DIR from environment (%q); using detected base directory %s", val, rt.cfg.BaseDir)
	}
}

func runConfigPathSource(rt *appRuntime) string {
	if rt.args.ConfigPathSource != "" {
		return rt.args.ConfigPathSource
	}
	return "configured path"
}
