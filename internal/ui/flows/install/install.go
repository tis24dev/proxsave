// Package install implements the Charm screens of the install family: the
// existing-config decision, the configuration wizard (producing
// installer.InstallWizardData applied by the shared installer engine), the
// new-install confirmation, the post-install audit, and the Telegram pairing
// step. The caller owns the Session. Parity reference: the deleted tview
// wizard (internal/tui/wizard at commit bb568fb).
package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// mapCancel converts the shell abort sentinel into the install-cancelled
// sentinel the cmd layer branches on.
func mapCancel(err error) error {
	if errors.Is(err, shell.ErrAborted) {
		return installer.ErrInstallCancelled
	}
	return err
}

// ResolveExistingConfig asks how to handle an already-present configuration
// file. When no file exists it returns Overwrite without any screen (same
// contract as the tview CheckExistingConfig and the CLI prompt).
func ResolveExistingConfig(ctx context.Context, session *shell.Session, configPath string) (installer.ExistingConfigAction, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return installer.ExistingConfigOverwrite, nil
		}
		return installer.ExistingConfigCancel, fmt.Errorf("failed to access configuration file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return installer.ExistingConfigCancel, fmt.Errorf("configuration file path is not a regular file: %s", configPath)
	}

	items := []components.SelectorItem[installer.ExistingConfigAction]{
		{Label: "Keep existing & continue", Description: "leave the file untouched and skip the configuration wizard", Value: installer.ExistingConfigKeepContinue},
		{Label: "Edit existing", Description: "use the current file as base for the wizard", Value: installer.ExistingConfigEdit},
		{Label: "Overwrite", Description: "start from the embedded template", Value: installer.ExistingConfigOverwrite},
		{Label: "Cancel installation", Description: "abort without changes", Value: installer.ExistingConfigCancel},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Existing configuration", items,
		components.WithSelectorPrompt[installer.ExistingConfigAction](
			fmt.Sprintf("%s already exists.\nChoose how to proceed.", configPath)),
		components.WithSelectorBack[installer.ExistingConfigAction](installer.ErrInstallCancelled),
	))
	if err != nil {
		return installer.ExistingConfigCancel, mapCancel(err)
	}
	return action, nil
}

// CollectWizardData runs the configuration wizard screens and returns the
// collected data, prefilled from baseTemplate. Esc on any screen cancels the
// installation (installer.ErrInstallCancelled).
func CollectWizardData(ctx context.Context, session *shell.Session, baseTemplate string) (*installer.InstallWizardData, error) {
	prefill := installer.DeriveInstallWizardPrefill(baseTemplate)
	data := &installer.InstallWizardData{CronTime: cronutil.DefaultTime}

	confirm := func(title, message string, def bool) (bool, error) {
		res, err := shell.Ask(ctx, session, components.NewConfirm(title, message,
			components.WithDefaultYes(def),
			// Esc cancels the whole wizard (tview Cancel button parity),
			// it is not a No answer.
			components.WithConfirmAbort(installer.ErrInstallCancelled)))
		if err != nil {
			return false, mapCancel(err)
		}
		return res.Answer, nil
	}

	// Secondary storage.
	enableSecondary, err := confirm("Secondary storage",
		"Configure an additional local path for redundant copies.\n\nIMPORTANT: the secondary path must be a filesystem-mounted directory (e.g. /mnt/nas-backup); network shares must be mounted before running the backup tool. For direct network access without mounting, use cloud storage (rclone) instead.\n\nEnable secondary backup path?",
		prefill.SecondaryEnabled)
	if err != nil {
		return nil, err
	}
	data.EnableSecondaryStorage = enableSecondary
	if enableSecondary {
		secondaryPath, err := shell.Ask(ctx, session, components.NewInput(
			"Secondary backup path", "Filesystem path for the redundant copies (SECONDARY_PATH).",
			components.WithInitialValue(prefill.SecondaryPath),
			components.WithValidate(func(v string) error {
				return config.ValidateRequiredSecondaryPath(strings.TrimSpace(v))
			}),
		))
		if err != nil {
			return nil, mapCancel(err)
		}
		data.SecondaryPath = strings.TrimSpace(secondaryPath)

		secondaryLog, err := shell.Ask(ctx, session, components.NewInput(
			"Secondary log path", "Optional log path (SECONDARY_LOG_PATH); leave empty to skip.",
			components.WithInitialValue(prefill.SecondaryLogPath),
			components.WithValidate(func(v string) error {
				return config.ValidateOptionalSecondaryLogPath(strings.TrimSpace(v))
			}),
		))
		if err != nil {
			return nil, mapCancel(err)
		}
		data.SecondaryLogPath = strings.TrimSpace(secondaryLog)
	}

	// Cloud storage.
	enableCloud, err := confirm("Cloud storage (rclone)",
		"Remember to configure rclone manually before enabling cloud backups.\n\nEnable cloud backups?",
		prefill.CloudEnabled)
	if err != nil {
		return nil, err
	}
	data.EnableCloudStorage = enableCloud
	if enableCloud {
		remote, err := shell.Ask(ctx, session, components.NewInput(
			"Rclone backup remote", "Rclone remote for backups (e.g. myremote:pbs-backups).",
			components.WithInitialValue(prefill.CloudRemote),
			components.WithValidate(requireNonEmpty("rclone backup remote")),
		))
		if err != nil {
			return nil, mapCancel(err)
		}
		data.RcloneBackupRemote = strings.TrimSpace(remote)

		logRemote, err := shell.Ask(ctx, session, components.NewInput(
			"Rclone log remote", "Rclone remote for logs (e.g. myremote:/logs).",
			components.WithInitialValue(prefill.CloudLogPath),
			components.WithValidate(requireNonEmpty("rclone log path")),
		))
		if err != nil {
			return nil, mapCancel(err)
		}
		data.RcloneLogRemote = strings.TrimSpace(logRemote)
	}

	// Firewall rules.
	firewall, err := confirm("Firewall rules",
		"Enable collection of firewall rules (e.g. iptables/nftables).\nYou can change this later in backup.env via BACKUP_FIREWALL_RULES.\n\nBackup firewall rules?",
		prefill.FirewallEnabled)
	if err != nil {
		return nil, err
	}
	data.BackupFirewallRules = &firewall

	// Notifications.
	telegram, err := confirm("Telegram",
		"Enable Telegram notifications (centralized)?",
		prefill.TelegramEnabled)
	if err != nil {
		return nil, err
	}
	email, err := confirm("Email",
		"Default email delivery uses the TIS24 cloud relay, with local sendmail as failover.\nProxSave does not collect raw SMTP settings; choose pmf only when Proxmox Notifications is configured.\n\nEnable email notifications?",
		prefill.EmailEnabled)
	if err != nil {
		return nil, err
	}
	switch {
	case telegram && email:
		data.NotificationMode = "both"
	case telegram:
		data.NotificationMode = "telegram"
	case email:
		data.NotificationMode = "email"
	default:
		data.NotificationMode = "none"
	}
	if email {
		method, err := selectEmailDeliveryMethod(ctx, session, prefill.EmailDeliveryMethod)
		if err != nil {
			return nil, err
		}
		data.EmailDeliveryMethod = method
		fallbackSendmail := true
		data.EmailFallbackSendmail = &fallbackSendmail
	}

	// Encryption.
	encryption, err := confirm("Encryption",
		"Enable backup encryption (AGE)?",
		prefill.EncryptionEnabled)
	if err != nil {
		return nil, err
	}
	data.EnableEncryption = encryption

	// Schedule.
	cronTime, err := shell.Ask(ctx, session, components.NewInput(
		"Schedule", fmt.Sprintf("Cron time for the daily proxsave job (HH:MM, default %s).", cronutil.DefaultTime),
		components.WithInitialValue(cronutil.DefaultTime),
		components.WithValidate(func(v string) error {
			_, err := cronutil.NormalizeTime(v, cronutil.DefaultTime)
			return err
		}),
	))
	if err != nil {
		return nil, mapCancel(err)
	}
	normalized, err := cronutil.NormalizeTime(cronTime, cronutil.DefaultTime)
	if err != nil {
		return nil, err
	}
	data.CronTime = normalized

	return data, nil
}

