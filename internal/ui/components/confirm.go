package components

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

// ConfirmResult carries the yes/no answer and whether a countdown expired.
// Timeout ALWAYS resolves to Answer=false regardless of the Enter default:
// parity with the tview prompt (restore_tui.go) whose auto-skip is safe by
// construction.
type ConfirmResult struct {
	Answer   bool
	TimedOut bool
}

// confirmTickMsg carries the owning confirm's token so a tick chain never
// re-arms a different confirm screen (messages are broadcast to the whole
// stack).
type confirmTickMsg struct {
	token uint64
	t     time.Time
}

var confirmToken atomic.Uint64

// Confirm is a yes/no prompt. The Enter default equals the initial button
// focus, so a bare Enter always picks the advertised default.
type Confirm struct {
	shell.Resolver[ConfirmResult]
	token           uint64
	title           string
	message         string
	yesLabel        string
	noLabel         string
	defaultYes      bool
	focusYes        bool
	danger          bool
	abortErr        error
	countdownPrefix string
	timeout         time.Duration
	deadline        time.Time
	now             time.Time

	// Button hit ranges from the last render (body coordinates).
	lastButtonsY             int
	yesX0, yesX1, noX0, noX1 int
}

// ConfirmOption customizes a Confirm.
type ConfirmOption func(*Confirm)

// WithLabels overrides the button labels (default "Yes"/"No").
func WithLabels(yes, no string) ConfirmOption {
	return func(c *Confirm) {
		if yes != "" {
			c.yesLabel = sanitizeLine(yes)
		}
		if no != "" {
			c.noLabel = sanitizeLine(no)
		}
	}
}

// WithDefaultYes sets the Enter default (and initial focus) to the yes
// button. The default is the no button: destructive prompts stay safe on a
// reflexive Enter.
func WithDefaultYes(defaultYes bool) ConfirmOption {
	return func(c *Confirm) {
		c.defaultYes = defaultYes
		c.focusYes = defaultYes
	}
}

// WithCountdown auto-resolves to No after timeout, rendering the same
// countdown line the tview prompt used.
func WithCountdown(timeout time.Duration) ConfirmOption {
	return func(c *Confirm) {
		c.timeout = timeout
		now := time.Now()
		c.now = now
		c.deadline = now.Add(timeout)
	}
}

// WithCountdownPrefix overrides the countdown verb (default "Auto-skip",
// e.g. "Rollback" for the network-commit prompt where inaction rolls back).
func WithCountdownPrefix(prefix string) ConfirmOption {
	return func(c *Confirm) { c.countdownPrefix = sanitizeLine(prefix) }
}

// WithConfirmAbort makes Esc resolve with err (e.g. a wizard-cancel
// sentinel) instead of answering No. Default (esc = No) matches the tview
// yes/no prompts; wizard-style steps opt in.
func WithConfirmAbort(err error) ConfirmOption {
	return func(c *Confirm) { c.abortErr = err }
}

// WithDanger marks a destructive confirmation: the message is rendered in
// the warning style and the single-key y/n shortcuts are DISABLED, so the
// dangerous choice always requires explicit navigation plus Enter (parity
// with tview, where buttons only ever activated on Enter).
func WithDanger() ConfirmOption {
	return func(c *Confirm) { c.danger = true }
}

// NewConfirm builds a yes/no screen.
func NewConfirm(title, message string, opts ...ConfirmOption) *Confirm {
	c := &Confirm{
		token:           confirmToken.Add(1),
		title:           sanitizeLine(title),
		message:         sanitize(message),
		yesLabel:        "Yes",
		noLabel:         "No",
		countdownPrefix: "Auto-skip",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Confirm) Init() tea.Cmd {
	if c.timeout > 0 {
		return c.tick()
	}
	return nil
}

func (c *Confirm) tick() tea.Cmd {
	token := c.token
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return confirmTickMsg{token: token, t: t}
	})
}

func (c *Confirm) Title() string { return c.title }

func (c *Confirm) Help() string {
	if c.danger {
		return "←/→ switch · enter confirm · esc cancel"
	}
	return "←/→ switch · enter confirm · y/n direct · esc cancel"
}

