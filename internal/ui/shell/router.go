package shell

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// Fallback dimensions used until the first WindowSizeMsg arrives (and in
// renderless tests).
const (
	defaultWidth  = 80
	defaultHeight = 24
	minWidth      = 40
	minHeight     = 10
)

type screenEntry struct {
	id     uint64
	screen Screen
	abort  func()
}

// rootModel renders the ProxSave frame and routes messages to the top screen
// of a modal stack.
type rootModel struct {
	cfg    Config
	stack  []screenEntry
	width  int
	height int
}

func newRootModel(cfg Config) rootModel {
	return rootModel{cfg: cfg, width: defaultWidth, height: defaultHeight}
}

func (m rootModel) Init() tea.Cmd { return nil }

func (m rootModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case pushScreenMsg:
		m.stack = append(m.stack, screenEntry(msg))
		if m.cfg.observeScreenPush != nil {
			m.cfg.observeScreenPush(msg.screen.Title())
		}
		return m, msg.screen.Init()
	case removeScreenMsg:
		// Removal is strictly by id: a resolve command runs asynchronously
		// and may land after the engine has already pushed the next screen,
		// so popping the top here would drop the wrong screen.
		for i, e := range m.stack {
			if e.id == msg.id {
				m.stack = append(m.stack[:i], m.stack[i+1:]...)
				break
			}
		}
		return m, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			if n := len(m.stack); n > 0 {
				// Abort the top screen: its Ask returns ErrAborted and
				// the engine drives teardown (it owns cleanup, e.g.
				// network rollback). The program itself keeps running.
				top := m.stack[n-1]
				m.stack = m.stack[:n-1]
				top.abort()
				return m, nil
			}
			// No screen to abort (engine busy between Asks): terminate
			// the program, approximating the SIGINT the terminal would
			// deliver outside raw mode. Pending Asks get ErrClosed.
			return m, tea.Interrupt
		}
	}
	if isUserInput(msg) {
		// User input goes to the top screen only (modal stack). Mouse
		// coordinates are translated into body space (the area handed to
		// Screen.View), so components can hit-test their own layout.
		msg = m.translateMouse(msg)
		if msg == nil {
			return m, nil
		}
		if n := len(m.stack); n > 0 {
			scr, cmd := m.stack[n-1].screen.Update(msg)
			m.stack[n-1].screen = scr
			return m, cmd
		}
		return m, nil
	}
	// Everything else (countdown ticks, task progress/done, spinner ticks)
	// goes to the top screen and to buried screens that opted in via
	// BackgroundMessageReceiver: a buried confirm must keep its countdown
	// chain and a buried task its completion, while third-party widgets
	// (text inputs, huh forms) stay insulated from each other's unexported
	// internal messages.
	var cmds []tea.Cmd
	for i := range m.stack {
		if i != len(m.stack)-1 {
			bg, ok := m.stack[i].screen.(BackgroundMessageReceiver)
			if !ok || !bg.ReceivesBackgroundMessages() {
				continue
			}
		}
		scr, cmd := m.stack[i].screen.Update(msg)
		m.stack[i].screen = scr
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) == 0 {
		return m, nil
	}
	return m, tea.Batch(cmds...)
}

func (m rootModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	// Parity with the old tview UI, which always enabled mouse support.
	v.MouseMode = tea.MouseModeCellMotion
	if m.cfg.UseColor {
		// Only repaint the terminal background when color is enabled:
		// with DISABLE_COLORS the OSC 11 repaint would contradict the
		// monochrome contract.
		v.BackgroundColor = theme.Background
	}
	if m.cfg.AppName != "" && m.cfg.UseColor {
		// OSC 2 (window title) is gated with the rest of the escape
		// extras: monochrome mode targets dumb/serial terminals.
		v.WindowTitle = m.cfg.AppName
	}
	return v
}

func (m rootModel) top() (Screen, bool) {
	if n := len(m.stack); n > 0 {
		return m.stack[n-1].screen, true
	}
	return nil, false
}

