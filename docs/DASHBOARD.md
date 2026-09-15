# Dashboard

The dashboard is how you use ProxSave. Run `proxsave` with no arguments on a terminal and it opens: installing, editing the configuration, upgrading, enabling or checking the daemon, running a backup, restoring, and opening a support report are all one keypress away. It is a launcher: where a choice has a matching command-line flag, picking it runs that flag's code, so nothing you do here behaves differently from the CLI. A few rows have no flag at all and exist only here, and the table below marks them.

The flags stay fully supported, and [CLI_REFERENCE.md](CLI_REFERENCE.md) documents all of them. Reach for them when there is no terminal to open the dashboard on: headless hosts, cron entries, provisioning scripts, remote automation, and recovery when the TUI cannot run.

This guide covers when the dashboard appears, the menu, what each entry does, and which flag each entry maps to. For the architecture behind it (the Charm/bubbletea stack, the Session, the Ask bridge, testing), see [DASHBOARD_TUI.md](DASHBOARD_TUI.md).

## When the dashboard opens

The dashboard opens only when both of these are true:

- You ran `proxsave` completely bare, with no flags at all. Any flag, even `--config`, skips it.
- Standard input and standard output are both real terminals, and `TERM` is set and is not `dumb`.

If either is false, `proxsave` runs the backup exactly as it always has. That covers cron, systemd timers, pipes, `ssh` without a TTY, and serial or dumb terminals. The gate is deliberately strict: when in doubt, ProxSave runs the backup and never surprises you with a menu.

The reverse is also a guarantee: a failed or abandoned dashboard never falls through into a backup. If you press Esc or Ctrl+C at the menu, or the screen cannot render, ProxSave exits without doing anything and says so on stderr:

```text
Dashboard unavailable: exiting without action. Use proxsave --backup to run a backup non-interactively.
```

To run a backup without the menu (from a script, or just deliberately), use `proxsave --backup`.

### Idle timeout

The menu waits up to 10 minutes for a choice. If nothing is selected in that window, ProxSave exits without action and prints:

```text
Dashboard idle timeout: exiting without action. Use proxsave --backup for non-interactive runs.
```

The clock resets every time you interact, so only a genuinely idle menu (or an accidental pty wrapper) hits it. It never turns into a backup. The same 10-minute bound is applied to each sub-screen you open from the menu, so an abandoned check screen cannot hold the terminal either.

## What's new

Before the menu opens for the first time after an upgrade, the dashboard shows a one-shot "What's new" screen listing what changed in the releases between the version you last acknowledged and the one now installed. Esc and `q` do nothing there: Enter is the only key that closes it, and Ctrl+C still quits the dashboard. Either way the notes are marked seen, so the screen does not come back.

Until the notes are acknowledged, every automated run logs one warning:

```text
ProxSave <version> has unseen release notes. Open proxsave to view the new features.
```

That warning is what an unattended host uses to tell you the dashboard is worth opening. If you upgraded a host you never sit at, `proxsave --show-whatsnew` displays the same screen once and clears the warning. Development builds never show the screen and never warn.

## Menu entries and their flags

Where an entry has a flag, picking it runs that flag's code: some entries set the flag internally and fall through to the identical flow, the rest call the same function inside the live dashboard session. The rows marked "none, dashboard only" have no flag and exist only here.

| Menu path | What it does | CLI equivalent |
|-----------|--------------|----------------|
| Backup | Runs a backup with the current configuration, streamed on screen | `--backup` |
| Restore | Restores a backup onto this system | `--restore` |
| Decrypt | Converts an encrypted backup into a plaintext bundle | `--decrypt` |
| New key | Creates a new AGE encryption key | `--newkey` (alias `--age-newkey`) |
| Install > Edit install | Re-runs the installer against the current configuration | `--install` |
| Install > Wipe install | Resets the install directory, then re-runs the installer | `--new-install` |
| Upgrade > Check upgrade | Checks for a newer release and installs it | `--upgrade` |
| Upgrade > Check config | Adds new template variables to `backup.env` | `--upgrade-config` (its read-only step is `--upgrade-config-dry-run`) |
| Telegram | Verifies the Telegram relay pairing | none, dashboard only |
| Healthchecks | Verifies backup monitoring and shows the portal details | none, dashboard only |
| Post-install | Re-runs the post-install audit | none, dashboard only (it runs `proxsave --dry-run` internally) |
| Daemon > Install | Switches the scheduler from cron to the resident daemon | `--daemon-setup` |
| Daemon > Disable | Reverts the scheduler to cron | `--daemon-remove` |
| Daemon > Restart | Restarts the resident daemon and verifies it came back | none, dashboard only |
| Daemon > Status | Shows the daemon service and scheduler state | `--daemon-status` |
| Cleanup guards | Removes leftover restore mount guards | `--cleanup-guards` (preview with `--dry-run`) |
| Support | Runs a support backup and emails the debug log to the maintainer | `--support` |
| What's new | Shown once per release before the menu | `--show-whatsnew` |

