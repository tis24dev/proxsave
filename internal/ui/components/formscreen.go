package components

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// FormScreen embeds a huh form as a Screen. It resolves struct{} on
// completion: callers read the values through the bindings they installed
// with huh's Value(&x), which are written by the event loop strictly before
// the resolve is delivered. The *huh.Form itself is never handed back to the
// engine goroutine (the loop keeps mutating it for layout).
type FormScreen struct {
	shell.Resolver[struct{}]
	title   string
	form    *huh.Form
	backErr error
}

// FormOption customizes a FormScreen.
type FormOption func(*FormScreen)

// WithFormBack makes Esc resolve with err (back-navigation sentinel) instead
// of the hard shell.ErrAborted.
func WithFormBack(err error) FormOption {
	return func(f *FormScreen) { f.backErr = err }
}

// NewFormScreen wraps a huh form. The caller keeps the value bindings.
func NewFormScreen(title string, form *huh.Form, opts ...FormOption) *FormScreen {
	f := &FormScreen{title: sanitizeLine(title), form: form}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func (f *FormScreen) Init() tea.Cmd { return f.form.Init() }

func (f *FormScreen) Title() string { return f.title }

func (f *FormScreen) Help() string { return "tab/enter navigate · esc cancel" }

func (f *FormScreen) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	// huh only binds ctrl+c as quit; map esc to the component-standard
	// abort (or the configured back sentinel).
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "esc" {
		err := f.backErr
		if err == nil {
			err = shell.ErrAborted
		}
		return f, f.Resolve(struct{}{}, err)
	}
	model, cmd := f.form.Update(msg)
	if form, ok := model.(*huh.Form); ok {
		f.form = form
	}
	switch f.form.State {
	case huh.StateCompleted:
		return f, tea.Batch(cmd, f.Resolve(struct{}{}, nil))
	case huh.StateAborted:
		return f, tea.Batch(cmd, f.Resolve(struct{}{}, shell.ErrAborted))
	}
	return f, cmd
}

func (f *FormScreen) View(width, height int) string {
	f.form = f.form.WithWidth(width).WithHeight(height - 1)
	header := ""
	if f.title != "" {
		header = theme.Emphasis.Render(f.title) + "\n\n"
	}
	return header + f.form.View()
}
