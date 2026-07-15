// Package installer holds the UI-agnostic install engine shared by the CLI
// wizard and the Charm install flow: the wizard data model, the config
// template builder, prefill derivation from an existing backup.env, and the
// post-install audit collector. Moved verbatim from internal/tui/wizard as
// part of the tview-to-Charm migration (Phase 4); the characterization
// goldens in cmd/proxsave/testdata/install_characterization lock behavior.
package installer

import (
	"bufio"
	"errors"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/pkg/utils"
)

// ErrNilInstallData is returned when ApplyInstallData or its validators
// receive a nil payload.
var ErrNilInstallData = errors.New("install wizard data cannot be nil")

// SetEnvValueInTemplate sets or updates an environment variable in the template.
func SetEnvValueInTemplate(template, key, value string) string {
	return utils.SetEnvValue(template, key, value)
}

func setEnvValue(template, key, value string) string {
	return SetEnvValueInTemplate(template, key, value)
}

// InstallWizardPrefill holds the values parsed from an existing backup.env so a
// re-run of the install wizard can default to them. It is the single source of
// truth shared by the TUI wizard and the CLI wizard (cmd/proxsave) — see
// DeriveInstallWizardPrefill — so both modes default to the stored config and
// neither silently resets a toggle on a no-op edit.
type InstallWizardPrefill struct {
	SecondaryEnabled    bool
	SecondaryPath       string
	SecondaryLogPath    string
	CloudEnabled        bool
	CloudRemote         string
	CloudLogPath        string
	FirewallEnabled     bool
	TelegramEnabled     bool
	TelegramType        string
	EmailEnabled        bool
	EmailDeliveryMethod string
	EncryptionEnabled   bool
	SchedulerMode       string // "cron" | "daemon" (empty on a fresh config)
	SchedulerTime       string // HH:MM "Run at" time (empty on a fresh config)
	HealthcheckMode     string // "off" | "centralized" | "self" (empty on a fresh/pre-daemon config)
}

// InstallWizardData holds the collected installation data
type InstallWizardData struct {
	BaseDir                string
	ConfigPath             string
	EnableSecondaryStorage bool
	SecondaryPath          string
	SecondaryLogPath       string
	EnableCloudStorage     bool
	RcloneBackupRemote     string
	RcloneLogRemote        string
	BackupFirewallRules    *bool
	NotificationMode       string // "none", "telegram", "email", "both"
	EmailDeliveryMethod    string // "relay", "sendmail", or "pmf"
	EmailFallbackSendmail  *bool
	CronTime               string // HH:MM (the "Run at" time)
	EnableEncryption       bool
	SchedulerMode          string // "cron" | "daemon"
	HealthcheckMode        string // "off" | "centralized" | "self"; empty with daemon -> backward-compat centralized-on
}

// HealthcheckSelfParams holds the full ping URLs the self-mode params screen
// collects. AliveURL and BackupURL are required by the UI; the remaining sensors
// are optional. Each non-empty value is written verbatim into the matching
// HEALTHCHECK_*_URL key (the daemon's selfURLs() already prefers the full-URL
// branch over PING_ENDPOINT+ID), and empty values leave the key blank.
type HealthcheckSelfParams struct {
	AliveURL          string
	BackupURL         string
	UpdatesURL        string
	NotifyEmailURL    string
	NotifyTelegramURL string
	NotifyGotifyURL   string
	NotifyWebhookURL  string
}

// normalizeHealthcheckMode maps a raw mode string to one of "off" | "centralized"
// | "self". Any unrecognised non-empty value falls back to "centralized"; an empty
// value is returned as "" so callers can apply their own default (e.g. the
// backward-compat daemon default in ApplyInstallData).
func normalizeHealthcheckMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return ""
	case "off":
		return "off"
	case "self":
		return "self"
	default:
		return "centralized"
	}
}