Going the other way, `--config`, `--dry-run`, `--log-level` and `--cli` are run modifiers with no menu row, and `--daemon` is what `proxsave-daemon.service` executes rather than something you type.

Compatibility rules are re-checked after the menu, so a menu choice can never reach a state the flags would reject.

Backup, Support and the binary upgrade keep the menu's frame alive and stream their run inside it (see [Backup](#backup)). The diagnostic, daemon and recovery actions run in place and return you to the menu. Everything else hands off to its flow.

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

```text
What do you want to do?
(Non-interactive invocations, e.g. cron, run the backup directly.)
```

The groups and their items:

```text
─── Backup ───
  Backup            start a backup with the current configuration
─── Tools ───
  Restore           restore a backup onto this system
  Decrypt           convert an encrypted backup into a plaintext bundle
─── Maintenance ───
  New key           create new encryption AGE key
  Install           install or re-install ProxSave (edit or wipe)
  Upgrade           update the proxsave binary and merge new config keys
─── Diagnostic Checks ───
  Telegram          verify the Telegram relay pairing
  Healthchecks      verify backup monitoring and show the portal details
  Post-install      re-run the post-install audit
─── Daemon ───
  (context-aware, see below)
  Status            show the daemon service and scheduler state
─── Recovery ───
  Cleanup guards    remove leftover restore mount guards
  Support           run a support backup and email the debug log to the maintainer
──────────────
  Exit              leave without doing anything
```

The Daemon group changes with the scheduler recorded in `backup.env`, so it only ever offers the command that makes sense:

| Current scheduler | Command shown (plus `Status`) |
|-------------------|-------------------------------|
| The resident daemon | `Disable`, `Restart` |
| Cron | `Install` |
| Config unreadable | nothing extra (only `Status`) |

Navigate with the arrow keys (or `j`/`k`), `/` filters the list, Enter selects, Esc exits. There are no number shortcuts on this menu because it is long enough that filtering is offered instead.

## Backup

Backup is the one screen that keeps the frame and streams the run inside it. Your `[timestamp] LEVEL message` log lines flow, in color, into a scrollable panel on screen instead of scrolling past in raw text. The same blank-line spacing between sections that you see on the CLI is preserved.

While it runs:

- Arrow keys, PgUp/PgDn, Home/End, and the mouse wheel scroll within the panel. Scrolling up stops the auto-follow so the newest line does not yank you back down; press End (or scroll back to the bottom) to follow again.
- `c` copies the whole log to the clipboard (the original lines, not the wrapped-on-screen rows), handy for a support request.
- Esc requests cancellation of the run.

When the run finishes, the panel shows the outcome block and waits. Press Enter or Space to return. A non-fatal problem reads as a yellow "completed with warnings", not a red failure; the exit code is identical to a plain CLI run.

The outcome block is the only recap a dashboard backup gets, because the engine skips its own logged recap on this path. It carries the banner, the backup statistics, the secondary and cloud destination status when they are in use, the Telegram Server ID and the healthchecks portal link or address in centralized mode, the path of the run log, and finally a warnings/errors recap (the first 10 issue lines, with a "... and N more (scroll up to review)" note when there were more). The full list stays scrollable in the panel above.

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
- `Wipe install` resets the installation directory, preserving `build`, `env` and `identity`, then runs the installer (`--new-install`). It asks you to confirm the destructive wipe first.
- `Back` returns to the menu.

See [INSTALL.md](INSTALL.md).

## Upgrade

