package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui/wizard"
	"github.com/tis24dev/proxsave/internal/ui/flows/agesetup"
	"github.com/tis24dev/proxsave/internal/ui/shell"
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

	// Derive BASE_DIR from the installed executable path.
	baseDir, _ := detectedBaseDirOrFallback()
	_ = os.Setenv("BASE_DIR", baseDir)

	// Entrypoint cleanup + recreation is deferred to runPostInstallSymlinksAndCron
	// (success path only), so an aborted/non-interactive install never leaves the
	// host without a working proxsave/proxmox-backup command.
	execInfo := getExecInfo()

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
	existingAction, err := wizard.CheckExistingConfig(ctx, configPath, buildSig)
	if err != nil {
		return err
	}

	var skipConfigWizard bool
	var wizardData *wizard.InstallWizardData
	baseTemplate := ""

	switch existingAction {
	case wizard.ExistingConfigCancel:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "user cancelled installation")
		return wrapInstallError(errInteractiveAborted)
	case wizard.ExistingConfigKeepContinue:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "using existing configuration and skipping wizard")
		skipConfigWizard = true
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
		wizardData, err = wizard.RunInstallWizard(ctx, configPath, baseDir, buildSig, baseTemplate)
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

		if bootstrap != nil {
			bootstrap.Debug("Configuration saved at %s", configPath)
		}
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
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "running AGE setup via orchestrator")
		setupResult, err := runInstallAgeSetup(ctx, configPath, buildSig)
		if err != nil {
			return err
		}

		if bootstrap != nil {
			bootstrap.Info("AGE encryption configured successfully")
			if setupResult.WroteRecipientFile && setupResult.RecipientPath != "" {
				bootstrap.Info("Recipient saved to: %s", setupResult.RecipientPath)
			} else if setupResult.ReusedExistingRecipients {
				bootstrap.Info("Using existing AGE recipient configuration")
			}
			bootstrap.Info("IMPORTANT: Keep your passphrase/private key offline and secure!")
		}
	}

	// Optional post-install audit: run a dry-run and offer to disable unused collectors
	// based on actionable warning hints like "set BACKUP_*=false to disable".
	if !skipConfigWizard {
		auditRes, auditErr := wizard.RunPostInstallAuditWizard(ctx, execInfo.ExecPath, configPath, buildSig)
		if bootstrap != nil {
			if auditErr != nil {
				bootstrap.Warning("Post-install check failed (non-blocking): %v", auditErr)
			} else {
				switch {
				case !auditRes.Ran:
					bootstrap.Info("Post-install audit: skipped by user")
				case auditRes.CollectErr != nil:
					bootstrap.Warning("Post-install audit failed (non-blocking): %v", auditRes.CollectErr)
				case len(auditRes.Suggestions) == 0:
					bootstrap.Info("Post-install audit: no unused components detected")
				default:
					keys := make([]string, 0, len(auditRes.Suggestions))
					for _, s := range auditRes.Suggestions {
						keys = append(keys, s.Key)
					}
					bootstrap.Debug("Post-install audit: suggested disables (%d): %s", len(keys), strings.Join(keys, ", "))
					if len(auditRes.AppliedKeys) > 0 {
						bootstrap.Info("Post-install audit: disabled %d of %d unused component(s)", len(auditRes.AppliedKeys), len(keys))
						bootstrap.Debug("Post-install audit: disabled keys: %s", strings.Join(auditRes.AppliedKeys, ", "))
					} else {
						bootstrap.Info("Post-install audit: %d unused component(s) detected, none disabled", len(keys))
					}
				}
			}
		}
	}

	// Telegram setup (centralized bot): guide the user through pairing with an
	// explicit verification step (retry + skip). Eligibility is decided solely by
	// RunTelegramSetupWizard (BuildTelegramSetupBootstrap reads the written
	// TELEGRAM_ENABLED/mode), the same single source of truth the CLI uses — it
	// returns Shown=false without any UI when Telegram is not centrally enabled.
	if !skipConfigWizard {
		telegramRes, telegramErr := wizard.RunTelegramSetupWizard(ctx, baseDir, configPath, buildSig)
		if telegramErr != nil && bootstrap != nil {
			bootstrap.Warning("Telegram setup failed (non-blocking): %v", telegramErr)
		}
		if bootstrap != nil && telegramErr == nil {
			logTelegramSetupBootstrapOutcome(bootstrap, telegramRes.TelegramSetupBootstrap)
		}
		if bootstrap != nil && telegramRes.Shown {
			if telegramRes.Verified {
				bootstrap.Info("Telegram setup: verified (code=%d)", telegramRes.LastStatusCode)
			} else if telegramRes.SkippedVerification {
				bootstrap.Info("Telegram setup: verification skipped by user")
			} else if telegramRes.CheckAttempts > 0 {
				bootstrap.Info("Telegram setup: not verified (attempts=%d last=%d %s)", telegramRes.CheckAttempts, telegramRes.LastStatusCode, telegramRes.LastStatusMessage)
			} else {
				bootstrap.Info("Telegram setup: not verified (no check performed)")
			}
		}
	}

	// Finalize legacy-symlink cleanup, entrypoint cleanup/recreation, and cron via the
	// shared post-install engine (the same runPostInstallSymlinksAndCron the CLI uses),
	// so TUI and CLI behave identically here.
	wizardCronSchedule := ""
	if wizardData != nil {
		wizardCronSchedule = cronutil.TimeToSchedule(wizardData.CronTime)
	}
	cronSchedule := buildInstallCronSchedule(skipConfigWizard, wizardCronSchedule)
	runPostInstallSymlinksAndCron(ctx, baseDir, execInfo, bootstrap, cronSchedule)

	// Attempt to resolve or create a server identity for Telegram pairing
	if info, err := identity.DetectWithContext(ctx, baseDir, nil); err == nil {
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

// runInstallAgeSetup runs the AGE recipient setup on its own Charm session
// (the tview install wizard has already released the terminal at this
// point). The session is closed before the bootstrap success lines print.
func runInstallAgeSetup(ctx context.Context, configPath, buildSig string) (*encryptionSetupResult, error) {
	session := newAgeSetupSession(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   "AGE Encryption Setup",
		ConfigPath: configPath,
		BuildSig:   buildSig,
		UseColor:   true,
	})
	defer func() { _ = session.Close() }()

	result, err := runInitialEncryptionSetupWithUI(ctx, configPath, agesetup.New(session))
	closeErr := session.Close()
	if err != nil {
		if errors.Is(err, shell.ErrClosed) && closeErr == nil {
			return nil, wrapInstallError(errInteractiveAborted)
		}
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return result, nil
}
