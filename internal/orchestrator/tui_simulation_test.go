package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/tis24dev/proxsave/internal/tui"
)

type simKey struct {
	Key tcell.Key
	R   rune
	Mod tcell.ModMask
}

func withSimAppSequence(t *testing.T, keys []simKey) <-chan struct{} {
	t.Helper()

	orig := newTUIApp
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}
	screen.SetSize(120, 40)

	drawCh := make(chan struct{}, 8)
	var injectOnce sync.Once

	newTUIApp = func() *tui.App {
		app := tui.NewApp()
		app.SetScreen(screen)
		readyCh := make(chan struct{})
		var readyOnce sync.Once
		app.SetAfterDrawFunc(func(screen tcell.Screen) {
			readyOnce.Do(func() {
				close(readyCh)
				drawCh <- struct{}{}
			})
		})

		injectOnce.Do(func() {
			go func() {
				<-readyCh
				for _, k := range keys {
					mod := k.Mod
					if mod == 0 {
						mod = tcell.ModNone
					}
					screen.InjectKey(k.Key, k.R, mod)
				}
			}()
		})
		return app
	}

	t.Cleanup(func() {
		newTUIApp = orig
	})

	return drawCh
}

func withSimApp(t *testing.T, keys []tcell.Key) <-chan struct{} {
	t.Helper()
	seq := make([]simKey, 0, len(keys))
	for _, k := range keys {
		seq = append(seq, simKey{Key: k})
	}
	return withSimAppSequence(t, seq)
}

func TestPromptOverwriteAction_SelectsOverwrite(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	decision, newPath, err := promptExistingPathDecisionTUI(context.Background(), "/tmp/existing", "file", "", "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("promptExistingPathDecisionTUI error: %v", err)
	}
	if decision != PathDecisionOverwrite {
		t.Fatalf("decision=%v; want %v", decision, PathDecisionOverwrite)
	}
	if newPath != "" {
		t.Fatalf("newPath=%q; want empty", newPath)
	}
}

func TestPromptNewPathInput_ContinueReturnsEditedPath(t *testing.T) {
	withSimAppSequence(t, []simKey{
		{Key: tcell.KeyRune, R: '/'},
		{Key: tcell.KeyRune, R: 'a'},
		{Key: tcell.KeyRune, R: 'l'},
		{Key: tcell.KeyRune, R: 't'},
		{Key: tcell.KeyTab},
		{Key: tcell.KeyEnter},
	})

	got, err := promptNewPathInputTUI(context.Background(), "/tmp/newpath", "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("promptNewPathInputTUI error: %v", err)
	}
	if got != "/tmp/newpath/alt" {
		t.Fatalf("path=%q; want %q", got, "/tmp/newpath/alt")
	}
}

func TestPromptExistingPathDecisionTUI_ContextCanceledWhileRunning(t *testing.T) {
	drawCh := withSimAppSequence(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-drawCh
		cancel()
	}()

	_, _, err := promptExistingPathDecisionTUI(ctx, "/tmp/existing", "file", "", "/tmp/config.env", "sig")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestPromptExistingPathDecisionTUI_NewPathContextCanceledWhileRunning(t *testing.T) {
	drawCh := withSimApp(t, []tcell.Key{tcell.KeyRight, tcell.KeyEnter})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-drawCh
		<-drawCh
		cancel()
	}()

	_, _, err := promptExistingPathDecisionTUI(ctx, "/tmp/existing", "file", "", "/tmp/config.env", "sig")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestPromptDecryptSecretTUI_ContextCanceledWhileRunning(t *testing.T) {
	drawCh := withSimAppSequence(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-drawCh
		cancel()
	}()

	_, err := promptDecryptSecretTUI(ctx, "/tmp/config.env", "sig", "backup", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestSelectRestoreModeTUI_SelectsStorage(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyDown, tcell.KeyEnter})

	mode, err := selectRestoreModeTUI(SystemTypePVE, "/tmp/config.env", "sig", "backup")
	if err != nil {
		t.Fatalf("selectRestoreModeTUI error: %v", err)
	}
	if mode != RestoreModeStorage {
		t.Fatalf("mode=%q; want %q", mode, RestoreModeStorage)
	}
}

func TestPromptClusterRestoreModeTUI_SelectsRecovery(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyDown, tcell.KeyEnter})

	choice, err := promptClusterRestoreModeTUI("/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("promptClusterRestoreModeTUI error: %v", err)
	}
	if choice != 2 {
		t.Fatalf("choice=%d; want 2", choice)
	}
}

func TestPromptClusterRestoreModeTUI_CancelAborts(t *testing.T) {
	// Switch focus to the Cancel button then submit.
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	_, err := promptClusterRestoreModeTUI("/tmp/config.env", "sig")
	if err == nil {
		t.Fatalf("expected abort error")
	}
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
