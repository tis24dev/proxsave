package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

type confirmCapture struct {
	resolved bool
	result   ConfirmResult
	err      error
}

func bindConfirm(c *Confirm) *confirmCapture {
	cap := &confirmCapture{}
	c.Bind(func(v ConfirmResult, err error) {
		cap.resolved = true
		cap.result = v
		cap.err = err
	})
	return cap
}

func press(t *testing.T, scr shell.Screen, key string) {
	t.Helper()
	scr.Update(shell.KeyMsg(key)) //nolint:errcheck // side effects only
}

// TestConfirmDefaultParity is the core defaultYes contract: a bare Enter
// always picks the advertised default, for both default polarities.
func TestConfirmDefaultParity(t *testing.T) {
	cases := []struct {
		name       string
		defaultYes bool
		keys       []string
		want       bool
	}{
		{"default-no bare enter is No", false, []string{"enter"}, false},
		{"default-yes bare enter is Yes", true, []string{"enter"}, true},
		{"default-no y is Yes", false, []string{"y"}, true},
		{"default-yes n is No", true, []string{"n"}, false},
		{"default-no toggle then enter is Yes", false, []string{"left", "enter"}, true},
		{"default-yes toggle then enter is No", true, []string{"tab", "enter"}, false},
		{"default-no up then enter is Yes", false, []string{"up", "enter"}, true},
		{"default-yes down then enter is No", true, []string{"down", "enter"}, false},
		{"default-yes esc is No", true, []string{"esc"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewConfirm("Confirm", "Proceed?", WithDefaultYes(tc.defaultYes))
			cap := bindConfirm(c)
			for _, k := range tc.keys {
				press(t, c, k)
			}
			if !cap.resolved {
				t.Fatal("confirm did not resolve")
			}
			if cap.err != nil {
				t.Fatalf("unexpected error: %v", cap.err)
			}
			if cap.result.Answer != tc.want {
				t.Fatalf("answer = %v, want %v", cap.result.Answer, tc.want)
			}
			if cap.result.TimedOut {
				t.Fatal("user answer must not be marked TimedOut")
			}
		})
	}
}

// TestConfirmTimeoutAlwaysNo locks the safety-critical rule: the countdown
// expiry resolves to No even when the Enter default is Yes (parity with
// restore_tui.go, where auto-skip on timeout proceeds with No).
func TestConfirmTimeoutAlwaysNo(t *testing.T) {
	for _, defaultYes := range []bool{true, false} {
		c := NewConfirm("Confirm", "Proceed?", WithDefaultYes(defaultYes), WithCountdown(30*time.Second))
		cap := bindConfirm(c)
		if c.Init() == nil {
			t.Fatal("countdown confirm must start a tick command")
		}
		// Tick before the deadline: no resolution.
		c.Update(confirmTickMsg{token: c.token, t: c.deadline.Add(-5 * time.Second)}) //nolint:errcheck
		if cap.resolved {
			t.Fatal("resolved before deadline")
		}
		// Tick at the deadline: resolves to No regardless of default.
		c.Update(confirmTickMsg{token: c.token, t: c.deadline}) //nolint:errcheck
		if !cap.resolved {
			t.Fatal("did not resolve at deadline")
		}
		if cap.result.Answer {
			t.Fatalf("timeout with defaultYes=%v must resolve to No", defaultYes)
		}
		if !cap.result.TimedOut {
			t.Fatal("timeout resolution must be marked TimedOut")
		}
	}
}

// TestConfirmIgnoresForeignTicks: ticks are broadcast to the whole stack, so
// a confirm must only honor (and re-arm on) its own token.
func TestConfirmIgnoresForeignTicks(t *testing.T) {
	c := NewConfirm("Confirm", "Proceed?", WithCountdown(30*time.Second))
	cap := bindConfirm(c)
	_, cmd := c.Update(confirmTickMsg{token: c.token + 999, t: c.deadline.Add(time.Hour)})
	if cap.resolved {
		t.Fatal("foreign tick must not resolve the confirm")
	}
	if cmd != nil {
		t.Fatal("foreign tick must not re-arm the tick chain")
	}
}

// TestConfirmDangerDisablesShortcuts: destructive prompts must not resolve
// on a single y/n keystroke (parity with tview, where buttons activate only
// on Enter; the CLI even requires typing the word COMMIT).
func TestConfirmDangerDisablesShortcuts(t *testing.T) {
	c := NewConfirm("Network commit", "Commit?", WithDanger(),
		WithLabels("COMMIT", "Let rollback run"))
	cap := bindConfirm(c)
	press(t, c, "y")
	press(t, c, "n")
	if cap.resolved {
		t.Fatal("danger confirm must ignore y/n shortcuts")
	}
	press(t, c, "enter")
	if !cap.resolved || cap.result.Answer {
		t.Fatalf("enter on danger confirm must pick the focused default (No), got %+v", cap)
	}
}

// TestConfirmMessageOverflowKeepsButtons: an oversized message is truncated
// so the countdown line and the buttons remain visible.
func TestConfirmMessageOverflowKeepsButtons(t *testing.T) {
	long := strings.Repeat("warning line\n", 40)
	c := NewConfirm("Confirm", long, WithCountdown(30*time.Second), WithLabels("COMMIT", "Let rollback run"))
	view := c.View(80, 20)
	lines := strings.Split(view, "\n")
	if len(lines) > 20 {
		t.Fatalf("view is %d lines, exceeds height 20", len(lines))
	}
	if !strings.Contains(view, "more lines") {
		t.Error("truncated message must show an overflow indicator")
	}
	if !strings.Contains(view, "Auto-skip in") {
		t.Error("countdown must stay visible on overflow")
	}
	if !strings.Contains(view, "COMMIT") || !strings.Contains(view, "Let rollback run") {
		t.Error("buttons must stay visible on overflow")
	}
}

