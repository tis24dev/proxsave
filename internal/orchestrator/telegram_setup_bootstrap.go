package orchestrator

import (
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
)

const defaultTelegramServerAPIHost = "https://bot.tis24.it:1443"

type TelegramSetupEligibility int

const (
	TelegramSetupEligibilityUnknown TelegramSetupEligibility = iota
	TelegramSetupEligibleCentralized
	TelegramSetupSkipDisabled
	TelegramSetupSkipConfigError
	TelegramSetupSkipPersonalMode
	TelegramSetupSkipIdentityUnavailable
)

type TelegramSetupBootstrap struct {
	Eligibility TelegramSetupEligibility

	ConfigLoaded bool
	ConfigError  string

	TelegramEnabled bool
	TelegramMode    string
	ServerAPIHost   string

	ServerID            string
	IdentityFile        string
	IdentityPersisted   bool
	IdentityDetectError string
}

var (
	telegramSetupBootstrapLoadConfig     = config.LoadConfig
	telegramSetupBootstrapIdentityDetect = identity.Detect
	telegramSetupBootstrapStat           = os.Stat
)

func BuildTelegramSetupBootstrap(configPath, baseDir string) (TelegramSetupBootstrap, error) {
	state := TelegramSetupBootstrap{}

	cfg, err := telegramSetupBootstrapLoadConfig(configPath)
	if err != nil {
		state.Eligibility = TelegramSetupSkipConfigError
		state.ConfigError = err.Error()
		return state, nil
	}

	state.ConfigLoaded = true
	if cfg != nil {
		state.TelegramEnabled = cfg.TelegramEnabled
		state.TelegramMode = strings.ToLower(strings.TrimSpace(cfg.TelegramBotType))
		state.ServerAPIHost = strings.TrimSpace(cfg.TelegramServerAPIHost)
	}

	if !state.TelegramEnabled {
		state.Eligibility = TelegramSetupSkipDisabled
		return state, nil
	}

	if state.TelegramMode == "" {
		state.TelegramMode = "centralized"
	}
	if state.ServerAPIHost == "" {
		state.ServerAPIHost = defaultTelegramServerAPIHost
	}

	if state.TelegramMode == "personal" {
		state.Eligibility = TelegramSetupSkipPersonalMode
		return state, nil
	}

	info, err := telegramSetupBootstrapIdentityDetect(baseDir, nil)
	if err != nil {
		state.Eligibility = TelegramSetupSkipIdentityUnavailable
		state.IdentityDetectError = err.Error()
		return state, nil
	}

	if info != nil {
		state.ServerID = strings.TrimSpace(info.ServerID)
		state.IdentityFile = strings.TrimSpace(info.IdentityFile)
		if state.IdentityFile != "" {
			if _, statErr := telegramSetupBootstrapStat(state.IdentityFile); statErr == nil {
				state.IdentityPersisted = true
			}
		}
	}

	if state.ServerID == "" {
		state.Eligibility = TelegramSetupSkipIdentityUnavailable
		return state, nil
	}

	state.Eligibility = TelegramSetupEligibleCentralized
	return state, nil
}
