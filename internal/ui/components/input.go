package components

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Input is a single-line text (or secret) prompt resolving to the entered
// string. Esc resolves shell.ErrAborted.
type Input struct {
	shell.Resolver[string]
	title    string
	prompt   string
	note     string
	ti       textinput.Model
	validate func(string) error
	errMsg   string
	backErr  error
}

// InputOption customizes an Input.
type InputOption func(*Input)

// WithSecret masks the typed value (passwords, tokens).
func WithSecret() InputOption {
	return func(i *Input) { i.ti.EchoMode = textinput.EchoPassword }
}

// WithInitialValue pre-fills the field.
func WithInitialValue(v string) InputOption {
	return func(i *Input) { i.ti.SetValue(sanitizeLine(v)) }
}

// WithPlaceholder sets the placeholder text.
func WithPlaceholder(p string) InputOption {
	return func(i *Input) { i.ti.Placeholder = sanitizeLine(p) }
}

// WithValidate rejects values for which f returns an error; the message is
// shown inline and the prompt stays active.
func WithValidate(f func(string) error) InputOption {
	return func(i *Input) { i.validate = f }
}

// WithNote adds an explanatory line under the prompt.
func WithNote(note string) InputOption {
	return func(i *Input) { i.note = sanitize(note) }
}

// WithErrorText pre-seeds the inline error line (e.g. the previous failed
// attempt on a retry prompt). Cleared on the next successful validation.
func WithErrorText(msg string) InputOption {
	return func(i *Input) { i.errMsg = sanitizeLine(msg) }
}

// WithInputBack makes Esc resolve with err (back-navigation sentinel)
// instead of the hard shell.ErrAborted.
func WithInputBack(err error) InputOption {
	return func(i *Input) { i.backErr = err }
}

// NewInput builds a text input screen.
func NewInput(title, prompt string, opts ...InputOption) *Input {
	in := &Input{title: sanitizeLine(title), prompt: sanitize(prompt), ti: textinput.New()}
	in.ti.SetVirtualCursor(true)
	for _, opt := range opts {
		opt(in)
	}
	return in
}

func (i *Input) Init() tea.Cmd { return i.ti.Focus() }

func (i *Input) Title() string { return i.title }

func (i *Input) Help() string {
	if i.backErr != nil {
		return "enter confirm · esc back"
	}
	return "enter confirm · esc cancel"
}

func (i *Input) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			value := i.ti.Value()
			if i.validate != nil {
				if err := i.validate(value); err != nil {
					// Validator errors can echo the (untrusted) value.
					i.errMsg = sanitizeLine(err.Error())
					return i, nil
				}
			}
			return i, i.Resolve(value, nil)
		case "esc":
			err := i.backErr
			if err == nil {
				err = shell.ErrAborted
			}
			return i, i.Resolve("", err)
		}
	}
	var cmd tea.Cmd
	i.ti, cmd = i.ti.Update(msg)
	return i, cmd
}

func (i *Input) View(width, height int) string {
	i.ti.SetWidth(max(width-4, 10))
	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(i.title))
	b.WriteString("\n\n")
	if i.prompt != "" {
		b.WriteString(theme.Text.Width(width).Render(i.prompt))
		b.WriteString("\n")
	}
	if i.note != "" {
		b.WriteString(theme.Subtle.Width(width).Render(i.note))
		b.WriteString("\n")
	}
	b.WriteString("\n" + i.ti.View())
	if i.errMsg != "" {
		b.WriteString("\n\n" + theme.ErrorText.Render(theme.SymbolError+" "+i.errMsg))
	}
	return b.String()
}
