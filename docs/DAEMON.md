# Resident daemon + healthchecks monitoring

ProxSave can run as a **resident daemon** instead of a one-shot `cron` job. The
daemon schedules and supervises each backup, adds a hang watchdog, and reports two
signals to a [healthchecks](https://healthchecks.io/) monitor so silent failures
(a crash before notifying, a run that hangs, a host that is simply down) are
caught by an external dead-man switch.

## Why

A pure "notify only on failure" model is blind to the worst failures: the process
cannot speak when it panics, is OOM-killed, hangs, or never starts. The daemon
solves this by pushing to an **external** monitor that alarms on *silence*.

## What it does

- **Schedules** the daily backup itself (replacing the crontab entry) at the
  `SCHEDULER_TIME` ("Run at") time.
- **Supervises** each run as a child process under a `MAX_RUN_DURATION` timeout. A
  run that overruns is sent `SIGTERM`, then `SIGKILL`, and reported as a **hang**.
- **Reports** two independent checks:
  - `proxsave-alive` - a heartbeat every `HEALTHCHECK_HEARTBEAT_INTERVAL`; if the
    daemon dies or the host goes down, this check goes down.
  - `proxsave-backup` - per run: `/start`, then the exit status (`/0` success,
    `/1` warning **alerts**, `/<code>` on error with a log tail), or `/fail` on a
    hang.

systemd (`proxsave-daemon.service`, `Restart=always`) is only the keep-alive
supervisor; the daemon schedules internally.

## Two modes

- **centralized** (default): the daemon fetches its two ping URLs from the
  ProxSave server (`GET /api/healthcheck/config`), reusing the SAME identity it
  already uses for Telegram notifications (`server_id` + relay secret). No manual
  setup, no API key on the client. Requires the client to have been paired on
  Telegram (that is where the relay secret comes from). The portal login link is
  delivered separately on the notification responses.
- **self**: point the daemon at your own healthchecks (self-hosted or SaaS) by
  setting `HEALTHCHECK_PING_ENDPOINT` (+ optional `HEALTHCHECK_PING_KEY`) and the
  two check IDs `HEALTHCHECK_ALIVE_ID` / `HEALTHCHECK_BACKUP_ID`.

A client without a relay secret still gets the daemon (scheduling + hang
watchdog); the healthcheck reporting stays off until `self` mode is configured.

## Install

New installs default to the daemon. The install wizard (TUI and `--cli`) asks for
the **Scheduler engine** (daemon or cron) just before the **Run at** time, in the
same screen as the storage/notification settings. Choosing the daemon installs
`proxsave-daemon.service`, removes the cron entry, and enables centralized
healthchecks.

## Retrofit existing installs

- `--upgrade` **auto-migrates** a cron install to the daemon after updating the
  binary and config (unless you opted out - see below). Idempotent; a migration
  failure leaves you on cron.
- `--daemon-setup` switches to the daemon at any time.
- `--daemon-remove` reverts to cron, disables the service, and sets
  `DAEMON_OPT_OUT=true` so later upgrades will **not** reinstall the daemon.

## Configuration keys (`backup.env`)

```
SCHEDULER_MODE=cron            # cron | daemon
SCHEDULER_TIME=02:00           # daily HH:MM ("Run at"), daemon mode
MAX_RUN_DURATION=6h            # watchdog hard timeout for one backup
DAEMON_OPT_OUT=false           # set true by --daemon-remove; upgrade won't re-migrate

HEALTHCHECK_ENABLED=false
HEALTHCHECK_MODE=centralized   # centralized (fetch from server) | self
HEALTHCHECK_HEARTBEAT_INTERVAL=5m
HEALTHCHECK_SEND_LOG=true
HEALTHCHECK_ALIVE_URL=         # centralized cache (auto-filled; do not edit)
HEALTHCHECK_BACKUP_URL=        # centralized cache (auto-filled; do not edit)
HEALTHCHECK_PING_ENDPOINT=https://hc-ping.com   # self mode
HEALTHCHECK_PING_KEY=          # self mode
HEALTHCHECK_ALIVE_ID=          # self mode
HEALTHCHECK_BACKUP_ID=         # self mode
```

## Operating

```bash
systemctl status proxsave-daemon.service      # is it running?
journalctl -u proxsave-daemon.service -f      # follow its log
proxsave --daemon-remove                       # back to cron
proxsave --daemon-setup                        # back to the daemon
```

## Caveat: uninterruptible sleep (D state)

A backup child wedged in uninterruptible sleep on a dead mount cannot be killed
even with `SIGKILL` (a kernel limit). The daemon still **reports the hang and
moves on**, and the monitor's server-side `/start` + grace catches it too; the
`FS_IO_TIMEOUT` / `safefs` defenses are the layer below this watchdog.
