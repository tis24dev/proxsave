package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// FieldKind selects the control rendered on a FormGrid row.
type FieldKind int

const (
	FieldToggle FieldKind = iota
	FieldText
	FieldSelect
)

// FormField is one row of a FormGrid: label on the left, control on the
// right, every control starting at the same column (the classic aligned
// form layout of the old tview wizard). The caller keeps the pointers and
// reads Bool/Text/OptionIndex back after the grid resolves.
type FormField struct {
	Label       string
	Description string // shown as a hint line while the field is focused
	Kind        FieldKind

	Bool        bool     // toggle value
	Text        string   // text value
	Secret      bool     // mask text
	Options     []string // select options (display strings)
	OptionIndex int

	// Validate rejects a text value on submit (and inline when leaving the
	// field); only called while the field is Active.
	Validate func(value string) error
	// Active gates the field: inactive rows render dimmed, are skipped by
	// navigation, and are not validated. nil = always active.
	Active func() bool
}

func (f *FormField) active() bool { return f.Active == nil || f.Active() }

// FormGrid is a single-screen aligned form. Up/down move between rows,
// left/right/space drive toggles and selects, text rows edit inline; Enter
// advances and, on the Continue button, submits (validating every active
// field). Esc cancels.
type FormGrid struct {
	shell.Resolver[struct{}]
	title    string
	fields   []*FormField
	backErr  error
	cursor   int // index into fields; len(fields) = buttons row
	onCancel bool
	offset   int
	errMsg   string
	ti       textinput.Model
	editing  int // field index currently bound to ti, -1 none
}

// FormGridOption customizes a FormGrid.
type FormGridOption func(*FormGrid)

// WithFormGridBack overrides the Esc/Cancel error (default shell.ErrAborted).
func WithFormGridBack(err error) FormGridOption {
	return func(g *FormGrid) { g.backErr = err }
}

// NewFormGrid builds the grid. Field labels/descriptions are sanitized; the
// field structs stay owned by the caller.
func NewFormGrid(title string, fields []*FormField, opts ...FormGridOption) *FormGrid {
	for _, f := range fields {
		f.Label = sanitizeLine(f.Label)
		f.Description = sanitizeLine(f.Description)
		for i := range f.Options {
			f.Options[i] = sanitizeLine(f.Options[i])
		}
		if f.OptionIndex < 0 || f.OptionIndex >= len(f.Options) {
			f.OptionIndex = 0
		}
	}
	g := &FormGrid{
		title:   sanitizeLine(title),
		fields:  fields,
		backErr: shell.ErrAborted,
		editing: -1,
		ti:      textinput.New(),
	}
	for _, opt := range opts {
		opt(g)
	}
	g.cursor = g.firstActive()
	g.bindEditor()
	return g
}

func (g *FormGrid) firstActive() int {
	for i, f := range g.fields {
		if f.active() {
			return i
		}
	}
	return len(g.fields) // straight to buttons
}

// bindEditor attaches the shared text editor to the focused field.
func (g *FormGrid) bindEditor() {
	if g.editing >= 0 && g.editing < len(g.fields) {
		g.fields[g.editing].Text = g.ti.Value()
	}
	g.editing = -1
	if g.cursor < len(g.fields) {
		f := g.fields[g.cursor]
		if f.Kind == FieldText && f.active() {
			g.ti.SetValue(f.Text)
			g.ti.CursorEnd() // SetValue keeps the previous cursor position
			if f.Secret {
				g.ti.EchoMode = textinput.EchoPassword
			} else {
				g.ti.EchoMode = textinput.EchoNormal
			}
			g.ti.Focus()
			g.editing = g.cursor
		}
	}
}

// move advances the cursor to the next/previous active row (or the buttons
// row past the end).
func (g *FormGrid) move(delta int) {
	i := g.cursor
	for {
		i += delta
		if i < 0 {
			return
		}
		if i >= len(g.fields) {
			g.cursor = len(g.fields)
			g.onCancel = false
			g.bindEditor()
			return
		}
		if g.fields[i].active() {
			g.cursor = i
			g.bindEditor()
			return
		}
	}
}

func (g *FormGrid) Init() tea.Cmd {
	if g.editing >= 0 {
		return g.ti.Focus()
	}
	return nil
}

func (g *FormGrid) Title() string { return g.title }

func (g *FormGrid) Help() string {
	return "↑/↓ move · ←/→/space toggle · enter next/confirm · esc cancel"
}

func (g *FormGrid) submit() (shell.Screen, tea.Cmd) {
	// Sync the editor, then validate every active text field in order.
	g.bindEditor()
	for i, f := range g.fields {
		if !f.active() || f.Kind != FieldText || f.Validate == nil {
			continue
		}
		if err := f.Validate(f.Text); err != nil {
			g.errMsg = fmt.Sprintf("%s: %v", f.Label, err)
			g.cursor = i
			g.bindEditor()
			return g, nil
		}
	}
	return g, g.Resolve(struct{}{}, nil)
}

