# Notifications and the centralized bot relay

ProxSave reports the outcome of every backup run over a small set of channels:
**Email**, **Telegram**, **Gotify**, and **Webhooks**. On top of those it runs a
separate monitoring layer that turns each channel's outcome into an external
dead-man sensor. This document explains how the channels work, the centralized
Telegram relay (where the bot token stays on the host), the two-response delivery
model, the portal magic-link, and the log-redaction rules that keep secrets out of
the logs.

For the full list of `backup.env` keys see [CONFIGURATION.md](CONFIGURATION.md).
For the per-channel healthchecks sensors and the resident daemon that pings them see
[DAEMON.md](DAEMON.md).

## The one invariant: notifications never abort the backup

A notification is always best-effort. No channel can fail, delay, or block a backup:

- Every notifier reports `IsCritical() == false`. The interface comment is explicit:
  "notifications never abort backup" (`internal/notify/notify.go`).
- The orchestrator adapter swallows every `Send` error: it logs the failure, records
  an `error` outcome for that channel, and returns `nil` to the caller
  (`internal/orchestrator/notification_adapter.go`). The dispatcher ignores the return
  value too.
- The run's exit code is **frozen before any channel sends**. `applyIssueExitCode`
  promotes the exit code from the collected errors and warnings first, then the
  notification phase runs. A warning raised while sending a notification is still a
  warning, never an error, and never changes the exit code
  (`internal/orchestrator/extensions.go`).

So a red channel in the logs tells you a message did not go out. It never means the
backup failed.

## Two tiers

There are two independent layers, and it helps to keep them apart:

- **Tier 1, delivery.** During the run's notification phase each enabled channel
  (Email, Telegram, Gotify, Webhook) formats the report and sends it. This is the
  user-facing "did I get a message" layer.
- **Tier 2, monitoring.** Each channel's outcome (`ok` / `warning` / `error` /
  `disabled`) is handed to the resident daemon, which turns it into a per-channel
  `proxsave-notify-<channel>` healthchecks sensor (`/0` up, `/1` down). Alongside it,
  an always-visible **Healthchecks** section prints the current transmission status.
  This is the "is my monitoring actually working" layer. See [DAEMON.md](DAEMON.md).

The two tiers are decoupled on purpose. A Telegram message the relay accepted but
Telegram did not deliver keeps the run green (Tier 1 success), yet drives the
`notify-telegram` sensor DOWN (Tier 2), so an undelivered message never reports "ok".

### Dispatch order

Channels are wired and dispatched in a fixed order:

```
Email, Telegram, Gotify, Webhook, Healthchecks
```

Healthchecks is deliberately **last**. The Telegram relay may piggyback a fresh
portal magic-link on its response; dispatching Healthchecks last means that link has
already been captured onto the run's stats before the Healthchecks section renders.
Each channel is gated independently on its own `*_ENABLED` flag, so the ordering is
about link capture, not about one channel depending on another.

### The per-channel handoff file

The backup child writes its per-channel outcomes to
`<BASE_DIR>/identity/.notify_results.json` (atomic write, mode `0600`), and the
resident daemon (the only process that pings the monitor) reads it after the child
exits. Two guards keep it honest:

- The write is **gated on `PROXSAVE_RUN_ID`**, which only the daemon sets on its
  child. A bare `proxsave --backup` by hand writes no file and leaves nothing stale.
- The daemon **rejects any file whose run id is not the run it supervised**, and an
  empty result set is written as an empty object so the daemon can tell "child ran,
  nothing to report" from "child crashed" (missing file).

The pings are driven by the channel set **in the file**, not by the daemon's cached
config, so a channel toggled off between runs never flaps a stale DOWN. The
Healthchecks section is a reporting surface, not a delivery channel, so it never
gets a `notify-*` result of its own.

## Telegram: personal vs centralized

Telegram has two modes, chosen by `BOT_TELEGRAM_TYPE` (`personal` or `centralized`,
default `centralized`). Any other value makes the Telegram notifier skip itself (a
warning is logged and Telegram is not registered); the backup is unaffected.

