package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/identity"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/orchestrator"
	"github.com/tis24dev/proxmox-backup/internal/tui/wizard"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

func runInstall(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger) error {
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}
	configPath = resolvedPath

	// Derive BASE_DIR from the configuration path so that configs/, identity/, logs/, etc.
	// all live under the same root, even during --install.
	baseDir := filepath.Dir(filepath.Dir(configPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		baseDir = "/opt/proxmox-backup"
	}
	_ = os.Setenv("BASE_DIR", baseDir)

	var telegramCode string
	var installErr error

	defer func() {
		printInstallFooter(installErr, configPath, baseDir, telegramCode)
	}()

	if err := ensureInteractiveStdin(); err != nil {
		installErr = err
		return installErr
	}

	tmpConfigPath := configPath + ".tmp"
	defer func() {
		if _, err := os.Stat(tmpConfigPath); err == nil {
			_ = os.Remove(tmpConfigPath)
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	printInstallBanner(configPath)
	enableEncryption := false

	// Detect legacy Bash-based installation (old backup.env or proxmox-backup.sh)
	legacyPaths := []string{
		filepath.Join(baseDir, "env", "backup.env"),
		filepath.Join(baseDir, "proxmox-backup.sh"),
		filepath.Join(baseDir, "script", "proxmox-backup.sh"),
	}
	legacyFound := false
	for _, p := range legacyPaths {
		if _, err := os.Stat(p); err == nil {
			legacyFound = true
			break
		}
	}

	if legacyFound {
		yellow := "\033[33m"
		reset := "\033[0m"
		fmt.Println(string(yellow) + "A previous Bash-based version of the Proxmox Backup script has been detected on this system." + string(reset))
		fmt.Println(string(yellow) + "This Go version requires migrating or recreating the configuration file. You will also have access to the migration tool." + string(reset))
		fmt.Println()

		confirm, err := promptYesNo(ctx, reader, "Do you want to continue with the Go install wizard? [y/N]: ", false)
		if err != nil {
			installErr = wrapInstallError(err)
			return installErr
		}
		if !confirm {
			installErr = wrapInstallError(errInteractiveAborted)
			return installErr
		}

		fmt.Println()
	}

	template, skipConfigWizard, err := prepareBaseTemplate(ctx, reader, configPath)
	if err != nil {
		installErr = wrapInstallError(err)
		return installErr
	}

	if !skipConfigWizard {
		if template, err = configureSecondaryStorage(ctx, reader, template); err != nil {
			installErr = wrapInstallError(err)
			return installErr
		}
		if template, err = configureCloudStorage(ctx, reader, template); err != nil {
			installErr = wrapInstallError(err)
			return installErr
		}
		if template, err = configureNotifications(ctx, reader, template); err != nil {
			installErr = wrapInstallError(err)
			return installErr
		}
		enableEncryption, err = configureEncryption(ctx, reader, &template)
		if err != nil {
			installErr = wrapInstallError(err)
			return installErr
		}

		// Ensure BASE_DIR is explicitly present in the generated env file so that
		// subsequent runs and encryption setup use the same root directory.
		template = setEnvValue(template, "BASE_DIR", baseDir)

		if err := writeConfigFile(configPath, tmpConfigPath, template); err != nil {
			installErr = err
			return installErr
		}
		bootstrap.Info("✓ Configuration saved at %s", configPath)
	}

	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		installErr = fmt.Errorf("install documentation: %w", err)
		return installErr
	}

	if !skipConfigWizard && enableEncryption {
		if err := runInitialEncryptionSetup(ctx, configPath); err != nil {
			installErr = err
			return installErr
		}
	}

	// Clean up legacy bash-based symlinks that point to the old installer scripts.
	cleanupLegacyBashSymlinks(baseDir, bootstrap)

	// Ensure a proxmox-backup entry points to this Go binary, if not already customized.
	execInfo := getExecInfo()
	ensureGoSymlink(execInfo.ExecPath, bootstrap)

	// Migrate legacy cron entries pointing to the bash script to the Go binary.
	// If no cron entry exists at all, create a default one at 02:00 every day.
	cronSchedule := resolveCronSchedule(nil)
	migrateLegacyCronEntries(ctx, baseDir, execInfo.ExecPath, bootstrap, cronSchedule)

	// Attempt to resolve or create a server identity so that we can show a
	// Telegram pairing code to the user (similar to the legacy installer).
	if info, err := identity.Detect(baseDir, nil); err == nil {
		if code := strings.TrimSpace(info.ServerID); code != "" {
			telegramCode = code
		}
	}

	installErr = nil
	return nil
}

func runNewInstall(ctx context.Context, configPath string, bootstrap *logging.BootstrapLogger, useCLI bool) error {
	resolvedPath, err := resolveInstallConfigPath(configPath)
	if err != nil {
		return err
	}

	baseDir := filepath.Dir(filepath.Dir(resolvedPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		baseDir = "/opt/proxmox-backup"
	}

	if err := ensureInteractiveStdin(); err != nil {
		return err
	}

	buildSig := buildSignature()
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}

	confirm, err := wizard.ConfirmNewInstall(baseDir, buildSig)
	if err != nil {
		return wrapInstallError(err)
	}
	if !confirm {
		return wrapInstallError(errInteractiveAborted)
	}

	bootstrap.Info("Resetting %s (preserving env/ and identity/)", baseDir)
	if err := resetInstallBaseDir(baseDir, bootstrap); err != nil {
		return err
	}

	if useCLI {
		return runInstall(ctx, resolvedPath, bootstrap)
	}
	return runInstallTUI(ctx, resolvedPath, bootstrap)
}

func printInstallFooter(installErr error, configPath, baseDir, telegramCode string) {
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

	// For user-aborted runs, stop here to avoid showing next steps/commands.
	if installErr != nil && isInstallAbortedError(installErr) {
		return
	}

	fmt.Println("Next steps:")
	fmt.Println("0. If you need, start migration from old backup.env:  proxmox-backup --env-migration")
	if strings.TrimSpace(configPath) != "" {
		fmt.Printf("1. Edit configuration: %s\n", configPath)
	} else {
		fmt.Println("1. Edit configuration: <configuration path unavailable>")
	}
	if strings.TrimSpace(baseDir) != "" {
		fmt.Println("2. Run first backup: proxmox-backup")
		fmt.Printf("3. Check logs: tail -f %s/log/*.log\n", baseDir)
	} else {
		fmt.Println("2. Run first backup: proxmox-backup")
		fmt.Println("3. Check logs: tail -f /opt/proxmox-backup/log/*.log")
	}
	if telegramCode != "" {
		fmt.Printf("4. Telegram: Open @ProxmoxAN_bot and enter code: %s\n", telegramCode)
	} else {
		fmt.Println("4. Telegram: Open @ProxmoxAN_bot and enter your unique code")
	}
	fmt.Println()
	fmt.Println("\033[31mEXTRA STEP - IF YOU FIND THIS TOOL USEFUL AND WANT TO THANK ME, A COFFEE IS ALWAYS WELCOME!\033[0m")
	fmt.Println("https://buymeacoffee.com/tis24dev")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  proxmox-backup     - Start backup")
	fmt.Println("  --help             - Show all options")
	fmt.Println("  --dry-run          - Test without changes")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep env/identity) then run installer")
	fmt.Println("  --newkey           - Generate a new encryption key for backups")
	fmt.Println("  --decrypt          - Decrypt an existing backup archive")
	fmt.Println("  --restore          - Run interactive restore workflow (select bundle, decrypt if needed, apply to system)")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println("  --support          - Run backup in support mode (force debug log level and send email with attached log to github-support@tis24.it)")
	fmt.Println()
}

