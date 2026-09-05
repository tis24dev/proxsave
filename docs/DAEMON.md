# Resident daemon

ProxSave can run as a **resident daemon** instead of a one-shot `cron` job. The daemon schedules and supervises each backup, adds a hang watchdog, and reports to an external monitor so silent failures (a crash before notifying, a run that hangs, a host that is simply down) are caught by an external dead-man switch.

This document covers the daemon itself. The monitoring side, which checks exist, what they cover, and how to configure the centralized or self-hosted monitor, is in [HEALTHCHECKS.md](HEALTHCHECKS.md).

## Why

A pure "notify only on failure" model is blind to the worst failures: the process cannot speak when it panics, is OOM-killed, hangs, or never starts. The daemon pushes to an **external** monitor that alarms on *silence*, so those cases are still caught.

## What it does

- **Schedules** the daily backup itself (replacing the crontab entry) at the `SCHEDULER_TIME` ("Run at") time. There is **no catch-up**: the next run is always computed forward from now, so a window missed because the host was off or the service was down is skipped rather than made up. That shows on the monitor as `proxsave-backup` going down, which is the intended signal.
- **Supervises** each run as a child process (`proxsave --backup`) under a `MAX_RUN_DURATION` timeout. A run that overruns gets `SIGTERM`, then `SIGKILL` after a 30-second grace, and is reported as a **hang**.
- **Reports** four families of monitored checks: daemon liveness, the backup outcome, release updates, and one per notification channel. See [HEALTHCHECKS.md](HEALTHCHECKS.md).

systemd (`proxsave-daemon.service`, `Restart=always`) is only the keep-alive supervisor; the daemon schedules internally.

### BACKUP_ENABLED=false

When `BACKUP_ENABLED=false`, the daemon skips the scheduled run entirely: no child process, no backup-outcome ping, so `proxsave-backup` honestly goes down (no false green). The liveness heartbeat keeps signalling, so you can still tell the daemon is up.

## Standalone backups: the SIGUSR1 handoff

A backup run outside the daemon (by hand, or the dashboard "run now") does not ping the monitor itself. The resident daemon is the sole pinger. Instead, a standalone run drops a handoff file (`.manual_backup_outcome.json`) and wakes the daemon with `SIGUSR1`; the daemon then pings the backup check with that run's outcome. A handoff older than 15 minutes is dropped without pinging (so a long-past run never flips the check), and if no live daemon is found nothing pings.

## Binary alignment after an upgrade

An in-place `--upgrade` replaces the on-disk binary without restarting the resident daemon, so the daemon can keep running the **old** code (systemd keeps the old process alive). ProxSave detects this hash-free: Linux blocks overwriting a running executable, so an upgrade unlinks it and `/proc/<pid>/exe` ends in `" (deleted)"`, which alone proves the daemon is behind.

`--upgrade` and the dashboard reconcile this with a restart-and-verify: they wait (bounded, up to 4 minutes) for any in-progress daemon-supervised backup to finish (deferring the restart, never killing the backup), restart the service, then poll until the daemon is back, aligned, and freshly started. `--daemon-status` reports the same `behind - restart needed` verdict.

`--daemon-setup` does **not** do that. It restarts the unit immediately and then only polls until the daemon is alive with a readable alignment, accepting one that is still behind. If a daemon-supervised backup is running at that moment it is cancelled: the child gets SIGTERM, and SIGKILL if it does not stop within 30 seconds, and the run ends with no outcome ping, so the monitor sees a start that never finished. `--daemon-status` will not warn you: it reports liveness, unit state and binary alignment, never whether a backup is running. Check for the lock file under `LOCK_PATH`, or just run `--daemon-setup` outside the backup window.

## Operating

```bash
proxsave --daemon-status                       # read-only status + exit code (see below)
proxsave --daemon-status --log-level debug     # include script-path ownership/mode evidence
systemctl status proxsave-daemon.service       # is it running?
journalctl -u proxsave-daemon.service -f       # follow its log
proxsave --daemon-setup                        # switch to the daemon
proxsave --daemon-remove                       # revert to cron
```

`proxsave --daemon-status` prints a combined verdict and is meant for scripts:

