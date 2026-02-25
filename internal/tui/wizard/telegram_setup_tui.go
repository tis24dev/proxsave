package wizard

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/notify"
	"github.com/tis24dev/proxsave/internal/tui"
)

var (
	telegramSetupWizardRunner = func(app *tui.App, root, focus tview.Primitive) error {
		return app.SetRoot(root, true).SetFocus(focus).Run()
	}

	telegramSetupLoadConfig          = config.LoadConfig
	telegramSetupReadFile            = os.ReadFile
	telegramSetupStat                = os.Stat
	telegramSetupIdentityDetect      = identity.Detect
	telegramSetupCheckRegistration   = notify.CheckTelegramRegistration
	telegramSetupQueueUpdateDraw     = func(app *tui.App, f func()) { app.QueueUpdateDraw(f) }
	telegramSetupGo                  = func(fn func()) { go fn() }
)

type TelegramSetupResult struct {
	Shown bool

	ConfigLoaded bool
	ConfigError  string

	TelegramEnabled bool
	TelegramMode    string
	ServerAPIHost   string

	ServerID            string
	IdentityFile        string
	IdentityPersisted   bool
	IdentityDetectError string

	CheckAttempts     int
	Verified          bool
	LastStatusCode    int
	LastStatusMessage string
	LastStatusError   string

	SkippedVerification bool
}

