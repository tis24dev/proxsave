package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/tui/wizard"
)

// runNewKey performs a standalone AGE recipient setup without running a backup.
func runNewKey(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger) error {
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
