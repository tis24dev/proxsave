package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/orchestrator"
	"github.com/tis24dev/proxmox-backup/internal/tui/wizard"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

// runNewKey performs a standalone AGE recipient setup without running a backup.
func runNewKey(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger, useCLI bool) error {
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	// Derive BASE_DIR from the configuration path
	baseDir := filepath.Dir(filepath.Dir(configPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		baseDir = "/opt/proxmox-backup"
	}
	_ = os.Setenv("BASE_DIR", baseDir)

	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	if useCLI {
		return runNewKeyCLI(ctx, configPath, baseDir, bootstrap)
	}
	return runNewKeyTUI(ctx, configPath, baseDir, bootstrap)
}

func runNewKeyTUI(ctx context.Context, configPath, baseDir string, bootstrap *logging.BootstrapLogger) error {
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	sig := buildSignature()
	if strings.TrimSpace(sig) == "" {
		sig = "n/a"
	}

	// If a recipient already exists, ask for confirmation before overwriting
	if _, err := os.Stat(recipientPath); err == nil {
		confirm, err := wizard.ConfirmRecipientOverwrite(recipientPath, configPath, sig)
		if err != nil {
			return err
		}
		if !confirm {
			return wrapInstallError(errInteractiveAborted)
		}
	}

	// Run AGE setup wizard
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

	// Save the recipient
	if err := wizard.SaveAgeRecipient(recipientPath, recipientKey); err != nil {
		return fmt.Errorf("failed to save AGE recipient: %w", err)
	}

	bootstrap.Info("✓ New AGE recipient generated and saved to %s", recipientPath)
	bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")

	return nil
}

func runNewKeyCLI(ctx context.Context, configPath, baseDir string, bootstrap *logging.BootstrapLogger) error {
	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")

	cfg := &config.Config{
		BaseDir:          baseDir,
		ConfigPath:       configPath,
		EncryptArchive:   true,
		AgeRecipientFile: recipientPath,
	}

	useColor := term.IsTerminal(int(os.Stdout.Fd()))
	logger := logging.New(types.LogLevelInfo, useColor)

	orch := orchestrator.New(logger, false)
	orch.SetConfig(cfg)
	orch.SetForceNewAgeRecipient(true)

	if err := orch.EnsureAgeRecipientsReady(ctx); err != nil {
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			return wrapInstallError(errInteractiveAborted)
		}
		return fmt.Errorf("AGE setup failed: %w", err)
	}

	bootstrap.Info("✓ New AGE recipient generated and saved to %s", recipientPath)
	bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")

	return nil
}