func resetInstallBaseDir(baseDir string, bootstrap *logging.BootstrapLogger) error {
	baseDir = filepath.Clean(baseDir)
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		return fmt.Errorf("refusing to reset unsafe base directory: %q", baseDir)
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return fmt.Errorf("failed to create base directory %s: %w", baseDir, err)
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return fmt.Errorf("failed to list base directory %s: %w", baseDir, err)
	}

	preserve := map[string]struct{}{
		"env":      {},
		"identity": {},
		"build":    {},
	}

	for _, entry := range entries {
		name := entry.Name()
		if _, keep := preserve[name]; keep {
			bootstrap.Info("Preserving %s", filepath.Join(baseDir, name))
			continue
		}
		target := filepath.Join(baseDir, name)
		clearImmutableAttributes(target, bootstrap)
		// Best-effort: ensure write permission before removal
		if entry.IsDir() {
			_ = os.Chmod(target, 0o700)
		} else {
			_ = os.Chmod(target, 0o600)
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("failed to remove %s: %w", target, err)
		}
		bootstrap.Info("Removed %s", target)
	}

	return nil
}

func printInstallBanner(configPath string) {
	fmt.Println("===========================================")
	fmt.Println("  Proxmox Backup - Go Version")
	fmt.Printf("  Version: %s\n", version)
	sig := buildSignature()
	if strings.TrimSpace(sig) == "" {
		sig = "n/a"
	}
	fmt.Printf("  Build Signature: %s\n", sig)
	fmt.Println("  Mode: Install Wizard")
	fmt.Println("===========================================")
	fmt.Printf("Configuration file: %s\n\n", configPath)
}

