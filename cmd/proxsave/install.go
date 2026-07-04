package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
	buildinfo "github.com/tis24dev/proxsave/internal/version"
)

var (
	newInstallEnsureInteractiveStdin = ensureInteractiveStdin
	newInstallConfirmCLI             = confirmNewInstallCLI
	newInstallConfirmTUI             = confirmNewInstallCharm
	newInstallRunInstall             = runInstall
	newInstallRunInstallTUI          = runInstallTUI
	configureCronTimeFunc            = configureCronTime
)

type installConfigResult struct {
	EnableEncryption bool
	SkipConfigWizard bool
	CronSchedule     string
	SchedulerMode    string // "cron" | "daemon"
}

func runInstall(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger) (err error) {
	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "resolving configuration path")
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	baseDir, _ := detectedBaseDirOrFallback()
	_ = os.Setenv("BASE_DIR", baseDir)

	done := logging.DebugStartBootstrap(bootstrap, "install workflow (cli)", "config=%s base=%s", configPath, baseDir)
	defer func() { done(err) }()

	// Entrypoint cleanup + recreation is deferred to runPostInstallSymlinksAndCron
	// (success path only), so an aborted/non-interactive install never leaves the
	// host without a working proxsave/proxmox-backup command.
	execInfo := getExecInfo()

	if bootstrap != nil {
		bootstrap.Info("Starting --install in CLI mode")
		bootstrap.Info("  Configuration path: %s", configPath)
		bootstrap.Info("  Base directory: %s", baseDir)
	}

	var telegramCode string
	var permStatus string
	var permMessage string

	defer func() {
		printInstallFooter(err, configPath, baseDir, telegramCode, permStatus, permMessage)
	}()

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "ensuring interactive stdin")
	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	tmpConfigPath := configPath + ".tmp"
	defer cleanupTempConfig(tmpConfigPath)

	reader := bufio.NewReader(os.Stdin)
	printInstallBanner(configPath)

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "running config wizard")
	configResult, err := runConfigWizardCLI(ctx, reader, configPath, tmpConfigPath, baseDir, bootstrap)
	if err != nil {
		return err
	}
	logging.DebugStepBootstrap(
		bootstrap,
		"install workflow (cli)",
		"config wizard done (encryption=%v skip=%v cron=%s)",
		configResult.EnableEncryption,
		configResult.SkipConfigWizard,
		configResult.CronSchedule,
	)

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "installing support docs")
	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		return fmt.Errorf("install documentation: %w", err)
	}

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "running encryption setup if needed")
	if err := runEncryptionSetupIfNeeded(ctx, configPath, configResult.EnableEncryption, configResult.SkipConfigWizard, bootstrap); err != nil {
		return err
	}

	// Optional post-install audit: run a dry-run and offer to disable unused collectors.
	if !configResult.SkipConfigWizard {
		logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "post-install audit")
		if err := runPostInstallAuditCLI(ctx, reader, execInfo.ExecPath, configPath, bootstrap); err != nil {
			return err
		}

		// Telegram setup (centralized bot): if enabled, guide the user through pairing
		// and allow an explicit verification step with retry + skip.
		logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "telegram setup")
		if err := runTelegramSetupCLI(ctx, reader, baseDir, configPath, bootstrap); err != nil {
			return err
		}
	}

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "finalizing symlinks and cron")
	runPostInstallSymlinksAndCron(
		ctx,
		baseDir,
		execInfo,
		bootstrap,
		buildInstallCronSchedule(configResult.SkipConfigWizard, configResult.CronSchedule),
	)
	// Reconcile the scheduler engine (daemon unit vs cron entry) as a mutually
	// exclusive choice, INCLUDING the keep-existing path (SchedulerMode empty ->
	// read from the kept config), so a re-install never leaves both active.
	reconcileSchedulerAfterInstall(ctx, configResult.SchedulerMode, configPath, execInfo, bootstrap)

	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "detecting telegram identity")
	telegramCode = detectTelegramCodeWithContext(ctx, baseDir)
	if telegramCode != "" {
		logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "telegram identity detected")
	} else {
		logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "telegram identity not found")
	}

	// Best-effort post-install permission and ownership normalization so that
	// the environment starts in a consistent state.
	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "normalizing permissions")
	permStatus, permMessage = fixPermissionsAfterInstall(ctx, configPath, baseDir, bootstrap)
	logging.DebugStepBootstrap(bootstrap, "install workflow (cli)", "permissions status=%s", permStatus)

	return nil
}