// ApplyHealthcheckSelfParams writes the self-mode full ping URLs into the template.
// Every non-empty URL is set on its HEALTHCHECK_*_URL key; empty URLs are written as
// blank so a cleared field wipes a stale value. It does NOT touch HEALTHCHECK_ENABLED
// or HEALTHCHECK_MODE (ApplyInstallData owns those); it only fills the ping targets.
func ApplyHealthcheckSelfParams(template string, p HealthcheckSelfParams) string {
	set := func(tmpl, key, val string) string {
		return setEnvValue(tmpl, key, strings.TrimSpace(val))
	}
	template = set(template, "HEALTHCHECK_ALIVE_URL", p.AliveURL)
	template = set(template, "HEALTHCHECK_BACKUP_URL", p.BackupURL)
	template = set(template, "HEALTHCHECK_UPDATES_URL", p.UpdatesURL)
	template = set(template, "HEALTHCHECK_NOTIFY_EMAIL_URL", p.NotifyEmailURL)
	template = set(template, "HEALTHCHECK_NOTIFY_TELEGRAM_URL", p.NotifyTelegramURL)
	template = set(template, "HEALTHCHECK_NOTIFY_GOTIFY_URL", p.NotifyGotifyURL)
	template = set(template, "HEALTHCHECK_NOTIFY_WEBHOOK_URL", p.NotifyWebhookURL)
	return template
}

// DeriveHealthcheckSelfParams reads the self-mode full ping URLs back out of an
// existing template so a re-run of the params screen defaults to the stored values.
func DeriveHealthcheckSelfParams(template string) HealthcheckSelfParams {
	values := parseEnvTemplate(template)
	return HealthcheckSelfParams{
		AliveURL:          readTemplateString(values, "HEALTHCHECK_ALIVE_URL"),
		BackupURL:         readTemplateString(values, "HEALTHCHECK_BACKUP_URL"),
		UpdatesURL:        readTemplateString(values, "HEALTHCHECK_UPDATES_URL"),
		NotifyEmailURL:    readTemplateString(values, "HEALTHCHECK_NOTIFY_EMAIL_URL"),
		NotifyTelegramURL: readTemplateString(values, "HEALTHCHECK_NOTIFY_TELEGRAM_URL"),
		NotifyGotifyURL:   readTemplateString(values, "HEALTHCHECK_NOTIFY_GOTIFY_URL"),
		NotifyWebhookURL:  readTemplateString(values, "HEALTHCHECK_NOTIFY_WEBHOOK_URL"),
	}
}

// ExistingConfigAction represents how to handle an already-present configuration file.

