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
	ActionNewInstall // wipe the install dir (keep build/env/identity) then re-run the installer (--new-install)
	// Second group (diagnostics): each re-opens an existing setup/check screen
	// in the live dashboard session; the caller loops back to the menu after.
	ActionCheckTelegram
	ActionCheckHealthcheck
	ActionPostInstallCheck
	ActionCheckUpgrade // check for a newer release and (on confirm) install it in-session
	ActionUpdateConfig // merge new template keys into the config file (--upgrade-config), two-step check -> apply
	// Third group (daemon scheduler): setup/remove run the same admin path as the
	// --daemon-setup / --daemon-remove flags; status runs a read-only screen.
	ActionDaemonSetup   // install OR re-enable the resident daemon (--daemon-setup)
	ActionDaemonRemove  // disable the daemon, revert to cron (--daemon-remove)
	ActionDaemonRestart // restart the running daemon (e.g. to pick up a rebuilt binary)
	ActionDaemonStatus  // show the daemon/scheduler state
	// Recovery: post-restore cleanup of leftover mount guards (--cleanup-guards),
	// run in-session as a two-step dry-run -> confirm -> apply result flow.
	ActionCleanupGuards
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
		// Backup: the primary action.
		{Label: "─── Backup ───", Separator: true},
		{Label: "Backup", Description: "start a backup with the current configuration", Value: ActionBackup},
		// Tools: operate on existing backups.
		{Label: "─── Tools ───", Separator: true},
		{Label: "Restore", Description: "restore a backup onto this system", Value: ActionRestore},
		{Label: "Decrypt", Description: "convert an encrypted backup into a plaintext bundle", Value: ActionDecrypt},
		// Maintenance: key/config management and updates.
		{Label: "─── Maintenance ───", Separator: true},
		{Label: "New key", Description: "create new encryption AGE key", Value: ActionNewKey},
		{Label: "Install", Description: "re-run the interactive installation/setup (--install)", Value: ActionReconfigure},
		{Label: "New install", Description: "wipe the install directory (keep build/env/identity) then re-run the installer (--new-install)", Value: ActionNewInstall},
		{Label: "Updates", Description: "check for a newer release and install it from here", Value: ActionCheckUpgrade},
		{Label: "Update config", Description: "add new template keys to the configuration file", Value: ActionUpdateConfig},
		// Diagnostic Checks: re-open existing check screens (the group already says "Check").
		{Label: "─── Diagnostic Checks ───", Separator: true},
		{Label: "Telegram", Description: "verify the Telegram relay pairing", Value: ActionCheckTelegram},
		{Label: "Healthchecks", Description: "verify backup monitoring and show the portal link", Value: ActionCheckHealthcheck},
		{Label: "Post-install", Description: "re-run the post-install audit", Value: ActionPostInstallCheck},
	}

	// Daemon scheduler group: context-aware - only the command that fits the current
	// state, plus the read-only status. The group header already says "Daemon", so the
	// items drop the redundant word. Setup/remove run the same admin path as the flags.
	items = append(items, components.SelectorItem[Action]{Label: "─── Daemon ───", Separator: true})
	switch daemon {
	case DaemonStateActive:
		items = append(items, components.SelectorItem[Action]{Label: "Disable", Description: "stop the daemon and revert to the cron scheduler", Value: ActionDaemonRemove})
		items = append(items, components.SelectorItem[Action]{Label: "Restart", Description: "restart the resident daemon (e.g. to load a rebuilt binary)", Value: ActionDaemonRestart})
	case DaemonStateDisabled:
		items = append(items, components.SelectorItem[Action]{Label: "Re-enable", Description: "switch back to the resident daemon scheduler", Value: ActionDaemonSetup})
	case DaemonStateOnCron:
		items = append(items, components.SelectorItem[Action]{Label: "Install", Description: "switch to the resident daemon scheduler (from cron)", Value: ActionDaemonSetup})
	}
	items = append(items, components.SelectorItem[Action]{Label: "Status", Description: "show the daemon service and scheduler state", Value: ActionDaemonStatus})

	// Recovery: post-restore cleanup of leftover mount guards.
	items = append(items, components.SelectorItem[Action]{Label: "─── Recovery ───", Separator: true})
	items = append(items, components.SelectorItem[Action]{Label: "Cleanup guards", Description: "remove leftover restore mount guards", Value: ActionCleanupGuards})

	// Detach the standalone Exit from the Daemon group above with its own divider.
	items = append(items, components.SelectorItem[Action]{Label: "──────────────", Separator: true})
	items = append(items, components.SelectorItem[Action]{Label: "Exit", Description: "leave without doing anything", Value: ActionExit})
	action, err := shell.Ask(ctx, session, components.NewSelector(
		"Dashboard", items,
		components.WithSelectorPrompt[Action]("What do you want to do?\n(Non-interactive invocations, e.g. cron, run the backup directly.)"),
		components.WithSelectorBack[Action](errMenuExit),
	))
	if err != nil {
		if errors.Is(err, errMenuExit) || shell.IsAbort(err) {
			return ActionExit, nil
		}
		return ActionExit, err
	}
	return action, nil
}
