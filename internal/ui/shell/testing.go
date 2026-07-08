package shell

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"

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

// SyncBuffer is a goroutine-safe writer that accumulates renderer output so
// tests can wait for screen text before sending keys.
type SyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *SyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *SyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *SyncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

// StartForTestWithOutput launches a Session that renders plain (monochrome)
// output into w, so end-to-end tests can observe what the user would see.
// Combine with SyncBuffer and Session.Send to script whole flows.
func StartForTestWithOutput(ctx context.Context, cfg Config, w io.Writer) *Session {
	return StartObservedForTest(ctx, cfg, w, nil)
}

// StartObservedForTest is StartForTestWithOutput plus a deterministic
// screen-push observer: onPush receives each pushed screen's title from the
// event loop, so a scripted test can wait for a screen before sending keys
// (renderer output alone is racy: the cell-diff renderer emits nothing for
// an identical re-render).
func StartObservedForTest(ctx context.Context, cfg Config, w io.Writer, onPush func(title string)) *Session {
	cfg.UseColor = false // deterministic, greppable output
	cfg.observeScreenPush = onPush
	return Start(ctx, cfg,
		tea.WithOutput(w),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(120, 36),
	)
}

// StartInlineForTestWithOutput launches an INLINE (non-altscreen) Session that
// renders plain output into w. Because the program is not on the altscreen,
// tea.Println lines land in w (in the altscreen they are a no-op), so
// streaming tests can assert the emitted log lines actually reached the
// terminal.
func StartInlineForTestWithOutput(ctx context.Context, cfg Config, w io.Writer) *Session {
	cfg.UseColor = false // deterministic, greppable output
	cfg.Inline = true
	return Start(ctx, cfg,
		tea.WithOutput(w),
		tea.WithInput(strings.NewReader("")),
		tea.WithoutSignalHandler(),
		tea.WithWindowSize(120, 36),
	)
}