// TestConfirmTinyHeightsKeepButtons locks the strict height budget at the
// boundary: whatever the height, the last rendered line is the button row
// (the router crops overflow from the bottom, so the actionable row must
// never sink below the budget).
func TestConfirmTinyHeightsKeepButtons(t *testing.T) {
	long := strings.Repeat("warning line\n", 10)
	for _, withCountdown := range []bool{true, false} {
		for h := 1; h <= 12; h++ {
			opts := []ConfirmOption{WithLabels("COMMIT", "Let rollback run")}
			if withCountdown {
				opts = append(opts, WithCountdown(30*time.Second))
			}
			c := NewConfirm("Confirm", long, opts...)
			view := c.View(80, h)
			lines := strings.Split(view, "\n")
			if len(lines) > h {
				t.Fatalf("h=%d countdown=%v: view has %d lines, exceeds budget", h, withCountdown, len(lines))
			}
			last := lines[len(lines)-1]
			if !strings.Contains(last, "COMMIT") || !strings.Contains(last, "Let rollback run") {
				t.Fatalf("h=%d countdown=%v: last line is not the buttons: %q", h, withCountdown, last)
			}
			if withCountdown && h >= 3 && !strings.Contains(view, "Auto-skip in") {
				t.Fatalf("h=%d: countdown line must be visible when it fits", h)
			}
		}
	}
}

// TestConfirmSanitizesInputs locks the constructor-level sanitize wiring:
// removing sanitize() from NewConfirm must fail here (the sanitize function
// itself is unit-tested separately).
func TestConfirmSanitizesInputs(t *testing.T) {
	c := NewConfirm("Ti\x1b[31mtle", "msg \x1b[31mevil\nline2", WithLabels("Y\x07es", "N\x07o"))
	view := c.View(80, 20)
	if strings.Contains(view, "\x1b[31m") || strings.Contains(view, "\x07") {
		t.Fatalf("unsanitized control data in view: %q", view)
	}
	if !strings.Contains(view, "evil") || !strings.Contains(view, "line2") {
		t.Errorf("sanitized content missing: %q", view)
	}
	if c.Title() != "Title" {
		t.Errorf("title not sanitized: %q", c.Title())
	}
}

func TestConfirmAbortOption(t *testing.T) {
	sentinel := fmt.Errorf("wizard cancelled")
	c := NewConfirm("Step", "Enable?", WithConfirmAbort(sentinel))
	cap := bindConfirm(c)
	press(t, c, "esc")
	if !cap.resolved || !errors.Is(cap.err, sentinel) {
		t.Fatalf("esc with abort option must resolve the sentinel, got %+v", cap)
	}
	// Without the option, esc stays a plain No.
	c2 := NewConfirm("Step", "Enable?")
	cap2 := bindConfirm(c2)
	press(t, c2, "esc")
	if cap2.err != nil || cap2.result.Answer {
		t.Fatalf("default esc must answer No with nil error, got %+v", cap2)
	}
}

func TestConfirmCountdownPrefix(t *testing.T) {
	c := NewConfirm("Network commit", "Commit?", WithCountdown(30*time.Second),
		WithCountdownPrefix("Rollback"))
	view := c.View(80, 20)
	if !strings.Contains(view, "Rollback in 30s") {
		t.Errorf("custom countdown prefix missing: %q", view)
	}
}

func TestConfirmCountdownLine(t *testing.T) {
	c := NewConfirm("Confirm", "Apply?", WithDefaultYes(true), WithCountdown(30*time.Second),
		WithLabels("COMMIT", "Let rollback run"))
	view := c.View(80, 20)
	if !strings.Contains(view, "Auto-skip in 30s") {
		t.Errorf("countdown line missing/wrong: %q", view)
	}
	// The countdown advertises the TIMEOUT outcome (always the No label), not the
	// Enter default: a countdown-armed prompt auto-resolves to No on expiry, so a
	// defaultYes prompt must not read "default: <yes label>" there.
	if !strings.Contains(view, "on timeout: Let rollback run") {
		t.Errorf("countdown must advertise the timeout outcome (No label): %q", view)
	}
	if strings.Contains(view, "default: COMMIT") {
		t.Errorf("countdown must not advertise the Enter default as the timeout outcome: %q", view)
	}
	// The Enter default stays advertised on the button, not the countdown.
	if !strings.Contains(view, "COMMIT (default)") {
		t.Errorf("Enter default must be marked on the button: %q", view)
	}
	if !strings.Contains(view, c.deadline.Format("15:04:05")) {
		t.Error("countdown must show the absolute deadline")
	}
}

func TestConfirmDefaultMarkerAndLabels(t *testing.T) {
	c := NewConfirm("Confirm", "Proceed?", WithLabels("Apply", "Skip"))
	view := c.View(80, 20)
	if !strings.Contains(view, "Apply") || !strings.Contains(view, "Skip (default)") {
		t.Errorf("expected custom labels with default marker on No, got %q", view)
	}
	// Initial focus must equal the default (bare Enter picks it).
	if c.focusYes {
		t.Error("default-no confirm must start focused on the no button")
	}
	cYes := NewConfirm("Confirm", "Proceed?", WithDefaultYes(true))
	if !cYes.focusYes {
		t.Error("default-yes confirm must start focused on the yes button")
	}
}