// If baseTemplate is empty, the embedded default template is used.
func ApplyInstallData(baseTemplate string, data *InstallWizardData) (string, error) {
	if data == nil {
		return "", ErrNilInstallData
	}

	template := baseTemplate
	editingExisting := strings.TrimSpace(baseTemplate) != ""
	existingValues := map[string]string{}
	if editingExisting {
		existingValues = parseEnvTemplate(baseTemplate)
	}
	if strings.TrimSpace(template) == "" {
		template = config.DefaultEnvTemplate()
	}
	if err := validateSecondaryInstallData(data); err != nil {
		return "", err
	}
	if err := validateCloudInstallData(data); err != nil {
		return "", err
	}

	// BASE_DIR and cron values are derived at runtime/finalization time.
	// Keep them out of backup.env to avoid pinning the installation.
	template = config.RemoveRuntimeDerivedEnvKeys(template)

	// Apply secondary storage
	template = config.ApplySecondaryStorageSettings(
		template,
		data.EnableSecondaryStorage,
		data.SecondaryPath,
		data.SecondaryLogPath,
	)

	// Apply cloud storage
	if data.EnableCloudStorage {
		template = setEnvValue(template, "CLOUD_ENABLED", "true")
		template = setEnvValue(template, "CLOUD_REMOTE", data.RcloneBackupRemote)
		template = setEnvValue(template, "CLOUD_LOG_PATH", data.RcloneLogRemote)
	} else {
		template = setEnvValue(template, "CLOUD_ENABLED", "false")
		template = setEnvValue(template, "CLOUD_REMOTE", "")
		template = setEnvValue(template, "CLOUD_LOG_PATH", "")
	}

	// Apply firewall rules backup (optional; keep template default when unset)
	if data.BackupFirewallRules != nil {
		if *data.BackupFirewallRules {
			template = setEnvValue(template, "BACKUP_FIREWALL_RULES", "true")
		} else {
			template = setEnvValue(template, "BACKUP_FIREWALL_RULES", "false")
		}
	}

	// Apply notifications
	if data.NotificationMode == "telegram" || data.NotificationMode == "both" {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "true")
		// Preserve existing telegram mode when editing an existing config.
		if !editingExisting || strings.TrimSpace(existingValues["BOT_TELEGRAM_TYPE"]) == "" {
			template = setEnvValue(template, "BOT_TELEGRAM_TYPE", "centralized")
		}
	} else {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "false")
	}

	if data.NotificationMode == "email" || data.NotificationMode == "both" {
		template = setEnvValue(template, "EMAIL_ENABLED", "true")
		method := strings.TrimSpace(data.EmailDeliveryMethod)
		if method == "" && editingExisting {
			method = strings.TrimSpace(existingValues["EMAIL_DELIVERY_METHOD"])
		}
		method = installEmailDeliveryMethodOrDefault(method)
		template = setEnvValue(template, "EMAIL_DELIVERY_METHOD", method)

		fallbackRaw := readTemplateString(existingValues, "EMAIL_FALLBACK_SENDMAIL", "EMAIL_FALLBACK_PMF")
		switch {
		case data.EmailFallbackSendmail != nil:
			template = unsetEnvValue(template, "EMAIL_FALLBACK_PMF")
			template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", fmt.Sprintf("%t", *data.EmailFallbackSendmail))
		case fallbackRaw == "":
			template = unsetEnvValue(template, "EMAIL_FALLBACK_PMF")
			template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", "true")
		case strings.TrimSpace(existingValues["EMAIL_FALLBACK_SENDMAIL"]) == "":
			template = unsetEnvValue(template, "EMAIL_FALLBACK_PMF")
			template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", fmt.Sprintf("%t", utils.ParseBool(fallbackRaw)))
		}
	} else {
		template = setEnvValue(template, "EMAIL_ENABLED", "false")
	}

	// Apply encryption
	if data.EnableEncryption {
		template = setEnvValue(template, "ENCRYPT_ARCHIVE", "true")
	} else {
		template = setEnvValue(template, "ENCRYPT_ARCHIVE", "false")
	}

	// Apply scheduler engine + the daily "Run at" time. SCHEDULER_TIME is a real
	// daemon config key (unlike the removed CRON_* keys), so it is written here.
	mode := normalizeSchedulerMode(data.SchedulerMode)
	template = setEnvValue(template, "SCHEDULER_MODE", mode)
	if strings.TrimSpace(data.CronTime) != "" {
		template = setEnvValue(template, "SCHEDULER_TIME", strings.TrimSpace(data.CronTime))
	}

	// Apply the healthchecks connector mode. Healthchecks require the daemon (the
	// sole pinger), so in cron mode the connector is always off. In daemon mode the
	// user's explicit choice wins; an EMPTY choice with the daemon keeps the previous
	// behaviour (centralized-on out of the box) so pre-UI callers still work.
	hcMode := normalizeHealthcheckMode(data.HealthcheckMode)
	if mode != "daemon" {
		hcMode = "off"
	} else if hcMode == "" {
		hcMode = "centralized"
	}
	// HEALTHCHECK_ALIVE_URL/BACKUP_URL are dual-purpose (self: the user's full ping
	// URLs; centralized: a cache the server auto-fills). Reset them symmetrically on
	// every mode switch so a leftover self URL never lingers as the centralized cache
	// (which the daemon would ping if the server fetch fails). Self rewrites them via
	// the params screen right after; centralized/off leave them blank (the server
	// repopulates the centralized cache on next fetch).
	switch hcMode {
	case "off":
		template = setEnvValue(template, "HEALTHCHECK_ENABLED", "false")
		template = setEnvValue(template, "HEALTHCHECK_ALIVE_URL", "")
		template = setEnvValue(template, "HEALTHCHECK_BACKUP_URL", "")
	case "self":
		template = setEnvValue(template, "HEALTHCHECK_ENABLED", "true")
		template = setEnvValue(template, "HEALTHCHECK_MODE", "self")
		// Clear the centralized cache so a switch to self does not ping a stale
		// server-minted URL before the params screen writes the user's own URLs.
		template = setEnvValue(template, "HEALTHCHECK_ALIVE_URL", "")
		template = setEnvValue(template, "HEALTHCHECK_BACKUP_URL", "")
	default: // centralized
		template = setEnvValue(template, "HEALTHCHECK_ENABLED", "true")
		template = setEnvValue(template, "HEALTHCHECK_MODE", "centralized")
		// Clear any leftover self URL so it can't be pinged as the centralized
		// cache; the server repopulates this cache on the next fetch.
		template = setEnvValue(template, "HEALTHCHECK_ALIVE_URL", "")
		template = setEnvValue(template, "HEALTHCHECK_BACKUP_URL", "")
	}

	return template, nil
}