func requireNonEmpty(field string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s cannot be empty", field)
		}
		return nil
	}
}

func selectEmailDeliveryMethod(ctx context.Context, session *shell.Session, current string) (string, error) {
	current = installer.EmailDeliveryMethodOrDefault(current)
	items := []components.SelectorItem[string]{
		{Label: "Cloud relay (relay)", Description: "TIS24 cloud relay over outbound HTTPS (default)", Value: "relay"},
		{Label: "Local sendmail (sendmail)", Description: "local /usr/sbin/sendmail; requires a local MTA", Value: "sendmail"},
		{Label: "Proxmox Notifications (pmf)", Description: "proxmox-mail-forward; SMTP lives in Proxmox", Value: "pmf"},
	}
	cursor := 0
	for i, it := range items {
		if it.Value == current {
			cursor = i
		}
	}
	method, err := shell.Ask(ctx, session, components.NewSelector(
		"Email delivery method", items,
		components.WithSelectorCursor[string](cursor),
		components.WithSelectorBack[string](installer.ErrInstallCancelled),
	))
	if err != nil {
		return "", mapCancel(err)
	}
	return method, nil
}

// ConfirmNewInstall confirms the --new-install base directory reset.
func ConfirmNewInstall(ctx context.Context, session *shell.Session, baseDir string, preservedEntries []string) (bool, error) {
	res, err := shell.Ask(ctx, session, components.NewConfirm(
		"Confirm new install",
		fmt.Sprintf("Base directory to reset:\n%s\n\nThis keeps %s\nbut deletes everything else.\n\nContinue?",
			baseDir, formatPreservedEntries(baseDir, preservedEntries)),
		components.WithLabels("Continue", "Cancel"),
		components.WithDefaultYes(false),
		components.WithDanger(),
	))
	if err != nil {
		if errors.Is(err, shell.ErrAborted) {
			return false, nil
		}
		return false, err
	}
	return res.Answer, nil
}

func formatPreservedEntries(baseDir string, entries []string) string {
	formatted := make([]string, 0, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, "/") {
			resolved := filepath.Join(baseDir, trimmed)
			if fi, err := os.Stat(resolved); err == nil && fi.IsDir() {
				trimmed += "/"
			}
		}
		formatted = append(formatted, trimmed)
	}
	if len(formatted) == 0 {
		return "(none)"
	}
	return strings.Join(formatted, " ")
}