func (c *Confirm) Update(msg tea.Msg) (shell.Screen, tea.Cmd) {
	switch msg := msg.(type) {
	case confirmTickMsg:
		if msg.token != c.token || c.timeout <= 0 {
			return c, nil
		}
		c.now = msg.t
		if !c.now.Before(c.deadline) {
			return c, c.Resolve(ConfirmResult{Answer: false, TimedOut: true}, nil)
		}
		return c, c.tick()
	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft || msg.Y != c.lastButtonsY {
			return c, nil
		}
		switch {
		case msg.X >= c.yesX0 && msg.X < c.yesX1:
			return c, c.Resolve(ConfirmResult{Answer: true}, nil)
		case msg.X >= c.noX0 && msg.X < c.noX1:
			return c, c.Resolve(ConfirmResult{Answer: false}, nil)
		}
		return c, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "left", "right", "tab", "shift+tab", "up", "down":
			c.focusYes = !c.focusYes
			return c, nil
		case "y", "Y":
			if !c.danger {
				return c, c.Resolve(ConfirmResult{Answer: true}, nil)
			}
			return c, nil
		case "n", "N":
			if !c.danger {
				return c, c.Resolve(ConfirmResult{Answer: false}, nil)
			}
			return c, nil
		case "enter":
			return c, c.Resolve(ConfirmResult{Answer: c.focusYes}, nil)
		case "esc":
			if c.abortErr != nil {
				return c, c.Resolve(ConfirmResult{}, c.abortErr)
			}
			return c, c.Resolve(ConfirmResult{Answer: false}, nil)
		}
	}
	return c, nil
}

// ReceivesBackgroundMessages keeps the countdown chain alive while buried
// under another screen.
func (c *Confirm) ReceivesBackgroundMessages() bool { return c.timeout > 0 }

func (c *Confirm) View(width, height int) string {
	msgStyle := theme.Text
	if c.danger {
		msgStyle = theme.WarningText
	}

	// Strict height budget with priority buttons > countdown > title >
	// message: the actionable rows must never be pushed past the bottom
	// (the router crops overflow from below). tview reserved fixed form
	// rows for the same reason.
	tail := []string{}
	if c.timeout > 0 {
		left := c.deadline.Sub(c.now)
		if left < 0 {
			left = 0
		}
		// The countdown advertises the TIMEOUT outcome, which always resolves to No
		// (c.noLabel), never the Enter default: a countdown-armed defaultYes prompt
		// would otherwise read "default: Apply" while an expiry picks Skip. The Enter
		// default stays advertised on the button ("(default)" marker).
		countdown := theme.WarningText.Render(fmt.Sprintf(
			"%s in %ds (at %s, on timeout: %s)",
			c.countdownPrefix, int(left.Seconds()), c.deadline.Format("15:04:05"), c.noLabel))
		tail = append(tail, countdown, "")
	}
	tail = append(tail, c.renderButtons(width))
	if len(tail) > height {
		// Extreme terminals: keep the buttons, drop the countdown line.
		tail = tail[len(tail)-1:]
	}

	headBudget := max(height-len(tail), 0)
	head := make([]string, 0, headBudget)
	if headBudget >= 1 {
		head = append(head, theme.Emphasis.Render(c.title))
	}
	if headBudget >= 2 {
		head = append(head, "")
	}
	msgBudget := headBudget - len(head) - 1 // keep one blank before the tail
	if msgBudget < 0 {
		msgBudget = headBudget - len(head)
	}
	if msgBudget > 0 && c.message != "" {
		msgLines := strings.Split(msgStyle.Width(width).Render(c.message), "\n")
		if len(msgLines) > msgBudget {
			indicator := theme.Subtle.Render(fmt.Sprintf(
				"(… %d more lines, enlarge the terminal)", len(msgLines)-(msgBudget-1)))
			if msgBudget == 1 {
				msgLines = []string{indicator}
			} else {
				msgLines = append(msgLines[:msgBudget-1], indicator)
			}
		}
		head = append(head, msgLines...)
	}
	if len(head)+len(tail) < height {
		head = append(head, "")
	}
	c.lastButtonsY = len(head) + len(tail) - 1
	return strings.Join(append(head, tail...), "\n")
}

func (c *Confirm) renderButtons(width int) string {
	yes := c.yesLabel
	no := c.noLabel
	if c.defaultYes {
		yes += " (default)"
	} else {
		no += " (default)"
	}
	var yesBtn, noBtn string
	if c.focusYes {
		yesBtn = theme.ButtonFocused.Render(yes)
		noBtn = theme.ButtonBlurred.Render(no)
	} else {
		yesBtn = theme.ButtonBlurred.Render(yes)
		noBtn = theme.ButtonFocused.Render(no)
	}
	row := lipgloss.JoinHorizontal(lipgloss.Center, yesBtn, "  ", noBtn)
	// Record the click ranges (body coordinates after centering).
	wYes, wNo := lipgloss.Width(yesBtn), lipgloss.Width(noBtn)
	leftPad := max((width-(wYes+2+wNo))/2, 0)
	c.yesX0, c.yesX1 = leftPad, leftPad+wYes
	c.noX0, c.noX1 = leftPad+wYes+2, leftPad+wYes+2+wNo
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, row)
}
