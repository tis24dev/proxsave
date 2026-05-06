package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

type tuiWorkflowUI struct {
	configPath string
	buildSig   string
	logger     *logging.Logger
	buildPage  tuiPageBuilder

	selectedBackupSummary string
}

func newTUIWorkflowUI(configPath, buildSig string, logger *logging.Logger) *tuiWorkflowUI {
	if strings.TrimSpace(buildSig) == "" {
		buildSig = "n/a"
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	return &tuiWorkflowUI{
		configPath: configPath,
		buildSig:   buildSig,
		logger:     logger,
		buildPage:  buildWizardPage,
	}
}

func newTUIRestoreWorkflowUI(configPath, buildSig string, logger *logging.Logger) *tuiWorkflowUI {
	ui := newTUIWorkflowUI(configPath, buildSig, logger)
	ui.buildPage = buildRestoreWizardPage
	return ui
}

func (u *tuiWorkflowUI) screenEnv() tuiScreenEnv {
	return tuiScreenEnv{
		configPath: u.configPath,
		buildSig:   u.buildSig,
		logger:     u.logger,
		buildPage:  u.buildPage,
	}
}

func (u *tuiWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	if ctx == nil {
		ctx = context.Background()
	}

	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	app := newTUIApp()

	messageView := tview.NewTextView().
		SetText(tview.Escape(strings.TrimSpace(initialMessage))).
		SetTextAlign(tview.AlignCenter).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	form.SetOnCancel(func() {
		cancel()
		app.Stop()
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(messageView, 0, 1, false).
		AddItem(form.Form, 3, 0, true)

	page := u.buildPage(title, u.configPath, u.buildSig, content)
	form.SetParentView(page)

	done := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	var runErr error

	queueProgressUpdate := func(update func()) {
		select {
		case <-taskCtx.Done():
			return
		default:
		}
		go func() {
			select {
			case <-taskCtx.Done():
				return
			default:
			}
			app.QueueUpdateDraw(update)
		}()
	}

	report := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		queueProgressUpdate(func() {
			messageView.SetText(tview.Escape(message))
		})
	}

	startTask := func() {
		startOnce.Do(func() {
			close(started)
			go func() {
				runErr = run(taskCtx, report)
				close(done)
				app.Stop()
			}()
		})
	}

	app.SetRoot(page, true).SetFocus(form.Form)
	app.SetAfterDrawFunc(func(screen tcell.Screen) {
		startTask()
	})
	if err := app.RunWithContext(taskCtx); err != nil {
		cancel()
		select {
		case <-started:
			<-done
		default:
		}
		return err
	}

	cancel()
	select {
	case <-started:
		<-done
	default:
	}
	return runErr
}

func (u *tuiWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	return u.showOKModal(ctx, title, message, tui.ProxmoxOrange)
}

func (u *tuiWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	return u.showOKModal(ctx, title, fmt.Sprintf("%s %s", tui.SymbolError, message), tui.ErrorRed)
}

func (u *tuiWorkflowUI) showOKModal(ctx context.Context, title, message string, borderColor tcell.Color) error {
	app := newTUIApp()

	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s\n\n[yellow]Press ENTER to continue[white]", tview.Escape(strings.TrimSpace(message)))).
		AddButtons([]string{"OK"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			app.Stop()
		})

	modal.SetBorder(true).
		SetTitle(fmt.Sprintf(" %s ", strings.TrimSpace(title))).
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(borderColor).
		SetBorderColor(borderColor).
		SetBackgroundColor(tcell.ColorBlack)

	page := u.buildPage(title, u.configPath, u.buildSig, modal)
	app.SetRoot(page, true).SetFocus(modal)
	return app.RunWithContext(ctx)
}

func (u *tuiWorkflowUI) SelectBackupSource(ctx context.Context, options []decryptPathOption) (decryptPathOption, error) {
	app := newTUIApp()
	var (
		selected decryptPathOption
		aborted  bool
	)

	list := tview.NewList().ShowSecondaryText(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	for _, opt := range options {
		label := fmt.Sprintf("%s (%s)", opt.Label, opt.Path)
		list.AddItem(label, "", 0, nil)
	}

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(options) {
			return
		}
		selected = options[index]
		app.Stop()
	})
	list.SetDoneFunc(func() {
		aborted = true
		app.Stop()
	})

	form := components.NewForm(app)
	listHeight := len(options)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	form.Form.AddFormItem(
		components.NewListFormItem(list).
			SetLabel("Available backup sources").
			SetFieldHeight(listHeight),
	)
	form.Form.SetFocus(0)
	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := u.buildPage("Select backup source", u.configPath, u.buildSig, form.Form)
	form.SetParentView(page)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := app.RunWithContext(ctx); err != nil {
		return decryptPathOption{}, err
	}
	if aborted || strings.TrimSpace(selected.Path) == "" {
		return decryptPathOption{}, ErrDecryptAborted
	}
	return selected, nil
}

func (u *tuiWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*backupCandidate) (*backupCandidate, error) {
	app := newTUIApp()
	var (
		selected *backupCandidate
		aborted  bool
	)

	list := tview.NewList().ShowSecondaryText(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	type row struct {
		created     string
		hostname    string
		mode        string
		tool        string
		target      string
		compression string
	}

	rows := make([]row, len(candidates))
	var maxHost, maxMode, maxTool, maxTarget, maxComp int

	for idx, cand := range candidates {
		display := describeBackupCandidate(cand)

		rows[idx] = row{
			created:     display.Created,
			hostname:    display.Hostname,
			mode:        display.Mode,
			tool:        display.Tool,
			target:      display.Target,
			compression: display.Compression,
		}

		if len(display.Hostname) > maxHost {
			maxHost = len(display.Hostname)
		}
		if len(display.Mode) > maxMode {
			maxMode = len(display.Mode)
		}
		if len(display.Tool) > maxTool {
			maxTool = len(display.Tool)
		}
		if len(display.Target) > maxTarget {
			maxTarget = len(display.Target)
		}
		if len(display.Compression) > maxComp {
			maxComp = len(display.Compression)
		}
	}

	for idx, r := range rows {
		line := fmt.Sprintf(
			"%2d) %s  %-*s  %-*s  %-*s  %-*s",
			idx+1,
			r.created,
			maxHost, r.hostname,
			maxMode, r.mode,
			maxTool, r.tool,
			maxTarget, r.target,
		)
		if maxComp > 0 {
			line = fmt.Sprintf("%s  %-*s", line, maxComp, r.compression)
		}
		list.AddItem(line, "", 0, nil)
	}

	list.SetSelectedFunc(func(index int, mainText, secondaryText string, shortcut rune) {
		if index < 0 || index >= len(candidates) {
			return
		}
		selected = candidates[index]
		u.selectedBackupSummary = backupSummaryForUI(selected)
		app.Stop()
	})
	list.SetDoneFunc(func() {
		aborted = true
		app.Stop()
	})

	form := components.NewForm(app)
	listHeight := len(candidates)
	if listHeight < 8 {
		listHeight = 8
	}
	if listHeight > 14 {
		listHeight = 14
	}
	form.Form.AddFormItem(
		components.NewListFormItem(list).
			SetLabel("Available backups").
			SetFieldHeight(listHeight),
	)
	form.Form.SetFocus(0)
	form.SetOnCancel(func() {
		aborted = true
	})
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := u.buildPage("Select backup", u.configPath, u.buildSig, form.Form)
	form.SetParentView(page)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := app.RunWithContext(ctx); err != nil {
		return nil, err
	}
	if aborted || selected == nil {
		return nil, ErrDecryptAborted
	}
	return selected, nil
}

func (u *tuiWorkflowUI) PromptDestinationDir(ctx context.Context, defaultDir string) (string, error) {
	app := newTUIApp()
	var (
		destDir   string
		cancelled bool
	)

	defaultDir = strings.TrimSpace(defaultDir)
	if defaultDir == "" {
		defaultDir = "./decrypt"
	}

	form := components.NewForm(app)
	label := "Destination directory"
	form.AddInputFieldWithValidation(label, defaultDir, 48, func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("destination directory cannot be empty")
		}
		return nil
	})
	form.SetOnSubmit(func(values map[string]string) error {
		destDir = strings.TrimSpace(values[label])
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	page := u.buildPage("Destination directory", u.configPath, u.buildSig, form.Form)
	form.SetParentView(page)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := app.RunWithContext(ctx); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return filepath.Clean(destDir), nil
}

func (u *tuiWorkflowUI) ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
	decision, newPath, err := tuiPromptExistingPathDecision(ctx, u.screenEnv(), path, description, failure)
	if err != nil {
		return PathDecisionCancel, "", err
	}
	if decision != PathDecisionNewPath {
		return decision, "", nil
	}
	trimmed := strings.TrimSpace(newPath)
	if trimmed == "" {
		return decision, "", nil
	}
	return decision, filepath.Clean(trimmed), nil
}

func (u *tuiWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	return tuiPromptDecryptSecret(ctx, u.screenEnv(), displayName, previousError)
}

func backupSummaryForUI(cand *backupCandidate) string {
	return describeBackupCandidate(cand).Summary
}
