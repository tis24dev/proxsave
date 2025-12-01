package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
