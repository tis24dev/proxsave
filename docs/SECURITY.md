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
- **Release artifacts.** Downloads are verified before use (ECDSA P-256 signature plus
  SLSA build provenance; see [PROVENANCE_VERIFICATION.md](PROVENANCE_VERIFICATION.md)).

## Execution model

Every external process ProxSave runs goes through `internal/safeexec`. Nothing ProxSave
builds is ever handed to a shell interpreter.

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
- **The allowlist includes `sh`**, along with `cat`/`tail`/`echo`, but these are invoked
  only with a fixed, constant argv. For example the background network rollback runs
  `sh -c '<constant template>'` and feeds the untrusted values (sleep seconds, script
  path) as positional parameters `$1`/`$2`, never interpolated into the command text.

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

## Security preflight

Before a backup, ProxSave runs a preflight (`internal/security`, `security.Run`), gated
by `SECURITY_CHECK_ENABLED` (default `true`). Checks run in this order: dependency
availability, executable integrity, config file, sensitive files, directories,
secure-account files, and a private-key scan; then, only if
`CHECK_NETWORK_SECURITY` is on (default off), the firewall and open-port checks; and
finally, always, the suspicious-process scan.

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
directories at `0700`/`0755`, all owned `root:root`. With `AUTO_FIX_PERMISSIONS`
(default `true`) a wrong mode or owner is corrected, **except** that ProxSave refuses to
`chmod` or `chown` a **symlink** (that refusal is an error, not a silent fix). Symlink
status is re-derived with `Lstat`, so a fix never follows a link.

**Private-key scan.** The identity directory is walked for the markers
`AGE-SECRET-KEY-`, `BEGIN AGE PRIVATE KEY`, and `OPENSSH PRIVATE KEY`; a match is a
warning to review manually.

**Network and process checks** (opt-in via `CHECK_NETWORK_SECURITY`). The firewall check
runs `iptables -L -n` and warns when no rules are present; the open-port check compares
listeners against `SUSPICIOUS_PORTS` with a `program:port` `PORT_WHITELIST`. The
suspicious-process scan matches against `SUSPICIOUS_PROCESSES` (with a built-in list),
exempting anything in the user `SAFE_PROCESSES` allowlist and, for bracketed kernel-style
names, `SAFE_BRACKET_PROCESSES` / `SAFE_KERNEL_PROCESSES` plus kernel-thread heuristics.

**Abort semantics.** Warnings never abort. An **error** aborts the run with exit code
`ExitSecurityError` unless `CONTINUE_ON_SECURITY_ISSUES=true`; with neither
`CONTINUE_ON_SECURITY_ISSUES` nor `ABORT_ON_SECURITY_ISSUES` set, the default is to
abort on error. Every preflight syscall is bounded by `FS_IO_TIMEOUT` (see below): on a
dead or stale mount the check **warns and skips** rather than wedging the run.

**Preflight configuration keys** (`backup.env`):

| Key | Default | Effect |
|-----|---------|--------|
| `SECURITY_CHECK_ENABLED` | `true` | run the preflight (legacy alias `FULL_SECURITY_CHECK`) |
| `AUTO_FIX_PERMISSIONS` | `true` | auto-correct mode/owner (never on a symlink) |
| `AUTO_UPDATE_HASHES` | `true` | create/refresh the `<exe>.md5` integrity hash |
| `CONTINUE_ON_SECURITY_ISSUES` | `false` | if false, a security error aborts the run |
| `FS_IO_TIMEOUT` | `30` | per-syscall bound (seconds) for filesystem I/O; `0` = unbounded |
| `CHECK_NETWORK_SECURITY` | `false` | enable the firewall and open-port checks |
| `SET_BACKUP_PERMISSIONS` | `false` | skip root-ownership enforcement on the backup/log dirs (externally managed) |

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
Scanning alert is dismissed accordingly. Operators who want a private relay can point
`CLOUDFLARE_*` / the relay config at their own worker with their own secret. See
[NOTIFICATIONS.md](NOTIFICATIONS.md#the-shared-cloud-relay-worker).
