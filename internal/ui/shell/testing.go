package shell

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Key name to key code for the specials used across ProxSave screens. Kept
// in a non-test file so orchestrator and flow test suites can share it.
var specialKeys = map[string]rune{
	"enter":     tea.KeyEnter,
	"esc":       tea.KeyEscape,
	"tab":       tea.KeyTab,
	"space":     tea.KeySpace,
	"backspace": tea.KeyBackspace,
	"up":        tea.KeyUp,
	"down":      tea.KeyDown,
	"left":      tea.KeyLeft,
	"right":     tea.KeyRight,
	"pgup":      tea.KeyPgUp,
	"pgdown":    tea.KeyPgDown,
	"home":      tea.KeyHome,
	"end":       tea.KeyEnd,
}

// KeyMsg builds a key-press message from a readable name: "enter", "esc",
// "tab", "shift+tab", "ctrl+c", or a single printable character.
func KeyMsg(name string) tea.Msg {
	var mod tea.KeyMod
	for {
		switch {
		case strings.HasPrefix(name, "ctrl+"):
			mod |= tea.ModCtrl
			name = strings.TrimPrefix(name, "ctrl+")
			continue
		case strings.HasPrefix(name, "alt+"):
			mod |= tea.ModAlt
			name = strings.TrimPrefix(name, "alt+")
			continue
		case strings.HasPrefix(name, "shift+"):
			mod |= tea.ModShift
			name = strings.TrimPrefix(name, "shift+")
			continue
		}
		break
	}
	if code, ok := specialKeys[name]; ok {
		return tea.KeyPressMsg{Code: code, Mod: mod}
	}
	r := []rune(name)[0]
	key := tea.KeyPressMsg{Code: r, Mod: mod}
	if mod == 0 {
		key.Text = string(r)
	}
	return key
}

// Keys parses a space-separated script of key names into messages.
func Keys(script string) []tea.Msg {
	fields := strings.Fields(script)
	msgs := make([]tea.Msg, 0, len(fields))
	for _, f := range fields {
		msgs = append(msgs, KeyMsg(f))
	}
	return msgs
}

// StartForTest launches a renderless Session suitable for headless tests:
// no terminal, no signal handler, fixed window size. Drive it with
// Session.Send and the KeyMsg/Keys helpers.
func StartForTest(ctx context.Context, cfg Config) *Session {
	return Start(ctx, cfg,
		tea.WithoutRenderer(),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(100, 30),
	)
}
