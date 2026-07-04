package orchestrator

import (
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
)

const defaultTelegramServerAPIHost = "https://bot.proxsave.dev"

// TelegramSetupMaxVerificationAttempts bounds how many registration checks the
// install wizard runs before it stops offering another check and requires the
// user to skip. Shared by the CLI and TUI so both enforce the same policy.
const TelegramSetupMaxVerificationAttempts = 10

// TelegramSetupStatusMessageMaxRunes caps the length of the untrusted Telegram
// registration status message shown during verification. Shared by the CLI and
// TUI so both apply the same bound.
const TelegramSetupStatusMessageMaxRunes = 200

// telegramSetupTruncationSuffix is appended to a truncated status message; its
// length is reserved so the final string stays within TelegramSetupStatusMessageMaxRunes.
const telegramSetupTruncationSuffix = "...(truncated)"

// TruncateTelegramSetupStatusMessage trims and rune-safely truncates an untrusted
// Telegram registration status message for display. Shared by the CLI and TUI so
// both truncate identically and neither slices a multi-byte rune mid-sequence
// (the previous TUI byte-based truncation could emit invalid UTF-8). The suffix is
// counted against the cap so the returned string never exceeds
// TelegramSetupStatusMessageMaxRunes runes.
func TruncateTelegramSetupStatusMessage(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return ""
	}
	runes := []rune(msg)
	if len(runes) <= TelegramSetupStatusMessageMaxRunes {
		return msg
	}
	keep := TelegramSetupStatusMessageMaxRunes - len([]rune(telegramSetupTruncationSuffix))
	if keep < 0 {
		keep = 0
	}
	return string(runes[:keep]) + telegramSetupTruncationSuffix
}

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
	telegramSetupBootstrapLoadConfig     = config.LoadConfigWithBaseDir
	telegramSetupBootstrapIdentityDetect = identity.Detect
	telegramSetupBootstrapStat           = os.Stat
)

func BuildTelegramSetupBootstrap(configPath, baseDir string) (TelegramSetupBootstrap, error) {
	state := TelegramSetupBootstrap{}

	cfg, err := telegramSetupBootstrapLoadConfig(configPath, baseDir)
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
