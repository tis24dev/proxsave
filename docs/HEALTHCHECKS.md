# Backup monitoring (healthchecks)

ProxSave reports every backup outcome, plus a liveness heartbeat, to an external
[healthchecks](https://healthchecks.io/) monitor. The monitor alarms on **silence**, so
the failures ProxSave cannot report itself are still caught: a crash before the
notification phase, an OOM kill, a run wedged on a dead mount, a host that never came
back from a reboot.

Monitoring is driven by the resident daemon, which is the only thing that pings. A host
still on the cron scheduler reports nothing, no matter how the keys below are set. See
[DAEMON.md](DAEMON.md) for the scheduler engines and how to switch.

## Why silence is the signal

A "notify me when a backup fails" setup is blind to the worst failures, because a
process that panics, hangs, or never starts cannot send you anything. Every channel in
[NOTIFICATIONS.md](NOTIFICATIONS.md) has that blind spot by construction.

The daemon closes it by pushing to something outside this host. Each check has an
expected cadence on the monitor side. Miss it and the check goes down and your alerts
fire, whether ProxSave was able to say anything or not.

## What gets monitored

The daemon reports four families of checks, each shown on the monitor as a
`proxsave-*` check.

| Check | When it pings | What it covers |
|-------|---------------|----------------|
| `proxsave-alive` | immediately at daemon start, then every `HEALTHCHECK_HEARTBEAT_INTERVAL` | the daemon and the host are up. Stops when either dies, and the monitor alarms on the silence |
| `proxsave-backup` | per run: `/start` at launch, then the run's exit code, or `/fail` on a hang | whether the backup ran and how it ended |
| `proxsave-updates` | immediately at daemon start, then every `HEALTHCHECK_UPDATE_INTERVAL` | `/0` when up to date, `/1` when a newer release exists, so the check goes down and tells you to upgrade |
| `proxsave-notify-<channel>` | after each run, one per channel the backup attempted | whether that notification channel actually delivered |

### Ping details

The exact wire behavior, in case you are reading the monitor's event log:

- Every ping is a POST, bounded at 10 seconds. A slow or down monitor never stalls the
  daemon and never delays a backup.
- The run start ping carries `?rid=<uuid>`, a fresh run id, so the monitor can pair it
  with the finish ping and measure the run's duration.
- The finish ping is `/` plus the run's exit status, clamped into 0..255. There is no
  separate "warning" suffix: exit `1` pings `/1`, and a start failure or an external
  kill reports a non-zero code the same way. `/0` is the only green outcome.
- A hang pings `/fail` with a `timed out after <duration>` body, because a killed child
  has no exit code to report.
- With `HEALTHCHECK_SEND_LOG=true`, a log tail rides along as the request body on a
  **supervised** run that failed or hung. The daemon keeps the last 8 KiB of the run's
  output for this, and the ping body is hard-capped at 100 kB regardless. A standalone
  run (see below) hands off only its exit code, so its finish ping carries no log tail.
- The updates check only pings `/0` on a definite up-to-date answer. An inconclusive
  check, for instance GitHub unreachable or rate limited, re-affirms the previous
  verdict instead of flapping a real `/1` back to green, and pings nothing at all when
  there is no previous verdict to re-affirm.
- The per-channel checks are driven by what the backup actually attempted, recorded per
  run, and not by cached configuration. A channel you turn off does not leave a stale
  down check behind. There is one check per enabled channel among email, telegram,
  gotify, and webhook. In centralized mode the daemon tells the server which channels
  are enabled, so it provisions exactly those.
- Ping URLs embed the check identifier, which is a low-capability secret. ProxSave
  registers them with the log masker and strips them out of transport errors, so a
  failed ping logs the reason and never the URL.

### When the daemon has no backup to report

With `BACKUP_ENABLED=false` the daemon skips the scheduled run entirely: no child
process and no outcome ping, so `proxsave-backup` honestly goes down rather than
reporting a false green. The heartbeat keeps signalling, so you can still tell the
daemon itself is healthy.

### Backups run outside the daemon

A backup started by hand, or from the dashboard's "run now", does not ping the monitor
itself. The resident daemon is the only pinger. A standalone run instead drops a handoff
file and wakes the daemon with `SIGUSR1`, and the daemon pings `proxsave-backup` with
that outcome. A handoff older than 15 minutes is discarded without pinging, so a
long-past run never flips the check, and if no live daemon is found nothing pings at
all.

## Two modes

`HEALTHCHECK_MODE` picks where the pings go.

- **`centralized`** (the default): ProxSave runs the monitor for you and provisions
  this host's checks. Nothing to set up, no API key on this machine.
- **`self`**: you point the daemon at your own healthchecks instance, self-hosted or
  the SaaS, and own the checks yourself.

## Centralized: the ProxSave monitoring server

This host is identified to the monitoring server by its **Server ID**, which is
generated at install time, and a per-server relay credential. The credential is
**provisioned automatically**: the daemon asks the server for one on its first run and
persists it. No Telegram pairing, no account, no key to copy. It is the same identity
the centralized Telegram relay uses, so a host that already sends Telegram
notifications is already provisioned.

Once provisioned, the daemon fetches its ping URLs from the server and starts
reporting. If a later fetch fails, it falls back to the URLs cached in `backup.env`
(`HEALTHCHECK_ALIVE_URL` and `HEALTHCHECK_BACKUP_URL`), so a transient server outage
does not stop reporting. It warns once and then keeps retrying quietly.

The credential also self-heals. If the server ever rejects it, or reports that this
host's account was parked for being unused, the daemon clears the stale credential and
provisions a fresh one on its next attempt, which re-registers the host. Transient
errors never touch a working credential. Provisioning retries are throttled to roughly
every 15 minutes, spread out per host, and the daemon honors a server-side back-off
when it is asked to wait.

### Your monitoring portal

Every centralized host gets its own portal on the monitoring server, where you can see
each check's state and history and decide how you want to be alerted.

You reach it through a **single-use login link**, valid for about an hour, which
ProxSave shows you in three places:

- during the install wizard, on the monitoring screen, in both the TUI and `--cli`;
- in the dashboard, under `Healthchecks` in the diagnostic checks;
- at the end of a backup run, on the `Healthchecks Portal:` line.

Open it and **set a password**. That is what turns the link into an account you can log
into later, and it is the point at which you can configure alert channels, email and
the rest, so the monitor can reach you when a check goes down.

Two things worth knowing:

- The server stops minting new links after your first login, since from then on you
  have a password. Before that first login, every place listed above hands you a fresh
  one, so a link that expired is never a problem: just open the dashboard check again.
- ProxSave never opens or follows the link, it only prints it. It also refuses to print
  anything that is not a clean http(s) URL on the monitoring server's own domain, so a
  tampered response cannot put a phishing address in front of you.

## Self mode: your own healthchecks

In self mode ProxSave pings the URLs you give it and does nothing else. There is no
identity, no portal, and no provisioning: the checks, the alert rules, and the
retention are yours to manage on your own instance.

### During install

Choosing `Your own server` on the monitoring step opens a form that collects the full
ping URL of each check, for example `https://hc-ping.com/<uuid>`. The alive and backup
URLs are required; the updates URL and the four per-channel notification URLs are
optional. Whatever you leave empty simply is not reported. A verification screen then
pings your alive URL to confirm it is reachable from this host, using a state-neutral
ping that does not leave a spurious success or failure on your check.

### In backup.env

You can also configure it by hand. Each check accepts either a full URL or an
identifier that gets assembled onto a shared endpoint:

```bash
HEALTHCHECK_PING_ENDPOINT=https://hc-ping.com   # base for the *_ID form
HEALTHCHECK_PING_KEY=                           # optional, inserted between base and id
HEALTHCHECK_ALIVE_ID=
HEALTHCHECK_BACKUP_ID=
```

A full `*_URL` always wins over the matching `*_ID`. With a ping key set, an id
resolves to `<endpoint>/<key>/<id>`; without one, to `<endpoint>/<id>`.

Self mode covers all four families: `*_ALIVE_*`, `*_BACKUP_*`, `*_UPDATES_*`, and
`*_NOTIFY_<CHANNEL>_*` for email, telegram, gotify, and webhook. Configure only the
checks you actually want. The alive and backup pair alone is a perfectly reasonable
setup.

Note that `HEALTHCHECK_ALIVE_URL` and `HEALTHCHECK_BACKUP_URL` do double duty: in self
mode they are your own ping URLs, and in centralized mode they are the cache the server
fills in for you. In centralized mode, do not edit them.

## Where monitoring shows up

**Install wizard.** With the daemon engine selected, step 8 asks for the monitoring
mode, then a screen verifies the connection. In centralized mode it also boxes the
portal link. `--cli` installs show the same information as plain text.

**Dashboard.** `Healthchecks` under the diagnostic checks runs on entry and reports the
real operational state, not just one-shot reachability. In centralized mode it boxes a
fresh portal link. Under the verdict, a `Sensors:` list gives one colored line per
monitored check with its state and the age of its last ping.

**End of a backup run.** The run prints a `Healthchecks` line reporting whether the
daemon is actually transmitting. This section sends nothing itself: the daemon is the
only pinger, and it records every ping outcome to disk, so the run reads that record
and reports what was really sent. A missing record reads as "nothing transmitted yet",
which is honest for a first run or a stopped daemon, and never as a false success.

**`proxsave --daemon-status`.** A scriptable verdict on the daemon itself, covered in
[DAEMON.md](DAEMON.md).

## Troubleshooting

### What the check screen tells you

The install screen and the dashboard check share one vocabulary. `WORKING` is the only
fully healthy centralized state; in self mode it is `REACHABLE`.

| Keyword | What it means | What to do |
|---------|---------------|------------|
| `WORKING` | daemon running and reporting | nothing |
| `REACHABLE` | self mode: your ping URL answered | nothing |
| `PROVISIONING` | the credential or the server-side setup is not ready yet | check again shortly; if it persists, this host cannot reach the monitoring server |
| `UNREACHABLE` | the monitor did not answer from this host | check outbound connectivity and DNS |
| `UNCONFIRMED` | provisioned, but reachability could not be confirmed | run the check again |
| `NOT INSTALLED` | the monitor is reachable but the daemon service is not installed | `proxsave --daemon-setup` |
| `NOT RUNNING` | the service is installed and stopped, or never wrote a heartbeat | `systemctl start proxsave-daemon.service` |
| `RUNNING, NOT REPORTING` | the process is up but has written no heartbeat yet | usually a stale build; restart the service |
| `STALE` | the last heartbeat is older than twice the heartbeat interval, with a one-minute floor so a single slipped tick does not read as down | the daemon is stopped or wedged; check `journalctl -u proxsave-daemon.service` |
| `BEHIND` | the running daemon is on an older binary than the one on disk | restart the service so it loads the upgrade |
| `NOT PROVISIONED` | the daemon runs but has no ping target yet | centralized: wait for provisioning. Self: the ping URLs are missing |
| `MONITOR UNREACHABLE` | the daemon runs but its pings do not arrive | outbound connectivity from this host |
| `TRANSMIT FAILED` | the last backup outcome did not reach the monitor | usually transient; check the daemon log |
| `REJECTED` | the server refused this host's credential | it is cleared and re-provisioned automatically on the next daemon run |
| `NOT REGISTERED` | the server does not know this host yet | registered automatically on the next daemon run |
| `PARKED` | the server had removed this host's unused account | cleared and re-registered automatically |
| `DISABLED` | centralized monitoring is turned off on the server | nothing to configure here |
| `NO IDENTITY` | this host has no server identity | re-run the installer to regenerate it |
| `NOT ENABLED` | monitoring is off on this host | switch to the daemon scheduler with monitoring enabled |
| `NOT CONFIGURED` | self mode selected but no alive URL entered | fill in the healthchecks parameters |
| `CONFIG ERROR` | `backup.env` could not be loaded | re-run the installer to repair it |
| `STATUS UNREADABLE` | the on-disk monitoring status file could not be read | a corrupt file is quarantined and reset automatically |
| `UNKNOWN` | the daemon state could not be determined | check the service and its log |

### What the sensor list tells you

The `Sensors:` list under the dashboard check reports each monitored check separately.
Its wording distinguishes two different things: whether the ping left this host, and
what the ping said.

| State | Meaning |
|-------|---------|
| `no data` | nothing recorded for this check yet |
| `stale` | the last ping is older than the check's expected cadence |
| `not provisioned` | the daemon tried to report but has no URL for this check |
| `transmit failed` | the ping did not reach the monitor |
| `ok` | fresh and transmitted |
| `up to date` | updates check: you are on the latest release |
| `update available` | updates check: a newer release exists, so the check is down |
| `sent` | notification check: that channel delivered cleanly |
| `send failed` | notification check: that channel reported a warning or an error |
| `failed` | backup check: the last run failed or hung |

Only the alive and updates checks can go `stale`, because only they have a fixed
cadence. The backup and notification checks are event driven: they report when a run
happens, so an old timestamp on them is not a fault.

### The portal link is not shown

Expected once you have logged into the portal for the first time: the server stops
minting links from then on, and you log in with the password you set. Before that first
login, a missing link means the mint attempt did not succeed, which is best effort and
deliberately quiet. Open the dashboard check again to get another one.

A link is also dropped, silently, if it does not pass the trust rules: it must be a
clean http(s) URL on the monitoring server's own domain.

### A backup ran but the check stayed silent

Only the daemon pings. A run outside the daemon relies on the handoff described above,
which needs a live daemon and a handoff no older than 15 minutes. If the daemon was
down while you ran the backup by hand, that outcome is not reported.

### Everything looks right but nothing arrives

Check the daemon's own log first:

```bash
journalctl -u proxsave-daemon.service -f
```

The daemon warns once when it cannot reach the monitoring server and then drops to
debug, so a recurring failure is quiet by design. Raise `DEBUG_LEVEL` if you need to
see each attempt.

## Configuration keys

```bash
HEALTHCHECK_ENABLED=false      # forced true by --daemon-setup and by upgrade auto-migration
HEALTHCHECK_MODE=centralized   # centralized | self
HEALTHCHECK_HEARTBEAT_INTERVAL=5m
HEALTHCHECK_UPDATE_INTERVAL=5m
HEALTHCHECK_SEND_LOG=true      # attach a log tail on a failed or hung supervised run

# Centralized cache, filled in from the server. Do not edit.
HEALTHCHECK_ALIVE_URL=
HEALTHCHECK_BACKUP_URL=

# Self mode
HEALTHCHECK_PING_ENDPOINT=https://hc-ping.com
HEALTHCHECK_PING_KEY=
HEALTHCHECK_ALIVE_ID=
HEALTHCHECK_BACKUP_ID=
HEALTHCHECK_UPDATES_URL=
HEALTHCHECK_UPDATES_ID=
HEALTHCHECK_NOTIFY_EMAIL_URL=
HEALTHCHECK_NOTIFY_EMAIL_ID=
HEALTHCHECK_NOTIFY_TELEGRAM_URL=
HEALTHCHECK_NOTIFY_TELEGRAM_ID=
HEALTHCHECK_NOTIFY_GOTIFY_URL=
HEALTHCHECK_NOTIFY_GOTIFY_ID=
HEALTHCHECK_NOTIFY_WEBHOOK_URL=
HEALTHCHECK_NOTIFY_WEBHOOK_ID=
```

Both interval defaults fall back to 5 minutes when the configured value is not a
positive duration.

See [DAEMON.md](DAEMON.md) for the daemon itself, [CONFIGURATION.md](CONFIGURATION.md)
for the full `backup.env` reference, and [NOTIFICATIONS.md](NOTIFICATIONS.md) for how
the per-channel checks relate to the delivery channels.
