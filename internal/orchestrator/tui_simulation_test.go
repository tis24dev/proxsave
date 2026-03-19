package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

const simAppInitialDrawTimeout = 2 * time.Second

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
	done := make(chan struct{})
	var injectOnce sync.Once
	var injectWG sync.WaitGroup

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
			injectWG.Add(1)
			go func() {
				defer injectWG.Done()

				timer := time.NewTimer(simAppInitialDrawTimeout)
				defer func() {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
				}()

				select {
				case <-readyCh:
				case <-done:
					return
				case <-timer.C:
					return
				}

				for _, k := range keys {
					mod := k.Mod
					if mod == 0 {
						mod = tcell.ModNone
					}
					select {
					case <-done:
						return
					default:
					}
					screen.InjectKey(k.Key, k.R, mod)
				}
			}()
		})
		return app
	}

	t.Cleanup(func() {
		close(done)
		injectWG.Wait()
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

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	decision, newPath, err := promptExistingPathDecisionTUI(context.Background(), ui.screenEnv(), "/tmp/existing", "file", "")
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

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	got, err := promptNewPathInputTUI(context.Background(), ui.screenEnv(), "/tmp/newpath")
	if err != nil {
		t.Fatalf("promptNewPathInputTUI error: %v", err)
	}
	if got != "/tmp/newpath/alt" {
		t.Fatalf("path=%q; want %q", got, "/tmp/newpath/alt")
	}
}

func TestPromptNewPathInputTUI_UsesProvidedBuilder(t *testing.T) {
	withSimAppSequence(t, []simKey{
		{Key: tcell.KeyRune, R: '/'},
		{Key: tcell.KeyRune, R: 'a'},
		{Key: tcell.KeyRune, R: 'l'},
		{Key: tcell.KeyRune, R: 't'},
		{Key: tcell.KeyTab},
		{Key: tcell.KeyEnter},
	})

	ui := newTUIRestoreWorkflowUI("/tmp/config.env", "sig", nil)
	builderCalls := 0
	var gotTitle, gotConfigPath, gotBuildSig string
	ui.buildPage = func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
		builderCalls++
		gotTitle = title
		gotConfigPath = configPath
		gotBuildSig = buildSig
		return buildRestoreWizardPage(title, configPath, buildSig, content)
	}

	got, err := promptNewPathInputTUI(context.Background(), ui.screenEnv(), "/tmp/newpath")
	if err != nil {
		t.Fatalf("promptNewPathInputTUI error: %v", err)
	}
	if got != "/tmp/newpath/alt" {
		t.Fatalf("path=%q; want %q", got, "/tmp/newpath/alt")
	}
	if builderCalls != 1 {
		t.Fatalf("builderCalls=%d; want 1", builderCalls)
	}
	if gotTitle != "Choose destination path" {
		t.Fatalf("title=%q; want %q", gotTitle, "Choose destination path")
	}
	if gotConfigPath != "/tmp/config.env" {
		t.Fatalf("configPath=%q; want %q", gotConfigPath, "/tmp/config.env")
	}
	if gotBuildSig != "sig" {
		t.Fatalf("buildSig=%q; want %q", gotBuildSig, "sig")
	}
}

