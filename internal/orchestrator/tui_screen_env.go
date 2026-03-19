package orchestrator

import (
	"github.com/rivo/tview"

	"github.com/tis24dev/proxsave/internal/logging"
)

type tuiPageBuilder func(title, configPath, buildSig string, content tview.Primitive) tview.Primitive

type tuiScreenEnv struct {
	configPath string
	buildSig   string
	logger     *logging.Logger
	buildPage  tuiPageBuilder
}

func (e tuiScreenEnv) page(title string, content tview.Primitive) tview.Primitive {
	buildPage := e.buildPage
	if buildPage == nil {
		buildPage = buildWizardPage
	}
	return buildPage(title, e.configPath, e.buildSig, content)
}