// normalizeSchedulerMode maps any unrecognised value to the safe default "cron".
func normalizeSchedulerMode(v string) string {
	if strings.ToLower(strings.TrimSpace(v)) == "daemon" {
		return "daemon"
	}
	return "cron"
}

// ValidateSecondaryInstallData validates the secondary-storage fields.
func ValidateSecondaryInstallData(data *InstallWizardData) error {
	return validateSecondaryInstallData(data)
}

func validateSecondaryInstallData(data *InstallWizardData) error {
	if data == nil {
		return ErrNilInstallData
	}
	if !data.EnableSecondaryStorage {
		return nil
	}
	if err := config.ValidateRequiredSecondaryPath(data.SecondaryPath); err != nil {
		return err
	}
	if err := config.ValidateOptionalSecondaryLogPath(data.SecondaryLogPath); err != nil {
		return err
	}
	return nil
}

// ValidateCloudInstallData validates the cloud-storage fields.
func ValidateCloudInstallData(data *InstallWizardData) error {
	return validateCloudInstallData(data)
}

// validateCloudInstallData mirrors the wizard's own onSubmit checks: when cloud
// storage is enabled both rclone remotes are required. It is a defense-in-depth
// guard so a partially-filled payload can never write CLOUD_ENABLED=true with an
// empty CLOUD_REMOTE/CLOUD_LOG_PATH into the config.
func validateCloudInstallData(data *InstallWizardData) error {
	if data == nil {
		return ErrNilInstallData
	}
	if !data.EnableCloudStorage {
		return nil
	}
	if strings.TrimSpace(data.RcloneBackupRemote) == "" {
		return errors.New("cloud storage is enabled but the rclone backup remote is empty")
	}
	if strings.TrimSpace(data.RcloneLogRemote) == "" {
		return errors.New("cloud storage is enabled but the rclone log path is empty")
	}
	return nil
}

// setEnvValue sets or updates an environment variable in the template

// UnsetEnvValueInTemplate removes a key from the template entirely.
func UnsetEnvValueInTemplate(template, key string) string {
	return unsetEnvValue(template, key)
}

func unsetEnvValue(template, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return template
	}

	lines := strings.Split(template, "\n")
	out := make([]string, 0, len(lines))

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			out = append(out, line)
			continue
		}

		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			out = append(out, line)
			continue
		}

		parsedKey := strings.TrimSpace(parts[0])
		if fields := strings.Fields(parsedKey); len(fields) >= 2 && fields[0] == "export" {
			parsedKey = fields[1]
		}
		if strings.EqualFold(parsedKey, key) {
			continue
		}
		out = append(out, line)
	}

	return strings.Join(out, "\n")
}

