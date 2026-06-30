package wizard

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/tui"
	"github.com/tis24dev/proxsave/internal/types"
)

var (
	telegramSetupWizardRunner = func(ctx context.Context, app *tui.App, root, focus tview.Primitive) error {
		app.SetRoot(root, true)
		app.SetFocus(focus)
		return app.RunWithContext(ctx)
	}

	telegramSetupBuildBootstrap    = orchestrator.BuildTelegramSetupBootstrap
	telegramSetupCheckRegistration = notify.CheckTelegramRegistrationAndProvision
	telegramSetupQueueUpdateDraw   = func(app *tui.App, f func()) { app.QueueUpdateDraw(f) }
	telegramSetupGo                = func(fn func()) { go fn() }
)

type TelegramSetupResult struct {
	orchestrator.TelegramSetupBootstrap

	Shown bool

	CheckAttempts     int
	Verified          bool
	Partial           bool
	LastStatusFatal   bool
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

	// Real-but-silent logger: the reused provision path registers and masks the
	// relay secret via this logger, but io.Discard keeps any debug line out of the
	// tview surface so the rendering is never corrupted.
	silentLogger := logging.New(types.LogLevelDebug, false)
	silentLogger.SetOutput(io.Discard)

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

	var b strings.Builder
	b.WriteString("[yellow]Mode:[white] centralized\n")
	b.WriteString("\n1) Open Telegram and start [yellow]@ProxmoxAN_bot[white]\n")
	b.WriteString("2) Send the [yellow]Server ID[white] below (digits only)\n")
	b.WriteString("3) Press [yellow]Check[white] to verify\n\n")
	b.WriteString("If the check fails, you can press Check again.\n")
	b.WriteString("You can also Skip verification and complete pairing later.\n")
	instructions.SetText(b.String())

	escapedServerID := tview.Escape(result.ServerID)
	serverIDLine := fmt.Sprintf("[yellow]%s[white]", escapedServerID)
	identityLine := ""
	if result.IdentityFile != "" {
		persisted := "not persisted"
		if result.IdentityPersisted {
			persisted = "persisted"
		}
		escapedIdentityFile := tview.Escape(result.IdentityFile)
		escapedPersisted := tview.Escape(persisted)
		identityLine = fmt.Sprintf("\n[gray]Identity file:[white] %s ([yellow]%s[white])", escapedIdentityFile, escapedPersisted)
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
			res := telegramSetupCheckRegistration(checkCtx, result.ServerAPIHost, serverID, baseDir, silentLogger)
			telegramSetupQueueUpdateDraw(app, func() {
				mu.Lock()
				defer mu.Unlock()
				if closing {
					return
				}
				checking = false

				status := res.Status
				result.CheckAttempts++
				result.LastStatusCode = status.Code
				result.LastStatusMessage = status.Message // RAW preserved (truncation/escaping tests assert raw)
				if status.Error != nil {
					result.LastStatusError = status.Error.Error()
				} else {
					result.LastStatusError = ""
				}

				st := orchestrator.ClassifyTelegramSetupResult(res)
				result.LastStatusFatal = st.Fatal
				if st.Verified { // latch: a later re-check can never un-verify
					result.Verified = true
					result.Partial = st.Partial
				}

				switch {
				case st.Verified && !st.Partial:
					setStatus(fmt.Sprintf("[green]✓ %s[white]", tview.Escape(st.Message)))
				case st.Verified && st.Partial:
					setStatus(fmt.Sprintf("[orange]%s[white]", tview.Escape(st.Message)))
				case st.Fatal:
					setStatus(fmt.Sprintf("[red]%s[white]", tview.Escape(st.Message)))
				default:
					hint := "\n\n" + orchestrator.TelegramSetupRetryHint
					if result.CheckAttempts >= orchestrator.TelegramSetupMaxVerificationAttempts {
						hint = "\n\n" + orchestrator.TelegramSetupMaxAttemptsHint
					}
					setStatus(fmt.Sprintf("[yellow]%s[white]%s", tview.Escape(st.Message), hint))
				}
				if refreshButtons != nil {
					refreshButtons()
				}
			})
		})
	}

	refreshButtons = func() {
		form.ClearButtons()
		// Stop offering another check once the shared attempt cap is reached (and
		// not yet verified), matching the CLI which stops after the same number of
		// attempts; an explicit Skip is then required to leave. A fatal status
		// (invalid Server ID / upgrade required) also removes Check: re-checking
		// cannot help, so the user must Skip and resolve it out of band.
		if !result.LastStatusFatal && (result.Verified || result.CheckAttempts < orchestrator.TelegramSetupMaxVerificationAttempts) {
			form.AddButton("Check", checkHandler)
		}
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

	if runErr := telegramSetupWizardRunner(ctx, app, layout, form); runErr != nil {
		return TelegramSetupResult{}, runErr
	}

	return result, nil
}