### Personal mode (the bot token leaves the host)

Personal mode talks straight to Telegram. It requires `TELEGRAM_BOT_TOKEN` and
`TELEGRAM_CHAT_ID` (validated against `^[0-9]+:[A-Za-z0-9_-]{35,}$` and `^-?[0-9]+$`)
and POSTs to `https://api.telegram.org/bot<token>/sendMessage`. The raw bot token is
in the request URL, so in personal mode the token leaves the host on every send. Use
this only if you run your own bot.

### Centralized mode (the bot token stays on the host)

Centralized mode routes through the shared ProxSave bot-server at
`https://bot.proxsave.dev` (the hardcoded `ServerAPIHost`). It requires a `SERVER_ID`.
Once a per-server relay secret has been provisioned (see below), the message is
POSTed to `/api/notify` with the body `{server_id, message}` and an `X-Server-Auth`
header carrying that secret. **The server looks up the chat and sends to Telegram
itself, so the bot token never leaves the host.** The legacy direct
`api.telegram.org` path is used only as a fallback when no relay secret exists yet.

This is the security keystone of centralized mode: the client stores a low-capability
per-server secret, not the bot token.

## TOFU secret provisioning

The per-server relay secret is provisioned trust-on-first-use:

1. **Fetch.** The client calls `GET /api/get-chat-id?server_id=<id>` with no
   `X-Server-Auth` (there is no secret yet). It sends `X-Proxsave-Provision: 1` only
   when a persist target exists (`BaseDir` set), so a run with nowhere to store a
   secret never churns one on the server.
2. **Adopt and persist.** If the `200` response carries a `notify_secret`, the client
   registers it with the logger for scrubbing, then persists it to
   `<BASE_DIR>/identity/.notify_secret` (mode `0600`, then `chattr +i`, the same
   immutable mechanism used for the server identity file). Adoption **overwrites** any
   existing secret rather than skipping: a `200` carrying a secret means the server
   wants this one adopted, so a re-issued secret is never stranded.
3. **Confirm.** The client POSTs `/api/confirm-secret` with `{server_id}` and the new
   secret in `X-Server-Auth`, which tells the server to stop re-issuing it. This step
   is best-effort and non-fatal.
4. **Relay.** The same run then relays through `/api/notify` with the fresh secret.

The persisted secret matches `^[0-9a-z]+(-[0-9a-z]+)*$` (for example
`3h64-dyi8-q3d6-wcm5`) and is format-validated on **both write and read**.
`LoadNotifySecret` returns an empty string for an absent, empty, or malformed file
(junk is never fed into the auth header) and reads under `os.Root` confinement.

`get-chat-id` status codes: `200` success; `403` first contact or the bot has not been
started; `409` missing registration; `422` invalid `SERVER_ID`; `426` "Upgrade
ProxSave to v0.28.0 or later to complete pairing".

### Stale-secret recovery

If `/api/notify` answers `401` or `403` (a rotated or stale secret), the notifier
drops the in-memory secret, reprovisions once through the fetch path, and relays that
run once (or falls back to the legacy token path). It never loops. Like every other
notification failure, this is non-critical and never touches the backup.

## The two-response delivery model

Centralized Telegram delivery has **two responses**, and the distinction is the
load-bearing subtlety of the whole channel.

**Response 1, acceptance.** The `POST /api/notify` result is the success signal:

- `200` means the legacy synchronous relay delivered inside the request (nothing to
  poll).
- `202` means the message was accepted onto the server's durable outbox and will be
  retried server-side.

Either way, acceptance sets `result.Success = true`. Other statuses: `404` server
unknown, `409` registration missing, `413` message too long.

**Response 2, delivery.** The real Telegram delivery outcome is learned separately by
polling `GET /api/notify/status?server_id=<id>&notify_id=<id>`. This poll **never
changes `result.Success`** and never fails the caller. Only `X-Server-Auth` is on the
wire here; the bot token never appears. It resolves to one of `delivered`, `failed`,
`pending`, or `unknown`:

