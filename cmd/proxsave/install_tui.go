package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/flows/agesetup"
	flowinstall "github.com/tis24dev/proxsave/internal/ui/flows/install"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// runInstallTUI runs the installation wizard on the Charm UI: one long-lived
// Session drives every interactive step (existing-config decision, config
// wizard, AGE setup, post-install audit, Telegram pairing) over the same
// installer engine helpers the CLI uses. The step order mirrors runInstall.
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

	if !dashboardHandoffPending() {
		// Plain-terminal banner for a direct --install run. Coming from
		// the dashboard the alternate screen is still up (the session is
		// adopted below): printing here would inject the banner into the
		// UI, and the frame footer already shows config path and build.
		printInstallBanner(configPath)
	}

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	session := newAgeSetupSession(ctx, shell.Config{
		AppName:    "ProxSave",
		Subtitle:   "Install Wizard",
		ConfigPath: configPath,
		BuildSig:   buildSig,
		UseColor:   true,
	})
	// Bootstrap console prints would land in the alternate screen and
	// corrupt it: quiet them for the session lifetime (mirror/log records
	// keep working). Defers run LIFO: the session closes first.
	bootstrap.SetConsoleQuiet(true)
	defer bootstrap.SetConsoleQuiet(false)
	// Deferred for panic safety; Close is idempotent for the normal path
	// (closed explicitly before the non-interactive finalization below).
	defer func() { _ = session.Close() }()
	mapUIDeath := func(stepErr error) error {
		if errors.Is(stepErr, shell.ErrClosed) {
			if closeErr := session.Close(); closeErr == nil {
				return wrapInstallError(errInteractiveAborted)
			}
		}
		return stepErr
	}

	// Check if config exists
	logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "checking existing configuration")
	existingAction, err := flowinstall.ResolveExistingConfig(ctx, session, configPath)
	if err != nil {
		if errors.Is(err, installer.ErrInstallCancelled) {
			return wrapInstallError(errInteractiveAborted)
		}
		return mapUIDeath(err)
	}

	var skipConfigWizard bool
	var wizardData *installer.InstallWizardData
	baseTemplate := ""

	switch existingAction {
	case installer.ExistingConfigCancel:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "user cancelled installation")
		return wrapInstallError(errInteractiveAborted)
	case installer.ExistingConfigKeepContinue:
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "using existing configuration and skipping wizard")
		skipConfigWizard = true
	case installer.ExistingConfigEdit:
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
		wizardData, err = flowinstall.CollectWizardData(ctx, session, baseTemplate)
		if err != nil {
			if errors.Is(err, installer.ErrInstallCancelled) {
				return wrapInstallError(errInteractiveAborted)
			}
			if mapped := mapUIDeath(err); errors.Is(mapped, errInteractiveAborted) {
				return mapped
			}
			return fmt.Errorf("wizard failed: %w", err)
		}

		// Apply collected data to template
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "applying wizard data")
		template, err := installer.ApplyInstallData(baseTemplate, wizardData)
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
		setupResult, err := runInitialEncryptionSetupWithUI(ctx, configPath, agesetup.New(session))
		if err != nil {
			return mapUIDeath(err)
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
		// The bootstrap lines above land in the altscreen and vanish on
		// Close: show the outcome where the user can actually read it.
		ageMsg := "AGE encryption configured successfully."
		if setupResult.WroteRecipientFile && setupResult.RecipientPath != "" {
			ageMsg += "\nRecipient saved to:\n" + setupResult.RecipientPath
		} else if setupResult.ReusedExistingRecipients {
			ageMsg += "\nUsing the existing AGE recipient configuration."
		}
		ageMsg += "\n\nIMPORTANT: keep your passphrase/private key offline and secure!"
		_, _ = shell.Ask(ctx, session, components.NewNotice(components.NoticeSuccess, "Encryption ready", ageMsg))
	}

	// Optional post-install audit: run a dry-run and offer to disable unused collectors
	// based on actionable warning hints like "set BACKUP_*=false to disable".
	if !skipConfigWizard {
		auditRes, auditErr := flowinstall.RunPostInstallAudit(ctx, session, execInfo.ExecPath, configPath, false)
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
	// RunTelegramSetup (BuildTelegramSetupBootstrap reads the written
	// TELEGRAM_ENABLED/mode), the same single source of truth the CLI uses — it
	// returns Shown=false without any UI when Telegram is not centrally enabled.
	if !skipConfigWizard {
		telegramRes, telegramErr := flowinstall.RunTelegramSetup(ctx, session, baseDir, configPath, false)
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

		// Self-mode healthchecks: collect the ping URLs BEFORE the healthcheck
		// bootstrap re-reads the config (ordering invariant - eligibility keys off the
		// written HEALTHCHECK_ALIVE_URL). Only when self was chosen in the wizard.
		if wizardData != nil && wizardData.HealthcheckMode == "self" {
			logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "healthcheck self params")
			if hcParamsErr := flowinstall.RunHealthcheckSelfParams(ctx, session, baseDir, configPath); hcParamsErr != nil {
				if errors.Is(hcParamsErr, installer.ErrInstallCancelled) {
					return wrapInstallError(errInteractiveAborted)
				}
				if mapped := mapUIDeath(hcParamsErr); errors.Is(mapped, errInteractiveAborted) {
					return mapped
				}
				if bootstrap != nil {
					bootstrap.Warning("Healthcheck self params failed (non-blocking): %v", hcParamsErr)
				}
			}
		}

		// Healthchecks setup: when the daemon engine with monitoring was chosen, guide
		// the user + (centralized) show the portal magic-link + a connection check, or
		// (self) verify the pasted alive URL is reachable. Eligibility is decided solely
		// by RunHealthcheckSetup (re-reads the written config); Shown=false with no UI otherwise.
		hcRes, hcErr := flowinstall.RunHealthcheckSetup(ctx, session, baseDir, configPath, false)
		if hcErr != nil && bootstrap != nil {
			bootstrap.Warning("Healthcheck setup failed (non-blocking): %v", hcErr)
		}
		if bootstrap != nil && hcErr == nil && hcRes.Shown {
			if hcRes.Verified {
				bootstrap.Info("Healthcheck setup: verified")
			} else if hcRes.SkippedVerification {
				bootstrap.Info("Healthcheck setup: check skipped by user")
			} else if hcRes.CheckAttempts > 0 {
				bootstrap.Info("Healthcheck setup: not verified (attempts=%d)", hcRes.CheckAttempts)
			} else {
				bootstrap.Info("Healthcheck setup: not verified (no check performed)")
			}
		}
	}

	// All interactive steps are done. Unlike the CLI, the TUI keeps the ALTSCREEN
	// session OPEN and streams the non-interactive finalization INSIDE the graphics
	// via a CONTAINED, scrollable, COLORED viewport panel (components.RunStreamTask):
	// the same shared engine helpers run, but their [ts] LEVEL log lines are
	// captured (logging.CaptureConsoleWithColor) and appended to the viewport
	// instead of landing raw on the alternate screen, so scrolling stays within the
	// box. The session is closed only after the user presses Continue, so the
	// deferred footer still prints to the persistent scrollback exactly like the CLI.
	wizardCronSchedule := ""
	if wizardData != nil {
		wizardCronSchedule = cronutil.TimeToSchedule(wizardData.CronTime)
	}
	cronSchedule := buildInstallCronSchedule(skipConfigWizard, wizardCronSchedule)
	wizardMode := ""
	if wizardData != nil {
		wizardMode = wizardData.SchedulerMode
	}

	streamErr := components.RunStreamTask(ctx, session, "Finalizing installation",
		func(taskCtx context.Context, emit func(line string)) (string, error) {
			// Route the loggers AND raw os.Stdout (fmt.Println spacers) through one
			// pipe into the panel; restored on return/panic. So the panel shows the
			// same colored lines + blank section spacers as the CLI. (captureRunOutput
			// is defined in backup_stream.go.)
			defer captureRunOutput(bootstrap, emit)()

			// Finalize legacy-symlink cleanup, entrypoint cleanup/recreation, and cron
			// via the shared post-install engine (the same runPostInstallSymlinksAndCron
			// the CLI uses), so TUI and CLI behave identically here.
			runPostInstallSymlinksAndCron(taskCtx, baseDir, execInfo, bootstrap, cronSchedule)
			rv, verified := reconcileSchedulerAfterInstall(taskCtx, wizardMode, configPath, execInfo, bootstrap)

			// Attempt to resolve or create a server identity for Telegram pairing.
			if info, idErr := identity.DetectWithContext(taskCtx, baseDir, nil); idErr == nil {
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
			// the environment starts in a consistent state. Its temporary logger writes
			// to os.Stdout, which captureRunOutput has redirected into the panel, so pass
			// nil (no explicit sink needed).
			logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "normalizing permissions")
			permStatus, permMessage = fixPermissionsAfterInstall(taskCtx, configPath, baseDir, bootstrap, nil)
			logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "permissions status=%s", permStatus)

			return buildInstallOutcomePrompt(rv, verified, permStatus, permMessage), nil
		})
	if streamErr != nil {
		// Finalization is best-effort (the CLI ignores these errors too): an abort
		// or UI-death here never fails the install, so only trace it.
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "finalization stream: %v", streamErr)
	}

	// The user pressed Continue: release the terminal so the deferred footer
	// prints to the plain scrollback (the in-graphics viewport/outcome vanish with
	// the alternate screen, matching the AGE notice behavior).
	if closeErr := session.Close(); closeErr != nil && err == nil {
		logging.DebugStepBootstrap(bootstrap, "install workflow (tui)", "session close: %v", closeErr)
	}

	return nil
}

// confirmNewInstallCharm confirms the --new-install reset on its own Charm
// session (created and closed around the single prompt).
func confirmNewInstallCharm(ctx context.Context, baseDir, buildSig string, preservedEntries []string) (bool, error) {
	session := newAgeSetupSession(ctx, shell.Config{
		AppName:  "ProxSave",
		Subtitle: "New Install",
		BuildSig: buildSig,
		UseColor: true,
	})
	defer func() { _ = session.Close() }()
	ok, err := flowinstall.ConfirmNewInstall(ctx, session, baseDir, preservedEntries)
	closeErr := session.Close()
	if err != nil {
		if errors.Is(err, shell.ErrClosed) && closeErr == nil {
			return false, nil
		}
		return false, err
	}
	return ok, nil
}
