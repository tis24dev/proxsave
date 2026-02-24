package wizard

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
	"github.com/tis24dev/proxsave/pkg/utils"
)

type installWizardPrefill struct {
	SecondaryEnabled   bool
	SecondaryPath      string
	SecondaryLogPath   string
	CloudEnabled       bool
	CloudRemote        string
	CloudLogPath       string
	FirewallEnabled    bool
	TelegramEnabled    bool
	EmailEnabled       bool
	EncryptionEnabled  bool
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
	CronTime               string // HH:MM
	EnableEncryption       bool
}

// ExistingConfigAction represents how to handle an already-present configuration file.
type ExistingConfigAction int

const (
	ExistingConfigOverwrite ExistingConfigAction = iota // Start from embedded template (overwrite)
	ExistingConfigEdit                                  // Keep existing file as base and edit
	ExistingConfigSkip                                  // Leave the file untouched and skip wizard
)

var (
	// ErrInstallCancelled is returned when the user aborts the install wizard.
	ErrInstallCancelled       = errors.New("installation aborted by user")
	checkExistingConfigRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return app.SetRoot(root, true).SetFocus(focus).Run()
	}
)

// RunInstallWizard runs the TUI-based installation wizard
func RunInstallWizard(ctx context.Context, configPath string, baseDir string, buildSig string, baseTemplate string) (*InstallWizardData, error) {
	defaultFirewallRules := false
	data := &InstallWizardData{
		BaseDir:             baseDir,
		ConfigPath:          configPath,
		CronTime:            "02:00",
		EnableEncryption:    false, // Default to disabled
		BackupFirewallRules: &defaultFirewallRules,
	}

	app := tui.NewApp()

	prefill := deriveInstallWizardPrefill(baseTemplate)

	// Build the form
	form := components.NewForm(app)

	// Welcome text
	welcomeText := tview.NewTextView().
		SetText("Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n" +
			"This wizard will guide you through configuring your backup system for Proxmox.\n" +
			"All settings can be changed later by editing the configuration file.").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	// Navigation instructions
	navInstructions := tview.NewTextView().
		SetText("[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to open dropdowns | ←→ on buttons | ENTER to submit | Mouse clicks enabled").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

	// Add separator
	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

	// Track if any dropdown is currently open
	var dropdownOpen bool

	// Secondary Storage section
	secondaryEnabled := prefill.SecondaryEnabled
	var secondaryPathField, secondaryLogField *tview.InputField

	secondaryDropdown := tview.NewDropDown().
		SetLabel("Enable Secondary Storage").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			secondaryEnabled = (option == "Yes")
			// Show/hide secondary path fields
			if secondaryPathField != nil {
				secondaryPathField.SetDisabled(!secondaryEnabled)
			}
			if secondaryLogField != nil {
				secondaryLogField.SetDisabled(!secondaryEnabled)
			}
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(secondaryEnabled))

	secondaryDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})

	form.Form.AddFormItem(secondaryDropdown)

	secondaryHint := tview.NewInputField().
		SetLabel("  tip: SECONDARY_PATH needs a mounted path; for 192.168.0.10/folder use an rclone remote").
		SetFieldWidth(0).
		SetText("")
	secondaryHint.SetDisabled(true)
	form.Form.AddFormItem(secondaryHint)

	secondaryPathField = tview.NewInputField().
		SetLabel("  └─ Secondary Backup Path").
		SetText("/mnt/secondary-backup").
		SetFieldWidth(40)
	if prefill.SecondaryPath != "" {
		secondaryPathField.SetText(prefill.SecondaryPath)
	}
	secondaryPathField.SetDisabled(!secondaryEnabled)
	form.Form.AddFormItem(secondaryPathField)

	secondaryLogField = tview.NewInputField().
		SetLabel("  └─ Secondary Log Path").
		SetText("/mnt/secondary-backup/logs").
		SetFieldWidth(40)
	if prefill.SecondaryLogPath != "" {
		secondaryLogField.SetText(prefill.SecondaryLogPath)
	}
	secondaryLogField.SetDisabled(!secondaryEnabled)
	form.Form.AddFormItem(secondaryLogField)

	// Cloud Storage section
	cloudEnabled := prefill.CloudEnabled
	var rcloneBackupField, rcloneLogField *tview.InputField

	cloudDropdown := tview.NewDropDown().
		SetLabel("Enable Cloud Storage (rclone)").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			cloudEnabled = (option == "Yes")
			if rcloneBackupField != nil {
				rcloneBackupField.SetDisabled(!cloudEnabled)
			}
			if rcloneLogField != nil {
				rcloneLogField.SetDisabled(!cloudEnabled)
			}
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(cloudEnabled))

	cloudDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})

	form.Form.AddFormItem(cloudDropdown)

	cloudHint := tview.NewInputField().
		SetLabel("  Tip: remote name (via 'rclone config'), e.g. myremote (or myremote:path)").
		SetFieldWidth(0).
		SetText("")
	cloudHint.SetDisabled(true)
	form.Form.AddFormItem(cloudHint)

	rcloneBackupField = tview.NewInputField().
		SetLabel("  └─ Rclone Backup Remote").
		SetText("myremote:pbs-backups").
		SetFieldWidth(40)
	if prefill.CloudRemote != "" {
		rcloneBackupField.SetText(prefill.CloudRemote)
	}
	rcloneBackupField.SetDisabled(!cloudEnabled)
	form.Form.AddFormItem(rcloneBackupField)

	rcloneLogField = tview.NewInputField().
		SetLabel("  └─ Rclone Log Path").
		SetText("myremote:pbs-logs").
		SetFieldWidth(40)
	if prefill.CloudLogPath != "" {
		rcloneLogField.SetText(prefill.CloudLogPath)
	}
	rcloneLogField.SetDisabled(!cloudEnabled)
	form.Form.AddFormItem(rcloneLogField)

	// Firewall rules backup (system collection)
	firewallEnabled := prefill.FirewallEnabled
	firewallDropdown := tview.NewDropDown().
		SetLabel("Backup Firewall Rules (iptables/nftables)").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			firewallEnabled = (option == "Yes")
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(firewallEnabled))

	firewallDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})

	form.Form.AddFormItem(firewallDropdown)

	// Notifications (header + two toggles)
	telegramEnabled := prefill.TelegramEnabled
	emailEnabled := prefill.EmailEnabled
	notificationHeader := tview.NewInputField().
		SetLabel("Notifications").
		SetFieldWidth(0).
		SetText("").
		SetDisabled(true)
	form.Form.AddFormItem(notificationHeader)

	telegramDropdown := tview.NewDropDown().
		SetLabel("  └─ Enable Telegram notifications").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			telegramEnabled = (option == "Yes")
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(telegramEnabled))
	telegramDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})
	form.Form.AddFormItem(telegramDropdown)

	emailDropdown := tview.NewDropDown().
		SetLabel("  └─ Enable Email notifications").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			emailEnabled = (option == "Yes")
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(emailEnabled))
	emailDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})
	form.Form.AddFormItem(emailDropdown)

	// Encryption
	encryptionDropdown := tview.NewDropDown().
		SetLabel("Enable Backup Encryption (AGE)").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			dropdownOpen = false
		}).
		SetCurrentOption(boolToOptionIndex(prefill.EncryptionEnabled))

	encryptionDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})

	form.Form.AddFormItem(encryptionDropdown)

	// Separator before scheduling
	cronSeparator := tview.NewInputField().
		SetLabel(strings.Repeat("─", 40)).
		SetFieldWidth(0).
		SetText("").
		SetDisabled(true)
	form.Form.AddFormItem(cronSeparator)

	// Cron schedule (after encryption)
	cronField := tview.NewInputField().
		SetLabel("Cron time (HH:MM)").
		SetText("").
		SetPlaceholder(data.CronTime).
		SetFieldWidth(7)
	form.Form.AddFormItem(cronField)

	// Set up form submission
	form.SetOnSubmit(func(values map[string]string) error {
		// Collect data
		data.EnableSecondaryStorage = secondaryEnabled
		if secondaryEnabled {
			data.SecondaryPath = secondaryPathField.GetText()
			data.SecondaryLogPath = secondaryLogField.GetText()

			// Validate paths
			if !filepath.IsAbs(data.SecondaryPath) {
				return fmt.Errorf("secondary backup path must be absolute")
			}
			if !filepath.IsAbs(data.SecondaryLogPath) {
				return fmt.Errorf("secondary log path must be absolute")
			}
		}

		data.EnableCloudStorage = cloudEnabled
		if cloudEnabled {
			data.RcloneBackupRemote = strings.TrimSpace(rcloneBackupField.GetText())
			data.RcloneLogRemote = strings.TrimSpace(rcloneLogField.GetText())

			// Validate rclone inputs (allow both "remote" and "remote:path", logs can also be path-only)
			if data.RcloneBackupRemote == "" {
				return fmt.Errorf("rclone backup remote cannot be empty")
			}
			if data.RcloneLogRemote == "" {
				return fmt.Errorf("rclone log path cannot be empty")
			}
		}

		data.BackupFirewallRules = &firewallEnabled

		// Get notification mode from two toggles
		switch {
		case telegramEnabled && emailEnabled:
			data.NotificationMode = "both"
		case telegramEnabled:
			data.NotificationMode = "telegram"
		case emailEnabled:
			data.NotificationMode = "email"
		default:
			data.NotificationMode = "none"
		}

		// Get encryption setting
		data.EnableEncryption = values["Enable Backup Encryption (AGE)"] == "Yes"

		// Cron time validation (HH:MM)
		cron := strings.TrimSpace(cronField.GetText())
		if cron == "" {
			cron = "02:00"
		}
		parts := strings.Split(cron, ":")
		if len(parts) != 2 {
			return fmt.Errorf("cron time must be in HH:MM format")
		}
		hour, err := strconv.Atoi(parts[0])
		if err != nil || hour < 0 || hour > 23 {
			return fmt.Errorf("cron hour must be between 00 and 23")
		}
		minute, err := strconv.Atoi(parts[1])
		if err != nil || minute < 0 || minute > 59 {
			return fmt.Errorf("cron minute must be between 00 and 59")
		}
		data.CronTime = fmt.Sprintf("%02d:%02d", hour, minute)

		return nil
	})

	form.SetOnCancel(func() {
		// User cancelled
		data = nil
	})

	// Add buttons
	form.AddSubmitButton("Install")
	form.AddCancelButton("Cancel")

	// Style the form
	form.SetBorderWithTitle("ProxSave Installation")
	form.Form.SetBackgroundColor(tcell.ColorBlack)

	// Add arrow key support for navigation
	form.Form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// If a dropdown is open, don't intercept arrow keys - let them work naturally
		if dropdownOpen {
			return event
		}

		// Check if focus is on a button (not on a form field)
		formItemIndex, buttonIndex := form.Form.GetFocusedItemIndex()
		isOnButton := (formItemIndex < 0 && buttonIndex >= 0)
		isOnFormField := (formItemIndex >= 0)

		if isOnButton {
			// When on buttons, convert arrows to Backtab/Tab for navigation
			switch event.Key() {
			case tcell.KeyLeft, tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyRight, tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		} else if isOnFormField {
			// When on form fields, convert up/down arrows to Backtab/Tab
			switch event.Key() {
			case tcell.KeyUp:
				return tcell.NewEventKey(tcell.KeyBacktab, 0, tcell.ModNone)
			case tcell.KeyDown:
				return tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone)
			}
		}
		return event
	})

	// Config path footer
	configPathText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Configuration file:[white] %s", configPath)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	configPathText.SetBorder(false)

	buildSigText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	buildSigText.SetBorder(false)

	// Create layout
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(form.Form, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(" ProxSave Installation ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	// Run the app - ignore errors from normal app termination
	_ = app.SetRoot(flex, true).SetFocus(form.Form).Run()

	if data == nil {
		return nil, ErrInstallCancelled
	}

	return data, nil
}

// ApplyInstallData applies the collected data to the config template.
// If baseTemplate is empty, the embedded default template is used.
func ApplyInstallData(baseTemplate string, data *InstallWizardData) (string, error) {
	template := baseTemplate
	editingExisting := strings.TrimSpace(baseTemplate) != ""
	existingValues := map[string]string{}
	if editingExisting {
		existingValues = parseEnvTemplate(baseTemplate)
	}
	if strings.TrimSpace(template) == "" {
		template = config.DefaultEnvTemplate()
	}

	// BASE_DIR is auto-detected at runtime from the executable/config location.
	// Keep it out of backup.env to avoid pinning the installation to a specific path.
	template = unsetEnvValue(template, "BASE_DIR")
	template = unsetEnvValue(template, "CRON_SCHEDULE")
	template = unsetEnvValue(template, "CRON_HOUR")
	template = unsetEnvValue(template, "CRON_MINUTE")

	// Apply secondary storage
	if data.EnableSecondaryStorage {
		template = setEnvValue(template, "SECONDARY_ENABLED", "true")
		template = setEnvValue(template, "SECONDARY_PATH", data.SecondaryPath)
		template = setEnvValue(template, "SECONDARY_LOG_PATH", data.SecondaryLogPath)
	} else {
		template = setEnvValue(template, "SECONDARY_ENABLED", "false")
	}

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
		// Preserve existing delivery preferences when editing an existing config.
		if !editingExisting || strings.TrimSpace(existingValues["EMAIL_DELIVERY_METHOD"]) == "" {
			template = setEnvValue(template, "EMAIL_DELIVERY_METHOD", "relay")
		}
		if !editingExisting || strings.TrimSpace(existingValues["EMAIL_FALLBACK_SENDMAIL"]) == "" {
			template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", "true")
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

	return template, nil
}

// setEnvValue sets or updates an environment variable in the template
func setEnvValue(template, key, value string) string {
	return utils.SetEnvValue(template, key, value)
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

func boolToOptionIndex(value bool) int {
	if value {
		return 1
	}
	return 0
}

func deriveInstallWizardPrefill(baseTemplate string) installWizardPrefill {
	out := installWizardPrefill{}
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
	out.EmailEnabled = readTemplateBool(values, "EMAIL_ENABLED")

	out.EncryptionEnabled = readTemplateBool(values, "ENCRYPT_ARCHIVE")

	return out
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

// CheckExistingConfig checks if config file exists and asks how to proceed
func CheckExistingConfig(configPath string, buildSig string) (ExistingConfigAction, error) {
	if _, err := os.Stat(configPath); err == nil {
		// File exists, ask how to proceed
		app := tui.NewApp()
		action := ExistingConfigSkip

		// Welcome text (same as main wizard)
		welcomeText := tview.NewTextView().
			SetText("Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n" +
				"This wizard will guide you through configuring your backup system for Proxmox.\n" +
				"All settings can be changed later by editing the configuration file.").
			SetTextColor(tui.ProxmoxLight).
			SetDynamicColors(true)
		welcomeText.SetBorder(false)

		// Navigation instructions (no dropdowns in this view)
		navInstructions := tview.NewTextView().
			SetText("[yellow]Navigation:[white] Press [yellow]TAB[white] or [yellow]↑↓[white] to move between fields | " +
				"Use [yellow]←→[white] on buttons | Press [yellow]ENTER[white] to submit | Mouse clicks enabled").
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true).
			SetTextAlign(tview.AlignCenter)
		navInstructions.SetBorder(false)

		buildSigText := tview.NewTextView().
			SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
			SetTextColor(tcell.ColorWhite).
			SetDynamicColors(true).
			SetTextAlign(tview.AlignCenter)
		buildSigText.SetBorder(false)

		// Separator
		separator := tview.NewTextView().
			SetText(strings.Repeat("─", 80)).
			SetTextColor(tui.ProxmoxOrange)
		separator.SetBorder(false)

		// Confirmation modal
		modal := tview.NewModal().
			SetText(fmt.Sprintf("Configuration file already exists at:\n[yellow]%s[white]\n\n"+
				"Choose how to proceed:\n"+
				"[yellow]Overwrite[white]   - Start from embedded template\n"+
				"[yellow]Edit existing[white] - Keep current file as base\n"+
				"[yellow]Keep & exit[white]   - Leave file untouched, exit wizard", configPath)).
			AddButtons([]string{"Overwrite", "Edit existing", "Keep & exit"}).
			SetDoneFunc(func(buttonIndex int, buttonLabel string) {
				switch buttonLabel {
				case "Overwrite":
					action = ExistingConfigOverwrite
				case "Edit existing":
					action = ExistingConfigEdit
				default:
					action = ExistingConfigSkip
				}
				app.Stop()
			})

		modal.SetBorder(true).
			SetTitle(" Configuration Exists ").
			SetTitleAlign(tview.AlignCenter).
			SetTitleColor(tui.WarningYellow).
			SetBorderColor(tui.WarningYellow).
			SetBackgroundColor(tcell.ColorBlack)

			// Create layout with welcome text at top
		flex := tview.NewFlex().
			SetDirection(tview.FlexRow).
			AddItem(welcomeText, 5, 0, false).
			AddItem(navInstructions, 2, 0, false).
			AddItem(separator, 1, 0, false).
			AddItem(modal, 0, 1, true).
			AddItem(buildSigText, 1, 0, false)

		flex.SetBorder(true).
			SetTitle(" ProxSave Installation ").
			SetTitleAlign(tview.AlignCenter).
			SetTitleColor(tui.ProxmoxOrange).
			SetBorderColor(tui.ProxmoxOrange).
			SetBackgroundColor(tcell.ColorBlack)

		// Run the modal - ignore errors from normal app termination
		_ = checkExistingConfigRunner(app, flex, modal)

		return action, nil
	} else if !os.IsNotExist(err) {
		return ExistingConfigSkip, err
	}

	return ExistingConfigOverwrite, nil // File doesn't exist, proceed
}