// skipOptionalInstallStepOnAbort turns a prompt error (Ctrl-D/EOF or a cancelled
// context) from an optional install step — the post-install audit or the Telegram
// setup — into a non-blocking outcome: the step is abandoned with a warning and
// the caller continues the install so the entrypoint/cron finalization still
// runs. This matches the TUI, which logs such errors as non-blocking warnings and
// never aborts the install.
func skipOptionalInstallStepOnAbort(bootstrap *logging.BootstrapLogger, step string, err error) error {
	fmt.Printf("%s skipped (input aborted, non-blocking): %v\n", step, err)
	if bootstrap != nil {
		bootstrap.Warning("%s skipped (input aborted, non-blocking): %v", step, err)
	}
	return nil
}

func runPostInstallAuditCLI(ctx context.Context, reader *bufio.Reader, execPath, configPath string, bootstrap *logging.BootstrapLogger) error {
	fmt.Println("\n--- Post-install check (optional) ---")
	run, err := promptYesNo(ctx, reader, "Run a dry-run to detect unused components and reduce warnings? [Y/n]: ", true)
	if err != nil {
		return skipOptionalInstallStepOnAbort(bootstrap, "Post-install audit", err)
	}
	if !run {
		if bootstrap != nil {
			bootstrap.Info("Post-install audit: skipped by user")
		}
		return nil
	}

	if bootstrap != nil {
		bootstrap.Info("Post-install audit: running dry-run (this may take a minute)")
	}

	suggestions, err := installer.CollectPostInstallDisableSuggestions(ctx, execPath, configPath)
	if err != nil {
		fmt.Printf("WARNING: Post-install check failed (non-blocking): %v\n", err)
		if bootstrap != nil {
			bootstrap.Warning("Post-install audit failed (non-blocking): %v", err)
		}
		return nil
	}
	if len(suggestions) == 0 {
		fmt.Println("No unused components detected. No changes required.")
		if bootstrap != nil {
			bootstrap.Info("Post-install audit: no unused components detected")
		}
		return nil
	}

	fmt.Printf("Detected %d unused/optional component(s) that may cause WARNINGs.\n", len(suggestions))
	if bootstrap != nil {
		keys := make([]string, 0, len(suggestions))
		for _, s := range suggestions {
			keys = append(keys, s.Key)
		}
		bootstrap.Debug("Post-install audit: suggested disables (%d): %s", len(keys), strings.Join(keys, ", "))
	}
	for _, s := range suggestions {
		reason := ""
		if len(s.Messages) > 0 {
			reason = strings.TrimSpace(s.Messages[0])
		}
		if reason != "" {
			fmt.Printf("  - %s: %s\n", s.Key, reason)
		} else {
			fmt.Printf("  - %s\n", s.Key)
		}
	}
	fmt.Println()

	disableAny, err := promptYesNo(ctx, reader, "Disable any of the suggested components now (set KEY=false)? [y/N]: ", false)
	if err != nil {
		return skipOptionalInstallStepOnAbort(bootstrap, "Post-install audit", err)
	}
	if !disableAny {
		fmt.Println("No changes applied. You can disable unused components later by editing backup.env.")
		if bootstrap != nil {
			bootstrap.Info("Post-install audit: no disables applied")
		}
		return nil
	}

	keys := make([]string, 0, len(suggestions))
	for _, s := range suggestions {
		disable, err := promptYesNo(ctx, reader, fmt.Sprintf("Disable %s? [y/N]: ", s.Key), false)
		if err != nil {
			return skipOptionalInstallStepOnAbort(bootstrap, "Post-install audit", err)
		}
		if disable {
			keys = append(keys, s.Key)
		}
	}
	if len(keys) == 0 {
		fmt.Println("No changes selected. Nothing was modified.")
		if bootstrap != nil {
			bootstrap.Info("Post-install audit: no disables selected")
		}
		return nil
	}

	contentBytes, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Printf("ERROR: Unable to update configuration (read failed): %v\n", err)
		if bootstrap != nil {
			bootstrap.Warning("Post-install audit: unable to update configuration (read failed): %v", err)
		}
		return nil
	}
	content := string(contentBytes)

	sort.Strings(keys)
	for _, key := range keys {
		content = setEnvValue(content, key, "false")
	}

	tmpAuditPath := configPath + ".tmp.audit"
	defer cleanupTempConfig(tmpAuditPath)
	if err := writeConfigFile(configPath, tmpAuditPath, content); err != nil {
		fmt.Printf("ERROR: Unable to update configuration (write failed): %v\n", err)
		if bootstrap != nil {
			bootstrap.Warning("Post-install audit: unable to update configuration (write failed): %v", err)
		}
		return nil
	}

	fmt.Printf("✓ Updated %s: disabled %d component(s): %s\n", configPath, len(keys), strings.Join(keys, ", "))
	if bootstrap != nil {
		bootstrap.Info("Post-install audit: disabled %d of %d unused component(s)", len(keys), len(suggestions))
		bootstrap.Debug("Post-install audit: disabled keys: %s", strings.Join(keys, ", "))
	}
	return nil
}

