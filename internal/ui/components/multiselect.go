package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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

	lastRowsTop int // body row of the first visible item (set by View)
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

// NewMultiSelect builds a checkbox list screen.
func NewMultiSelect[T any](title string, items []MultiSelectItem[T], opts ...MultiSelectOption[T]) *MultiSelect[T] {
	clean := make([]MultiSelectItem[T], len(items))
	for i, it := range items {
		clean[i] = MultiSelectItem[T]{
			Label:       sanitizeLine(it.Label),
			Description: sanitizeLine(it.Description),
			Value:       it.Value,
			Selected:    it.Selected,
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
	help := "↑/↓ move · space toggle · a all · i invert · enter confirm"
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

func (m *MultiSelect[T]) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch mouse := msg.(type) {
	case tea.MouseWheelMsg:
		switch mouse.Button {
		case tea.MouseWheelUp:
			if m.cursor > 0 {
				m.cursor--
			}
		case tea.MouseWheelDown:
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		}
		return m, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return m, nil
		}
		row := mouse.Y - m.lastRowsTop + m.offset
		if row >= 0 && row < len(m.items) {
			m.cursor = row
			m.items[row].Selected = !m.items[row].Selected
			m.errMsg = ""
		}
		return m, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = max(len(m.items)-1, 0)
	case "space":
		if len(m.items) > 0 {
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

	for row := m.offset; row < len(m.items) && row < m.offset+rows; row++ {
		it := m.items[row]
		box := theme.SymbolUncheck
		if it.Selected {
			box = theme.SymbolCheck
		}
		line := box + " " + it.Label
		if it.Description != "" {
			line += "  " + it.Description
		}
		line = ansi.Truncate(line, width-2, "…")
		switch {
		case row == m.cursor:
			b.WriteString(theme.Selected.Render(theme.SymbolSelected + " " + line))
		case it.Selected:
			b.WriteString(theme.SuccessText.Render("  " + line))
		default:
			b.WriteString(theme.Text.Render("  " + line))
		}
		b.WriteString("\n")
	}
	if m.errMsg != "" {
		b.WriteString(theme.ErrorText.Render(theme.SymbolError + " " + m.errMsg))
	}
	return strings.TrimRight(b.String(), "\n")
}