```text
Daemon status: <keyword>
Scheduler mode: <cron|daemon>
Daemon service (proxsave-daemon.service): installed | not installed
Service state (systemctl is-active): <active|inactive|...>
Running version: <version> (<commit>)
Binary alignment: aligned | BEHIND (restart needed) | unknown
Personal pre-run script:
  Running daemon: NOT CONFIGURED | READY | READY WITH WARNING | REFUSED
  Current configuration: NOT CONFIGURED | READY | READY WITH WARNING | REFUSED
  Synchronization: IN SYNC | OUT OF SYNC | PATH STATE CHANGED SINCE STARTUP | UNKNOWN
Personal post-run script:
  Running daemon: NOT CONFIGURED | READY | READY WITH WARNING | REFUSED
  Current configuration: NOT CONFIGURED | READY | READY WITH WARNING | REFUSED
  Synchronization: IN SYNC | OUT OF SYNC | PATH STATE CHANGED SINCE STARTUP | UNKNOWN
```

`Running version:` and `Binary alignment:` appear only when their daemon evidence is available. The two personal-script sections always appear. `Running daemon` is the state captured and applied at daemon startup; `Current configuration` is what a restart would load and how that path looks now. `NOT CONFIGURED` means the setting was empty, `READY` means the path passed without an advisory, `READY WITH WARNING` means it remains enabled under an explicit administrator trust decision, and `REFUSED` includes the exact refusal reason. If a live daemon has not published matching runtime state, the command says `RUNNING DAEMON STATE UNAVAILABLE`; it never turns missing runtime evidence into a false `NOT CONFIGURED` verdict.

With debug logging, the block also shows the UID used for each decision and every inspected path component's owner and mode. It reads the effective UID from the live daemon's `/proc/<pid>/status` when possible and explicitly reports a fallback to the status command's UID when it cannot. The synchronization verdict distinguishes a changed config source or script path (`OUT OF SYNC`, restart required) from changed ownership, mode, or policy evidence at the same path (`PATH STATE CHANGED SINCE STARTUP`).

This check is read-only: it never executes a personal script, starts a backup, acquires daemon ownership, or sends a healthcheck ping. Script status does not alter its exit code. It exits `0` **only** when the daemon is running, beating, and aligned; every daemon gap (not installed, not running, stale, running but not reporting, or behind) exits non-zero, so `proxsave --daemon-status` can gate a script. It cannot be combined with `--daemon`, `--daemon-setup`, or `--daemon-remove`.

## Install

