package shell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

// ErrAborted was historically resolved into a pending Ask when the user pressed
// Ctrl+C on the current screen. Ctrl+C is now a GLOBAL interrupt (it terminates
// the program, surfacing as ErrClosed), so ErrAborted is retained only so flow
// adapters keep matching it defensively via IsAbort.
var ErrAborted = errors.New("ui: aborted by user")

// ErrClosed is returned by Ask when the underlying program has terminated and can
// no longer answer -- a UI death OR a Ctrl+C interrupt. Flow adapters treat it as
// "abort the current operation".
var ErrClosed = errors.New("ui: session closed")

// IsAbort reports whether err signals that the user tore down the UI: a Ctrl+C
// interrupt or a programmatic/terminal session close (both surface as ErrClosed),
// or the legacy ErrAborted. Flows map it to their canonical abort. NOTE: Esc is
// NOT an abort -- it resolves a per-screen Back sentinel (or a "No" answer) that
// each screen handles BEFORE falling through to this check.
func IsAbort(err error) bool {
	return errors.Is(err, ErrClosed) || errors.Is(err, ErrAborted)
}

// Config describes the immutable chrome of a Session.
type Config struct {
	// AppName is rendered in the frame header and terminal title.
	AppName string
	// Subtitle names the flow (e.g. "Restore", "Decrypt").
	Subtitle string
	// Version is rendered right-aligned in the header. Optional.
	Version string
	// ConfigPath and BuildSig are rendered in the footer status line, the
	// same data the tview ScreenSpec carried. Optional.
	ConfigPath string
	BuildSig   string
	// UseColor mirrors the USE_COLOR/DISABLE_COLORS config knobs. When
	// false the program renders monochrome (layout preserved).
	UseColor bool
	// Inline runs the program in the terminal's normal buffer (no
	// altscreen), so tea.Println lines land in the native scrollback with
	// colors and text selection preserved. Default false keeps every
	// existing session on the altscreen, byte-identical. Inline sessions
	// are created fresh (StartInline), never adopted.
	Inline bool

	// observeScreenPush, when set (test harness only), is called from the
	// event loop with the screen title every time a screen is pushed. It
	// gives scripted tests a deterministic "screen is now on the stack"
	// signal: keys sent after the notification are ordered after the push.
	observeScreenPush func(title string)
}

// Session owns one long-lived Bubble Tea program for an interactive mode.
// Engine goroutines interact with it only through Ask and Send.
type Session struct {
	prog   *tea.Program
	done   chan struct{}
	runErr error // written once, before done is closed
	nextID atomic.Uint64
}

// Start launches the program in a background goroutine and returns
// immediately. Cancelling ctx kills the program (terminal restored); Close
// quits it cleanly. Extra program options are appended after the defaults,
// so tests can override input/renderer.
func Start(ctx context.Context, cfg Config, opts ...tea.ProgramOption) *Session {
	// ConfigPath comes from the --config flag: scrub it (and the rest of
	// the chrome strings) like any other data rendered in the frame.
	cfg.AppName = cleanChrome(cfg.AppName)
	cfg.Subtitle = cleanChrome(cfg.Subtitle)
	cfg.Version = cleanChrome(cfg.Version)
	cfg.ConfigPath = cleanChrome(cfg.ConfigPath)
	cfg.BuildSig = cleanChrome(cfg.BuildSig)

	popts := []tea.ProgramOption{tea.WithContext(ctx)}
	if !cfg.UseColor {
		popts = append(popts, tea.WithColorProfile(colorprofile.Ascii))
	}
	popts = append(popts, opts...)
	s := &Session{done: make(chan struct{})}
	s.prog = tea.NewProgram(newRootModel(cfg), popts...)
	go func() {
		_, err := s.prog.Run()
		s.runErr = err
		close(s.done)
	}()
	return s
}

// StartInline launches a non-altscreen Session (cfg.Inline forced true), so
// long streamed operations can emit lines into the native scrollback via
// tea.Println with colors and text selection preserved. Interactive screens
// (wizard, menus) belong on the altscreen; use Start for those.
func StartInline(ctx context.Context, cfg Config, opts ...tea.ProgramOption) *Session {
	cfg.Inline = true
	return Start(ctx, cfg, opts...)
}

// Send injects a message into the program. Safe from any goroutine and a
// no-op once the program has terminated.
func (s *Session) Send(msg tea.Msg) { s.prog.Send(msg) }

// Done is closed when the program has terminated.
func (s *Session) Done() <-chan struct{} { return s.done }

// Close quits the program and blocks until it has fully shut down, so the
// terminal is restored before callers print to stdout/stderr. Expected
// terminations (clean quit, context kill, interrupt) return nil.
func (s *Session) Close() error {
	s.prog.Quit()
	<-s.done
	if s.runErr != nil && !errors.Is(s.runErr, tea.ErrProgramKilled) && !errors.Is(s.runErr, tea.ErrInterrupted) {
		return s.runErr
	}
	return nil
}

func (s *Session) closedErr() error {
	if s.runErr != nil {
		return fmt.Errorf("%w: %v", ErrClosed, s.runErr)
	}
	return ErrClosed
}

// adoptConfigMsg swaps the frame chrome (subtitle, config path, colors) of a
// running session, so a flow can take over the dashboard's program without a
// visible altscreen teardown.
type adoptConfigMsg struct{ cfg Config }

// Adopt rebrands a live session for a new flow: the frame stays on screen,
// only the chrome (subtitle, version, config path, build signature, color
// gate) changes. Screens already on the stack are unaffected.
func (s *Session) Adopt(cfg Config) {
	cfg.AppName = cleanChrome(cfg.AppName)
	cfg.Subtitle = cleanChrome(cfg.Subtitle)
	cfg.Version = cleanChrome(cfg.Version)
	cfg.ConfigPath = cleanChrome(cfg.ConfigPath)
	cfg.BuildSig = cleanChrome(cfg.BuildSig)
	s.prog.Send(adoptConfigMsg{cfg: cfg})
}

// cleanChrome strips ANSI escapes and control characters from a single-line
// chrome string (frame header/footer data).
func cleanChrome(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return ' '
		}
		return r
	}, s)
}
