# Security

This document describes the ProxSave runtime security model: the trust boundary,
how external commands are executed, the preflight checks, and the layers that keep
secrets and untrusted data from doing harm. For the notification and relay security
model (per-server secret provisioning, the bot token staying on the host, log
redaction per channel) see [NOTIFICATIONS.md](NOTIFICATIONS.md). For release
signing and provenance see [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md).

## Threat model

ProxSave is a Proxmox (PVE/PBS) system backup and restore tool that runs **as root**
on the host it protects. Reading and writing arbitrary system paths (`/etc/pve`,
`/etc/proxmox-backup`, `/proc`, the configured backup destinations, and so on) is its
core function, not a vulnerability: an attacker able to influence those paths would
already need root on the host.

The interesting boundaries are the few places where **untrusted content** enters:

- **Restore archive extraction.** Entry targets are sanitized against the destination
  root, and symlink/hardlink targets that escape the root are rejected. Extraction is
  atomic (sibling temp then rename) and process-owned staging, temp, and
  download/install I/O is confined with `os.Root`, so traversal via `..` or an escaping
  symlink fails at the syscall level. The full mechanism is in
  [RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md).
- **Backup-derived and server-derived display strings.** Text pulled from a backup
  (filenames, config values) or returned by the central relay/monitor is scrubbed of
  terminal escapes before it is printed (see below).
- **Release artifacts.** `install.sh` and `proxsave --upgrade` check the detached ECDSA
  P-256 signature over `SHA256SUMS` against a public key pinned in the tool itself, then
  check the downloaded archive's SHA256 against that authenticated list. A missing or
  invalid signature aborts. That is the **download** path only: `proxsave --upgrade
  --localfile` skips the release check and the download, so it verifies nothing at all and
  just finalizes around the binary already on disk. Every release also publishes SLSA build
  provenance, but that
  attestation is **not** part of the automatic path: verifying it is a separate manual
  step with the GitHub CLI (see
  [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md)).

## Execution model

Every external process ProxSave runs goes through `internal/safeexec`, which builds argv
directly and never wraps a caller's command in a shell. The single exception is the resident
daemon's backup child (see **Self re-execution** below), which re-execs the running binary
with a raw `exec.CommandContext` under a documented `#nosec G204`, bypassing the allowlist.
Several callers do invoke `/bin/sh`
on purpose, but only one of them puts shell **text** on a command line: the background
rollback timer runs `sh -c '<compile-time constant>'` and passes the sleep seconds and the
script path as positional parameters. The others hand a shell nothing but the path of a
script ProxSave generated and wrote to disk, so generated text never travels as shell code
on a command line (see **Generated rollback scripts** below).

- **No shell, argv only.** `safeexec.CommandContext(ctx, name, args...)` requires `name`
  to be an exact key in a static, package-level allowlist (76 literal command names
  today). Each entry builds `exec.CommandContext(ctx, "<literal>")` with the binary name
  hard-coded and appends the caller's arguments as argv, with no metacharacter
  interpretation. `CombinedOutput` and `Output` wrap the same gate.