func runNewInstall(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger, useCLI bool) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "new-install workflow", "config=%s", configPath)
	defer func() { done(err) }()

	logging.DebugStepBootstrap(bootstrap, "new-install workflow", "ensuring interactive stdin")
	if err := newInstallEnsureInteractiveStdin(); err != nil {
		return err
	}

	logging.DebugStepBootstrap(bootstrap, "new-install workflow", "building reset plan")
	plan, err := buildNewInstallPlan(configPath)
	if err != nil {
		return err
	}

	logging.DebugStepBootstrap(bootstrap, "new-install workflow", "confirming reset")
	var confirm bool
	if useCLI {
		confirm, err = newInstallConfirmCLI(ctx, bufio.NewReader(os.Stdin), plan)
	} else {
		confirm, err = newInstallConfirmTUI(ctx, plan.BaseDir, plan.BuildSignature, plan.PreservedEntries)
	}
	if err != nil {
		return wrapInstallError(err)
	}
	if !confirm {
		return wrapInstallError(errInteractiveAborted)
	}

	if bootstrap != nil {
		bootstrap.Info("Resetting %s (preserving %s)", plan.BaseDir, formatNewInstallPreservedEntries(plan.PreservedEntries))
	}
	logging.DebugStepBootstrap(bootstrap, "new-install workflow", "resetting base dir")
	if err := resetInstallBaseDirWithContext(ctx, plan.BaseDir, bootstrap); err != nil {
		return err
	}

	if useCLI {
		return newInstallRunInstall(ctx, plan.ResolvedConfigPath, bootstrap)
	}
	return newInstallRunInstallTUI(ctx, plan.ResolvedConfigPath, bootstrap)
}

func printInstallFooter(installErr error, configPath, baseDir, telegramCode, permStatus, permMessage string) {
	colorReset := "\033[0m"

	title := "Go-based installation completed"
	color := "\033[32m" // green by default

	if installErr != nil {
		if isInstallAbortedError(installErr) {
			// User-driven abort (Ctrl+C, exit, setup aborted) -> SKIP color
			color = "\033[35m"
			title = "Go-based installation aborted"
		} else {
			// Any other error -> red
			color = "\033[31m"
			title = "Go-based installation failed"
		}
	}

	fmt.Println()
	fmt.Printf("%s================================================\n", color)
	fmt.Printf(" %s\n", title)
	fmt.Printf("================================================%s\n", colorReset)
	fmt.Println()

	if permStatus != "" {
		switch permStatus {
		case "ok":
			fmt.Printf("Permissions: %s\n", permMessage)
		case "warning":
			fmt.Printf("Permissions: WARNING (non blocking) - %s\n", permMessage)
		case "error":
			fmt.Printf("Permissions: ERROR (non blocking) - %s\n", permMessage)
		case "skipped":
			fmt.Printf("Permissions: %s\n", permMessage)
		default:
			fmt.Printf("Permissions: %s\n", permMessage)
		}
		fmt.Println()
	}

	// For user-aborted runs, stop here to avoid showing next steps/commands.
	if installErr != nil && isInstallAbortedError(installErr) {
		return
	}

	fmt.Println("Next steps:")
	fmt.Println("0. If you need, start migration from old backup.env:  proxsave --env-migration (alias: proxmox-backup --env-migration)")
	if strings.TrimSpace(configPath) != "" {
		fmt.Printf("1. Edit configuration: %s\n", configPath)
	} else {
		fmt.Println("1. Edit configuration: <configuration path unavailable>")
	}
	if strings.TrimSpace(baseDir) != "" {
		fmt.Println("2. Run first backup: proxsave")
		fmt.Printf("3. Check logs: tail -f %s/log/*.log\n", baseDir)
	} else {
		fmt.Println("2. Run first backup: proxsave")
		fmt.Println("3. Check logs: tail -f /opt/proxsave/log/*.log")
	}
	if telegramCode != "" {
		fmt.Printf("4. Telegram: Open @ProxmoxAN_bot and enter code: %s\n", telegramCode)
	} else {
		fmt.Println("4. Telegram: Open @ProxmoxAN_bot and enter your unique code")
	}
	fmt.Println()
	fmt.Println("\033[31mEXTRA STEP - IF YOU FIND THIS TOOL USEFUL AND WANT TO THANK ME, A COFFEE IS ALWAYS WELCOME!\033[0m")
	fmt.Println("https://github.com/sponsors/tis24dev")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  proxsave (alias: proxmox-backup) - Open the interactive dashboard (runs the backup directly when non-interactive, e.g. cron)")
	fmt.Println("  --backup           - Run a backup now (what bare proxsave does when non-interactive)")
	fmt.Println("  --help             - Show all options")
	fmt.Println("  --dry-run          - Test without changes")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep build/env/identity) then run installer")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (also adds missing keys to backup.env)")
	fmt.Println("  --newkey           - Generate a new encryption key for backups")
	fmt.Println("  --decrypt          - Decrypt an existing backup archive")
	fmt.Println("  --restore          - Run interactive restore workflow (select bundle, decrypt if needed, apply to system)")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println("  --support          - Run in support mode (force debug log level and send email with attached log to github-support@tis24.it); available for standard backup and --restore")
	fmt.Println()
}

