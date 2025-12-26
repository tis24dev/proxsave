package components

import (
	"errors"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func TestValidateAllWithValidators(t *testing.T) {
	form := NewForm(tui.NewApp())
	form.AddInputFieldWithValidation("Name", "", 10, func(v string) error {
		if strings.TrimSpace(v) == "" {
			return errors.New("empty")
		}
		return nil
	})

	values := map[string]string{"Name": ""}
	if err := form.ValidateAll(values); err == nil {
		t.Fatalf("expected validation error for empty value")
	}

	values["Name"] = "ok"
	if err := form.ValidateAll(values); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestGetFormValuesCollectsWidgets(t *testing.T) {
	form := NewForm(tui.NewApp())

	form.AddInputFieldWithValidation("Input", "", 10)
	form.Form.AddCheckbox("Check", true, nil)
	form.Form.AddDropDown("Drop", []string{"a", "b"}, 1, nil)

	// Set values
	if input, ok := form.Form.GetFormItem(0).(*tview.InputField); ok {
		input.SetText("value")
	}
	if dd, ok := form.Form.GetFormItem(2).(*tview.DropDown); ok {
		dd.SetCurrentOption(1)
	}

	values := form.GetFormValues()

	if got := values["Input"]; got != "value" {
		t.Fatalf("input value mismatch: got %q", got)
	}
	if got := values["Check"]; got != "true" {
		t.Fatalf("checkbox value mismatch: got %q", got)
	}
	if got := values["Drop"]; got != "b" {
		t.Fatalf("dropdown value mismatch: got %q", got)
	}
}

func TestAddPasswordFieldRegistersValidators(t *testing.T) {
	form := NewForm(tui.NewApp())
	form.AddPasswordField("Password", 12, func(value string) error {
		if strings.TrimSpace(value) == "" {
			return errors.New("empty password")
		}
		return nil
	})

	if _, ok := form.validators["Password"]; !ok {
		t.Fatalf("expected validators to be registered for Password")
	}
	if form.Form.GetFormItemCount() != 1 {
		t.Fatalf("form item count=%d; want 1", form.Form.GetFormItemCount())
	}
	if got := form.Form.GetFormItem(0).(*tview.InputField).GetLabel(); got != "Password" {
		t.Fatalf("label=%q; want %q", got, "Password")
	}
}

func TestAddSubmitButtonShowsValidationError(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		form := NewForm(app)
		form.AddInputFieldWithValidation("Name", "", 10, func(value string) error {
			if strings.TrimSpace(value) == "" {
				return errors.New("empty name")
			}
			return nil
		})
		form.SetOnSubmit(func(values map[string]string) error { return nil })
		form.AddSubmitButton("Continue")

		btn := form.Form.GetButton(form.Form.GetButtonCount() - 1)
		btn.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	})

	if modal.GetTitle() != " Validation Error " {
		t.Fatalf("modal title=%q; want %q", modal.GetTitle(), " Validation Error ")
	}
	if !strings.Contains(modalText(modal), "empty name") {
		t.Fatalf("expected validation error message in modal text: %q", modalText(modal))
	}
}

func TestAddSubmitButtonShowsSubmitError(t *testing.T) {
	modal := captureModal(t, func(app *tui.App) {
		form := NewForm(app)
		form.AddInputFieldWithValidation("Name", "ok", 10)
		form.SetOnSubmit(func(values map[string]string) error { return errors.New("boom") })
		form.AddSubmitButton("Continue")

		btn := form.Form.GetButton(form.Form.GetButtonCount() - 1)
		btn.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	})

	if modal.GetTitle() != " Error " {
		t.Fatalf("modal title=%q; want %q", modal.GetTitle(), " Error ")
	}
	if !strings.Contains(modalText(modal), "boom") {
		t.Fatalf("expected submit error message in modal text: %q", modalText(modal))
	}
}

func TestAddSubmitButtonUsesInlineErrorWhenParentViewSet(t *testing.T) {
	returnTo := &recordingPrimitive{Box: tview.NewBox()}
	modal := captureModal(t, func(app *tui.App) {
		form := NewForm(app)
		form.SetParentView(returnTo)
		form.AddInputFieldWithValidation("Name", "", 10, func(value string) error {
			return errors.New("bad")
		})
		form.SetOnSubmit(func(values map[string]string) error { return nil })
		form.AddSubmitButton("Continue")

		btn := form.Form.GetButton(form.Form.GetButtonCount() - 1)
		btn.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)
	})

	modalDone(modal)(0, "OK")
	if !returnTo.focused {
		t.Fatalf("expected parent view to receive focus after dismissing inline error")
	}
}

func TestAddCancelButtonCallsHandler(t *testing.T) {
	called := false
	form := NewForm(tui.NewApp())
	form.SetOnCancel(func() { called = true })
	form.AddCancelButton("Cancel")

	btn := form.Form.GetButton(form.Form.GetButtonCount() - 1)
	btn.InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), nil)

	if !called {
		t.Fatalf("expected cancel handler to be called")
	}
}

func TestSetBorderWithTitleSetsTitle(t *testing.T) {
	form := NewForm(tui.NewApp())
	form.SetBorderWithTitle("Wizard")
	if form.Form.GetTitle() != " Wizard " {
		t.Fatalf("title=%q; want %q", form.Form.GetTitle(), " Wizard ")
	}
}
