package orchestrator

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
)

func stubTelegramSetupBootstrapDeps(t *testing.T) {
	t.Helper()

	origLoadConfig := telegramSetupBootstrapLoadConfig
	origIdentityDetect := telegramSetupBootstrapIdentityDetect
	origStat := telegramSetupBootstrapStat

	t.Cleanup(func() {
		telegramSetupBootstrapLoadConfig = origLoadConfig
		telegramSetupBootstrapIdentityDetect = origIdentityDetect
		telegramSetupBootstrapStat = origStat
	})
}

func TestBuildTelegramSetupBootstrap_ConfigLoadFailureSkips(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return nil, errors.New("parse failed")
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		t.Fatalf("identity detect should not run on config failure")
		return nil, nil
	}

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupSkipConfigError {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupSkipConfigError)
	}
	if state.ConfigError == "" {
		t.Fatalf("expected ConfigError to be set")
	}
	if state.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=false")
	}
}

func TestBuildTelegramSetupBootstrap_DisabledSkips(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{TelegramEnabled: false}, nil
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		t.Fatalf("identity detect should not run when telegram is disabled")
		return nil, nil
	}

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupSkipDisabled {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupSkipDisabled)
	}
	if state.TelegramEnabled {
		t.Fatalf("expected TelegramEnabled=false")
	}
}

func TestBuildTelegramSetupBootstrap_PersonalModeSkips(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       " Personal ",
			TelegramServerAPIHost: "",
		}, nil
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		t.Fatalf("identity detect should not run in personal mode")
		return nil, nil
	}

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupSkipPersonalMode {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupSkipPersonalMode)
	}
	if state.TelegramMode != "personal" {
		t.Fatalf("TelegramMode=%q, want personal", state.TelegramMode)
	}
	if state.ServerAPIHost != defaultTelegramServerAPIHost {
		t.Fatalf("ServerAPIHost=%q, want %q", state.ServerAPIHost, defaultTelegramServerAPIHost)
	}
}

func TestBuildTelegramSetupBootstrap_IdentityErrorSkips(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled: true,
			TelegramBotType: "centralized",
		}, nil
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return nil, errors.New("detect failed")
	}

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupSkipIdentityUnavailable {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupSkipIdentityUnavailable)
	}
	if state.IdentityDetectError == "" {
		t.Fatalf("expected IdentityDetectError to be set")
	}
	if state.ServerAPIHost != defaultTelegramServerAPIHost {
		t.Fatalf("ServerAPIHost=%q, want %q", state.ServerAPIHost, defaultTelegramServerAPIHost)
	}
}

func TestBuildTelegramSetupBootstrap_EmptyServerIDSkips(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "centralized",
			TelegramServerAPIHost: "https://api.example.test",
		}, nil
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{ServerID: " ", IdentityFile: " /tmp/id "}, nil
	}

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupSkipIdentityUnavailable {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupSkipIdentityUnavailable)
	}
	if state.ServerID != "" {
		t.Fatalf("ServerID=%q, want empty", state.ServerID)
	}
	if state.IdentityFile != "/tmp/id" {
		t.Fatalf("IdentityFile=%q, want /tmp/id", state.IdentityFile)
	}
}

func TestBuildTelegramSetupBootstrap_EligibleCentralized(t *testing.T) {
	stubTelegramSetupBootstrapDeps(t)

	identityFile := filepath.Join(t.TempDir(), ".server_identity")
	if err := os.WriteFile(identityFile, []byte("id"), 0o600); err != nil {
		t.Fatalf("write identity file: %v", err)
	}

	telegramSetupBootstrapLoadConfig = func(path string) (*config.Config, error) {
		return &config.Config{
			TelegramEnabled:       true,
			TelegramBotType:       "   ",
			TelegramServerAPIHost: " https://api.example.test ",
		}, nil
	}
	telegramSetupBootstrapIdentityDetect = func(baseDir string, logger *logging.Logger) (*identity.Info, error) {
		return &identity.Info{
			ServerID:     " 123456789 ",
			IdentityFile: " " + identityFile + " ",
		}, nil
	}
	telegramSetupBootstrapStat = os.Stat

	state, err := BuildTelegramSetupBootstrap("/fake/backup.env", t.TempDir())
	if err != nil {
		t.Fatalf("BuildTelegramSetupBootstrap error: %v", err)
	}
	if state.Eligibility != TelegramSetupEligibleCentralized {
		t.Fatalf("Eligibility=%v, want %v", state.Eligibility, TelegramSetupEligibleCentralized)
	}
	if !state.ConfigLoaded {
		t.Fatalf("expected ConfigLoaded=true")
	}
	if state.TelegramMode != "centralized" {
		t.Fatalf("TelegramMode=%q, want centralized", state.TelegramMode)
	}
	if state.ServerAPIHost != "https://api.example.test" {
		t.Fatalf("ServerAPIHost=%q, want https://api.example.test", state.ServerAPIHost)
	}
	if state.ServerID != "123456789" {
		t.Fatalf("ServerID=%q, want 123456789", state.ServerID)
	}
	if state.IdentityFile != identityFile {
		t.Fatalf("IdentityFile=%q, want %q", state.IdentityFile, identityFile)
	}
	if !state.IdentityPersisted {
		t.Fatalf("expected IdentityPersisted=true")
	}
}
