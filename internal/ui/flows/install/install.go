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

// CollectWizardData shows the configuration wizard as ONE aligned form
// (label column left, controls right, like the tview wizard it replaced),
// prefilled from baseTemplate. Esc cancels the installation.
func CollectWizardData(ctx context.Context, session *shell.Session, baseTemplate string) (*installer.InstallWizardData, error) {
	prefill := installer.DeriveInstallWizardPrefill(baseTemplate)

	secondary := &components.FormField{
		Label:       "Secondary storage",
		Description: "Additional local path for redundant copies; must be filesystem-mounted (e.g. /mnt/nas-backup). For direct network access use cloud storage (rclone).",
		Kind:        components.FieldToggle,
		Bool:        prefill.SecondaryEnabled,
	}
	secondaryPath := &components.FormField{
		Label:       "Secondary backup path",
		Description: "SECONDARY_PATH: filesystem path for the redundant copies.",
		Kind:        components.FieldText,
		Text:        prefill.SecondaryPath,
		Active:      func() bool { return secondary.Bool },
		Validate: func(v string) error {
			return config.ValidateRequiredSecondaryPath(strings.TrimSpace(v))
		},
	}
	secondaryLog := &components.FormField{
		Label:       "Secondary log path",
		Description: "SECONDARY_LOG_PATH, optional: leave empty to skip.",
		Kind:        components.FieldText,
		Text:        prefill.SecondaryLogPath,
		Active:      func() bool { return secondary.Bool },
		Validate: func(v string) error {
			return config.ValidateOptionalSecondaryLogPath(strings.TrimSpace(v))
		},
	}
	cloud := &components.FormField{
		Label:       "Cloud backups (rclone)",
		Description: "Configure rclone manually before enabling cloud backups.",
		Kind:        components.FieldToggle,
		Bool:        prefill.CloudEnabled,
	}
	cloudRemote := &components.FormField{
		Label:       "Rclone backup remote",
		Description: "CLOUD_REMOTE, e.g. myremote:pbs-backups.",
		Kind:        components.FieldText,
		Text:        prefill.CloudRemote,
		Active:      func() bool { return cloud.Bool },
		Validate: func(v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("cannot be empty")
			}
			return nil
		},
	}
	cloudLog := &components.FormField{
		Label:       "Rclone log remote",
		Description: "CLOUD_LOG_PATH, e.g. myremote:/logs.",
		Kind:        components.FieldText,
		Text:        prefill.CloudLogPath,
		Active:      func() bool { return cloud.Bool },
		Validate: func(v string) error {
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("cannot be empty")
			}
			return nil
		},
	}
	firewall := &components.FormField{
		Label:       "Backup firewall rules",
		Description: "Collect firewall rules (iptables/nftables); changeable later via BACKUP_FIREWALL_RULES.",
		Kind:        components.FieldToggle,
		Bool:        prefill.FirewallEnabled,
	}
	telegram := &components.FormField{
		Label:       "Telegram notifications",
		Description: "Centralized bot pairing; the setup step follows after the wizard.",
		Kind:        components.FieldToggle,
		Bool:        prefill.TelegramEnabled,
	}
	email := &components.FormField{
		Label:       "Email notifications",
		Description: "Default delivery uses the TIS24 cloud relay with local sendmail as failover.",
		Kind:        components.FieldToggle,
		Bool:        prefill.EmailEnabled,
	}
	methodOptions := []string{"Cloud relay (relay)", "Local sendmail (sendmail)", "Proxmox Notifications (pmf)"}
	methodValues := []string{"relay", "sendmail", "pmf"}
	methodIndex := 0
	for i, v := range methodValues {
		if v == installer.EmailDeliveryMethodOrDefault(prefill.EmailDeliveryMethod) {
			methodIndex = i
		}
	}
	method := &components.FormField{
		Label:       "Email delivery method",
		Description: "Choose pmf only when Proxmox Notifications is configured.",
		Kind:        components.FieldSelect,
		Options:     methodOptions,
		OptionIndex: methodIndex,
		Active:      func() bool { return email.Bool },
	}
	encryption := &components.FormField{
		Label:       "Backup encryption (AGE)",
		Description: "The AGE recipient setup follows after the wizard.",
		Kind:        components.FieldToggle,
		Bool:        prefill.EncryptionEnabled,
	}
	cronField := &components.FormField{
		Label:       "Cron time (HH:MM)",
		Description: fmt.Sprintf("Daily proxsave job schedule; default %s.", cronutil.DefaultTime),
		Kind:        components.FieldText,
		Text:        cronutil.DefaultTime,
		Validate: func(v string) error {
			_, err := cronutil.NormalizeTime(v, cronutil.DefaultTime)
			return err
		},
	}

	fields := []*components.FormField{
		secondary, secondaryPath, secondaryLog,
		cloud, cloudRemote, cloudLog,
		firewall, telegram, email, method, encryption, cronField,
	}
	if _, err := shell.Ask(ctx, session, components.NewFormGrid(
		"Configuration", fields,
		components.WithFormGridBack(installer.ErrInstallCancelled),
	)); err != nil {
		return nil, mapCancel(err)
	}

	data := &installer.InstallWizardData{
		EnableSecondaryStorage: secondary.Bool,
		EnableCloudStorage:     cloud.Bool,
		BackupFirewallRules:    &firewall.Bool,
		EnableEncryption:       encryption.Bool,
	}
	if secondary.Bool {
		data.SecondaryPath = strings.TrimSpace(secondaryPath.Text)
		data.SecondaryLogPath = strings.TrimSpace(secondaryLog.Text)
	}
	if cloud.Bool {
		data.RcloneBackupRemote = strings.TrimSpace(cloudRemote.Text)
		data.RcloneLogRemote = strings.TrimSpace(cloudLog.Text)
	}
	switch {
	case telegram.Bool && email.Bool:
		data.NotificationMode = "both"
	case telegram.Bool:
		data.NotificationMode = "telegram"
	case email.Bool:
		data.NotificationMode = "email"
	default:
		data.NotificationMode = "none"
	}
	if email.Bool {
		data.EmailDeliveryMethod = methodValues[method.OptionIndex]
		fallbackSendmail := true
		data.EmailFallbackSendmail = &fallbackSendmail
	}
	normalized, err := cronutil.NormalizeTime(cronField.Text, cronutil.DefaultTime)
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
