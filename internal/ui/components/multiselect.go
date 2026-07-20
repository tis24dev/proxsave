package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// MultiSelectItem is one toggleable row.
type MultiSelectItem[T any] struct {
	Label       string
	Description string
	Value       T
	Selected    bool
	// Detail is the long text shown in the side detail pane for this row (used
	// only when the MultiSelect has a detail pane).
	Detail string
}

// MultiSelect is a checkbox list resolving to the values of the selected
// items, in item order. Space toggles, Enter confirms (subject to the
// minimum), Esc resolves the back sentinel when configured.
type MultiSelect[T any] struct {
	shell.Resolver[[]T]
	title       string
	prompt      string
	items       []MultiSelectItem[T]
	cursor      int
	offset      int
	backErr     error
	minSelected int
	errMsg      string

	// Optional action buttons rendered after the list. When enabled, a plain
	// Enter no longer confirms the whole screen: Enter on a toggle row toggles it,
	// Enter on the select-all button toggles every item, and only Enter on the
	// confirm button resolves the selection.
	actions        bool
	selectAllLabel string
	confirmLabel   string

	// Optional side detail pane: the list renders on the left and the highlighted
	// item's Detail on the right (a two-pane layout).
	detailPane  bool
	detailTitle string

	lastRowsTop   int // body row of the first visible item (set by View)
	lastWindowEnd int // body row just past the last rendered row (set by View)
}

// MultiSelectOption customizes a MultiSelect.
type MultiSelectOption[T any] func(*MultiSelect[T])

// WithMultiSelectBack makes Esc resolve with err (back-navigation sentinel).
func WithMultiSelectBack[T any](err error) MultiSelectOption[T] {
	return func(m *MultiSelect[T]) { m.backErr = err }
}

// WithMinSelected rejects confirmation until at least n items are selected.
func WithMinSelected[T any](n int) MultiSelectOption[T] {
	return func(m *MultiSelect[T]) { m.minSelected = n }
}

// WithMultiSelectPrompt adds an explanatory line under the title.
func WithMultiSelectPrompt[T any](prompt string) MultiSelectOption[T] {
	return func(m *MultiSelect[T]) { m.prompt = sanitize(prompt) }
}

// WithMultiSelectActions renders two action buttons at the end of the list and
// disables the generic Enter-confirms behavior. selectAllLabel toggles every item
// (select all, or deselect all when already all selected); confirmLabel is the
// only control that resolves the selection. Enter on a toggle row toggles that row.
func WithMultiSelectActions[T any](selectAllLabel, confirmLabel string) MultiSelectOption[T] {
	return func(m *MultiSelect[T]) {
		m.actions = true
		m.selectAllLabel = sanitizeLine(selectAllLabel)
		m.confirmLabel = sanitizeLine(confirmLabel)
	}
}

// WithMultiSelectDetailPane renders a side pane showing the highlighted item's
// Detail text (a two-pane list/description layout). title labels the pane.
func WithMultiSelectDetailPane[T any](title string) MultiSelectOption[T] {
	return func(m *MultiSelect[T]) {
		m.detailPane = true
		m.detailTitle = sanitizeLine(title)
	}
}

// NewMultiSelect builds a checkbox list screen.
func NewMultiSelect[T any](title string, items []MultiSelectItem[T], opts ...MultiSelectOption[T]) *MultiSelect[T] {
	clean := make([]MultiSelectItem[T], len(items))
	for i, it := range items {
		clean[i] = MultiSelectItem[T]{
			Label:       sanitizeLine(it.Label),
			Description: sanitizeLine(it.Description),
			Value:       it.Value,
			Selected:    it.Selected,
			Detail:      sanitize(it.Detail),
		}
	}
	m := &MultiSelect[T]{title: sanitizeLine(title), items: clean}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *MultiSelect[T]) Init() tea.Cmd { return nil }

func (m *MultiSelect[T]) Title() string { return m.title }

func (m *MultiSelect[T]) Help() string {
	var help string
	if m.actions {
		help = "↑/↓ move · space toggle · a all · i invert · enter act on row"
	} else {
		help = "↑/↓ move · space toggle · a all · i invert · enter confirm"
	}
	if m.backErr != nil {
		help += " · esc back"
	}
	return help
}

func (m *MultiSelect[T]) selectedCount() int {
	n := 0
	for _, it := range m.items {
		if it.Selected {
			n++
		}
	}
	return n
}

// hasSpacer reports whether a blank divider row sits between the items and the
// action buttons (only when there is at least one item to separate from).
func (m *MultiSelect[T]) hasSpacer() bool { return m.actions && len(m.items) > 0 }

// actionRowCount is the number of trailing rows after the items: 0 (no actions),
// 2 (the two buttons, no items to separate), or 3 (blank spacer + two buttons).
func (m *MultiSelect[T]) actionRowCount() int {
	switch {
	case !m.actions:
		return 0
	case m.hasSpacer():
		return 3
	default:
		return 2
	}
}

