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
	// Second group (diagnostics): each re-opens an existing setup/check screen
	// in the live dashboard session; the caller loops back to the menu after.
	ActionCheckTelegram
	ActionCheckHealthcheck
	ActionPostInstallCheck
	// Third group (daemon scheduler): setup/remove run the same admin path as the
	// --daemon-setup / --daemon-remove flags; status runs a read-only screen.
	ActionDaemonSetup  // install OR re-enable the resident daemon (--daemon-setup)
	ActionDaemonRemove // disable the daemon, revert to cron (--daemon-remove)
	ActionDaemonStatus // show the daemon/scheduler state
)

// DaemonState tells Run which daemon command(s) to offer, context-aware.
type DaemonState int

const (
	DaemonStateUnknown  DaemonState = iota // config unreadable: offer only Status
	DaemonStateOnCron                      // on cron, not opted out: offer Install
	DaemonStateActive                      // daemon is the active scheduler: offer Disable
	DaemonStateDisabled                    // reverted via --daemon-remove: offer Re-enable
)

// errMenuExit is the esc sentinel (leave without doing anything).
var errMenuExit = errors.New("dashboard: exit")

// Run shows the dashboard and returns the chosen action. Esc, Ctrl+C, and a
// dying UI all resolve to ActionExit: a human at the menu must never get a
// surprise backup out of a failed screen.
func Run(ctx context.Context, session *shell.Session, daemon DaemonState) (Action, error) {
	items := []components.SelectorItem[Action]{
		{Label: "Run backup now", Description: "start a backup with the current configuration", Value: ActionBackup},
		{Label: "Restore", Description: "restore a backup onto this system", Value: ActionRestore},
		{Label: "Decrypt", Description: "convert an encrypted backup into a plaintext bundle", Value: ActionDecrypt},
		{Label: "New encryption key", Description: "reset the AGE recipients and run the key setup", Value: ActionNewKey},
		{Label: "Reconfigure", Description: "re-run the interactive installation/setup", Value: ActionReconfigure},
		// Detached second group: diagnostics that re-open existing check screens.
		{Label: "─── Diagnostics ───", Separator: true},
		{Label: "Check Telegram", Description: "verify the Telegram relay pairing", Value: ActionCheckTelegram},
		{Label: "Check healthchecks", Description: "verify backup monitoring and show the portal link", Value: ActionCheckHealthcheck},
		{Label: "Post-install check", Description: "re-run the post-install audit", Value: ActionPostInstallCheck},
	}

	// Third group (daemon scheduler): context-aware - only the command that fits
	// the current state, plus the read-only status. Setup/remove run the same admin
	// path as the flags; status runs a screen in-session.
	items = append(items, components.SelectorItem[Action]{Label: "─── Daemon ───", Separator: true})
	switch daemon {
	case DaemonStateActive:
		items = append(items, components.SelectorItem[Action]{Label: "Disable daemon", Description: "stop the daemon and revert to the cron scheduler", Value: ActionDaemonRemove})
	case DaemonStateDisabled:
		items = append(items, components.SelectorItem[Action]{Label: "Re-enable daemon", Description: "switch back to the resident daemon scheduler", Value: ActionDaemonSetup})
	case DaemonStateOnCron:
		items = append(items, components.SelectorItem[Action]{Label: "Install daemon", Description: "switch to the resident daemon scheduler (from cron)", Value: ActionDaemonSetup})
	}
	items = append(items, components.SelectorItem[Action]{Label: "Daemon status", Description: "show the daemon service and scheduler state", Value: ActionDaemonStatus})

	items = append(items, components.SelectorItem[Action]{Label: "Exit", Description: "leave without doing anything", Value: ActionExit})
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Dashboard", items,
		components.WithSelectorPrompt[Action]("What do you want to do?\n(Non-interactive invocations, e.g. cron, run the backup directly.)"),
		components.WithSelectorBack[Action](errMenuExit),
	))
	if err != nil {
		if errors.Is(err, errMenuExit) || shell.IsAbort(err) || errors.Is(err, shell.ErrClosed) {
			return ActionExit, nil
		}
		return ActionExit, err
	}
	return action, nil
}