func RunTelegramSetupWizard(ctx context.Context, baseDir, configPath, buildSig string) (TelegramSetupResult, error) {
	result := TelegramSetupResult{Shown: true}

	cfg, cfgErr := telegramSetupLoadConfig(configPath)
	if cfgErr != nil {
		result.ConfigLoaded = false
		result.ConfigError = cfgErr.Error()
		// Fall back to raw env parsing so the wizard can still run even when the full
		// config parser fails for unrelated keys.
		if configBytes, readErr := telegramSetupReadFile(configPath); readErr == nil {
			values := parseEnvTemplate(string(configBytes))
			result.TelegramEnabled = readTemplateBool(values, "TELEGRAM_ENABLED")
			result.TelegramMode = strings.ToLower(strings.TrimSpace(readTemplateString(values, "BOT_TELEGRAM_TYPE")))
		}
	} else {
		result.ConfigLoaded = true
		result.TelegramEnabled = cfg.TelegramEnabled
		result.TelegramMode = strings.ToLower(strings.TrimSpace(cfg.TelegramBotType))
		result.ServerAPIHost = strings.TrimSpace(cfg.TelegramServerAPIHost)
	}

	if !result.TelegramEnabled {
		result.Shown = false
		return result, nil
	}
	if result.TelegramMode == "" {
		result.TelegramMode = "centralized"
	}
	if result.ServerAPIHost == "" {
		// Fallback (keeps behavior aligned with internal/config defaults).
		result.ServerAPIHost = "https://bot.tis24.it:1443"
	}

	idInfo, idErr := telegramSetupIdentityDetect(baseDir, nil)
	if idErr != nil {
		result.IdentityDetectError = idErr.Error()
	}
	if idInfo != nil {
		result.ServerID = strings.TrimSpace(idInfo.ServerID)
		result.IdentityFile = strings.TrimSpace(idInfo.IdentityFile)
		if result.IdentityFile != "" {
			if _, err := telegramSetupStat(result.IdentityFile); err == nil {
				result.IdentityPersisted = true
			}
		}
	}

	app := tui.NewApp()
	pages := tview.NewPages()

	titleText := tview.NewTextView().
		SetText("ProxSave - Telegram Setup\n\n" +
			"Telegram notifications are enabled.\n" +
			"Complete the bot pairing now to avoid warning noise and skipped notifications.\n").
		SetTextColor(tui.ProxmoxLight).
		SetDynamicColors(true)
	titleText.SetBorder(false)

	nav := tview.NewTextView().
		SetText("[yellow]Navigation:[white] TAB/↑↓ to move | ENTER to select | ESC to exit").
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	nav.SetBorder(false)

	separator := tview.NewTextView().
		SetText(strings.Repeat("─", 80)).
		SetTextColor(tui.ProxmoxOrange)
	separator.SetBorder(false)

	configPathText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Configuration file:[white] %s", configPath)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	configPathText.SetBorder(false)

	buildSigText := tview.NewTextView().
		SetText(fmt.Sprintf("[yellow]Build Signature:[white] %s", buildSig)).
		SetTextColor(tcell.ColorWhite).
		SetDynamicColors(true).
		SetTextAlign(tview.AlignCenter)
	buildSigText.SetBorder(false)

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

	modeLabel := result.TelegramMode
	if modeLabel == "" {
		modeLabel = "centralized"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("[yellow]Mode:[white] %s\n", modeLabel))
	if !result.ConfigLoaded && result.ConfigError != "" {
		b.WriteString(fmt.Sprintf("[red]WARNING:[white] failed to load config: %s\n\n", truncate(result.ConfigError, 200)))
	}
	if result.TelegramMode == "personal" {
		b.WriteString("\nPersonal mode uses your own bot.\n\n")
		b.WriteString("This installer does not guide the personal bot setup.\n")
		b.WriteString("Edit backup.env and set:\n")
		b.WriteString("  - TELEGRAM_BOT_TOKEN\n")
		b.WriteString("  - TELEGRAM_CHAT_ID\n\n")
		b.WriteString("Then run ProxSave once to validate notifications.\n")
	} else {
		b.WriteString("\n1) Open Telegram and start [yellow]@ProxmoxAN_bot[white]\n")
		b.WriteString("2) Send the [yellow]Server ID[white] below (digits only)\n")
		b.WriteString("3) Press [yellow]Check[white] to verify\n\n")
		b.WriteString("If the check fails, you can press Check again.\n")
		b.WriteString("You can also Skip verification and complete pairing later.\n")
	}
	instructions.SetText(b.String())

	serverIDLine := "[red]Server ID unavailable.[white]"
	if result.ServerID != "" {
		serverIDLine = fmt.Sprintf("[yellow]%s[white]", result.ServerID)
	}
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

	initialStatus := "[yellow]Not checked yet.[white]\n\nPress [yellow]Check[white] after sending the Server ID to the bot."
	if result.TelegramMode == "personal" {
		initialStatus = "[yellow]No centralized pairing check for personal mode.[white]"
	}
	if result.ServerID == "" && result.TelegramMode != "personal" {
		initialStatus = "[red]Cannot check registration: Server ID missing.[white]"
		if result.IdentityDetectError != "" {
			initialStatus += "\n\n" + truncate(result.IdentityDetectError, 200)
		}
	}
	setStatus(initialStatus)

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
		if result.TelegramMode == "personal" || strings.TrimSpace(result.ServerID) == "" {
			return
		}

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

		// Centralized mode pairing only works when the Server ID is available.
		if result.TelegramMode != "personal" && strings.TrimSpace(result.ServerID) != "" {
			form.AddButton("Check", checkHandler)
		}

		switch {
		case result.TelegramMode == "personal":
			form.AddButton("Continue", func() { doClose(false) })
		case strings.TrimSpace(result.ServerID) == "":
			form.AddButton("Continue", func() { doClose(false) })
		case result.Verified:
			form.AddButton("Continue", func() { doClose(false) })
		default:
			// Until verification succeeds, require an explicit skip to leave without pairing.
			form.AddButton("Skip", func() { doClose(true) })
		}
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

	layout := tview.NewFlex().
		SetDirection(tview.FlexRow).
		AddItem(titleText, 5, 0, false).
		AddItem(nav, 2, 0, false).
		AddItem(separator, 1, 0, false).
		AddItem(pages, 0, 1, true).
		AddItem(configPathText, 1, 0, false).
		AddItem(buildSigText, 1, 0, false)

	layout.SetBorder(true).
		SetTitle(" ProxSave ").
		SetTitleAlign(tview.AlignCenter).
		SetTitleColor(tui.ProxmoxOrange).
		SetBorderColor(tui.ProxmoxOrange).
		SetBackgroundColor(tcell.ColorBlack)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			if result.TelegramMode != "personal" && strings.TrimSpace(result.ServerID) != "" && !result.Verified {
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
