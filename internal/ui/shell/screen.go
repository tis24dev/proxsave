// Package shell owns the long-lived Bubble Tea program behind every
// interactive ProxSave mode and the bridge that lets engine goroutines drive
// it through blocking Ask calls. Models are touched exclusively by the tea
// event loop; engine goroutines only send messages and wait on channels.
package shell

import (
	tea "charm.land/bubbletea/v2"
)

// Screen is a single interactive page managed by the Session's root model.
// Screens are stacked; only the top screen receives messages.
type Screen interface {
	// Init returns the screen's initial command (spinner ticks, countdowns).
	Init() tea.Cmd
	// Update handles a message and returns the (possibly replaced) screen.
	Update(msg tea.Msg) (Screen, tea.Cmd)
	// View renders the screen body. width and height are the inner
	// dimensions available inside the frame.
	View(width, height int) string
	// Title is shown in the frame header next to the flow subtitle.
	Title() string
	// Help is the one-line key legend rendered in the frame footer.
	Help() string
}

// AskScreen is a Screen that resolves to a value of type T via Ask.
type AskScreen[T any] interface {
	Screen
	// Bind installs the resolve callback. Called exactly once, by Ask,
	// before the screen is pushed.
	Bind(respond func(T, error))
}

// Resolver provides the resolve half of an AskScreen. Embed it in a screen
// and return Resolve's command from Update when the user answers.
type Resolver[T any] struct {
	respond func(T, error)
	id      uint64
}

// Bind implements AskScreen.
func (r *Resolver[T]) Bind(respond func(T, error)) { r.respond = respond }

// setID records the stack entry id assigned by Ask so Resolve can pop this
// exact screen. Pop-by-id (not pop-top) matters: the resolve command runs
// asynchronously and the engine may have pushed the next screen before the
// pop lands.
func (r *Resolver[T]) setID(id uint64) { r.id = id }

// Resolve delivers the screen's result to the waiting Ask call and pops the
// screen from the stack. The once-guard installed by Ask makes extra calls
// harmless.
func (r *Resolver[T]) Resolve(v T, err error) tea.Cmd {
	if r.respond != nil {
		r.respond(v, err)
	}
	id := r.id
	return func() tea.Msg { return removeScreenMsg{id: id} }
}

// screenIdentity lets Ask hand the stack entry id to the embedded Resolver.
type screenIdentity interface {
	setID(id uint64)
}

// BackgroundMessageReceiver is implemented by screens that need non-input
// messages even while buried under other screens (task completion, countdown
// ticks). Screens that do not implement it only receive messages while on
// top, which keeps third-party widgets (text inputs, huh forms) insulated
// from each other's unexported internal messages.
type BackgroundMessageReceiver interface {
	ReceivesBackgroundMessages() bool
}

// Internal router messages.
type pushScreenMsg struct {
	id     uint64
	screen Screen
}

type removeScreenMsg struct {
	id uint64
}
