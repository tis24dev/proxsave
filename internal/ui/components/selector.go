// Package components provides the reusable Charm screens ProxSave flows are
// built from: list selection, confirmation (with countdown), text input,
// task progress, paged text, and notices. Every component embeds
// shell.Resolver and is driven through shell.Ask.
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

// filterThreshold is the item count above which the selector offers "/"
// filtering.
const filterThreshold = 8

// SelectorItem is one selectable row.
type SelectorItem[T any] struct {
	Label       string
	Description string
	Value       T
}

// Selector is a list picker that resolves to the chosen item's value.
type Selector[T any] struct {
	shell.Resolver[T]
	title     string
	items     []SelectorItem[T]
	cursor    int // index into the visible (filtered) rows
	offset    int // scroll offset into the visible rows
	filter    string
	filtering bool
	backErr   error
	prompt    string
}

// SelectorOption customizes a Selector.
type SelectorOption[T any] func(*Selector[T])

// WithSelectorBack makes Esc resolve with err (e.g. a back-navigation
// sentinel) instead of being ignored.
func WithSelectorBack[T any](err error) SelectorOption[T] {
	return func(s *Selector[T]) { s.backErr = err }
}

// WithSelectorCursor preselects the row at index i.
func WithSelectorCursor[T any](i int) SelectorOption[T] {
	return func(s *Selector[T]) {
		if i >= 0 && i < len(s.items) {
			s.cursor = i
		}
	}
}

// WithSelectorPrompt adds an explanatory line under the title.
func WithSelectorPrompt[T any](prompt string) SelectorOption[T] {
	return func(s *Selector[T]) { s.prompt = sanitize(prompt) }
}

// NewSelector builds a list picker screen. Labels and descriptions are
// sanitized: they routinely carry untrusted data (backup filenames).
func NewSelector[T any](title string, items []SelectorItem[T], opts ...SelectorOption[T]) *Selector[T] {
	clean := make([]SelectorItem[T], len(items))
	for i, it := range items {
		clean[i] = SelectorItem[T]{
			Label:       sanitizeLine(it.Label),
			Description: sanitizeLine(it.Description),
			Value:       it.Value,
		}
	}
	s := &Selector[T]{title: sanitizeLine(title), items: clean}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Selector[T]) Init() tea.Cmd { return nil }

func (s *Selector[T]) Title() string { return s.title }

func (s *Selector[T]) Help() string {
	parts := []string{"↑/↓ move", "enter select"}
	if len(s.items) > filterThreshold {
		parts = append(parts, "/ filter")
	}
	if s.backErr != nil {
		parts = append(parts, "esc back")
	}
	return strings.Join(parts, " · ")
}

// visible returns the indexes of items matching the current filter.
func (s *Selector[T]) visible() []int {
	if s.filter == "" {
		idx := make([]int, len(s.items))
		for i := range s.items {
			idx[i] = i
		}
		return idx
	}
	needle := strings.ToLower(s.filter)
	var idx []int
	for i, it := range s.items {
		if strings.Contains(strings.ToLower(it.Label), needle) ||
			strings.Contains(strings.ToLower(it.Description), needle) {
			idx = append(idx, i)
		}
	}
	return idx
}

func (s *Selector[T]) clampCursor(visibleLen int) {
	if s.cursor >= visibleLen {
		s.cursor = visibleLen - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
}

func (s *Selector[T]) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	vis := s.visible()
	s.clampCursor(len(vis))

	if s.filtering {
		switch key.String() {
		case "esc":
			s.filtering = false
			s.filter = ""
			s.cursor = 0
			return s, nil
		case "enter":
			s.filtering = false
			return s, nil
		case "backspace":
			if s.filter != "" {
				r := []rune(s.filter)
				s.filter = string(r[:len(r)-1])
			}
			s.cursor = 0
			return s, nil
		case "up", "down":
			// fall through to navigation below
		default:
			if key.Text != "" {
				s.filter += key.Text
				s.cursor = 0
				return s, nil
			}
			return s, nil
		}
	}

	switch key.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
		return s, nil
	case "down", "j":
		if s.cursor < len(vis)-1 {
			s.cursor++
		}
		return s, nil
	case "home":
		s.cursor = 0
		return s, nil
	case "end":
		s.cursor = len(vis) - 1
		s.clampCursor(len(vis))
		return s, nil
	case "enter":
		if len(vis) > 0 {
			return s, s.Resolve(s.items[vis[s.cursor]].Value, nil)
		}
		return s, nil
	case "esc":
		if s.filter != "" {
			// First esc clears a retained filter, matching the behavior
			// while editing; only a second esc navigates back.
			s.filter = ""
			s.cursor = 0
			return s, nil
		}
		if s.backErr != nil {
			var zero T
			return s, s.Resolve(zero, s.backErr)
		}
		return s, nil
	case "/":
		if len(s.items) > filterThreshold {
			s.filtering = true
			s.filter = ""
			s.cursor = 0
		}
		return s, nil
	}

	// Digit shortcuts select a row directly (only meaningful for short,
	// unfiltered lists).
	if !s.filtering && s.filter == "" && len(key.Text) == 1 && key.Text >= "1" && key.Text <= "9" {
		n := int(key.Text[0] - '0')
		if n <= len(vis) && len(s.items) <= 9 {
			return s, s.Resolve(s.items[vis[n-1]].Value, nil)
		}
	}
	return s, nil
}

func (s *Selector[T]) View(width, height int) string {
	vis := s.visible()
	s.clampCursor(len(vis))

	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(s.title))
	if s.prompt != "" {
		b.WriteString("\n" + theme.Text.Render(s.prompt))
	}
	b.WriteString("\n\n")

	overhead := lipgloss.Height(b.String())
	if s.filtering || s.filter != "" {
		overhead++
	}
	rows := max(height-overhead, 1)

	// Keep the cursor inside the scroll window.
	if s.cursor < s.offset {
		s.offset = s.cursor
	}
	if s.cursor >= s.offset+rows {
		s.offset = s.cursor - rows + 1
	}
	if s.offset < 0 {
		s.offset = 0
	}

	showDigits := len(s.items) <= 9 && s.filter == ""
	for row := s.offset; row < len(vis) && row < s.offset+rows; row++ {
		it := s.items[vis[row]]
		prefix := "  "
		if showDigits {
			prefix = fmt.Sprintf("%d ", row+1)
		}
		line := prefix + it.Label
		if it.Description != "" {
			line += "  " + it.Description
		}
		line = ansi.Truncate(line, width-2, "…")
		if row == s.cursor {
			b.WriteString(theme.Selected.Render(theme.SymbolSelected + " " + line))
		} else {
			b.WriteString(theme.Text.Render("  " + line))
		}
		b.WriteString("\n")
	}
	if len(vis) == 0 {
		b.WriteString(theme.Subtle.Render("  (no match)"))
		b.WriteString("\n")
	}
	if s.filtering || s.filter != "" {
		cursor := ""
		if s.filtering {
			cursor = "▌"
		}
		b.WriteString(theme.WarningText.Render("Filter: ") + theme.Emphasis.Render(s.filter+cursor))
		if len(vis) != len(s.items) {
			b.WriteString(theme.Subtle.Render(fmt.Sprintf("  (%d/%d)", len(vis), len(s.items))))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
