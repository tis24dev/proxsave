package shell

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/tis24dev/proxsave/internal/uitest"
)

// stubScreen resolves to its value on "enter". Init closes pushed, giving
// tests a race-free "screen is now on the stack" signal.
type stubScreen struct {
	Resolver[int]
	value         int
	pushed        chan struct{}
	doubleResolve bool
	lastMsg       tea.Msg
	lastKey       string
	background    bool
}

func (s *stubScreen) ReceivesBackgroundMessages() bool { return s.background }

func newStubScreen(value int) *stubScreen {
	return &stubScreen{value: value, pushed: make(chan struct{})}
}

func (s *stubScreen) Init() tea.Cmd {
	if s.pushed != nil {
		close(s.pushed)
	}
	return nil
}

func (s *stubScreen) Title() string        { return "stub" }
func (s *stubScreen) Help() string         { return "stub help" }
func (s *stubScreen) View(w, h int) string { return "stub view" }

func (s *stubScreen) Update(msg tea.Msg) (Screen, tea.Cmd) {
	s.lastMsg = msg
	if key, ok := msg.(tea.KeyPressMsg); ok {
		s.lastKey = key.String()
		if key.String() == "enter" {
			if s.doubleResolve {
				// First call wins; the second must be dropped by the once-guard.
				first := s.Resolve(s.value+1, nil)
				_ = s.Resolve(s.value, nil)
				return s, first
			}
			return s, s.Resolve(s.value, nil)
		}
	}
	return s, nil
}

func (s *stubScreen) waitPushed(t *testing.T) {
	t.Helper()
	select {
	case <-s.pushed:
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("screen was never pushed")
	}
}

type askOutcome[T any] struct {
	v   T
	err error
}

func startAsk[T any](ctx context.Context, s *Session, scr AskScreen[T]) <-chan askOutcome[T] {
	ch := make(chan askOutcome[T], 1)
	go func() {
		v, err := Ask(ctx, s, scr)
		ch <- askOutcome[T]{v: v, err: err}
	}()
	return ch
}

func askResult[T any](t *testing.T, ch <-chan askOutcome[T]) (T, error) {
	t.Helper()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("Ask did not return within timeout")
		var zero T
		return zero, nil
	}
}

