package wizard

import (
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
)

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
func RunInstallWizard(ctx context.Context, configPath string, baseDir string, buildSig string) (*InstallWizardData, error) {
	defaultFirewallRules := false
	data := &InstallWizardData{
		BaseDir:          baseDir,
		ConfigPath:       configPath,
		CronTime:         "02:00",
		EnableEncryption: false, // Default to disabled
		BackupFirewallRules: &defaultFirewallRules,
	}

	app := tui.NewApp()

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
	var secondaryEnabled bool
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
		SetCurrentOption(0)

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
	secondaryPathField.SetDisabled(true)
	form.Form.AddFormItem(secondaryPathField)

	secondaryLogField = tview.NewInputField().
		SetLabel("  └─ Secondary Log Path").
		SetText("/mnt/secondary-backup/logs").
		SetFieldWidth(40)
	secondaryLogField.SetDisabled(true)
	form.Form.AddFormItem(secondaryLogField)

	// Cloud Storage section
	var cloudEnabled bool
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
		SetCurrentOption(0)

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
		SetLabel("  Tip: remotename:path (via 'rclone config'), e.g. myremote:pbs-backups").
		SetFieldWidth(0).
		SetText("")
	cloudHint.SetDisabled(true)
	form.Form.AddFormItem(cloudHint)

	rcloneBackupField = tview.NewInputField().
		SetLabel("  └─ Rclone Backup Remote").
		SetText("myremote:pbs-backups").
		SetFieldWidth(40)
	rcloneBackupField.SetDisabled(true)
	form.Form.AddFormItem(rcloneBackupField)

	rcloneLogField = tview.NewInputField().
		SetLabel("  └─ Rclone Log Remote").
		SetText("myremote:pbs-logs").
		SetFieldWidth(40)
		rcloneLogField.SetDisabled(true)
		form.Form.AddFormItem(rcloneLogField)

		// Firewall rules backup (system collection)
		firewallEnabled := false
		firewallDropdown := tview.NewDropDown().
			SetLabel("Backup Firewall Rules (iptables/nftables)").
			SetOptions([]string{"No", "Yes"}, func(option string, index int) {
				firewallEnabled = (option == "Yes")
				dropdownOpen = false
			}).
			SetCurrentOption(0)

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
		var telegramEnabled, emailEnabled bool
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
		SetCurrentOption(0)
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
		SetCurrentOption(0)
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
		SetCurrentOption(0)

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
			data.RcloneBackupRemote = rcloneBackupField.GetText()
			data.RcloneLogRemote = rcloneLogField.GetText()

			// Validate rclone remotes
			if !strings.Contains(data.RcloneBackupRemote, ":") {
				return fmt.Errorf("rclone backup remote must be in format 'remote:path'")
			}
			if !strings.Contains(data.RcloneLogRemote, ":") {
				return fmt.Errorf("rclone log remote must be in format 'remote:path'")
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
	if strings.TrimSpace(template) == "" {
		template = config.DefaultEnvTemplate()
	}

	// Apply BASE_DIR
	template = setEnvValue(template, "BASE_DIR", data.BaseDir)

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
		template = setEnvValue(template, "BOT_TELEGRAM_TYPE", "centralized")
	} else {
		template = setEnvValue(template, "TELEGRAM_ENABLED", "false")
	}

	if data.NotificationMode == "email" || data.NotificationMode == "both" {
		template = setEnvValue(template, "EMAIL_ENABLED", "true")
		template = setEnvValue(template, "EMAIL_DELIVERY_METHOD", "relay")
		template = setEnvValue(template, "EMAIL_FALLBACK_SENDMAIL", "true")
	} else {
		template = setEnvValue(template, "EMAIL_ENABLED", "false")
	}

	// Apply cron schedule
	cron := strings.TrimSpace(data.CronTime)
	if cron == "" {
		cron = "02:00"
	}
	if parts := strings.Split(cron, ":"); len(parts) == 2 {
		min := strings.TrimSpace(parts[1])
		hr := strings.TrimSpace(parts[0])
		template = setEnvValue(template, "CRON_SCHEDULE", fmt.Sprintf("%s %s * * *", min, hr))
		template = setEnvValue(template, "CRON_HOUR", hr)
		template = setEnvValue(template, "CRON_MINUTE", min)
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
	lines := strings.Split(template, "\n")
	found := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + "=" + value
			found = true
			break
		}
	}

	if !found {
		// Add at the end
		lines = append(lines, key+"="+value)
	}

	return strings.Join(lines, "\n")
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
