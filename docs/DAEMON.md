# Resident daemon

<!-- site-region: schedule-backups:start -->

## Schedule and supervise backups

The resident daemon schedules backups and reports to an external monitor. systemd keeps
the service alive; the daemon chooses when backups run. It can report missed runs through
monitoring even when the backup process cannot send a notification. Cron remains an
alternative scheduler, but does not provide daemon monitoring or personal-script execution.

### Choose the scheduler and schedule

Run `proxsave` without arguments on an interactive terminal. For a new setup or an
existing configuration, choose **Maintenance** > **Install** > **Edit install**.
Review `Scheduler engine`, `Healthchecks`, `Notify level`, `Frequency` and `Run at`.
Weekly schedules also have `Weekday`; monthly schedules have `Day of month`, limited
to 1 through 28. The default schedule is daily at 02:00 in the host's local time.

For an installed host, the **Daemon** group offers actions according to the configured
engine:

| Recorded engine | Available actions |
| --- | --- |
| cron | Install, Status |
| daemon | Disable, Restart, Status |
| Unreadable configuration | Status only |

**Install** switches from cron to the daemon, enables monitoring, installs the service,
removes owned cron entries and restarts the daemon. Do this outside a running backup:
this installation action can stop a supervised run. It differs from **Restart**, which
waits for a running backup rather than immediately cancelling it.

**Disable** reverts to cron, records that preference and disables monitoring. It waits
up to four minutes for the backup lock. If the run does not finish, or configuration
cannot be read, it reports deferral and leaves the transition unapplied. Retry when
idle or after fixing the configuration.

### Prevent duplicate schedules

Inspect the action's result screen. ProxSave removes cron lines whose executed command
is named `proxsave` or `proxmox-backup`, including custom paths. A wrapper script with
another name is outside that ownership rule. It can remain scheduled after the daemon
is installed, and the action warns when an unowned schedule survives.