- Still pending at the deadline stays `pending` (the durable outbox keeps retrying).
- A definitive miss or auth issue (`404` / `401` / `403`) is `unknown`, not retried.
- Transient `5xx` or network errors are retried within the budget, then reported
  `pending`.

The poll is deliberately gentle: a deterministic per-notification jitter (0 to 499 ms,
derived from the notify id) so a midnight burst of clients desynchronizes, an interval
floored to 1 s and capped at 5 s, and a total timeout floored to 10 s and capped at
60 s, so a mis-set value can neither spin nor hang the end of a backup for minutes.

A single 32-hex-character **notify id** is minted once per send and reused across the
relay POST, the status poll, and any retry, so the server's idempotency dedupe never
double-queues a message. On the relay POST (and its retry) the id travels as the
`X-Notify-Id` header; on the status poll it travels as the `notify_id` query
parameter.

Delivery confirmation is config-gated:

| Key | Default | Meaning |
|-----|---------|---------|
| `TELEGRAM_CONFIRM_DELIVERY` | `true` | poll for the real delivery outcome after acceptance |
| `TELEGRAM_CONFIRM_TIMEOUT_SECONDS` | `10` | total poll budget (floored 10 s, capped 60 s) |
| `TELEGRAM_CONFIRM_INTERVAL_SECONDS` | `1` | poll interval (floored 1 s, capped 5 s) |

With `TELEGRAM_CONFIRM_DELIVERY=false` the state is `unconfirmed` (a quiet debug line,
distinct from `pending`) and no poll runs.

### Reading the result in the logs

Telegram prints **two lines**. The first is server acceptance, the second is delivery:

```
✓ Telegram: sent to ProxSave server (in 240ms)
✓ Telegram: delivered to Telegram
```

The first-line latency is acceptance-only (from `relay_accept_duration`), so it
reports true relay latency, not latency plus the poll budget. The second line maps
from the delivery state:

| State | Second line | Level |
|-------|-------------|-------|
| `delivered` | `✓ Telegram: delivered to Telegram` | info |
| `failed` | `❌ Telegram: not delivered (<reason>)` | warning |
| `pending` | `⚠️ Telegram: accepted; delivery in progress (auto-retry)` | warning |
| `unconfirmed` | (quiet, confirmation disabled) | debug |
| `unknown` | `⚠️ Telegram: accepted; delivery not confirmed` | warning |

