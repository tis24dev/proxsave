// Package menu implements the interactive dashboard shown when proxsave is
// invoked bare on an interactive terminal. It is a launcher only: the chosen
// action is dispatched by the caller through the exact same mode paths the
// explicit flags use, after this session is closed (so backup logs and the
// other flows own the terminal normally).
package menu

import (
	"context"
	"errors"

	"github.com/tis24dev/proxsave/internal/ui/components"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Action is the dashboard choice.
type Action int

const (
	ActionExit Action = iota
	ActionBackup
	ActionRestore
	ActionDecrypt
	ActionNewKey
	ActionReconfigure
)

// errMenuExit is the esc sentinel (leave without doing anything).
var errMenuExit = errors.New("dashboard: exit")

// Run shows the dashboard and returns the chosen action. Esc, Ctrl+C, and a
// dying UI all resolve to ActionExit: a human at the menu must never get a
// surprise backup out of a failed screen.
func Run(ctx context.Context, session *shell.Session) (Action, error) {
	items := []components.SelectorItem[Action]{
		{Label: "Run backup now", Description: "start a backup with the current configuration", Value: ActionBackup},
		{Label: "Restore", Description: "restore a backup onto this system", Value: ActionRestore},
		{Label: "Decrypt", Description: "convert an encrypted backup into a plaintext bundle", Value: ActionDecrypt},
		{Label: "New encryption key", Description: "reset the AGE recipients and run the key setup", Value: ActionNewKey},
		{Label: "Reconfigure", Description: "re-run the interactive installation/setup", Value: ActionReconfigure},
		{Label: "Exit", Description: "leave without doing anything", Value: ActionExit},
	}
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Dashboard", items,
		components.WithSelectorPrompt[Action]("What do you want to do?\n(Non-interactive invocations, e.g. cron, run the backup directly.)"),
		components.WithSelectorBack[Action](errMenuExit),
	))
	if err != nil {
		if errors.Is(err, errMenuExit) || errors.Is(err, shell.ErrAborted) || errors.Is(err, shell.ErrClosed) {
			return ActionExit, nil
		}
		return ActionExit, err
	}
	return action, nil
}