// totalRows is every row the cursor/scroll span: items + spacer + buttons.
func (m *MultiSelect[T]) totalRows() int { return len(m.items) + m.actionRowCount() }

// spacerRow is the index of the blank divider row, or -1 when there is none. It
// is non-navigable: the cursor skips it and clicks on it are a no-op.
func (m *MultiSelect[T]) spacerRow() int {
	if m.hasSpacer() {
		return len(m.items)
	}
	return -1
}

// selectAllRow / confirmRow are the row indices of the two buttons (valid only
// when actions are enabled).
func (m *MultiSelect[T]) selectAllRow() int {
	if m.hasSpacer() {
		return len(m.items) + 1
	}
	return len(m.items)
}
func (m *MultiSelect[T]) confirmRow() int { return m.selectAllRow() + 1 }

// setCursor clamps nc into range and, if it lands on the non-navigable spacer,
// nudges one more step in the travel direction (dir = +1 down, -1 up).
func (m *MultiSelect[T]) setCursor(nc, dir int) {
	last := m.totalRows() - 1
	if nc < 0 {
		nc = 0
	}
	if nc > last {
		nc = last
	}
	if nc == m.spacerRow() {
		nc += dir
		if nc < 0 {
			nc = 0
		}
		if nc > last {
			nc = last
		}
	}
	m.cursor = nc
}

// toggleAll selects every item, or deselects every item when all are already
// selected (a select-all/deselect-all toggle).
func (m *MultiSelect[T]) toggleAll() {
	allSelected := len(m.items) > 0 && m.selectedCount() == len(m.items)
	for i := range m.items {
		m.items[i].Selected = !allSelected
	}
	m.errMsg = ""
}

// confirm resolves the selection subject to the minimum, or sets an error.
func (m *MultiSelect[T]) confirm() (shell.Screen, tea.Cmd) {
	if n := m.selectedCount(); n < m.minSelected {
		m.errMsg = fmt.Sprintf("Select at least %d item(s); %d selected", m.minSelected, n)
		return m, nil
	}
	values := make([]T, 0, m.selectedCount())
	for _, it := range m.items {
		if it.Selected {
			values = append(values, it.Value)
		}
	}
	return m, m.Resolve(values, nil)
}

func (m *MultiSelect[T]) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch mouse := msg.(type) {
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.setCursor(m.cursor-1, -1)
		case tea.MouseWheelDown:
			m.setCursor(m.cursor+1, +1)
		}
		return m, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		// Only clicks inside the rendered row band map to a row; reject the
		// title/blank above and the padded/tail lines below, which when scrolled
		// would otherwise map to a valid off-screen item (or the hidden confirm
		// button, silently resolving the screen).
		if mouse.Y < m.lastRowsTop || mouse.Y >= m.lastWindowEnd {
			return m, nil
		}
		row := mouse.Y - m.lastRowsTop + m.offset
		if row < 0 || row >= m.totalRows() || row == m.spacerRow() {
			return m, nil
		}
		m.cursor = row
		switch {
		case row < len(m.items):
			m.items[row].Selected = !m.items[row].Selected
			m.errMsg = ""
		case m.actions && row == m.selectAllRow():
			m.toggleAll()
		case m.actions && row == m.confirmRow():
			return m.confirm()
		}
		return m, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		m.setCursor(m.cursor-1, -1)
	case "down", "j":
		m.setCursor(m.cursor+1, +1)
	case "home":
		m.setCursor(0, +1)
	case "end":
		m.setCursor(max(m.totalRows()-1, 0), -1)
	case "space":
		if m.cursor < len(m.items) && len(m.items) > 0 {
			m.items[m.cursor].Selected = !m.items[m.cursor].Selected
			m.errMsg = ""
		}
	case "a":
		for i := range m.items {
			m.items[i].Selected = true
		}
		m.errMsg = ""
	case "i":
		for i := range m.items {
			m.items[i].Selected = !m.items[i].Selected
		}
		m.errMsg = ""
	case "enter":
		if m.actions {
			switch m.cursor {
			case m.confirmRow():
				return m.confirm()
			case m.selectAllRow():
				m.toggleAll()
			default:
				if m.cursor < len(m.items) {
					m.items[m.cursor].Selected = !m.items[m.cursor].Selected
					m.errMsg = ""
				}
			}
			return m, nil
		}
		return m.confirm()
	case "esc":
		if m.backErr != nil {
			return m, m.Resolve(nil, m.backErr)
		}
	}
	return m, nil
}

