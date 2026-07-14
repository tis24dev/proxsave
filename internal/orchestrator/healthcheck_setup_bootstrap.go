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
	HealthcheckSetupSkipDisabled            // cron mode / HEALTHCHECK_ENABLED=false
	HealthcheckSetupSkipConfigError         // config failed to load
	HealthcheckSetupSkipSelfMode            // self mode: no centralized portal/magic-link
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

	// HealthcheckHeartbeatInterval is the daemon's configured heartbeat period; the
	// connection check needs it to judge (via health.Diagnose) whether the daemon's last
	// beat is fresh or stale, exactly as the run-start init check does.
	HealthcheckHeartbeatInterval time.Duration

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
		state.Eligibility = HealthcheckSetupSkipSelfMode
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
