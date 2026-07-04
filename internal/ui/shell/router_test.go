package shell

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func pushEntry(m rootModel, scr Screen, id uint64, abort func()) rootModel {
	updated, _ := m.Update(pushScreenMsg{id: id, screen: scr, abort: abort})
	return updated.(rootModel)
}

func TestRouterPushResolvePop(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	scr := newStubScreen(1)
	scr.setID(1)
	m = pushEntry(m, scr, 1, func() {})
	if len(m.stack) != 1 {
		t.Fatalf("expected 1 screen on stack, got %d", len(m.stack))
	}
	// Resolve emits a removal for the screen's own id.
	cmd := scr.Resolve(0, nil)
	updated, _ := m.Update(cmd())
	m = updated.(rootModel)
	if len(m.stack) != 0 {
		t.Fatalf("expected empty stack after resolve, got %d", len(m.stack))
	}
}

// TestRouterResolvePopsByIDNotTop locks the race fix: a resolve command that
// lands after the engine already pushed the next screen must remove the
// resolved screen, not whatever is on top.
func TestRouterResolvePopsByIDNotTop(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	s1 := newStubScreen(1)
	s1.setID(1)
	m = pushEntry(m, s1, 1, func() {})
	popCmd := s1.Resolve(0, nil) // resolve fires, pop message not yet delivered

	s2 := newStubScreen(2)
	s2.setID(2)
	m = pushEntry(m, s2, 2, func() {}) // next screen pushed before the pop lands

	updated, _ := m.Update(popCmd())
	m = updated.(rootModel)
	if len(m.stack) != 1 || m.stack[0].id != 2 {
		t.Fatalf("late pop must remove screen 1, stack=%+v", m.stack)
	}
}

func TestRouterRemoveByID(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	m = pushEntry(m, newStubScreen(1), 1, func() {})
	m = pushEntry(m, newStubScreen(2), 2, func() {})

	updated, _ := m.Update(removeScreenMsg{id: 1})
	m = updated.(rootModel)
	if len(m.stack) != 1 || m.stack[0].id != 2 {
		t.Fatalf("expected only screen 2 to remain, got %+v", m.stack)
	}
	// Removing an unknown id is a no-op.
	updated, _ = m.Update(removeScreenMsg{id: 99})
	m = updated.(rootModel)
	if len(m.stack) != 1 {
		t.Fatalf("unknown id removal must be a no-op, got %d screens", len(m.stack))
	}
}

func TestRouterCtrlCAbortsTopScreenOnly(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	abortedBottom := false
	abortedTop := false
	m = pushEntry(m, newStubScreen(1), 1, func() { abortedBottom = true })
	m = pushEntry(m, newStubScreen(2), 2, func() { abortedTop = true })

	updated, _ := m.Update(KeyMsg("ctrl+c"))
	m = updated.(rootModel)
	if !abortedTop || abortedBottom {
		t.Fatalf("expected only top abort: top=%v bottom=%v", abortedTop, abortedBottom)
	}
	if len(m.stack) != 1 {
		t.Fatalf("expected top screen popped, stack=%d", len(m.stack))
	}
}

func TestRouterForwardsKeysToTopScreenOnly(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	bottom := newStubScreen(1)
	top := newStubScreen(2)
	var got int
	top.Bind(func(v int, err error) { got = v })
	bottom.Bind(func(v int, err error) { got = -1 })

	m = pushEntry(m, bottom, 1, func() {})
	m = pushEntry(m, top, 2, func() {})
	m.Update(KeyMsg("enter")) //nolint:errcheck // model discarded; only resolve side effect matters

	if got != 2 {
		t.Fatalf("expected top screen (2) to resolve, got %d", got)
	}
}

