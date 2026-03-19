package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/tui/components"
)

var (
	tuiPromptExistingPathDecision = promptExistingPathDecisionTUI
	tuiPromptNewPathInput         = promptNewPathInputTUI
	tuiPromptDecryptSecret        = promptDecryptSecretTUI
)

func runTUIAppWithContext(ctx context.Context, app *tui.App) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)

	var state atomic.Int32

	go func() {
		select {
		case <-ctx.Done():
			if state.CompareAndSwap(0, 1) {
				app.Stop()
			}
		case <-done:
		}
	}()

	if err := app.Run(); err != nil {
		if state.CompareAndSwap(0, 2) {
			return err
		}
		if state.Load() == 1 {
			return ctx.Err()
		}
		return err
	}
	if state.CompareAndSwap(0, 2) {
		return nil
	}
	if state.Load() == 1 {
		return ctx.Err()
	}
	return nil
}

func promptExistingPathDecisionTUI(ctx context.Context, env tuiScreenEnv, path, description, failureMessage string) (ExistingPathDecision, string, error) {
	app := newTUIApp()
	decision := PathDecisionCancel

	message := fmt.Sprintf(
		"The %s [yellow]%s[white] already exists.\nSelect how you want to proceed.",
		tview.Escape(description),
		tview.Escape(path),
	)
	if strings.TrimSpace(failureMessage) != "" {
		message = fmt.Sprintf("%s\n\n[red]%s[white]", message, tview.Escape(strings.TrimSpace(failureMessage)))
	}
	message += "\n\n[yellow]Use ←→ or TAB to switch buttons | ENTER to confirm[white]"

	modal := tview.NewModal().
		SetText(message).
		AddButtons([]string{"Overwrite", "Use different path", "Cancel"}).
		SetDoneFunc(func(buttonIndex int, buttonLabel string) {
			switch buttonLabel {
			case "Overwrite":
				decision = PathDecisionOverwrite
			case "Use different path":
				decision = PathDecisionNewPath
			default:
				decision = PathDecisionCancel
			}
			app.Stop()
		})

	modal.SetBorder(true).
		SetTitle(" Existing file ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.WarningYellow).
		SetBorderColor(tui.WarningYellow).
		SetBackgroundColor(tcell.ColorBlack)

	page := env.page("Destination path", modal)
	app.SetRoot(page, true).SetFocus(modal)
	if err := runTUIAppWithContext(ctx, app); err != nil {
		return PathDecisionCancel, "", err
	}
	if decision != PathDecisionNewPath {
		return decision, "", nil
	}

	newPath, err := tuiPromptNewPathInput(ctx, env, path)
	if err != nil {
		if err == ErrDecryptAborted {
			return PathDecisionCancel, "", nil
		}
		return PathDecisionCancel, "", err
	}
	return PathDecisionNewPath, filepath.Clean(newPath), nil
}

func promptNewPathInputTUI(ctx context.Context, env tuiScreenEnv, defaultPath string) (string, error) {
	app := newTUIApp()
	var newPath string
	var cancelled bool

	form := components.NewForm(app)
	label := "New path"
	form.AddInputFieldWithValidation(label, defaultPath, 64, func(value string) error {
		_, err := validateDistinctNewPathInput(value, defaultPath)
		return err
	})
	form.SetOnSubmit(func(values map[string]string) error {
		trimmed, err := validateDistinctNewPathInput(values[label], defaultPath)
		if err != nil {
			return err
		}
		newPath = trimmed
		return nil
	})
	form.SetOnCancel(func() {
		cancelled = true
	})
	form.AddSubmitButton("Continue")
	form.AddCancelButton("Cancel")
	enableFormNavigation(form, nil)

	helper := tview.NewTextView().
		SetText("Provide a writable filesystem path for the decrypted files.").
		SetWrap(true).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true)

	content := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(helper, 3, 0, false).
		AddItem(form.Form, 0, 1, true)

	page := env.page("Choose destination path", content)
	form.SetParentView(page)

	app.SetRoot(page, true).SetFocus(form.Form)
	if err := runTUIAppWithContext(ctx, app); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return filepath.Clean(newPath), nil
}

func validateDistinctNewPathInput(value, defaultPath string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	trimmedDefault := strings.TrimSpace(defaultPath)
	if trimmedDefault != "" && filepath.Clean(trimmed) == filepath.Clean(trimmedDefault) {
		return "", fmt.Errorf("path must be different from existing path")
	}

	return trimmed, nil
}

func promptDecryptSecretTUI(ctx context.Context, env tuiScreenEnv, displayName, previousError string) (string, error) {
	app := newTUIApp()
	var (
		secret    string
		cancelled bool
	)

	name := strings.TrimSpace(displayName)
	if name == "" {
		name = "selected backup"
	}

	infoMessage := fmt.Sprintf(
		"Provide the AGE secret key or passphrase used for [yellow]%s[white].\n\n"+
			"Enter [yellow]0[white] to exit or use [yellow]Cancel[white].",
		tview.Escape(name),
	)
	if strings.TrimSpace(previousError) != "" {
		infoMessage = fmt.Sprintf("%s\n\n[red]%s[white]", infoMessage, tview.Escape(strings.TrimSpace(previousError)))
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
		raw := values[label]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return fmt.Errorf("key or passphrase cannot be empty")
		}
		if trimmed == "0" {
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

	page := env.page("Decrypt key", content)
	form.SetParentView(page)
	app.SetRoot(page, true).SetFocus(form.Form)
	if err := runTUIAppWithContext(ctx, app); err != nil {
		return "", err
	}
	if cancelled {
		return "", ErrDecryptAborted
	}
	return secret, nil
}
