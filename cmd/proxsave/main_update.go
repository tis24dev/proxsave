// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// UpdateInfo holds information about the version check result.
type UpdateInfo struct {
	NewVersion bool
	Current    string
	Latest     string
	// Tag is the latest release's git tag (e.g. "v0.29.0"), used to build the release
	// page URL. It is remote-controlled: consumers that display a URL from it MUST scrub
	// the tag portion. Empty when the release could not be read.
	Tag string
	// Notes is the latest release's CodeRabbit "Release Notes" summary (extracted from
	// the GitHub release body). It is remote-controlled text: consumers that render it
	// MUST sanitize it. Empty when the block is absent or the release could not be read.
	Notes string
	// NoticeLogged is set by logUpdateAvailable when it wrote the notice to the run log as a
	// WARNING. False on a host whose daemon reports updates to healthchecks.
	NoticeLogged bool
}

const (
	coderabbitNotesStart = "<!-- This is an auto-generated comment: release notes by coderabbit.ai -->"
	coderabbitNotesEnd   = "<!-- end of auto-generated comment: release notes by coderabbit.ai -->"
)

// extractReleaseNotes returns the CodeRabbit "Release Notes" block from a GitHub release
// body (the markdown between the two auto-generated-comment markers), or "" when absent.
// The returned text is raw markdown and is NOT sanitized here.
func extractReleaseNotes(body string) string {
	i := strings.Index(body, coderabbitNotesStart)
	if i < 0 {
		return ""
	}
	rest := body[i+len(coderabbitNotesStart):]
	if j := strings.Index(rest, coderabbitNotesEnd); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// checkForUpdates performs a best-effort check against the latest GitHub release. It only
// writes DEBUG entries: telling the operator is logUpdateAvailable's job, because whether the
// finding is a WARNING of the run depends on who else reports it. A populated *UpdateInfo is
// returned so that callers can propagate structured information into notifications/metrics.
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

	latestTag, latestVersion, releaseBody, err := fetchLatestRelease(checkCtx)
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

	notes := extractReleaseNotes(releaseBody)

	if !isNewerVersion(currentVersion, latestVersion) {
		logger.Debug("Update check completed: latest=%s current=%s (up to date)", latestVersion, currentVersion)
		return &UpdateInfo{
			NewVersion: false,
			Current:    currentVersion,
			Latest:     latestVersion,
			Tag:        latestTag,
			Notes:      notes,
		}
	}

	logger.Debug("Update check completed: latest=%s current=%s (new version available)", latestVersion, currentVersion)

	return &UpdateInfo{
		NewVersion: true,
		Current:    currentVersion,
		Latest:     latestVersion,
		Tag:        latestTag,
		Notes:      notes,
	}
}

// logUpdateAvailable tells the operator about a newer release found by checkForUpdates. A
// WARNING is counted into the run's exit code, and the daemon hands that exit code to the
// healthchecks backup check, which then went down for an available update alone (issue #334).
// So where the daemon already reports the finding to the healthchecks updates check, the run
// keeps it at DEBUG; everywhere else it stays a WARNING and NoticeLogged records it, so the
// notifications list the notice once either way (from the parsed log, or added by the
// orchestrator when the log does not carry it).
func logUpdateAvailable(logger *logging.Logger, cfg *config.Config, info *UpdateInfo) {
	if logger == nil || info == nil || !info.NewVersion {
		return
	}
	if daemonReportsUpdates(cfg) {
		logger.Debug("Update notice: new version %s left to the daemon's healthchecks updates check (SCHEDULER_MODE=daemon, HEALTHCHECK_ENABLED=true, HEALTHCHECK_MODE=%s), not a warning of this run",
			info.Latest, cfg.HealthcheckMode)
		return
	}
	logger.Warning("%s", orchestrator.UpdateNoticeMessage(info.Latest, info.Current))
	info.NoticeLogged = true
}

// daemonReportsUpdates reports whether this host's daemon sends the update finding to a
// healthchecks updates check: the daemon schedules the backups, healthchecks is enabled, and
// the updates check can be resolved. In centralized mode the ProxSave HC Server provisions it
// for every host; in self mode it exists only when HEALTHCHECK_UPDATES_URL or
// HEALTHCHECK_UPDATES_ID is set (the daemon resolves it the same way, daemon.selfURLs).
func daemonReportsUpdates(cfg *config.Config) bool {
	if cfg == nil || cfg.SchedulerMode != "daemon" || !cfg.HealthcheckEnabled {
		return false
	}
	switch cfg.HealthcheckMode {
	case config.HealthcheckModeCentralized:
		return true
	case config.HealthcheckModeSelf:
		return cfg.HealthcheckSelfPingURL(cfg.HealthcheckUpdatesURL, cfg.HealthcheckUpdatesID) != ""
	}
	return false
}

// isNewerVersion returns true if latest is strictly newer than current.
// It compares MAJOR.MINOR.PATCH, ignores build metadata, and treats a stable
// release as newer than a prerelease with the same numeric version.
func isNewerVersion(current, latest string) bool {
	parse := func(v string) (int, int, int, bool) {
		v = strings.TrimSpace(v)
		v = strings.TrimPrefix(v, "v")
		if i := strings.IndexByte(v, '+'); i >= 0 {
			v = v[:i]
		}
		hasPrerelease := false
		if i := strings.IndexByte(v, '-'); i >= 0 {
			hasPrerelease = true
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
		return major, minor, patch, hasPrerelease
	}

	curMaj, curMin, curPatch, curPrerelease := parse(current)
	latMaj, latMin, latPatch, latPrerelease := parse(latest)

	if latMaj != curMaj {
		return latMaj > curMaj
	}
	if latMin != curMin {
		return latMin > curMin
	}
	if latPatch != curPatch {
		return latPatch > curPatch
	}
	return curPrerelease && !latPrerelease
}