func cleanupTempConfig(tmpConfigPath string) {
	if tmpConfigPath == "" {
		return
	}
	if _, err := os.Stat(tmpConfigPath); err == nil {
		_ = os.Remove(tmpConfigPath)
	}
}

func runConfigWizardCLI(ctx context.Context, reader *bufio.Reader, configPath, tmpConfigPath, baseDir string, bootstrap *logging.BootstrapLogger) (result installConfigResult, err error) {
	done := logging.DebugStartBootstrap(bootstrap, "install config wizard (cli)", "config=%s", configPath)
	defer func() { done(err) }()

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "preparing base template")
	template, skipConfigWizard, fromExisting, err := prepareBaseTemplate(ctx, reader, configPath)
	if err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}

	if skipConfigWizard {
		return installConfigResult{SkipConfigWizard: true}, nil
	}

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring secondary storage")
	if template, err = configureSecondaryStorage(ctx, reader, template); err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}
	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring cloud storage")
	if template, err = configureCloudStorage(ctx, reader, template); err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}
	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring firewall rules")
	if template, err = configureFirewallRules(ctx, reader, template); err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}
	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring notifications")
	if template, err = configureNotifications(ctx, reader, template); err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring encryption")
	result.EnableEncryption, err = configureEncryption(ctx, reader, &template)
	if err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring scheduler engine")
	engine, err := configureSchedulerEngine(ctx, reader, schedulerEngineDefault(fromExisting, template))
	if err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}
	result.SchedulerMode = engine

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "configuring run-at time")
	cronTime, err := configureCronTimeFunc(ctx, reader, cronutil.DefaultTime)
	if err != nil {
		return installConfigResult{}, wrapInstallError(err)
	}
	result.CronSchedule = cronutil.TimeToSchedule(cronTime)

	if bootstrap != nil {
		bootstrap.Info("Scheduler: %s, run at %s", engine, cronTime)
	}

	logging.DebugStepBootstrap(bootstrap, "install config wizard (cli)", "writing configuration")
	template = config.RemoveRuntimeDerivedEnvKeys(template)
	template = setEnvValue(template, "SCHEDULER_MODE", engine)
	template = setEnvValue(template, "SCHEDULER_TIME", cronTime)
	if engine == "daemon" {
		template = setEnvValue(template, "HEALTHCHECK_ENABLED", "true")
	}
	if err := writeConfigFile(configPath, tmpConfigPath, template); err != nil {
		return installConfigResult{}, err
	}

	if bootstrap != nil {
		bootstrap.Info("✓ Configuration saved at %s", configPath)
	}

	return result, nil
}

