package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

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
	buildPage  func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive

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

func (u *tuiWorkflowUI) RunTask(ctx context.Context, title, initialMessage string, run func(ctx context.Context, report ProgressReporter) error) error {
	if ctx == nil {
		ctx = context.Background()
	}

	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	app := newTUIApp()

	messageView := tview.NewTextView().
		SetText(strings.TrimSpace(initialMessage)).
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
	var runErr error

	report := func(message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		app.QueueUpdateDraw(func() {
			messageView.SetText(message)
		})
	}

	go func() {
		runErr = run(taskCtx, report)
		close(done)
		app.QueueUpdateDraw(func() {
			app.Stop()
		})
	}()

	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		cancel()
		<-done
		return err
	}

	cancel()
	<-done
	return runErr
}

func (u *tuiWorkflowUI) ShowMessage(ctx context.Context, title, message string) error {
	return u.showOKModal(title, message, tui.ProxmoxOrange)
}

func (u *tuiWorkflowUI) ShowError(ctx context.Context, title, message string) error {
	return u.showOKModal(title, fmt.Sprintf("%s %s", tui.SymbolError, message), tui.ErrorRed)
}

func (u *tuiWorkflowUI) showOKModal(title, message string, borderColor tcell.Color) error {
	app := newTUIApp()

	modal := tview.NewModal().
		SetText(fmt.Sprintf("%s\n\n[yellow]Press ENTER to continue[white]", strings.TrimSpace(message))).
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
	return app.SetRoot(page, true).SetFocus(modal).Run()
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
	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return decryptPathOption{}, err
	}
	if aborted || strings.TrimSpace(selected.Path) == "" {
		return decryptPathOption{}, ErrDecryptAborted
	}
	return selected, nil
}

func (u *tuiWorkflowUI) SelectBackupCandidate(ctx context.Context, candidates []*decryptCandidate) (*decryptCandidate, error) {
	app := newTUIApp()
	var (
		selected *decryptCandidate
		aborted  bool
	)

	list := tview.NewList().ShowSecondaryText(false)
	list.SetMainTextColor(tcell.ColorWhite).
		SetSelectedTextColor(tcell.ColorWhite).
		SetSelectedBackgroundColor(tui.ProxmoxOrange)

	type row struct {
		created     string
		mode        string
		tool        string
		targets     string
		compression string
	}

	rows := make([]row, len(candidates))
	var maxMode, maxTool, maxTargets, maxComp int

	for idx, cand := range candidates {
		created := ""
		if cand != nil && cand.Manifest != nil {
			created = cand.Manifest.CreatedAt.Format("2006-01-02 15:04:05")
		}

		mode := strings.ToUpper(statusFromManifest(cand.Manifest))
		if mode == "" {
			mode = "UNKNOWN"
		}

		toolVersion := "unknown"
		if cand != nil && cand.Manifest != nil {
			if v := strings.TrimSpace(cand.Manifest.ScriptVersion); v != "" {
				toolVersion = v
			}
		}
		tool := "Tool " + toolVersion

		targets := "Targets: unknown"
		if cand != nil && cand.Manifest != nil {
			targets = buildTargetInfo(cand.Manifest)
		}

		comp := ""
		if cand != nil && cand.Manifest != nil {
			if c := strings.TrimSpace(cand.Manifest.CompressionType); c != "" {
				comp = strings.ToUpper(c)
			}
		}

		rows[idx] = row{
			created:     created,
			mode:        mode,
			tool:        tool,
			targets:     targets,
			compression: comp,
		}

		if len(mode) > maxMode {
			maxMode = len(mode)
		}
		if len(tool) > maxTool {
			maxTool = len(tool)
		}
		if len(targets) > maxTargets {
			maxTargets = len(targets)
		}
		if len(comp) > maxComp {
			maxComp = len(comp)
		}
	}

	for idx, r := range rows {
		line := fmt.Sprintf(
			"%2d) %s  %-*s  %-*s  %-*s",
			idx+1,
			r.created,
			maxMode, r.mode,
			maxTool, r.tool,
			maxTargets, r.targets,
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
	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
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
	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return filepath.Clean(destDir), nil
}

func (u *tuiWorkflowUI) ResolveExistingPath(ctx context.Context, path, description, failure string) (ExistingPathDecision, string, error) {
	action, err := promptOverwriteActionFunc(path, description, failure, u.configPath, u.buildSig)
	if err != nil {
		return PathDecisionCancel, "", err
	}
	switch action {
	case pathActionOverwrite:
		return PathDecisionOverwrite, "", nil
	case pathActionNew:
		newPath, err := promptNewPathInputFunc(path, u.configPath, u.buildSig)
		if err != nil {
			return PathDecisionCancel, "", err
		}
		return PathDecisionNewPath, filepath.Clean(newPath), nil
	default:
		return PathDecisionCancel, "", ErrDecryptAborted
	}
}

func (u *tuiWorkflowUI) PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error) {
	app := newTUIApp()
	var (
		secret    string
		cancelled bool
	)

	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "selected backup"
	}

	infoMessage := fmt.Sprintf("Provide the AGE secret key or passphrase used for [yellow]%s[white].", name)
	if strings.TrimSpace(previousError) != "" {
		infoMessage = fmt.Sprintf("%s\n\n[red]%s[white]", infoMessage, strings.TrimSpace(previousError))
	}

	infoText := tview.NewTextView().
		SetText(infoMessage).
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	form := components.NewForm(app)
	label := "Key or passphrase:"
	form.AddPasswordField(label, 64)
	form.SetOnSubmit(func(values map[string]string) error {
		raw := strings.TrimSpace(values[label])
		if raw == "" {
			return fmt.Errorf("key or passphrase cannot be empty")
		}
		if raw == "0" {
			cancelled = true
			return nil
		}
		secret = raw
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(infoText, 0, 2, false).
		AddItem(form.Form, 0, 1, true)

	page := u.buildPage("Decrypt key", u.configPath, u.buildSig, content)
	form.SetParentView(page)
	if err := app.SetRoot(page, true).SetFocus(form.Form).Run(); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return secret, nil
}

func backupSummaryForUI(cand *decryptCandidate) string {
	if cand == nil {
		return ""
	}

	base := strings.TrimSpace(cand.DisplayBase)
	if base == "" {
		switch {
		case strings.TrimSpace(cand.BundlePath) != "":
			base = filepath.Base(strings.TrimSpace(cand.BundlePath))
		case strings.TrimSpace(cand.RawArchivePath) != "":
			base = filepath.Base(strings.TrimSpace(cand.RawArchivePath))
		}
	}

	created := ""
	if cand.Manifest != nil {
		created = cand.Manifest.CreatedAt.Format("2006-01-02 15:04:05")
	}

	if base == "" {
		return created
	}
	if created == "" {
		return base
	}
	return fmt.Sprintf("%s (%s)", base, created)
}
