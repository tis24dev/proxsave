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
func runInstallTUI(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "install workflow (tui)", "config=%s", configPath)
	defer func() { done(err) }()
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	// Derive BASE_DIR from the configuration path
	baseDir := deriveBaseDirFromConfig(configPath)
	_ = os.Setenv("BASE_DIR", baseDir)

	// Before starting the TUI wizard, perform a best-effort cleanup of any existing
	// proxsave/proxmox-backup entrypoints so that the installer can recreate a clean
	// symlink for the Go binary.
	execInfo := getExecInfo()
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "cleaning legacy entrypoints")
	cleanupGlobalProxmoxBackupEntrypoints(execInfo.ExecPath, bootstrap)

	if bootstrap != nil {
		bootstrap.Info("Starting --install in TUI mode")
		bootstrap.Info("  Configuration path: %s", configPath)
		bootstrap.Info("  Base directory: %s", baseDir)
	}

	var telegramCode string
	var permStatus string
	var permMessage string

	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "ensuring interactive stdin")
	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	defer func() {
		printInstallFooter(err, configPath, baseDir, telegramCode, permStatus, permMessage)
	}()

	printInstallBanner(configPath)

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	// Check if config exists
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "checking existing configuration")
	existingAction, err := wizard.CheckExistingConfig(configPath, buildSig)
	if err != nil {
		return err
	}

	var skipConfigWizard bool
	var wizardData *wizard.InstallWizardData
	baseTemplate := ""

	switch existingAction {
	case wizard.ExistingConfigSkip:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "user skipped configuration")
		return wrapInstallError(errInteractiveAborted)
	case wizard.ExistingConfigEdit:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "editing existing configuration")
		content, readErr := os.ReadFile(configPath)
		if readErr != nil {
			return fmt.Errorf("read existing configuration: %w", readErr)
		}
		baseTemplate = string(content)
	default:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "using embedded template")
		// Overwrite: use embedded template (handled as empty base)
	}

	if !skipConfigWizard {
		// Run the wizard
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "running install wizard")
		wizardData, err = wizard.RunInstallWizard(ctx, configPath, baseDir, buildSig)
		if err != nil {
			if errors.Is(err, wizard.ErrInstallCancelled) {
				return wrapInstallError(errInteractiveAborted)
			} else {
				return fmt.Errorf("wizard failed: %w", err)
			}
		}

		// Apply collected data to template
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "applying wizard data")
		template, err := wizard.ApplyInstallData(baseTemplate, wizardData)
		if err != nil {
			return err
		}

		// Write configuration file
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "writing configuration")
		tmpConfigPath := configPath + ".tmp"
		defer func() {
			if _, err := os.Stat(tmpConfigPath); err == nil {
				_ = os.Remove(tmpConfigPath)
			}
		}()

		if err := writeConfigFile(configPath, tmpConfigPath, template); err != nil {
			return err
		}

		bootstrap.Debug("Configuration saved at %s", configPath)
	}

	// Install support docs
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "installing support docs")
	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		return fmt.Errorf("install documentation: %w", err)
	}

	// Run encryption setup if enabled (only if wizard was run)
	if !skipConfigWizard && wizardData != nil && wizardData.EnableEncryption {
		if bootstrap != nil {
			bootstrap.Info("Running initial encryption setup (AGE recipients)")
		}
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "running AGE setup wizard")
		recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
		ageData, err := wizard.RunAgeSetupWizard(ctx, recipientPath, configPath, buildSig)
		if err != nil {
			if errors.Is(err, wizard.ErrAgeSetupCancelled) {
				return fmt.Errorf("encryption setup aborted by user: %w", errInteractiveAborted)
			} else {
				return fmt.Errorf("AGE setup failed: %w", err)
			}
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
				return fmt.Errorf("failed to derive recipient from passphrase: %w", err)
			}
			recipientKey = recipient
		case "privatekey":
			// Derive recipient from private key
			recipient, err := deriveRecipientFromPrivateKey(ageData.PrivateKey)
			if err != nil {
				return fmt.Errorf("failed to derive recipient from private key: %w", err)
			}
			recipientKey = recipient
		}

		// Save the recipient
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "saving AGE recipient")
		if err := wizard.SaveAgeRecipient(recipientPath, recipientKey); err != nil {
			return fmt.Errorf("failed to save AGE recipient: %w", err)
		}

		bootstrap.Info("AGE encryption configured successfully")
		bootstrap.Info("Recipient saved to: %s", recipientPath)
		bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")
	}

	// Clean up legacy bash-based symlinks
	if bootstrap != nil {
		bootstrap.Info("Cleaning up legacy bash-based symlinks (if present)")
	}
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "cleaning legacy bash symlinks")
	cleanupLegacyBashSymlinks(baseDir, bootstrap)

	// Ensure proxsave/proxmox-backup entrypoints point to this Go binary
	if bootstrap != nil {
		bootstrap.Info("Ensuring 'proxsave' and 'proxmox-backup' commands point to the Go binary")
	}
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "ensuring go symlink")
	ensureGoSymlink(execInfo.ExecPath, bootstrap)

	// Migrate legacy cron entries
	cronSchedule := resolveCronSchedule(wizardData)
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "migrating cron entries")
	migrateLegacyCronEntries(ctx, baseDir, execInfo.ExecPath, bootstrap, cronSchedule)

	// Attempt to resolve or create a server identity for Telegram pairing
	if info, err := identity.Detect(baseDir, nil); err == nil {
		if code := info.ServerID; code != "" {
			telegramCode = code
		}
	}
	if telegramCode != "" {
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "telegram identity detected")
	} else {
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "telegram identity not found")
	}

	// Best-effort post-install permission and ownership normalization so that
	// the environment starts in a consistent state.
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "normalizing permissions")
	permStatus, permMessage = fixPermissionsAfterInstall(ctx, configPath, baseDir, bootstrap)
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "permissions status=%s", permStatus)

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