func (g *FormGrid) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		if g.editing >= 0 {
			var cmd tea.Cmd
			g.ti, cmd = g.ti.Update(msg)
			return g, cmd
		}
		return g, nil
	}

	onButtons := g.cursor >= len(g.fields)
	switch key.String() {
	case "esc":
		return g, g.Resolve(struct{}{}, g.backErr)
	case "up", "shift+tab":
		if onButtons {
			g.cursor = len(g.fields) // stay, move() handles from a field
			// jump back to the last active field
			g.cursor = len(g.fields)
			for i := len(g.fields) - 1; i >= 0; i-- {
				if g.fields[i].active() {
					g.cursor = i
					break
				}
			}
			g.bindEditor()
			return g, nil
		}
		g.move(-1)
		return g, nil
	case "down", "tab":
		g.move(1)
		return g, nil
	case "enter":
		if onButtons {
			if g.onCancel {
				return g, g.Resolve(struct{}{}, g.backErr)
			}
			return g.submit()
		}
		g.errMsg = ""
		g.move(1)
		return g, nil
	}

	if onButtons {
		switch key.String() {
		case "left", "right":
			g.onCancel = !g.onCancel
		}
		return g, nil
	}

	f := g.fields[g.cursor]
	switch f.Kind {
	case FieldToggle:
		switch key.String() {
		case "left", "right", "space", "y":
			if key.String() == "y" {
				f.Bool = true
			} else if key.String() == "space" || key.String() == "left" || key.String() == "right" {
				f.Bool = !f.Bool
			}
			g.errMsg = ""
			return g, nil
		case "n":
			f.Bool = false
			g.errMsg = ""
			return g, nil
		}
	case FieldSelect:
		switch key.String() {
		case "left":
			f.OptionIndex = (f.OptionIndex - 1 + len(f.Options)) % len(f.Options)
			return g, nil
		case "right", "space":
			f.OptionIndex = (f.OptionIndex + 1) % len(f.Options)
			return g, nil
		}
	case FieldText:
		var cmd tea.Cmd
		g.ti, cmd = g.ti.Update(msg)
		g.fields[g.cursor].Text = g.ti.Value()
		return g, cmd
	}
	return g, nil
}

func (g *FormGrid) labelWidth() int {
	w := 0
	for _, f := range g.fields {
		if l := lipgloss.Width(f.Label); l > w {
			w = l
		}
	}
	return w
}

func (g *FormGrid) renderControl(f *FormField, focused bool, width int) string {
	dim := !f.active()
	switch f.Kind {
	case FieldToggle:
		yes, no := "Yes", "No"
		style := func(val string, sel bool) string {
			switch {
			case dim:
				return theme.Subtle.Render(" " + val + " ")
			case sel && focused:
				return theme.ButtonFocused.Render(val)
			case sel:
				return theme.Selected.Render(" " + val + " ")
			default:
				return theme.Subtle.Render(" " + val + " ")
			}
		}
		return style(yes, f.Bool) + " " + style(no, !f.Bool)
	case FieldSelect:
		val := ""
		if len(f.Options) > 0 {
			val = f.Options[f.OptionIndex]
		}
		switch {
		case dim:
			return theme.Subtle.Render("  " + val)
		case focused:
			return theme.WarningText.Render("‹ ") + theme.ButtonFocused.Render(val) + theme.WarningText.Render(" ›")
		default:
			return theme.Text.Render("  " + val)
		}
	default: // FieldText
		if focused && g.editing == g.cursor {
			g.ti.SetWidth(max(width, 10))
			return g.ti.View()
		}
		val := f.Text
		if f.Secret && val != "" {
			val = strings.Repeat("*", len([]rune(val)))
		}
		if val == "" {
			val = "-"
		}
		val = ansi.Truncate(val, max(width, 10), "…")
		if dim {
			return theme.Subtle.Render(val)
		}
		return theme.Text.Render(val)
	}
}

func (g *FormGrid) View(width, height int) string {
	labelW := g.labelWidth()
	controlW := max(width-labelW-4, 10)

	rows := make([]string, 0, len(g.fields)+2)
	for i, f := range g.fields {
		focused := i == g.cursor
		label := f.Label
		prefix := "  "
		labelStyle := theme.Text
		if !f.active() {
			labelStyle = theme.Subtle
		}
		if focused {
			prefix = theme.Title.Render(theme.SymbolSelected + " ")
			labelStyle = theme.Title
		}
		row := prefix + labelStyle.Render(padRight(label, labelW)) + "  " + g.renderControl(f, focused, controlW)
		rows = append(rows, row)
	}

	// Buttons row.
	onButtons := g.cursor >= len(g.fields)
	continueBtn := theme.ButtonBlurred.Render("Continue")
	cancelBtn := theme.ButtonBlurred.Render("Cancel")
	if onButtons && !g.onCancel {
		continueBtn = theme.ButtonFocused.Render("Continue")
	}
	if onButtons && g.onCancel {
		cancelBtn = theme.ButtonFocused.Render("Cancel")
	}
	buttons := "  " + strings.Repeat(" ", labelW) + "  " + continueBtn + "  " + cancelBtn

	// Footer zone: hint (focused field description) and/or error.
	footer := make([]string, 0, 2)
	if g.errMsg != "" {
		footer = append(footer, theme.ErrorText.Render(theme.SymbolError+" "+g.errMsg))
	} else if !onButtons && g.cursor < len(g.fields) {
		if d := g.fields[g.cursor].Description; d != "" {
			footer = append(footer, theme.Subtle.Width(width).Render(d))
		}
	}

	// Scroll window over the field rows so buttons/footer stay visible.
	head := 2 // title + blank
	tailLines := 2 + len(footer)
	visible := max(height-head-tailLines, 1)
	if g.cursor < len(g.fields) {
		if g.cursor < g.offset {
			g.offset = g.cursor
		}
		if g.cursor >= g.offset+visible {
			g.offset = g.cursor - visible + 1
		}
	}
	if g.offset < 0 {
		g.offset = 0
	}
	end := min(g.offset+visible, len(rows))
	windowed := rows[g.offset:end]

	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(g.title))
	b.WriteString("\n\n")
	b.WriteString(strings.Join(windowed, "\n"))
	b.WriteString("\n\n")
	b.WriteString(buttons)
	for _, line := range footer {
		b.WriteString("\n" + line)
	}
	return b.String()
}

func padRight(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