New installs default to the daemon. The install wizard (TUI and `--cli`) asks for the **Scheduler engine** (daemon or cron) just before the **Run at** time. Choosing the daemon installs `proxsave-daemon.service`, removes the proxsave cron entry ([what that means exactly](#what-removes-the-cron-entry-means)), and turns on centralized monitoring. The wizard then asks for the monitoring mode; see [HEALTHCHECKS.md](HEALTHCHECKS.md).

## Retrofit existing installs

- `--upgrade` **auto-migrates** to the daemon only on a host that has never recorded a scheduler engine, i.e. one where that same upgrade's config merge had to add `SCHEDULER_MODE`. Once the key is in the file the value is honoured and no upgrade revisits the host: `cron` means cron, whether you chose it in the wizard, edited it by hand or reached it with `--daemon-remove`. On the host it does migrate it **refuses**, and changes nothing at all, if the crontab still schedules ProxSave through an entry ProxSave does not own ([below](#what-removes-the-cron-entry-means)): installing the daemon on top of one would run every backup twice. `--daemon-setup` installs it anyway and reports what it found.

  Note that on the ordinary download path this decision is made by the binary being *replaced*, because `--upgrade` never re-execs; only the `backup.env` merge runs under the new binary. A change to this rule therefore takes effect one release later.
- `--daemon-setup` switches to the daemon at any time. It installs the service, removes the proxsave cron entry ([what that means exactly](#what-removes-the-cron-entry-means)), writes `SCHEDULER_MODE=daemon` and `HEALTHCHECK_ENABLED=true`, then restarts and verifies the daemon. It reports how many cron lines it actually removed, or that it found none, and it **warns and proceeds** (rather than refusing) if an entry it does not own survives, because you asked for the daemon explicitly.
- `--daemon-remove` reverts to cron and disables the service. It records `SCHEDULER_MODE=cron`, and that record is what stops later upgrades reinstalling the daemon: the key is present, so it is honoured. It **always** writes a canonical cron line at `SCHEDULER_TIME`, and when the host also schedules ProxSave through an entry ProxSave does not own it says so and leaves that entry alone.

  It used to withhold the line in that case, to avoid a second nightly backup. That was the wrong side of the trade. `--daemon-setup` deletes every proxsave cron line on the way in, so a host arriving at a revert has none, and one misidentified entry left it with no daemon and no cron line, at exit `0`, with nothing able to notice: the run that would have noticed is the backup that was never scheduled. The detector answers "is this named after proxsave", not "does this run a proxsave backup" ([below](#what-removes-the-cron-entry-means)), so misidentification is not exotic. A host that really does carry both now runs the backup twice, and the run that loses the per-run lock exits `16` where you can see it.

Enabling the daemon sets `HEALTHCHECK_ENABLED=true` even though its raw config default is `false`, so a retrofitted host gets the dead-man switch. `--daemon-remove` sets it back to `false`. That symmetry matters: the checks it turns on are daemon-only, so a host left on cron with `HEALTHCHECK_ENABLED=true` warned `Healthchecks: daemon not installed` on every otherwise successful run and exited `1` for a daemon it was never meant to have. The rollback is what fixes that, by clearing the key the operator never chose. A cron host that still carries `HEALTHCHECK_ENABLED=true` on purpose is warned about it, and that warning still costs the exit code: monitoring cannot work without the daemon, and the key says the operator wants monitoring.

A host that reverted with an **older** build still carries that stale `true`, and nothing it runs rewrites the key for it, so it warns and exits `1` on every otherwise successful backup. ProxSave does not repair it: on disk that host is indistinguishable from one whose operator set the key on purpose, and rewriting a monitoring setting on evidence that cannot tell the two apart takes away a choice instead of tidying a leftover.

Two ways out, both yours to choose: set `HEALTHCHECK_ENABLED=false` in `backup.env`, or run `--daemon-setup` followed by `--daemon-remove`, which writes the key back to `false` on the way out. Hosts reverted from this release on are unaffected, and a cron host that never enabled the daemon carries the template default `false`.

### What "removes the cron entry" means

Precisely: **every cron line whose command is named `proxsave` or `proxmox-backup`** is deleted, matched on the command's basename, not only the line ProxSave wrote itself and not only the canonical `/usr/local/bin/proxsave` path. The rest of the line is never looked at, so a job that merely *mentions* the binary (`cp /usr/local/bin/proxsave /backup/`) is left alone, and so is `proxmox-backup-client`.

The consequence is the case that matters: **a wrapper is not a proxsave cron entry.** If your crontab runs the backup through a script of your own,

```cron
30 02 * * * /usr/local/sbin/proxsave-nas-guard
```

then its command is named `proxsave-nas-guard`, the rule above does not recognise it, and the daemon migration can neither remove it, nor adopt its run time into `SCHEDULER_TIME` (see below), nor count it as a proxsave schedule.

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

Files under `/etc` are **read only**. Everything that deletes a cron line, and the `SCHEDULER_TIME` adoption below, still work on the root crontab alone: ProxSave writes the crontab it owns and never edits a file it did not place. Entries whose name `cron` itself ignores (anything outside `A-Z a-z 0-9 _ -`, such as `proxsave.bak`) are skipped, because a schedule that never fires cannot collide with anything.

Because of that, the messages state what actually happened instead of asserting a removal:

```text
Daemon mode enabled: proxsave-daemon.service is active. The cron entry was removed.
Daemon mode enabled: proxsave-daemon.service is active. 2 proxsave cron entries were removed.
Daemon mode enabled: proxsave-daemon.service is active. No proxsave cron entry was present to remove.
Daemon mode enabled: proxsave-daemon.service is active. The crontab could not be checked, so a proxsave cron entry may still be scheduled alongside it.
```

The third line is normal on a fresh daemon install, which never had a cron entry. On a host that **was** on cron it is the signal to look: something whose command is not named `proxsave` was scheduling the backup, and it is still scheduled. Either delete that entry and let the daemon schedule the run (moving whatever the wrapper checked elsewhere), or run `proxsave --daemon-remove`, which records `SCHEDULER_MODE=cron` so upgrades leave the host alone. Note that the revert writes its own cron line as well, so a host that keeps the wrapper ends with two: it reports both and leaves the choice to you.

### The run time is inherited, not reset

`SCHEDULER_TIME` only exists since 0.30; on an older install the crontab line was the sole record of the run time. So before the config merge adds the key — and before the migration deletes that cron line — the existing proxsave cron entry is read and its time is written to `SCHEDULER_TIME`. A host running at 21:00 keeps running at 21:00.

`--daemon-setup` and the dashboard's install action do the same, and there the key is **overwritten** rather than seeded. On a cron host the crontab is the schedule and `SCHEDULER_TIME` is a leftover nothing keeps in step, so a cron line edited to 21:00 would otherwise hand the daemon whatever hour the key still held.

- A `SCHEDULER_TIME` you set yourself always wins; the crontab is only consulted when the key is absent or empty.
- Only an unambiguous single daily entry is adopted (`MM HH * * *`, or `@daily`/`@midnight`). A sub-daily or multi-time cron entry (`*/15`, lists, ranges) is something the daemon cannot express: it is **not** guessed at, `SCHEDULER_TIME` stays at `02:00`, and the upgrade warns so you can set it yourself.
- Two proxsave cron lines at different times are equally ambiguous and warn the same way.
- Only the **root crontab** is inherited from, because that is the table ProxSave owns and is about to rewrite: taking its time is continuity, since the line it came from is the line being replaced.
- A proxsave entry under `/etc/crontab` or `/etc/cron.d` is **reported and never adopted**. ProxSave does not edit files it did not place, so that entry survives the install; copying its hour into `SCHEDULER_TIME` would put the line ProxSave writes in the exact minute the surviving one already occupies, and the two runs would meet on the per-run lock with one exiting `16` every night. Left alone the host keeps its `/etc` entry and gains ProxSave's at `02:00`: still two backups, both of which succeed. The note names the file and the hour so you can settle it.
- Under `/etc` only a **direct** `proxsave` command is reported this way, not the heuristics the advisories use. Nothing there opens a script to look inside.
- A wrapper entry is not adopted anywhere, so `SCHEDULER_TIME` stays at `02:00`, the very minute the wrapper is likely already using. ProxSave says so ("No proxsave cron entry was found, but … appears to run ProxSave") instead of staying silent, but it will not adopt a run time out of a script it did not write: set `SCHEDULER_TIME` yourself.

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

The daemon coordinates through six small files under `<BASE_DIR>/identity/`, all written atomically (temp file then rename, mode `0600`) and deliberately not made immutable so they can be rewritten:

| File | Purpose |
|------|---------|
| `.daemon.pid` | the daemon's PID; the contract a standalone backup reads to send `SIGUSR1` |
| `.daemon_info.json` | the running daemon's identity (pid, exec path, version, commit, start time), for display and the restart-verify freshness check |
| `.healthcheck_status.json` | the last ping outcome per check, read back by the run phase to report real transmission; a corrupt file is quarantined to `.corrupt` and reset |
| `.notify_results.json` | the backup child's per-channel notification severities, handed to the daemon to drive the `proxsave-notify-*` pings |
| `.manual_backup_outcome.json` | a standalone run's outcome, handed off for the daemon to ping |
| `.daemon_abandoned.json` | a backup child the kernel would not let the daemon reap: the orphan's pid and start time, the run id, and when it happened. See [the D-state caveat](#caveat-uninterruptible-sleep-d-state) |

`.daemon.pid` and `.daemon_info.json` are written at startup and removed on shutdown.
`.daemon_abandoned.json` is the exception that deliberately **survives** shutdown — that is its whole purpose — and is removed only once something shows backups can run again.

## Configuration keys (`backup.env`)

```ini
# Scheduler engine
SCHEDULER_MODE=cron            # cron | daemon
SCHEDULER_TIME=02:00           # daily HH:MM ("Run at")
MAX_RUN_DURATION=1h            # watchdog hard timeout for one backup
BACKUP_ENABLED=true            # false: daemon skips the scheduled run (backup check goes down)

# Personal scripts: your own, started around each run (daemon only)
PERSONAL_SCRIPT_PRE_RUN=       # path to a script started before the run (works only with the daemon)
PERSONAL_SCRIPT_POST_RUN=      # path to a script started after the run, whatever the outcome (works only with the daemon)

# Monitoring: enabled here, configured in HEALTHCHECKS.md
HEALTHCHECK_ENABLED=false      # forced true by --daemon-setup / auto-migration
```

The `HEALTHCHECK_*` keys that decide *where* and *what* the daemon reports live in [HEALTHCHECKS.md](HEALTHCHECKS.md).

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference and [CLI_REFERENCE.md](CLI_REFERENCE.md) for the `--daemon-*` flags.

## Personal scripts around a run

`PERSONAL_SCRIPT_PRE_RUN` and `PERSONAL_SCRIPT_POST_RUN` name two scripts of your own that
the daemon starts around each supervised run: the first before the backup child, the second
after it. Both are empty by default, which is the shipped state and means nothing is started.

They are **yours, not ProxSave's**, and the whole contract follows from that:

- **Daemon runs only.** A manual `proxsave --backup`, a cron-mode run, and a dashboard run
  start neither script. Only the run the daemon schedules and supervises is bracketed.
- **Nothing is reported about their execution.** Their standard output and standard error go to
  `/dev/null`. Nothing they print or fail at appears in the run log, in the log file, in the
  run recap, in an email, Telegram, Gotify or webhook notification, in a healthchecks ping, or
  in the Prometheus metrics. If you want a record of what your script did, your script writes
  it.
- **Their exit code is ignored.** A script that fails or goes missing at run time changes
  nothing: the backup runs anyway, the run's own exit code is unaffected, and no warning is
  counted in the run. (A path that is not executable never gets that far: the trusted-path
  gate below refuses it at daemon start.) `PERSONAL_SCRIPT_POST_RUN` is started after **every** outcome: success,
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
`proxsave --daemon-status --log-level debug`. It reports the running daemon's startup snapshot
beside a current inspection as `NOT CONFIGURED`, `READY`, `READY WITH WARNING`, or `REFUSED`,
then reports whether the two are synchronized. Debug output includes the UID plus ownership/mode
evidence. This does not prove the script's own logic succeeds; test that separately, as root, with
`/path/to/my-pre-run.sh; echo $?`.

## Caveat: uninterruptible sleep (D state)

A backup child wedged in uninterruptible sleep on a dead mount cannot be killed even with `SIGKILL`, and cannot be waited on either (both are kernel limits). The daemon gives the child its normal timeout, then `SIGTERM`, then `SIGKILL`, then 15 more seconds to actually be collected. If it still has not been, the child is **abandoned** and the daemon takes itself out of the way:

1. Both checks go DOWN, in that order: `proxsave-backup` gets the hang report, then `proxsave-alive` is explicitly failed with the orphan's pid and run id in the body. The service-alive check must never stay green while backups are dead, so this is the one situation in which it reports DOWN for a daemon that is provably running.
2. A marker file, `<BASE_DIR>/identity/.daemon_abandoned.json`, records the abandon so the fact outlives the process.
3. The daemon exits `4` (backup error) and **systemd restarts it** (`Restart=always`, `RestartSec=10`). The restart is what clears out the goroutines and file descriptors stranded behind a child that can never be reaped. Expect the gap to be minutes rather than the nominal ten seconds: the orphan is still in the unit's cgroup, so the stop job sits through its timeout waiting for a cgroup that cannot drain.
4. The restarted daemon reads the marker and keeps `proxsave-alive` DOWN instead of sending its usual heartbeat, so the outage does not look like it recovered ten seconds later. The orphan itself stays in D state; nothing in userspace can clear that.

What lifts the degrade -- and deletes the marker -- is **the orphan being gone**, not a backup succeeding. The daemon re-checks it on every heartbeat and after every completed run, identifying the process by its pid *and* its start time, since the kernel recycles pid numbers. So a mount that comes back recovers within one heartbeat interval; a reboot clears it; and a run that completes while the orphan is still there does **not** clear it, whatever its exit code. Backups demonstrably working and `proxsave-alive` DOWN can therefore coexist, by design: the orphan is still holding a lock nobody can take from it.

The exit code only matters in one fallback, when the marker is too corrupt to name a pid the daemon can check. There is nothing to probe, so the run's own outcome is the only evidence, and only a code that proves the run got *past* the lock counts: `0`, `1` (a clean run with warnings), or a per-phase failure such as a storage or encryption error, all of which are reached after the lock gate. A pre-flight failure -- the directory or disk-space check that the same dead mount fails first -- proves nothing and lifts nothing.

If backups are administratively off (`BACKUP_ENABLED=false`) the marker is kept but the alive check is left alone -- with backups off nothing could ever lift the degrade, and `proxsave-backup` is already down on its own merits.

Your fastest confirmation is `ps -eo pid,stat,wchan,cmd | grep ' D'`; the monitor's server-side `/start` plus grace catches the same run from the other side, and the `FS_IO_TIMEOUT` / `safefs` defenses are the layer below this watchdog.
