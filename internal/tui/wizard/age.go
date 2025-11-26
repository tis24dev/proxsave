package wizard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxmox-backup/internal/tui"
	"github.com/tis24dev/proxmox-backup/internal/tui/components"
)

// AgeSetupData holds the collected AGE encryption setup data
type AgeSetupData struct {
	SetupType    string // "existing", "passphrase", "privatekey"
	PublicKey    string // For "existing" type
	Passphrase   string // For "passphrase" type
	PrivateKey   string // For "privatekey" type
	RecipientKey string // The final recipient key to save
}

var (
	// ErrAgeSetupCancelled is returned when the user aborts the AGE setup wizard.
	ErrAgeSetupCancelled = errors.New("encryption setup aborted by user")
)

// ConfirmRecipientOverwrite shows a TUI modal to confirm overwriting an existing AGE recipient.
func ConfirmRecipientOverwrite(recipientPath, configPath, buildSig string) (bool, error) {
	app := tui.NewApp()
	overwrite := false

	welcomeText := tview.NewTextView().
		SetText("PROXMOX SYSTEM BACKUP - By TIS24DEV\nAGE Encryption Setup\n\n" +
			"Configure encryption for your backups using the AGE encryption tool.\n" +
			"Choose how you want to set up your encryption key.\n").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	navInstructions := tview.NewTextView().
		SetText("\n[yellow]Navigation:[white] Use [yellow]←→[white] on buttons | Press [yellow]ENTER[white] to select | Mouse clicks enabled").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

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

	modal := tview.NewModal().
		SetText(fmt.Sprintf("Existing recipient:\n[yellow]%s[white]\n\nOverwrite with a new one?", recipientPath)).
		AddButtons([]string{"Overwrite", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			if buttonLabel == "Overwrite" {
				overwrite = true
			}
			app.Stop()
		})

	modal.SetBorder(true).
		SetTitle(" Existing AGE Recipient ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(modal, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(" AGE Encryption Setup ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	if err := app.SetRoot(flex, true).SetFocus(modal).Run(); err != nil {
		return false, err
	}

	return overwrite, nil
}

// RunAgeSetupWizard runs the TUI-based AGE encryption setup wizard
func RunAgeSetupWizard(ctx context.Context, recipientPath, configPath, buildSig string) (*AgeSetupData, error) {
	data := &AgeSetupData{}
	app := tui.NewApp()

	// Track if dropdown is open
	var dropdownOpen bool

	// Build the form
	form := components.NewForm(app)

	// Welcome text
	welcomeText := tview.NewTextView().
		SetText("PROXMOX SYSTEM BACKUP - By TIS24DEV\nAGE Encryption Setup\n\n" +
			"Configure encryption for your backups using the AGE encryption tool.\n" +
			"Choose how you want to set up your encryption key.\n").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	welcomeText.SetBorder(false)

	// Navigation instructions
	navInstructions := tview.NewTextView().
		SetText("\n[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to open dropdowns | ←→ on buttons | ENTER to submit | Mouse clicks enabled").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	navInstructions.SetBorder(false)

	// Add separator
	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

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

	// Setup type dropdown
	var setupType string
	var publicKeyField, passphraseField, passphraseConfirmField, privateKeyField *tview.InputField

	setupTypeDropdown := tview.NewDropDown().
		SetLabel("Setup Type").
		SetOptions([]string{
			"Use existing AGE public key",
			"Generate key from passphrase",
			"Generate key from existing private key",
		}, func(option string, index int) {
			switch index {
			case 0:
				setupType = "existing"
				if publicKeyField != nil {
					publicKeyField.SetDisabled(false)
				}
				if passphraseField != nil {
					passphraseField.SetDisabled(true)
				}
				if passphraseConfirmField != nil {
					passphraseConfirmField.SetDisabled(true)
				}
				if privateKeyField != nil {
					privateKeyField.SetDisabled(true)
				}
			case 1:
				setupType = "passphrase"
				if publicKeyField != nil {
					publicKeyField.SetDisabled(true)
				}
				if passphraseField != nil {
					passphraseField.SetDisabled(false)
				}
				if passphraseConfirmField != nil {
					passphraseConfirmField.SetDisabled(false)
				}
				if privateKeyField != nil {
					privateKeyField.SetDisabled(true)
				}
			case 2:
				setupType = "privatekey"
				if publicKeyField != nil {
					publicKeyField.SetDisabled(true)
				}
				if passphraseField != nil {
					passphraseField.SetDisabled(true)
				}
				if passphraseConfirmField != nil {
					passphraseConfirmField.SetDisabled(true)
				}
				if privateKeyField != nil {
					privateKeyField.SetDisabled(false)
				}
			}
			dropdownOpen = false
		}).
		SetCurrentOption(0)

	setupTypeDropdown.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEnter {
			dropdownOpen = !dropdownOpen
		} else if event.Key() == tcell.KeyEscape {
			dropdownOpen = false
		}
		return event
	})

	form.Form.AddFormItem(setupTypeDropdown)

	// Public key field (for "existing" type)
	publicKeyField = tview.NewInputField().
		SetLabel("  └─ AGE Public Key").
		SetText("").
		SetFieldWidth(70)
	form.Form.AddFormItem(publicKeyField)

	// Passphrase fields (for "passphrase" type)
	passphraseField = tview.NewInputField().
		SetLabel("  └─ Passphrase").
		SetText("").
		SetFieldWidth(50).
		SetMaskCharacter('*')
	passphraseField.SetDisabled(true)
	form.Form.AddFormItem(passphraseField)

	passphraseConfirmField = tview.NewInputField().
		SetLabel("  └─ Confirm Passphrase").
		SetText("").
		SetFieldWidth(50).
		SetMaskCharacter('*')
	passphraseConfirmField.SetDisabled(true)
	form.Form.AddFormItem(passphraseConfirmField)

	// Private key field (for "privatekey" type)
	privateKeyField = tview.NewInputField().
		SetLabel("  └─ AGE Private Key").
		SetText("").
		SetFieldWidth(70).
		SetMaskCharacter('*')
	privateKeyField.SetDisabled(true)
	form.Form.AddFormItem(privateKeyField)

	// Initialize with "existing" type selected
	setupType = "existing"
	passphraseField.SetDisabled(true)
	passphraseConfirmField.SetDisabled(true)
	privateKeyField.SetDisabled(true)

	// Set up form submission
	form.SetOnSubmit(func(values map[string]string) error {
		data.SetupType = setupType

		switch setupType {
		case "existing":
			publicKey := strings.TrimSpace(publicKeyField.GetText())
			if publicKey == "" {
				return fmt.Errorf("public key cannot be empty")
			}
			if !strings.HasPrefix(publicKey, "age1") {
				return fmt.Errorf("public key must start with 'age1'")
			}
			data.PublicKey = publicKey
			data.RecipientKey = publicKey

		case "passphrase":
			passphrase := passphraseField.GetText()
			passphraseConfirm := passphraseConfirmField.GetText()

			if passphrase == "" {
				return fmt.Errorf("passphrase cannot be empty")
			}
			if len(passphrase) < 8 {
				return fmt.Errorf("passphrase must be at least 8 characters long")
			}
			if passphrase != passphraseConfirm {
				return fmt.Errorf("passphrases do not match")
			}
			data.Passphrase = passphrase

		case "privatekey":
			privateKey := strings.TrimSpace(privateKeyField.GetText())
			if privateKey == "" {
				return fmt.Errorf("private key cannot be empty")
			}
			if !strings.HasPrefix(privateKey, "AGE-SECRET-KEY-1") {
				return fmt.Errorf("private key must start with 'AGE-SECRET-KEY-1'")
			}
			data.PrivateKey = privateKey
		}

		return nil
	})

	form.SetOnCancel(func() {
		// User cancelled
		data = nil
	})

	// Style the form
	form.SetBorderWithTitle("AGE Encryption Setup")
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

	// Create layout
	flex := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(welcomeText, 5, 0, false).
		AddItem(navInstructions, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(form.Form, 0, 1, true)
		// Footers
	flex.AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	flex.SetBorder(true).
		SetTitle(" AGE Encryption Setup ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	// Set the parent view for inline error display, then add buttons
	form.SetParentView(flex)
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")

	// Run the app - ignore errors from normal app termination
	_ = app.SetRoot(flex, true).SetFocus(form.Form).Run()

	if data == nil {
		return nil, ErrAgeSetupCancelled
	}

	return data, nil
}

// SaveAgeRecipient saves the AGE recipient to the file
func SaveAgeRecipient(recipientPath, recipient string) error {
	if err := os.MkdirAll(filepath.Dir(recipientPath), 0o700); err != nil {
		return fmt.Errorf("create recipient directory: %w", err)
	}

	content := recipient + "\n"
	if err := os.WriteFile(recipientPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write recipient file: %w", err)
	}

	if err := os.Chmod(recipientPath, 0o600); err != nil {
		return fmt.Errorf("chmod recipient file: %w", err)
	}

	return nil
}