func prepareBaseTemplate(ctx context.Context, reader *bufio.Reader, configPath string) (string, bool, error) {
	if info, err := os.Stat(configPath); err == nil {
		if info.Mode().IsRegular() {
			overwrite, err := promptYesNo(ctx, reader, fmt.Sprintf("%s already exists. Overwrite? [y/N]: ", configPath), false)
			if err != nil {
				return "", false, err
			}
			if !overwrite {
				fmt.Println("Existing configuration detected, keeping current backup.env and skipping configuration wizard.")
				return "", true, nil
			}
		}
	} else if !os.IsNotExist(err) {
		return "", false, fmt.Errorf("failed to access configuration file: %w", err)
	}

	return config.DefaultEnvTemplate(), false, nil
}

func configureSecondaryStorage(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Secondary storage ---")
	fmt.Println("Configure an additional local path for redundant copies.")
	fmt.Println("IMPORTANT: Secondary path must be a filesystem-mounted directory (e.g., /mnt/nas-backup)")
	fmt.Println("Network shares must be mounted BEFORE running this backup tool.")
	fmt.Println("For direct network access without mounting, use cloud storage (rclone) instead.")
	fmt.Println("(You can change these settings later in backup.env)")
	enableSecondary, err := promptYesNo(ctx, reader, "Enable secondary backup path? [y/N]: ", false)
	if err != nil {
		return "", err
	}
	if enableSecondary {
		secondaryPath, err := promptNonEmpty(ctx, reader, "Secondary backup path (SECONDARY_PATH): ")
		if err != nil {
			return "", err
		}
		secondaryPath = sanitizeEnvValue(secondaryPath)
		secondaryLog, err := promptNonEmpty(ctx, reader, "Secondary log path (SECONDARY_LOG_PATH): ")
		if err != nil {
			return "", err
		}
		secondaryLog = sanitizeEnvValue(secondaryLog)
		template = setEnvValue(template, "SECONDARY_ENABLED", "true")
		template = setEnvValue(template, "SECONDARY_PATH", secondaryPath)
		template = setEnvValue(template, "SECONDARY_LOG_PATH", secondaryLog)
	} else {
		template = setEnvValue(template, "SECONDARY_ENABLED", "false")
		template = setEnvValue(template, "SECONDARY_PATH", "")
		template = setEnvValue(template, "SECONDARY_LOG_PATH", "")
	}
	return template, nil
}