// DeriveInstallWizardPrefill parses an existing backup.env template into an
// InstallWizardPrefill. Both the TUI wizard and the CLI install wizard call this
// so each prompt/field defaults to the stored value.
func DeriveInstallWizardPrefill(baseTemplate string) InstallWizardPrefill {
	out := InstallWizardPrefill{}
	if strings.TrimSpace(baseTemplate) == "" {
		return out
	}
	values := parseEnvTemplate(baseTemplate)

	out.SecondaryEnabled = readTemplateBool(values, "SECONDARY_ENABLED", "ENABLE_SECONDARY_BACKUP")
	out.SecondaryPath = readTemplateString(values, "SECONDARY_PATH", "SECONDARY_BACKUP_PATH")
	out.SecondaryLogPath = readTemplateString(values, "SECONDARY_LOG_PATH")

	out.CloudEnabled = readTemplateBool(values, "CLOUD_ENABLED", "ENABLE_CLOUD_BACKUP")
	out.CloudRemote = readTemplateString(values, "CLOUD_REMOTE", "RCLONE_REMOTE")
	out.CloudLogPath = readTemplateString(values, "CLOUD_LOG_PATH")

	out.FirewallEnabled = readTemplateBool(values, "BACKUP_FIREWALL_RULES")

	out.TelegramEnabled = readTemplateBool(values, "TELEGRAM_ENABLED")
	out.TelegramType = readTemplateString(values, "BOT_TELEGRAM_TYPE")
	out.EmailEnabled = readTemplateBool(values, "EMAIL_ENABLED")
	out.EmailDeliveryMethod = installEmailDeliveryMethodOrDefault(readTemplateString(values, "EMAIL_DELIVERY_METHOD"))

	out.EncryptionEnabled = readTemplateBool(values, "ENCRYPT_ARCHIVE")
	out.SchedulerMode = readTemplateString(values, "SCHEDULER_MODE")
	out.SchedulerTime = readTemplateString(values, "SCHEDULER_TIME")

	// Healthchecks mode: not-enabled -> "off"; otherwise the normalized MODE value
	// (defaulting to centralized when MODE is absent but the connector is enabled).
	if readTemplateBool(values, "HEALTHCHECK_ENABLED") {
		hcMode := normalizeHealthcheckMode(readTemplateString(values, "HEALTHCHECK_MODE"))
		if hcMode == "" {
			hcMode = "centralized"
		}
		out.HealthcheckMode = hcMode
	} else {
		out.HealthcheckMode = "off"
	}

	return out
}

// ParseEnvTemplate parses KEY=VALUE lines from a backup.env template.
func ParseEnvTemplate(template string) map[string]string {
	return parseEnvTemplate(template)
}

func parseEnvTemplate(template string) map[string]string {
	values := make(map[string]string)

	scanner := bufio.NewScanner(strings.NewReader(template))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if utils.IsComment(trimmed) {
			continue
		}

		key, value, ok := utils.SplitKeyValue(line)
		if !ok {
			continue
		}
		if fields := strings.Fields(key); len(fields) >= 2 && fields[0] == "export" {
			key = fields[1]
		}
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		values[key] = strings.TrimSpace(value)
	}

	return values
}

func readTemplateString(values map[string]string, keys ...string) string {
	for _, key := range keys {
		key = strings.ToUpper(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if val, ok := values[key]; ok {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func readTemplateBool(values map[string]string, keys ...string) bool {
	raw := readTemplateString(values, keys...)
	if strings.TrimSpace(raw) == "" {
		return false
	}
	return utils.ParseBool(raw)
}

// EmailDeliveryMethodOrDefault normalizes an email delivery method,
// defaulting to relay.
func EmailDeliveryMethodOrDefault(method string) string {
	return installEmailDeliveryMethodOrDefault(method)
}

func installEmailDeliveryMethodOrDefault(method string) string {
	if strings.TrimSpace(method) == "" {
		return "relay"
	}
	normalized := config.NormalizeEmailDeliveryMethod(method)
	switch normalized {
	case "relay", "sendmail", "pmf":
		return normalized
	default:
		return "relay"
	}
}
