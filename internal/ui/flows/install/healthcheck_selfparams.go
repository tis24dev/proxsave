package install

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// validateHealthcheckPingURL accepts only a well-formed absolute http(s) ping URL
// with a host. It mirrors the http(s) gate style of serverbot.SanitizeLoginURL but
// is a full-URL validator: an empty value is rejected (use it on required fields;
// wrap it for optional ones). Callers paste the ENTIRE ping URL of each check
// (e.g. https://hc-ping.com/<uuid>), so the daemon's selfURLs() full-URL branch
// resolves it verbatim.
func validateHealthcheckPingURL(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("cannot be empty")
	}
	if !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	u, err := url.ParseRequestURI(v)
	if err != nil {
		return fmt.Errorf("invalid URL: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("URL must start with http:// or https://")
	}
	if u.Host == "" {
		return fmt.Errorf("URL must include a host")
	}
	return nil
}

// validateOptionalHealthcheckPingURL is validateHealthcheckPingURL for optional
// fields: an empty value is accepted (the sensor is simply not configured), a
// non-empty value must still be a valid http(s) URL.
func validateOptionalHealthcheckPingURL(v string) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return validateHealthcheckPingURL(v)
}

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
	contentBytes, err := os.ReadFile(configPath) // #nosec G304 -- admin-supplied config path
	if err != nil {
		return fmt.Errorf("read configuration for healthcheck parameters: %w", err)
	}
	template := string(contentBytes)
	prefill := installer.DeriveHealthcheckSelfParams(template)

	alive := &components.FormField{
		Label:       "Alive ping URL",
		Description: "HEALTHCHECK_ALIVE_URL (obbligatorio): URL di ping service-alive completo (es. https://hc-ping.com/<uuid>).",
		Kind:        components.FieldText,
		Text:        prefill.AliveURL,
		Validate:    validateHealthcheckPingURL,
	}
	backup := &components.FormField{
		Label:       "Backup ping URL",
		Description: "HEALTHCHECK_BACKUP_URL (obbligatorio): URL di ping dell'esito backup completo.",
		Kind:        components.FieldText,
		Text:        prefill.BackupURL,
		Validate:    validateHealthcheckPingURL,
	}
	updates := &components.FormField{
		Label:       "Updates ping URL",
		Description: "HEALTHCHECK_UPDATES_URL (opzionale): URL di ping del check aggiornamenti.",
		Kind:        components.FieldText,
		Text:        prefill.UpdatesURL,
		Validate:    validateOptionalHealthcheckPingURL,
	}
	notifyEmail := &components.FormField{
		Label:       "Notify email URL",
		Description: "HEALTHCHECK_NOTIFY_EMAIL_URL (opzionale): URL di ping notifica email.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyEmailURL,
		Validate:    validateOptionalHealthcheckPingURL,
	}
	notifyTelegram := &components.FormField{
		Label:       "Notify Telegram URL",
		Description: "HEALTHCHECK_NOTIFY_TELEGRAM_URL (opzionale): URL di ping notifica Telegram.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyTelegramURL,
		Validate:    validateOptionalHealthcheckPingURL,
	}
	notifyGotify := &components.FormField{
		Label:       "Notify Gotify URL",
		Description: "HEALTHCHECK_NOTIFY_GOTIFY_URL (opzionale): URL di ping notifica Gotify.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyGotifyURL,
		Validate:    validateOptionalHealthcheckPingURL,
	}
	notifyWebhook := &components.FormField{
		Label:       "Notify webhook URL",
		Description: "HEALTHCHECK_NOTIFY_WEBHOOK_URL (opzionale): URL di ping notifica webhook.",
		Kind:        components.FieldText,
		Text:        prefill.NotifyWebhookURL,
		Validate:    validateOptionalHealthcheckPingURL,
	}

	fields := []*components.FormField{
		alive, backup, updates,
		notifyEmail, notifyTelegram, notifyGotify, notifyWebhook,
	}
	if _, err := shell.Ask(ctx, session, components.NewFormGrid(
		"Healthchecks - parametri server personale", fields,
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
