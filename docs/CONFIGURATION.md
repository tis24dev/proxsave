# Configuration Reference

Complete reference for all 200+ configuration variables in `configs/backup.env`.

Most installs never need to open this file. Run `proxsave` with no arguments on a terminal
and the dashboard's configuration form covers the settings a typical host changes; the
[map below](#editing-the-configuration-from-the-dashboard) says which block it owns and
which blocks are hand-edited. Reach for the file itself for everything the form does not
ask about, and for hosts with no terminal.

## Table of Contents

- [Editing the configuration from the dashboard](#editing-the-configuration-from-the-dashboard)
- [Configuration File Location](#configuration-file-location)
- [Configuration integrity check](#configuration-integrity-check)
- [General Settings](#general-settings)
- [Scheduler engine](#scheduler-engine)
- [Personal scripts (daemon)](#personal-scripts-daemon)
- [Healthchecks connector (daemon)](#healthchecks-connector-daemon)
- [Restore Behavior & Dual-Role Hosts](#restore-behavior--dual-role-hosts)
- [Security Settings](#security-settings)
- [Disk Space](#disk-space)
- [Storage Paths](#storage-paths)
- [Compression Settings](#compression-settings)
- [Advanced Optimizations](#advanced-optimizations)
- [Network Preflight](#network-preflight)
- [Collection Exclusions](#collection-exclusions)
- [Secondary Storage](#secondary-storage)
- [Cloud Storage (rclone)](#cloud-storage-rclone)
- [Storage Comparison](#storage-comparison)
- [rclone Settings](#rclone-settings)
- [Batch Deletion (Cloud)](#batch-deletion-cloud)
- [Retention Policies](#retention-policies)
- [Encryption & Bundling](#encryption--bundling)
- [Notifications](#notifications)
- [Metrics - Prometheus](#metrics---prometheus)
- [Collector Options](#collector-options)
- [Custom Paths & Blacklist](#custom-paths--blacklist)

---

## Editing the configuration from the dashboard

`proxsave`, typed bare on a terminal, opens the dashboard. `Maintenance` -> `Install` ->
`Edit install` runs the installer against the current file: answer `Edit existing` at the
first question and the configuration form comes up pre-filled from `backup.env`, so a pass
that changes no field keeps every value you had. (`Overwrite` starts from the shipped
template instead, and `Keep existing & continue` skips the form altogether.) The dashboard
opens before the configuration is loaded, so this route also works on a host whose
`backup.env` is missing or broken.

An `Edit existing` pass is not byte-for-byte identical, though, and two of its
normalizations are worth knowing: it removes any `BASE_DIR`, `CRON_SCHEDULE`, `CRON_HOUR`
and `CRON_MINUTE` lines, which are derived at runtime and deprecated in the file (see
[Storage Paths](#storage-paths)), and with email enabled it drops the transitional
`EMAIL_FALLBACK_PMF` after carrying its value onto `EMAIL_FALLBACK_SENDMAIL`.

The form has fourteen fields and writes exactly the variables below. Everything else in
this reference is hand-edited.

| Form field | Variables it writes | Section here |
|---|---|---|
| Secondary storage | `SECONDARY_ENABLED` | [Secondary Storage](#secondary-storage) |
| Secondary backup path | `SECONDARY_PATH` | [Secondary Storage](#secondary-storage) |
| Secondary log path | `SECONDARY_LOG_PATH` | [Secondary Storage](#secondary-storage) |
| Cloud backups (rclone) | `CLOUD_ENABLED` | [Cloud Storage](#cloud-storage-rclone) |
| Rclone backup remote | `CLOUD_REMOTE` | [Cloud Storage](#cloud-storage-rclone) |
| Rclone log remote | `CLOUD_LOG_PATH` | [Cloud Storage](#cloud-storage-rclone) |
| Backup firewall rules | `BACKUP_FIREWALL_RULES` | [System Collectors](#system-collectors) |
| Telegram notifications | `TELEGRAM_ENABLED`, plus `BOT_TELEGRAM_TYPE` unless the edited file already sets it | [Telegram](#telegram) |
| Email notifications | `EMAIL_ENABLED` | [Email](#email) |
| Email delivery mode | `EMAIL_DELIVERY_METHOD` (and `EMAIL_FALLBACK_SENDMAIL`, preserved when already set) | [Email](#email) |
| Backup encryption (AGE) | `ENCRYPT_ARCHIVE` | [Encryption & Bundling](#encryption--bundling) |
| Scheduler engine | `SCHEDULER_MODE` | [Scheduler engine](#scheduler-engine) |
| Healthchecks | `HEALTHCHECK_ENABLED`, `HEALTHCHECK_MODE` | [Healthchecks connector](#healthchecks-connector-daemon) |
| Run at (HH:MM) | `SCHEDULER_TIME` | [Scheduler engine](#scheduler-engine) |

Note what the form does **not** write, because it is easy to assume otherwise:
`CLOUD_REMOTE_PATH` is not one of its fields (only `CLOUD_REMOTE` and `CLOUD_LOG_PATH`
are), and no retention, compression, security, collector, metrics or custom-path variable
is on it.

Four other dashboard rows change configuration or configuration-adjacent state:

| Dashboard row | What it does |
|---|---|
| `Maintenance` -> `Upgrade` -> `Check config` | merges variables the shipped template has and your file lacks (`--upgrade-config`), after showing you the list |
| `Maintenance` -> `New key` | generates the AGE recipient that `AGE_RECIPIENT_FILE` points at |
| `Daemon` -> `Install` / `Disable` | switches `SCHEDULER_MODE` between `daemon` and `cron`, and turns `HEALTHCHECK_ENABLED` on and back off with it |
| `Diagnostic Checks` -> `Telegram` / `Healthchecks` | verifies what the notification and monitoring variables above actually do, without running a backup |

Everything else in this file is edited with a text editor. Two rules that apply whichever
way you edit: an assignment that repeats later in the file wins over the earlier one, the
four concatenating variables aside (see
[Configuration integrity check](#configuration-integrity-check)), and values are read as
plain text, not shell (see [Personal scripts](#personal-scripts-daemon) for what `#` and
`$` do to a value).

---

## Configuration File Location

**Default**: `configs/backup.env`, resolved under the detected install directory (`BASE_DIR`), so typically `/opt/proxsave/configs/backup.env`.
**Custom**: specify with `--config`. An absolute path is used as-is; a relative path is joined onto the install directory, not the current working directory.

```bash
# Use custom config file
proxsave --config /path/to/my-backup.env
```

`--config` is an automation flag: passing it, like passing any flag, skips the dashboard,
so a run with a custom configuration file is always a non-interactive one. Use it for a
second profile (a snapshot or fixture run, for example) rather than for everyday editing.

---

## Configuration integrity check

Every run audits `backup.env` against the template embedded in the binary, right after loading
it and before the effective settings are printed:

```text
INFO     Configuration integrity check:
DEBUG    Configuration integrity: file=/opt/proxsave/configs/backup.env lines=475 assignments=182 distinct=182 template=182
DEBUG    Configuration integrity: template assigns 182 variables and documents 33 more as commented examples
DEBUG    Configuration integrity: variables that may repeat without discarding: AGE_RECIPIENT, BACKUP_BLACKLIST, BACKUP_EXCLUDE_PATTERNS, CUSTOM_BACKUP_PATHS
DEBUG    Configuration integrity: 0 duplicated, 0 absent, 0 unknown, 0 legacy (duration=3.1ms)
INFO     ✓ Configuration file ok
```

It reports four things, one line each, and their levels differ because the facts differ:

| Finding | Level | Meaning |
|---------|-------|---------|
| duplicated | `WARNING` | the variable is assigned more than once, so a value you wrote is discarded |
| absent | `WARNING` | the binary carries the variable in its embedded template, the file does not |
| unknown | `INFO` | the file assigns a variable the binary does not read at all: a misspelling |
| legacy | `WARNING` or `INFO` | the file uses a [legacy name](#legacy-key-names). `WARNING` when the canonical name is set too, because then one of the two lines has no effect; `INFO` when the legacy name is the only one |

```text
WARNING    PERSONAL_SCRIPT_PRE_RUN is set twice; line 456 wins and the value on line 120 is discarded
WARNING    HEALTHCHECK_UPDATES_ID is absent and falls back to its default
WARNING    LOCAL_BACKUP_PATH is the legacy name for BACKUP_PATH and both are set; LOCAL_BACKUP_PATH wins and BACKUP_PATH has no effect
INFO       RCLONE_REMOTE is the legacy name for CLOUD_REMOTE and is still read; rename it to CLOUD_REMOTE when convenient
INFO       PERSONAL_SCRIPTS_PRERUN is not a known variable and is ignored
WARNING  ⚠ Configuration file: 1 duplicated, 1 absent, 1 unknown, 2 legacy
```

### Duplicated: the one that loses data

A repeated variable is resolved **last-wins**. The last assignment in the file is kept and every
earlier one is discarded, silently, whatever it held. Thirty-six of the template's 182 variables
ship as an empty line, the personal scripts among them, so adding your own line **above** one of
them loses it, while the same file with the two lines swapped works:

```bash
PERSONAL_SCRIPT_PRE_RUN=/home/me/mount-pbs   # discarded: an assignment below wins
...
PERSONAL_SCRIPT_PRE_RUN=                     # the template line, empty, and it wins
```

Four variables can be repeated without losing anything, because a second `KEY=value` line
**concatenates** instead of overwriting: `AGE_RECIPIENT`, `BACKUP_BLACKLIST`,
`BACKUP_EXCLUDE_PATTERNS` and `CUSTOM_BACKUP_PATHS`.

The exemption is the FORM, not the name. `BACKUP_BLACKLIST` and `CUSTOM_BACKUP_PATHS` also accept
the multi-line block form the template ships them in, and a block does **not** concatenate: it
replaces everything set before it. Writing your own line above the template's block therefore
loses it, exactly like an ordinary duplicate, and the audit reports it:

```bash
CUSTOM_BACKUP_PATHS=/srv/important   # discarded: the block below replaces it
CUSTOM_BACKUP_PATHS="                # the template's block, and it wins
# /srv/custom-config.yaml
"
```

A line written **after** the block concatenates onto it and loses nothing.

### Absent: the merge never ran

The template is compiled into the binary, so a host that has not upgraded carries an older
binary with an older template and never sees this finding. Seeing it means the binary is new and
`backup.env` was not merged.

Fix it from the dashboard with `Maintenance` -> `Upgrade` -> `Check config`: the check lists
the variables it would add and only offers `Apply` when there is something to add, and the
apply takes a backup it can roll back to. The same two steps from a script are
`--upgrade-config-dry-run` (shows what it would add) and `--upgrade-config` (adds it).

A missing variable falls back to its default, so nothing you wrote is lost and the backup itself
is unaffected. The finding is still a `WARNING`, and like every other warning it promotes the run
to exit 1: a host that upgraded the binary without merging its `backup.env` exits 1 on every run,
and the Healthchecks backup check goes down with it, until the merge is run. Merge the
file, either way, or expect that state until you do.

### What the block does not do

Values are never printed, not even at debug level: a duplicated `TELEGRAM_BOT_TOKEN` would put a
secret in the log, and the line number locates it just as well. Nothing is rewritten and the
last-wins rule is unchanged; the audit only stops it from being silent.

The `WARNING` lines count towards the run's warning total and its exit code, like every other
warning. The same block runs under `proxsave --daemon-status`.

---

## General Settings

```bash
# Enable/disable backup system
BACKUP_ENABLED=true                # true | false

# Colored output in terminal
USE_COLOR=true                     # true | false

# Colorize "Step N/8" lines in logs
COLORIZE_STEP_LOGS=true            # true | false (requires USE_COLOR=true)

# Debug level
DEBUG_LEVEL=standard               # standard | advanced | extreme

# Dry-run mode (test without changes)
DRY_RUN=false                      # true | false

# Enable/disable always-on pprof profiling (CPU + heap)
PROFILING_ENABLED=true             # true | false (profiles written under /tmp/proxsave)
```

### DEBUG_LEVEL Details

| Level | Description |
|-------|-------------|
| `standard` | Basic operation logging |
| `advanced` | Detailed command execution, file operations |
| `extreme` | Full verbose output including rclone/compression internals |

At the config layer `standard` resolves to the `info` log level and both `advanced` and `extreme` resolve to `debug`. `--log-level` overrides `DEBUG_LEVEL` for a run (see [CLI_REFERENCE.md](CLI_REFERENCE.md)).

---

## Scheduler engine

ProxSave runs the backup either from a resident systemd daemon or from a cron entry. The
daemon is the normal engine and a fresh install selects it; cron is the legacy opt-out. The
behavior is documented in [DAEMON.md](DAEMON.md).

**From the dashboard**: the `Daemon` group offers the one command that fits the current
state (`Install` on a cron host, `Disable` and `Restart` on a daemon host) plus a read-only
`Status`. The daily run time and the engine are also on the configuration form, as
**Run at (HH:MM)** and **Scheduler engine**. The keys behind all of that are:

```bash
SCHEDULER_MODE=cron            # cron | daemon (any unrecognized value normalizes to cron)
SCHEDULER_TIME=02:00           # daily HH:MM "Run at" time
MAX_RUN_DURATION=1h            # daemon watchdog: hard timeout for one backup
```

The compiled default for `SCHEDULER_MODE` is `cron`, but a fresh install defaults to the daemon and writes `SCHEDULER_MODE=daemon`.

The key's **presence** matters as much as its value. `--upgrade` installs the daemon only on a host where that same upgrade's config merge had to add `SCHEDULER_MODE`, i.e. one that has never recorded an engine. Once the line is in the file the value is honoured and no upgrade revisits the host.

`MAX_RUN_DURATION` and the personal-script keys below are daemon-only and are on no form;
edit them here.

---

## Personal scripts (daemon)

Two scripts of your own, started by the resident daemon around each supervised run. Empty by
default, which means nothing is started. The full contract is in [DAEMON.md](DAEMON.md); the
keys are:

```bash
PERSONAL_SCRIPT_PRE_RUN=       # path to a script started before the run (works only with the daemon)
PERSONAL_SCRIPT_POST_RUN=      # path to a script started after the run, whatever the outcome (works only with the daemon)
```

Both take a filesystem path and nothing else: the path is executed directly, with no shell, so
arguments, pipes and redirections in the value are not interpreted. Two characters need care,
as in every other value in this file. An unquoted `#` truncates the value, so quote a path that
contains one. A `$` is expanded and no form of quoting suppresses it, not double quotes, not
single quotes, not a backslash: the name is looked up in this file first, then in the daemon's
own environment, and a name neither defines is replaced by nothing, silently. `$$`, `$@`, `$*`,
`$!`, `$?`, `$-`, `$0` to `$9` and a `${` with no closing brace are consumed the same way. Only
a `$` at the end of the value, or one before a character that is neither a letter, a digit, an
underscore nor one of those, survives. Rename the script rather than fighting the expansion.

These scripts belong to you. ProxSave starts them and reports nothing about them: their output
is discarded, their exit code is ignored, they never fail a backup or change its exit code, and
they appear in no log, notification, ping or metric. Each is killed after 10 minutes, on every
path but the abandoned-child unwind described in [DAEMON.md](DAEMON.md). They run
only under the daemon, never under a manual `proxsave --backup` or a cron-mode run.

The one line ProxSave will ever log about them is a refusal at daemon start: the path must not
traverse a symlink, it and every directory above it must belong to root (or the user the
daemon runs as), the script must not be writable by group or others, and a group- or
other-writable directory must carry the sticky bit. A path that fails is disabled for that
daemon with a `WARNING` naming the variable, the path and the reason (see
[SECURITY.md](SECURITY.md)).

---

## Healthchecks connector (daemon)

The daemon can push to an external [healthchecks](https://healthchecks.io/) monitor. The four checks, the monitoring portal, and the centralized-vs-self behavior are documented in [HEALTHCHECKS.md](HEALTHCHECKS.md).

**From the dashboard**: the configuration form's **Healthchecks** field offers `Off`,
`ProxSave HC Server` (centralized) and `Your own server` (self). It is active only when the
scheduler engine is the daemon, because the daemon is the only thing that pings. Choosing
`Your own server` leads to a follow-up screen for the ping URLs: the alive and backup URLs
are required there, the updates and per-channel ones optional. Whatever is configured,
`Diagnostic Checks` -> `Healthchecks` verifies it and shows the portal details without
running a backup. The keys are:

```bash
HEALTHCHECK_ENABLED=false      # true with the daemon (--daemon-setup / --upgrade); back to false by --daemon-remove
HEALTHCHECK_MODE=centralized   # centralized (fetch URLs from the server) | self
HEALTHCHECK_HEARTBEAT_INTERVAL=5m
HEALTHCHECK_UPDATE_INTERVAL=5m
HEALTHCHECK_SEND_LOG=true

# Dual purpose. In SELF mode these are the two required ping URLs and you fill them in
# yourself (they take precedence over the *_ID keys below). In CENTRALIZED mode they are
# a fallback cache that nothing auto-fills, so leave them empty unless you deliberately
# want one.
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

`HEALTHCHECK_ENABLED` is the only switch. `HEALTHCHECK_MODE` selects *how* the ping URLs are
obtained, and the loader recognizes exactly one value, `self`; anything else, `off`
included, resolves to `centralized`. Choosing `Off` on the form therefore writes
`HEALTHCHECK_MODE=off` **and** `HEALTHCHECK_ENABLED=false`, and it is the second line that
turns the connector off. Setting the mode to `off` by hand while leaving
`HEALTHCHECK_ENABLED=true` does not disable anything. The form also clears
`HEALTHCHECK_ALIVE_URL` and `HEALTHCHECK_BACKUP_URL` when it changes the mode, so a
leftover self URL cannot linger as the centralized cache; a same-mode pass leaves them
alone.

`HEALTHCHECK_ENABLED` parses as `false` by default. `--daemon-setup` and the `--upgrade` retrofit set it to `true` when enabling the daemon, and `--daemon-remove` sets it back to `false` when reverting to cron. The two directions belong together: the checks this key turns on are daemon-only, so a cron host left with `HEALTHCHECK_ENABLED=true` reports the missing daemon on every run, as a warning on either engine. On cron the warning names the engine as well, because the key is asking for monitoring that cannot run there. Clearing the key is what stops the report, which is why `--daemon-remove` does it for you.

A host reverted by an older build kept the key at `true`, and nothing rewrites it for that host: it reports the missing daemon on every run and exits `1` until you set `HEALTHCHECK_ENABLED=false` yourself, or run `--daemon-setup` then `--daemon-remove`. ProxSave does not correct it, because on disk that host looks exactly like one whose operator set the key deliberately. Only hosts reverted before this release are affected.

---

## Restore Behavior & Dual-Role Hosts

Restore behavior is chosen **interactively at restore time** when PBS-specific
categories are going to be applied. This is not configured through `backup.env`.

You will be asked to choose a behavior:
- **Merge (existing PBS)**: intended for restoring onto an already operational PBS; ProxSave applies supported PBS categories via `proxmox-backup-manager` without deleting existing objects that are not in the backup.
- **Clean 1:1 (fresh PBS install)**: intended for restoring onto a new, clean PBS; ProxSave attempts to make supported PBS objects match the backup (may remove objects that exist on the system but are not in the backup).

ProxSave applies supported PBS staged categories via API automatically (and may fall back to file-based staged apply only in **Clean 1:1** mode).

### Dual-role hosts

ProxSave automatically detects the current host role as one of:

- `pve`
- `pbs`
- `dual`
- `unknown`

There is no dedicated `backup.env` switch for `dual`. On a co-installed host,
`dual` is detected automatically and a single backup run can include both PVE
and PBS payloads plus one shared `common/system` payload.

Dual backups persist explicit target metadata (`BACKUP_TYPE=dual`,
`BACKUP_TARGETS=pve,pbs`) and restore uses that metadata to filter categories
to the roles supported by the current host.

**Current API coverage**:
- Node + traffic control (`pbs_host`)
- Datastores + S3 endpoints (`datastore_pbs`)
- Remotes (`pbs_remotes`)
- Jobs (sync/verify/prune) (`pbs_jobs`)
- Notifications endpoints/matchers (`pbs_notifications`)

---

## Security Settings

```bash
# Security preflight check
SECURITY_CHECK_ENABLED=true                     # true | false

# Auto-update file hashes
AUTO_UPDATE_HASHES=true                         # true | false

# Auto-fix permissions
AUTO_FIX_PERMISSIONS=true                       # true | false

# Block backup on security issues
CONTINUE_ON_SECURITY_ISSUES=false               # false = block, true = warn

# Network security checks
CHECK_NETWORK_SECURITY=false                    # true | false
CHECK_FIREWALL=false                            # true | false
CHECK_OPEN_PORTS=false                          # true | false

# Suspicious port list (space-separated)
SUSPICIOUS_PORTS="6666 6665 1337 31337 4444 5555 4242 6324 8888 2222 3389 5900"

# Port whitelist (format: service:port)
PORT_WHITELIST=                                 # e.g., "sshd:22,nginx:443"

# Suspicious process names (comma-separated)
# NOTE: Your values are ADDED to the built-in defaults (not replaced)
# Built-in defaults: ncat, cryptominer, xmrig, kdevtmpfsi, kinsing, minerd, mr.sh
SUSPICIOUS_PROCESSES="ncat,cryptominer,xmrig,kdevtmpfsi,kinsing,minerd,mr.sh"

# Safe process names for the bracketed "kernel-style" process warning.
# That warning ("Suspicious kernel-style process: ...") fires for any process the
# host's `ps` reports inside square brackets, e.g. a real kernel thread
# `[kworker/0:1]` or a container worker an unprivileged LXC exposes to the host as
# `[celeryd: celery@paperless:ForkPoolWorker-3057]`. Both lists below are checked.
# NOTE: Your values are ADDED to the built-in defaults (not replaced).
# Matching is against the text BETWEEN the brackets, case-insensitive, and a plain
# entry is an EXACT whole-name match (NOT a prefix). Use "name*" (prefix) or
# "regex:pattern" (unanchored) to match part of the name. So the celery worker
# above is matched by `celeryd*` or `regex:^celeryd`, but NOT by a plain `celeryd`.
# Built-in defaults for SAFE_BRACKET_PROCESSES: sshd:, systemd, cron, rsyslogd, dbus-daemon, zvol_tq*, arc_*, dbu_*, dbuf_*, l2arc_feed, lockd, nfsd*, nfsv4 callback*
SAFE_BRACKET_PROCESSES="sshd:,systemd,cron,rsyslogd,dbus-daemon"

# Built-in defaults for SAFE_KERNEL_PROCESSES: ksgxd, hwrng, usb-storage, vdev_autotrim, card1-crtc0, card1-crtc1, card1-crtc2, kvm-pit*, psimon, regex:^kvm-pit/[0-9]+$, regex:^worker/.+-drbd_as_pm-.*
# Your entries are ADDED to those defaults; the value below is just an example of extra patterns.
SAFE_KERNEL_PROCESSES="regex:^card[0-9]+-crtc[0-9]+$,regex:^drbd_[wrs]_.+,regex:^kmmpd-drbd[0-9]+$"

# Allowlist for the suspicious-process scan ONLY (no built-in defaults; purely user-driven).
# This list does NOT silence the bracketed "kernel-style" warning above; use
# SAFE_BRACKET_PROCESSES / SAFE_KERNEL_PROCESSES for those.
# A process is never flagged by the suspicious-process scan if any token of its
# command line (or that token's basename) matches an entry, even if it also matches
# SUSPICIOUS_PROCESSES.
# Matching is anchored to the start of each token: a plain entry matches any token that
# STARTS WITH it (e.g. "ssh" also matches "sshd"), so use "regex:^name$" for an exact match.
# "name*" wildcard and "regex:pattern" are also supported (case-insensitive).
# Useful for benign tools such as Frigate's ffmpeg -f concat demuxer.
# SAFE_PROCESSES="ffmpeg"

# Permission management (Bash-compatible behavior)
BACKUP_USER=backup                              # System user for backup/log directory ownership
BACKUP_GROUP=backup                             # System group for backup/log directory ownership
SET_BACKUP_PERMISSIONS=false                    # true = apply chown/chmod on backup/log directories
```

### Security Check Behavior

- Verifies file permissions (0700 for directories, 0600 for sensitive files)
- Checks for suspicious open ports
- Scans for suspicious processes
- Validates file hashes to detect tampering
- **If `CONTINUE_ON_SECURITY_ISSUES=false`**: Backup aborts on any issue
- **If `CONTINUE_ON_SECURITY_ISSUES=true`**: Issues logged as warnings, backup continues

#### Process List Merge Behavior

All security process lists use an **additive merge strategy**:

- **`SUSPICIOUS_PROCESSES`**: Your configured values are **added** to built-in defaults
- **`SAFE_BRACKET_PROCESSES`**: Your configured values are **added** to built-in defaults
- **`SAFE_KERNEL_PROCESSES`**: Your configured values are **added** to built-in defaults
- **`SAFE_PROCESSES`**: Allowlist for the suspicious-process scan; it has **no built-in defaults**, so the final list is exactly your configured entries

**Example**: If you configure `SUSPICIOUS_PROCESSES="mymalware,suspicious-app"`, the final list will include:
- Built-in defaults: `ncat, cryptominer, xmrig, kdevtmpfsi, kinsing, minerd, mr.sh`
- Your additions: `mymalware, suspicious-app`
- Final result: All of the above combined (duplicates automatically removed)

This means you don't need to repeat the default values - just add your custom entries.

#### Process Matching

proxsave runs two independent process detectors, and their allowlists are **not** interchangeable. Pick the list that matches the warning you see.

**Suspicious-process scan** (`SUSPICIOUS_PROCESSES`, allowlisted by `SAFE_PROCESSES`) is matched per command-line token (and each token's path basename), anchored to the **start** of the token:

- A **plain entry** matches any token that **starts with** it. For example `ncat` matches `ncat` and `/usr/bin/ncat`, but no longer matches the substring inside `concat`; note that `ssh` would also match `sshd`.
- Use **`regex:^name$`** for an exact match, or **`name*`** / **`regex:pattern`** for explicit wildcard/regex control (case-insensitive).

**Bracketed "kernel-style" detector** (`SAFE_BRACKET_PROCESSES` and `SAFE_KERNEL_PROCESSES`) handles the `Suspicious kernel-style process: ...` warning. It fires for a process the host's `ps` reports inside square brackets that is not recognised as a genuine kernel thread. Real kernel threads are filtered out before your lists are consulted, by a built-in prefix allowlist (`kworker`, `kthreadd`, `kswapd`, `rcu_`, `nfsd`, the ZFS `arc_`/`txg_`/`z_`/`spl_` families and about forty more) and then by a parent-is-pid-2 heuristic, so `[kworker/0:1]` never raises it. What does raise it is something like a container worker an unprivileged LXC exposes to the host (`[celeryd: celery@paperless:ForkPoolWorker-3057]`). It matches a **single name** (the text *between* the brackets), case-insensitively, and behaves differently from the scan above:

- A **plain entry is an exact, whole-name match** (not a prefix). `celeryd` does **not** match `celeryd: celery@paperless:ForkPoolWorker-3057`. This applies to the entries you configure; the built-in kernel allowlist above matches on prefix.
- Use **`name*`** for a prefix match or **`regex:pattern`** for an unanchored regex. The celery worker above is matched by `celeryd*` or `regex:^celeryd`, but not by a plain `celeryd` or by an anchored `regex:^celeryd$`.
- `SAFE_PROCESSES` has **no effect** on this detector. Allowlist bracketed processes via `SAFE_BRACKET_PROCESSES` (or `SAFE_KERNEL_PROCESSES`).

### Permission Management

When `SET_BACKUP_PERMISSIONS=true`, the system applies Bash-compatible ownership and permissions:

**Ownership (chown)**:
- Recursively changes owner:group for:
  - `BACKUP_PATH` (primary backup directory)
  - `LOG_PATH` (primary log directory)
  - `SECONDARY_PATH` (if configured)
  - `SECONDARY_LOG_PATH` (if configured)
- Uses `BACKUP_USER:BACKUP_GROUP` as the target owner
- Does NOT touch binary files, config files, or system paths

**Permissions (chmod)**:
- Applies mode `0750` (rwxr-x---) to directories only
- Files keep their existing permissions (unchanged)
- Conservative and safe approach

**Requirements**:
- Both `BACKUP_USER` and `BACKUP_GROUP` must be set
- User and group must already exist on the system
- **Does NOT create users or groups** (unlike legacy Bash version)

**Error handling**:
- Non-fatal: All failures logged as warnings
- Backup continues even if permission changes fail
- User/group not found: logs warning and skips operation
- Backup/log paths on non-POSIX filesystems such as CIFS/SMB, NTFS, FAT/exFAT, FUSE, or network filesystems without Unix ownership support are detected and skipped for `chown`/`chmod` and security permission warnings. Windows-backed CIFS shares are expected to manage permissions on the Windows side.

**Use cases**:
- Migration from legacy Bash version
- Multi-user environments requiring specific ownership
- Shared backup storage with group access
- POSIX-capable NFS mounts requiring specific ownership

**Example**:
```bash
# Create dedicated backup user/group first
groupadd backup
useradd -r -g backup -s /bin/false backup

# Configure ownership
BACKUP_USER=backup
BACKUP_GROUP=backup
SET_BACKUP_PERMISSIONS=true

# Result: All backup/log directories owned by backup:backup with mode 0750
```

---

## Disk Space

```bash
# Minimum free space required (GB)
MIN_DISK_SPACE_PRIMARY_GB=1        # Primary storage
MIN_DISK_SPACE_SECONDARY_GB=1      # Secondary storage
MIN_DISK_SPACE_CLOUD_GB=1          # Cloud storage (not enforced for remote)
```

**Behavior**: Backup aborts if available space < minimum threshold.

**Defaults**: when a key is absent the compiled fallback is `10` GB, and any value `<= 0` is coerced to `10`. `MIN_DISK_SPACE_SECONDARY_GB` and `MIN_DISK_SPACE_CLOUD_GB` fall back to whatever `MIN_DISK_SPACE_PRIMARY_GB` resolves to. The `1` shown above is an example, not the fallback.

---

## Pre-Backup Permission Check

```bash
SKIP_PERMISSION_CHECK=false        # true = skip the pre-backup permission check (test only)
```

The pre-backup checks verify that the backup and log directories are writable and owned as
expected before anything is collected. `SKIP_PERMISSION_CHECK=true` drops that verification.

Leave it `false` on a real host. It exists for test rigs where the directories are owned by
whoever runs the suite: skipping the check does not fix the ownership, it only stops ProxSave
from telling you about it, and an archive written under the wrong owner can be one nobody but
root reads back.

---

## Storage Paths

```bash
# Base directory for all operations (auto-detected at runtime)
# BASE_DIR is auto-detected from the installed proxsave executable; do not set it
# in backup.env. Active BASE_DIR=... lines are deprecated and ignored.
# BASE_DIR=/opt/proxsave

# Lock file directory
LOCK_PATH=${BASE_DIR}/lock

# Credentials directory
SECURE_ACCOUNT=${BASE_DIR}/secure_account

# Primary backup storage
BACKUP_PATH=${BASE_DIR}/backup

# Primary log storage
LOG_PATH=${BASE_DIR}/log
```

**Path resolution**: `${BASE_DIR}` expands automatically from the installed `proxsave` executable path. Scalar string values also support `$VAR` / `${VAR}` expansion (config keys first, then environment variables), but `BASE_DIR` itself is not configurable from `backup.env` or the parent environment.

### Legacy key names

Seven keys have a legacy alias from the Bash-era configuration, and **the legacy name wins when both are present**:

| Legacy name (wins) | Canonical name |
|---|---|
| `LOCAL_BACKUP_PATH` | `BACKUP_PATH` |
| `LOCAL_LOG_PATH` | `LOG_PATH` |
| `ENABLE_SECONDARY_BACKUP` | `SECONDARY_ENABLED` |
| `SECONDARY_BACKUP_PATH` | `SECONDARY_PATH` |
| `ENABLE_CLOUD_BACKUP` | `CLOUD_ENABLED` |
| `RCLONE_REMOTE` | `CLOUD_REMOTE` |
| `PROMETHEUS_ENABLED` | `METRICS_ENABLED` |

These seven are the ones where the legacy name is checked first. Every other pair goes the other way round: the canonical name is consulted first, so the legacy name on its own still works and setting both leaves the legacy line with no effect. Those pairs are not listed here because there are many of them and they are not the dangerous direction, but the [configuration integrity check](#configuration-integrity-check) names each one it finds in your file, whichever way it goes.

This matters after `--upgrade-config`, which keeps unknown keys in a "Custom keys" section while also adding the template's canonical line, and does not prune any of these. A config inherited from an older install can end up with both, and editing the canonical one then has no effect: the backups keep landing wherever the legacy key points. Grep your `backup.env` for the left column and delete those lines once you have moved the value across, or read them off the [configuration integrity check](#configuration-integrity-check), which lists every one of them by name and says which of the two the loader consults first. Setting both is the case worth acting on, and the check raises a `WARNING` for it: one of the two lines has no effect, and it is not always the one you would guess. These seven are the pairs where the LEGACY name is consulted first; every other alias goes the other way, so there the canonical name wins and the legacy line is the one being ignored. The check states the direction per variable rather than leaving you to remember which list a name belongs to. Two more cloud pairs are listed in [CLOUD_STORAGE.md](CLOUD_STORAGE.md), where the canonical name wins instead, so check that table rather than assuming.

---

## Compression Settings

```bash
# Compression algorithm
COMPRESSION_TYPE=xz                # none | gz | pigz | bz2 | xz | lzma | zst (gzip/bzip2/zstd accepted as aliases)

# Compression level
COMPRESSION_LEVEL=9                # Range depends on algorithm (see table below)

# Compression threads (0 = auto-detect CPU cores)
COMPRESSION_THREADS=0              # 0 = auto, >0 = fixed thread count

# Compression mode
COMPRESSION_MODE=ultra             # fast | standard | maximum | ultra
```

The canonical algorithm values are the short forms `gz`, `pigz`, `bz2`, `xz`, `lzma`, `zst` (plus `none`); `gzip`, `bzip2`, and `zstd` are accepted as aliases. Compiled fallbacks when a key is absent are `COMPRESSION_TYPE=xz`, `COMPRESSION_LEVEL=6`, `COMPRESSION_MODE=standard`. The shipped template sets `9` / `ultra` (the values above), so a copied template compresses harder than the bare defaults.

### Compression Algorithm Details

| Algorithm | Level Range | Notes |
|-----------|-------------|-------|
| `none` | 0 | No compression |
| `gz` (alias `gzip`) | 1-9 | Single-threaded, widely compatible |
| `pigz` | 1-9 | Parallel gzip, faster on multi-core |
| `bz2` (alias `bzip2`) | 1-9 | Higher compression, slower |
| `xz` | 0-9 | Excellent compression, supports `--extreme` |
| `lzma` | 0-9 | Similar to xz |
| `zst` (alias `zstd`) | 1-22 | Fast, good compression (>19 uses `--ultra`) |

### Compression Modes

| Mode | Description |
|------|-------------|
| `fast` | Lower levels, faster execution |
| `standard` | Balanced |
| `maximum` | Level 9 for gz/bz2/xz, level 19 for zst |
| `ultra` | Adds `--extreme` for xz/lzma, level 22 for zst |

### Examples

**Fast backup** (large files, quick compression):
```bash
COMPRESSION_TYPE=zstd
COMPRESSION_MODE=fast
```

`COMPRESSION_MODE` **replaces** `COMPRESSION_LEVEL`, it does not bias it. `fast` forces level 1, `maximum` and `ultra` force 9 (19 and 22 for zstd), whatever you wrote in `COMPRESSION_LEVEL`, with no warning. Set the level only with `COMPRESSION_MODE=standard`.

**Maximum compression** (archival, storage limited):
```bash
COMPRESSION_TYPE=xz
COMPRESSION_LEVEL=9
COMPRESSION_MODE=ultra
COMPRESSION_THREADS=0  # Use all CPU cores
```

**No compression** (already compressed data):
```bash
COMPRESSION_TYPE=none
```

---

## Advanced Optimizations

```bash
# Enable deduplication
ENABLE_DEDUPLICATION=true          # true | false

# Enable prefiltering
ENABLE_PREFILTER=true              # true | false

# Prefilter max file size (MB)
PREFILTER_MAX_FILE_SIZE_MB=8       # Skip prefilter for files >8MB
```

### What These Do

- **Deduplication**: Detects duplicate data blocks (reduces storage)
- **Prefilter**: Applies safe, semantic-preserving normalization to small text/JSON files to improve compression (e.g. removes CR from CRLF line endings and minifies JSON). It does **not** reorder, de-indent, or strip structured configuration files, and it avoids touching Proxmox/PBS structured config paths (e.g. `etc/pve/**`, `etc/proxmox-backup/**`).

### Prefilter (`ENABLE_PREFILTER`): details and risks

**What it does** (on the *staged* backup tree, before compression):
- Removes `\r` from CRLF text files (`.txt`, `.log`, `.md`, `.conf`, `.cfg`, `.ini`) to normalize line endings
- Minifies JSON (`.json`) while keeping valid JSON semantics

**What it does not do**:
- It does **not** reorder lines, remove indentation, or otherwise rewrite whitespace/ordering-sensitive structured configs.
- It does **not** follow symlinks (symlinks are skipped).
- It skips Proxmox/PBS structured configuration paths where formatting/order matters, such as:
  - `etc/pve/**`
  - `etc/proxmox-backup/**`
  - `etc/systemd/system/**`
  - `etc/ssh/**`
  - `etc/pam.d/**`

**Why you might disable it** (even though it's safe):
- If you need maximum fidelity (bit-for-bit) of text/JSON formatting as originally collected (CRLF preservation, JSON pretty-printing, etc.)
- If you prefer the most conservative pipeline possible (forensics/compliance)

**Important**: Prefilter never edits files on the host system; it only operates on the temporary staging directory that will be archived.

---

## Network Preflight

```bash
# Skip network connectivity checks
DISABLE_NETWORK_PREFLIGHT=false    # false = check, true = skip

# Use case: Offline environments without Telegram/email/cloud
```

**Behavior**:
- **false (default)**: Verifies connectivity before using network features
- **true**: Skips checks (operations may fail later if network unavailable)

---

## Collection Exclusions

```bash
# Glob patterns to exclude. Separators: comma, semicolon, pipe or newline. NOT space.
BACKUP_EXCLUDE_PATTERNS="**/cache/**, /var/tmp/**, *.log"
```

### Pattern Syntax

- `*`: matches anything **within one path segment**; it does not cross a `/`
- `**`: matches across separators, so this is the one to use for "at any depth"
- Example: `**/cache/**` excludes `cache/` directories anywhere. `*/cache/**` only matches a `cache` directory one level down, which is almost never what you want
- Separate patterns with commas. A space is **not** a separator: `"*.log */cache/**"` is read as one pattern containing a space, which matches nothing and is not reported as an error

### Exclusion Behavior (Guaranteed)

- Exclusions are enforced consistently for anything that would end up inside the backup archive (files/directories copied from the host, full PVE/PBS snapshots, command outputs under `var/lib/proxsave-info/commands/`, and generated metadata such as `manifest.json` and `var/lib/proxsave-info/backup_metadata.txt`).
- Patterns are matched against both:
  - the original host path (e.g. `/etc/pve/nodes/node1/qemu-server/100.conf`), and
  - the path inside the backup archive (e.g. `etc/pve/nodes/node1/qemu-server/100.conf`).
  This means you can write patterns with or without a leading `/` and get consistent results.

---

## Secondary Storage

**From the dashboard**: all three variables are on the configuration form
(`Maintenance` -> `Install` -> `Edit install`) as **Secondary storage**, **Secondary backup
path** and **Secondary log path**. The form validates both paths against the rules below
before it writes them, which is the easiest way to avoid the mistakes listed under
[What NOT to Do](#what-not-to-do).

```bash
# Enable secondary storage
SECONDARY_ENABLED=false            # true | false

# Secondary backup path (required when SECONDARY_ENABLED=true)
SECONDARY_PATH=/mnt/secondary/backup

# Secondary log path (optional)
SECONDARY_LOG_PATH=/mnt/secondary/log
```

### Use Case

Additional local storage for redundant backup copies - mounted NAS, USB drives, local disks.

### IMPORTANT PATH REQUIREMENTS

- `SECONDARY_PATH` **must be an absolute local filesystem path** (e.g., `/mnt/nas-backup`, `/media/usb-drive`)
- `SECONDARY_LOG_PATH`, when set, must follow the **same absolute local path rules**
- `SECONDARY_LOG_PATH` is optional; when empty, secondary backup copies still run, but secondary log copy/cleanup is disabled
- `SECONDARY_PATH` **CANNOT** be a network address (e.g., `192.168.0.10/folder`, `//server/share`)
- Network shares **must be mounted first** using standard Linux mounting (NFS/CIFS/SMB)

### Network Storage Setup

For NAS or network storage:

**1. First, mount the network share**:
```bash
# NFS example
sudo mount 192.168.0.10:/backup /mnt/nas-backup

# CIFS/SMB example
sudo mount -t cifs //192.168.0.10/backup /mnt/nas-backup -o credentials=/root/.smbcreds

# To make it permanent, add to /etc/fstab
```

**2. Then configure SECONDARY_PATH**:
```bash
SECONDARY_PATH=/mnt/nas-backup  # ✓ Correct - uses mounted path
SECONDARY_LOG_PATH=/mnt/nas-logs  # Optional
```

### What NOT to Do

```bash
SECONDARY_PATH=192.168.0.10/backup       # ✗ WRONG - network address
SECONDARY_PATH=//server/share            # ✗ WRONG - UNC path
SECONDARY_PATH=\\192.168.0.10\backup    # ✗ WRONG - Windows path
```

**For direct network access without mounting:** Use `CLOUD_REMOTE` with rclone instead (see [Cloud Storage](#cloud-storage-rclone) section).

### Behavior

- Secondary storage is **non-critical** (failures log warnings, don't abort backup)
- Files copied via native Go (no dependency on rclone)
- Same retention policy as primary storage
- Invalid configured secondary paths fail fast during configuration loading

---

## Cloud Storage (rclone)

**From the dashboard**: the configuration form has **Cloud backups (rclone)**,
**Rclone backup remote** and **Rclone log remote**, which write `CLOUD_ENABLED`,
`CLOUD_REMOTE` and `CLOUD_LOG_PATH`. That is the whole of its cloud coverage:
`CLOUD_REMOTE_PATH`, the upload mode, the verification switches and every rclone setting
below are edited here. Configure the rclone remote itself (`rclone config`) before turning
the toggle on; the form does not do it for you.

```bash
# Enable cloud storage
CLOUD_ENABLED=false                # true | false

# rclone remote (recommended: remote NAME + path via CLOUD_REMOTE_PATH)
CLOUD_REMOTE=GoogleDrive                   # remote name from `rclone config`
CLOUD_REMOTE_PATH=/proxsave/backup         # folder path inside the remote

# Cloud log path (optional)
# Recommended (same remote): path-only (no remote prefix) and no trailing slash
# CLOUD_LOG_PATH=proxsave/log
# Legacy / different remote: explicit remote:path
# CLOUD_LOG_PATH=OtherRemote:proxsave/log
CLOUD_LOG_PATH=/proxsave/log               # leave empty to disable log uploads

# Legacy compatibility (still supported):
# CLOUD_REMOTE=GoogleDrive:/proxsave/backup        # legacy combined syntax
# CLOUD_REMOTE_PATH=server1                        # extra suffix if needed
# CLOUD_LOG_PATH=GoogleDrive:/proxsave/log         # legacy explicit remote for logs

# Upload mode
CLOUD_UPLOAD_MODE=parallel         # sequential | parallel

# Parallel worker count
CLOUD_PARALLEL_MAX_JOBS=2          # Workers for associated files

# Verify associated/sidecar files in parallel
CLOUD_PARALLEL_VERIFICATION=true   # true | false

# Compare remote SHA256 to the local checksum after upload
CLOUD_VERIFY_CHECKSUM=true         # true | false (size-only fallback when the backend has no native hash)

# Force download-and-hash when the backend lacks a native SHA256
CLOUD_VERIFY_DOWNLOAD=false        # true | false (uses bandwidth)

# Preflight connectivity check
CLOUD_WRITE_HEALTHCHECK=false      # true | false (auto-fallback mode vs force write test)
```

### Recommended Remote Path Formats (Cloud)

To avoid ambiguity, prefer consistent formats:
- `CLOUD_REMOTE`: remote **name** only (no `:`), e.g. `nextcloud` or `GoogleDrive`.
- `CLOUD_REMOTE_PATH`: path **inside** the remote (no remote prefix), **no trailing slash** (leading `/` is accepted).
- `CLOUD_LOG_PATH`: log **folder path**. When logs are on the same remote as backups, prefer **path-only** here too; use `otherremote:/path` only when logs must go to a different remote.

Example (same remote for backups + logs):
```bash
CLOUD_REMOTE=nextcloud-katerasrael
CLOUD_REMOTE_PATH=B+K/BACKUP/marcellus
CLOUD_LOG_PATH=B+K/BACKUP/marcellus/logs
```

### Connectivity Check Modes

| Setting | Mode | Behavior |
|---------|------|----------|
| `false` (default) | **Auto mode** (RECOMMENDED) | Tries fast `rclone lsf` list check first, automatically falls back to write test if permission errors detected (403/401). Best-of-both-worlds approach. |
| `true` | **Force write test** | Skips list check entirely, only uses write test (creates/deletes temp file `.pbs-backup-healthcheck-<timestamp>`). Use if you want to skip list operations completely. |

### How Auto-Fallback Works (when `false`)

1. First attempt: Fast `rclone lsf` to check remote accessibility
2. If permission error detected (403/401/access denied): Automatically tries write test
3. If write test succeeds: Continues with backup (warns about list permission limitation)
4. If both fail: Returns error with clear troubleshooting hints

### When Auto-Fallback Helps

Automatic with `false`:
- ✅ **Cloudflare R2** with restricted API tokens (no list permissions)
- ✅ **S3-compatible providers** with minimal token permissions
- ✅ **Backblaze B2**, **Wasabi** write-only tokens
- ✅ **First-time setup** with uncertain token permissions

### When to Set CLOUD_WRITE_HEALTHCHECK=true

- You know your token lacks list permissions (skip failed list attempt)
- Slightly faster checks by avoiding initial list attempt
- Network with very slow list operations

### Performance

- Auto mode (`false`): ~1-2s on success, ~3-4s if fallback needed (one-time on first run)
- Force write (`true`): ~2-3s always (slightly slower than list, faster than auto-fallback)

### Remote Format

**Format**: `<remote-name>:<path>`
- `remote-name`: Configured in `rclone config`
- `:` (colon): Required separator
- `path`: Directory inside remote (optional for root)

**Examples**:
- `gdrive:pbs-backups` -> Google Drive, folder "pbs-backups"
- `s3:my-bucket/backups` -> S3 bucket, subfolder "backups"
- `minio:/pbs` -> MinIO, absolute path "/pbs"

### Upload Modes

| Mode | Description |
|------|-------------|
| `sequential` | Upload files one at a time (lower memory, predictable) |
| `parallel` | Upload main file sequentially, then associated files (.sha256, .metadata, .bundle) in parallel (faster, uses more memory) |

---

## Storage Comparison

Quick comparison to help you choose the right storage configuration:

| Feature | SECONDARY_PATH | CLOUD_REMOTE (rclone) |
|---------|----------------|----------------------|
| **Path Type** | Filesystem-mounted paths only | Network addresses via rclone |
| **Valid Examples** | `/mnt/nas-backup`<br>`/media/usb-drive`<br>`/backup/secondary` | `CLOUD_REMOTE=GoogleDrive` + `CLOUD_REMOTE_PATH=/backups`<br>`CLOUD_REMOTE=b2` + `CLOUD_REMOTE_PATH=/pbs-prod`<br>`CLOUD_REMOTE=minio` + `CLOUD_REMOTE_PATH=/pbs` |
| **Invalid Examples** | ❌ `192.168.0.10/folder`<br>❌ `//server/share`<br>❌ `\\192.168.0.10\backup` | N/A (handles network directly) |
| **Network Storage** | Must mount first via NFS/CIFS/SMB | Direct access via rclone config |
| **Setup Complexity** | Simple (native Go copy) | Moderate (requires rclone config) |
| **Dependencies** | None | Requires rclone installed |
| **Speed** | Fast (local filesystem I/O) | Depends on network/cloud |
| **Use Case** | - Local USB drives<br>- Pre-mounted NAS shares<br>- Additional local disks | - Cloud storage (GDrive, S3, B2)<br>- LAN servers (MinIO, S3)<br>- Remote storage without mounting |
| **Failure Behavior** | Non-critical (warns, continues) | Non-critical (warns, continues) |
| **Setup Example** | `sudo mount 192.168.0.10:/share /mnt/nas`<br>`SECONDARY_PATH=/mnt/nas` | `rclone config` (create "minio" remote)<br>`CLOUD_REMOTE=minio` + `CLOUD_REMOTE_PATH=/backups` |

### Decision Guide

- **Use SECONDARY_PATH if**: You have local storage (USB drive) OR willing to mount network shares to filesystem
- **Use CLOUD_REMOTE if**: You want direct network access (no mounting) OR using cloud providers OR using S3-compatible storage

**See** [docs/CLOUD_STORAGE.md](CLOUD_STORAGE.md) **for complete rclone setup guide.**

---

## rclone Settings

```bash
# Connection timeout (seconds)
RCLONE_TIMEOUT_CONNECTION=30       # Remote accessibility check (also used as per-command timeout for restore/decrypt cloud scan)

# Operation timeout (seconds)
RCLONE_TIMEOUT_OPERATION=300       # Upload/download operations (5 minutes default)

# Bandwidth limit
RCLONE_BANDWIDTH_LIMIT=            # empty = unlimited (compiled default); the template ships "10M"

# Parallel transfers inside rclone
RCLONE_TRANSFERS=16                # simultaneous transfers (compiled fallback 4; the template ships 16)

# Retry attempts
RCLONE_RETRIES=3                   # Retry count for failed operations

# Verification method
RCLONE_VERIFY_METHOD=primary       # primary | alternative

# Additional rclone flags
RCLONE_FLAGS="--checkers=4 --stats=0 --drive-use-trash=false --drive-pacer-min-sleep=10ms --drive-pacer-burst=100"
```

### Timeout Tuning

- **CONNECTION**: Short timeout for quick accessibility check (default 30s); also applies per rclone command during restore/decrypt cloud scanning (listing backups + reading manifests)
- **OPERATION**: Long timeout for large file uploads (increase for slow networks)

### Bandwidth Limit Format

- `""` = Unlimited
- `"10M"` = 10 MB/s
- `"512K"` = 512 KB/s

### Verification Methods

`RCLONE_VERIFY_METHOD` selects only *how the remote object is located* for verification:

- **primary**: Uses `rclone lsl <file>` (fast, direct)
- **alternative**: Uses `rclone ls <directory>` then searches (slower, compatible with all remotes)

Independently, when `CLOUD_VERIFY_CHECKSUM=true` (default) ProxSave compares the remote object's SHA256 to the local archive after the size check. If the backend cannot produce a native SHA256, it falls back to size-only verification (logged at debug; it never fails a good upload nor changes its exit code). Set `CLOUD_VERIFY_DOWNLOAD=true` to instead download the object and hash it locally on such backends (uses bandwidth).

---

## Batch Deletion (Cloud)

```bash
# Files per batch
CLOUD_BATCH_SIZE=20                # Delete max 20 files per batch

# Pause between batches (seconds)
CLOUD_BATCH_PAUSE=1                # Wait 1 second between batches
```

### Purpose

Avoid API rate limiting during retention cleanup.

### Example

Deleting 50 files with `BATCH_SIZE=20`, `BATCH_PAUSE=1`:
- Batch 1: Delete files 1-20, pause 1s
- Batch 2: Delete files 21-40, pause 1s
- Batch 3: Delete files 41-50, done

### Provider Tuning

| Provider | Recommended BATCH_SIZE | Recommended BATCH_PAUSE |
|----------|----------------------|------------------------|
| Google Drive | 10-15 | 2-3 |
| S3/Wasabi | 50-100 | 1 |
| Backblaze B2 | 20-30 | 2 |
| MinIO (self-hosted) | 100+ | 0 |

---

## Retention Policies

Two mutually exclusive strategies:

### 1. Simple Retention (Count-Based)

```bash
# Retention policy mode
RETENTION_POLICY=simple            # simple | gfs

# Keep N most recent backups
MAX_LOCAL_BACKUPS=15               # Primary storage
MAX_SECONDARY_BACKUPS=15           # Secondary storage
MAX_CLOUD_BACKUPS=15               # Cloud storage
```

**Behavior**:
- Keeps N most recent backups
- Deletes all older backups
- Simple, predictable
- Good for frequent backups with limited storage

**Example**: With `MAX_LOCAL_BACKUPS=30` and daily backups, keeps last 30 days.

**Compiled fallbacks** (when a key is absent): `MAX_LOCAL_BACKUPS=7`, `MAX_SECONDARY_BACKUPS=14`, `MAX_CLOUD_BACKUPS=30`. The shipped template sets all three to `15` (the values above).

### 2. GFS Retention (Grandfather-Father-Son)

```bash
# Retention policy mode
RETENTION_POLICY=gfs               # Activates GFS mode

# GFS tiers
RETENTION_DAILY=7                  # Keep last 7 daily backups (minimum accepted is 1; 0 treated as 1)
RETENTION_WEEKLY=4                 # Keep 4 weekly backups (1 per ISO week)
RETENTION_MONTHLY=12               # Keep 12 monthly backups (1 per month)
RETENTION_YEARLY=3                 # Keep 3 yearly backups (1 per year). 0 means keep ALL years, not off
```

**`RETENTION_POLICY=gfs` is the switch, and it is enough on its own.** The four tiers have a compiled fallback of `0`, and the `7/4/12/3` above are template examples, not defaults. Setting only the policy line does not mean "keep everything until I pick tiers": it activates GFS with every tier at zero, which means `RETENTION_DAILY` is forced up to `1`, weekly and monthly keep nothing, and yearly keeps one backup per past year. Everything else is classified for deletion and really is deleted, on local, secondary and cloud storage, on the very next run.

Set the tiers in the same edit as the policy.

### GFS Algorithm

| Tier | Selection Criteria | Example (7/4/12/3) |
|------|-------------------|-------------------|
| Daily | Most recent N backups | Last 7 backups (2025-11-17, 11-16, ..., 11-11) |
| Weekly | 1 per ISO week, excluding daily | Weeks 46, 45, 44, 43 (1 backup per week) |
| Monthly | 1 per month, excluding daily/weekly | Nov 2025, Oct 2025, ..., Dec 2024 |
| Yearly | 1 per year, excluding daily/weekly/monthly | 2025, 2024, 2023 |

### Benefits

- Better historical coverage than simple count
- Automatic time distribution
- ISO 8601 week numbering (standard)
- Efficient storage (fewer total backups)

### Example Output

```text
GFS classification -> daily: 7/7, weekly: 4/4, monthly: 12/12, yearly: 2/3, to_delete: 15
Deleting old backup: pbs-backup-20220115-120000.tar.xz (created: 2022-01-15 12:00:00)
Cloud storage retention applied: deleted 15 backups (logs deleted: 15), 26 backups remaining
```

### Storage Comparison

- **Simple**: `MAX_CLOUD_BACKUPS=1095` for 3 years daily = 1095 backups
- **GFS**: `DAILY=7, WEEKLY=4, MONTHLY=12, YEARLY=3` = ~26 backups (97% storage reduction!)

---

## Encryption & Bundling

**From the dashboard**: the configuration form's **Backup encryption (AGE)** toggle writes
`ENCRYPT_ARCHIVE`, and `Maintenance` -> `New key` generates the recipient that
`AGE_RECIPIENT_FILE` points at (from an existing public key, a passphrase, or an existing
private key). `BUNDLE_ASSOCIATED_FILES`, `AGE_RECIPIENT` and `AGE_RECIPIENT_FILE` itself
are edited here.

```bash
# Bundle associated files into single .tar
BUNDLE_ASSOCIATED_FILES=true       # true | false

# Encrypt archive with AGE
ENCRYPT_ARCHIVE=false              # true | false

# AGE public key recipient (inline)
AGE_RECIPIENT=                     # e.g., "age1..."

# AGE recipient file path
AGE_RECIPIENT_FILE=${BASE_DIR}/identity/age/recipient.txt
```

### Bundle Format

**Format**: `<name>.tar.<algo>.age.bundle.tar`

**Contents**:
- Main archive (`.tar.xz.age`)
- Checksum (`.sha256`)
- Metadata (`.metadata`)

### Encryption

- Uses AGE (age-encryption.org)
- Streaming encryption (no plaintext on disk)
- Supports multiple recipients
- Passphrase or key-based

**See** [docs/ENCRYPTION.md](ENCRYPTION.md) **for complete workflow.**

---

## Notifications

Two of the four channels are on the dashboard's configuration form: **Telegram
notifications**, **Email notifications** and **Email delivery mode**. Gotify and webhooks
have no form fields and are configured here only. After enabling Telegram,
`Diagnostic Checks` -> `Telegram` verifies the pairing without running a backup.

### Which runs get notified (`NOTIFY_ON`)

```bash
# When to send, on every channel below
NOTIFY_ON=always                   # always | warning | failure
```

`NOTIFY_ON` is a **severity threshold, not an exact match**:

| Value | Notified | Silent |
|-------|----------|--------|
| `always` (default) | success, warning, failure | nothing |
| `warning` | warning **and** failure | success |
| `failure` | failure | success, warning |

So `NOTIFY_ON=warning` means "warnings and anything worse", not "warnings only". It is the
setting for *tell me when something needs looking at*.

It applies on top of each channel's own `*_ENABLED` flag, and to every channel at once;
there is no per-channel form. A channel skipped by the threshold says so in the log:

```
SKIP     Webhook: NOTIFY_ON=warning and this run is a success
```

Two things it deliberately does **not** change:

- **The exit code.** A run that ends with warnings still exits `1`, still logs the
  warnings, and still reports `status=warning` in the Prometheus textfile. `NOTIFY_ON` is
  a delivery decision only, so anything watching the exit code sees what it saw before.
- **Healthchecks.** The [healthchecks connector](HEALTHCHECKS.md) is not a notification
  channel and is never filtered. That is the point: it is what still reports a run you
  chose not to hear about - including a run that never happened at all, which no
  notification channel can tell you about by construction. If you set `NOTIFY_ON` to
  anything other than `always`, read HEALTHCHECKS.md.

An unrecognised value is not silently accepted: it is named in a warning and treated as
`always`, because the failure mode of a typo here is silence, and silence looks exactly
like a backup that never ran.

### Telegram

**From the dashboard**: the form's **Telegram notifications** toggle writes
`TELEGRAM_ENABLED`, and also sets `BOT_TELEGRAM_TYPE=centralized` unless you are editing a
file that already carries that variable, so an existing `personal` setup survives an edit.
The bot token and chat ID are not on the form; a personal bot is configured here.

```bash
# Enable Telegram notifications
TELEGRAM_ENABLED=false             # true | false

# Bot type
BOT_TELEGRAM_TYPE=centralized      # centralized | personal

# Personal mode settings
TELEGRAM_BOT_TOKEN=                # Bot token (from @BotFather)
TELEGRAM_CHAT_ID=                  # Chat ID (your user ID or group ID)

# Two-response delivery confirmation
TELEGRAM_CONFIRM_DELIVERY=true     # poll the relay to confirm delivery after sending
TELEGRAM_CONFIRM_TIMEOUT_SECONDS=10
TELEGRAM_CONFIRM_INTERVAL_SECONDS=1
```

**Bot types**:
- **centralized**: Uses organization-wide bot (configured server-side)
- **personal**: Uses your own bot (requires `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`)

**Centralized mode pairing**:
1. Enable Telegram (`TELEGRAM_ENABLED=true`, `BOT_TELEGRAM_TYPE=centralized`)
2. Get your **Server ID**:
   - Shown during `--install` (TUI/CLI Telegram setup step)
   - Persisted in `<BASE_DIR>/identity/.server_identity` and reused on next runs
   - Also printed in the normal run logs
3. Open Telegram and start `@ProxmoxAN_bot`
4. Send the Server ID to the bot
5. Verify pairing:
   - **TUI installer**: the Telegram setup screen is shown only when config loads successfully, centralized mode is active, and a Server ID is available. When shown, press `Check` (retry supported). `Continue` appears only after success; use `Skip` (or `ESC`) to proceed without verification.
   - **CLI installer**: the same eligibility rules apply, then you can opt into the check and retry when prompted.
   - Normal runs also verify automatically and will skip Telegram if not paired yet.

**Setup personal bot**:
1. Message @BotFather on Telegram: `/newbot`
2. Copy token to `TELEGRAM_BOT_TOKEN`
3. Message @userinfobot: `/start` (get your chat ID)
4. Copy ID to `TELEGRAM_CHAT_ID`

### Email

**From the dashboard**: the form's **Email notifications** toggle writes `EMAIL_ENABLED`,
and **Email delivery mode** writes `EMAIL_DELIVERY_METHOD` (the three options map to
`relay`, `sendmail` and `pmf`). Neither field asks about `EMAIL_FALLBACK_SENDMAIL`: an
existing value is preserved byte for byte, a legacy `EMAIL_FALLBACK_PMF` is migrated onto
the current spelling, and only a file carrying neither gets `true` seeded. The recipient
and From address are edited here.

```bash
# Enable email notifications
EMAIL_ENABLED=false                # true | false

# Delivery method
EMAIL_DELIVERY_METHOD=relay        # relay | sendmail | pmf

# Fallback to local sendmail if the primary path cannot deliver
EMAIL_FALLBACK_SENDMAIL=true       # true | false

# Recipient
# - relay/sendmail: required (empty = auto-detect from Proxmox root@pam)
# - pmf: optional (used only for the To: header)
EMAIL_RECIPIENT=                   # e.g., "admin@example.com"

# From address (used by sendmail/pmf; relay may ignore/override it)
EMAIL_FROM=no-reply@proxmox.tis24.it
```

If `EMAIL_ENABLED` is omitted, the default remains `false`. The legacy alias `EMAIL_ENABLE` is still accepted during migration and runtime loading.

**Which delivery mode should I choose?**

| Method | Best when | Where SMTP is configured |
| --- | --- | --- |
| `relay` | Default for new installs. Uses the built-in TIS24 cloud relay over outbound HTTPS. | In the relay service; ProxSave only needs a recipient. |
| `sendmail` | The node already has a local MTA such as Postfix, Exim, or Sendmail. | In the local MTA. ProxSave calls `/usr/sbin/sendmail`. |
| `pmf` | You explicitly want Proxmox Notifications via `proxmox-mail-forward`. | In Proxmox (`Datacenter -> Notifications` on PVE, or the PBS notification UI/config). ProxSave does not ask for SMTP host/port/user/password. |

The value `pmf` may also be written as `proxmox`, `proxmox-notifications`, or `proxmox-mail-forward`; ProxSave normalizes those aliases to `pmf`.

**Notes**:
- Allowed values for `EMAIL_DELIVERY_METHOD` are: `pmf`, `relay`, `sendmail` (invalid values will skip Email with a warning).
- `EMAIL_FALLBACK_SENDMAIL=true` controls local `/usr/sbin/sendmail` failover. `EMAIL_FALLBACK_PMF` is accepted only as a transitional alias from older templates.
- `relay` requires a real mailbox recipient and blocks `root@` recipients; set `EMAIL_RECIPIENT` to a non-root mailbox if needed.
- Default install behavior is `relay -> sendmail`.
- If you manually set `EMAIL_DELIVERY_METHOD=pmf`, fallback order is `pmf -> relay -> sendmail` when `EMAIL_FALLBACK_SENDMAIL=true`.
- When logs say the relay "accepted request", it means the worker and upstream email API accepted the submission. It does **not** guarantee final inbox delivery (the message may still bounce, be deferred, or land in spam later).
- If `EMAIL_RECIPIENT` is empty, ProxSave auto-detects the recipient from the `root@pam` user:
  - **PVE**: Proxmox API via `pvesh get /access/users/root@pam` -> fallback to `pveum user list` -> fallback to `/etc/pve/user.cfg`
  - **PBS**: `proxmox-backup-manager user list` -> fallback to `/etc/proxmox-backup/user.cfg`
  - **Dual**: intentionally uses the **PVE** detection path for `root@pam` email discovery
- `sendmail` requires a recipient and uses `/usr/sbin/sendmail` (auto-detect applies if `EMAIL_RECIPIENT` is empty, as described above).
- With `pmf`, final delivery recipients are determined by Proxmox Notifications targets/matchers. `EMAIL_RECIPIENT` is only used for the `To:` header and may be empty.

### Gotify

Not on any dashboard form; configured here only.

```bash
# Enable Gotify notifications
GOTIFY_ENABLED=false               # true | false

# Gotify server URL
GOTIFY_SERVER_URL=                 # e.g., "https://gotify.example.com"

# Application token
GOTIFY_TOKEN=                      # From Gotify Apps page

# Priority levels
GOTIFY_PRIORITY_SUCCESS=2          # Success notifications
GOTIFY_PRIORITY_WARNING=5          # Warning notifications
GOTIFY_PRIORITY_FAILURE=8          # Failure notifications
```

**Setup**:
1. Install Gotify server (https://gotify.net)
2. Create application in Gotify
3. Copy app token to `GOTIFY_TOKEN`

### Webhook

Not on any dashboard form; configured here only.

```bash
# Enable webhook notifications
WEBHOOK_ENABLED=false              # true | false

# Comma-separated endpoint names
WEBHOOK_ENDPOINTS=                 # e.g., "discord_alerts,teams_ops"

# Default payload format
WEBHOOK_FORMAT=generic             # discord | slack | teams | generic | pushover

# Request timeout (seconds)
WEBHOOK_TIMEOUT=30

# Retry configuration
WEBHOOK_MAX_RETRIES=3
WEBHOOK_RETRY_DELAY=2              # Seconds between retries
```

**Per-endpoint configuration** (example for endpoint named `discord_alerts`):

```bash
# URL
WEBHOOK_DISCORD_ALERTS_URL=https://discord.com/api/webhooks/XXXX/YYY

# Payload format
WEBHOOK_DISCORD_ALERTS_FORMAT=discord  # discord | slack | teams | generic | pushover

# HTTP method
WEBHOOK_DISCORD_ALERTS_METHOD=POST     # POST | GET | HEAD

# Custom headers (comma-separated)
WEBHOOK_DISCORD_ALERTS_HEADERS="X-Custom-Token:abc123,X-Another:value"

# Authentication type
WEBHOOK_DISCORD_ALERTS_AUTH_TYPE=none  # none | bearer | basic | hmac

# Authentication credentials
WEBHOOK_DISCORD_ALERTS_AUTH_TOKEN=     # Bearer token (or Pushover application token)
WEBHOOK_DISCORD_ALERTS_AUTH_USER=      # Basic auth username (or Pushover user/group key)
WEBHOOK_DISCORD_ALERTS_AUTH_PASS=      # Basic auth password
WEBHOOK_DISCORD_ALERTS_AUTH_SECRET=    # HMAC secret key

# Pushover-specific (only honored when FORMAT=pushover; default 0, range -2..1)
WEBHOOK_DISCORD_ALERTS_PRIORITY=0
```

**Supported formats**:
- **discord**: Discord webhook JSON format
- **slack**: Slack incoming webhook format
- **teams**: Microsoft Teams connector format
- **generic**: Simple JSON `{"status": "...", "message": "..."}`
- **pushover**: [Pushover](https://pushover.net) push notifications. Reuses `AUTH_TOKEN` (application token) and `AUTH_USER` (user/group key); `AUTH_TYPE` stays `none` because Pushover takes credentials in the JSON body. Title is truncated to 250 characters and message to 1024 characters per Pushover's API limits. `PRIORITY` accepts -2..1 (default 0); emergency priority (2) is not supported.

---

## Metrics - Prometheus

```bash
# Enable Prometheus metrics export
METRICS_ENABLED=false              # true | false

# Metrics export path (textfile collector format)
METRICS_PATH=${BASE_DIR}/metrics   # Empty = /var/lib/prometheus/node-exporter
```

**Output**: Creates `proxmox_backup.prom` in `METRICS_PATH` with:
- Backup duration and start/end timestamps
- Archive size and raw bytes collected
- Files collected/failed and success/failure status
- Storage usage counters per location (local/secondary/cloud)

**Integration**: Point Prometheus node_exporter to `METRICS_PATH`.

---

## Collector Options

Exactly one collector variable is on the dashboard's configuration form:
`BACKUP_FIREWALL_RULES`, as the **Backup firewall rules** toggle. Every other variable in
this section is edited here.

### PVE-Specific

```bash
# Cluster configuration
BACKUP_CLUSTER_CONFIG=true         # Cluster config + runtime (corosync, pvecm status/nodes, HA status)

# PVE firewall rules
BACKUP_PVE_FIREWALL=true           # PVE firewall configuration

# vzdump configuration
BACKUP_VZDUMP_CONFIG=true          # /etc/vzdump.conf

# Access control lists
BACKUP_PVE_ACL=true                # Access control + priv credentials (shadow/token/tfa); realms when configured

# Scheduled jobs
BACKUP_PVE_JOBS=true               # Backup jobs configuration
BACKUP_PVE_SCHEDULES=true          # Cron schedules

# Replication
BACKUP_PVE_REPLICATION=true        # VM/CT replication config

# PVE backup files
BACKUP_PVE_BACKUP_FILES=true       # Include backup files from /var/lib/vz/dump
PVESH_TIMEOUT=15                   # Timeout (seconds) for each `pvesh` call (0=disabled)
FS_IO_TIMEOUT=30                   # Per-operation timeout (seconds) for filesystem syscalls (stat/readdir/open/read/write/close/glob/copy/hash) across the preflight, logging, storage, cloud and restore paths. Prevents hangs on dead/unreachable mounts (0=disabled)
BACKUP_SMALL_PVE_BACKUPS=false     # Include small backups only
MAX_PVE_BACKUP_SIZE=100M           # Max size for "small" backups
PVE_BACKUP_INCLUDE_PATTERN=        # A single literal substring, not a glob and not a list
                                   # Matched with a plain "contains" against the full path,
                                   # so use e.g. vzdump-qemu-100, never *.vma.zst.
                                   # A pattern that matches nothing copies nothing, silently

# Ceph configuration
BACKUP_CEPH_CONFIG=false           # Ceph cluster config
CEPH_CONFIG_PATH=/etc/ceph         # Ceph config directory

# VM/CT configurations
BACKUP_VM_CONFIGS=true             # VM/CT config files
```

**Note (PVE snapshot behavior)**: ProxSave snapshots `PVE_CONFIG_PATH` for completeness. When a PVE feature is disabled, proxsave also excludes its well-known files from that snapshot to avoid "still included via full directory copy" surprises (e.g. `qemu-server/` + `lxc/` for `BACKUP_VM_CONFIGS=false`, `firewall/` + `host.fw` for `BACKUP_PVE_FIREWALL=false`, `user.cfg`/`domains.cfg` plus the credential files `priv/shadow.cfg`/`priv/token.cfg`/`priv/tfa.cfg` for `BACKUP_PVE_ACL=false` (ACLs are stored in `user.cfg` on PVE), `jobs.cfg` + `vzdump.cron` for `BACKUP_PVE_JOBS=false`, `corosync.conf` (and `config.db` capture) for `BACKUP_CLUSTER_CONFIG=false`).

> **Security note**: `/etc/pve` is a pmxcfs mount backed by the cluster database `config.db`. Setting `BACKUP_PVE_ACL=false` removes the flat `priv/*` credential files from the snapshot, but the same secrets remain inside `config.db` (captured when `BACKUP_CLUSTER_CONFIG=true`). To exclude PVE access-control secrets from the backup entirely, set both `BACKUP_PVE_ACL=false` and `BACKUP_CLUSTER_CONFIG=false`. ProxSave logs a WARNING during backup when this combination leaves secrets in `config.db`.

### PBS-Specific

```bash
# PBS datastore configs
BACKUP_DATASTORE_CONFIGS=true      # Datastore definitions

# S3 endpoints (used by S3 datastores)
BACKUP_PBS_S3_ENDPOINTS=true       # s3.cfg (S3 endpoints, used by S3 datastores)

# Node/global config
BACKUP_PBS_NODE_CONFIG=true        # node.cfg (global PBS settings)

# ACME
BACKUP_PBS_ACME_ACCOUNTS=true      # acme/accounts/ (one file per account)
BACKUP_PBS_ACME_PLUGINS=true       # acme/plugins.cfg

# Integrations
BACKUP_PBS_METRIC_SERVERS=true     # metricserver.cfg
BACKUP_PBS_TRAFFIC_CONTROL=true    # traffic-control.cfg

# Notifications
BACKUP_PBS_NOTIFICATIONS=true      # notifications.cfg (targets/matchers/endpoints)
BACKUP_PBS_NOTIFICATIONS_PRIV=true # notifications-priv.cfg (secrets/credentials for endpoints)

# User and permissions
BACKUP_USER_CONFIGS=true           # PBS users/ACLs/realms + credentials (token.cfg, shadow.json, token.shadow, tfa.json)

# Remote configurations
BACKUP_REMOTE_CONFIGS=true         # Remote PBS servers

# Sync jobs
BACKUP_SYNC_JOBS=true              # Datastore sync jobs

# Verification jobs
BACKUP_VERIFICATION_JOBS=true      # Backup verification schedules

# Tape backup
BACKUP_TAPE_CONFIGS=true           # Tape library configuration

# Network configuration (PBS)
BACKUP_PBS_NETWORK_CONFIG=true     # network.cfg (PBS), independent from BACKUP_NETWORK_CONFIGS (system)

# Prune schedules
BACKUP_PRUNE_SCHEDULES=true        # Retention prune schedules

# PXAR metadata scanning
PXAR_SCAN_ENABLE=false             # Enable PXAR file metadata collection
PXAR_SCAN_DS_CONCURRENCY=3         # Datastores scanned in parallel
PXAR_FILE_INCLUDE_PATTERN=         # Include patterns (default: *.pxar, *.pxar.*, catalog.pxar, catalog.pxar.*)
PXAR_FILE_EXCLUDE_PATTERN=         # Exclude patterns (e.g., *.tmp, *.lock)
```

**Note (PBS snapshot behavior)**: ProxSave snapshots `PBS_CONFIG_PATH` (`/etc/proxmox-backup`) for completeness. When a PBS feature is disabled, proxsave excludes the corresponding well-known config files from that snapshot (for example, `remote.cfg` is excluded when `BACKUP_REMOTE_CONFIGS=false`) and also skips the related command outputs.

**PXAR scanning**: collects *metadata about* the `.pxar` archives inside your PBS datastores, never
their contents. ProxSave backs up configuration, so no datastore data is copied with or
without this setting: with it on you additionally get, per datastore, a subdirectory report
(`<datastore>_subdirs.txt`) and the VM and container PXAR listings
(`<datastore>_vm_pxar_list.txt`, `<datastore>_ct_pxar_list.txt`). Think of it as an inventory
of what the datastore held at backup time, useful when reconstructing what existed; losing it
costs you that picture, not any backup.

It is **off by default**, and turning it on has a real cost: the scan walks every configured or
discovered PBS datastore, `PXAR_SCAN_DS_CONCURRENCY` at a time. On a large or slow datastore that is the most
expensive thing in the run.

> **Changed in 0.30.0.** This used to compile to `true` while the shipped template said
> `false`, so one release behaved two ways: a fresh install had scanning off, while a config
> old enough to predate the key had it on. Both now default to off, which means a config that
> was getting these inventories stops getting them. Set `PXAR_SCAN_ENABLE=true` to keep them.

**Note**: `PXAR_FILE_INCLUDE_PATTERN` and `PXAR_FILE_EXCLUDE_PATTERN` are also reused for file sampling in PVE datastore metadata. Leave them empty to use the built-in defaults per platform.

### Override Collection Paths

```bash
# PVE paths
PVE_CONFIG_PATH=/etc/pve
PVE_CLUSTER_PATH=/var/lib/pve-cluster
COROSYNC_CONFIG_PATH=${PVE_CONFIG_PATH}/corosync.conf
VZDUMP_CONFIG_PATH=/etc/vzdump.conf

# PBS config directory
PBS_CONFIG_PATH=/etc/proxmox-backup

# PBS datastore paths (comma/space separated)
PBS_DATASTORE_PATH=                # e.g., "/mnt/pbs1,/mnt/pbs2"
# Extra filesystem scan roots for datastore/PXAR discovery; these do not create
# real PBS datastore definitions and may use path-derived output keys.

# System root override (testing/chroot)
SYSTEM_ROOT_PREFIX=                # Optional alternate root for system collection. Empty or "/" = real root.
# Use this to point the collector at a chroot/test fixture without touching the host FS.

# HA-LXC host-backup mode (issue #255)
HOST_BACKUP_MODE=false             # Appliance backing up a host mounted read-only at SYSTEM_ROOT_PREFIX. No effect without a prefix.
```

**Note**: `${PVE_CONFIG_PATH}` (and other `${VAR}` references) are resolved from the same `backup.env` file too, so you do not need to `export` them.

### PBS API credentials (remote server only)

Three variables let the PBS collectors reach a **remote** Proxmox Backup Server. A local
PBS needs none of them: it is detected from the node. They ship commented out in the
template.

```bash
# PBS_REPOSITORY=user@pbs!token@host:datastore
# PBS_PASSWORD=<api-token-secret>
# PBS_FINGERPRINT=<sha256-fingerprint-of-the-server-certificate>
```

Each is resolved in three steps, first match wins: the process environment, then this
file, then auto-detection from the local node. The environment coming first is deliberate,
so a systemd unit or a shell can supply the secret without it being written here. The
fingerprint is only auto-detected when the repository is not an explicitly remote one, so a
remote repository never silently borrows the local certificate's fingerprint. Without a
repository and a password, namespace information for the affected datastore is skipped with
a warning naming both variables; the rest of the backup is unaffected.

**Use case**: Working with mounted snapshots or mirrors at non-standard paths.

**HA-LXC appliance (`HOST_BACKUP_MODE`)**: run ProxSave inside a privileged LXC that backs up the Proxmox host bind-mounted read-only. A non-empty `SYSTEM_ROOT_PREFIX` already makes Proxmox detection and absolute-symlink resolution prefix-aware. `HOST_BACKUP_MODE=true` additionally frames the run as a host-backup appliance and enables the ZFS inventory, which reports the host pools accurately because a privileged LXC shares the host kernel. Namespace-scoped and cluster-daemon commands (udevadm, ethtool, pvesh, ceph) stay skipped under a prefix because they would describe the container, not the host; their data is collected from the host files instead. Symlink targets are stored verbatim, so a backup taken this way restores onto a real host unchanged.

Set `HOST_BACKUP_MODE=true` only in a privileged LXC that shares the host `/dev/zfs` and owns no independent ZFS pools of its own, otherwise the ZFS inventory could record the container's pools as the host's. The host network inventory relies on the host `/sys/class/net`, so it is only complete if the host sysfs is carried under the prefix (a plain non-recursive bind of `/` does not carry it); when it is absent ProxSave logs that the host sysfs is not available rather than reporting container interfaces.

### System Collectors

```bash
# Network configuration
BACKUP_NETWORK_CONFIGS=true        # /etc/network/interfaces, /etc/hosts
# Also captures /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg
# and /etc/dnsmasq.d/lxc-vmbr1.conf for LXC bridge overrides

# APT sources
BACKUP_APT_SOURCES=true            # /etc/apt/sources.list*

# Cron jobs
BACKUP_CRON_JOBS=true              # /etc/crontab, /etc/cron.*

# Systemd services
BACKUP_SYSTEMD_SERVICES=true       # /etc/systemd/system

# SSL certificates
BACKUP_SSL_CERTS=true              # /etc/ssl/certs, /etc/pve/local/pve-ssl.*

# Sysctl configuration
BACKUP_SYSCTL_CONFIG=true          # /etc/sysctl.conf, /etc/sysctl.d/

# Kernel modules
BACKUP_KERNEL_MODULES=true         # /etc/modules, /etc/modprobe.d/

# Firewall rules (the dashboard form's "Backup firewall rules" toggle writes this one)
BACKUP_FIREWALL_RULES=false        # iptables, nftables

# Installed packages
BACKUP_INSTALLED_PACKAGES=true     # dpkg -l, apt-mark showmanual

# Local admin scripts
BACKUP_SCRIPT_DIR=true             # /usr/local/bin and /usr/local/sbin
                                   # Not the ProxSave install dir: that is BACKUP_SCRIPT_REPOSITORY

# Critical system files
BACKUP_CRITICAL_FILES=true         # /etc/fstab, /etc/hostname, /etc/resolv.conf

# SSH keys
BACKUP_SSH_KEYS=true               # /root/.ssh, /etc/ssh

# ZFS configuration
BACKUP_ZFS_CONFIG=true             # /etc/zfs, /etc/hostid, zpool cache & properties

# Root home directory
BACKUP_ROOT_HOME=true              # /root (excluding .cache, .local/share/Trash)

# Backup script repository
BACKUP_SCRIPT_REPOSITORY=false     # Snapshot the ProxSave install dir (excludes .git and backup/log output)

# Backup configuration file
BACKUP_CONFIG_FILE=true            # Include this backup.env configuration file in the backup
```

**Note (SSH keys)**: `BACKUP_SSH_KEYS=false` also suppresses `.ssh/` directories when collecting home directories (root and users), so keys are not included indirectly via `BACKUP_ROOT_HOME`/home collection.

**Note**: `BACKUP_CONFIG_FILE=true` automatically includes the `configs/backup.env` file in the backup archive. This is highly recommended for disaster recovery, as it allows you to restore your exact backup configuration along with the system files. If you have sensitive credentials in `backup.env`, ensure your backups are encrypted (`ENCRYPT_ARCHIVE=true`).

---

## Custom Paths & Blacklist

```bash
# Custom paths to include (one per line)
CUSTOM_BACKUP_PATHS="
# /root/.config/rclone/rclone.conf
# /srv/custom-config.yaml
# /etc/custom/tool.conf
"

# Paths to exclude (one per line)
BACKUP_BLACKLIST="
# /root/.cache
# /root/*_tmp
"
```

**Format**: Bash-style heredoc, one path per line, `#` for comments.

---

## Related Documentation

- [README.md](../README.md) - Main documentation
- [DASHBOARD.md](DASHBOARD.md) - The interactive menu and every screen it opens
- [DAEMON.md](DAEMON.md) - The resident scheduler behind `SCHEDULER_MODE=daemon`
- [HEALTHCHECKS.md](HEALTHCHECKS.md) - The monitoring modes behind the `HEALTHCHECK_*` variables
- [CLOUD_STORAGE.md](CLOUD_STORAGE.md) - Complete rclone setup guide
- [ENCRYPTION.md](ENCRYPTION.md) - AGE encryption workflow
- [CLI_REFERENCE.md](CLI_REFERENCE.md) - Command-line reference, for automation and recovery
- [EXAMPLES.md](EXAMPLES.md) - Practical configuration examples
