package wizard

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/tui"
)

func buildWizardScreen(title, headerText, navText, configPath, buildSig string, content tview.Primitive) tview.Primitive {
	return tui.BuildScreen(tui.ScreenSpec{
		Title:           title,
		HeaderText:      headerText,
		NavText:         navText,
		ConfigPath:      configPath,
		BuildSig:        buildSig,
		TitleColor:      tui.ProxmoxOrange,
		BorderColor:     tui.ProxmoxOrange,
		BackgroundColor: tcell.ColorBlack,
	}, content)
}
