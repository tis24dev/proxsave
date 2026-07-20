package components

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// NoticeKind selects the notice severity styling.
type NoticeKind int

const (
	NoticeInfo NoticeKind = iota
	NoticeSuccess
	NoticeWarning
	NoticeError
)

// Notice is a message screen with a single acknowledge action.
type Notice struct {
	shell.Resolver[struct{}]
	kind    NoticeKind
	title   string
	message string
}

// NewNotice builds a message screen. Error notices routinely embed
// untrusted data (filenames inside error strings), hence the sanitize.
func NewNotice(kind NoticeKind, title, message string) *Notice {
	return &Notice{kind: kind, title: sanitizeLine(title), message: sanitize(message)}
}

func (n *Notice) Init() tea.Cmd { return nil }

func (n *Notice) Title() string { return n.title }

func (n *Notice) Help() string { return "enter continue" }

func (n *Notice) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter", "esc", "space":
			return n, n.Resolve(struct{}{}, nil)
		}
	}
	return n, nil
}

func (n *Notice) accent() (color.Color, string) {
	switch n.kind {
	case NoticeSuccess:
		return theme.Green, theme.SymbolSuccess
	case NoticeWarning:
		return theme.Yellow, theme.SymbolWarning
	case NoticeError:
		return theme.Red, theme.SymbolError
	default:
		return theme.Blue, theme.SymbolInfo
	}
}

func (n *Notice) View(width, height int) string {
	accent, symbol := n.accent()
	header := lipgloss.NewStyle().Foreground(accent).Bold(true).Render(symbol + " " + n.title)

	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n\n")
	// One sentence per line: never break a line mid-sentence (a wrapped word from
	// the next sentence reads as a broken phrase). Each sentence still wraps on its
	// own if longer than the width.
	if n.message != "" {
		for i, sentence := range splitSentences(n.message) {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(theme.Text.Width(width - 4).Render(sentence))
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1).
		Width(min(width, 76) - 2)
	return box.Render(b.String())
}
