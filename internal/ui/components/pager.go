package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Pager shows scrollable text (restore plans, reports) and resolves when the
// user continues.
type Pager struct {
	shell.Resolver[struct{}]
	title        string
	content      string
	confirmLabel string
	abortErr     error
	vp           viewport.Model
}

// PagerOption customizes a Pager.
type PagerOption func(*Pager)

// WithPagerConfirmLabel sets the continue label shown in the footer hint.
func WithPagerConfirmLabel(label string) PagerOption {
	return func(p *Pager) { p.confirmLabel = sanitizeLine(label) }
}

// WithPagerAbort overrides the error Esc resolves with (default
// shell.ErrAborted), e.g. a flow-specific decline sentinel.
func WithPagerAbort(err error) PagerOption {
	return func(p *Pager) { p.abortErr = err }
}

// NewPager builds a scrollable text screen. Esc aborts (shell.ErrAborted by
// default, or the WithPagerAbort sentinel): a reflex Esc on a restore plan
// must never count as acceptance.
func NewPager(title, content string, opts ...PagerOption) *Pager {
	p := &Pager{
		title:        sanitizeLine(title),
		content:      sanitize(content),
		confirmLabel: "continue",
		abortErr:     shell.ErrAborted,
		vp:           viewport.New(),
	}
	p.vp.SetContent(p.content)
	for _, opt := range opts {
		opt(p)
	}
	return p
}

func (p *Pager) Init() tea.Cmd { return nil }

func (p *Pager) Title() string { return p.title }

func (p *Pager) Help() string {
	help := "↑/↓ scroll · enter " + p.confirmLabel
	if p.abortErr != nil {
		help += " · esc cancel"
	}
	return help
}

func (p *Pager) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "enter":
			return p, p.Resolve(struct{}{}, nil)
		case "esc", "q":
			return p, p.Resolve(struct{}{}, p.abortErr)
		}
	}
	var cmd tea.Cmd
	p.vp, cmd = p.vp.Update(msg)
	return p, cmd
}

func (p *Pager) View(width, height int) string {
	bodyH := max(height-2, 1)
	p.vp.SetWidth(width)
	p.vp.SetHeight(bodyH)

	var b strings.Builder
	b.WriteString(theme.Emphasis.Render(p.title))
	scroll := ""
	if p.vp.TotalLineCount() > bodyH {
		scroll = theme.Subtle.Render(fmt.Sprintf("  (%d%%)", int(p.vp.ScrollPercent()*100)))
	}
	b.WriteString(scroll)
	b.WriteString("\n")
	b.WriteString(p.vp.View())
	return b.String()
}
