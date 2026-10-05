package install

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// RunHealthcheckSelfParams shows the self-mode healthchecks parameters screen: one
// aligned form collecting the FULL ping URLs of every sensor. Alive + Backup are
// REQUIRED; updates and the four notify URLs are OPTIONAL. It prefills from the
// current config (installer.DeriveHealthcheckSelfParams) so a re-run keeps stored
// values, and on submit writes the URLs back into backup.env via
// installer.ApplyHealthcheckSelfParams + WriteConfigFileAtomic. This MUST run before
// RunHealthcheckSetup so the bootstrap re-reads the just-written alive URL. Esc
// cancels the install (mirrors CollectWizardData); ENABLED/MODE are owned by the
// wizard's ApplyInstallData and are not touched here.
func RunHealthcheckSelfParams(ctx context.Context, session *shell.Session, baseDir, configPath string) error {
	contentBytes, err := safefs.ReadFileUnderRoot(configPath)
	if err != nil {
		return fmt.Errorf("read configuration for healthcheck parameters: %w", err)
	}
	template := string(contentBytes)
	prefill := installer.DeriveHealthcheckSelfParams(template)

	alive := &components.FormField{
		Label:       "Alive ping URL",
		Description: "HEALTHCHECK_ALIVE_URL (required): the full service-alive ping URL (e.g. https://hc-ping.com/<uuid>).",
		Kind:        components.FieldText,
		Text:        prefill.AliveURL,
		Validate:    installer.ValidateHealthcheckPingURL,
	}
	backup := &components.FormField{
		Label:       "Backup ping URL",
		Description: "HEALTHCHECK_BACKUP_URL (required): the full backup-outcome ping URL.",
		Kind:        components.FieldText,
		Text:        prefill.BackupURL,
		Validate:    installer.ValidateHealthcheckPingURL,
	}
	updates := &components.FormField{
		Label:       "Updates ping URL",
		Description: "HEALTHCHECK_UPDATES_URL (optional): the updates-check ping URL.",
		Kind:        components.FieldText,
		Text:        prefill.UpdatesURL,
		Validate:    installer.ValidateOptionalHealthcheckPingURL,
	}
	notifyEmail := &components.FormField{
		Label:       "Email delivery ping URL",
		Description: "HEALTHCHECK_NOTIFY_EMAIL_URL (optional): ping URL of your check that watches email delivery. Not an email setting: those are the EMAIL_* variables.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyEmailURL,
		Validate:    installer.ValidateOptionalHealthcheckPingURL,
	}
	notifyTelegram := &components.FormField{
		Label:       "Telegram delivery ping URL",
		Description: "HEALTHCHECK_NOTIFY_TELEGRAM_URL (optional): ping URL of your check that watches Telegram delivery. Not a Telegram setting: those are the TELEGRAM_* variables.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyTelegramURL,
		Validate:    installer.ValidateOptionalHealthcheckPingURL,
	}
	notifyGotify := &components.FormField{
		Label:       "Gotify delivery ping URL",
		Description: "HEALTHCHECK_NOTIFY_GOTIFY_URL (optional): ping URL of your check that watches Gotify delivery. Not the Gotify server: that is GOTIFY_SERVER_URL.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyGotifyURL,
		Validate:    installer.ValidateOptionalHealthcheckPingURL,
	}
	notifyWebhook := &components.FormField{
		Label:       "Webhook delivery ping URL",
		Description: "HEALTHCHECK_NOTIFY_WEBHOOK_URL (optional): ping URL of your check that watches webhook delivery. Not a webhook endpoint: those are the WEBHOOK_* variables.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyWebhookURL,
		Validate:    installer.ValidateOptionalHealthcheckPingURL,
	}

	fields := []*components.FormField{
		alive, backup, updates,
		notifyEmail, notifyTelegram, notifyGotify, notifyWebhook,
	}
	if _, err := shell.Ask(ctx, session, components.NewFormGrid(
		"Healthchecks - your own server parameters", fields,
		components.WithFormGridBack(installer.ErrInstallCancelled),
	)); err != nil {
		return mapCancel(err)
	}

	params := installer.HealthcheckSelfParams{
		AliveURL:          strings.TrimSpace(alive.Text),
		BackupURL:         strings.TrimSpace(backup.Text),
		UpdatesURL:        strings.TrimSpace(updates.Text),
		NotifyEmailURL:    strings.TrimSpace(notifyEmail.Text),
		NotifyTelegramURL: strings.TrimSpace(notifyTelegram.Text),
		NotifyGotifyURL:   strings.TrimSpace(notifyGotify.Text),
		NotifyWebhookURL:  strings.TrimSpace(notifyWebhook.Text),
	}
	updated := installer.ApplyHealthcheckSelfParams(template, params)
	if err := installer.WriteConfigFileAtomic(configPath, configPath+".tmp.hcself", updated); err != nil {
		return err
	}
	return nil
}
