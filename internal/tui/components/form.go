package components

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

// ValidatorFunc is a function that validates an input value
type ValidatorFunc func(value string) error

// FormField represents a form field with validation
type FormField struct {
	Label      string
	Value      string
	Validators []ValidatorFunc
	Hidden     bool
}

// Form wraps tview.Form with Proxmox styling and validation
type Form struct {
	*tview.Form
	app        *tui.App
	validators map[string][]ValidatorFunc
	onSubmit   func(values map[string]string) error
	onCancel   func()
	parentView tview.Primitive // The layout containing this form, for inline error display
}

// NewForm creates a new form with Proxmox styling
func NewForm(app *tui.App) *Form {
	form := tview.NewForm().
		SetButtonsAlign(tview.AlignCenter).
		SetButtonBackgroundColor(tui.ProxmoxOrange).
		SetButtonTextColor(tcell.ColorWhite).
		SetLabelColor(tui.ProxmoxLight).
		SetFieldBackgroundColor(tui.ProxmoxDark).
		SetFieldTextColor(tcell.ColorWhite)

	return &Form{
		Form:       form,
		app:        app,
		validators: make(map[string][]ValidatorFunc),
	}
}

// AddInputFieldWithValidation adds an input field with validation
func (f *Form) AddInputFieldWithValidation(label, value string, fieldWidth int, validators ...ValidatorFunc) *Form {
	f.validators[label] = validators
	f.Form.AddInputField(label, value, fieldWidth, nil, nil)
	return f
}

// AddPasswordField adds a password field (masked input)
func (f *Form) AddPasswordField(label string, fieldWidth int, validators ...ValidatorFunc) *Form {
	f.validators[label] = validators
	f.Form.AddPasswordField(label, "", fieldWidth, '*', nil)
	return f
}

// SetOnSubmit sets the submit handler
func (f *Form) SetOnSubmit(handler func(values map[string]string) error) *Form {
	f.onSubmit = handler
	return f
}

// SetOnCancel sets the cancel handler
func (f *Form) SetOnCancel(handler func()) *Form {
	f.onCancel = handler
	return f
}

// SetParentView sets the parent layout containing this form (for inline error display)
func (f *Form) SetParentView(parent tview.Primitive) *Form {
	f.parentView = parent
	return f
}

// AddSubmitButton adds a styled submit button
func (f *Form) AddSubmitButton(label string) *Form {
	f.Form.AddButton(label, func() {
		if f.onSubmit != nil {
			values := f.GetFormValues()
			if err := f.ValidateAll(values); err != nil {
				if f.parentView != nil {
					ShowErrorInline(f.app, "Validation Error", err.Error(), f.parentView)
				} else {
					ShowError(f.app, "Validation Error", err.Error())
				}
				return
			}
			if err := f.onSubmit(values); err != nil {
				if f.parentView != nil {
					ShowErrorInline(f.app, "Error", err.Error(), f.parentView)
				} else {
					ShowError(f.app, "Error", err.Error())
				}
				return
			}
		}
		f.app.Stop()
	})
	return f
}

// AddCancelButton adds a styled cancel button
func (f *Form) AddCancelButton(label string) *Form {
	f.Form.AddButton(label, func() {
		if f.onCancel != nil {
			f.onCancel()
		}
		f.app.Stop()
	})
	return f
}

// GetFormValues extracts all form values
func (f *Form) GetFormValues() map[string]string {
	values := make(map[string]string)
	for i := 0; i < f.Form.GetFormItemCount(); i++ {
		item := f.Form.GetFormItem(i)
		if inputField, ok := item.(*tview.InputField); ok {
			label := inputField.GetLabel()
			value := inputField.GetText()
			values[label] = value
		}
		if checkbox, ok := item.(*tview.Checkbox); ok {
			label := checkbox.GetLabel()
			if checkbox.IsChecked() {
				values[label] = "true"
			} else {
				values[label] = "false"
			}
		}
		if dropdown, ok := item.(*tview.DropDown); ok {
			label := dropdown.GetLabel()
			_, option := dropdown.GetCurrentOption()
			values[label] = option
		}
	}
	return values
}

// ValidateAll validates all fields
func (f *Form) ValidateAll(values map[string]string) error {
	for label, validators := range f.validators {
		value := values[label]
		for _, validator := range validators {
			if err := validator(value); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetBorderWithTitle sets border and title with Proxmox styling
func (f *Form) SetBorderWithTitle(title string) *Form {
	f.Form.SetBorder(true).
		SetTitle(" " + title + " ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)
	return f
}
