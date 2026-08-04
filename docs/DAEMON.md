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
Opted out of auto-migration (--daemon-remove): yes | no
Running version: <version> (<commit>)
Binary alignment: aligned | BEHIND (restart needed) | unknown
```

The last two lines (`Running version:` and `Binary alignment:`) appear only when a running daemon and its identity record are available; they are omitted when the daemon is not installed or not running. It exits `0` **only** when the daemon is running, beating, and aligned; every gap (not installed, not running, stale, running but not reporting, or behind) exits non-zero, so `proxsave --daemon-status` can gate a script. It cannot be combined with `--daemon`, `--daemon-setup`, or `--daemon-remove`.

## Install

New installs default to the daemon. The install wizard (TUI and `--cli`) asks for the **Scheduler engine** (daemon or cron) just before the **Run at** time. Choosing the daemon installs `proxsave-daemon.service`, removes the cron entry, and turns on centralized monitoring. The wizard then asks for the monitoring mode; see [HEALTHCHECKS.md](HEALTHCHECKS.md).

## Retrofit existing installs

- `--upgrade` **auto-migrates** a cron install to the daemon after updating the binary and config, unless you opted out (see `--daemon-remove`). It is idempotent; a migration failure leaves you on cron.
- `--daemon-setup` switches to the daemon at any time. It installs the service, removes the cron entry, writes `SCHEDULER_MODE=daemon`, `DAEMON_OPT_OUT=false`, and `HEALTHCHECK_ENABLED=true`, then restarts and verifies the daemon.
- `--daemon-remove` reverts to cron, disables the service, and sets `DAEMON_OPT_OUT=true` so later upgrades will **not** reinstall the daemon.

Enabling the daemon forces `HEALTHCHECK_ENABLED=true` even though its raw config default is `false`, so a retrofitted host gets the dead-man switch.

### The run time is inherited, not reset

`SCHEDULER_TIME` only exists since 0.30; on an older install the crontab line was the sole record of the run time. So before the config merge adds the key — and before the migration deletes that cron line — the existing proxsave cron entry is read and its time is written to `SCHEDULER_TIME`. A host running at 21:00 keeps running at 21:00.

- A `SCHEDULER_TIME` you set yourself always wins; the crontab is only consulted when the key is absent or empty.
- Only an unambiguous single daily entry is adopted (`MM HH * * *`, or `@daily`/`@midnight`). A sub-daily or multi-time cron entry (`*/15`, lists, ranges) is something the daemon cannot express: it is **not** guessed at, `SCHEDULER_TIME` stays at `02:00`, and the upgrade warns so you can set it yourself.
- Two proxsave cron lines at different times are equally ambiguous and warn the same way.

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
DAEMON_OPT_OUT=false           # set true by --daemon-remove; upgrade won't re-migrate
BACKUP_ENABLED=true            # false: daemon skips the scheduled run (backup check goes down)

# Monitoring: enabled here, configured in HEALTHCHECKS.md
HEALTHCHECK_ENABLED=false      # forced true by --daemon-setup / auto-migration
```

The `HEALTHCHECK_*` keys that decide *where* and *what* the daemon reports live in [HEALTHCHECKS.md](HEALTHCHECKS.md).

See [CONFIGURATION.md](CONFIGURATION.md) for the full variable reference and [CLI_REFERENCE.md](CLI_REFERENCE.md) for the `--daemon-*` flags.

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
