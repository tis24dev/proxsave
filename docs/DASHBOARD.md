# Dashboard

Run `proxsave` without arguments on an interactive terminal to open the dashboard. It provides backup, restore, decryption, configuration, maintenance, diagnostics, scheduling and support actions. Menu choices use the same underlying operation code as explicit commands, with dashboard-specific presentation and confirmation screens.

The flags stay fully supported, and [CLI_REFERENCE.md](CLI_REFERENCE.md) documents all of them. Reach for them when there is no terminal to open the dashboard on: headless hosts, cron entries, provisioning scripts, remote automation, and recovery when the TUI cannot run.

This guide covers invocation rules, navigation and result screens. For the architecture behind it (the Charm/bubbletea stack, the Session, the Ask bridge, testing), see [DASHBOARD_TUI.md](DASHBOARD_TUI.md).

<!-- site-region: using-proxsave:start -->

## When the dashboard opens

The dashboard opens only when both of these are true:

- You ran `proxsave` completely bare, with no flags at all. Any flag, even `--config`, skips it.
- Standard input and standard output are both real terminals, and `TERM` is set and is not `dumb`.

If either is false, the dashboard is skipped. Explicit action flags select their operation; without an action flag, the normal backup path runs. A bare invocation from cron, a systemd timer, a pipe, `ssh` without a TTY, or a terminal with an unset or `dumb` TERM therefore starts a backup. An interactive serial console can open the dashboard if it satisfies the same terminal checks.

The reverse is also a guarantee: a failed or abandoned dashboard never falls through into a backup. If you press Esc or Ctrl+C at the menu, or the screen cannot render, ProxSave exits without doing anything and says so on stderr:

```text
Dashboard unavailable: exiting without action. Use proxsave --backup to run a backup non-interactively.
```

