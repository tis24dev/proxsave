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
	intro    []string // consent/intro note rendered ABOVE the fields (one line each)
	fields   []*FormField
	backErr  error
	cursor   int // index into fields; len(fields) = buttons row
	onCancel bool
	offset   int
	errMsg   string
	ti       textinput.Model
	editing  int // field index currently bound to ti, -1 none

	// Layout from the last render (body coordinates) for mouse hit-testing.
	lastRowsTop                    int
	lastWindowEnd                  int
	lastButtonsY                   int
	contX0, contX1, cancX0, cancX1 int
}

// FormGridOption customizes a FormGrid.
type FormGridOption func(*FormGrid)

// WithFormGridBack overrides the Esc/Cancel error (default shell.ErrAborted).
func WithFormGridBack(err error) FormGridOption {
	return func(g *FormGrid) { g.backErr = err }
}

// WithFormGridNote sets an intro/consent note rendered ABOVE the fields, one
// line per string. Pass each clause as its own line (coherent, never broken
// mid-sentence) — the grid does not re-wrap them. Lines are sanitized and empty
// ones dropped.
func WithFormGridNote(lines ...string) FormGridOption {
	return func(g *FormGrid) {
		note := make([]string, 0, len(lines))
		for _, l := range lines {
			if s := sanitizeLine(l); s != "" {
				note = append(note, s)
			}
		}
		g.intro = note
	}
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
	switch mouse := msg.(type) {
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			g.move(-1)
		case tea.MouseWheelDown:
			g.move(1)
		}
		return g, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return g, nil
		}
		if mouse.Y == g.lastButtonsY {
			switch {
			case mouse.X >= g.contX0 && mouse.X < g.contX1:
				g.cursor = len(g.fields)
				g.onCancel = false
				return g.submit()
			case mouse.X >= g.cancX0 && mouse.X < g.cancX1:
				return g, g.Resolve(struct{}{}, g.backErr)
			}
			return g, nil
		}
		// Only clicks inside the rendered field window map to a field; reject the
		// title/intro/blank above and the blank separator below (which, when
		// scrolled, would otherwise hit an off-screen field).
		if mouse.Y < g.lastRowsTop || mouse.Y >= g.lastWindowEnd {
			return g, nil
		}
		row := mouse.Y - g.lastRowsTop + g.offset
		if row >= 0 && row < len(g.fields) && g.fields[row].active() {
			g.cursor = row
			g.bindEditor()
			f := g.fields[row]
			switch f.Kind {
			case FieldToggle:
				f.Bool = !f.Bool
				g.errMsg = ""
			case FieldSelect:
				if len(f.Options) > 0 {
					f.OptionIndex = (f.OptionIndex + 1) % len(f.Options)
				}
			}
		}
		return g, nil
	}

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
			// Jump back to the last active field (stay on the buttons row if none).
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
		if len(f.Options) == 0 {
			// A select with no options is not navigable; guard the modulo so
			// left/right/space can never divide by zero (mirrors renderControl).
			return g, nil
		}
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

	// Footer zone: hint (focused field description) and/or error, wrapped
	// at a readable measure instead of running across the whole terminal.
	hintWidth := min(width, 100)
	footer := make([]string, 0, 2)
	if g.errMsg != "" {
		footer = append(footer, theme.ErrorText.Width(hintWidth).Render(theme.SymbolError+" "+g.errMsg))
	} else if !onButtons && g.cursor < len(g.fields) {
		if d := g.fields[g.cursor].Description; d != "" {
			// One sentence per line: never break a line mid-sentence.
			for _, sentence := range splitSentences(d) {
				footer = append(footer, theme.Subtle.Width(hintWidth).Render(sentence))
			}
		}
	}
	footerHeight := 0
	for _, block := range footer {
		footerHeight += lipgloss.Height(block)
	}

	// Intro/consent note rendered ABOVE the fields: fixed (never scrolled), one
	// line per clause. Styled like the field hints for cosmetic consistency.
	introWidth := min(width, 100)
	intro := make([]string, 0, len(g.intro))
	for _, line := range g.intro {
		intro = append(intro, theme.Subtle.Width(introWidth).Render(line))
	}
	introHeight := 0
	for _, block := range intro {
		introHeight += lipgloss.Height(block)
	}

	// Scroll window over the field rows so buttons/footer stay visible.
	head := 2 // title + blank
	if len(intro) > 0 {
		head += introHeight + 1 // note lines + a blank separator before the fields
	}
	tailLines := 2 + footerHeight
	if len(footer) > 0 {
		tailLines++ // blank line between the buttons and the footer
	}
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

	// Mouse hit-test layout: rows start after title+blank; buttons after
	// the window plus its blank separator.
	g.lastRowsTop = head
	g.lastWindowEnd = head + len(windowed)
	g.lastButtonsY = g.lastWindowEnd + 1
	contBtnW := lipgloss.Width(continueBtn)
	cancBtnW := lipgloss.Width(cancelBtn)
	g.contX0 = 2 + labelW + 2
	g.contX1 = g.contX0 + contBtnW
	g.cancX0 = g.contX1 + 2
	g.cancX1 = g.cancX0 + cancBtnW

	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(g.title))
	b.WriteString("\n\n")
	if len(intro) > 0 {
		b.WriteString(strings.Join(intro, "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(strings.Join(windowed, "\n"))
	b.WriteString("\n\n")
	b.WriteString(buttons)
	if len(footer) > 0 {
		// Same breathing room below the buttons as above them.
		b.WriteString("\n")
	}
	for _, line := range footer {
		b.WriteString("\n" + line)
	}
	return b.String()
}

// splitSentences breaks text at sentence boundaries: a ". " followed by an
// uppercase letter (so "e.g. /mnt" or "e.g. foo" never split).
func splitSentences(text string) []string {
	var out []string
	runes := []rune(text)
	start := 0
	for i := 0; i+2 < len(runes); i++ {
		if runes[i] == '.' && runes[i+1] == ' ' && runes[i+2] >= 'A' && runes[i+2] <= 'Z' {
			out = append(out, strings.TrimSpace(string(runes[start:i+1])))
			start = i + 2
		}
	}
	if rest := strings.TrimSpace(string(runes[start:])); rest != "" {
		out = append(out, rest)
	}
	return out
}

func padRight(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
