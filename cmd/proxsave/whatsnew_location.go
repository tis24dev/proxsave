package main

import (
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// whatsnewLoadConfig is a seam so tests can resolve LOG_PATH without an on-disk config.
var whatsnewLoadConfig = config.LoadConfigWithBaseDir

// whatsnewLocation resolves where the seen-flag lives for a caller that has not loaded the
// configuration yet (the dashboard, --show-whatsnew, the install seed). Without a readable
// configuration LOG_PATH is unknown: the Location then carries no LogPath and the whatsnew
// package stays silent (no screen, no nudge, no write).
func whatsnewLocation(configPath, baseDir string) whatsnew.Location {
	cfg, err := whatsnewLoadConfig(configPath, baseDir)
	if err != nil || cfg == nil {
		return whatsnew.Location{BaseDir: baseDir}
	}
	return whatsnewLocationFromConfig(cfg, baseDir)
}

// whatsnewLocationFromConfig is the seen-flag's Location for a loaded configuration: the flag in
// LOG_PATH, every operation on it bounded by FS_IO_TIMEOUT, and baseDir for the one-time move
// from identity/.
func whatsnewLocationFromConfig(cfg *config.Config, baseDir string) whatsnew.Location {
	if cfg == nil {
		return whatsnew.Location{BaseDir: baseDir}
	}
	return whatsnew.Location{
		LogPath: cfg.LogPath,
		BaseDir: baseDir,
		Timeout: fsIoTimeoutDuration(cfg),
	}
}