For explicit headless operation, use the [CLI reference](CLI_REFERENCE.md#basic-operations).

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

That warning is what an unattended host uses to tell you the dashboard is worth opening. For unattended hosts, see [release-note acknowledgement](CLI_REFERENCE.md#binary-upgrade). Development builds never show the screen and never warn.

## Menu entries and their flags

The flag mapping is maintained in the [CLI reference](CLI_REFERENCE.md#all-flags). Navigate the dashboard by the task names below; diagnostic checks and Daemon Restart are dashboard-only actions.

| Task | Menu route | Complete procedure |
|---|---|---|
| Create a backup | Backup | [First backup](BACKUP_GUIDE.md#run-and-verify-your-first-backup) |
| Restore selected host configuration | Tools > Restore | [Restore guide](RESTORE_GUIDE.md) |
| Produce a plaintext bundle | Tools > Decrypt | [Decryption](ENCRYPTION.md#decrypting-backups) |
| Set up public AGE recipients | Maintenance > New key | [Encryption](ENCRYPTION.md) |
| Edit or reinstall | Maintenance > Install > Edit install / Wipe install | [Installation](INSTALL.md#fast-install) |
| Update executable or merge settings | Maintenance > Upgrade > Check upgrade / Check config | [Upgrade](INSTALL.md#upgrading-proxsave-binary) |
| Check Telegram or monitoring | Diagnostic Checks > Telegram / Healthchecks | [Notifications](NOTIFICATIONS.md), [monitoring](HEALTHCHECKS.md) |
| Review optional collectors | Diagnostic Checks > Post-install > Check | [Configuration](CONFIGURATION.md#editing-the-configuration-from-the-dashboard) |
| Manage the scheduler | Daemon | [Scheduling](DAEMON.md) |
| Remove restore guards | Recovery > Cleanup guards | [Guard screen](#recovery-cleanup-guards) |
| Send a diagnostic run log | Recovery > Support | [Support consent](#support) |

Backup, Support and binary upgrade stream their output inside the dashboard frame. Other choices open a check screen or hand off to their interactive workflow. Operation compatibility is checked after selection too.

## Reading the screens: the Status vocabulary

Every result screen speaks one small vocabulary, so a color and a symbol always mean the same thing:

| Look | Meaning |
|------|---------|
| Green success | Ok. The thing succeeded or is in the expected state. |
| Red error | Error. Something failed. |
| Yellow warning | Warning. Needs attention, usually retryable or a "here is what to do next". |
| Yellow, no symbol | A neutral, pre-check state. You see this before you run a check, shown as `NOT CHECKED`. |

Two keywords are worth calling out because they look similar but are not the same level:

- `NOT CHECKED` is the neutral, no-symbol state: a check that has not run yet.
- `NOT CONFIGURED` is a yellow warning: the feature is not enabled on this host, so there is nothing to check.

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

Backup keeps the dashboard frame and streams the run inside it. Your `[timestamp] LEVEL message` log lines flow, in color, into a scrollable panel on screen instead of scrolling past in raw text. The same blank-line spacing between sections that you see on the CLI is preserved.

While it runs:

- Arrow keys, PgUp/PgDn, Home/End, and the mouse wheel scroll within the panel. Scrolling up stops the auto-follow so the newest line does not yank you back down; press End (or scroll back to the bottom) to follow again.
- `c` copies the retained original lines to the clipboard, not the wrapped-on-screen rows. The panel retains up to 5,000 lines; use the saved run log for the complete output.
- Esc requests cancellation of the run.

When the run finishes, the panel shows the outcome block and waits. Press Enter or Space to return. A non-fatal problem reads as a yellow "completed with warnings", not a red failure; the exit code is identical to a plain CLI run.

The outcome block is the only recap a dashboard backup gets, because the engine skips its own logged recap on this path. It carries the banner, the backup statistics, the secondary and cloud destination status when they are in use, the Telegram Server ID and the healthchecks portal link or address in centralized mode, the path of the run log, and finally a warnings/errors recap (the first 10 issue lines, with a "... and N more (scroll up to review)" note when there were more). Retained lines remain scrollable in the panel; the saved log contains the complete record.

Scheduled and explicit headless backups run without the dashboard panel.

## Restore and Decrypt

These choices open the interactive backup selector and the corresponding workflow. Read the [restore guide](RESTORE_GUIDE.md) or [decryption procedure](ENCRYPTION.md#decrypting-backups) before proceeding. Restore category selection and the final overwrite guard are separate confirmations; the overwrite guard defaults to Cancel. Esc or `q` in the plan pager aborts rather than accepting it.

For cluster archives, SAFE and RECOVERY are separate strategy choices. SAFE can offer live API changes after exporting cluster material; it does not replace the cluster database. RECOVERY requires the offline or isolated conditions in the [cluster recovery guide](CLUSTER_RECOVERY.md). Do not infer that SAFE leaves the entire live host unchanged.

## New key

Opens public AGE recipient setup. Follow the [encryption guide](ENCRYPTION.md) for recipient choices and independent private-key recovery. Generating a recipient is not a verification that you can decrypt a backup.

## Install

Choose **Edit install** to reopen the installer, **Wipe install** for a destructive reset, or **Back**. Wipe preserves only `build`, `daemon_state`, `env`, `guards`, `identity` and `restore`; stock local archives, logs and configuration are deleted. Copy and verify required data independently before confirming. See [installation and reinstall](INSTALL.md#reinstall-safely).

## Upgrade

Choose **Check upgrade** to check releases automatically, then review the offered version and notes before **Run upgrade**. **Check config** previews the template merge and offers Apply when changes are available. Review its added and retired keys before applying. See [upgrade and configuration effects](INSTALL.md#upgrading-proxsave-binary) for daemon restart, verification and failure handling.

### Which binary drives the upgrade

Release discovery and verification use the currently installed binary. Finalization can use the new binary when the old release supports delegation. The [two upgrade paths](INSTALL.md#the-two-upgrade-paths) explain this boundary and the external installer fallback.

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

Re-runs the post-install audit. Unlike the other checks it waits for you to press `Check`. It runs a diagnostic simulation that writes logs but does not create or upload a backup archive (this can take a minute), then offers a checkbox list of unused or optional collectors you can turn off, each with a detail pane. What you select is written as `KEY=false` into `backup.env`. If nothing is unused it says `NO UNUSED COMPONENTS`; if you select nothing it says `NO CHANGES` and writes nothing. Any error here is non-fatal.

## Daemon

The resident daemon (`proxsave-daemon.service`) is the normal scheduler and the only thing that transmits healthcheck pings. Cron is the legacy path, kept for schedules the daemon cannot express. The menu only shows the ones that apply to your current scheduler (see [the menu](#the-menu)).

- `Status` computes a combined verdict and shows it: whether the service is installed and active, the scheduler mode, the running version, whether the on-disk binary matches the running process, and the readiness of any personal pre/post scripts. It runs automatically when opened; `Re-check` re-computes it so a restart done elsewhere shows up. A fresh heartbeat reads green `RUNNING`; a replaced binary while active reads yellow `BEHIND - RESTART NEEDED`. All problem states today are yellow warnings.
- `Install` installs and enables the service and removes the cron entry. The result screen states what the cron removal actually did. If the crontab could not be verified it reads yellow `INSTALLED - NO CRON ENTRY REMOVED`, and if another schedule that also runs backups is still present it reads yellow `INSTALLED - DUPLICATE SCHEDULE` and tells you to clean up your crons, because the host would otherwise back up twice.
- `Disable` reverts to the cron scheduler and stops future upgrades from reinstalling the daemon. If a backup is in progress it reports yellow `DEFERRED - BACKUP RUNNING` rather than tearing the service out from under it.
- `Restart` restarts the service and verifies it came back aligned. It first waits for any in-progress backup to finish, because a restart would kill a daemon-supervised backup. If the wait times out it reports yellow `DEFERRED - BACKUP RUNNING`; if the restart happened but could not be confirmed it reports yellow `RESTARTED, NOT CONFIRMED` and points you at `Status`. There is no flag for this one: it exists only here.

Everything about the daemon is in [DAEMON.md](DAEMON.md).

## Recovery: Cleanup guards

During some restores ProxSave places a read-only guard over a datastore mountpoint so a restore cannot write to `/` while the underlying storage is offline. `Cleanup guards` removes leftover guards. It is a two-step screen:

1. A read-only check. If there is nothing to clean it shows green `CLEAN` and offers only `Re-check` and `Back`, never Apply. If it finds guards it shows yellow `FOUND` with a count.
2. Apply runs the real cleanup. If everything is removed it shows green `DONE`. If anything is left behind (or the state cannot be re-read) it shows yellow `PENDING` with guidance to unmount the datastore and run it again once the storage is offline.

Headless cleanup and exit-code interpretation are in the [CLI reference](CLI_REFERENCE.md#cleanup-mount-guards-optional).

## Support

Support collects a little context, then runs a backup in debug mode and emails the log to the maintainer. It is a single screen (not a sequence): a consent note above the fields.

The note tells you the run is in debug mode, that the full log is emailed to the maintainer at the end of the run, that anything personal or sensitive in it will be shared, and that it may contain personal data such as this server's MAC address. Below it are four rows: your GitHub nickname, the GitHub issue number (`#1234`), and two acknowledgement toggles that both have to be set to Yes before Continue is accepted, one for consent to send the log and one confirming the GitHub issue is already open. Untouched toggles read No, so nothing is armed by walking away from the form. The maintainer's email address is never shown.

On confirm it runs like Backup, streaming the debug run in the frame. Cancel or Esc returns to the menu. After initial consent, the log is sent automatically when the run ends; there is no final review screen. See [support preparation](TROUBLESHOOTING.md) to prepare a log for review before sharing.

## Keyboard and mouse

- Esc goes back one screen, or answers `No` on a yes/no prompt, or clears an active filter first. It is never a global quit.
- Ctrl+C quits the whole dashboard from any screen.
- The mouse is always on. Click a row to select it, use the wheel to scroll a list or a log panel.
- On a checkbox list, Space toggles a row, `a` selects all, `i` inverts.

## Exiting and timeouts

You leave the dashboard by choosing `Exit`, pressing Esc or Ctrl+C at the menu, or letting the 10-minute idle timeout fire. All of these exit cleanly without running anything.

<!-- site-region: using-proxsave:end -->