Review root's crontab and any external scheduler or automation that starts backups.
Remove an obsolete wrapper only after establishing what it does. On reversion, ProxSave
writes its canonical cron line even if an unowned schedule remains; review the warning
so both do not run. A backup lock can reject an overlapping run, but that is not a
substitute for choosing one scheduler. Current upgrade migration respects an explicit
`SCHEDULER_MODE`; legacy migration and finalize behavior are described in the
[retrofit reference](#retrofit-existing-installs).

### Check the effective schedule

Choose **Daemon** > **Status**. Compare the running schedule with the configuration
and read the synchronization verdict. Use **Re-check** to refresh the screen.
A hand edit of schedule keys takes effect after restart, so a changed file can show
`OUT OF SYNC` until you choose **Daemon** > **Restart**.

In centralized monitoring mode a new frequency becomes effective after the monitoring
server confirms the corresponding check period. Until confirmation, the daemon retains
its previously confirmed frequency, or daily if none was confirmed. `PENDING` therefore
requires checking the reason and effective schedule, not assuming the new frequency
already runs. With self-hosted monitoring or monitoring disabled, the schedule applies
locally; set your external backup check's period to match.

Invalid hand-edited schedule values produce warnings and a daily fallback, at the valid
configured time or 02:00 if the time is invalid. Correct the file and restart.
There is no catch-up: a run missed while the host or service was down is skipped, and
monitoring should alert on that absence.

### Verify service and backup outcomes

After switching engines or changing the schedule, check **Daemon** > **Status** for a
running, reporting and aligned daemon. Then check the next scheduled backup's saved
artifacts and the external monitor's event history. Status does not start a backup,
execute personal scripts or prove that the next backup will succeed.

**Restart** waits up to four minutes for a supervised backup, then verifies a fresh,
aligned daemon. An upgrade also attempts restart and verification; a deferred or failed
restart can leave the old process running after the on-disk binary was replaced.
Use the status screen to decide whether further action is needed. An external
`systemctl restart` has no equivalent backup wait and can kill a run in progress.
System service logs can be read outside the dashboard:

```bash
journalctl -u proxsave-daemon.service
```

A dashboard **Backup** run is standalone. It can hand its outcome to a live daemon,
but it does not exercise scheduled personal scripts or update scheduled per-channel
notification sensors. With `BACKUP_ENABLED=false`, scheduled backups are skipped and
no successful backup ping is invented; the liveness heartbeat continues.

### Set supervision and optional personal scripts

Advanced settings are hand edits of `configs/backup.env`. `MAX_RUN_DURATION` bounds a
supervised run. A timed-out child receives SIGTERM and then SIGKILL after a 30-second
grace. Review a timeout as a failed or hung run, not a completed backup.

`PERSONAL_SCRIPT_BEFORE` and `PERSONAL_SCRIPT_AFTER` run only around daemon-supervised
backups. Read the [personal-script policy](#personal-scripts-around-a-run) before using
them: ownership, permissions, mount policy and startup trust decisions matter. Status
shows the running snapshot separately from what a restart would load. A script refused
by policy is not automatically made safe by a restart. These scripts do not run for
cron or dashboard backups.

An uninterruptible D-state child can survive termination. The daemon records persistent
abandonment, suppresses further scheduled backups and marks liveness failed while it is
outstanding. Restarting the daemon does not clear a living abandoned child. Fix the
underlying mount or I/O problem; see [uninterruptible sleep](#caveat-uninterruptible-sleep-d-state)
for cleanup and recovery limits.

For a silent or failed monitor continue with
[monitoring](HEALTHCHECKS.md#configure-and-verify-backup-monitoring).
Automation flags and script exit codes belong in [CLI_REFERENCE.md](CLI_REFERENCE.md).

<!-- site-region: schedule-backups:end -->

## Implementation appendix

## systemd unit

`proxsave-daemon.service` at `/etc/systemd/system/proxsave-daemon.service`:

```ini
[Unit]
Description=ProxSave backup daemon
Documentation=https://github.com/tis24dev/proxsave
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/proxsave --daemon --config <backup.env path>
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

`ExecStart` pins the same `backup.env` the install resolved (via `--config`), so the exact path in the generated unit is your install's config path.

## On-disk state files

The daemon coordinates through eight small files under `<BASE_DIR>/daemon_state/` (a root-only directory, mode `0700`), all written atomically (temp file then rename, mode `0600`) and deliberately not made immutable so they can be rewritten:

| File | Purpose |
|------|---------|
| `.daemon.pid` | the daemon's PID; the contract a standalone backup reads to send `SIGUSR1` |
| `.daemon_info.json` | the running daemon's identity (pid, exec path, version, commit, start time), for display and the restart-verify freshness check |
| `.daemon_runtime.json` | what the running daemon loaded at start (configuration path, personal-script verdicts), read by `--daemon-status` |
| `.schedule_state.json` | the schedule the daemon started with, how healthchecks ran then, and the frequency ProxSave HC Server last confirmed |
| `.healthcheck_status.json` | the last ping outcome per check, read back by the run phase to report real transmission; a corrupt file is quarantined to `.corrupt` and reset |
| `.notify_results.json` | the backup child's per-channel notification severities, handed to the daemon to drive the `proxsave-notify-*` pings |
| `.manual_backup_outcome.json` | a standalone run's outcome, handed off for the daemon to ping |
| `.daemon_abandoned.json` | a backup child the kernel would not let the daemon reap: the orphan's pid and start time, the run id, and when it happened. See [the D-state caveat](#caveat-uninterruptible-sleep-d-state) |

`.daemon.pid` and `.daemon_info.json` are written at startup and removed on shutdown.
`.daemon_abandoned.json` is the exception that deliberately **survives** shutdown - that is its whole purpose - and is removed only once something shows backups can run again.

A ninth file in the same directory, `.daemon.lock`, is not state but the single-instance ownership lock (an advisory `flock`, mode `0600`). It stays on disk between runs; what matters is who holds the lock, not that the file exists.

Before 0.41 these files lived in `<BASE_DIR>/identity/`. The first start of a 0.41 daemon moves them, and `identity/` keeps only the server identity, the relay secret and the AGE keys. `--new-install` keeps `daemon_state/` like `identity/`.

## Configuration keys (`backup.env`)

```ini
# Scheduler engine
SCHEDULER_MODE=cron            # cron | daemon. Raw template value; the wizard writes daemon by default
SCHEDULER_FREQUENCY=daily      # daily, weekly or monthly
SCHEDULER_WEEKDAY=mon          # weekly only: mon, tue, wed, thu, fri, sat or sun
SCHEDULER_MONTHDAY=1           # monthly only: 1-28
SCHEDULER_TIME=02:00           # HH:MM ("Run at")
MAX_RUN_DURATION=1h            # watchdog hard timeout for one backup
BACKUP_ENABLED=true            # false: daemon skips the scheduled run (backup check goes down)

# Personal scripts: your own, started around each run (daemon only)
PERSONAL_SCRIPT_PRE_RUN=       # path to a script started before the run (works only with the daemon)
PERSONAL_SCRIPT_POST_RUN=      # path to a script started after the run, whatever the outcome (works only with the daemon)

# Monitoring: enabled here, configured in HEALTHCHECKS.md
HEALTHCHECK_ENABLED=false      # forced true by --daemon-setup, the dashboard's Install, auto-migration
```

The `HEALTHCHECK_*` keys that decide *where* and *what* the daemon reports live in [HEALTHCHECKS.md](HEALTHCHECKS.md).

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference and [CLI_REFERENCE.md](CLI_REFERENCE.md) for the `--daemon-*` flags.



## Detailed reference

The task above is the operator procedure. The following material preserves detailed behavior, limits and implementation context.

## Backup schedule

The daemon reads `SCHEDULER_FREQUENCY`, `SCHEDULER_WEEKDAY`, `SCHEDULER_MONTHDAY` and `SCHEDULER_TIME` when it starts, and its journal reports the result in one block that ends with the outcome:

```text
Applying backup schedule...
  Frequency: weekly
  Weekday: Monday
  Time: 02:00
Backup schedule: applied
daemon: next backup at 2026-10-05 02:00 (in 3d 15h5m53s)
```

- **Centralized monitor.** The frequency travels to ProxSave HC Server on the daemon's config poll, and the server moves the `proxsave-backup` check, and the periodic notify checks, to that period: 1 day, 7 days or 31 days, grace 1 hour ([HEALTHCHECKS.md](HEALTHCHECKS.md)). A new frequency applies only once the server confirms it. Until then the daemon keeps the frequency it last had confirmed (daily on a host that never had one confirmed), so the backup check never goes down for a run that was not due, and the block ends `Warning Backup schedule: pending, no action needed` with an `In effect:` line and the reason (`ProxSave HC Server not reachable`, `not ready` or `did not confirm`). The daemon asks three times at start, then once per heartbeat (`HEALTHCHECK_HEARTBEAT_INTERVAL`, 5 minutes by default); the heartbeat that gets the confirmation prints the block again, applied, followed by the next backup. The confirmed frequency is kept in `daemon_state/.schedule_state.json`, so a restart while the server is unreachable keeps it.
- **Self-hosted monitor, or healthchecks off.** The frequency applies at once. On your own server ProxSave does not manage the backup check's period: with a weekly or monthly schedule the block adds `Backup check: pinged weekly on your own server` (or `monthly`), and setting the check's period is up to you.
- **An invalid value** (a frequency other than daily, weekly or monthly, a day of the month outside 1-28, a time that is not HH:MM) gets a WARNING block, `Warning Backup schedule: not applied`, with one line per invalid value, before every backup until the value is fixed and the daemon restarted. The backup runs daily meanwhile, at `SCHEDULER_TIME`, or at 02:00 when the time itself is invalid. A variable that is missing or empty takes its default (daily, `mon`, `1`, `02:00`), and the day the frequency does not use is ignored.

A change to these variables takes effect at the next daemon restart. Until then `--daemon-status` reports the schedule `OUT OF SYNC`; see [From the command line](#from-the-command-line).

## Standalone backups: the SIGUSR1 handoff

A backup run outside the daemon (by hand with `--backup`, from a cron line, or from the dashboard's **Backup** row) does not ping the monitor itself. The resident daemon is the sole pinger. Instead, a standalone run drops a handoff file (`.manual_backup_outcome.json`) and wakes the daemon with `SIGUSR1`; the daemon then pings the backup check with that run's outcome. A handoff older than 15 minutes is dropped without pinging (so a long-past run never flips the check), and if no live daemon is found nothing pings.

## Binary alignment after an upgrade

Replacing the on-disk binary alone does not restart the resident daemon, so it can keep running the **old** code. An upgrade restart that is deferred or fails leaves this same condition until the daemon is restarted. ProxSave detects this hash-free: Linux blocks overwriting a running executable, so an upgrade unlinks it and `/proc/<pid>/exe` ends in `" (deleted)"`, which alone proves the daemon is behind.

`--upgrade`, the dashboard's **Upgrade**, and the dashboard's **Daemon** > **Restart** reconcile this with a restart-and-verify: they wait (bounded, up to 4 minutes) for any in-progress daemon-supervised backup to finish (deferring the restart, never killing the backup), restart the service, then poll until the daemon is back, aligned, and freshly started. `--daemon-status` and the dashboard's **Daemon** > **Status** report the same verdict: `behind - restart needed` from the flag, `BEHIND - RESTART NEEDED` on the dashboard screen, which puts every status keyword in ALL-CAPS.

`--daemon-setup` does **not** do that. It restarts the unit immediately and then only polls until the daemon is alive with a readable alignment, accepting one that is still behind. If a daemon-supervised backup is running at that moment it is cancelled: the child gets SIGTERM, and SIGKILL if it does not stop within 30 seconds, and the run ends with no outcome ping, so the monitor sees a start that never finished. `--daemon-status` will not warn you: it reports liveness, unit state and binary alignment, never whether a backup is running. Check for the lock file under `LOCK_PATH`, or choose **Daemon** > **Install** outside the backup window.

## Retrofit existing installs

The dashboard is the ordinary route: the **Daemon** group offers **Install** on a cron host and **Disable** on a daemon host, and each runs exactly the operation its flag runs, including the `backup.env` writes and the crontab changes described below. The flags are for hosts where the TUI is not an option.

- `--upgrade` **auto-migrates** to the daemon only on a host that has never recorded a scheduler engine, i.e. one where that same upgrade's config merge had to add `SCHEDULER_MODE`. Once the key is in the file the value is honoured and no upgrade revisits the host: `cron` means cron, whether you chose it in the wizard, edited it by hand or reached it with `--daemon-remove`. On the host it does migrate it **refuses**, and changes nothing at all, if the crontab still schedules ProxSave through an entry ProxSave does not own ([below](#what-removes-the-cron-entry-means)): installing the daemon on top of one would run every backup twice. `--daemon-setup` installs it anyway and reports what it found.

  Which binary makes this decision on an in-place `proxsave --upgrade` depends on the release already installed. From **0.36.0** onward the whole post-install finalize is handed to the binary that was just installed, this rule included, along with the `backup.env` merge, the documentation refresh, the symlinks, the legacy cron repointing and the daemon restart. Coming from a release older than 0.36.0 there is no handover, because that binary has no `--upgrade-finalize` to hand it to: the replaced binary runs its own finalize, and a change to this rule takes effect one release later. What never moves, on either path, is the release check, the download, the signature and checksum verification and the install itself, because a freshly downloaded binary cannot be the party that verifies itself. The dashboard's **Upgrade** row runs the same code in the process already running and behaves the same way. The externally fetched route hands the finalize over regardless of the release you are coming from:

  ```bash
  bash -c "$(curl -fsSL https://raw.githubusercontent.com/tis24dev/proxsave/main/install.sh)" -- --upgrade
  ```

  That script downloads the new binary first and then hands the finalization to *that* binary (it appends `--localfile`, which skips the release check and download the script already did), so migration logic shipped in the new release is what runs. Use it when upgrading from an older release, or when a release note says the upgrade path itself changed.
- `--daemon-setup`, and the dashboard's **Install**, switch to the daemon at any time. They install the service, remove the proxsave cron entry ([what that means exactly](#what-removes-the-cron-entry-means)), write `SCHEDULER_MODE=daemon` and `HEALTHCHECK_ENABLED=true`, then restart and verify the daemon. They report how many cron lines were actually removed, or that none were found, and they **warn and proceed** (rather than refusing) if an entry ProxSave does not own survives, because you asked for the daemon explicitly.
- `--daemon-remove`, and the dashboard's **Disable**, revert to cron and disable the service. They record `SCHEDULER_MODE=cron`, and that record is what stops later upgrades reinstalling the daemon: the key is present, so it is honoured. A canonical cron line at the configured schedule (`SCHEDULER_FREQUENCY`, the day it uses, and `SCHEDULER_TIME`) is **always** written, and when the host also schedules ProxSave through an entry ProxSave does not own it says so and leaves that entry alone.

  It used to withhold the line in that case, to avoid a second nightly backup. That was the wrong side of the trade. `--daemon-setup` deletes every proxsave cron line on the way in, so a host arriving at a revert has none, and one misidentified entry left it with no daemon and no cron line, at exit `0`, with nothing able to notice: the run that would have noticed is the backup that was never scheduled. The detector answers "is this named after proxsave", not "does this run a proxsave backup" ([below](#what-removes-the-cron-entry-means)), so misidentification is not exotic. A host that really does carry both now runs the backup twice, and the run that loses the per-run lock exits `16` where you can see it.

  Unlike `--daemon-setup`, the revert never tears the daemon down on top of a running backup: removing the unit stops it, which would kill the run. It waits up to 4 minutes for the per-run lock to free, and if the backup is still running when that elapses it aborts having changed nothing, at exit `1` on the flag and on a `DEFERRED - BACKUP RUNNING` screen in the dashboard. Retry when the backup finishes. It aborts the same way, again changing nothing, when the configuration cannot be read, because then the real `LOCK_PATH` is unknown and it cannot tell whether a backup is running at all.

Retrofitting the daemon sets `HEALTHCHECK_ENABLED=true` even though its raw config default is `false`, so a retrofitted host gets the dead-man switch. That is `--daemon-setup`, the dashboard's **Install**, and the upgrade auto-migration; the install wizard is not one of them, because there you answered the monitoring question yourself. `--daemon-remove` and the dashboard's **Disable** set the key back to `false`. That symmetry matters: the checks it turns on are daemon-only, so a host left on cron with `HEALTHCHECK_ENABLED=true` warned `Healthchecks: daemon not installed` on every otherwise successful run and exited `1` for a daemon it was never meant to have. The rollback is what fixes that, by clearing the key the operator never chose. A cron host that still carries `HEALTHCHECK_ENABLED=true` on purpose is warned about it, and that warning still costs the exit code: monitoring cannot work without the daemon, and the key says the operator wants monitoring.

A host that reverted with an **older** build still carries that stale `true`, and nothing it runs rewrites the key for it, so it warns and exits `1` on every otherwise successful backup. ProxSave does not repair it: on disk that host is indistinguishable from one whose operator set the key on purpose, and rewriting a monitoring setting on evidence that cannot tell the two apart takes away a choice instead of tidying a leftover.

Two ways out, both yours to choose: set `HEALTHCHECK_ENABLED=false` in `backup.env`, or use **Daemon** > **Install** followed by **Disable**, which writes the key back to `false` on the way out. Hosts reverted from this release on are unaffected, and a cron host that never enabled the daemon carries the template default `false`.

### What "removes the cron entry" means

Precisely: **every cron line whose command is named `proxsave` or `proxmox-backup`** is deleted, matched on the command's basename, not only the line ProxSave wrote itself and not only the canonical `/usr/local/bin/proxsave` path. The rest of the line is never looked at, so a job that merely *mentions* the binary (`cp /usr/local/bin/proxsave /backup/`) is left alone, and so is `proxmox-backup-client`.

The consequence is the case that matters: **a wrapper is not a proxsave cron entry.** If your crontab runs the backup through a script of your own,

```cron
30 02 * * * /usr/local/sbin/proxsave-nas-guard
```

then its command is named `proxsave-nas-guard`, the rule above does not recognise it, and the daemon migration can neither remove it, nor adopt its schedule into the `SCHEDULER_*` variables (see below), nor count it as a proxsave schedule.

ProxSave detects such an entry separately and never touches it. A wrapper is yours, it may carry a mount guard or an `flock`, and deleting it on a name heuristic would destroy a safety net ProxSave did not write. What it does instead depends on who started the change: the unattended `--upgrade` retrofit **refuses** and changes nothing, while `--daemon-setup` and the install wizard **warn and proceed**, because you asked for the daemon explicitly.

That detection reads three kinds of place: the root crontab; `/etc/crontab` and the active entries in `/etc/cron.d`; and the executable entries of `/etc/cron.hourly`, `/etc/cron.daily`, `/etc/cron.weekly` and `/etc/cron.monthly`.

`/etc/crontab` and `/etc/cron.d` use the system crontab format, where a user field sits between the schedule and the command, so a wrapper installed there is found and reported by its file name:

```text
17 02 * * * root /usr/local/sbin/proxsave-nas-guard [/etc/cron.d/proxsave-guard]   -> its command "proxsave-nas-guard" is named after proxsave
```

The four `cron.*` directories hold no schedule at all: `run-parts` executes every entry that passes its filter, at the cadence `/etc/crontab` gives that directory, so there the file itself is the wrapper and its content is what is read. Such a finding names the script and says why no time is shown:

```text
/etc/cron.daily/nas-guard [/etc/cron.daily]   -> run-parts script with no cron time of its own; it calls the proxsave binary
```

A script `run-parts` would not run is skipped for the same reason a `cron.d` entry `cron` ignores is skipped: it has no execute bit, or its name falls outside `A-Z a-z 0-9 _ -`. Stopping one is `chmod -x` or removing it, not an edit, and the advisory says so.

Files under `/etc` are **read only**. Everything that deletes a cron line, and the schedule adoption below, still work on the root crontab alone: ProxSave writes the crontab it owns and never edits a file it did not place. Entries whose name `cron` itself ignores (anything outside `A-Z a-z 0-9 _ -`, such as `proxsave.bak`) are skipped, because a schedule that never fires cannot collide with anything.

Because of that, the messages state what actually happened instead of asserting a removal:

```text
Daemon mode enabled: proxsave-daemon.service is active. The cron entry was removed.
Daemon mode enabled: proxsave-daemon.service is active. 2 proxsave cron entries were removed.
Daemon mode enabled: proxsave-daemon.service is active. No proxsave cron entry was present to remove.
Daemon mode enabled: proxsave-daemon.service is active. The crontab could not be checked, so a proxsave cron entry may still be scheduled alongside it.
```

The third line is normal on a fresh daemon install, which never had a cron entry. On a host that **was** on cron it is the signal to look: something whose command is not named `proxsave` was scheduling the backup, and it is still scheduled. Either delete that entry and let the daemon schedule the run (moving whatever the wrapper checked elsewhere), or choose **Daemon** > **Disable**, which records `SCHEDULER_MODE=cron` so upgrades leave the host alone. Note that the revert writes its own cron line as well, so a host that keeps the wrapper ends with two: it reports both and leaves the choice to you.

### The schedule is inherited, not reset

`SCHEDULER_TIME` exists since 0.30, and `SCHEDULER_FREQUENCY`, `SCHEDULER_WEEKDAY` and `SCHEDULER_MONTHDAY` since 0.41; before them the crontab line was the only record of when the host backs up. So before the config merge adds them, and before the migration deletes that cron line, the existing proxsave cron entry is read and its schedule is written to `backup.env`. A host running weekly on Sunday at 21:00 keeps running weekly on Sunday at 21:00, and the install or upgrade says so:

```text
Adopting backup schedule from cron entry...
  Frequency: weekly
  Weekday: Sunday
  Time: 21:00
Backup schedule: adopted from cron entry
```

The adoption writes the frequency, the time and the day the frequency uses; the other day variable is left as it is.

`--daemon-setup` and the dashboard's install action do the same, and there the variables are **overwritten** rather than seeded. On a cron host the crontab is the schedule and the `SCHEDULER_*` variables are leftovers nothing keeps in step, so a cron line edited to 21:00 would otherwise hand the daemon whatever the variables still held.

- A schedule you set yourself wins. Install and upgrade consult the crontab only where the file does not record one: with `SCHEDULER_TIME` missing they adopt the line's whole schedule; with `SCHEDULER_TIME` set and `SCHEDULER_FREQUENCY` missing they adopt a weekly or monthly line. A variable present with an empty value counts as set: it means its default, and nothing is adopted over it.
- Only a line that reads as daily, weekly or monthly is adopted: `MM HH * * *` (or `@daily`/`@midnight`), `MM HH * * D`, `MM HH N * *`. A day of the month 29-31 is not adopted, because the daemon runs monthly on days 1-28 so that no month is skipped; neither is a sub-daily or multi-time line (`*/15`, lists, ranges), which the daemon cannot express. Nothing is guessed: the schedule stays as the file has it (daily at 02:00 when it has nothing), and the install or upgrade warns so you can set it yourself.
- Two proxsave cron lines with different schedules are equally ambiguous and warn the same way.
- Only the **root crontab** is inherited from, because that is the table ProxSave owns and is about to rewrite: taking its schedule is continuity, since the line it came from is the line being replaced.
- A proxsave entry under `/etc/crontab` or `/etc/cron.d` is **reported and never adopted**. ProxSave does not edit files it did not place, so that entry survives the install; copying its schedule into the `SCHEDULER_*` variables would put the line ProxSave writes in the exact minute the surviving one already occupies, and the two runs would meet on the per-run lock with one exiting `16` every time. Left alone the host keeps its `/etc` entry and gains ProxSave's own at the configured schedule (daily at 02:00 by default): still two backups, both of which succeed. The warning names the file and the schedule so you can settle it.
- Under `/etc` only a **direct** `proxsave` command is reported this way, not the heuristics the advisories use. Nothing there opens a script to look inside.
- A wrapper entry is not adopted anywhere, so the schedule stays at its default, daily at 02:00, the very minute the wrapper is likely already using. ProxSave warns that the wrapper appears to run ProxSave instead of staying silent, but it will not adopt a schedule out of a script it did not write: set the `SCHEDULER_*` variables yourself.

On the first `--upgrade` to 0.41 the closing summary is printed by the binary being replaced, which lists the warnings about cron lines not adopted but not the block that reports an adoption. The adopted values are in `backup.env` all the same.

## Personal scripts around a run

`PERSONAL_SCRIPT_PRE_RUN` and `PERSONAL_SCRIPT_POST_RUN` name two scripts of your own that
the daemon starts around each supervised run: the first before the backup child, the second
after it. Both are empty by default, which is the shipped state and means nothing is started.

They are **yours, not ProxSave's**, and the whole contract follows from that:

- **Daemon runs only.** A standalone backup, a cron-mode run, and a dashboard run
  start neither script. Only the run the daemon schedules and supervises is bracketed.
- **Nothing is reported about their execution.** Their standard output and standard error go to
  `/dev/null`. Nothing they print or fail at appears in the run log, in the log file, in the
  run recap, in an email, Telegram, Gotify or webhook notification, in a healthchecks ping, or
  in the Prometheus metrics. If you want a record of what your script did, your script writes
  it. The one thing that is reported is not about your script but about ProxSave's own
  decision: when a script is **not started**, the daemon log carries one `WARNING` saying so.
  See **When a script is not started** below.
- **Their exit code is ignored.** A script that fails at run time changes nothing: the backup
  runs anyway, the run's own exit code is unaffected, and no warning is counted in the run. A
  script that has gone missing is not the same thing and does produce that one daemon-log
  `WARNING`, because it did not run at all. (A path that is not executable never gets that far:
  the trusted-path gate below refuses it at daemon start.) `PERSONAL_SCRIPT_POST_RUN` is started after **every** outcome: success,
  failure, a child that skipped because another backup held the lock, hang, and a run
  interrupted by a shutdown. Neither script starts when there is no run at all, which is
  `BACKUP_ENABLED=false` or a tick arriving while the daemon is stopping.
- **Ten minutes each, then killed.** A script still running after 10 minutes is `SIGKILL`ed
  and the daemon carries on. The pre script is waited for before the backup starts, so a slow
  one delays the run by its own duration; the post script is waited for before the daemon goes
  back to waiting for the next scheduled time.
- **A slow pre script delays the monitor's start signal.** The pre script runs before the run
  id is minted, before the `/start` ping is sent and before the `MAX_RUN_DURATION` clock is
  started, deliberately: a script started any later would spend the backup's own watchdog
  budget and would add its own time to the run duration your monitor measures. The price is
  that the `proxsave-backup` check hears from the run only once the pre script has finished, so
  a pre script that takes minutes needs a schedule grace on the monitor wide enough to cover
  it. Both the run and its announcement move later by the pre script's duration; what does not
  move is the run duration the monitor measures, which still begins at the backup itself.
- **The path must be trustworthy, and startup warnings are the one loud thing here.** At
  daemon start each configured path passes a trusted-path gate. The script file itself must
  be owned by root (or by the daemon UID when the service deliberately runs as another user),
  executable, and not writable by group or others. Symlinked paths and loosely writable
  non-sticky directories are refused.

  A non-loosely-writable parent owned by another UID, such as a mode-0700 user home, is
  accepted with `READY WITH WARNING`. The configured path is an explicit trust decision by
  the root administrator: that owner can replace descendants which the daemon later executes
  with daemon privileges. The path remains enabled, and startup emits one `WARNING` for each
  advisory setting. A `REFUSED` path is disabled for that daemon and likewise produces one
  startup `WARNING`, naming the setting and reason. These advisory/refusal warnings are the
  single exception to the scripts' execution silence; without them, a policy decision would
  be indistinguishable from a script that ran and did nothing.

  The startup warning appears once per daemon start, not once per backup. Backups run in a
  child process that never repeats the check, so a scheduled run does not carry it. It comes
  back on a daemon restart, and on demand under `proxsave --daemon-status`.

### Clearing a `READY WITH WARNING`

The warning is not about your script's own ownership. That file already passed the stricter
check, or it would be `REFUSED` rather than accepted. What raises it is a **parent directory**
owned by neither root nor the daemon UID: that owner can unlink your script and put another
file in its place.

Moving the script to a root-owned path such as `/usr/local/bin` is the plain answer. Two others
exist, and they are not equivalent:

- A **hard link** (`ln`, not `ln -s`) of the script into a root-owned directory gives the same
  inode an ancestor chain owned entirely by root. The warning goes because its cause is gone,
  and nothing is weakened: the inode stays one the other user cannot modify. Be aware that most
  editors save by rename-replace, so editing the copy in the home directory creates a **new**
  inode and the hard link keeps serving the old content, silently.
- A **bind mount** of the user-owned directory onto a root-owned mountpoint also clears it,
  because symlink resolution does not see a bind mount and the ancestor chain read is the
  mountpoint's. The real directory stays writable by its owner, so the risk the warning
  describes is unchanged and simply no longer stated.

Do **not** chown a user's home to `root:root` to silence the warning. Root can already traverse
a mode-0700 home, which is why the path is accepted at all.

There is no setting that suppresses the warning, and lowering `DEBUG_LEVEL` to hide it would
hide every other warning ProxSave emits. Whichever of the above you choose, the per-run gate is
unaffected: ProxSave still revalidates the opened inode before every single invocation, so
nothing untrusted is executed even under a bind mount.

### When a script is not started

The startup gate speaks about the path as it was when the daemon started. A second gate runs
before **every** invocation: the file is opened without following a symlink, the opened inode is
re-checked for regular/executable/not group- or other-writable/owned by root or the daemon UID,
and the child execs that inode rather than the pathname, so replacing the file between the check
and the exec cannot change what runs.

When that gate refuses, the script does not run, and the daemon log carries one line:

```text
WARNING  PERSONAL_SCRIPT_PRE_RUN was not started for this run: /home/me/scripts/pre.sh is owned by uid 1000; accepted owners are root or daemon uid 0
```

One line per refusal, so a pre and a post script refused in the same run produce two. The reason
is the specific one: the file is gone, it is no longer executable, it became group- or
other-writable, its owner changed, or the open did not come back within five seconds (a path on
a dead NFS or CIFS mount). This is the only run-time line these scripts can produce, and it is
about ProxSave's decision, never about your script's own behaviour. It goes to the daemon's log,
not the backup's, so no run log, recap, notification, healthchecks ping or metric gains a row.
- **Started as they are.** The path is executed directly: no shell, so no pipes, redirections
  or arguments in the value, and no arguments passed. The script inherits the daemon's own
  environment with two variables removed, `LOG_FILE` and `BASE_DIR`: the first names the run
  log ProxSave is writing at that moment, the second the installation, and between them they
  are the only way a script could learn about the run or write into ProxSave's log. Nothing is
  added, so there is no run id either. The script runs as the daemon does, as `root`, in the
  daemon's working directory. Make it executable and give it a shebang.
- **A stop does not queue behind either script.** A `systemctl stop` or `systemctl restart`
  landing while the run is in flight still starts `PERSONAL_SCRIPT_POST_RUN`, but does not wait
  for it: the daemon's whole teardown budget is the unit's stop timeout, 90 seconds on a stock
  host, so a waited script would be `SIGKILL`ed along with the daemon and would leave
  `.daemon.pid` and `.daemon_info.json` behind. Started and left behind, the script gets its
  chance and the daemon exits cleanly; systemd then collects whatever is still running with the
  rest of the cgroup. A stop landing while a pre or post script is already being **waited for**
  works the same way: the daemon abandons the wait, never the script - it keeps its own
  10-minute budget and is collected with the cgroup if still alive at teardown.
- **The other exception to the wait.** On the abandoned-child path (see the D-state caveat
  below) the daemon must exit fast so systemd can restart it, so there too the post script is
  started and left behind rather than waited for. On both this path and a shutdown there is no
  10-minute kill, because the process that would deliver it is exiting: systemd collects the
  script with the rest of the unit's cgroup.
- **One unbounded corner.** The 10-minute budget covers the wait, not the start. A path that
  lives on a dead NFS or CIFS mount blocks the daemon inside `execve` before any timeout
  exists, and nothing here can interrupt that. Keep the scripts on local storage.

Both values are read once, when the daemon starts. Changing a path in `backup.env` takes
effect at the next daemon restart; changing the contents of the script file takes effect at
the next run.

To diagnose the configured paths without executing either script, run
the diagnostic status invocation described in [CLI_REFERENCE.md](CLI_REFERENCE.md). It reports the running daemon's startup snapshot
beside a current inspection as `NOT CONFIGURED`, `READY`, `READY WITH WARNING`, or `REFUSED`,
then reports whether the two are synchronized. Debug output includes the UID plus ownership/mode
evidence. This does not prove the script's own logic succeeds; test that separately, as root, with
`/path/to/my-pre-run.sh; echo $?`.

## Caveat: uninterruptible sleep (D state)

A backup child wedged in uninterruptible sleep on a dead mount cannot be killed even with `SIGKILL`, and cannot be waited on either (both are kernel limits). The daemon gives the child its normal timeout, then `SIGTERM`, then `SIGKILL`, then 15 more seconds to actually be collected. If it still has not been, the child is **abandoned** and the daemon takes itself out of the way:

1. Both checks go DOWN, in that order: `proxsave-backup` gets the hang report, then `proxsave-alive` is explicitly failed with the orphan's pid and run id in the body. The service-alive check must never stay green while backups are dead, so this is the one situation in which it reports DOWN for a daemon that is provably running.
2. A marker file, `<BASE_DIR>/daemon_state/.daemon_abandoned.json`, records the abandon so the fact outlives the process.
3. The daemon exits `4` (backup error) and **systemd restarts it** (`Restart=always`, `RestartSec=10`). The restart is what clears out the goroutines and file descriptors stranded behind a child that can never be reaped. Expect the gap to be minutes rather than the nominal ten seconds: the orphan is still in the unit's cgroup, so the stop job sits through its timeout waiting for a cgroup that cannot drain.
4. The restarted daemon reads the marker and keeps `proxsave-alive` DOWN instead of sending its usual heartbeat, so the outage does not look like it recovered ten seconds later. The orphan itself stays in D state; nothing in userspace can clear that.

What lifts the degrade -- and deletes the marker -- is **the orphan being gone**, not a backup succeeding. The daemon re-checks it on every heartbeat and after every completed run, identifying the process by its pid *and* its start time, since the kernel recycles pid numbers. So a mount that comes back recovers within one heartbeat interval; a reboot clears it; and a run that completes while the orphan is still there does **not** clear it, whatever its exit code. Backups demonstrably working and `proxsave-alive` DOWN can therefore coexist, by design: the orphan is still holding a lock nobody can take from it.

The exit code only matters in one fallback, when the marker is too corrupt to name a pid the daemon can check. There is nothing to probe, so the run's own outcome is the only evidence, and only a code that proves the run got *past* the lock counts: `0`, `1` (a clean run with warnings), or a per-phase failure such as a storage or encryption error, all of which are reached after the lock gate. A pre-flight failure -- the directory or disk-space check that the same dead mount fails first -- proves nothing and lifts nothing.

If backups are administratively off (`BACKUP_ENABLED=false`) the marker is kept but the alive check is left alone -- with backups off nothing could ever lift the degrade, and `proxsave-backup` is already down on its own merits.

Your fastest confirmation is `ps -eo pid,stat,wchan,cmd | grep ' D'`; the monitor's server-side `/start` plus grace catches the same run from the other side, and the `FS_IO_TIMEOUT` / `safefs` defenses are the layer below this watchdog.

## Daemon status reference

`--daemon` itself is not an operator command: it is what `proxsave-daemon.service` puts in its `ExecStart`. Running it by hand does not talk to the daemon systemd owns, and it does not become a second one either: an ownership lock (`<BASE_DIR>/daemon_state/.daemon.lock`) makes it warn that another daemon already owns this install and exit `16`.

The daemon status screen exposes the following running and configured state. Command-line status semantics and exit codes are in [CLI_REFERENCE.md](CLI_REFERENCE.md):

```text
Daemon status: <keyword>
Scheduler mode: <cron|daemon>
Daemon service (proxsave-daemon.service): installed | not installed
Service state (systemctl is-active): <active|inactive|...>
Running version: <version> (<commit>)
Binary alignment: aligned | BEHIND (restart needed) | unknown
Running daemon configuration: <backup.env path>
Running daemon loaded at: <start time>
Backup schedule:
  Daemon now: DAILY (<HH:MM>) | WEEKLY (<weekday> at <HH:MM>) | MONTHLY (day <N> at <HH:MM>) | NOT RUNNING | UNAVAILABLE (<why>)
  Configuration: DAILY (...) | WEEKLY (...) | MONTHLY (...) | INVALID (<why>) | UNKNOWN: <why>
  Synchronization: IN SYNC | OUT OF SYNC (<why>) | PENDING (<why>) | NOT APPLICABLE | UNKNOWN (<why>)
Personal pre-run script:
  Daemon now: NOT RUNNING | NOT CONFIGURED | READY | READY WITH WARNING | REFUSED | UNAVAILABLE
  Configuration: NOT CONFIGURED (<why>) | READY | READY WITH WARNING | REFUSED | UNKNOWN
  Synchronization: NOT APPLICABLE | IN SYNC | OUT OF SYNC | PATH STATE CHANGED SINCE STARTUP | UNKNOWN
Personal post-run script:
  Daemon now: NOT RUNNING | NOT CONFIGURED | READY | READY WITH WARNING | REFUSED | UNAVAILABLE
  Configuration: NOT CONFIGURED (<why>) | READY | READY WITH WARNING | REFUSED | UNKNOWN
  Synchronization: NOT APPLICABLE | IN SYNC | OUT OF SYNC | PATH STATE CHANGED SINCE STARTUP | UNKNOWN
```

`Running version:` and `Binary alignment:` appear only when their daemon evidence is available, the two `Running daemon` lines only when the live daemon published its runtime state. The backup schedule and the two personal-script sections always appear.

`Backup schedule` compares the schedule the daemon runs (`Daemon now`, from `daemon_state/.schedule_state.json`) with the one `backup.env` configures now. `OUT OF SYNC (restart the daemon to apply current schedule configuration)` means the file changed after the daemon started. `PENDING (ProxSave switches to weekly automatically; no action needed)` means the daemon is waiting for ProxSave HC Server to confirm the frequency and runs the last confirmed one meanwhile ([Backup schedule](#backup-schedule)); it switches by itself. A file changed after the start reads `OUT OF SYNC` even while a confirmation is pending, because only a restart brings the new value. `INVALID` names the invalid values, the same reasons the daemon's `not applied` block gives. `UNAVAILABLE` on `Daemon now` means the live daemon left no usable schedule state (none, one from an earlier start, or one that cannot be read).

In the personal-script sections, `Daemon now` is the state captured and applied at daemon startup; `Configuration` is what a restart would load and how that path looks now. `NOT CONFIGURED` means the setting was empty, `READY` means the path passed without an advisory, `READY WITH WARNING` means it remains enabled under an explicit administrator trust decision, and `REFUSED` includes the exact refusal reason. If a live daemon has not published matching runtime state, the command says `Running daemon personal-script state: UNAVAILABLE (<why>)` and marks each `Daemon now:` line `UNAVAILABLE (<why>)`; it never turns missing runtime evidence into a false `NOT CONFIGURED` verdict.

On the `Configuration` line only, `NOT CONFIGURED` also says what the file being read right now actually contains, because the verdict alone cannot tell three different files apart:

```text
  Configuration: NOT CONFIGURED (PERSONAL_SCRIPT_PRE_RUN is not in the file)
  Configuration: NOT CONFIGURED (PERSONAL_SCRIPT_PRE_RUN is on line 456 & is empty)
  Configuration: NOT CONFIGURED (PERSONAL_SCRIPT_PRE_RUN is on lines 120 and 456 & line 456 wins and is empty)
```

The third is the last-wins rule described in [CONFIGURATION.md](CONFIGURATION.md#configuration-integrity-check): a value you wrote is being discarded by a later assignment. The `Daemon now` line never carries this note. That daemon read its own copy of the file when it started, so describing today's file as if it explained the daemon's verdict would be a guess.

`UNKNOWN` on `Configuration` means the configuration file itself could not be read, so there is no current verdict to state: it is not `NOT CONFIGURED`, which asserts both settings were empty. The synchronization line then reads `UNKNOWN` as well, and in particular does not ask for a daemon restart on evidence it does not have. Both carry the loader's own error, so the line names the file and the reason it could not be read. `proxsave --daemon-status` exits earlier when the config is unreadable, so this pair is what the dashboard's daemon-status screen shows.

With debug logging, the block also shows the UID used for each decision and every inspected path component's owner and mode. It reads the effective UID from the live daemon's `/proc/<pid>/status` when possible and explicitly reports a fallback to the status command's UID when it cannot. The synchronization verdict distinguishes a changed config source or script path (`OUT OF SYNC`, restart required) from changed ownership, mode, or policy evidence at the same path (`PATH STATE CHANGED SINCE STARTUP`).

This check is read-only: it never executes a personal script, starts a backup, acquires daemon ownership, or sends a healthcheck ping. Neither the schedule nor the script status alters its exit code. It exits `0` **only** when the daemon is running, beating, and aligned; every daemon gap (not installed, not running, stale, running but not reporting, or behind) exits non-zero, so `proxsave --daemon-status` can gate a script. It cannot be combined with `--daemon`, `--daemon-setup`, or `--daemon-remove`.


### From the command line

See [CLI_REFERENCE.md](CLI_REFERENCE.md) for scheduler flags, automation examples and status exit codes.

## Earlier guide entry points

## Install

See the complete [operator procedure](#schedule-and-supervise-backups). Detailed settings and implementation material remain in the reference sections above.

## Operating

See the complete [operator procedure](#schedule-and-supervise-backups). Detailed settings and implementation material remain in the reference sections above.

## What it does

See the complete [operator procedure](#schedule-and-supervise-backups). Detailed settings and implementation material remain in the reference sections above.

## Why

See the complete [operator procedure](#schedule-and-supervise-backups). Detailed settings and implementation material remain in the reference sections above.
