package orchestrator

import "github.com/tis24dev/proxsave/internal/tui"

// newTUIApp is an injection point for tests. Production uses tui.NewApp.
var newTUIApp = tui.NewApp