If acceptance itself failed, the first line reads
`❌ Telegram: could not send to ProxSave server`. The failure `<reason>` on the
`failed` line is translated for humans: `bot blocked by the user` (`http_403`),
`invalid chat` (`http_400` / `http_404`), `message too long` (`http_413`),
`Telegram unreachable too long (expired)` and `too many failed attempts` (the
server's give-up reasons), or `rejected by Telegram` when the reason is empty.

Every non-success second line is at most a warning. None of it blocks the backup.

### The monitoring sensor

The Tier 2 `notify-telegram` sensor is stricter than `result.Success`: a relay-accepted
message whose delivery poll came back `failed` drives the sensor to `error` (`/1`
DOWN), even though the run stays green. Only `failed` counts; `pending` and
`unconfirmed` do not. This is the whole point of Tier 2: an undelivered message must
not report "ok" and defeat the monitoring.

## The portal magic-link

The bot-server can piggyback a fresh portal login link on its `/api/notify` response
(field `login_url`), until the user's first portal login, after which it stops. The
Healthchecks section can also mint one best-effort via
`GET /api/healthcheck/config?server_id=<id>&login=1` (the daemon's own polls pass no
`login=1`, so only install-time and run-phase callers request a mint).

The link is short-lived (about an hour), single-use, and display-only (ProxSave never
fetches it). It is handled with one specific discipline:

- It is carried **raw** end-to-end (through `result.Metadata["login_url"]` onto
  `stats.HealthcheckLink`) and is **never** registered as a log secret, because it has
  to stay visible when printed.
- It is sanitized through `serverbot.SanitizeLoginURL` at exactly **one** display
  boundary (`logMonitoringPortalLink`), which prints
  `Healthchecks Portal: <url>` right after the Server MAC address line at the end of
  the run.

`SanitizeLoginURL` is the single guard against a hostile or MITM bot-server injecting
terminal escapes into your console: it returns the link only if it is a clean
`http(s)` URL made entirely of printable ASCII (`0x21` to `0x7e`); anything else,
including any control character, space, or Unicode bidi/format trick, fails closed to
an empty string. It is all-or-nothing and never truncated (truncating would break the
link).

## Email

Email supports three delivery methods, chosen by `EMAIL_DELIVERY_METHOD`:

| Method | How | Fallback |
|--------|-----|----------|
| `relay` | POST a JSON report to the shared Cloudflare relay worker | `sendmail` (if `EMAIL_FALLBACK_SENDMAIL`) |
| `pmf` | route via Proxmox `proxmox-mail-forward` | `relay`, then `sendmail` |
| `sendmail` | hand off to the local `/usr/sbin/sendmail` | none |

For `pmf` the relay fallback is skipped when the recipient is empty or `root`. If
`EMAIL_FROM` is empty it defaults to `no-reply@proxmox.tis24.it`.

**Recipient handling.** An empty `EMAIL_RECIPIENT` triggers auto-detection for
`root@pam` (PVE order: `pvesh` on `/access/users/root@pam`, then `pveum user list`,
then `/etc/pve/user.cfg`; PBS order: `proxmox-backup-manager user list`, then
`/etc/proxmox-backup/user.cfg`). The detected address is logged redacted. An empty or
malformed recipient is a hard failure for `relay` and `sendmail` (sending anyway
would report a false success); `pmf` only warns, because it routes through Proxmox
Notifications and uses the address only for the `To:` header.

**`root@` is blocked only for `relay`.** With `sendmail` fallback enabled the notifier
bypasses relay and goes straight to sendmail; without a fallback it hard-fails with
`recipient <addr> is not allowed (root accounts are blocked)`. The `sendmail` and
`pmf` methods accept `root` recipients.

**Delivery is handoff, not guaranteed delivery.** Relay logs an accepted request
(HTTP `200` from the worker); sendmail and pmf explicitly warn that exit code `0`
means "accepted to queue, not necessarily delivered".

### The shared cloud relay worker

The `relay` method POSTs to a hardcoded Cloudflare Worker
(`https://relay-tis24.weathered-hill-5216.workers.dev/send`) with these headers:
`Authorization: Bearer <WorkerToken>`, `X-Signature` (HMAC-SHA256 of the JSON
payload), `X-Script-Version`, `X-Server-MAC`, and a `proxsave/<version>` user agent.

The `WorkerToken` (`v1_public_20251024`) and the HMAC secret are a **shared public
anti-abuse credential, not a confidential secret**: the same values ship in every
distributed binary and are published in the repository, so they cannot be kept secret
on the client. They only gate the free shared worker, whose real protection is
server-side rate limiting keyed on `server_mac` / `server_id`. Both sites carry a
`#nosec G101` documenting this. See
[SECURITY.md](SECURITY.md#hardcoded-relay-credential-g101). Operators who want a
private relay can point `CLOUDFLARE_WORKER_URL` / `CLOUDFLARE_WORKER_TOKEN` /
`CLOUDFLARE_HMAC_SECRET` at their own worker.

The JSON report body (`buildReportData`) must byte-match the legacy Bash
`collect_email_report_data()` output, otherwise the worker's HMAC signature check
fails. Changing the report shape breaks relay auth.

## Gotify

Set `GOTIFY_ENABLED=true` with `GOTIFY_SERVER_URL` and `GOTIFY_TOKEN` (both required).
ProxSave POSTs to `<GOTIFY_SERVER_URL>/message?token=<GOTIFY_TOKEN>`; success is any
`2xx`. The token lives in the URL query, so on a request error it is redacted from the
message via `RedactSecrets` (which masks both the raw and the URL-encoded form).

Priority maps from the run outcome, with defaults you can override:

| Outcome | Key | Default |
|---------|-----|---------|
| success | `GOTIFY_PRIORITY_SUCCESS` | `2` |
| warning | `GOTIFY_PRIORITY_WARNING` | `5` |
| failure | `GOTIFY_PRIORITY_FAILURE` | `8` |

## Webhooks

`WEBHOOK_ENABLED=true` plus `WEBHOOK_ENDPOINTS`, a comma-separated list of names. Each
name expands to a block of `WEBHOOK_<NAME>_` keys:
`URL`, `FORMAT`, `METHOD`, `AUTH_TYPE`, `AUTH_TOKEN`, `AUTH_USER`, `AUTH_PASS`,
`AUTH_SECRET`, `HEADERS`, `PRIORITY`.

- **Fan-out.** Every endpoint is tried. The channel succeeds if **at least one**
  endpoint succeeds; it reports an error only when all fail (`all N endpoints failed`).
- **Formats.** `discord`, `slack`, `teams`, `pushover`, `generic` (default; an unknown
  format falls back to `generic` with a warning).
- **Auth types.** `bearer` (`Authorization: Bearer`), `basic`
  (`Authorization: Basic base64(user:pass)`), `hmac-sha256` (`X-Signature` plus
  `X-Signature-Algorithm: hmac-sha256`), or none.
- **Retry.** Per endpoint, `WEBHOOK_MAX_RETRIES` (default `3`) with `WEBHOOK_RETRY_DELAY`
  (default `2s`). `2xx` succeeds; `400/401/403/404` are not retried; `429` retries after
  a cooldown; `5xx` and unexpected codes retry. The protected headers `host`,
  `content-length`, `content-type`, and `transfer-encoding` are stripped from any
  custom `HEADERS`.

**Pushover** is special-cased: the app `token` and `user` key go in the JSON **body**
(not headers), `PRIORITY` is constrained to `-2..1` (emergency `2` is unsupported), and
`METHOD` must be `POST`. Because the body carries credentials, the debug payload
preview is suppressed (`Payload preview omitted: pushover payload contains
credentials`). For Discord and Slack the endpoint URL is itself the secret, so on a
request error the URL is redacted via `RedactSecrets` before logging.

## Security model and log redaction

The redaction rules are deliberate and asymmetric. What gets registered with the
logger for scrubbing, and what deliberately does not:

| Value | Registered as a log secret? | Why |
|-------|-----------------------------|-----|
| Telegram bot token | yes | confidential |
| Telegram relay secret (`.notify_secret`) | yes | confidential per-server credential |
| Gotify token | yes | confidential; lives in the URL query |
| Webhook endpoint URL / token / secret / pass | yes | the URL is often the secret (Discord, Slack) |
| Cloudflare relay worker token + HMAC secret | **no** | shared public anti-abuse credential, not confidential |
| Portal magic-link (`login_url`) | **no** | must stay visible when printed; guarded by `SanitizeLoginURL` instead |

The primitives (`internal/logging/redact.go`):

- **`MaskSecret`** renders a fixed 12-asterisk prefix plus the last 4 characters;
  secrets of 8 runes or fewer are fully masked. The prefix is fixed-width so the mask
  never leaks the real length.
- **`RedactSecrets`** replaces every occurrence of each secret in both raw and
  URL-encoded form, skips secrets shorter than 6 runes, and orders the forms
  longest-first so a short secret cannot partially mask a longer one.
- **`RedactURLError`** unwraps a `*url.Error` and keeps only `<op>: <error>`, dropping
  the full URL that Go prints verbatim (request URLs to the central servers embed
  low-capability values like `server_id` in the query).

The `serverbot` transport composes these: a `TransportError` is **pre-redacted** at
construction (the `*url.Error` URL stripped and the per-request secret masked), so it
is safe to log as-is. Debug logging in the transport is body-free and secret-free
(method, path, and status only). One caveat: `Response.Snippet` strips non-printable
runes for console safety but is **not** secret-redacted, so a caller must wrap it with
`RedactSecrets` when the per-request secret could appear in the body.

## Developer notes

### The `serverbot` transport

`internal/serverbot` is the single host-to-bot-server transport, and the **only**
thing on the host that talks to `bot.proxsave.dev`. Telegram and Healthchecks
delivery happen beyond the bot-server, not from the host directly. Key contracts:

- It is a **leaf**: it owns headers, a per-request timeout (default 5 s), a bounded
  response read (default 8192 bytes), and transport-error redaction, and nothing else.
  It carries no endpoint paths, query keys, DTOs, or HTTP-status semantics; those live
  in the callers (`notify`, `health`). A Makefile guard (`check-serverbot-leaf`)
  fails the build if it imports `internal/health`, `internal/notify`,
  `internal/orchestrator`, `internal/config`, or `internal/identity` (the packages
  that would create an import cycle and drag endpoint vocabulary back into the
  transport); the intended contract is `internal/logging`, `internal/version`, and
  the standard library only.
- **An HTTP status is never an error.** `Client.Do` returns `(Response, nil)` for any
  completed exchange, including non-2xx; the caller inspects `Response.Status`. An
  error comes back only on an encode, build, dial, or read failure, and it is a
  pre-redacted `*TransportError`. `AuthRejected(status)` is a helper for the shared
  `401`/`403` concept, not an error path.
- The `serverID` and secret are **per-request**, not on the client. One stateless
  client serves the no-secret `get-chat-id` call, the authenticated `notify` call, and
  the `confirm-secret` call. Headers are stamped conditionally: `X-Proxsave-Version`
  always, `X-Server-Auth` only when a secret is present, `X-Proxsave-Provision: 1` only
  when provisioning, `X-Notify-Id` only when set.
- It must never be pointed at `api.telegram.org` or the `hc.proxsave.dev` monitor.

### Adding a notifier

A channel is anything that implements `notify.Notifier`
(`Name`, `IsEnabled`, `Send`, `IsCritical`). To add one:

1. Implement the interface. `IsCritical()` **must** return `false`, and `Send` should
   return a `*NotificationResult` with `Success=false` on failure rather than a
   non-nil error where possible; a notification must never abort the backup.
2. Register secrets (`RegisterSecret`) for any token or URL that could appear in a log
   or an error before you make the first request.
3. Wire it in `initializeBackupNotifications` (skip with a `disabled` log line when its
   `*_ENABLED` flag is off) and add it to the fixed `dispatchNotifications` entries,
   keeping Healthchecks last.
4. The adapter records the per-channel severity into `.notify_results.json` for you, so
   the daemon can raise a `proxsave-notify-<name>` sensor without further work.

## Troubleshooting

| Symptom | Likely cause | Where to look |
|---------|--------------|---------------|
| Telegram: "sent to ProxSave server" but "delivery not confirmed" | acceptance ok, poll ended before a definite answer (durable outbox still retrying) | raise `TELEGRAM_CONFIRM_TIMEOUT_SECONDS`, or accept it (delivery is best-effort) |
| Telegram: "not delivered (bot blocked by the user)" | the user blocked the bot | unblock the bot, re-pair |
| Telegram: "could not send to ProxSave server" repeatedly | stale relay secret or unknown server | the client reprovisions once automatically; if it persists, re-pair (`--install` Telegram step) |
| `426` on `get-chat-id` | server needs a newer client to finish pairing | upgrade ProxSave to v0.28.0 or later |
| `notify-telegram` sensor DOWN but run green | message accepted but not delivered (Tier 2 is stricter than Tier 1) | fix the delivery cause above; the run staying green is by design |
| Email relay `INVALID_SIGNATURE` | report shape changed, HMAC no longer matches; or a private worker misconfigured | keep the stock report shape; check `CLOUDFLARE_*` if self-hosting |
| Email: "recipient is not allowed (root accounts are blocked)" | `relay` method with a `root@` recipient and no sendmail fallback | set a real recipient, enable `EMAIL_FALLBACK_SENDMAIL`, or use `sendmail`/`pmf` |
| No portal link printed | link already consumed (first login done), or it failed the sanitizer | expected after first login; minting is best-effort and quiet |

See [CONFIGURATION.md](CONFIGURATION.md) for every key, [DAEMON.md](DAEMON.md) for the
monitoring sensors, and [INSTALL.md](INSTALL.md) for the Telegram pairing wizard.
