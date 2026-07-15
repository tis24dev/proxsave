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

// SelectorItem is one row. A row with Separator set is a non-selectable visual
// divider (its Label is rendered dimmed, verbatim): navigation, enter, digit
// shortcuts and clicks all skip it, and it does not count toward the digit/filter
// thresholds. Use it to detach a second group of choices from the first.
type SelectorItem[T any] struct {
	Label       string
	Description string
	Value       T
	Separator   bool
}

// Selector is a list picker that resolves to the chosen item's value.
type Selector[T any] struct {
	shell.Resolver[T]
	title        string
	items        []SelectorItem[T]
	cursor       int // index into the visible (filtered) rows
	offset       int // scroll offset into the visible rows
	filter       string
	filtering    bool
	backErr      error
	prompt       string
	promptStyled bool // prompt is pre-rendered (colors/box) - render verbatim, don't sanitize/wrap

	lastRowsTop int // body row of the first visible item (set by View)
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

// WithSelectorPromptStyled sets a PRE-RENDERED prompt (already carrying colors,
// borders, etc.) rendered verbatim under the title - NOT sanitized and NOT wrapped
// in the default text style. The caller owns the styling and MUST ensure any
// embedded dynamic data is already sanitized/validated.
func WithSelectorPromptStyled[T any](rendered string) SelectorOption[T] {
	return func(s *Selector[T]) {
		s.prompt = rendered
		s.promptStyled = true
	}
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
			Separator:   it.Separator,
		}
	}
	s := &Selector[T]{title: sanitizeLine(title), items: clean}
	for _, opt := range opts {
		opt(s)
	}
	// The initial cursor must never rest on a separator.
	s.clampCursor(s.visible())
	return s
}

// selectableCount is the number of non-separator rows; the digit and filter
// thresholds count only these so a divider never skews them.
func (s *Selector[T]) selectableCount() int {
	n := 0
	for i := range s.items {
		if !s.items[i].Separator {
			n++
		}
	}
	return n
}

// step moves the cursor by delta over vis, skipping separators. It stops at the
// current position when it would run off either end (no wrap).
func (s *Selector[T]) step(vis []int, delta int) {
	i := s.cursor
	for {
		i += delta
		if i < 0 || i >= len(vis) {
			return
		}
		if !s.items[vis[i]].Separator {
			s.cursor = i
			return
		}
	}
}

func (s *Selector[T]) Init() tea.Cmd { return nil }

func (s *Selector[T]) Title() string { return s.title }

func (s *Selector[T]) Help() string {
	parts := []string{"↑/↓ move", "enter select"}
	if s.selectableCount() > filterThreshold {
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

func (s *Selector[T]) clampCursor(vis []int) {
	if s.cursor >= len(vis) {
		s.cursor = len(vis) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	if len(vis) == 0 {
		return
	}
	// Never rest on a separator: snap forward, then backward, to a real row.
	if s.items[vis[s.cursor]].Separator {
		for i := s.cursor; i < len(vis); i++ {
			if !s.items[vis[i]].Separator {
				s.cursor = i
				return
			}
		}
		for i := s.cursor; i >= 0; i-- {
			if !s.items[vis[i]].Separator {
				s.cursor = i
				return
			}
		}
	}
}

func (s *Selector[T]) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch mouse := msg.(type) {
	case tea.MouseWheelMsg:
		vis := s.visible()
		s.clampCursor(vis)
		switch mouse.Button {
		case tea.MouseWheelUp:
			s.step(vis, -1)
		case tea.MouseWheelDown:
			s.step(vis, +1)
		}
		return s, nil
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return s, nil
		}
		vis := s.visible()
		row := mouse.Y - s.lastRowsTop + s.offset
		if row >= 0 && row < len(vis) && !s.items[vis[row]].Separator {
			s.cursor = row
			return s, s.Resolve(s.items[vis[row]].Value, nil)
		}
		return s, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return s, nil
	}
	vis := s.visible()
	s.clampCursor(vis)

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
		s.step(vis, -1)
		return s, nil
	case "down", "j":
		s.step(vis, +1)
		return s, nil
	case "home":
		s.cursor = 0
		s.clampCursor(vis)
		return s, nil
	case "end":
		s.cursor = len(vis) - 1
		s.clampCursor(vis)
		return s, nil
	case "enter":
		if len(vis) > 0 && !s.items[vis[s.cursor]].Separator {
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
		if s.selectableCount() > filterThreshold {
			s.filtering = true
			s.filter = ""
			s.cursor = 0
			s.clampCursor(s.visible())
		}
		return s, nil
	}

	// Digit shortcuts select the n-th SELECTABLE visible row (separators are not
	// counted), only meaningful for short, unfiltered lists.
	if !s.filtering && s.filter == "" && len(key.Text) == 1 && key.Text >= "1" && key.Text <= "9" {
		n := int(key.Text[0] - '0')
		if s.selectableCount() <= 9 {
			ord := 0
			for _, vi := range vis {
				if s.items[vi].Separator {
					continue
				}
				ord++
				if ord == n {
					return s, s.Resolve(s.items[vi].Value, nil)
				}
			}
		}
	}
	return s, nil
}

func (s *Selector[T]) View(width, height int) string {
	vis := s.visible()
	s.clampCursor(vis)

	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(s.title))
	if s.prompt != "" {
		if s.promptStyled {
			b.WriteString("\n" + s.prompt)
		} else {
			b.WriteString("\n" + theme.Text.Render(s.prompt))
		}
	}
	b.WriteString("\n\n")

	overhead := lipgloss.Height(b.String())
	s.lastRowsTop = overhead - 1 // rows start after title/prompt/blank
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

	showDigits := s.selectableCount() <= 9 && s.filter == ""
	// Digit label per visible row = its selectable ordinal (separators get none);
	// computed from the start of vis so scrolling never renumbers a row.
	ord := make([]int, len(vis))
	c := 0
	for i, vi := range vis {
		if s.items[vi].Separator {
			continue
		}
		c++
		ord[i] = c
	}
	// Widest selectable label, so descriptions align in one column instead of
	// trailing each command name at a different offset.
	maxLabel := 0
	for i := range s.items {
		if s.items[i].Separator {
			continue
		}
		if w := ansi.StringWidth(s.items[i].Label); w > maxLabel {
			maxLabel = w
		}
	}
	for row := s.offset; row < len(vis) && row < s.offset+rows; row++ {
		it := s.items[vis[row]]
		if it.Separator {
			// Non-selectable dim divider: label verbatim, no cursor/digit.
			b.WriteString(theme.Subtle.Render("  " + ansi.Truncate(it.Label, width-2, "…")))
			b.WriteString("\n")
			continue
		}
		prefix := "  "
		if showDigits && ord[row] <= 9 {
			prefix = fmt.Sprintf("%d ", ord[row])
		}
		line := prefix + it.Label
		if it.Description != "" {
			// Pad the label to the widest one so every description starts at the
			// same column (aligned), instead of trailing each name.
			line += strings.Repeat(" ", maxLabel-ansi.StringWidth(it.Label)) + "  " + it.Description
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