func runEncryptionSetupIfNeeded(ctx context.Context, configPath string, enableEncryption, skipConfigWizard bool, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "install encryption setup", "config=%s", configPath)
	defer func() { done(err) }()
	if skipConfigWizard || !enableEncryption {
		logging.DebugStepBootstrap(bootstrap, "install encryption setup", "skipped")
		return nil
	}

	if bootstrap != nil {
		bootstrap.Info("Running initial encryption setup (AGE recipients)")
	}

	if err := runInitialEncryptionSetup(ctx, configPath); err != nil {
		return err
	}

	return nil
}

func runPostInstallSymlinksAndCron(ctx context.Context, baseDir string, execInfo ExecInfo, bootstrap *logging.BootstrapLogger, cronSchedule string) {
	done := logging.DebugStartBootstrap(bootstrap, "post-install setup", "base=%s", baseDir)
	defer func() { done(nil) }()

	// Remove stale proxsave/proxmox-backup *symlinks* (PATH, /usr/local/bin,
	// /usr/bin) that do not point to this Go binary, then recreate clean ones. Real
	// (non-symlink) files are left in place so a package-managed /usr/bin binary is
	// never deleted. This runs here — immediately before recreation and only on the
	// success path — so an aborted or non-interactive install can never leave the
	// host without a working entrypoint.
	logging.DebugStepBootstrap(bootstrap, "post-install setup", "cleaning legacy entrypoints")
	cleanupGlobalProxmoxBackupEntrypoints(execInfo.ExecPath, bootstrap)

	// Ensure proxsave/proxmox-backup entrypoints point to this Go binary, if not already customized.
	if bootstrap != nil {
		bootstrap.Info("Ensuring the 'proxsave' command points to the Go binary")
	}
	logging.DebugStepBootstrap(bootstrap, "post-install setup", "ensuring go symlink")
	ensureGoSymlink(execInfo.ExecPath, bootstrap)

	// Ensure a cron entry for the Go binary: preserve an entry that already
	// targets it, drop outdated proxsave/proxmox-backup binary entries, and if
	// no entry exists at all create a default one at 02:00 every day.
	if strings.TrimSpace(cronSchedule) == "" {
		cronSchedule = resolveCronScheduleFromEnv()
	}
	logging.DebugStepBootstrap(bootstrap, "post-install setup", "migrating cron entries")
	migrateLegacyCronEntries(ctx, baseDir, execInfo.ExecPath, bootstrap, cronSchedule)
}

func detectTelegramCodeWithContext(ctx context.Context, baseDir string) string {
	info, err := identity.DetectWithContext(ctx, baseDir, nil)
	if err != nil {
		return ""
	}
	code := strings.TrimSpace(info.ServerID)
	return code
}

func resetInstallBaseDir(baseDir string, bootstrap *logging.BootstrapLogger) (err error) {
	return resetInstallBaseDirWithContext(context.Background(), baseDir, bootstrap)
}

func resetInstallBaseDirWithContext(ctx context.Context, baseDir string, bootstrap *logging.BootstrapLogger) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	done := logging.DebugStartBootstrap(bootstrap, "reset install base", "base=%s", baseDir)
	defer func() { done(err) }()
	baseDir = filepath.Clean(baseDir)
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		return fmt.Errorf("refusing to reset unsafe base directory: %q", baseDir)
	}

	if err := os.MkdirAll(baseDir, 0o750); err != nil {
		return fmt.Errorf("failed to create base directory %s: %w", baseDir, err)
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("failed to list base directory %s: %w", baseDir, err)
	}

	preserve := newInstallPreserveSet()

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if _, keep := preserve[name]; keep {
			logBootstrapInfo(bootstrap, "Preserving %s", filepath.Join(baseDir, name))
			continue
		}
		target := filepath.Join(baseDir, name)
		logging.DebugStepBootstrap(bootstrap, "reset install base", "removing %s", target)
		if err := clearImmutableAttributesWithContext(ctx, target, bootstrap); err != nil {
			return err
		}
		// Best-effort: ensure write permission before removal
		if entry.IsDir() {
			// 0700 is the minimum that lets the owner traverse and delete the
			// directory's contents in the os.RemoveAll below: a directory needs the
			// execute bit, so gosec G302's file-oriented <=0600 ceiling does not apply.
			// Owner-only (group and others have no access), and the mode is transient -
			// the directory is removed on the very next line.
			// #nosec G302 -- transient 0700 on a directory about to be removed; owner-only.
			_ = os.Chmod(target, 0o700)
		} else {
			_ = os.Chmod(target, 0o600)
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("failed to remove %s: %w", target, err)
		}
		logBootstrapInfo(bootstrap, "Removed %s", target)
	}

	return nil
}

