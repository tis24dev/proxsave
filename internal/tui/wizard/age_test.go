package wizard

import (
	"context"
	"errors"
	"github.com/gdamore/tcell/v2"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestSaveAgeRecipient(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "recipient.txt")

	if err := SaveAgeRecipient(target, "age1abcd"); err != nil {
		t.Fatalf("SaveAgeRecipient error: %v", err)
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("failed to read recipient file: %v", err)
	}
	if string(data) != "age1abcd\n" {
		t.Fatalf("unexpected file content: %q", string(data))
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("expected permissions 0600, got %v", info.Mode().Perm())
	}
}

func TestConfirmRecipientOverwriteAccept(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(0, "Overwrite")
		return nil
	}

	overwrite, err := ConfirmRecipientOverwrite("/tmp/recipient", "/tmp/config", "sig")
	if err != nil {
		t.Fatalf("ConfirmRecipientOverwrite error: %v", err)
	}
	if !overwrite {
		t.Fatalf("expected overwrite=true when Overwrite selected")
	}
}

func TestConfirmRecipientOverwriteCancel(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(1, "Cancel")
		return nil
	}

	overwrite, err := ConfirmRecipientOverwrite("/tmp/recipient", "/tmp/config", "sig")
	if err != nil {
		t.Fatalf("ConfirmRecipientOverwrite error: %v", err)
	}
	if overwrite {
		t.Fatalf("expected overwrite=false when Cancel selected")
	}
}

func TestConfirmRecipientOverwriteMessageContainsPaths(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	var captured string
	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	}

	if _, err := ConfirmRecipientOverwrite("/var/lib/age/recipient.txt", "/etc/proxsave/backup.env", "sig"); err != nil {
		t.Fatalf("ConfirmRecipientOverwrite error: %v", err)
	}
	if !strings.Contains(captured, "/var/lib/age/recipient.txt") {
		t.Fatalf("modal text did not include recipient path: %q", captured)
	}
}

func TestRunAgeSetupWizardExistingKey(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected focus to be form, got %T", focus)
		}

		dd, ok := form.GetFormItem(0).(*tview.DropDown)
		if !ok {
			t.Fatalf("expected first form item to be dropdown, got %T", form.GetFormItem(0))
		}
		dd.SetCurrentOption(0)

		pubField, ok := form.GetFormItem(1).(*tview.InputField)
		if !ok {
			t.Fatalf("expected public key field, got %T", form.GetFormItem(1))
		}
		pubField.SetText("age1deadbeefexamplekey")

		// Exercise dropdown input capture branches
		if handler := dd.GetInputCapture(); handler != nil {
			handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
			handler(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
		}
		if capture := form.GetInputCapture(); capture != nil {
			capture(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
		}

		// Trigger submit
		clickFormButton(t, form, 0)
		return nil
	}

	data, err := RunAgeSetupWizard(context.Background(), "/tmp/recipient", "/tmp/config", "sig")
	if err != nil {
		t.Fatalf("RunAgeSetupWizard error: %v", err)
	}
	if data == nil {
		t.Fatalf("expected data to be populated")
	}
	if data.SetupType != "existing" {
		t.Fatalf("expected setup type 'existing', got %q", data.SetupType)
	}
	if data.PublicKey != "age1deadbeefexamplekey" || data.RecipientKey != data.PublicKey {
		t.Fatalf("unexpected public/recipient key: %+v", data)
	}
}

func TestRunAgeSetupWizardCancel(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected focus to be form, got %T", focus)
		}
		// Cancel button is the second added button
		clickFormButton(t, form, 1)
		return nil
	}

	data, err := RunAgeSetupWizard(context.Background(), "/tmp/recipient", "/tmp/config", "sig")
	if !errors.Is(err, ErrAgeSetupCancelled) {
		t.Fatalf("expected cancellation error, got %v", err)
	}
	if data != nil {
		t.Fatalf("expected data to be nil on cancel")
	}
}

