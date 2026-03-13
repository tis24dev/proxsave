package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui/wizard"
	"github.com/tis24dev/proxsave/internal/types"
)

// runNewKey performs a standalone AGE recipient setup without running a backup.
func runNewKey(ctx context.Context, configPath string, logLevel types.LogLevel, bootstrap *logging.BootstrapLogger, useCLI bool) (err error) {
	if logLevel == types.LogLevelNone {
		logLevel = types.LogLevelInfo
	}

	useColor := term.IsTerminal(int(os.Stdout.Fd()))
	logger, logPath, closeLog, logErr := logging.StartSessionLogger("newkey", logLevel, useColor)
	if logErr != nil {
		logger = logging.New(logLevel, useColor)
		closeLog = func() {}
	} else if bootstrap != nil {
		bootstrap.Info("NEWKEY log: %s", logPath)
	}
	if !useCLI {
		logger.SetOutput(io.Discard)
	}
	defer closeLog()

	logging.SetDefaultLogger(logger)
	if bootstrap != nil {
		bootstrap.SetLevel(logLevel)
		bootstrap.SetMirrorLogger(logger)
	}

	done := logging.DebugStartBootstrap(bootstrap, "newkey workflow", "mode=%s", modeLabel(useCLI))
	defer func() { done(err) }()

	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	// Derive BASE_DIR from the configuration path
	baseDir := filepath.Dir(filepath.Dir(configPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		if _, err := os.Stat("/opt/proxsave"); err == nil {
			baseDir = "/opt/proxsave"
		} else {
			baseDir = "/opt/proxmox-backup"
		}
	}
	_ = os.Setenv("BASE_DIR", baseDir)

	logging.DebugStepBootstrap(bootstrap, "newkey workflow", "config=%s base=%s", configPath, baseDir)
	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	if useCLI {
		return runNewKeyCLI(ctx, configPath, baseDir, logger, bootstrap)
	}
	return runNewKeyTUI(ctx, configPath, baseDir, bootstrap)
}

func runNewKeyTUI(ctx context.Context, configPath, baseDir string, bootstrap *logging.BootstrapLogger) (err error) {
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	sig := buildSignature()
	if strings.TrimSpace(sig) == "" {
		sig = "n/a"
	}
	done := logging.DebugStartBootstrap(bootstrap, "newkey workflow (tui)", "recipient=%s", recipientPath)
	defer func() { done(err) }()

	logging.DebugStepBootstrap(bootstrap, "newkey workflow (tui)", "running AGE setup via orchestrator")
	if err := runNewKeySetup(ctx, configPath, baseDir, logging.GetDefaultLogger(), wizard.NewAgeSetupUI(configPath, sig)); err != nil {
		return err
	}

	bootstrap.Info("✓ New AGE recipient(s) generated and saved to %s", recipientPath)
	bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")

	return nil
}

func runNewKeyCLI(ctx context.Context, configPath, baseDir string, logger *logging.Logger, bootstrap *logging.BootstrapLogger) error {
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	if err := runNewKeySetup(ctx, configPath, baseDir, logger, nil); err != nil {
		return err
	}

	bootstrap.Info("✓ New AGE recipient(s) generated and saved to %s", recipientPath)
	bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")

	return nil
}

func modeLabel(useCLI bool) string {
	if useCLI {
		return "cli"
	}
	return "tui"
}

func runNewKeySetup(ctx context.Context, configPath, baseDir string, logger *logging.Logger, ui orchestrator.AgeSetupUI) error {
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")

	cfg := &config.Config{
		BaseDir:          baseDir,
		ConfigPath:       configPath,
		EncryptArchive:   true,
		AgeRecipientFile: recipientPath,
	}

	if logger == nil {
		useColor := term.IsTerminal(int(os.Stdout.Fd()))
		logger = logging.New(types.LogLevelInfo, useColor)
	}

	orch := orchestrator.New(logger, false)
	orch.SetConfig(cfg)
	orch.SetForceNewAgeRecipient(true)

	var err error
	if ui != nil {
		err = orch.EnsureAgeRecipientsReadyWithUI(ctx, ui)
	} else {
		err = orch.EnsureAgeRecipientsReady(ctx)
	}
	if err != nil {
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return wrapInstallError(errInteractiveAborted)
		}
		return fmt.Errorf("AGE setup failed: %w", err)
	}

	return nil
}
