# Dashboard

The dashboard is the interactive menu you get when you run `proxsave` with no arguments on a terminal. It is a launcher: every choice runs the exact same code as the matching command-line flag, so nothing you do here behaves differently from the CLI. This guide walks through when it appears, the menu, the shared Status vocabulary, and each screen.

For the architecture behind it (the Charm/bubbletea stack, the Session, the Ask bridge, testing), see [DASHBOARD_TUI.md](DASHBOARD_TUI.md).

## When the dashboard opens

The dashboard opens only when both of these are true:

- You ran `proxsave` completely bare, with no flags at all. Any flag, even `--config`, skips it.
- Standard input and standard output are both real terminals, and `TERM` is set and is not `dumb`.

If either is false, `proxsave` runs the backup exactly as it always has. That covers cron, systemd timers, pipes, `ssh` without a TTY, and serial or dumb terminals. The gate is deliberately strict: when in doubt, ProxSave runs the backup and never surprises you with a menu.

The reverse is also a guarantee: a failed or abandoned dashboard never falls through into a backup. If you press Esc or Ctrl+C at the menu, or the screen cannot render, ProxSave exits without doing anything.

To run a backup without the menu (from a script, or just deliberately), use `proxsave --backup`.

### Idle timeout

The menu waits up to 10 minutes for a choice. If nothing is selected in that window, ProxSave exits without action and prints:

```
Dashboard idle timeout: exiting without action. Use proxsave --backup for non-interactive runs.
```

The clock resets every time you interact, so only a genuinely idle menu (or an accidental pty wrapper) hits it. It never turns into a backup.

## How it works

The menu does not contain any logic of its own. When you pick an action, ProxSave sets the same option the command-line flag would set and then runs the normal flow. "Restore" is `--restore`, "Decrypt" is `--decrypt`, "Install" is `--install`, and so on. Compatibility rules are re-checked after the menu, so a menu choice can never reach a state the flags would reject.

Two actions, Backup and Support, keep the menu's screen alive and stream their run inside it (see [Backup](#backup)). The diagnostic and daemon actions run in place and return you to the menu. Everything else hands off to its flow.

## Reading the screens: the Status vocabulary

Every result screen speaks one small vocabulary, so a color and a symbol always mean the same thing:

| Look | Meaning |
|------|---------|
| Green `✓` | Ok. The thing succeeded or is in the expected state. |
| Red `✗` | Error. Something failed. |
| Yellow `⚠` | Warning. Needs attention, usually retryable or a "here is what to do next". |
| Yellow, no symbol | A neutral, pre-check state. You see this before you run a check, shown as `NOT CHECKED`. |

Two keywords are worth calling out because they look similar but are not the same level:

- `NOT CHECKED` is the neutral, no-symbol state: a check that has not run yet.
- `NOT CONFIGURED` is a yellow `⚠` warning: the feature is not enabled on this host, so there is nothing to check.

## The menu

The menu is titled `Dashboard` and is grouped. The prompt reads:

```
What do you want to do?
(Non-interactive invocations, e.g. cron, run the backup directly.)
```

The groups and their items:

```
─── Backup ───
  Backup            start a backup with the current configuration
─── Tools ───
  Restore           restore a backup onto this system
  Decrypt           convert an encrypted backup into a plaintext bundle
─── Maintenance ───
  New key           create new encryption AGE key
  Install           install or re-install ProxSave (edit or wipe)
  Upgrade           check for a newer release and install it from here
─── Diagnostic Checks ───
  Telegram          verify the Telegram relay pairing
  Healthchecks      verify backup monitoring and show the portal link
  Post-install      re-run the post-install audit
─── Daemon ───
  (context-aware, see below)
  Status            show the daemon status
─── Recovery ───
  Cleanup guards    remove leftover restore mount guards
  Support           run a support backup and email the debug log to the maintainer

  Exit              leave without doing anything
```

The Daemon group changes with the current scheduler state, so it only ever offers the command that makes sense:

| Current state | Command shown (plus `Status`) |
|---------------|-------------------------------|
| Running as the daemon | `Disable`, `Restart` |
| On cron | `Install` |
| Daemon disabled earlier with `--daemon-remove` | `Re-enable` |
| Config unreadable | nothing extra (only `Status`) |

