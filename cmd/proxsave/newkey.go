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

	// If a recipient already exists, ask for confirmation before overwriting
	if _, err := os.Stat(recipientPath); err == nil {
		logging.DebugStepBootstrap(bootstrap, "newkey workflow (tui)", "existing recipient found")
		confirm, err := wizard.ConfirmRecipientOverwrite(recipientPath, configPath, sig)
		if err != nil {
			return err
		}
		if !confirm {
			return wrapInstallError(errInteractiveAborted)
		}
		if err := orchestrator.BackupAgeRecipientFile(recipientPath); err != nil && bootstrap != nil {
			bootstrap.Warning("WARNING: %v", err)
		}
	}

	recipients := make([]string, 0, 2)
	for {
		logging.DebugStepBootstrap(bootstrap, "newkey workflow (tui)", "running AGE setup wizard")
		ageData, err := wizard.RunAgeSetupWizard(ctx, recipientPath, configPath, sig)
		if err != nil {
			if errors.Is(err, wizard.ErrAgeSetupCancelled) {
				return wrapInstallError(errInteractiveAborted)
			}
			return fmt.Errorf("AGE setup failed: %w", err)
		}

		// Process the AGE data based on setup type
		var recipientKey string
		switch ageData.SetupType {
		case "existing":
			recipientKey = ageData.PublicKey
		case "passphrase":
			recipient, err := deriveRecipientFromPassphrase(ageData.Passphrase)
			if err != nil {
				return fmt.Errorf("failed to derive recipient from passphrase: %w", err)
			}
			recipientKey = recipient
		case "privatekey":
			recipient, err := deriveRecipientFromPrivateKey(ageData.PrivateKey)
			if err != nil {
				return fmt.Errorf("failed to derive recipient from private key: %w", err)
			}
			recipientKey = recipient
		default:
			return fmt.Errorf("unknown AGE setup type: %s", ageData.SetupType)
		}

		if err := orchestrator.ValidateRecipientString(recipientKey); err != nil {
			return fmt.Errorf("invalid recipient: %w", err)
		}
		recipients = append(recipients, recipientKey)

		logging.DebugStepBootstrap(bootstrap, "newkey workflow (tui)", "recipient count=%d", len(recipients))
		addMore, err := wizard.ConfirmAddRecipient(configPath, sig, len(recipients))
		if err != nil {
			return err
		}
		if !addMore {
			break
		}
	}

	recipients = orchestrator.DedupeRecipientStrings(recipients)
	if len(recipients) == 0 {
		return fmt.Errorf("no AGE recipients provided")
	}
	logging.DebugStepBootstrap(bootstrap, "newkey workflow (tui)", "saving recipients")
	if err := orchestrator.WriteRecipientFile(recipientPath, recipients); err != nil {
		return fmt.Errorf("failed to save AGE recipients: %w", err)
	}

	bootstrap.Info("✓ New AGE recipient(s) generated and saved to %s", recipientPath)
	bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")

	return nil
}

func runNewKeyCLI(ctx context.Context, configPath, baseDir string, logger *logging.Logger, bootstrap *logging.BootstrapLogger) error {
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

	if err := orch.EnsureAgeRecipientsReady(ctx); err != nil {
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return wrapInstallError(errInteractiveAborted)
		}
		return fmt.Errorf("AGE setup failed: %w", err)
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