func TestRouterRenderContainsChrome(t *testing.T) {
	m := newRootModel(Config{
		AppName:    "ProxSave",
		Subtitle:   "Restore",
		Version:    "v9.9.9",
		ConfigPath: "/etc/proxsave/backup.env",
		BuildSig:   "abcdef",
	})
	m = pushEntry(m, newStubScreen(1), 1, func() {})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(rootModel)

	out := m.render()
	for _, want := range []string{"ProxSave", "Restore", "v9.9.9", "stub view", "stub help", "/etc/proxsave/backup.env", "abcdef"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
	lines := strings.Split(out, "\n")
	if len(lines) != 30 {
		t.Errorf("expected render height 30, got %d", len(lines))
	}
}

// TestRouterHeaderSkipsDuplicateCrumb: a screen title equal to the flow
// subtitle (case-insensitive) must not be repeated in the header.
func TestRouterHeaderSkipsDuplicateCrumb(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	scr := newStubScreen(1) // Title() == "stub"
	m = pushEntry(m, scr, 1, func() {})

	header := m.renderHeader(96, scr, true)
	if !strings.Contains(header, "stub") {
		t.Fatalf("distinct screen title must appear: %q", header)
	}

	dup := &titledStub{stubScreen: *newStubScreen(2), title: "dashboard"}
	m2 := newRootModel(Config{AppName: "ProxSave", Subtitle: "Dashboard"})
	m2 = pushEntry(m2, dup, 2, func() {})
	header2 := m2.renderHeader(96, dup, true)
	if strings.Count(strings.ToLower(header2), "dashboard") != 1 {
		t.Fatalf("duplicate crumb must be skipped: %q", header2)
	}
}

type titledStub struct {
	stubScreen
	title string
}

func (s *titledStub) Title() string { return s.title }

func TestRouterRenderEmptyStack(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	out := m.render()
	if !strings.Contains(out, "ProxSave") {
		t.Error("idle render must still show the app name")
	}
}

// oversizeScreen renders far more content than the body can hold, in both
// dimensions.
type oversizeScreen struct {
	Resolver[int]
}

func (o *oversizeScreen) Init() tea.Cmd { return nil }
func (o *oversizeScreen) Title() string { return "big" }
func (o *oversizeScreen) Help() string  { return "help" }
func (o *oversizeScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	return o, nil
}
func (o *oversizeScreen) View(w, h int) string {
	line := strings.Repeat("x", 500)
	return strings.Repeat(line+"\n", 200)
}

// TestRouterRenderCropsOversizedBody locks the frame invariant: a screen
// that ignores its width/height budget must not break the frame (lipgloss
// Place/Height do NOT clip oversized content; the router crops explicitly).
func TestRouterRenderCropsOversizedBody(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave", Version: "v1"})
	m = pushEntry(m, &oversizeScreen{}, 1, func() {})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(rootModel)

	out := m.render()
	lines := strings.Split(out, "\n")
	if len(lines) != 24 {
		t.Fatalf("oversized body must not grow the frame: got %d lines, want 24", len(lines))
	}
	for i, line := range lines {
		if w := lipglossWidth(line); w != 80 {
			t.Fatalf("line %d width = %d, want exactly 80", i, w)
		}
	}
	// The footer help must survive the crop.
	if !strings.Contains(out, "help") {
		t.Error("footer must remain visible under body overflow")
	}
}

// TestRouterBackgroundDelivery: non-input messages must reach buried screens
// that opted in via BackgroundMessageReceiver (a task can complete while a
// dialog covers it), and must NOT reach buried screens that did not opt in
// (third-party widget insulation).
func TestRouterBackgroundDelivery(t *testing.T) {
	m := newRootModel(Config{AppName: "ProxSave"})
	optIn := newStubScreen(1)
	optIn.background = true
	optOut := newStubScreen(2) // background=false
	top := newStubScreen(3)
	m = pushEntry(m, optIn, 1, func() {})
	m = pushEntry(m, optOut, 2, func() {})
	m = pushEntry(m, top, 3, func() {})

	m.Update("custom-broadcast") //nolint:errcheck
	if top.lastMsg != "custom-broadcast" {
		t.Fatal("top screen must always receive non-input messages")
	}
	if optIn.lastMsg != "custom-broadcast" {
		t.Fatal("opted-in buried screen must receive non-input messages")
	}
	if optOut.lastMsg == "custom-broadcast" {
		t.Fatal("non-opted buried screen must be insulated")
	}

	// Keys still go to the top only, opt-in or not.
	m.Update(KeyMsg("x")) //nolint:errcheck
	if optIn.lastKey == "x" || optOut.lastKey == "x" {
		t.Fatal("keys must not reach non-top screens")
	}
	if top.lastKey != "x" {
		t.Fatal("keys must reach the top screen")
	}
}

func lipglossWidth(s string) int { return lipgloss.Width(s) }

func TestKeyMsgHelper(t *testing.T) {
	cases := map[string]string{
		"enter":     "enter",
		"esc":       "esc",
		"tab":       "tab",
		"shift+tab": "shift+tab",
		"ctrl+c":    "ctrl+c",
		"a":         "a",
		"/":         "/",
	}
	for input, want := range cases {
		msg := KeyMsg(input)
		key, ok := msg.(tea.KeyPressMsg)
		if !ok {
			t.Fatalf("KeyMsg(%q) is not a KeyPressMsg", input)
		}
		if got := key.String(); got != want {
			t.Errorf("KeyMsg(%q).String() = %q, want %q", input, got, want)
		}
	}
	if n := len(Keys("up down enter")); n != 3 {
		t.Errorf("Keys script length = %d, want 3", n)
	}
}
