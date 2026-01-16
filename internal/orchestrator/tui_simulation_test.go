package orchestrator

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/tis24dev/proxsave/internal/tui"
)

type simKey struct {
	Key tcell.Key
	R   rune
	Mod tcell.ModMask
}

func withSimAppSequence(t *testing.T, keys []simKey) {
	t.Helper()

	orig := newTUIApp
	screen := tcell.NewSimulationScreen("UTF-8")
	if err := screen.Init(); err != nil {
		t.Fatalf("screen.Init: %v", err)
	}
	screen.SetSize(120, 40)

	newTUIApp = func() *tui.App {
		app := tui.NewApp()
		app.SetScreen(screen)

		go func() {
		// Wait for app.Run() to start event processing.
		time.Sleep(50 * time.Millisecond)
		for _, k := range keys {
			mod := k.Mod
			if mod == 0 {
				mod = tcell.ModNone
			}
			screen.InjectKey(k.Key, k.R, mod)
			time.Sleep(10 * time.Millisecond)
		}
	}()
	return app
}

	t.Cleanup(func() {
		newTUIApp = orig
	})
}

func withSimApp(t *testing.T, keys []tcell.Key) {
	t.Helper()
	seq := make([]simKey, 0, len(keys))
	for _, k := range keys {
		seq = append(seq, simKey{Key: k})
	}
	withSimAppSequence(t, seq)
}

func TestPromptOverwriteAction_SelectsOverwrite(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyEnter})

	got, err := promptOverwriteAction("/tmp/existing", "file", "", "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("promptOverwriteAction error: %v", err)
	}
	if got != pathActionOverwrite {
		t.Fatalf("choice=%q; want %q", got, pathActionOverwrite)
	}
}

func TestPromptNewPathInput_ContinueReturnsDefault(t *testing.T) {
	// Move focus to Continue button then submit.
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyEnter})

	got, err := promptNewPathInput("/tmp/newpath", "/tmp/config.env", "sig")
	if err != nil {
		t.Fatalf("promptNewPathInput error: %v", err)
	}
	if got != "/tmp/newpath" {
		t.Fatalf("path=%q; want %q", got, "/tmp/newpath")
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