- **Name validation.** Before the allowlist lookup, a name that is empty, has leading or
  trailing whitespace, or contains a path separator (`/` or `\`) is rejected with
  `command not allowed` (`ErrCommandNotAllowed`). A name not in the allowlist is rejected
  the same way.
- **`/proc` access is narrowed.** `ProcPath(pid, leaf)` only permits the leaves `comm`,
  `status`, and `exe`, so an arbitrary `/proc` path can never be constructed.
- **The allowlist includes `sh`**, along with `cat` and `tail`. Those two take an ordinary
  argv that is often a computed path (`tail -n <n> <logfile>`, `cat <procfile>`), which
  carries no metacharacter interpretation. `sh` appears in two shapes. The background
  rollback timer runs `sh -c '<constant template>'` and feeds the sleep seconds and the
  script path as positional parameters `$1`/`$2`, never interpolated into the command
  text. The immediate rollback runs `sh <scriptPath>`, and the `systemd-run` timers run
  `/bin/sh <scriptPath>`; there the argument is only a path to a script ProxSave wrote.

**Self re-execution.** When ProxSave runs its own binary, the path is validated by
`ValidateTrustedExecutablePath`: it must be absolute, a regular file, have the execute
bit, and not be world-writable. The `--upgrade-config-json` child and the `chattr`
helper go through this `TrustedCommandContext` path. The one exception is the resident
daemon's backup child, which re-execs the running binary (`os.Executable`) with fixed,
literal arguments (`--backup [--config ...]`) under a documented `#nosec G204` waiver.

**rclone inputs** are sanitized before they reach the rclone argv:
`ValidateRcloneRemoteName` rejects an empty name, a leading `-` (flag injection), a
`/`, `\`, or `:`, and any whitespace or control character; `ValidateRemoteRelativePath`
rejects control characters and any `..` parent-directory traversal.

**Generated rollback scripts.** The rollback machinery for network, HA, firewall, and PVE
access control builds a `/bin/sh` script in Go, writes it with mode `0640`, and hands the
path to `sh`. All four arm a **deferred** run through a `systemd-run` timer, falling back to
a `nohup` background timer when `systemd-run` is unavailable **or its invocation fails**;
only the network flow also has an immediate path that runs its script inline. Each script embeds three runtime values, the
log path, the marker path, and the rollback archive path (the access-control script adds
five compile-time `/etc/pve` target paths). None of them is operator, backup, or server
input: the three runtime values are composed from a fixed `/tmp/proxsave` base, a
compile-time prefix, and a timestamp. **That provenance is the guarantee.** A `shellQuote`
helper is applied on the way in, but it only quotes a value containing whitespace or one of
`"'\$&;|<>`, so in practice every one of these paths is emitted verbatim, and its trigger
set omits the backtick, the glob characters, `(`, `)` and `#`. Treat it as incidental, not
as the control.

## Security preflight

Before a backup **or a restore**, ProxSave runs a preflight (`internal/security`,
`security.Run`), gated by `SECURITY_CHECK_ENABLED` (default `true`). Checks run in this
order: dependency availability, executable integrity, config file, sensitive files,
directories, secure-account files, and a private-key scan; then, only if
`CHECK_NETWORK_SECURITY` is on (default off), the network block, whose two real checks
each need a second key of their own; and finally, always, the suspicious-process scan.

**Executable integrity.** The binary is `Lstat`-ed and **refused if it is a symlink**,
then opened and re-checked with `os.SameFile` to catch a swap during the check (a
TOCTOU guard). Its SHA256 is compared against a sibling `<exe>.md5` file (read confined
with `os.OpenRoot`). With `AUTO_UPDATE_HASHES` (default `true`) a missing or mismatched
hash file is created or regenerated; otherwise it is a warning. The exact permission
bits are not enforced (both a packaged `0755` and a self-managed `0700` are accepted),
but a group- or world-writable executable is flagged and, on auto-fix, narrowed without
ever widening.

**Permissions and ownership.** Sensitive files are enforced at `0600` (the config file,
`identity/.server_identity`, the AGE recipient file, and every `secure_account/*.json`),
directories at `0700`/`0755`, all owned `root:root`. The exception is `BACKUP_PATH`,
`LOG_PATH`, `SECONDARY_PATH` and `SECONDARY_LOG_PATH`: their owner **and** mode checks are
skipped entirely when `SET_BACKUP_PERMISSIONS=true` (see below) or when the path sits on a
filesystem without Unix ownership. With `AUTO_FIX_PERMISSIONS`
(default `true`) a wrong mode or owner is corrected, **except** that ProxSave refuses to
`chmod` or `chown` a **symlink** (that refusal is an error, not a silent fix). Symlink
status is re-derived with `Lstat`, so a fix never follows a link.

**Private-key scan.** The identity directory is walked for the markers
`AGE-SECRET-KEY-`, `BEGIN AGE PRIVATE KEY`, and `OPENSSH PRIVATE KEY`; a match is a
warning to review manually.

**Network checks** are doubly gated, and `CHECK_NETWORK_SECURITY` on its own buys you
almost nothing. With it on and nothing else, ProxSave runs `ss -tuln` and logs one
informational line counting the services listening on non-loopback addresses. That line is
never a warning, and "non-loopback" counts the `0.0.0.0` and `::` wildcards, so it is not a
statement about internet reachability. The firewall check needs `CHECK_FIREWALL=true` as
well: it runs `iptables -L -n` and warns when no rules are present. The suspicious-port
check needs `CHECK_OPEN_PORTS=true` as well: it runs `ss -tulnap` and warns for every
public listener whose port is in `SUSPICIOUS_PORTS` and not covered by a `program:port`
entry in `PORT_WHITELIST`. Those two list keys are read by that check alone. All three
switches default off and ship off. None of these checks can abort a run: they only ever
add warnings.

**The suspicious-process scan** is gated by none of them and runs whenever the preflight
runs. It matches against `SUSPICIOUS_PROCESSES` (with a built-in list), exempting anything
in the user `SAFE_PROCESSES` allowlist and, for bracketed kernel-style names,
`SAFE_BRACKET_PROCESSES` / `SAFE_KERNEL_PROCESSES` plus kernel-thread heuristics.

**Abort semantics.** Warnings never abort. An **error** aborts the run with exit code
`ExitSecurityError` (14) unless `CONTINUE_ON_SECURITY_ISSUES=true`; with the key unset, the
default is to abort on error. A legacy `ABORT_ON_SECURITY_ISSUES` is consulted only when
`CONTINUE_ON_SECURITY_ISSUES` is absent from the file entirely, which on a stock install it
never is (the template ships it and `--upgrade` back-fills missing template keys), so
setting it has no effect. Use `CONTINUE_ON_SECURITY_ISSUES`.

**Timeouts are not all warnings.** Most preflight filesystem calls go through
`internal/safefs` and are bounded by `FS_IO_TIMEOUT` (see below). Most checks treat a
timeout as a **warning** and skip: the sensitive files, the directories, the secure-account
files, the `<exe>.md5` hash file, the identity private-key scan, and the shared
ownership/permission helper. Three are deliberately **fail-closed**: a timeout on the
`Lstat` or `Open` of the executable itself, or on the `Stat` of the configuration file, is
recorded as an **error**, so a mount gone dead under the binary or under `backup.env`
aborts the run rather than letting it proceed on an unverified binary or config.

The bound is per syscall and it does not cover the whole preflight. Once the executable is
open, the fstat on it and the SHA256 read of the entire binary are raw unbounded calls, and
inside the private-key scan the directory walk is bounded but the per-file open and read
are not. A mount that goes stale inside one of those windows still wedges the preflight, and
**the daemon watchdog does not save you there**. A child that merely overruns
`MAX_RUN_DURATION` gets `SIGTERM`, then `SIGKILL` after a 30s grace, and is reported as a
hang. A child stuck in uninterruptible sleep is not reported at all: the daemon reports only
after the child is reaped, and a D-state process is never reaped, so the daemon blocks in the
same wedge and stops scheduling. Only the monitor's silence on the missing finish ping
catches that case (see [DAEMON.md](DAEMON.md)). The daemon also supervises `--backup`
children only, so a restore, a manual run, or a dashboard "run now" has no watchdog at all.
`FS_IO_TIMEOUT=0` disables bounding everywhere.

**Preflight configuration keys** (`backup.env`):

| Key | Default | Effect |
|-----|---------|--------|
| `SECURITY_CHECK_ENABLED` | `true` | run the preflight (legacy alias `FULL_SECURITY_CHECK`) |
| `AUTO_FIX_PERMISSIONS` | `true` | auto-correct mode/owner (never on a symlink) |
| `AUTO_UPDATE_HASHES` | `true` | create/refresh the `<exe>.md5` integrity hash |
| `CONTINUE_ON_SECURITY_ISSUES` | `false` | if false, a security error aborts the run |
| `FS_IO_TIMEOUT` | `30` | per-syscall bound (seconds) for filesystem I/O; `0` = unbounded |
| `CHECK_NETWORK_SECURITY` | `false` | master switch for the network block; on its own it only logs a count of public listeners |
| `CHECK_FIREWALL` | `false` | run the `iptables` rule check (needs `CHECK_NETWORK_SECURITY` too) |
| `CHECK_OPEN_PORTS` | `false` | run the suspicious-port check (needs `CHECK_NETWORK_SECURITY` too) |
| `SUSPICIOUS_PORTS` | built-in list | ports the suspicious-port check flags; read by that check only |
| `PORT_WHITELIST` | _(empty)_ | `program:port` pairs exempted from the suspicious-port warning |
| `SET_BACKUP_PERMISSIONS` | `false` | when true, the run chowns the backup/log dirs to `BACKUP_USER:BACKUP_GROUP`, and the preflight then skips their owner **and mode** checks |

`SET_BACKUP_PERMISSIONS` is not a passive opt-out. When it is true, every run walks
`BACKUP_PATH`, `LOG_PATH`, `SECONDARY_PATH` and `SECONDARY_LOG_PATH` **recursively, before
the preflight**, sets every entry to `BACKUP_USER:BACKUP_GROUP`, and sets directories to
`0750`; files keep their mode. `--install` and `--upgrade` each run the same pass once more
at the end, and both force it to mutate: the finalization pass hardcodes `dryRun=false`, so
`DRY_RUN=true` does not hold the recursive chown back there.

The preflight skip is keyed on the **flag alone**, not on the chown having succeeded:
whenever `SET_BACKUP_PERMISSIONS=true`, those four paths are skipped entirely, the mode
check as well as the ownership check (a missing directory is still created at `0755`
first). That matters, because the pass changes nothing when `BACKUP_USER` or `BACKUP_GROUP`
is empty or cannot be resolved on the host, when the path sits on a filesystem without Unix
ownership, or under `--dry-run` (the install and upgrade finalization above is the
exception: it always mutates). In each of those cases the ownership stays as it was **and**
the check that would have flagged it is off.

Ownership is applied with `lchown`, so a symlink is never followed out of the backup tree,
and no failure aborts the run. Only the per-path failures warn (an empty or unresolvable
`BACKUP_USER`/`BACKUP_GROUP`, a failed stat or filesystem probe). A chown or chmod that
fails on an individual entry, and a subtree skipped after a walk error or a per-directory
timeout, are logged at **debug** level only, so a partially converted tree looks like a
clean run at the default log level. Note that the code default for `BACKUP_USER` and
`BACKUP_GROUP` is empty while the shipped template sets `backup:backup`. See
[CONFIGURATION.md](CONFIGURATION.md).

## Secret redaction in logs

Secrets are kept out of logs at two layers. The logger has a `RegisterSecret` path that
scrubs every log line, and `RedactSecrets` masks each secret in both its **raw and
URL-encoded** form (so a token embedded in a `*url.Error` is caught too). Secrets shorter
than 6 runes are not registered, and forms are ordered longest-first so a short secret
cannot partially mask a longer one. `MaskSecret` renders a **fixed 12-asterisk prefix**
plus the last 4 runes; a secret of 8 runes or fewer is fully masked. The fixed-width
prefix deliberately hides the real length. `RedactURLError` strips the URL from a
`*url.Error`, keeping only the operation and transport error, so request URLs (which
carry low-capability values like a check UUID or `server_id`) never reach a log. The
per-channel redaction rules and the deliberate exceptions (the public relay credential,
the display-only portal link) are documented in [NOTIFICATIONS.md](NOTIFICATIONS.md).

## Terminal-escape scrubbing of untrusted display data

Strings that originate from a backup or from the central server are scrubbed before they
are printed, so a hostile filename or a MITM server response cannot inject terminal
escapes into the operator's console (`internal/ui/components/sanitize.go`):

- `SanitizeText` strips ANSI sequences and drops C0 controls, `DEL`, and C1 bytes,
  keeping only newline and tab. `SanitizeLine` additionally collapses newline and tab to
  spaces for single-line contexts (table cells, filenames, menu rows).
- `sanitizeStreamLine` is the color-preserving variant for the live run viewport: it
  keeps only SGR (color) sequences and drops every other escape (cursor moves, OSC, mode
  toggles).

These are applied to daemon status fields, the restore abort IP, backup-derived
filenames, menu rows, and the streaming run panel. The portal magic-link has its own
fail-closed sanitizer (`SanitizeLoginURL`, printable-ASCII http(s) only); see
[NOTIFICATIONS.md](NOTIFICATIONS.md).

## Immutable identity secrets

The server identity (`.server_identity`) and the relay secret (`.notify_secret`) live
under `<BASE_DIR>/identity` and are written `0600` with `chattr +i` (the immutable flag
is cleared before an overwrite and restored after). Reads are confined with `os.Root`,
which resolves the gosec G304 concern structurally rather than with a `#nosec`
suppression. See [NOTIFICATIONS.md](NOTIFICATIONS.md) for how `.notify_secret` is
provisioned.

## Bounded filesystem I/O

A backup or restore can touch a mount that has gone dead or stale, where a normal
syscall blocks forever in uninterruptible sleep. `internal/safefs` bounds each such
syscall by `FS_IO_TIMEOUT` (default 30s): the call runs in a worker goroutine, and on
timeout it is **abandoned** (the kernel call is not cancelled, the operation returns
`ErrTimeout`, and the caller skips rather than wedging). File copies use a per-chunk
no-progress budget, and the bounded directory walk never follows symlinks. This is the
layer beneath the daemon's hang watchdog (see [DAEMON.md](DAEMON.md)); the logger caps
its own log-write budget at 5s and then falls back to stdout only.

## Read-only bind-mount guards

When a datastore or storage mountpoint is offline at restore time, ProxSave read-only
bind-mounts a guard directory over it (`MS_BIND | MS_REMOUNT | MS_RDONLY | MS_NODEV |
MS_NOSUID | MS_NOEXEC`), so nothing can write into the root filesystem while the real
storage is missing. Only mountpoints under `/mnt/`, `/media/`, or `/run/media/` are
eligible, and the target is resolved through symlinks and re-checked against that
allowlist **before** any `mkdir` or `mount`, closing a parent-component symlink escape.
Guards live under `/var/lib/proxsave/guards`; a bind guard is shadowed by the real mount
when it returns and is discarded on reboot. Legacy immutable (`chattr +i`) fallbacks are
recorded in a `0600` index so `proxsave --cleanup-guards` can clear exactly those. See
[RESTORE_TECHNICAL.md](RESTORE_TECHNICAL.md) and
[CLUSTER_RECOVERY.md](CLUSTER_RECOVERY.md).

## Static-analysis policy (gosec)

CI runs gosec (`.github/workflows/security-ultimate.yml`). Three rules deserve a note:

- **G305** (archive-extraction traversal / zip-slip): **kept enabled**; the restore
  extraction containment above is what satisfies it.
- **G304** ("file path from a variable"): **kept enabled project-wide**, but the current
  findings on the collectors, config readers, and backup-output writers are **dismissed
  as false positives** in Code Scanning, since those paths are tool/system/config
  controlled per the threat model. The rule stays enabled so any **new** variable-path
  I/O is surfaced for review. New contained I/O should prefer `os.Root` over a `#nosec`
  suppression; a few legacy `#nosec G304` sites remain.
- **G101** ("potential hardcoded credentials"): **kept enabled**. With one intentional
  exception (below), every current finding is a **name-only false positive**: a constant
  whose name matches `token`/`secret`/`salt` but whose value is a file path
  (`/etc/pve/priv/token.cfg`), a KDF domain-separation salt (public by construction), a
  token *name* (`backup@pbs!go-client`, not its secret), or an enum id
  (`pbs_runtime_access_user_tokens`). These are dismissed as false positives rather than
  renamed, since the names are correct as written.

### Hardcoded relay credential (G101)

The cloud email relay ships a hardcoded `WorkerToken` and `HMACSecret`
(`notify.DefaultCloudRelayConfig`, mirrored in `config.applyDefaults`). These are a
**shared, public anti-abuse credential**, not a confidential secret: the same value is
compiled into every distributed binary and published in this open-source repository, so
it cannot be kept secret on the client side. The token name is literally
`v1_public_...`. It only gates the free shared relay worker, whose real protection is
server-side (rate limiting, per-request `server_mac`/`server_id`); it grants no access to
user data or the host. The two sites carry a `#nosec G101` documenting this, and the Code
Scanning alert is dismissed accordingly.

There is currently **no way to point the relay at your own worker**. The URL, the token
and the HMAC secret are compiled-in constants with no configuration key behind them, so
adding `CLOUDFLARE_WORKER_URL` or anything similar to `backup.env` does nothing, silently.
If you do not want your reports transiting a shared third-party relay, use
`EMAIL_DELIVERY_METHOD=sendmail`: it is the only method with no relay fallback. `pmf` is
**not** an alternative here. When `proxmox-mail-forward` fails and a non-root recipient is
configured, ProxSave tries the shared relay **first** and only then sendmail, and no
configuration key disables that hop (`EMAIL_FALLBACK_SENDMAIL` gates the sendmail leg only).
See [NOTIFICATIONS.md](NOTIFICATIONS.md#the-shared-cloud-relay-worker).
