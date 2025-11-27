package wizard

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxmox-backup/internal/tui"
)

func extractModalDone(modal *tview.Modal) func(int, string) {
	field := reflect.ValueOf(modal).Elem().FieldByName("done")
	ptr := unsafe.Pointer(field.UnsafeAddr())
	return *(*func(int, string))(ptr)
}

func extractModalText(modal *tview.Modal) string {
	return reflect.ValueOf(modal).Elem().FieldByName("text").String()
}

func TestConfirmNewInstallContinue(t *testing.T) {
	originalRunner := confirmNewInstallRunner
	defer func() { confirmNewInstallRunner = originalRunner }()

	confirmNewInstallRunner = func(app *tui.App, root, focus tview.Primitive) error {
		done := extractModalDone(focus.(*tview.Modal))
		done(0, "Continue")
		return nil
	}

	proceed, err := ConfirmNewInstall("/opt/proxmox", "sig-123")
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

	proceed, err := ConfirmNewInstall("/opt/proxmox", "sig-123")
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

	_, err := ConfirmNewInstall("/var/lib/data", "build-sig")
	if err != nil {
		t.Fatalf("ConfirmNewInstall error: %v", err)
	}
	if !strings.Contains(captured, "/var/lib/data") {
		t.Fatalf("expected modal text to mention base dir, got %q", captured)
	}
}