func configureCloudStorage(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Cloud storage (rclone) ---")
	fmt.Println("Remember to configure rclone manually before enabling cloud backups.")
	enableCloud, err := promptYesNo(ctx, reader, "Enable cloud backups? [y/N]: ", false)
	if err != nil {
		return "", err
	}
	if enableCloud {
		remote, err := promptNonEmpty(ctx, reader, "Rclone remote for backups (e.g. myremote:pbs-backups): ")
		if err != nil {
			return "", err
		}
		remote = sanitizeEnvValue(remote)
		logRemote, err := promptNonEmpty(ctx, reader, "Rclone remote for logs (e.g. myremote:/logs): ")
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

func configureNotifications(ctx context.Context, reader *bufio.Reader, template string) (string, error) {
	fmt.Println("\n--- Telegram ---")
	enableTelegram, err := promptYesNo(ctx, reader, "Enable Telegram notifications (centralized)? [y/N]: ", false)
	if err != nil {
		return "", err
	}
	if enableTelegram {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "true")
		template = setEnvValue(template, "BOT_TELEGRAM_TYPE", "centralized")
	} else {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "false")
	}

	fmt.Println("\n--- Email ---")
	enableEmail, err := promptYesNo(ctx, reader, "Enable email notifications (central relay)? [y/N]: ", false)
	if err != nil {
		return "", err
	}
	if enableEmail {
		template = setEnvValue(template, "EMAIL_ENABLED", "true")
		template = setEnvValue(template, "EMAIL_DELIVERY_METHOD", "relay")
		template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", "true")
	} else {
		template = setEnvValue(template, "EMAIL_ENABLED", "false")
	}
	return template, nil
}

func configureEncryption(ctx context.Context, reader *bufio.Reader, template *string) (bool, error) {
	fmt.Println("\n--- Encryption ---")
	enableEncryption, err := promptYesNo(ctx, reader, "Enable backup encryption? [y/N]: ", false)
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

func writeConfigFile(configPath, tmpConfigPath, content string) error {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create configuration directory: %w", err)
	}
	if err := os.WriteFile(tmpConfigPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("failed to write configuration file: %w", err)
	}
	if err := os.Rename(tmpConfigPath, configPath); err != nil {
		return fmt.Errorf("failed to finalize configuration file: %w", err)
	}
	return nil
}

func runInitialEncryptionSetup(ctx context.Context, configPath string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to reload configuration after install: %w", err)
	}
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)
	orch := orchestrator.New(logger, false)
	orch.SetConfig(cfg)
	if err := orch.EnsureAgeRecipientsReady(ctx); err != nil {
		if errors.Is(err, orchestrator.ErrAgeRecipientSetupAborted) {
			// Treat AGE wizard abort as an interactive abort for install UX
			return fmt.Errorf("encryption setup aborted by user: %w", errInteractiveAborted)
		}
		return fmt.Errorf("encryption setup failed: %w", err)
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

// clearImmutableAttributes attempts to remove immutable flags (chattr -i) so deletion can proceed.
// It logs warnings on failure but does not return an error, since removal will report issues later.
func clearImmutableAttributes(target string, bootstrap *logging.BootstrapLogger) {
	chattrPath, err := exec.LookPath("chattr")
	if err != nil {
		return
	}

	argsList := [][]string{{chattrPath, "-i", target}}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		argsList = append([][]string{{chattrPath, "-R", "-i", target}}, argsList...)
	}

	for _, args := range argsList {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			trimmed := strings.TrimSpace(string(out))
			if trimmed != "" {
				bootstrap.Warning("Failed to clear immutable flag on %s: %v (%s)", target, err, trimmed)
			} else {
				bootstrap.Warning("Failed to clear immutable flag on %s: %v", target, err)
			}
		}
	}
}