func printInstallBanner(configPath string) {
	fmt.Println("===========================================")
	fmt.Println("  ProxSave - Go Version")
	fmt.Printf("  Version: %s\n", buildinfo.String())
	sig := buildSignature()
	if strings.TrimSpace(sig) == "" {
		sig = "n/a"
	}
	fmt.Printf("  Build Signature: %s\n", sig)
	fmt.Println("  Mode: Install Wizard")
	fmt.Println("===========================================")
	fmt.Printf("Configuration file: %s\n\n", configPath)
}

func prepareBaseTemplate(ctx context.Context, reader *bufio.Reader, configPath string) (string, bool, bool, error) {
	decision, err := prepareExistingConfigDecisionCLI(ctx, reader, configPath)
	if err != nil {
		return "", false, false, err
	}
	if decision.AbortInstall {
		return "", false, false, errInteractiveAborted
	}
	if decision.SkipConfigWizard {
		fmt.Println("Existing configuration detected, keeping current backup.env and skipping configuration wizard.")
		return "", true, false, nil
	}
	return decision.BaseTemplate, false, decision.FromExistingFile, nil
}

func configureSecondaryStorage(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Secondary storage ---")
	fmt.Println("Configure an additional local path for redundant copies.")
	fmt.Println("IMPORTANT: Secondary path must be a filesystem-mounted directory (e.g., /mnt/nas-backup)")
	fmt.Println("Network shares must be mounted BEFORE running this backup tool.")
	fmt.Println("For direct network access without mounting, use cloud storage (rclone) instead.")
	fmt.Println("(You can change these settings later in backup.env)")
	prefill := installer.DeriveInstallWizardPrefill(template)
	enableSecondary, err := confirmDefault(ctx, reader, "Enable secondary backup path?", prefill.SecondaryEnabled)
	if err != nil {
		return "", err
	}
	if enableSecondary {
		var secondaryPath string
		for {
			secondaryPath, err = promptNonEmptyWithDefault(ctx, reader, "Secondary backup path (SECONDARY_PATH): ", prefill.SecondaryPath)
			if err != nil {
				return "", err
			}
			secondaryPath = sanitizeEnvValue(secondaryPath)
			if err := config.ValidateRequiredSecondaryPath(secondaryPath); err != nil {
				fmt.Printf("%v\n", err)
				continue
			}
			break
		}
		var secondaryLog string
		for {
			secondaryLog, err = promptOptionalWithDefault(ctx, reader, "Secondary log path (SECONDARY_LOG_PATH, optional - press Enter to skip): ", prefill.SecondaryLogPath)
			if err != nil {
				return "", err
			}
			secondaryLog = sanitizeEnvValue(secondaryLog)
			if err := config.ValidateOptionalSecondaryLogPath(secondaryLog); err != nil {
				fmt.Printf("%v\n", err)
				continue
			}
			break
		}
		template = config.ApplySecondaryStorageSettings(template, true, secondaryPath, secondaryLog)
	} else {
		template = config.ApplySecondaryStorageSettings(template, false, "", "")
	}
	return template, nil
}

func configureCloudStorage(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Cloud storage (rclone) ---")
	fmt.Println("Remember to configure rclone manually before enabling cloud backups.")
	prefill := installer.DeriveInstallWizardPrefill(template)
	enableCloud, err := confirmDefault(ctx, reader, "Enable cloud backups?", prefill.CloudEnabled)
	if err != nil {
		return "", err
	}
	if enableCloud {
		remote, err := promptNonEmptyWithDefault(ctx, reader, "Rclone remote for backups (e.g. myremote:pbs-backups): ", prefill.CloudRemote)
		if err != nil {
			return "", err
		}
		remote = sanitizeEnvValue(remote)
		logRemote, err := promptNonEmptyWithDefault(ctx, reader, "Rclone remote for logs (e.g. myremote:/logs): ", prefill.CloudLogPath)
		if err != nil {
			return "", err
		}
		logRemote = sanitizeEnvValue(logRemote)
		template = setEnvValue(template, "CLOUD_ENABLED", "true")
		template = setEnvValue(template, "CLOUD_REMOTE", remote)
		template = setEnvValue(template, "CLOUD_LOG_PATH", logRemote)
	} else {
		template = setEnvValue(template, "CLOUD_ENABLED", "false")
		template = setEnvValue(template, "CLOUD_REMOTE", "")
		template = setEnvValue(template, "CLOUD_LOG_PATH", "")
	}
	return template, nil
}

