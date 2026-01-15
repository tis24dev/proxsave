package wizard

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"golang.org/x/crypto/ssh"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestValidatePublicKey(t *testing.T) {
	validAge := "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
	validSSH := generateSSHPublicKey(t)
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid", input: " " + validAge + "  ", want: validAge},
		{name: "valid ssh", input: " " + validSSH + " ", want: validSSH},
		{name: "empty", input: "", wantErr: true},
		{name: "missing prefix", input: "abc", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := validatePublicKey(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidatePassphrase(t *testing.T) {
	cases := []struct {
		name    string
		pass    string
		confirm string
		wantErr bool
	}{
		{name: "valid", pass: "CorrectHorse1!", confirm: "CorrectHorse1!"},
		{name: "empty", pass: "", confirm: "", wantErr: true},
		{name: "short", pass: "short", confirm: "short", wantErr: true},
		{name: "mismatch", pass: "longenough", confirm: "diff", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := validatePassphrase(tc.pass, tc.confirm)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidatePrivateKey(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "valid", input: " AGE-SECRET-KEY-1abc  ", want: "AGE-SECRET-KEY-1abc"},
		{name: "empty", input: "", wantErr: true},
		{name: "wrong prefix", input: "SECRET", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := validatePrivateKey(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSaveAgeRecipientSuccess(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "keys", "recipient.age")

	origMkdir := ageMkdirAll
	origWrite := ageWriteFile
	origChmod := ageChmod
	t.Cleanup(func() {
		ageMkdirAll = origMkdir
		ageWriteFile = origWrite
		ageChmod = origChmod
	})

	ageMkdirAll = func(path string, perm os.FileMode) error {
		if perm != 0o700 {
			t.Fatalf("unexpected mkdir perm: %v", perm)
		}
		return nil
	}

	var written []byte
	ageWriteFile = func(path string, data []byte, perm os.FileMode) error {
		if path != target {
			t.Fatalf("unexpected path %s", path)
		}
		written = append([]byte(nil), data...)
		if perm != 0o600 {
			t.Fatalf("unexpected write perm: %v", perm)
		}
		return nil
	}

	var chmodPath string
	ageChmod = func(path string, perm os.FileMode) error {
		chmodPath = path
		if perm != 0o600 {
			t.Fatalf("unexpected chmod perm: %v", perm)
		}
		return nil
	}

	if err := SaveAgeRecipient(target, "age1final"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(written) != "age1final\n" {
		t.Fatalf("wrong payload: %q", string(written))
	}
	if chmodPath != target {
		t.Fatalf("chmod not called on target")
	}
}

func TestSaveAgeRecipientErrors(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "keys", "recipient.age")

	origMkdir := ageMkdirAll
	origWrite := ageWriteFile
	origChmod := ageChmod
	t.Cleanup(func() {
		ageMkdirAll = origMkdir
		ageWriteFile = origWrite
		ageChmod = origChmod
	})

	ageMkdirAll = func(string, os.FileMode) error {
		return errors.New("boom")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "create recipient directory") {
		t.Fatalf("unexpected error: %v", err)
	}

	ageMkdirAll = origMkdir

	ageWriteFile = func(string, []byte, os.FileMode) error {
		return errors.New("write fail")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "write recipient file") {
		t.Fatalf("unexpected error: %v", err)
	}

	ageWriteFile = origWrite

	ageChmod = func(string, os.FileMode) error {
		return errors.New("chmod fail")
	}
	if err := SaveAgeRecipient(target, "age1final"); err == nil || !strings.Contains(err.Error(), "chmod recipient file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfirmRecipientOverwriteSelection(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	tests := []struct {
		name   string
		button string
		want   bool
	}{
		{name: "overwrite", button: "Overwrite", want: true},
		{name: "cancel", button: "Cancel", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
				done := extractModalDone(focus.(*tview.Modal))
				done(0, tc.button)
				return nil
			}

			got, err := ConfirmRecipientOverwrite("/tmp/recipient.age", "/etc/proxsave/.env", "sig-xyz")
			if err != nil {
				t.Fatalf("ConfirmRecipientOverwrite returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v for button %q", got, tc.want, tc.button)
			}
		})
	}
}

func TestConfirmRecipientOverwriteModalIncludesRecipientPath(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	var modalText string
	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		modalText = extractModalText(focus.(*tview.Modal))
		return nil
	}

	_, err := ConfirmRecipientOverwrite("/var/lib/proxsave/recipient.age", "/etc/.env", "sig")
	if err != nil {
		t.Fatalf("ConfirmRecipientOverwrite returned error: %v", err)
	}
	if !strings.Contains(modalText, "/var/lib/proxsave/recipient.age") {
		t.Fatalf("expected modal to mention recipient path, got %q", modalText)
	}
}

func TestConfirmRecipientOverwriteRunnerError(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return errors.New("boom")
	}

	if _, err := ConfirmRecipientOverwrite("/tmp/recipient.age", "/etc/.env", "sig"); err == nil {
		t.Fatalf("expected error from runner")
	}
}

func TestConfirmAddRecipientSelection(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	tests := []struct {
		name   string
		button string
		want   bool
	}{
		{name: "add another", button: "Add Another", want: true},
		{name: "finish", button: "Finish", want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
				done := extractModalDone(focus.(*tview.Modal))
				done(0, tc.button)
				return nil
			}

			got, err := ConfirmAddRecipient("/etc/proxsave/.env", "sig-xyz", 2)
			if err != nil {
				t.Fatalf("ConfirmAddRecipient returned error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v for button %q", got, tc.want, tc.button)
			}
		})
	}
}

func TestConfirmAddRecipientModalIncludesCount(t *testing.T) {
	originalRunner := ageWizardRunner
	defer func() { ageWizardRunner = originalRunner }()

	var modalText string
	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		modalText = extractModalText(focus.(*tview.Modal))
		return nil
	}

	_, err := ConfirmAddRecipient("/etc/proxsave/.env", "sig", 3)
	if err != nil {
		t.Fatalf("ConfirmAddRecipient returned error: %v", err)
	}
	if !strings.Contains(modalText, "3") {
		t.Fatalf("expected modal to mention count, got %q", modalText)
	}
}

