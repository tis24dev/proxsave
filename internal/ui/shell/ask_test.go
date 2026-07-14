package shell

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	case <-time.After(5 * time.Second):
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
	case <-time.After(5 * time.Second):
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

func TestAskReturnsErrAbortedOnCtrlC(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := StartForTest(ctx, Config{AppName: "ProxSave", UseColor: true})
	defer func() { _ = s.Close() }()

	scr := newStubScreen(1)
	ch := startAsk[int](context.Background(), s, scr)
	scr.waitPushed(t)
	s.Send(KeyMsg("ctrl+c"))

	_, err := askResult(t, ch)
	if !errors.Is(err, ErrAborted) {
		t.Fatalf("expected ErrAborted, got %v", err)
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
	case <-time.After(5 * time.Second):
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

	for i := 0; i < 60; i++ {
		askCtx, askCancel := context.WithCancel(context.Background())
		scr := newStubScreen(i)
		ch := startAsk[int](askCtx, s, scr)
		scr.waitPushed(t)
		switch i % 3 {
		case 0:
			s.Send(KeyMsg("enter"))
			v, err := askResult(t, ch)
			if err != nil || v != i {
				t.Fatalf("iter %d: v=%d err=%v", i, v, err)
			}
		case 1:
			s.Send(KeyMsg("ctrl+c"))
			if _, err := askResult(t, ch); !errors.Is(err, ErrAborted) {
				t.Fatalf("iter %d: expected ErrAborted, got %v", i, err)
			}
		case 2:
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
