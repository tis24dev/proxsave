package wizard

import (
	"errors"
	"strings"
	"testing"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestConfirmNewInstallContinue(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(0, "Continue")
		return nil
	}

	proceed, err := ConfirmNewInstall("/opt/proxmox", "sig-123", []string{"build", "env", "identity"})
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !proceed {
		t.Fatalf("expected proceed=true when Continue is selected")
	}
}

func TestConfirmNewInstallCancel(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(1, "Cancel")
		return nil
	}

	proceed, err := ConfirmNewInstall("/opt/proxmox", "sig-123", []string{"build", "env", "identity"})
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if proceed {
		t.Fatalf("expected proceed=false when Cancel is selected")
	}
}

func TestConfirmNewInstallMessageIncludesBaseDir(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	var captured string
	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	}

	_, err := ConfirmNewInstall("/var/lib/data", "build-sig", []string{"build", "env", "identity"})
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "/var/lib/data") {
		t.Fatalf("expected modal text to mention base dir, got %q", captured)
	}
}

func TestConfirmNewInstallMessageIncludesPreservedEntries(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	var captured string
	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		captured = extractModalText(focus.(*tview.Modal))
		return nil
	}

	_, err := ConfirmNewInstall("/var/lib/data", "build-sig", []string{"build", "env", "identity"})
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "build/ env/ identity/") {
		t.Fatalf("expected modal text to mention preserved entries, got %q", captured)
	}
}

func TestConfirmNewInstallPropagatesRunnerError(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	expectedErr := errors.New("runner failed")
	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return expectedErr
	}

	_, err := ConfirmNewInstall("/opt/proxmox", "sig-123", []string{"build", "env", "identity"})
	if !errors.Is(err, expectedErr) {
		t.Fatalf("expected error %v, got %v", expectedErr, err)
	}
}