func TestRunAgeSetupWizardExistingKey(t *testing.T) {
	validAge := "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
	data, err := runAgeWizardTest(t, func(form *tview.Form) {
		drop := form.GetFormItem(0).(*tview.DropDown)
		drop.SetCurrentOption(0)
		publicKey := form.GetFormItem(1).(*tview.InputField)
		publicKey.SetText(validAge)
		pressFormButton(t, form, "Continue")
	})
	if err != nil {
		t.Fatalf("RunAgeSetupWizard returned error: %v", err)
	}
	if data.SetupType != "existing" {
		t.Fatalf("unexpected setup type: %s", data.SetupType)
	}
	if data.RecipientKey != validAge {
		t.Fatalf("expected recipient key propagated, got %q", data.RecipientKey)
	}
}

func TestRunAgeSetupWizardPassphrase(t *testing.T) {
	data, err := runAgeWizardTest(t, func(form *tview.Form) {
		drop := form.GetFormItem(0).(*tview.DropDown)
		drop.SetCurrentOption(1)
		pass := form.GetFormItem(2).(*tview.InputField)
		confirm := form.GetFormItem(3).(*tview.InputField)
		pass.SetText("CorrectHorse1!")
		confirm.SetText("CorrectHorse1!")
		pressFormButton(t, form, "Continue")
	})
	if err != nil {
		t.Fatalf("RunAgeSetupWizard returned error: %v", err)
	}
	if data.SetupType != "passphrase" {
		t.Fatalf("unexpected setup type: %s", data.SetupType)
	}
	if data.Passphrase != "CorrectHorse1!" {
		t.Fatalf("expected passphrase saved, got %q", data.Passphrase)
	}
	if data.PublicKey != "" || data.PrivateKey != "" {
		t.Fatalf("unexpected keys for passphrase mode: %+v", data)
	}
}

func TestRunAgeSetupWizardPrivateKey(t *testing.T) {
	data, err := runAgeWizardTest(t, func(form *tview.Form) {
		drop := form.GetFormItem(0).(*tview.DropDown)
		drop.SetCurrentOption(2)
		privateField := form.GetFormItem(4).(*tview.InputField)
		privateField.SetText("AGE-SECRET-KEY-1valid")
		pressFormButton(t, form, "Continue")
	})
	if err != nil {
		t.Fatalf("RunAgeSetupWizard returned error: %v", err)
	}
	if data.SetupType != "privatekey" {
		t.Fatalf("unexpected setup type: %s", data.SetupType)
	}
	if data.PrivateKey != "AGE-SECRET-KEY-1valid" {
		t.Fatalf("expected private key saved, got %q", data.PrivateKey)
	}
	if data.Passphrase != "" || data.PublicKey != "" {
		t.Fatalf("unexpected fields populated: %+v", data)
	}
}

func TestRunAgeSetupWizardCancel(t *testing.T) {
	data, err := runAgeWizardTest(t, func(form *tview.Form) {
		pressFormButton(t, form, "Cancel")
	})
	if err != ErrAgeSetupCancelled {
		t.Fatalf("expected ErrAgeSetupCancelled, got %v", err)
	}
	if data != nil {
		t.Fatalf("expected nil data on cancel")
	}
}

func runAgeWizardTest(t *testing.T, configure func(form *tview.Form)) (*AgeSetupData, error) {
	t.Helper()
	originalRunner := ageWizardRunner
	ageWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		form, ok := focus.(*tview.Form)
		if !ok {
			t.Fatalf("expected *tview.Form focus, got %T", focus)
		}
		configure(form)
		return nil
	}
	t.Cleanup(func() { ageWizardRunner = originalRunner })
	return RunAgeSetupWizard(context.Background(), "/tmp/recipient.age", "/etc/proxsave/config.env", "sig-test")
}

func pressFormButton(t *testing.T, form *tview.Form, label string) {
	t.Helper()
	index := form.GetButtonIndex(label)
	if index < 0 {
		t.Fatalf("button %q not found", label)
	}
	button := form.GetButton(index)
	handler := button.InputHandler()
	if handler == nil {
		t.Fatalf("button %q has no input handler", label)
	}
	handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) {})
}

func generateSSHPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh.NewPublicKey: %v", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}
