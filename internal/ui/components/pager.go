package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
	wrapWidth    int // width the content was last wrapped to (-1 = not yet)
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
		wrapWidth:    -1,
	}
	// Content is wrapped and set in View(), once the width is known: SoftWrap
	// stays OFF and we wrap ourselves (like StreamTask) so a long restore-plan
	// line WRAPS instead of clipping off-screen before a destructive confirm,
	// keeping each line's leading indent on its continuation rows.
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
	// (Re)wrap only when the width changes: the content is static, so this runs
	// once on the first render and again on a resize, never per keypress/frame.
	// SetContent AFTER SetWidth/SetHeight (viewport gotcha), SoftWrap stays off.
	if width != p.wrapWidth {
		p.vp.SetContentLines(wrapPlan(p.content, width))
		p.wrapWidth = width
	}

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

// wrapPlan wraps the (static, sanitized) pager content to width so no row ever
// exceeds it (the viewport keeps SoftWrap off and would otherwise clip). Each
// wrapped line's continuation rows reuse its leading indent (a hanging indent),
// so a long description or path stays visually under its parent instead of
// reflowing to column 0.
func wrapPlan(content string, width int) []string {
	if width < 1 {
		width = 1
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, wrapPlanLine(line, width)...)
	}
	return out
}

// wrapPlanLine word-wraps one line to width with a hanging indent. A single
// token wider than the available row is hard-split grapheme-safe via wrapLine.
func wrapPlanLine(line string, width int) []string {
	if ansi.StringWidth(line) <= width {
		return []string{line}
	}
	indent := leadingIndent(line)
	// On an absurdly narrow terminal drop the indent entirely, so a row (prefix +
	// at least a 1-col chunk) can never exceed width.
	if ansi.StringWidth(indent)+1 >= width {
		indent = ""
	}
	// Continuation rows hang under the parent; drop the hang if the indent would
	// leave no usable room on a very narrow terminal.
	cont := indent
	if ansi.StringWidth(cont)+4 > width {
		cont = ""
	}
	words := strings.Fields(line[len(indent):])
	if len(words) == 0 {
		return []string{line}
	}

	var rows []string
	prefix := indent
	var row strings.Builder
	rowW := 0
	emit := func() {
		rows = append(rows, prefix+row.String())
		row.Reset()
		rowW = 0
		prefix = cont
	}
	for _, w := range words {
		wWidth := ansi.StringWidth(w)
		avail := width - ansi.StringWidth(prefix)
		if rowW > 0 {
			if rowW+1+wWidth <= avail {
				row.WriteByte(' ')
				row.WriteString(w)
				rowW += 1 + wWidth
				continue
			}
			emit()
			avail = width - ansi.StringWidth(prefix)
		}
		if wWidth <= avail {
			row.WriteString(w)
			rowW = wWidth
			continue
		}
		// Token wider than a whole row: hard-split it grapheme-safe.
		chunks := wrapLine(w, avail)
		for i, ch := range chunks {
			if i < len(chunks)-1 {
				rows = append(rows, prefix+ch)
				prefix = cont
			} else {
				row.WriteString(ch)
				rowW = ansi.StringWidth(ch)
			}
		}
	}
	rows = append(rows, prefix+row.String())
	return rows
}

// leadingIndent returns the run of leading spaces/tabs of s.
func leadingIndent(s string) string {
	n := 0
	for n < len(s) && (s[n] == ' ' || s[n] == '\t') {
		n++
	}
	return s[:n]
}