func (m *MultiSelect[T]) View(width, height int) string {
	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(m.title))
	b.WriteString(theme.Subtle.Render(fmt.Sprintf("  (%d/%d selected)", m.selectedCount(), len(m.items))))
	if m.prompt != "" {
		b.WriteString("\n" + theme.Text.Render(m.prompt))
	}
	b.WriteString("\n\n")

	overhead := len(strings.Split(b.String(), "\n"))
	m.lastRowsTop = overhead - 1 // rows start after title/prompt/blank
	if m.errMsg != "" {
		overhead++
	}
	rows := max(height-overhead, 1)

	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
	// Rendered row band = [lastRowsTop, lastWindowEnd) over items+spacer+buttons.
	// lipgloss.Place top-pads the body (and a tall detail pane can overrun the list),
	// so clicks below the window must not map to an off-screen row (or the hidden
	// confirm button).
	m.lastWindowEnd = m.lastRowsTop + max(min(m.totalRows(), m.offset+rows)-m.offset, 0)

	// leftWidth is the list column width; with a detail pane the description takes
	// the rest. Fall back to a single column when the terminal is too narrow.
	leftWidth := width
	twoPane := m.detailPane && width >= 48
	sep := " │ "
	if twoPane {
		leftWidth = min(max(width*3/7, 28), 44)
	}

	visibleRows := func() []int {
		out := []int{}
		for row := m.offset; row < m.totalRows() && row < m.offset+rows; row++ {
			out = append(out, row)
		}
		return out
	}

	if twoPane {
		rightWidth := width - leftWidth - lipgloss.Width(sep)
		leftLines := []string{}
		for _, row := range visibleRows() {
			leftLines = append(leftLines, m.renderRow(row, leftWidth))
		}
		rightLines := m.detailLines(rightWidth, rows)
		n := max(len(leftLines), len(rightLines))
		for i := 0; i < n; i++ {
			l, r := "", ""
			if i < len(leftLines) {
				l = leftLines[i]
			}
			if i < len(rightLines) {
				r = rightLines[i]
			}
			b.WriteString(padCol(l, leftWidth) + theme.Subtle.Render(sep) + r + "\n")
		}
	} else {
		for _, row := range visibleRows() {
			b.WriteString(m.renderRow(row, leftWidth))
			b.WriteString("\n")
		}
	}
	if m.errMsg != "" {
		b.WriteString(theme.ErrorText.Render(theme.SymbolError + " " + m.errMsg))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderRow renders a single body row (item, spacer, or action button) within
// colWidth. A spacer row renders as an empty line.
func (m *MultiSelect[T]) renderRow(row, colWidth int) string {
	if row == m.spacerRow() {
		return ""
	}
	if row >= len(m.items) {
		label := m.selectAllLabel
		if row == m.confirmRow() {
			label = m.confirmLabel
		}
		btn := ansi.Truncate("[ "+label+" ]", colWidth-2, "…")
		if row == m.cursor {
			return theme.Selected.Render(theme.SymbolSelected + " " + btn)
		}
		return theme.Emphasis.Render("  " + btn)
	}
	it := m.items[row]
	box := theme.SymbolUncheck
	if it.Selected {
		box = theme.SymbolCheck
	}
	line := box + " " + it.Label
	// Without a detail pane the first message rides inline; with a detail pane
	// the list stays clean (just the key) and the description moves to the pane.
	if it.Description != "" && !m.detailPane {
		line += "  " + it.Description
	}
	line = ansi.Truncate(line, colWidth-2, "…")
	switch {
	case row == m.cursor:
		return theme.Selected.Render(theme.SymbolSelected + " " + line)
	case it.Selected:
		return theme.SuccessText.Render("  " + line)
	default:
		return theme.Text.Render("  " + line)
	}
}

// detailLines renders the side pane for the highlighted row: the pane title then
// the wrapped Detail text (or a hint when the cursor is on an action button),
// capped at maxLines.
func (m *MultiSelect[T]) detailLines(width, maxLines int) []string {
	if width < 4 {
		return nil
	}
	var out []string
	if m.detailTitle != "" {
		out = append(out, theme.Emphasis.Render(ansi.Truncate(m.detailTitle, width, "…")), "")
	}

	detail := ""
	switch {
	case m.cursor < len(m.items):
		detail = strings.TrimSpace(m.items[m.cursor].Detail)
	case m.actions && m.cursor == m.selectAllRow():
		detail = m.selectAllLabel + " checks or unchecks every component. Then choose " + m.confirmLabel + " to apply."
	case m.actions && m.cursor == m.confirmRow():
		detail = m.confirmLabel + " writes KEY=false for each checked component and applies the change."
	}
	if detail == "" {
		out = append(out, theme.Subtle.Render("(no description)"))
	} else {
		out = append(out, strings.Split(theme.Text.Width(width).Render(detail), "\n")...)
	}
	if len(out) > maxLines {
		out = out[:maxLines]
	}
	return out
}

// padCol pads a possibly-styled line with trailing spaces to a visible width w.
func padCol(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}
