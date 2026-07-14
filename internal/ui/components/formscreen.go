package components

import (
	"charm.land/bubbles/v2/key"
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
	width   int
	height  int
}

// FormOption customizes a FormScreen.
type FormOption func(*FormScreen)

// WithFormBack makes Esc resolve with err (back-navigation sentinel) instead
// of the hard shell.ErrAborted.
func WithFormBack(err error) FormOption {
	return func(f *FormScreen) { f.backErr = err }
}

// NewFormScreen wraps a huh form with the ProxSave theme and arrow-key field
// navigation. The caller keeps the value bindings.
func NewFormScreen(title string, form *huh.Form, opts ...FormOption) *FormScreen {
	form = form.
		WithTheme(huh.ThemeFunc(proxsaveFormTheme)).
		WithKeyMap(proxsaveFormKeyMap()).
		WithShowHelp(false)
	f := &FormScreen{title: sanitizeLine(title), form: form, width: -1, height: -1}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

// proxsaveFormTheme derives a left-aligned form theme from the ProxSave
// palette (the huh default is purple and floats confirm buttons around).
func proxsaveFormTheme(isDark bool) *huh.Styles {
	s := huh.ThemeBase(isDark)

	s.Focused.Base = s.Focused.Base.BorderForeground(theme.Orange)
	s.Focused.Title = s.Focused.Title.Foreground(theme.Orange).Bold(true)
	s.Focused.Description = s.Focused.Description.Foreground(theme.Gray)
	s.Focused.ErrorIndicator = s.Focused.ErrorIndicator.Foreground(theme.Red)
	s.Focused.ErrorMessage = s.Focused.ErrorMessage.Foreground(theme.Red)
	s.Focused.SelectSelector = s.Focused.SelectSelector.Foreground(theme.Orange)
	s.Focused.Option = s.Focused.Option.Foreground(theme.Light)
	s.Focused.SelectedOption = s.Focused.SelectedOption.Foreground(theme.White).Bold(true)
	s.Focused.FocusedButton = s.Focused.FocusedButton.Foreground(theme.White).Background(theme.Orange).Bold(true)
	s.Focused.BlurredButton = s.Focused.BlurredButton.Foreground(theme.Light).Background(theme.Surface)
	s.Focused.TextInput.Cursor = s.Focused.TextInput.Cursor.Foreground(theme.Orange)
	s.Focused.TextInput.Prompt = s.Focused.TextInput.Prompt.Foreground(theme.Orange)
	s.Focused.TextInput.Text = s.Focused.TextInput.Text.Foreground(theme.White)

	s.Blurred = s.Focused
	s.Blurred.Base = s.Blurred.Base.BorderStyle(s.Blurred.Base.GetBorderStyle()).BorderForeground(theme.Surface)
	s.Blurred.Title = s.Blurred.Title.UnsetBold().Foreground(theme.Light)
	s.Blurred.TextInput.Text = s.Blurred.TextInput.Text.Foreground(theme.Light)
	s.Blurred.FocusedButton = s.Blurred.FocusedButton.UnsetBold().Foreground(theme.White).Background(theme.Gray)

	return s
}

// proxsaveFormKeyMap extends the default huh keymap so up/down arrows move
// between fields on inputs and confirms (selects keep the arrows for their
// own options, as expected).
func proxsaveFormKeyMap() *huh.KeyMap {
	km := huh.NewDefaultKeyMap()
	km.Input.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("shift+tab/up", "back"))
	km.Input.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("enter", "next"))
	km.Confirm.Prev = key.NewBinding(key.WithKeys("shift+tab", "up"), key.WithHelp("shift+tab/up", "back"))
	km.Confirm.Next = key.NewBinding(key.WithKeys("enter", "tab", "down"), key.WithHelp("enter", "next"))
	return km
}

func (f *FormScreen) Init() tea.Cmd { return f.form.Init() }

func (f *FormScreen) Title() string { return f.title }

func (f *FormScreen) Help() string {
	return "↑/↓ move · ←/→ toggle · enter next · esc cancel"
}

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
	// Resize only when the frame actually changed: re-applying the size
	// every render resets huh's internal group viewport (the top field's
	// title was being scrolled out of view).
	if width != f.width || height != f.height {
		f.width, f.height = width, height
		f.form = f.form.WithWidth(width).WithHeight(height - 1)
	}
	header := ""
	if f.title != "" {
		header = theme.Emphasis.Render(f.title) + "\n\n"
	}
	return header + f.form.View()
}
