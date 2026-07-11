package orchestrator

import (
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
)

// HealthcheckSetupMaxVerificationAttempts bounds how many connection checks the
// install wizard runs before it stops offering another and requires a skip.
// Mirrors TelegramSetupMaxVerificationAttempts.
const HealthcheckSetupMaxVerificationAttempts = 10

// HealthcheckSetupEligibility is the verdict of the install-time gate: the setup
// screen is rendered ONLY for HealthcheckSetupEligibleCentralized; every skip
// verdict renders nothing (mirroring the Telegram setup pattern).
type HealthcheckSetupEligibility int

const (
	HealthcheckSetupEligibilityUnknown HealthcheckSetupEligibility = iota
	HealthcheckSetupEligibleCentralized
	HealthcheckSetupEligibleSelf            // self mode with a collected alive ping URL: reachability check only
	HealthcheckSetupSkipDisabled            // cron mode / HEALTHCHECK_ENABLED=false
	HealthcheckSetupSkipConfigError         // config failed to load
	HealthcheckSetupSkipSelfMode            // self mode but no alive URL collected yet (params screen not run)
	HealthcheckSetupSkipIdentityUnavailable // no ServerID or no relay secret on disk
)

// HealthcheckSetupBootstrap is the eligibility + context the front-ends need.
type HealthcheckSetupBootstrap struct {
	Eligibility HealthcheckSetupEligibility

	ConfigLoaded bool
	ConfigError  string

	HealthcheckEnabled bool
	HealthcheckMode    string
	ServerAPIHost      string

	// HealthcheckAliveURL is the self-mode full service-alive ping URL (cfg.HealthcheckAliveURL).
	// It is the sole input the self reachability check needs; centralized mode leaves it empty.
	HealthcheckAliveURL string

	// HealthcheckHeartbeatInterval is the daemon's configured heartbeat period; the
	// connection check needs it to judge (via health.Diagnose) whether the daemon's last
	// beat is fresh or stale, exactly as the run-start init check does.
	HealthcheckHeartbeatInterval time.Duration

	// HealthcheckUpdateInterval is the daemon's configured update-check period; the
	// updates sensor ages against 2x this cadence (not the heartbeat), so a longer
	// HEALTHCHECK_UPDATE_INTERVAL does not render a freshly-transmitted updates ping stale.
	HealthcheckUpdateInterval time.Duration

	ServerID  string
	HasSecret bool
}

var (
	healthcheckSetupLoadConfig     = config.LoadConfigWithBaseDir
	healthcheckSetupIdentityDetect = identity.Detect
	healthcheckSetupLoadSecret     = func(baseDir string) string {
		s, _ := identity.LoadNotifySecret(baseDir)
		return s
	}
)

// BuildHealthcheckSetupBootstrap re-reads the just-written config (the same
// single-source-of-truth approach Telegram uses) and decides whether the
// centralized healthchecks setup screen should appear. Eligible requires: the
// daemon engine was chosen (HEALTHCHECK_ENABLED=true), centralized mode, a resolved
// ServerID, and a relay secret on disk (from Telegram pairing, needed for the
// authenticated fetch). All skip paths return (state, nil) - never an error - so
// the step is fully non-blocking.
func BuildHealthcheckSetupBootstrap(configPath, baseDir string) (HealthcheckSetupBootstrap, error) {
	state := HealthcheckSetupBootstrap{}

	cfg, err := healthcheckSetupLoadConfig(configPath, baseDir)
	if err != nil {
		state.Eligibility = HealthcheckSetupSkipConfigError
		state.ConfigError = err.Error()
		return state, nil
	}
	state.ConfigLoaded = true
	if cfg != nil {
		state.HealthcheckEnabled = cfg.HealthcheckEnabled
		state.HealthcheckMode = strings.ToLower(strings.TrimSpace(cfg.HealthcheckMode))
		state.ServerAPIHost = strings.TrimSpace(cfg.ServerAPIHost)
		state.HealthcheckHeartbeatInterval = cfg.HealthcheckHeartbeatInterval
		state.HealthcheckUpdateInterval = cfg.HealthcheckUpdateInterval
		state.HealthcheckAliveURL = strings.TrimSpace(cfg.HealthcheckAliveURL)
	}

	if !state.HealthcheckEnabled {
		state.Eligibility = HealthcheckSetupSkipDisabled
		return state, nil
	}
	if state.HealthcheckMode == "" {
		state.HealthcheckMode = "centralized"
	}
	if state.ServerAPIHost == "" {
		state.ServerAPIHost = defaultServerAPIHost
	}
	if state.HealthcheckMode == "self" {
		// Self mode needs no centralized identity (no ServerID / relay secret): the
		// check is pure reachability of the user's own alive URL. Return BEFORE the
		// identity checks. When the params screen has not run yet (no alive URL), skip.
		if state.HealthcheckAliveURL != "" {
			state.Eligibility = HealthcheckSetupEligibleSelf
		} else {
			state.Eligibility = HealthcheckSetupSkipSelfMode
		}
		return state, nil
	}

	if info, derr := healthcheckSetupIdentityDetect(baseDir, nil); derr == nil && info != nil {
		state.ServerID = strings.TrimSpace(info.ServerID)
	}
	if state.ServerID == "" {
		state.Eligibility = HealthcheckSetupSkipIdentityUnavailable
		return state, nil
	}

	state.HasSecret = strings.TrimSpace(healthcheckSetupLoadSecret(baseDir)) != ""
	if !state.HasSecret {
		state.Eligibility = HealthcheckSetupSkipIdentityUnavailable
		return state, nil
	}

	state.Eligibility = HealthcheckSetupEligibleCentralized
	return state, nil
}