Navigate with the arrow keys (or `j`/`k`), `/` filters the list, Enter selects, Esc exits. There are no number shortcuts on this menu because it is long enough that filtering is offered instead.

## Backup

Backup is the one screen that keeps the frame and streams the run inside it. Your `[timestamp] LEVEL message` log lines flow, in color, into a scrollable panel on screen instead of scrolling past in raw text. The same blank-line spacing between sections that you see on the CLI is preserved.

While it runs:

- Arrow keys, PgUp/PgDn, Home/End, and the mouse wheel scroll within the panel. Scrolling up stops the auto-follow so the newest line does not yank you back down; press End (or scroll back to the bottom) to follow again.
- `c` copies the whole log to the clipboard (the original lines, not the wrapped-on-screen rows), handy for a support request.
- Esc requests cancellation of the run.

When the run finishes, the panel shows the outcome banner and waits. Press Enter or Space to return. A non-fatal problem reads as a yellow "completed with warnings", not a red failure; the exit code is identical to a plain CLI run. The last few issues are recapped under the log (capped at 10 lines).

A backup started any other way (cron, `--backup`, the daemon) runs plainly with no panel.

## Restore and Decrypt

Restore and Decrypt hand off to their normal flows, rendered in the TUI. They are the same workflows as `proxsave --restore` and `proxsave --decrypt`; only the interface differs. Add `--cli` on the command line if you want the plain text prompts instead.

A few things you will meet in the restore flow:

- You pick categories from a checkbox list (at least one). Esc goes back to the mode selection, it does not cancel the whole restore.
- Confirmation is two stages. First a `RESTORE` button (which holds the default focus), then a destructive `Overwrite and restore` guard whose default is `Cancel` and which has no single-key `y`/`n` shortcut, so a reflex keypress cannot trigger it.
- For a cluster backup you are asked to choose `SAFE` (export cluster files only, does not write the cluster database) or `RECOVERY` (restore the full cluster database, only when the cluster is offline or isolated), or exit.
- The restore plan is shown in a scrollable pager. Esc or `q` there aborts; it never counts as acceptance.

Full detail lives in [RESTORE_GUIDE.md](RESTORE_GUIDE.md), and cluster specifics in [CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md).

## New key

New key runs the AGE encryption setup, the same as `proxsave --newkey`. See [ENCRYPTION.md](ENCRYPTION.md).

## Install

The single `Install` row opens a small chooser:

- `Edit install` re-runs the installer against your current configuration (`--install`).
- `Wipe install` resets the installation directory, preserving `build/env/identity`, then runs the installer (`--new-install`). It asks you to confirm the destructive wipe first.
- `Back` returns to the menu.

See [INSTALL.md](INSTALL.md).

## Upgrade

The `Upgrade` row opens a chooser with two entries:

- `Check upgrade` looks for a newer release. It runs the check the moment you open it. If you are current it shows green `NO UPGRADE (<version>)`. If a newer release exists it shows yellow with the version, the release URL and notes, and a `Run upgrade` button. A failed check shows yellow `CHECK FAILED`.
- `Check config` compares your `backup.env` against the shipped template. This is the two-step check-and-apply described under [Cleanup guards](#recovery-cleanup-guards); it lists the keys it would add and only offers Apply when there is something to add.

When you run the binary upgrade from here, its log streams into the same contained panel that Backup uses; press Enter when it finishes. On success the daemon, if present, is restarted once and verified.

Binary upgrade details are in [CLI_REFERENCE.md](CLI_REFERENCE.md#binary-upgrade).

## Diagnostic Checks

These verify a feature and return you to the menu. In the dashboard each check (except Post-install) runs automatically when you open it, and its buttons read `Re-check` and `Back`.

### Telegram

Shown for centralized Telegram mode. It lists the pairing steps and boxes your Server ID to send to the bot:

```
Server ID (send this to the bot):
╭───────────────╮
│  <your id>    │
╰───────────────╯
```

Open Telegram, start `@ProxmoxAN_bot`, send the Server ID (digits only), then let the check confirm the pairing. A successful pairing latches, so a later re-check can never un-verify it. Colors follow the shared vocabulary: green for paired, yellow for "start the bot" or "send the ID" or a temporary upstream problem (all retryable), red for a fatal error.

If Telegram is not in centralized mode on this host, the screen reads `Status: ⚠ NOT CONFIGURED`.

### Healthchecks

Verifies backup monitoring and, in centralized mode, boxes a single-use link to your monitoring portal:

```
Your monitoring portal (single-use link, valid ~1h):
╭────────────────────────────────────────╮
│  https://...                            │
╰────────────────────────────────────────╯
```

Open it to set a password and configure alert channels. The link is single-use and valid for about an hour. If you run your own healthchecks server (self mode), the check instead confirms that your alive URL is reachable and shows no link. After each check a `Sensors:` list shows one colored line per monitored check with its state and last-ping age.

See [DAEMON.md](DAEMON.md) for the monitoring model.

### Post-install

Re-runs the post-install audit. Unlike the other checks it waits for you to press `Check`. It runs `proxsave --dry-run` (this can take a minute), then offers a checkbox list of unused or optional collectors you can turn off. What you select is written as `KEY=false` into `backup.env`. If nothing is unused it says so; if you select nothing it makes no changes. Any error here is non-fatal.

## Daemon

The daemon operations are the graphical equivalent of the `--daemon-*` flags. The menu only shows the ones that apply to your current scheduler state (see [the menu](#the-menu)).

- `Status` computes a combined verdict and shows it: whether the service is installed and active, the scheduler mode, the opt-out flag, the running version, and whether the on-disk binary matches the running process. It runs automatically when opened; `Re-check` re-computes it so a restart done elsewhere shows up. A fresh heartbeat reads green `running`; a replaced binary while active reads yellow `behind - restart needed`. All problem states today are yellow warnings.
- `Install` / `Re-enable` installs and enables the service and removes the cron entry.
- `Disable` reverts to the cron scheduler and stops future upgrades from reinstalling the daemon.
- `Restart` restarts the service and verifies it came back aligned. It first waits for any in-progress backup to finish, because a restart would kill a daemon-supervised backup. If the wait times out it reports yellow `deferred - backup running` rather than forcing it.

Everything about the daemon is in [DAEMON.md](DAEMON.md).

## Recovery: Cleanup guards

During some restores ProxSave places a read-only guard over a datastore mountpoint so a restore cannot write to `/` while the underlying storage is offline. `Cleanup guards` removes leftover guards. It is a two-step screen:

1. A read-only check. If there is nothing to clean it shows green and offers only `Re-check` and `Back`, never Apply. If it finds guards it shows yellow with a count.
2. Apply runs the real cleanup. If everything is removed it shows green `DONE`. If anything is left behind (or the state cannot be re-read) it shows yellow `PENDING` with guidance to unmount the datastore and run it again once the storage is offline.

The command-line equivalent is `proxsave --cleanup-guards`; see [CLI_REFERENCE.md](CLI_REFERENCE.md#cleanup-mount-guards-optional).

## Support

Support collects a little context, then runs a backup in debug mode and emails the log to the maintainer. It is a single screen (not a sequence): a short consent note above two fields.

The note tells you the run is in debug mode, the log will be emailed to the maintainer, and it may contain personal data such as this server's MAC address. The two fields are your GitHub nickname and the GitHub issue number (`#1234`). The maintainer's email address is never shown.

On confirm it runs like Backup, streaming the debug run in the frame. Cancel or Esc returns to the menu. The command-line equivalent is `proxsave --support`.

## Keyboard and mouse

- Esc goes back one screen, or answers `No` on a yes/no prompt, or clears an active filter first. It is never a global quit.
- Ctrl+C quits the whole dashboard from any screen.
- The mouse is always on. Click a row to select it, use the wheel to scroll a list or a log panel.
- On a checkbox list, Space toggles a row, `a` selects all, `i` inverts.

## Exiting and timeouts

You leave the dashboard by choosing `Exit`, pressing Esc or Ctrl+C at the menu, or letting the 10-minute idle timeout fire. All of these exit cleanly without running anything.
