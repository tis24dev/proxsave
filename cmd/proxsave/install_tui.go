package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui/wizard"
)

// runInstallTUI runs the TUI-based installation wizard
func runInstallTUI(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger) error {
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	// Derive BASE_DIR from the configuration path
	baseDir := filepath.Dir(filepath.Dir(configPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		baseDir = "/opt/proxsave"
	}
	_ = os.Setenv("BASE_DIR", baseDir)

	// Before starting the TUI wizard, perform a best-effort cleanup of any existing
	// proxsave/proxmox-backup entrypoints so that the installer can recreate a clean
	// symlink for the Go binary.
	execInfo := getExecInfo()
	cleanupGlobalProxmoxBackupEntrypoints(execInfo.ExecPath, bootstrap)

	if bootstrap != nil {
		bootstrap.Info("Starting --install in TUI mode")
		bootstrap.Info("  Configuration path: %s", configPath)
		bootstrap.Info("  Base directory: %s", baseDir)
	}

	var telegramCode string
	var installErr error

	if err := ensureInteractiveStdin(); err != nil {
		installErr = err
		return installErr
	}

	defer func() {
		printInstallFooter(installErr, configPath, baseDir, telegramCode)
	}()

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	// Check if config exists
	existingAction, err := wizard.CheckExistingConfig(configPath, buildSig)
	if err != nil {
		installErr = err
		return installErr
	}

	var skipConfigWizard bool
	var wizardData *wizard.InstallWizardData
	baseTemplate := ""

	switch existingAction {
	case wizard.ExistingConfigSkip:
		installErr = wrapInstallError(errInteractiveAborted)
		return installErr
	case wizard.ExistingConfigEdit:
		content, readErr := os.ReadFile(configPath)
		if readErr != nil {
			installErr = fmt.Errorf("read existing configuration: %w", readErr)
			return installErr
		}
		baseTemplate = string(content)
	default:
		// Overwrite: use embedded template (handled as empty base)
	}

	if !skipConfigWizard {
		// Run the wizard
		wizardData, err = wizard.RunInstallWizard(ctx, configPath, baseDir, buildSig)
		if err != nil {
			if errors.Is(err, wizard.ErrInstallCancelled) {
				installErr = wrapInstallError(errInteractiveAborted)
			} else {
				installErr = fmt.Errorf("wizard failed: %w", err)
			}
			return installErr
		}

		// Apply collected data to template
		template, err := wizard.ApplyInstallData(baseTemplate, wizardData)
		if err != nil {
			installErr = err
			return installErr
		}

		// Write configuration file
		tmpConfigPath := configPath + ".tmp"
		defer func() {
			if _, err := os.Stat(tmpConfigPath); err == nil {
				_ = os.Remove(tmpConfigPath)
			}
		}()

		if err := writeConfigFile(configPath, tmpConfigPath, template); err != nil {
			installErr = err
			return installErr
		}

		bootstrap.Debug("Configuration saved at %s", configPath)
	}

	// Install support docs
	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		installErr = fmt.Errorf("install documentation: %w", err)
		return installErr
	}

	// Run encryption setup if enabled (only if wizard was run)
	if !skipConfigWizard && wizardData != nil && wizardData.EnableEncryption {
		if bootstrap != nil {
			bootstrap.Info("Running initial encryption setup (AGE recipients)")
		}
		recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
		ageData, err := wizard.RunAgeSetupWizard(ctx, recipientPath, configPath, buildSig)
		if err != nil {
			if errors.Is(err, wizard.ErrAgeSetupCancelled) {
				installErr = fmt.Errorf("encryption setup aborted by user: %w", errInteractiveAborted)
			} else {
				installErr = fmt.Errorf("AGE setup failed: %w", err)
			}
			return installErr
		}

		// Process the AGE data based on setup type
		var recipientKey string
		switch ageData.SetupType {
		case "existing":
			recipientKey = ageData.PublicKey
		case "passphrase":
			// Derive recipient from passphrase
			recipient, err := deriveRecipientFromPassphrase(ageData.Passphrase)
			if err != nil {
				installErr = fmt.Errorf("failed to derive recipient from passphrase: %w", err)
				return installErr
			}
			recipientKey = recipient
		case "privatekey":
			// Derive recipient from private key
			recipient, err := deriveRecipientFromPrivateKey(ageData.PrivateKey)
			if err != nil {
				installErr = fmt.Errorf("failed to derive recipient from private key: %w", err)
				return installErr
			}
			recipientKey = recipient
		}

		// Save the recipient
		if err := wizard.SaveAgeRecipient(recipientPath, recipientKey); err != nil {
			installErr = fmt.Errorf("failed to save AGE recipient: %w", err)
			return installErr
		}

		bootstrap.Info("AGE encryption configured successfully")
		bootstrap.Info("Recipient saved to: %s", recipientPath)
		bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")
	}

	// Clean up legacy bash-based symlinks
	if bootstrap != nil {
		bootstrap.Info("Cleaning up legacy bash-based symlinks (if present)")
	}
	cleanupLegacyBashSymlinks(baseDir, bootstrap)

	// Ensure proxsave/proxmox-backup entrypoints point to this Go binary
	if bootstrap != nil {
		bootstrap.Info("Ensuring 'proxsave' and 'proxmox-backup' commands point to the Go binary")
	}
	ensureGoSymlink(execInfo.ExecPath, bootstrap)

	// Migrate legacy cron entries
	cronSchedule := resolveCronSchedule(wizardData)
	migrateLegacyCronEntries(ctx, baseDir, execInfo.ExecPath, bootstrap, cronSchedule)

	// Attempt to resolve or create a server identity for Telegram pairing
	if info, err := identity.Detect(baseDir, nil); err == nil {
		if code := info.ServerID; code != "" {
			telegramCode = code
		}
	}

	installErr = nil
	return nil
}

// deriveRecipientFromPassphrase derives a deterministic AGE recipient from a passphrase
func deriveRecipientFromPassphrase(passphrase string) (string, error) {
	return orchestrator.DeriveDeterministicRecipientFromPassphrase(passphrase)
}

// deriveRecipientFromPrivateKey derives the recipient (public key) from an AGE private key
func deriveRecipientFromPrivateKey(privateKey string) (string, error) {
	privateKey = strings.TrimSpace(privateKey)
	if privateKey == "" {
		return "", fmt.Errorf("private key cannot be empty")
	}

	identity, err := age.ParseX25519Identity(privateKey)
	if err != nil {
		return "", fmt.Errorf("invalid AGE private key: %w", err)
	}

	return identity.Recipient().String(), nil
}
