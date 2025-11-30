package components

import (
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func captureModal(t *testing.T, fn func(app *tui.App)) *tview.Modal {
	t.Helper()
	original := modalCreatedHook
	var captured *tview.Modal
	modalCreatedHook = func(m *tview.Modal) {
		captured = m
	}
	t.Cleanup(func() {
		modalCreatedHook = original
	})

	app := tui.NewApp()
	fn(app)
	if captured == nil {
		t.Fatalf("modal not captured")
	}
	return captured
}

func modalText(modal *tview.Modal) string {
	return reflect.ValueOf(modal).Elem().FieldByName("text").String()
}

func modalDone(modal *tview.Modal) func(int, string) {
	field := reflect.ValueOf(modal).Elem().FieldByName("done")
	ptr := unsafe.Pointer(field.UnsafeAddr())
	return *(*func(int, string))(ptr)
}

func TestShowConfirmAddsNavigationHint(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		ShowConfirm(app, "Confirm", "Danger zone", nil, nil)
	})
	text := modalText(modal)
	if !strings.Contains(text, "Use TAB or") {
		t.Fatalf("expected navigation hint in modal text: %q", text)
	}
}

func TestShowConfirmRespectsExistingHint(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		ShowConfirm(app, "Confirm", "[yellow]Custom hint", nil, nil)
	})
	if strings.Contains(modalText(modal), "Use TAB or") {
		t.Fatalf("expected custom hint to be preserved without default navigation")
	}
}

func TestShowConfirmCallbacks(t *testing.T) {
	calledYes := false
	calledNo := false
	modal := captureModal(t, func(app *tui.App) {
		ShowConfirm(app, "Confirm", "Danger", func() { calledYes = true }, func() { calledNo = true })
	})

	done := modalDone(modal)
	done(0, "Yes")
	if !calledYes || calledNo {
		t.Fatalf("expected yes callback only, got yes=%v no=%v", calledYes, calledNo)
	}

	calledYes = false
	done(1, "No")
	if calledYes || !calledNo {
		t.Fatalf("expected no callback only, got yes=%v no=%v", calledYes, calledNo)
	}
}

func TestShowInfoAddsInstruction(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		ShowInfo(app, "Info", "All good")
	})
	if !strings.Contains(modalText(modal), "Press ENTER to continue") {
		t.Fatalf("info modal missing continue hint")
	}
}

func TestShowSuccessPrefixesSymbol(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		ShowSuccess(app, "Done", "Backup complete")
	})
	if !strings.HasPrefix(modalText(modal), tui.SymbolSuccess) {
		t.Fatalf("success modal missing prefix symbol: %q", modalText(modal))
	}
}

func TestShowErrorInlineRestoresPreviousPrimitive(t *testing.T) {
	returnTo := &recordingPrimitive{Box: tview.NewBox()}
	modal := captureModal(t, func(app *tui.App) {
		ShowErrorInline(app, "Oops", "failure", returnTo)
	})

	modalDone(modal)(0, "OK")
	if !returnTo.focused {
		t.Fatalf("expected previous primitive to receive focus")
	}
}

func TestShowWarningUsesWarningSymbol(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		ShowWarning(app, "Warn", "Check settings")
	})
	if !strings.HasPrefix(modalText(modal), tui.SymbolWarning) {
		t.Fatalf("warning modal missing warning symbol")
	}
}

type recordingPrimitive struct {
	*tview.Box
	focused bool
}

func (r *recordingPrimitive) Focus(delegate func(p tview.Primitive)) {
	r.focused = true
}

func (r *recordingPrimitive) Blur() {
	r.focused = false
}