func configureFirewallRules(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Firewall rules ---")
	fmt.Println("Enable collection of firewall rules (e.g., iptables/nftables).")
	fmt.Println("(You can change this later in backup.env via BACKUP_FIREWALL_RULES)")
	enable, err := confirmDefault(ctx, reader, "Backup firewall rules?", installer.DeriveInstallWizardPrefill(template).FirewallEnabled)
	if err != nil {
		return "", err
	}
	if enable {
		template = setEnvValue(template, "BACKUP_FIREWALL_RULES", "true")
	} else {
		template = setEnvValue(template, "BACKUP_FIREWALL_RULES", "false")
	}
	return template, nil
}

func configureNotifications(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	prefill := installer.DeriveInstallWizardPrefill(template)
	fmt.Println("\n--- Telegram ---")
	enableTelegram, err := confirmDefault(ctx, reader, "Enable Telegram notifications (centralized)?", prefill.TelegramEnabled)
	if err != nil {
		return "", err
	}
	if enableTelegram {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "true")
		// Preserve a stored bot mode (e.g. personal); only seed the centralized
		// default when none is set yet, mirroring the TUI's ApplyInstallData.
		if strings.TrimSpace(prefill.TelegramType) == "" {
			template = setEnvValue(template, "BOT_TELEGRAM_TYPE", "centralized")
		}
	} else {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "false")
	}

	fmt.Println("\n--- Email ---")
	fmt.Println("Default email delivery uses the ProxSave Cloud Relay, with local sendmail as failover.")
	fmt.Println("ProxSave does not collect raw SMTP settings; choose pmf only when Proxmox Notifications is configured.")
	enableEmail, err := confirmDefault(ctx, reader, "Enable email notifications?", prefill.EmailEnabled)
	if err != nil {
		return "", err
	}
	if enableEmail {
		method, err := promptEmailDeliveryMethod(ctx, reader, prefill.EmailDeliveryMethod)
		if err != nil {
			return "", err
		}
		template = setEnvValue(template, "EMAIL_ENABLED", "true")
		template = setEnvValue(template, "EMAIL_DELIVERY_METHOD", method)
		template = unsetEnvValue(template, "EMAIL_FALLBACK_PMF")
		template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", "true")
	} else {
		template = setEnvValue(template, "EMAIL_ENABLED", "false")
	}
	return template, nil
}

func promptEmailDeliveryMethod(ctx context.Context, reader *bufio.Reader, defaultMethod string) (string, error) {
	defaultMethod = config.NormalizeEmailDeliveryMethod(defaultMethod)
	if defaultMethod != "relay" && defaultMethod != "sendmail" && defaultMethod != "pmf" {
		defaultMethod = "relay"
	}

	fmt.Println("Email delivery methods:")
	fmt.Println("  relay    ProxSave Cloud Relay over outbound HTTPS (default)")
	fmt.Println("  sendmail Local /usr/sbin/sendmail (fallback/default failover; requires a local MTA)")
	fmt.Println("  pmf      Proxmox Notifications via proxmox-mail-forward (SMTP lives in Proxmox)")
	for {
		resp, err := promptOptional(ctx, reader, fmt.Sprintf("Email delivery method [%s]: ", defaultMethod))
		if err != nil {
			return "", err
		}
		method := defaultMethod
		if strings.TrimSpace(resp) != "" {
			method = config.NormalizeEmailDeliveryMethod(resp)
		}
		switch method {
		case "pmf", "relay", "sendmail":
			return method, nil
		default:
			fmt.Println("Please enter 'pmf', 'relay', or 'sendmail'. Aliases like 'proxmox-notifications' are accepted for pmf.")
		}
	}
}

func configureEncryption(ctx context.Context, reader *bufio.Reader, template *string) (bool, error) {
	fmt.Println("\n--- Encryption ---")
	enableEncryption, err := confirmDefault(ctx, reader, "Enable backup encryption?", installer.DeriveInstallWizardPrefill(*template).EncryptionEnabled)
	if err != nil {
		return false, err
	}
	if enableEncryption {
		*template = setEnvValue(*template, "ENCRYPT_ARCHIVE", "true")
	} else {
		*template = setEnvValue(*template, "ENCRYPT_ARCHIVE", "false")
	}
	return enableEncryption, nil
}

