# Backup monitoring (healthchecks)

<!-- site-region: monitor-backups:start -->

## Configure and verify backup monitoring

Monitoring detects missing runs as well as reported failures. The resident daemon sends
liveness, backup outcome, update and notification-delivery signals to an external
Healthchecks service. The service alerts on silence, which can reveal a dead host,
a crashed backup or a job that never started. Backup notifications alone cannot do that.

### Enable a monitored daemon

Run `proxsave` without arguments on an interactive terminal, then choose
**Maintenance** > **Install** > **Edit install**. Select the daemon under
`Scheduler engine`, then choose `Healthchecks`: centralized monitoring, your own server
or Off. The field is inactive for cron because only the resident daemon transmits.

For an existing cron installation, **Daemon** > **Install** switches engines and
enables monitoring. Review its cron warnings to avoid duplicate schedules; see
[scheduling](DAEMON.md#schedule-and-supervise-backups). Installing or reinstalling the
daemon enables monitoring even if it was previously disabled. Later upgrades preserve
an explicitly disabled value.

The actual transmission switch is `HEALTHCHECK_ENABLED`. Setting it to `false` in the
active `configs/backup.env` silences monitoring while allowing daemon scheduling.
`HEALTHCHECK_MODE=off` alone does not silence anything: runtime treats every mode value
except `self` as centralized. Disabling the daemon through **Daemon** > **Disable**
also disables monitoring and restores cron.

### Choose centralized monitoring

Centralized mode provisions the host's checks using its Server ID and relay credential.
You do not need to paste an API key. After setup choose **Diagnostic Checks** >
**Healthchecks** and read the monitor's state and portal details.

Before you have a portal password the screen provides a single-use login link, valid
for about an hour. Open it and set a password. Configure alert channels in the portal,
then trigger a test alert through the monitoring service and confirm it arrives.
Opening the link without setting a password does not complete onboarding. If it expires,
reopen the dashboard check for a fresh link. Once a password exists, the screen shows
the portal address and email login identity instead of issuing another magic link.

A local successful check only proves communication with the monitor. Until alert delivery
is configured and confirmed, a check going down may not reach you. Missing Server ID
or unresolved monitoring URLs means reporting is incomplete. Provisioning outages can
prevent a newly started daemon from resolving its URLs; an already running daemon can
continue using resolved URLs. See [centralized provisioning](#centralized-the-proxsave-monitoring-server)
for retry and fallback behavior.

### Use your own Healthchecks service

Create checks and attach alert channels on your instance before configuring ProxSave.
Use a full ping URL such as `https://hc-ping.com/<uuid>` for each check. The setup form
requires alive and backup URLs; updates and per-channel delivery checks are optional.

| Check | Expected activity | Suggested period |
| --- | --- | --- |
| Service alive | At daemon startup, then every heartbeat | Heartbeat interval, default 5 minutes |
| Backup outcome | Start and completion, or timeout failure | Backup frequency, with grace covering maximum run duration |
| Updates | Periodic release check | Update interval, default 5 minutes |
| Channel delivery | After supervised runs that attempted that channel | Backup frequency, subject to your notification filter |

For weekly or monthly backups, set the external backup check period accordingly.
Unlike centralized mode, ProxSave does not change your service's periods or alert rules.
Give long backups enough grace, normally at least `MAX_RUN_DURATION`, so a run still
working is not reported overdue prematurely.

Advanced self-mode settings are hand edits. Set `HEALTHCHECK_ENABLED=true` and
`HEALTHCHECK_MODE=self`, then supply full `HEALTHCHECK_*_URL` values or IDs with the
shared ping endpoint. See the [complete key list](#configuration-keys) for endpoint,
interval, optional log-tail and notification-check settings. Ping URLs are credentials;
keep them private.

An alive check is mandatory when self monitoring is enabled. Leaving both its URL and
ID empty warns, disables reporting and makes an otherwise clean run exit with a warning.
Blanking URLs does not switch monitoring off. Either configure a real alive check or
set `HEALTHCHECK_ENABLED=false`.

### Verify real coverage

1. Choose **Diagnostic Checks** > **Healthchecks**. Review transmission, daemon and
   alert-delivery status; resolve missing service, identity, URL or credential errors.
2. Confirm the alive check receives recurring events on the external service.
3. Confirm the next scheduled backup has a start and outcome event. Verify its actual
   saved backup independently; a green monitoring check is not archive validation.
4. Confirm your external alert channel receives a deliberate test alert. Keep
   `NOTIFY_ON=always` until this route is working.
5. If you use channel delivery checks, inspect them after a scheduled run. Their URLs
   watch channel delivery; they are not Telegram, email, Gotify or webhook settings.

`NOTIFY_ON` filters backup messages, not monitoring. Restricting messages below `always`
is applied only when monitoring is confirmed to alert you. Missing alert confirmation
can therefore leave every run notified despite a lower configured threshold.

### Interpret silence and failure

Only the daemon sends pings. A standalone dashboard backup can hand off its result to a
live daemon, but does not update per-channel scheduled delivery checks. A stale handoff
is discarded; without a live daemon no ping is sent. A skipped scheduled child can leave
a start with no finish. Disabling backups leaves the backup check silent while the
alive check continues.

A timeout is reported as failure; nonzero exit outcomes are not green. Optional failed-run
log tails may disclose sensitive context to the monitoring service, so choose
`HEALTHCHECK_SEND_LOG` deliberately. Standalone handoffs carry only the exit result.

For a missing portal link, first resolve the monitoring error and identity state, then
reopen **Healthchecks**. For a missing daemon unit while configuration still says daemon,
use the repair instructions in [CLI_REFERENCE.md](CLI_REFERENCE.md); the dashboard's
Install action is offered only when the recorded engine is cron. Do not invent a green
state by manually pinging a check whose real backup did not run.

Read [check-screen troubleshooting](#what-the-check-screen-tells-you) for every displayed
verdict and [ping details](#ping-details) for event protocol semantics.

<!-- site-region: monitor-backups:end -->

## Implementation appendix

### Ping details

The exact wire behavior, in case you are reading the monitor's event log:

- Every ping is a POST, bounded at 10 seconds, with no retry. A slow or down monitor
  cannot stall the daemon for longer than that. The one ping on the critical path is
  the run start ping, which is sent before the backup child is launched, so an
  unreachable monitor can delay the start of a scheduled backup by up to 10 seconds.
- The run start ping carries `?rid=<uuid>`, a fresh run id, so the monitor can pair it
  with the finish ping and measure the run's duration.
- The finish ping is `/` plus the run's exit status, clamped into 0..255. There is no
  separate "warning" suffix: exit `1` pings `/1`, and a start failure or an external
  kill reports a non-zero code the same way. `/0` is the only green outcome.
- A hang pings `/fail` with a `timed out after <duration>` body, because a killed child
  has no exit code to report.
- A start ping goes unanswered whenever the run produces no outcome to report: the
  child exits with the skipped code (another backup holds the lock, or it re-read
  `BACKUP_ENABLED` as false), or the daemon was stopped while the child was still
  running. The daemon has already pinged `/start` and then deliberately stays silent,
  which in the monitor's event log shows up as a started run that never finished.
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
  down check behind. In centralized mode there is one check per enabled channel among
  email, telegram, gotify, and webhook: the daemon tells the server which channels are
  enabled, so it provisions exactly those. In self mode a channel has a check only when
  you set its `HEALTHCHECK_NOTIFY_<CHANNEL>_URL` or `_ID`: see
  [Notification delivery checks](#notification-delivery-checks).
- Ping URLs embed the check identifier, which is a low-capability secret. ProxSave
  registers them with the log masker and strips them out of transport errors, so a
  failed ping logs the reason and never the URL.



## Detailed reference

The task above is the operator procedure. The following material preserves detailed behavior, limits and implementation context.

## What gets monitored

The daemon reports four families of checks. In centralized mode the server creates them
and they appear on the monitor as the `proxsave-*` checks below. In self mode you create
them on your own instance, under any name you like, and give the daemon their ping URLs:
see [Self mode](#self-mode-your-own-healthchecks).

| Check | When it pings | What it covers |
|-------|---------------|----------------|
| `proxsave-alive` | immediately at daemon start, then every `HEALTHCHECK_HEARTBEAT_INTERVAL` | the daemon and the host are up. Stops when either dies, and the monitor alarms on the silence. It is also pinged `/fail` on purpose, with the reason in the body, while a backup child abandoned in uninterruptible sleep is outstanding -- see [DAEMON.md](DAEMON.md#caveat-uninterruptible-sleep-d-state) |
| `proxsave-backup` | per run: `/start` at launch, then the run's exit code, or `/fail` on a hang | whether the backup ran and how it ended |
| `proxsave-updates` | immediately at daemon start, then every `HEALTHCHECK_UPDATE_INTERVAL` | `/0` when up to date, `/1` when a newer release exists, so the check goes down and tells you to upgrade |
| `proxsave-notify-<channel>` | after each daemon-supervised run, one per channel the backup attempted | whether that notification channel actually delivered |

A run you start yourself, from the dashboard's **Backup** row or by hand, leaves the
per-channel checks untouched. Only `proxsave-backup` picks up a standalone run, through
the handoff described below.

### When the daemon has no backup to report

With `BACKUP_ENABLED=false` the daemon skips the scheduled run entirely: no child
process and no outcome ping, so `proxsave-backup` honestly goes down rather than
reporting a false green. The heartbeat keeps signalling, so you can still tell the
daemon itself is healthy.

### Backups run outside the daemon

A backup started by hand, or from the dashboard's **Backup** row, does not ping the monitor
itself. The resident daemon is the only pinger. A standalone run instead drops a handoff
file and wakes the daemon with `SIGUSR1`, and the daemon pings `proxsave-backup` with
that outcome. A handoff older than 15 minutes is discarded without pinging, so a
long-past run never flips the check, and if no live daemon is found nothing pings at
all.

## Turning monitoring off

`HEALTHCHECK_ENABLED` is the only switch that stops transmission. It ships `false` in the
config template, so a host that never enabled the daemon is already silent. It is written
`true` by the install wizard when you pick either monitoring mode, and forced `true` by
every path that retrofits the daemon onto an existing install: `--daemon-setup`, the
dashboard's **Daemon** > **Install**, and the upgrade auto-migration. `--daemon-remove` and
the dashboard's **Daemon** > **Disable** write it back to `false`.

To send nothing anywhere while keeping the daemon as your scheduler, set it yourself:

```bash
HEALTHCHECK_ENABLED=false
```

That value survives later upgrades. It does not survive re-running `--daemon-setup` or the
dashboard's **Daemon** > **Install**, which force it back to `true` every time: choosing
the daemon engine turns monitoring on.

Note what does **not** turn monitoring off. `HEALTHCHECK_MODE` cannot: see below. Blanking
the self-mode ping URLs cannot either, and is worse than doing nothing (see
[Self mode](#self-mode-your-own-healthchecks)).

## Two modes

`HEALTHCHECK_MODE` picks where the pings go. It has exactly two values at run time.

- **`centralized`** (the default): ProxSave runs the monitor for you and provisions
  this host's checks. Nothing to set up, no API key on this machine.
- **`self`**: you point the daemon at your own healthchecks instance, self-hosted or
  the SaaS, and own the checks yourself.

Anything else in the file is read as `centralized`. The comparison is on the lowercased,
trimmed value against the single literal `self`; every other string, including an empty
value, a typo, and the `off` the install wizard itself writes when you answer `Off`,
resolves to `centralized`. So `HEALTHCHECK_MODE=off` in `backup.env` is not an off switch:
what silences that host is the `HEALTHCHECK_ENABLED=false` the wizard writes next to it.
Edit one of them by hand and you can end up with `off` sitting beside `true`, which is a
centralized host that reports.

## Centralized: the ProxSave monitoring server

This host is identified to the monitoring server by its **Server ID**, which is
generated at install time, and a per-server relay credential. The credential is
**provisioned automatically**: the daemon asks the server for one on its first run and
persists it. No Telegram pairing, no account, no key to copy. It is the same identity
the centralized Telegram relay uses, so a host that already sends Telegram
notifications is already provisioned.

Once provisioned, the daemon fetches its ping URLs from the server at startup and
keeps them in memory only. While a URL is still unresolved it retries on each
heartbeat; once resolved it reuses the same URLs until the service restarts, so a
server-side change to them is picked up at the next restart.

The same request carries the backup schedule's frequency and `NOTIFY_ON`, and at startup the
daemon journal reports each answer in its own block: `Applying backup schedule...`, then
`Applying notify level...`, and, only when the request failed, `Applying healthchecks ping
URLs...`, which ends in `WARNING Warning Healthchecks ping URLs: not available` when no URL is
available at all. The heartbeat that later gets an answer prints those blocks again, applied,
and the daemon keeps retrying quietly until then.

An outage of the provisioning server therefore does not stop a daemon that is already
reporting: the pings go to the monitoring host, not to the config API. What it does
block is a daemon that has not resolved its URLs yet, for example one that started
while the server was down. There is a fallback to `HEALTHCHECK_ALIVE_URL` and
`HEALTHCHECK_BACKUP_URL` in `backup.env` for exactly that case, but nothing fills them
in for you: a centralized fetch is never written back to the file, so on a
wizard-installed centralized host they are empty and the fallback resolves to nothing.
That host reports nothing until the fetch succeeds, and the gap shows up as a missed
heartbeat.

The credential also self-heals. If the server ever rejects it, or reports that this
host's account was parked for being unused, the daemon clears the stale credential and
provisions a fresh one on its next attempt, which re-registers the host. Transient
errors never touch a working credential. Provisioning retries are throttled to one
attempt every 15 minutes. If the server asks for a longer wait, the daemon honors it
and adds a small per-host offset (up to a tenth of the requested wait, capped at five
minutes) so a fleet coming back at once does not arrive in lockstep.

### The backup check's period

The server gives `proxsave-backup`, and the periodic notify checks used with
`NOTIFY_ON=always`, a period equal to the backup schedule: 1 day for daily, 7 days for
weekly, 31 days for monthly, each with a grace of 1 hour. The daemon sends
`SCHEDULER_FREQUENCY` with the request above, and a new frequency applies only once the server
confirms it has moved the checks; until then the daemon keeps the frequency it last had
confirmed, so a weekly run is never late on a daily check. See
[DAEMON.md](DAEMON.md#backup-schedule).

### Your monitoring portal

Every centralized host gets its own portal on the monitoring server, where you can see
each check's state and history and decide how you want to be alerted.

ProxSave shows you how to reach it in three places:

- during the install wizard, on the monitoring screen, in both the TUI and `--cli`;
- in the dashboard, under `Healthchecks` in the diagnostic checks;
- at the end of a backup run, in the log epilogue and in the run screen's outcome box.

What it shows depends on whether you have given yourself a portal password yet.

**Before you have a password**, you get a **single-use login link**, valid for about an
hour. Open it and **set a password**. That is what turns the link into an account you
can log into later, and it is the point at which you can configure alert channels,
email and the rest, so the monitor can reach you when a check goes down. Until then the
server mints a fresh link every time, so a link that expired is never a problem: just
open the dashboard check again.

**Once you have a password**, the link stops being minted and its place is taken by the
portal's own address plus the identity you sign in with. That identity is an **email
address**, not a username. Sign in there with the password you chose.

The exact wording differs a little per surface. The log epilogue uses
`Healthchecks Portal:` for both states, the link and the sign-in address alike, and
adds a `Healthchecks Login:` line only in the second. The run screen distinguishes them
by name: `Healthchecks link:` for the single-use link, `Healthchecks portal:` and
`Healthchecks login:` for the second state. The wizard and dashboard box the same
values with a short caption.

Two things worth knowing:

- Setting a password, not opening the link, is what retires the link. Looking around
  the portal and closing the tab changes nothing: you keep getting fresh links until
  you actually choose a password.
- ProxSave never opens or follows the link, it only prints it. It also refuses to print
  anything that is not a clean http(s) URL on the monitoring server's own domain, so a
  tampered response cannot put a phishing address in front of you. The same applies to
  the portal address in the second state.

## Self mode: your own healthchecks

In self mode ProxSave pings the URLs you give it and does nothing else. There is no
identity, no portal, and no provisioning: the checks, the alert rules, and the
retention are yours to manage on your own instance.

**The service-alive check is mandatory in self mode.** With `HEALTHCHECK_ENABLED=true`,
`HEALTHCHECK_MODE=self` and both `HEALTHCHECK_ALIVE_URL` and `HEALTHCHECK_ALIVE_ID` empty,
every run warns

```text
WARNING  Healthchecks: no alive check configured
SKIP     Healthchecks: disabled
```

and that warning costs the run its exit code: an otherwise clean backup ends at `1`
instead of `0`. It is deliberate rather than pedantic. `HEALTHCHECK_ENABLED=true` says you
want monitoring, and self mode with no liveness check is the one shape that looks
configured and catches nothing: the dead-man switch is the whole point, and a backup-only
self config has no liveness signal at all. Either fill in an alive check, or set
`HEALTHCHECK_ENABLED=false` and be honestly unmonitored. Blanking the URLs is not the way
to switch monitoring off.

The backup check's period is yours too. With a weekly or monthly `SCHEDULER_FREQUENCY`, give
the check a matching period on your instance; the daemon journal reminds you at start with
`Backup check: pinged weekly on your own server` (or `monthly`).

Centralized mode has the matching rule with a different missing piece: a host with no
Server ID, the identity generated at install time, warns `Healthchecks: no SERVER_ID`
instead, at the same cost to the exit code. On a host whose configured engine is cron,
either reason arrives with `(cron mode: only the resident daemon transmits)` appended,
because there nothing would have transmitted even with the key filled in.

### Setting it up

The operator setup and verification sequence is in [Configure and verify backup monitoring](#configure-and-verify-backup-monitoring). Full URL and ID forms are documented below.

### In backup.env

You can also configure it by hand. Each check accepts either a full URL or an
identifier that gets assembled onto a shared endpoint:

```bash
HEALTHCHECK_ENABLED=true
HEALTHCHECK_MODE=self
HEALTHCHECK_PING_ENDPOINT=https://hc-ping.com   # base for the *_ID form; your own address if you self-host
HEALTHCHECK_PING_KEY=                           # optional, inserted between base and id
HEALTHCHECK_ALIVE_URL=                          # service-alive check: full ping URL...
HEALTHCHECK_ALIVE_ID=                           # ...or its UUID or slug
HEALTHCHECK_BACKUP_URL=                         # backup-outcome check
HEALTHCHECK_BACKUP_ID=
HEALTHCHECK_UPDATES_URL=                        # updates check, optional
HEALTHCHECK_UPDATES_ID=
HEALTHCHECK_NOTIFY_EMAIL_URL=                   # notification delivery checks, optional:
HEALTHCHECK_NOTIFY_EMAIL_ID=                    # checks on your instance, not the channel settings
HEALTHCHECK_NOTIFY_TELEGRAM_URL=
HEALTHCHECK_NOTIFY_TELEGRAM_ID=
HEALTHCHECK_NOTIFY_GOTIFY_URL=
HEALTHCHECK_NOTIFY_GOTIFY_ID=
HEALTHCHECK_NOTIFY_WEBHOOK_URL=
HEALTHCHECK_NOTIFY_WEBHOOK_ID=
```

A full `*_URL` always wins over the matching `*_ID`. With a ping key set, an id
resolves to `<endpoint>/<key>/<id>`; without one, to `<endpoint>/<id>`.

Apart from `HEALTHCHECK_ENABLED` and `HEALTHCHECK_MODE`, none of these variables has any
effect in centralized mode, with one exception: `HEALTHCHECK_ALIVE_URL` and
`HEALTHCHECK_BACKUP_URL` do double duty. In self mode they are
your own ping URLs. In centralized mode they are an optional fallback, used only when
ProxSave HC Server cannot be reached, that nothing fills in, so leave them empty unless you
deliberately want one.

### Notification delivery checks

`HEALTHCHECK_NOTIFY_EMAIL_*`, `HEALTHCHECK_NOTIFY_TELEGRAM_*`, `HEALTHCHECK_NOTIFY_GOTIFY_*`
and `HEALTHCHECK_NOTIFY_WEBHOOK_*` each point to a check on your healthchecks instance
that watches whether one notification channel delivered. They are not that channel's
settings, and they send no message. The channels themselves are configured with their own
variables, described in [NOTIFICATIONS.md](NOTIFICATIONS.md): Gotify, for instance, with
`GOTIFY_SERVER_URL` and `GOTIFY_TOKEN`. A Gotify server address in
`HEALTHCHECK_NOTIFY_GOTIFY_URL`, or a Gotify token in `HEALTHCHECK_NOTIFY_GOTIFY_ID`,
configures neither Gotify nor a working check.

After each scheduled run, the daemon pings the check of every channel that sent in that
run: `/0` when the channel delivered cleanly, `/1` when it reported a warning or an error.
A channel that is switched off is not pinged, and no channel is pinged after a run you
start yourself.

Setting any of these variables keeps `NOTIFY_ON` at `always`: every run is notified,
whatever the setting says. A delivery check on your instance expects a ping after every
scheduled run, and a run the filter kept quiet would turn it down. To let `NOTIFY_ON=warning`
or `failure` apply in self mode, leave all eight empty. See
[Alert delivery and NOTIFY_ON](#alert-delivery-and-notify_on).

In centralized mode these variables have no effect: the server creates the
`proxsave-notify-<channel>` checks itself.

## Alert delivery and NOTIFY_ON

A monitor that alerts nobody is not a monitor. Before a run lets `NOTIFY_ON=warning` or
`failure` keep a clean run quiet, it checks that this monitor would reach you, and it
notifies every outcome when it cannot confirm it.

**Centralized mode.** At the start of every run, ProxSave asks the monitoring server about
this host's project on the portal. The answer is one of:

| Status | Meaning |
|---|---|
| `ready` | an alert channel covering both the alive and the backup check has delivered a DOWN, and no failed delivery came after it |
| `not configured` | no alert channel is set up for DOWN alerts |
| `not verified` | alert channels exist, but none has delivered a DOWN yet |
| `degraded` | an alert channel failed, is switched off or paused, only one of the two checks is covered, or the monitor is not sending alerts |
| `unknown` | the server could not tell |

Only `ready` lets the filter apply. A DOWN counts whether it was real or a test: after you
add or change an alert channel on the portal, send it a test notification (the channel's
**Test!** button), so the server has a delivery to verify. The server also has to apply the
same `NOTIFY_ON` to this host's `proxsave-notify-*` checks, so that they stop expecting a
ping after every run. The daemon sends the value, and sends it again before the next
scheduled run when you change it in `backup.env` or switch a channel on or off. A backup
started by hand does not: after a change, it notifies every outcome until the next
scheduled run has passed it on.

**Self mode.** The filter applies when both ping URLs are valid, the daemon is transmitting
and no `HEALTHCHECK_NOTIFY_*` variable is set: a notify check you run on a period would go
DOWN on every run the filter kept quiet. See
[Notification delivery checks](#notification-delivery-checks).

**Where you see it.** The run log's `Applying notification filter...` block (`Setting`,
`Healthchecks status`, `Filter in effect`, then `Notification filter: applied`, or
`Warning Notification filter: not applied` after the reason), the daemon journal's
`Applying notify level...` block, at start and before a run that sends a changed level, and
the `Notifications:` block of the Healthchecks check screen and of the install check:

```text
Notifications:
Setting: NOTIFY_ON=warning
Current: always (Healthchecks not configured)
```

`Current` gives the reason in parentheses whenever it differs from the setting; in self mode
both lines read `Self mode`.

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
| `NOT INSTALLED` | the monitor is reachable but the daemon service is not installed | Use **Daemon** > **Install** when the recorded engine is cron. If configuration records daemon but the unit is missing, use the repair entry point in [CLI_REFERENCE.md](CLI_REFERENCE.md); the dashboard offers Disable, Restart and Status in that state. |
| `NOT RUNNING` | the service is installed and stopped, or never wrote a heartbeat | `systemctl start proxsave-daemon.service` |
| `RUNNING, NOT REPORTING` | the process is up but has written no heartbeat yet | usually a stale build; restart the service |
| `STALE` | the last heartbeat is older than twice the heartbeat interval, and the interval is floored at one minute first, so the smallest stale window is two minutes | the daemon is stopped or wedged; check `journalctl -u proxsave-daemon.service`. On a systemd host you will normally see `RUNNING, NOT REPORTING` instead: an active unit with a stale heartbeat is reclassified, so `STALE` surfaces only when systemd could not be asked |
| `BEHIND` | the running daemon is on an older binary than the one on disk | restart the service so it loads the upgrade |
| `NOT PROVISIONED` | the daemon runs but has no ping target yet | centralized: wait for provisioning. Self: the ping URLs are missing |
| `MONITOR UNREACHABLE` | the daemon runs but its pings do not arrive | outbound connectivity from this host |
| `TRANSMIT FAILED` | the last backup outcome did not reach the monitor | usually transient; check the daemon log |
| `REJECTED` | the server refused this host's credential | it is cleared and re-provisioned automatically on the next daemon run |
| `NOT REGISTERED` | the server does not know this host yet | registered automatically on the next daemon run |
| `PARKED` | the server had removed this host's unused account | cleared and re-registered automatically |
| `DISABLED` | centralized monitoring is turned off on the server | nothing to configure here |
| `NO IDENTITY` | this host has no server identity | re-run the installer to regenerate it |
| `NOT ENABLED` | monitoring is off on this host: `HEALTHCHECK_ENABLED=false` | nothing, if that is what you want. Otherwise switch to the daemon scheduler, which sets the key |
| `NOT CONFIGURED` | self mode selected but there is no service-alive check to ping: no `HEALTHCHECK_ALIVE_URL`, and no `HEALTHCHECK_ALIVE_ID` that resolves against `HEALTHCHECK_PING_ENDPOINT` | fill in the healthchecks parameters, or set `HEALTHCHECK_ENABLED=false` |
| `CONFIG ERROR` | `backup.env` could not be loaded | re-run the installer to repair it |
| `STATUS UNREADABLE` | the on-disk monitoring status file could not be read | a corrupt file is quarantined and reset automatically |
| `UNKNOWN` | the daemon state could not be determined | check the service and its log |

Self-mode `NOT CONFIGURED` asks the same question the run asks at start-up: is there a
service-alive check to ping. Both accept a full `HEALTHCHECK_ALIVE_URL` or a
`HEALTHCHECK_ALIVE_ID`, which is assembled against `HEALTHCHECK_PING_ENDPOINT` (and the
optional `HEALTHCHECK_PING_KEY`). A host configured with only an alive **id** is a
configured host on both sides.

The one case where the screen is stricter than the run: an alive **id** with
`HEALTHCHECK_PING_ENDPOINT` set to empty resolves to no URL at all, so this screen says
`NOT CONFIGURED` while the run's start-up check, which only looks for a non-empty id, lets
the backup proceed and then has nothing to ping. Leave the ping endpoint at its default
unless you run your own healthchecks instance, in which case point it at that.

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

If you see the portal address and a `Login:` line instead, that is the expected state
once you have set a portal password: the server stops minting links from then on. Sign
in at that address, with that identity, using the password you chose.

If you see nothing at all about the portal, the mint attempt did not succeed. It is
best effort and deliberately quiet, so there is no error to read. Open the dashboard
check again to get another one. Note that ProxSave will not guess: it only shows the
address-and-identity form when the server confirms a password exists, so a failed
attempt shows nothing rather than sending you to a sign-in page you have no password
for.

Opening the link is not what retires it. If you opened the portal once, never chose a
password, and now see no link, that is a failed mint and not the expected end state.

A link or address is also dropped, silently, if it does not pass the trust rules: it
must be a clean http(s) URL on the monitoring server's own domain.

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
debug, so a recurring failure is quiet by design. To see each attempt, raise
`DEBUG_LEVEL` in `backup.env` and then restart the service: the daemon reads its log
level once at startup and has no reload path, so the change does nothing until it
comes back up.

```bash
systemctl restart proxsave-daemon.service
```

## Configuration keys

```bash
HEALTHCHECK_ENABLED=false      # the only off switch. Template default false; forced true by --daemon-setup,
                               # the dashboard's Daemon > Install, and the upgrade auto-migration;
                               # --daemon-remove and Daemon > Disable write it back to false
HEALTHCHECK_MODE=centralized   # centralized | self. Any other value, "off" included, reads as centralized
HEALTHCHECK_HEARTBEAT_INTERVAL=5m
HEALTHCHECK_UPDATE_INTERVAL=5m
HEALTHCHECK_SEND_LOG=true      # attach a log tail on a failed or hung supervised run

# Centralized: optional fallback cache, nothing auto-fills it.
HEALTHCHECK_ALIVE_URL=
HEALTHCHECK_BACKUP_URL=

# Self mode only: checks on your own healthchecks instance (see Self mode above).
HEALTHCHECK_PING_ENDPOINT=https://hc-ping.com   # base for the *_ID form
HEALTHCHECK_PING_KEY=                           # optional, inserted between base and id
HEALTHCHECK_ALIVE_ID=                           # service-alive check, used when HEALTHCHECK_ALIVE_URL is empty
HEALTHCHECK_BACKUP_ID=                          # backup-outcome check, used when HEALTHCHECK_BACKUP_URL is empty
HEALTHCHECK_UPDATES_URL=                        # updates check, optional: /1 when a newer release exists
HEALTHCHECK_UPDATES_ID=
# Notification delivery checks, optional. Not the channel settings; any of them set
# keeps every run notified whatever NOTIFY_ON says.
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
the per-channel checks relate to the delivery channels. In self mode, the per-channel
checks are the [notification delivery checks](#notification-delivery-checks).

## Earlier guide entry points

## Where monitoring shows up

See the complete [operator procedure](#configure-and-verify-backup-monitoring). Detailed settings and implementation material remain in the reference sections above.

## Why silence is the signal

See the complete [operator procedure](#configure-and-verify-backup-monitoring). Detailed settings and implementation material remain in the reference sections above.