func (m rootModel) render() string {
	w := max(m.width, minWidth)
	h := max(m.height, minHeight)
	// lipgloss v2 sizes are border-box: Width/Height include border and
	// padding. Inner content area = total minus border (2) minus padding (2).
	innerW := w - 4

	top, hasTop := m.top()

	// Chrome is cropped like the body: an over-long screen title or config
	// path must never push the right border out or wrap the frame.
	header := crop(m.renderHeader(innerW, top, hasTop), innerW, 1)
	rule := theme.Subtle.Render(strings.Repeat("─", innerW))
	footer := crop(m.renderFooter(innerW, top, hasTop), innerW, 2)

	chromeH := lipgloss.Height(header) + lipgloss.Height(rule) + lipgloss.Height(footer)
	bodyH := max(h-2-chromeH, 1)

	body := ""
	if hasTop {
		body = top.View(innerW, bodyH)
	}
	// Hard-crop before placing: lipgloss.Place is a no-op when the content
	// exceeds the box, and an oversized body would push the footer (and in
	// altscreen the bottom rows, i.e. buttons) off the terminal.
	body = crop(body, innerW, bodyH)
	body = lipgloss.Place(innerW, bodyH, lipgloss.Left, lipgloss.Top, body)

	content := lipgloss.JoinVertical(lipgloss.Left, header, rule, body, footer)
	frame := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Orange).
		Padding(0, 1).
		Width(w).
		Height(h)
	return frame.Render(content)
}

func (m rootModel) renderHeader(innerW int, top Screen, hasTop bool) string {
	left := theme.Title.Render(m.cfg.AppName)
	crumbs := make([]string, 0, 2)
	if m.cfg.Subtitle != "" {
		crumbs = append(crumbs, m.cfg.Subtitle)
	}
	if hasTop {
		// Skip the screen crumb when it just repeats the flow subtitle
		// ("ProxSave  Dashboard→ Dashboard" reads as a bug).
		if t := top.Title(); t != "" && !strings.EqualFold(strings.TrimSpace(t), strings.TrimSpace(m.cfg.Subtitle)) {
			crumbs = append(crumbs, t)
		}
	}
	if len(crumbs) > 0 {
		left += theme.Text.Render("  " + strings.Join(crumbs, theme.SymbolArrow+" "))
	}
	right := ""
	if m.cfg.Version != "" {
		right = theme.Subtle.Render(m.cfg.Version)
	}
	gap := innerW - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m rootModel) renderFooter(innerW int, top Screen, hasTop bool) string {
	lines := make([]string, 0, 2)
	if hasTop {
		if help := top.Help(); help != "" {
			lines = append(lines, center(innerW, theme.Subtle.Render(help)))
		}
	}
	status := make([]string, 0, 2)
	if m.cfg.ConfigPath != "" {
		status = append(status, theme.WarningText.Render("Config: ")+theme.Text.Render(m.cfg.ConfigPath))
	}
	if m.cfg.BuildSig != "" {
		status = append(status, theme.WarningText.Render("Build: ")+theme.Text.Render(m.cfg.BuildSig))
	}
	if len(status) > 0 {
		lines = append(lines, center(innerW, strings.Join(status, theme.Subtle.Render("  "+theme.SymbolBullet+"  "))))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func center(width int, s string) string {
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, s)
}

// Body origin inside the frame: border (1) + horizontal padding (1)
// columns, border (1) + header (1) + rule (1) rows. Keep in sync with
// render().
const (
	bodyOriginX = 2
	bodyOriginY = 3
)

// translateMouse rebases mouse coordinates onto the body area. Clicks on
// the chrome are swallowed (nil); wheel events pass through regardless of
// position so scrolling works wherever the pointer is.
func (m rootModel) translateMouse(msg tea.Msg) tea.Msg {
	rebase := func(mouse tea.Mouse) (tea.Mouse, bool) {
		mouse.X -= bodyOriginX
		mouse.Y -= bodyOriginY
		return mouse, mouse.X >= 0 && mouse.Y >= 0
	}
	switch mm := msg.(type) {
	case tea.MouseClickMsg:
		if adj, ok := rebase(tea.Mouse(mm)); ok {
			return tea.MouseClickMsg(adj)
		}
		return nil
	case tea.MouseReleaseMsg:
		if adj, ok := rebase(tea.Mouse(mm)); ok {
			return tea.MouseReleaseMsg(adj)
		}
		return nil
	case tea.MouseMotionMsg:
		if adj, ok := rebase(tea.Mouse(mm)); ok {
			return tea.MouseMotionMsg(adj)
		}
		return nil
	case tea.MouseWheelMsg:
		adj, _ := rebase(tea.Mouse(mm))
		return tea.MouseWheelMsg(adj)
	}
	return msg
}

// isUserInput reports whether msg is direct user input (keys, mouse,
// bracketed paste), which must only ever reach the top (focused) screen.
func isUserInput(msg tea.Msg) bool {
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg, tea.PasteMsg, tea.PasteStartMsg, tea.PasteEndMsg:
		return true
	}
	return false
}

// crop bounds a block to width x height: extra lines are dropped, long lines
// are ANSI-aware truncated with an ellipsis.
func crop(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "…")
		}
	}
	return strings.Join(lines, "\n")
}