// schedulerEngineDefault picks the engine prompt default. Fresh installs and
// Overwrite (both start from the embedded template) default to the resident
// daemon, matching the Charm front-end and the daemon-by-default intent. Only an
// Edit of an existing config defaults to its stored SCHEDULER_MODE, so a no-op
// edit never flips the scheduler; an old config without the key stays on cron.
func schedulerEngineDefault(fromExisting bool, template string) string {
	// Fresh/Overwrite, or an Edit whose base is effectively empty, are "start from
	// scratch" -> daemon (this also keeps the empty-base boundary identical to the
	// Charm front-end, which keys off an empty base template).
	if !fromExisting || strings.TrimSpace(template) == "" {
		return "daemon"
	}
	switch strings.ToLower(strings.TrimSpace(installer.DeriveInstallWizardPrefill(template).SchedulerMode)) {
	case "daemon":
		return "daemon"
	default:
		return "cron"
	}
}

func configureSchedulerEngine(ctx context.Context, reader *bufio.Reader, def string) (string, error) {
	fmt.Println("\n--- Scheduler ---")
	raw, err := promptOptional(ctx, reader, fmt.Sprintf("Scheduler engine: daemon (resident, hang watchdog + healthchecks) or cron [%s]: ", def))
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "cron":
		return "cron", nil
	case "daemon":
		return "daemon", nil
	default:
		return def, nil
	}
}

func configureCronTime(ctx context.Context, reader *bufio.Reader, defaultCron string) (string, error) {
	fmt.Println("\n--- Schedule ---")
	for {
		cronTime, err := promptOptional(ctx, reader, fmt.Sprintf("Run at (daily, HH:MM) [%s]: ", defaultCron))
		if err != nil {
			return "", err
		}
		normalized, err := cronutil.NormalizeTime(cronTime, defaultCron)
		if err != nil {
			fmt.Printf("%v\n", err)
			continue
		}
		return normalized, nil
	}
}

func writeConfigFile(configPath, tmpConfigPath, content string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create configuration directory: %w", err)
	}
	// Confine the temp write to the configuration directory via os.Root so the
	// admin-supplied --config path cannot place the file outside that directory
	// (gosec G703 path-traversal containment). tmpConfigPath is configPath with a
	// suffix, so it always resolves to a single component within dir.
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("failed to open configuration directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.WriteFile(filepath.Base(tmpConfigPath), []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write configuration file: %w", err)
	}
	if err := os.Rename(tmpConfigPath, configPath); err != nil {
		return fmt.Errorf("failed to finalize configuration file: %w", err)
	}
	return nil
}

func wrapInstallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errInteractiveAborted) {
		// Preserve sentinel so callers can detect user-aborted installs with errors.Is
		return fmt.Errorf("installation aborted by user: %w", err)
	}
	return err
}

func isInstallAbortedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errInteractiveAborted) {
		return true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "installation aborted by user") {
		return true
	}
	if strings.Contains(msg, "installation aborted (existing configuration kept)") {
		return true
	}
	if strings.Contains(msg, "encryption setup aborted by user") {
		return true
	}
	return false
}

func clearImmutableAttributesWithContext(ctx context.Context, target string, bootstrap *logging.BootstrapLogger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	chattrPath, err := exec.LookPath("chattr")
	if err != nil {
		return nil
	}

	argsList := [][]string{{chattrPath, "-i", target}}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		argsList = append([][]string{{chattrPath, "-R", "-i", target}}, argsList...)
	}

	for _, args := range argsList {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd, err := safeexec.TrustedCommandContext(ctx, args[0], args[1:]...)
		if err != nil {
			logBootstrapWarning(bootstrap, "Failed to prepare chattr for %s: %v", target, err)
			continue
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			trimmed := strings.TrimSpace(string(out))
			if trimmed != "" {
				logBootstrapWarning(bootstrap, "Failed to clear immutable flag on %s: %v (%s)", target, err, trimmed)
			} else {
				logBootstrapWarning(bootstrap, "Failed to clear immutable flag on %s: %v", target, err)
			}
		}
	}
	return nil
}