The `Upgrade` row opens a chooser showing your current version, with two entries:

- `Check upgrade` looks for a newer release. It runs the check the moment you open it. If you are current it shows green `NO UPGRADE (<version>)`. If a newer release exists it shows yellow with the version, the release URL and notes, and a `Run upgrade` button. A failed check shows yellow `CHECK FAILED`.
- `Check config` compares your `backup.env` against the shipped template. This is the two-step check-and-apply described under [Cleanup guards](#recovery-cleanup-guards); it lists the variables it would add and only offers Apply when there is something to add. A backup of the file is saved before the merge. It is the same operation as `proxsave --upgrade-config`.

When you run the binary upgrade from here, its log streams into the same contained panel that Backup uses; press Enter when it finishes. On success the daemon, if it is active, is restarted once and verified, and then the dashboard relaunches itself from the freshly installed binary, so the session you continue in is the new version. If the daemon is not active there is nothing to restart and the screen tells you the new binary is on disk.

Binary upgrade details are in [CLI_REFERENCE.md](CLI_REFERENCE.md#binary-upgrade).

### Which binary drives the upgrade

`Run upgrade` here is `proxsave --upgrade`, and that command is executed by the binary already installed on the host. The release check, the download, the signature verification and the binary swap are all the OLD release's code. So a fix to the upgrade flow itself only helps the upgrade AFTER the one that installs it: a host coming from an older release cannot benefit from migration logic shipped in the new one. (Since 0.36.0 the post-install finalize phase is handed to the freshly installed binary, but only when the binary doing the upgrading is new enough to know how to hand it over.)

The externally fetched installer avoids that entirely: it downloads and swaps the binary itself, then runs the NEW binary to finalize.

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
```

Use that route when an upgrade misbehaves, or when the release notes say the upgrade path itself changed. The in-place upgrade, from here or from `proxsave --upgrade`, is the convenient one for routine releases.

## Diagnostic Checks

These verify a feature and return you to the menu. They exist only in the dashboard; there is no flag for them. Each check (except Post-install) runs automatically when you open it, and its buttons read `Re-check` and `Back`.

### Telegram

Shown for centralized Telegram mode. It lists the pairing steps and boxes your Server ID to send to the bot:

```text
Server ID (send this to the bot):
╭───────────────╮
│  <your id>    │
╰───────────────╯
```

Open Telegram, start `@ProxmoxAN_bot`, send the Server ID (digits only), then let the check confirm the pairing. A successful pairing latches, so a later re-check can never un-verify it. Colors follow the shared vocabulary: green for paired, yellow for "start the bot" or "send the ID" or a temporary upstream problem (all retryable), red for a fatal error.

If Telegram is not in centralized mode on this host, the screen says which of the reasons applies instead of one generic line.

### Healthchecks

Verifies backup monitoring and, in centralized mode, boxes the way into your monitoring portal. Until you set a portal password, the box shows a single-use link:

```text
╭──────────────────────────────────────────────╮
│  Your monitoring portal (single-use link,    │
│  valid ~1h):                                 │
│  https://...                                 │
╰──────────────────────────────────────────────╯
Open it to set a password and configure alert channels.
```

Once you have a password the server stops minting links and the box shows the portal address plus the identity you sign in with, which is an email address:

```text
╭──────────────────────────────────────────────╮
│  Your monitoring portal:                     │
│  https://...                                 │
│  Login: ...                                  │
╰──────────────────────────────────────────────╯
Sign in with the password you set.
```

If you run your own healthchecks server (self mode), the check instead confirms that your alive URL is reachable and shows no portal box. After each check a `Sensors:` list shows one colored line per monitored check with its state and last-ping age; that list is centralized only.

Monitoring only transmits under the resident daemon: it is the only thing that pings. If this host is still on cron, enable the daemon from the Daemon group before expecting pings. See [HEALTHCHECKS.md](HEALTHCHECKS.md) for the monitoring model, what each status keyword means, and what to do about it.

### Post-install

Re-runs the post-install audit. Unlike the other checks it waits for you to press `Check`. It runs `proxsave --dry-run` (this can take a minute), then offers a checkbox list of unused or optional collectors you can turn off, each with a detail pane. What you select is written as `KEY=false` into `backup.env`. If nothing is unused it says `NO UNUSED COMPONENTS`; if you select nothing it says `NO CHANGES` and writes nothing. Any error here is non-fatal.

## Daemon

The resident daemon (`proxsave-daemon.service`) is the normal scheduler and the only thing that transmits healthcheck pings. Cron is the legacy path, kept for schedules the daemon cannot express. The daemon operations here are the graphical equivalent of the `--daemon-*` flags, and the menu only shows the ones that apply to your current scheduler (see [the menu](#the-menu)).

- `Status` computes a combined verdict and shows it: whether the service is installed and active, the scheduler mode, the running version, whether the on-disk binary matches the running process, and the readiness of any personal pre/post scripts. It runs automatically when opened; `Re-check` re-computes it so a restart done elsewhere shows up. A fresh heartbeat reads green `RUNNING`; a replaced binary while active reads yellow `BEHIND - RESTART NEEDED`. All problem states today are yellow warnings. It is the same verdict `proxsave --daemon-status` prints, ALL-CAPS here and in lower case on the command line.
- `Install` installs and enables the service and removes the cron entry. The result screen states what the cron removal actually did. If the crontab could not be verified it reads yellow `INSTALLED - NO CRON ENTRY REMOVED`, and if another schedule that also runs backups is still present it reads yellow `INSTALLED - DUPLICATE SCHEDULE` and tells you to clean up your crons, because the host would otherwise back up twice.
- `Disable` reverts to the cron scheduler and stops future upgrades from reinstalling the daemon. If a backup is in progress it reports yellow `DEFERRED - BACKUP RUNNING` rather than tearing the service out from under it.
- `Restart` restarts the service and verifies it came back aligned. It first waits for any in-progress backup to finish, because a restart would kill a daemon-supervised backup. If the wait times out it reports yellow `DEFERRED - BACKUP RUNNING`; if the restart happened but could not be confirmed it reports yellow `RESTARTED, NOT CONFIRMED` and points you at `Status`. There is no flag for this one: it exists only here.

Everything about the daemon is in [DAEMON.md](DAEMON.md).

## Recovery: Cleanup guards

During some restores ProxSave places a read-only guard over a datastore mountpoint so a restore cannot write to `/` while the underlying storage is offline. `Cleanup guards` removes leftover guards. It is a two-step screen:

1. A read-only check. If there is nothing to clean it shows green `CLEAN` and offers only `Re-check` and `Back`, never Apply. If it finds guards it shows yellow `FOUND` with a count.
2. Apply runs the real cleanup. If everything is removed it shows green `DONE`. If anything is left behind (or the state cannot be re-read) it shows yellow `PENDING` with guidance to unmount the datastore and run it again once the storage is offline.

The command-line equivalent is `proxsave --cleanup-guards`; see [CLI_REFERENCE.md](CLI_REFERENCE.md#cleanup-mount-guards-optional).

## Support

Support collects a little context, then runs a backup in debug mode and emails the log to the maintainer. It is a single screen (not a sequence): a consent note above the fields.

The note tells you the run is in debug mode, that the full log is emailed to the maintainer at the end of the run, that anything personal or sensitive in it will be shared, and that it may contain personal data such as this server's MAC address. Below it are four rows: your GitHub nickname, the GitHub issue number (`#1234`), and two acknowledgement toggles that both have to be set to Yes before Continue is accepted, one for consent to send the log and one confirming the GitHub issue is already open. Untouched toggles read No, so nothing is armed by walking away from the form. The maintainer's email address is never shown.

On confirm it runs like Backup, streaming the debug run in the frame. Cancel or Esc returns to the menu. The command-line equivalent is `proxsave --support`, which asks the same questions on stdin.

## Keyboard and mouse

- Esc goes back one screen, or answers `No` on a yes/no prompt, or clears an active filter first. It is never a global quit.
- Ctrl+C quits the whole dashboard from any screen.
- The mouse is always on. Click a row to select it, use the wheel to scroll a list or a log panel.
- On a checkbox list, Space toggles a row, `a` selects all, `i` inverts.

## Exiting and timeouts

You leave the dashboard by choosing `Exit`, pressing Esc or Ctrl+C at the menu, or letting the 10-minute idle timeout fire. All of these exit cleanly without running anything.