func TestPromptExistingPathDecisionTUI_ContextCanceledWhileRunning(t *testing.T) {
	drawCh := withSimAppSequence(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-drawCh
		cancel()
	}()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, _, err := promptExistingPathDecisionTUI(ctx, ui.screenEnv(), "/tmp/existing", "file", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestPromptExistingPathDecisionTUI_NewPathContextCanceledWhileRunning(t *testing.T) {
	drawCh := withSimApp(t, []tcell.Key{tcell.KeyRight, tcell.KeyEnter})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// This flow produces two draw events: the first read waits for the initial
		// promptExistingPathDecisionTUI dialog render, and the second waits for the
		// secondary "new path" dialog opened after selecting that option. Cancel
		// only after both drawCh reads so the test simulates context cancellation
		// while the second dialog is already running.
		<-drawCh
		<-drawCh
		cancel()
	}()

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, _, err := promptExistingPathDecisionTUI(ctx, ui.screenEnv(), "/tmp/existing", "file", "")
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

	ui := newTUIWorkflowUI("/tmp/config.env", "sig", nil)
	_, err := promptDecryptSecretTUI(ctx, ui.screenEnv(), "backup", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestPromptExistingPathDecisionTUI_PassesBuilderToNestedPrompt(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyRight, tcell.KeyEnter})

	ui := newTUIRestoreWorkflowUI("/tmp/config.env", "sig", nil)
	builderCalls := 0
	var gotTitles []string
	var gotConfigPaths []string
	var gotBuildSigs []string
	ui.buildPage = func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
		builderCalls++
		gotTitles = append(gotTitles, title)
		gotConfigPaths = append(gotConfigPaths, configPath)
		gotBuildSigs = append(gotBuildSigs, buildSig)
		return buildRestoreWizardPage(title, configPath, buildSig, content)
	}
	restore := stubTUINewPathInputPrompt(func(ctx context.Context, env tuiScreenEnv, defaultPath string) (string, error) {
		if page := env.page("Spy", tview.NewBox()); page == nil {
			t.Fatalf("expected non-nil page")
		}
		return "/tmp/existing/alt", nil
	})
	defer restore()

	decision, newPath, err := promptExistingPathDecisionTUI(context.Background(), ui.screenEnv(), "/tmp/existing", "file", "")
	if err != nil {
		t.Fatalf("promptExistingPathDecisionTUI error: %v", err)
	}
	if decision != PathDecisionNewPath {
		t.Fatalf("decision=%v; want %v", decision, PathDecisionNewPath)
	}
	if newPath != "/tmp/existing/alt" {
		t.Fatalf("newPath=%q; want %q", newPath, "/tmp/existing/alt")
	}
	if builderCalls != 2 {
		t.Fatalf("builderCalls=%d; want 2", builderCalls)
	}
	if gotTitles[0] != "Destination path" || gotTitles[1] != "Spy" {
		t.Fatalf("titles=%v; want %v", gotTitles, []string{"Destination path", "Spy"})
	}
	for i, configPath := range gotConfigPaths {
		if configPath != "/tmp/config.env" {
			t.Fatalf("configPath[%d]=%q; want %q", i, configPath, "/tmp/config.env")
		}
	}
	for i, buildSig := range gotBuildSigs {
		if buildSig != "sig" {
			t.Fatalf("buildSig[%d]=%q; want %q", i, buildSig, "sig")
		}
	}
}

func TestPromptDecryptSecretTUI_UsesProvidedBuilder(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyTab, tcell.KeyTab, tcell.KeyEnter})

	ui := newTUIRestoreWorkflowUI("/tmp/config.env", "sig", nil)
	builderCalls := 0
	var gotTitle, gotConfigPath, gotBuildSig string
	ui.buildPage = func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive {
		builderCalls++
		gotTitle = title
		gotConfigPath = configPath
		gotBuildSig = buildSig
		return buildRestoreWizardPage(title, configPath, buildSig, content)
	}

	_, err := promptDecryptSecretTUI(context.Background(), ui.screenEnv(), "backup", "")
	if err != ErrDecryptAborted {
		t.Fatalf("err=%v; want %v", err, ErrDecryptAborted)
	}
	if builderCalls != 1 {
		t.Fatalf("builderCalls=%d; want 1", builderCalls)
	}
	if gotTitle != "Decrypt key" {
		t.Fatalf("title=%q; want %q", gotTitle, "Decrypt key")
	}
	if gotConfigPath != "/tmp/config.env" {
		t.Fatalf("configPath=%q; want %q", gotConfigPath, "/tmp/config.env")
	}
	if gotBuildSig != "sig" {
		t.Fatalf("buildSig=%q; want %q", gotBuildSig, "sig")
	}
}

func TestSelectRestoreModeTUI_SelectsStorage(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyDown, tcell.KeyEnter})

	mode, err := selectRestoreModeTUI(context.Background(), SystemTypePVE, "/tmp/config.env", "sig", "backup")
	if err != nil {
		t.Fatalf("selectRestoreModeTUI error: %v", err)
	}
	if mode != RestoreModeStorage {
		t.Fatalf("mode=%q; want %q", mode, RestoreModeStorage)
	}
}

func TestPromptClusterRestoreModeTUI_SelectsRecovery(t *testing.T) {
	withSimApp(t, []tcell.Key{tcell.KeyDown, tcell.KeyEnter})

	choice, err := promptClusterRestoreModeTUI(context.Background(), "/tmp/config.env", "sig")
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

	_, err := promptClusterRestoreModeTUI(context.Background(), "/tmp/config.env", "sig")
	if err == nil {
		t.Fatalf("expected abort error")
	}
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
}
