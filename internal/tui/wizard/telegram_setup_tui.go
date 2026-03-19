package wizard

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui"
)

var (
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return app.SetRoot(root, true).SetFocus(focus).Run()
	}

	telegramSetupBuildBootstrap    = orchestrator.BuildTelegramSetupBootstrap
	telegramSetupCheckRegistration = notify.CheckTelegramRegistration
	telegramSetupQueueUpdateDraw   = func(app *tui.App, f func()) { app.QueueUpdateDraw(f) }
	telegramSetupGo                = func(fn func()) { go fn() }
)

type TelegramSetupResult struct {
	orchestrator.TelegramSetupBootstrap

	Shown bool

	CheckAttempts     int
	Verified          bool
	LastStatusCode    int
	LastStatusMessage string
	LastStatusError   string

	SkippedVerification bool
}

func RunTelegramSetupWizard(ctx context.Context, baseDir, configPath, buildSig string) (TelegramSetupResult, error) {
	state, err := telegramSetupBuildBootstrap(configPath, baseDir)
	if err != nil {
		return TelegramSetupResult{}, err
	}
	result := TelegramSetupResult{
		TelegramSetupBootstrap: state,
		Shown:                  true,
	}
	if result.Eligibility != orchestrator.TelegramSetupEligibleCentralized {
		result.Shown = false
		return result, nil
	}

	app := tui.NewApp()
	pages := tview.NewPages()

	instructions := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true)
	instructions.SetBorder(true).
		SetTitle(" Instructions ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)

	serverIDView := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	serverIDView.SetBorder(true).
		SetTitle(" Server ID ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)

	statusView := tview.NewTextView().
		SetDynamicColors(true).
		SetWrap(true)
	statusView.SetBorder(true).
		SetTitle(" Status ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange)

	truncate := func(s string, max int) string {
		s = strings.TrimSpace(s)
		if max <= 0 || len(s) <= max {
			return s
		}
		return s[:max] + "...(truncated)"
	}

	var b strings.Builder
	b.WriteString("[yellow]Mode:[white] centralized\n")
	b.WriteString("\n1) Open Telegram and start [yellow]@ProxmoxAN_bot[white]\n")
	b.WriteString("2) Send the [yellow]Server ID[white] below (digits only)\n")
	b.WriteString("3) Press [yellow]Check[white] to verify\n\n")
	b.WriteString("If the check fails, you can press Check again.\n")
	b.WriteString("You can also Skip verification and complete pairing later.\n")
	instructions.SetText(b.String())

	serverIDLine := fmt.Sprintf("[yellow]%s[white]", result.ServerID)
	identityLine := ""
	if result.IdentityFile != "" {
		persisted := "not persisted"
		if result.IdentityPersisted {
			persisted = "persisted"
		}
		identityLine = fmt.Sprintf("\n[gray]Identity file:[white] %s ([yellow]%s[white])", result.IdentityFile, persisted)
	}
	serverIDView.SetText(serverIDLine + identityLine)

	setStatus := func(text string) {
		statusView.SetText(text)
	}

	setStatus("[yellow]Not checked yet.[white]\n\nPress [yellow]Check[white] after sending the Server ID to the bot.")

	var mu sync.Mutex
	checking := false
	closing := false

	checkCtx, cancelChecks := context.WithCancel(ctx)
	defer cancelChecks()

	form := tview.NewForm()
	form.SetBorder(true).
		SetTitle(" Actions ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	doClose := func(skipped bool) {
		mu.Lock()
		closing = true
		result.SkippedVerification = skipped
		mu.Unlock()
		cancelChecks()
		app.Stop()
	}

	var refreshButtons func()

	checkHandler := func() {
		mu.Lock()
		if checking || closing {
			mu.Unlock()
			return
		}
		checking = true
		mu.Unlock()

		setStatus("[blue]Checking registration…[white]\n\nPlease wait.")
		serverID := result.ServerID
		telegramSetupGo(func() {
			status := telegramSetupCheckRegistration(checkCtx, result.ServerAPIHost, serverID, nil)
			telegramSetupQueueUpdateDraw(app, func() {
				mu.Lock()
				defer mu.Unlock()
				if closing {
					return
				}
				checking = false

				result.CheckAttempts++
				result.LastStatusCode = status.Code
				result.LastStatusMessage = status.Message
				if status.Error != nil {
					result.LastStatusError = status.Error.Error()
				} else {
					result.LastStatusError = ""
				}

				if status.Code == 200 && status.Error == nil {
					result.Verified = true
					setStatus(fmt.Sprintf("[green]✓ Linked successfully.[white]\n\n%s", status.Message))
					if refreshButtons != nil {
						refreshButtons()
					}
					return
				}

				msg := status.Message
				if msg == "" {
					msg = "Registration not active yet"
				}
				var hint string
				switch status.Code {
				case 403, 409:
					hint = "\n\nStart the bot, send the Server ID, then press Check again."
				case 422:
					hint = "\n\nThe Server ID appears invalid. If this persists, re-run the installer or regenerate the identity file."
				default:
					hint = "\n\nYou can press Check again, or Skip verification and complete pairing later."
				}
				setStatus(fmt.Sprintf("[yellow]%s[white]%s", truncate(msg, 300), hint))
			})
		})
	}

	refreshButtons = func() {
		form.ClearButtons()
		form.AddButton("Check", checkHandler)
		if result.Verified {
			form.AddButton("Continue", func() { doClose(false) })
			return
		}
		// Until verification succeeds, require an explicit skip to leave without pairing.
		form.AddButton("Skip", func() { doClose(true) })
	}
	refreshButtons()

	body := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(instructions, 0, 3, false).
		AddItem(serverIDView, 4, 0, false).
		AddItem(statusView, 0, 2, false).
		AddItem(form, 7, 0, true)

	body.SetBorder(true).
		SetTitle(" Telegram Setup ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	pages.AddPage("main", body, true, true)

	layout := buildWizardScreen(
		"ProxSave",
		"ProxSave - Telegram Setup\n\n"+
			"Telegram notifications are enabled.\n"+
			"Complete the bot pairing now to avoid warning noise and skipped notifications.\n",
		"[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit",
		configPath,
		buildSig,
		pages,
	)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			mu.Lock()
			verified := result.Verified
			mu.Unlock()
			if !verified {
				doClose(true)
			} else {
				doClose(false)
			}
			return nil
		}
		return event
	})

	if runErr := telegramSetupWizardRunner(app, layout, form); runErr != nil {
		return TelegramSetupResult{}, runErr
	}

	return result, nil
}
