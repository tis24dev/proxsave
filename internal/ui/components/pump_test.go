package components

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// pump feeds msg to the screen and keeps executing the returned commands,
// feeding their messages back, until no command remains or the depth cap is
// hit. This mirrors what the Bubble Tea runtime does for command-driven
// models (huh forms advance state via commands, not directly in Update).
func pump(t *testing.T, scr shell.Screen, msg tea.Msg) {
	t.Helper()
	queue := []tea.Msg{msg}
	for depth := 0; len(queue) > 0 && depth < 100; depth++ {
		next := queue[0]
		queue = queue[1:]
		_, cmd := scr.Update(next)
		queue = append(queue, collectMsgs(cmd)...)
	}
}

// collectMsgs executes a command tree and returns the produced messages.
// Commands that block longer than the watchdog (cursor-blink ticks) are
// dropped: tests never need them.
func collectMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(200 * time.Millisecond):
		return nil
	}
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, collectMsgs(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}