func TestAskResolvesOnUserAnswer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := StartForTest(ctx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	scr := newStubScreen(42)
	ch := startAsk[int](context.Background(), s, scr)
	scr.waitPushed(t)
	s.Send(KeyMsg("enter"))

	v, err := askResult(t, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 42 {
		t.Fatalf("expected 42, got %d", v)
	}
}

func TestAskFirstResolveWins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := StartForTest(ctx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	scr := newStubScreen(7)
	scr.doubleResolve = true
	ch := startAsk[int](context.Background(), s, scr)
	scr.waitPushed(t)
	s.Send(KeyMsg("enter"))

	v, err := askResult(t, ch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 8 {
		t.Fatalf("expected first resolve (8) to win, got %d", v)
	}
}

func TestAskReturnsOnContextCancel(t *testing.T) {
	progCtx, progCancel := context.WithCancel(context.Background())
	defer progCancel()
	s := StartForTest(progCtx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	askCtx, askCancel := context.WithCancel(context.Background())
	scr := newStubScreen(1)
	ch := startAsk[int](askCtx, s, scr)
	scr.waitPushed(t)
	askCancel()

	_, err := askResult(t, ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// The cancelled screen must be gone: a fresh Ask must receive the keys.
	scr2 := newStubScreen(5)
	ch2 := startAsk[int](context.Background(), s, scr2)
	scr2.waitPushed(t)
	s.Send(KeyMsg("enter"))
	v, err := askResult(t, ch2)
	if err != nil || v != 5 {
		t.Fatalf("follow-up Ask failed: v=%d err=%v", v, err)
	}
}

func TestAskReturnsErrClosedOnCtrlC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := StartForTest(ctx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	scr := newStubScreen(1)
	ch := startAsk[int](context.Background(), s, scr)
	scr.waitPushed(t)
	s.Send(KeyMsg("ctrl+c"))

	// Ctrl+C is a global interrupt: it terminates the program, so the pending Ask
	// resolves to ErrClosed (not a per-screen abort). Flows treat that as an abort
	// via shell.IsAbort; Esc, by contrast, resolves a per-screen Back sentinel.
	_, err := askResult(t, ch)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on ctrl+c interrupt, got %v", err)
	}
	// errors.Is(ErrClosed) alone does not say WHICH termination happened: a UI
	// death satisfies it too (TestAskReturnsErrClosedWhenProgramDies). The whole
	// value is what tells them apart, and it is the value callers outside this
	// package have to stand in for, so pin it here against the real thing rather
	// than leaving each caller to guess at the wording.
	if got, want := err.Error(), ClosedByInterrupt().Error(); got != want {
		t.Fatalf("a real ctrl+c produced %q, but ClosedByInterrupt hands callers %q: every fixture built on it is now a shape production never emits", got, want)
	}
	// Text is not enough for a CALLER: whatsnewRender has to save the seen-flag for a
	// person who pressed ctrl+c and withhold it for a UI that died, and matching on
	// wording would put that decision at the mercy of bubbletea's phrasing. The cause
	// has to survive in the CHAIN.
	if !errors.Is(err, tea.ErrInterrupted) {
		t.Fatalf("ctrl+c does not carry tea.ErrInterrupted in its chain, so a caller cannot tell it from a UI death: %v", err)
	}
}

func TestAskReturnsErrClosedWhenProgramDies(t *testing.T) {
	progCtx, progCancel := context.WithCancel(context.Background())
	s := StartForTest(progCtx, Config{AppName: "ProxSave", UseColor: true})

	scr := newStubScreen(1)
	ch := startAsk[int](context.Background(), s, scr)
	scr.waitPushed(t)
	progCancel() // kill the program out from under the Ask

	_, err := askResult(t, ch)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
	// The other half of the pair: a program that died is NOT a person pressing
	// Ctrl+C, and the two must stay distinguishable. They differ by text only,
	// because closedErr flattens its cause with %v, so a caller that wants to
	// treat them differently has to change closedErr to %w first. This is what
	// pins that they have not quietly collapsed into one value.
	if err.Error() == ClosedByInterrupt().Error() {
		t.Fatalf("a killed program is indistinguishable from ctrl+c: both are %q", err)
	}
	if errors.Is(err, tea.ErrInterrupted) {
		t.Fatalf("a program killed out from under the Ask claims to be a user interrupt: %v", err)
	}
	_ = s.Close()
}

// TestCtrlCOnEmptyStackTerminatesProgram: between Asks there is no screen to
// abort; ctrl+c must terminate the program (approximating SIGINT outside raw
// mode) so the user is never trapped in an unresponsive blank UI.
func TestCtrlCOnEmptyStackTerminatesProgram(t *testing.T) {
	s := StartForTest(context.Background(), Config{AppName: "ProxSave", UseColor: true})
	s.Send(KeyMsg("ctrl+c"))
	select {
	case <-s.Done():
	case <-time.After(uitest.Deadline(5 * time.Second)):
		t.Fatal("program did not terminate on empty-stack ctrl+c")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("interrupt must be an expected close, got %v", err)
	}
}

func TestCloseIsIdempotentAndCleanAfterQuit(t *testing.T) {
	s := StartForTest(context.Background(), Config{AppName: "ProxSave", UseColor: true})
	if err := s.Close(); err != nil {
		t.Fatalf("clean close returned error: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close returned error: %v", err)
	}
}

// TestAskStress exercises the bridge under -race: sequential Asks answered,
// aborted, and cancelled while unrelated messages keep flowing.
func TestAskStress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := StartForTest(ctx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.Send("noise")
				time.Sleep(time.Millisecond)
			}
		}
	}()

	for i := range 60 {
		askCtx, askCancel := context.WithCancel(context.Background())
		scr := newStubScreen(i)
		ch := startAsk[int](askCtx, s, scr)
		scr.waitPushed(t)
		// Ctrl+C is deliberately NOT exercised here: it is now a global interrupt
		// that terminates the program, which would kill the session for the rest of
		// the loop. It has its own dedicated test (TestAskReturnsErrClosedOnCtrlC).
		switch i % 2 {
		case 0:
			s.Send(KeyMsg("enter"))
			v, err := askResult(t, ch)
			if err != nil || v != i {
				t.Fatalf("iter %d: v=%d err=%v", i, v, err)
			}
		case 1:
			askCancel()
			if _, err := askResult(t, ch); !errors.Is(err, context.Canceled) {
				t.Fatalf("iter %d: expected canceled, got %v", i, err)
			}
		}
		askCancel()
	}
	close(stop)
	wg.Wait()
}
