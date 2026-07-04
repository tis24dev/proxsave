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

	"charm.land/huh/v2"

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

// CollectWizardData shows the configuration wizard as ONE screen (a single
// form with every setting, like the tview wizard it replaced), prefilled
// from baseTemplate. Esc cancels the installation.
func CollectWizardData(ctx context.Context, session *shell.Session, baseTemplate string) (*installer.InstallWizardData, error) {
	prefill := installer.DeriveInstallWizardPrefill(baseTemplate)

	enableSecondary := prefill.SecondaryEnabled
	secondaryPath := prefill.SecondaryPath
	secondaryLog := prefill.SecondaryLogPath
	enableCloud := prefill.CloudEnabled
	cloudRemote := prefill.CloudRemote
	cloudLog := prefill.CloudLogPath
	firewall := prefill.FirewallEnabled
	telegram := prefill.TelegramEnabled
	email := prefill.EmailEnabled
	method := installer.EmailDeliveryMethodOrDefault(prefill.EmailDeliveryMethod)
	encryption := prefill.EncryptionEnabled
	cronTime := cronutil.DefaultTime

	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().
			Title("Secondary storage").
			Description("Additional local path for redundant copies. Must be a filesystem-mounted directory (e.g. /mnt/nas-backup); mount network shares first. For direct network access use cloud storage (rclone).").
			Affirmative("Yes").Negative("No").
			Value(&enableSecondary),
		huh.NewInput().
			Title("Secondary backup path (SECONDARY_PATH)").
			Value(&secondaryPath).
			Validate(func(v string) error {
				if !enableSecondary {
					return nil
				}
				return config.ValidateRequiredSecondaryPath(strings.TrimSpace(v))
			}),
		huh.NewInput().
			Title("Secondary log path (SECONDARY_LOG_PATH, optional)").
			Value(&secondaryLog).
			Validate(func(v string) error {
				if !enableSecondary {
					return nil
				}
				return config.ValidateOptionalSecondaryLogPath(strings.TrimSpace(v))
			}),
		huh.NewConfirm().
			Title("Cloud backups (rclone)").
			Description("Configure rclone manually before enabling cloud backups.").
			Affirmative("Yes").Negative("No").
			Value(&enableCloud),
		huh.NewInput().
			Title("Rclone remote for backups (e.g. myremote:pbs-backups)").
			Value(&cloudRemote).
			Validate(func(v string) error {
				if !enableCloud {
					return nil
				}
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("rclone backup remote cannot be empty")
				}
				return nil
			}),
		huh.NewInput().
			Title("Rclone remote for logs (e.g. myremote:/logs)").
			Value(&cloudLog).
			Validate(func(v string) error {
				if !enableCloud {
					return nil
				}
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("rclone log path cannot be empty")
				}
				return nil
			}),
		huh.NewConfirm().
			Title("Backup firewall rules").
			Description("Collect firewall rules (iptables/nftables). Changeable later via BACKUP_FIREWALL_RULES.").
			Affirmative("Yes").Negative("No").
			Value(&firewall),
		huh.NewConfirm().
			Title("Telegram notifications (centralized)").
			Affirmative("Yes").Negative("No").
			Value(&telegram),
		huh.NewConfirm().
			Title("Email notifications").
			Description("Default delivery uses the TIS24 cloud relay with local sendmail as failover; choose pmf only when Proxmox Notifications is configured.").
			Affirmative("Yes").Negative("No").
			Value(&email),
		huh.NewSelect[string]().
			Title("Email delivery method").
			Options(
				huh.NewOption("Cloud relay (relay)", "relay"),
				huh.NewOption("Local sendmail (sendmail)", "sendmail"),
				huh.NewOption("Proxmox Notifications (pmf)", "pmf"),
			).
			Value(&method),
		huh.NewConfirm().
			Title("Backup encryption (AGE)").
			Affirmative("Yes").Negative("No").
			Value(&encryption),
		huh.NewInput().
			Title(fmt.Sprintf("Cron time for the daily job (HH:MM, default %s)", cronutil.DefaultTime)).
			Value(&cronTime).
			Validate(func(v string) error {
				_, err := cronutil.NormalizeTime(v, cronutil.DefaultTime)
				return err
			}),
	))

	if _, err := shell.Ask(ctx, session, components.NewFormScreen("Configuration", form)); err != nil {
		return nil, mapCancel(err)
	}

	data := &installer.InstallWizardData{
		EnableSecondaryStorage: enableSecondary,
		EnableCloudStorage:     enableCloud,
		BackupFirewallRules:    &firewall,
		EnableEncryption:       encryption,
	}
	if enableSecondary {
		data.SecondaryPath = strings.TrimSpace(secondaryPath)
		data.SecondaryLogPath = strings.TrimSpace(secondaryLog)
	}
	if enableCloud {
		data.RcloneBackupRemote = strings.TrimSpace(cloudRemote)
		data.RcloneLogRemote = strings.TrimSpace(cloudLog)
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
		data.EmailDeliveryMethod = installer.EmailDeliveryMethodOrDefault(method)
		fallbackSendmail := true
		data.EmailFallbackSendmail = &fallbackSendmail
	}
	normalized, err := cronutil.NormalizeTime(cronTime, cronutil.DefaultTime)
	if err != nil {
		return nil, err
	}
	data.CronTime = normalized

	return data, nil
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