func TestRunAgeSetupWizardPassphrase(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected focus to be form, got %T", focus)
		}

		dd, ok := form.GetFormItem(0).(*tview.DropDown)
		if !ok {
			t.Fatalf("expected dropdown, got %T", form.GetFormItem(0))
		}
		dd.SetCurrentOption(1) // passphrase mode

		passField, ok := form.GetFormItem(2).(*tview.InputField)
		if !ok {
			t.Fatalf("expected passphrase field, got %T", form.GetFormItem(2))
		}
		confirmField, ok := form.GetFormItem(3).(*tview.InputField)
		if !ok {
			t.Fatalf("expected confirm field, got %T", form.GetFormItem(3))
		}

		passField.SetText("longpass")
		confirmField.SetText("longpass")

		clickFormButton(t, form, 0)
		return nil
	}

	data, err := RunAgeSetupWizard(context.Background(), "/tmp/recipient", "/tmp/config", "sig")
	if err != nil {
		t.Fatalf("RunAgeSetupWizard error: %v", err)
	}
	if data == nil || data.SetupType != "passphrase" || data.Passphrase != "longpass" {
		t.Fatalf("unexpected passphrase data: %+v", data)
	}
}

func TestRunAgeSetupWizardPrivateKey(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected focus to be form, got %T", focus)
		}

		dd, ok := form.GetFormItem(0).(*tview.DropDown)
		if !ok {
			t.Fatalf("expected dropdown, got %T", form.GetFormItem(0))
		}
		dd.SetCurrentOption(2) // private key mode

		privateField, ok := form.GetFormItem(4).(*tview.InputField)
		if !ok {
			t.Fatalf("expected private key field, got %T", form.GetFormItem(4))
		}
		privateField.SetText("AGE-SECRET-KEY-1example")

		clickFormButton(t, form, 0)
		return nil
	}

	data, err := RunAgeSetupWizard(context.Background(), "/tmp/recipient", "/tmp/config", "sig")
	if err != nil {
		t.Fatalf("RunAgeSetupWizard error: %v", err)
	}
	if data == nil || data.SetupType != "privatekey" || data.PrivateKey != "AGE-SECRET-KEY-1example" {
		t.Fatalf("unexpected private key data: %+v", data)
	}
}

func TestSaveAgeRecipientDirErrors(t *testing.T) {
	dir := t.TempDir()
	fileParent := filepath.Join(dir, "parent-file")
	if err := os.WriteFile(fileParent, []byte("x"), 0o600); err != nil {
		t.Fatalf("prepare parent file: %v", err)
	}

	target := filepath.Join(fileParent, "recipient.txt")
	err := SaveAgeRecipient(target, "age1abcd")
	if err == nil || !strings.Contains(err.Error(), "create recipient directory") {
		t.Fatalf("expected directory creation error, got %v", err)
	}
}

func TestSaveAgeRecipientWriteErrors(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "recipient-dir")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatalf("prepare target dir: %v", err)
	}

	err := SaveAgeRecipient(targetDir, "age1abcd")
	if err == nil || !strings.Contains(err.Error(), "write recipient file") {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestSaveAgeRecipientChmodErrors(t *testing.T) {
	originalChmod := ageChmod
	defer func() { ageChmod = originalChmod }()
	ageChmod = func(string, os.FileMode) error {
		return errors.New("chmod fail")
	}

	target := filepath.Join(t.TempDir(), "recipient.txt")
	err := SaveAgeRecipient(target, "age1abcd")
	if err == nil || !strings.Contains(err.Error(), "chmod recipient file") {
		t.Fatalf("expected chmod error, got %v", err)
	}
}

func clickFormButton(t *testing.T, form *tview.Form, index int) {
	t.Helper()
	btn := form.GetButton(index)
	if btn == nil {
		t.Fatalf("button %d not found", index)
	}
	selectedField := reflect.ValueOf(btn).Elem().FieldByName("selected")
	if !selectedField.IsValid() || selectedField.IsNil() {
		t.Fatalf("selected func missing on button %d", index)
	}
	callback := *(*func())(unsafe.Pointer(selectedField.UnsafeAddr()))
	callback()
}
