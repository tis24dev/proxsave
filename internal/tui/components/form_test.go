package components

import (
	"errors"
	"strings"
	"testing"

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
