package wizard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

type ExistingConfigAction int

const (
	ExistingConfigOverwrite    ExistingConfigAction = iota // Start from embedded template (overwrite)
	ExistingConfigEdit                                     // Keep existing file as base and edit
	ExistingConfigKeepContinue                             // Leave file untouched and continue installation
	ExistingConfigCancel                                   // Abort installation
)

var (
	installEmailDeliveryOptions = []struct {
		Label  string
		Method string
	}{
		{Label: "Cloud relay (relay)", Method: "relay"},
		{Label: "Local sendmail (sendmail)", Method: "sendmail"},
		{Label: "Proxmox Notifications (pmf)", Method: "pmf"},
	}

	// ErrInstallCancelled is returned when the user aborts the install wizard.
	ErrInstallCancelled    = errors.New("installation aborted by user")
	runInstallWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		app.SetRoot(root, true)
		app.SetFocus(focus)
		return app.RunWithContext(ctx)
	}
	checkExistingConfigRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		app.SetRoot(root, true)
		app.SetFocus(focus)
		return app.RunWithContext(ctx)
	}
)

// RunInstallWizard runs the TUI-based installation wizard
func RunInstallWizard(ctx context.Context, configPath string, baseDir string, buildSig string, baseTemplate string) (*InstallWizardData, error) {
	defaultFirewallRules := false
	defaultEmailFallbackSendmail := true
	data := &InstallWizardData{
		BaseDir:               baseDir,
		ConfigPath:            configPath,
		CronTime:              cronutil.DefaultTime,
		EnableEncryption:      false, // Default to disabled
		BackupFirewallRules:   &defaultFirewallRules,
		EmailDeliveryMethod:   "relay",
		EmailFallbackSendmail: &defaultEmailFallbackSendmail,
	}

	app := tui.NewApp()

	prefill := DeriveInstallWizardPrefill(baseTemplate)

	// Build the form
	form := components.NewForm(app)

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

	form.AddFormItem(secondaryDropdown)

	secondaryHint := tview.NewInputField().
		SetLabel("  tip: SECONDARY_PATH needs a mounted path; for 192.168.0.10/folder use an rclone remote").
		SetFieldWidth(0).
		SetText("")
	secondaryHint.SetDisabled(true)
	form.AddFormItem(secondaryHint)

	secondaryPathField = tview.NewInputField().
		SetLabel("  └─ Secondary Backup Path").
		SetText("/mnt/secondary-backup").
		SetFieldWidth(40)
	if prefill.SecondaryPath != "" {
		secondaryPathField.SetText(prefill.SecondaryPath)
	}
	secondaryPathField.SetDisabled(!secondaryEnabled)
	form.AddFormItem(secondaryPathField)

	secondaryLogField = tview.NewInputField().
		SetLabel("  └─ Secondary Log Path").
		SetText("/mnt/secondary-backup/logs").
		SetFieldWidth(40)
	if prefill.SecondaryLogPath != "" {
		secondaryLogField.SetText(prefill.SecondaryLogPath)
	}
	secondaryLogField.SetDisabled(!secondaryEnabled)
	form.AddFormItem(secondaryLogField)

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

	form.AddFormItem(cloudDropdown)

	cloudHint := tview.NewInputField().
		SetLabel("  Tip: remote name (via 'rclone config'), e.g. myremote (or myremote:path)").
		SetFieldWidth(0).
		SetText("")
	cloudHint.SetDisabled(true)
	form.AddFormItem(cloudHint)

	rcloneBackupField = tview.NewInputField().
		SetLabel("  └─ Rclone Backup Remote").
		SetText("myremote:pbs-backups").
		SetFieldWidth(40)
	if prefill.CloudRemote != "" {
		rcloneBackupField.SetText(prefill.CloudRemote)
	}
	rcloneBackupField.SetDisabled(!cloudEnabled)
	form.AddFormItem(rcloneBackupField)

	rcloneLogField = tview.NewInputField().
		SetLabel("  └─ Rclone Log Path").
		SetText("myremote:pbs-logs").
		SetFieldWidth(40)
	if prefill.CloudLogPath != "" {
		rcloneLogField.SetText(prefill.CloudLogPath)
	}
	rcloneLogField.SetDisabled(!cloudEnabled)
	form.AddFormItem(rcloneLogField)

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

	form.AddFormItem(firewallDropdown)

	// Notifications (header + two toggles)
	telegramEnabled := prefill.TelegramEnabled
	emailEnabled := prefill.EmailEnabled
	emailDeliveryMethod := installEmailDeliveryMethodOrDefault(prefill.EmailDeliveryMethod)
	var emailMethodDropdown *tview.DropDown
	notificationHeader := tview.NewInputField().
		SetLabel("Notifications").
		SetFieldWidth(0).
		SetText("").
		SetDisabled(true)
	form.AddFormItem(notificationHeader)

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
	form.AddFormItem(telegramDropdown)

	emailDropdown := tview.NewDropDown().
		SetLabel("  └─ Enable Email notifications").
		SetOptions([]string{"No", "Yes"}, func(option string, index int) {
			emailEnabled = (option == "Yes")
			if emailMethodDropdown != nil {
				emailMethodDropdown.SetDisabled(!emailEnabled)
			}
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
	form.AddFormItem(emailDropdown)

	emailMethodLabels := installEmailDeliveryMethodLabels()
	emailMethodDropdown = tview.NewDropDown().
		SetLabel("  └─ Email delivery method").
		SetOptions(emailMethodLabels, func(_ string, index int) {
			if index >= 0 && index < len(installEmailDeliveryOptions) {
				emailDeliveryMethod = installEmailDeliveryOptions[index].Method
			}
			dropdownOpen = false
		}).
		SetCurrentOption(installEmailDeliveryMethodIndex(emailDeliveryMethod))
	emailMethodDropdown.SetDisabled(!emailEnabled)
	emailMethodDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})
	form.AddFormItem(emailMethodDropdown)

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

	form.AddFormItem(encryptionDropdown)

	// Separator before scheduling
	cronSeparator := tview.NewInputField().
		SetLabel(strings.Repeat("─", 40)).
		SetFieldWidth(0).
		SetText("").
		SetDisabled(true)
	form.AddFormItem(cronSeparator)

	// Cron schedule (after encryption)
	cronField := tview.NewInputField().
		SetLabel("Cron time (HH:MM)").
		SetText("").
		SetPlaceholder(data.CronTime).
		SetFieldWidth(7)
	form.AddFormItem(cronField)

	// Set up form submission
	form.SetOnSubmit(func(values map[string]string) error {
		// Collect data
		data.EnableSecondaryStorage = secondaryEnabled
		if secondaryEnabled {
			data.SecondaryPath = strings.TrimSpace(secondaryPathField.GetText())
			data.SecondaryLogPath = strings.TrimSpace(secondaryLogField.GetText())
			if err := validateSecondaryInstallData(data); err != nil {
				return err
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
		data.EmailDeliveryMethod = installEmailDeliveryMethodOrDefault(emailDeliveryMethod)
		data.EmailFallbackSendmail = &defaultEmailFallbackSendmail

		// Get encryption setting
		data.EnableEncryption = values["Enable Backup Encryption (AGE)"] == "Yes"

		normalizedCron, err := cronutil.NormalizeTime(cronField.GetText(), cronutil.DefaultTime)
		if err != nil {
			return err
		}
		data.CronTime = normalizedCron

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
	form.SetBackgroundColor(tcell.ColorBlack)

	// Add arrow key support for navigation
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// If a dropdown is open, don't intercept arrow keys - let them work naturally
		if dropdownOpen {
			return event
		}

		// Check if focus is on a button (not on a form field)
		formItemIndex, buttonIndex := form.GetFocusedItemIndex()
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

	flex := buildWizardScreen(
		"ProxSave Installation",
		"Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n"+
			"This wizard will guide you through configuring your backup system for Proxmox.\n"+
			"All settings can be changed later by editing the configuration file.",
		"[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to open dropdowns | ←→ on buttons | ENTER to submit | Mouse clicks enabled",
		configPath,
		buildSig,
		form.Form,
	)

	// Route validation/submit errors to an inline modal that returns to the form,
	// instead of ShowError() which stops the app and would make RunInstallWizard
	// return the partially-filled data with a nil error. The submit closure reads
	// parentView lazily, so setting it here (after the button was added) is fine.
	form.SetParentView(flex)

	if err := runInstallWizardRunner(ctx, app, flex, form.Form); err != nil {
		return nil, err
	}

	if data == nil {
		return nil, ErrInstallCancelled
	}

	return data, nil
}

func boolToOptionIndex(value bool) int {
	if value {
		return 1
	}
	return 0
}

func installEmailDeliveryMethodLabels() []string {
	labels := make([]string, 0, len(installEmailDeliveryOptions))
	for _, option := range installEmailDeliveryOptions {
		labels = append(labels, option.Label)
	}
	return labels
}

func installEmailDeliveryMethodIndex(method string) int {
	method = installEmailDeliveryMethodOrDefault(method)
	for i, option := range installEmailDeliveryOptions {
		if option.Method == method {
			return i
		}
	}
	return 0
}

// CheckExistingConfig checks if config file exists and asks how to proceed
func CheckExistingConfig(ctx context.Context, configPath string, buildSig string) (ExistingConfigAction, error) {
	if info, err := os.Stat(configPath); err == nil {
		if !info.Mode().IsRegular() {
			return ExistingConfigCancel, fmt.Errorf("configuration file path is not a regular file: %s", configPath)
		}

		// File exists, ask how to proceed
		app := tui.NewApp()
		action := ExistingConfigCancel
		escapedConfigPath := tview.Escape(configPath)

		// Confirmation modal
		modal := tview.NewModal().
			SetText(fmt.Sprintf("Configuration file already exists at:\n[yellow]%s[white]\n\n"+
				"Choose how to proceed:\n"+
				"[yellow]Overwrite[white]   - Start from embedded template\n"+
				"[yellow]Edit existing[white] - Keep current file as base\n"+
				"[yellow]Keep & continue[white] - Leave file untouched, continue install\n"+
				"[yellow]Cancel[white]      - Exit installation", escapedConfigPath)).
			AddButtons([]string{"Overwrite", "Edit existing", "Keep & continue", "Cancel"}).
			SetDoneFunc(func(buttonIndex int, buttonLabel string) {
				switch buttonLabel {
				case "Overwrite":
					action = ExistingConfigOverwrite
				case "Edit existing":
					action = ExistingConfigEdit
				case "Keep & continue":
					action = ExistingConfigKeepContinue
				default:
					action = ExistingConfigCancel
				}
				app.Stop()
			})

		modal.SetBorder(true).
			SetTitle(" Configuration Exists ").
			SetTitleAlign(tview.AlignCenter).
			SetTitleColor(tui.WarningYellow).
			SetBorderColor(tui.WarningYellow).
			SetBackgroundColor(tcell.ColorBlack)
		modal.SetFocus(2)

		flex := buildWizardScreen(
			"ProxSave Installation",
			"Welcome to ProxSave Installation Wizard - By TIS24DEV\n\n"+
				"This wizard will guide you through configuring your backup system for Proxmox.\n"+
				"All settings can be changed later by editing the configuration file.",
			"[yellow]Navigation:[white] Press [yellow]TAB[white] or [yellow]↑↓[white] to move between fields | Use [yellow]←→[white] on buttons | Press [yellow]ENTER[white] to submit | Mouse clicks enabled",
			"",
			buildSig,
			modal,
		)

		if err := checkExistingConfigRunner(ctx, app, flex, modal); err != nil {
			return ExistingConfigCancel, err
		}

		return action, nil
	} else if !os.IsNotExist(err) {
		return ExistingConfigCancel, err
	}

	return ExistingConfigOverwrite, nil // File doesn't exist, proceed
}
