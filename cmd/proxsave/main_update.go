// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// UpdateInfo holds information about the version check result.
type UpdateInfo struct {
	NewVersion bool
	Current    string
	Latest     string
}

// checkForUpdates performs a best-effort check against the latest GitHub release.
//   - If the latest version cannot be determined or the current version is already up to date,
//     only a DEBUG log entry is written (no user-facing output).
//   - If a newer version is available, a WARNING is logged suggesting the --upgrade command.
//     Additionally, a populated *UpdateInfo is returned so that callers can propagate
//     structured information into notifications/metrics.
func checkForUpdates(ctx context.Context, logger *logging.Logger, currentVersion string) *UpdateInfo {
	if logger == nil {
		return nil
	}

	currentVersion = strings.TrimSpace(currentVersion)
	if currentVersion == "" {
		logger.Debug("Update check skipped: current version is empty")
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	logger.Debug("Checking for ProxSave updates (current: %s)", currentVersion)

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)
	logger.Debug("Fetching latest release from GitHub: %s", apiURL)

	_, latestVersion, err := fetchLatestRelease(checkCtx)
	if err != nil {
		logger.Debug("Update check skipped: GitHub unreachable: %v", err)
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
		}
	}

	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion == "" {
		logger.Debug("Update check skipped: latest version from GitHub is empty")
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
		}
	}

	if !isNewerVersion(currentVersion, latestVersion) {
		logger.Debug("Update check completed: latest=%s current=%s (up to date)", latestVersion, currentVersion)
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
			Latest:     latestVersion,
		}
	}

	logger.Debug("Update check completed: latest=%s current=%s (new version available)", latestVersion, currentVersion)
	logger.Warning("New ProxSave version %s (current %s): run 'proxsave --upgrade' to install.", latestVersion, currentVersion)

	return &UpdateInfo{
		NewVersion: true,
		Current:    currentVersion,
		Latest:     latestVersion,
	}
}

// isNewerVersion returns true if latest is strictly newer than current,
// comparing MAJOR.MINOR.PATCH (ignoring any leading 'v', pre-release suffixes, and build metadata).
func isNewerVersion(current, latest string) bool {
	parse := func(v string) (int, int, int) {
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(v, "v")
		if i := strings.IndexByte(v, '-'); i >= 0 {
			v = v[:i]
		}
		if i := strings.IndexByte(v, '+'); i >= 0 {
			v = v[:i]
		}

		parts := strings.Split(v, ".")
		toInt := func(s string) int {
			n, _ := strconv.Atoi(s)
			return n
		}

		major, minor, patch := 0, 0, 0
		if len(parts) > 0 {
			major = toInt(parts[0])
		}
		if len(parts) > 1 {
			minor = toInt(parts[1])
		}
		if len(parts) > 2 {
			patch = toInt(parts[2])
		}
		return major, minor, patch
	}

	curMaj, curMin, curPatch := parse(current)
	latMaj, latMin, latPatch := parse(latest)

	if latMaj != curMaj {
		return latMaj > curMaj
	}
	if latMin != curMin {
		return latMin > curMin
	}
	return latPatch > curPatch
}
