# Personal Script Runtime Diagnostics and Trusted-Home Policy

Date: 2026-09-05
Issue: #306
Status: Approved design

## Problem

The personal pre/post script gate and `--daemon-status` currently answer two
different questions while presenting the result as though it came from one
source.

At daemon startup, ProxSave loads its configuration, validates each personal
script using the daemon's effective UID, and blanks a refused setting in the
daemon's in-memory configuration. Later, `proxsave --daemon-status` starts a
separate process, reloads a configuration file, and evaluates those newly
loaded values. It does not know which configuration values the resident daemon
loaded or which validation verdict it applied.

Consequently, the status command can report `NOT CONFIGURED` even while the
resident daemon is executing a configured script. This is the behavior seen on
machines 1 and 3 in issue #306. The diagnostic is not authoritative about the
running daemon.

The current ownership policy also refuses a root-owned script when any parent
directory is owned by a non-root UID. On the affected machine, the daemon runs
as UID 0, the scripts and their immediate directory are root-owned, and
`/home/howard` is owned by UID 1000 with mode `0700`. Linux permits UID 0 to
traverse and execute that path. ProxSave refuses it as a defense against the
home owner replacing a descendant between startup validation and a later
execution.

For ProxSave's intended deployment model, the host and the root-owned
configuration are controlled by a trusted Proxmox administrator. Selecting a
script under that administrator's home is an explicit trust decision. Treating
the ownership of a non-writable-to-others ancestor as an unconditional refusal
is therefore too strict for this product model.

## Goals

1. Make `--daemon-status` report the personal-script state actually loaded and
   applied by the resident daemon.
2. Compare that startup state with the current configuration file and current
   filesystem state.
3. Identify configuration drift without implying that the whole daemon
   configuration was compared.
4. Permit a root/daemon-owned script below a user-owned ancestor while making
   the trust decision explicit as a warning.
5. Preserve hard failures for malformed or directly unsafe script targets.
6. Keep CLI and dashboard output based on one presentation-neutral diagnostic
   model.
7. Preserve the rule that script execution and script output never affect the
   backup result, run log, healthcheck payload, or notification.

## Non-goals

- Reloading daemon configuration without a restart.
- Adding a daemon control socket or other IPC protocol.
- Reporting synchronization for every configuration key.
- Capturing script stdout, stderr, duration, or exit status.
- Executing or probing a script as part of `--daemon-status`.
- Removing all trusted-path validation.
- Changing the daemon-health exit-code contract of `--daemon-status`.

## Chosen Approach

The daemon will write a separate, root-only runtime snapshot after validating
the configured personal scripts. The status collector will treat that snapshot
as authoritative only when it can bind the snapshot to the currently running
daemon. It will independently inspect the current configuration and filesystem,
then present the two views and their synchronization state.

A separate runtime file is preferred to extending `.daemon_info.json`.
`DaemonInfo` remains the process identity/version record, while the new record
owns configuration-dependent runtime decisions. Reading `/proc/<pid>/cmdline`
and reloading its config path was rejected because it still cannot recover the
values already loaded into memory or the verdict applied at startup. A local
socket was rejected as unnecessary complexity and a new privileged interface.

## Personal Script Policy

### States

Each configured slot has one of four states:

- `not-configured`: the setting is empty.
- `ready`: all validation checks pass without an advisory.
- `ready-with-warning`: the target is accepted and retained, but at least one
  ancestor is owned by a UID other than root or the daemon UID.
- `refused`: the target fails a hard validation rule and is blanked in the
  daemon's in-memory configuration.

### Hard failures

The following remain blocking failures:

- the configured path cannot be resolved or inspected;
- the path traverses a symbolic link;
- the target is not a regular executable accepted by the existing executable
  validation;
- the target file is owned by neither root nor the effective daemon UID;
- the target file is writable by group or others;
- an ancestor is writable by group or others without the existing sticky-bit
  exception;
- ownership or mode cannot be determined.

For the standard root daemon, the target must therefore remain root-owned. The
existing allowance for a target owned by a non-root daemon's own UID remains
valid.

### Advisory condition

An ancestor owned by a UID other than root or the effective daemon UID is no
longer a refusal by itself. If its mode passes the existing writability rule,
the result becomes `ready-with-warning`. The configured path remains in the
daemon configuration and is executed normally.

The startup warning must explain the consequence rather than claim that root
cannot access the directory: the ancestor owner can replace descendants and
the selected script will execute as the daemon UID. The warning is emitted once
at daemon startup, not once per backup.

No opt-in configuration key is added. The root administrator's explicit choice
of the absolute script path is the authorization, and the diagnostic makes the
tradeoff visible.

## Runtime Snapshot

### Storage

The daemon writes:

```text
<base-dir>/identity/.daemon_runtime.json
```

The file uses the existing atomic JSON-write pattern and mode `0600`. It is
removed during a clean daemon shutdown alongside the PID and daemon-info files.
A stale file may remain after a crash and must therefore never be trusted solely
because it exists.

### Data model

The persisted schema contains:

```json
{
  "schema_version": 1,
  "pid": 1234,
  "start_ts": 1788537600,
  "config_path": "/opt/proxsave/configs/backup.env",
  "daemon_uid": 0,
  "personal_scripts": {
    "pre": {
      "path": "/home/howard/dd/mount-pve",
      "state": "ready-with-warning",
      "reason": "/home/howard is owned by uid 1000",
      "components": [
        {"path": "/home/howard/dd/mount-pve", "uid": 0, "mode": 493},
        {"path": "/home/howard/dd", "uid": 0, "mode": 493},
        {"path": "/home/howard", "uid": 1000, "mode": 448}
      ]
    },
    "post": {
      "path": "/home/howard/dd/umount-pve",
      "state": "ready-with-warning",
      "reason": "/home/howard is owned by uid 1000",
      "components": [
        {"path": "/home/howard/dd/umount-pve", "uid": 0, "mode": 493},
        {"path": "/home/howard/dd", "uid": 0, "mode": 493},
        {"path": "/home/howard", "uid": 1000, "mode": 448}
      ]
    }
  }
}
```

The numeric mode representation is an implementation detail of the JSON
contract; renderers continue to display conventional octal modes. The real
snapshot records all inspected components through `/` for accepted paths and
through the failing component for refused paths.

The stored path is the value presented to and validated by the daemon before a
refused value is blanked. This is necessary to explain why a script is disabled.
No configuration secrets, script output, or script contents are stored.

### Publication order

The daemon performs these steps after acquiring single-instance ownership and
before starting scheduled work:

1. Inspect both personal-script settings using the daemon's effective UID.
2. Log any refusal or advisory once.
3. Retain `ready` and `ready-with-warning` paths; blank only `refused` paths.
4. Capture one startup timestamp for both daemon identity and runtime records.
5. Publish PID, daemon identity, and the runtime snapshot atomically per file.
6. Start heartbeat and scheduling work.

Failure to write the runtime snapshot does not stop backups. It emits a startup
warning because it materially degrades later diagnostics.

## Diagnostic Data Flow

The shared diagnostic collector builds two explicitly labeled views.

### Running-daemon view

The runtime snapshot is authoritative only when all of the following hold:

- daemon state reports a live process;
- the snapshot PID equals the live daemon PID;
- `.daemon_info.json` is available;
- the snapshot start timestamp equals the daemon-info start timestamp;
- the runtime schema version is supported.

If any binding check fails, the snapshot is reported as missing, stale,
malformed, or unsupported and is not used as live-daemon evidence. PID-only
matching is insufficient because PIDs can be reused.

### Current-configuration view

The status process continues to inspect the personal-script values loaded from
the currently selected configuration file. It uses the resident daemon UID when
that UID can be resolved from `/proc`; otherwise it labels the current-process
UID as a fallback. This view represents what a restart would attempt to load,
not what the running daemon currently uses.

### Comparison

The collector compares, per script slot:

- the configuration file path used by the daemon and the one selected by the
  status process;
- configured path;
- policy state;
- reason/advisories;
- inspected component ownership and modes.

It derives one of these synchronization results:

- `in-sync`: daemon-startup and current views agree;
- `configuration-drift`: the configuration source or configured script paths
  differ, including configured versus empty; restart required to apply the
  current file;
- `path-state-changed`: the configured path agrees but its current validation
  evidence differs from the daemon-startup evidence;
- `runtime-state-unavailable`: no authoritative daemon snapshot exists;
- `not-applicable`: no live daemon exists.

These results are labeled **Personal-script configuration synchronization**.
They must never be presented as proof that all daemon configuration is in sync.

## Presentation

CLI and dashboard consume the same expanded `daemonDiagnostics` model. Neither
renderer reloads files or recomputes verdicts.

At `INFO`, the status output includes the information necessary to distinguish
the two sources:

```text
Running daemon
  Config loaded: /opt/proxsave/configs/backup.env
  Loaded at: 2026-09-04 14:00:00
  Daemon UID: 0

Personal pre-run script
  Running daemon: READY WITH WARNING (/home/howard/dd/mount-pve)
  Current config: READY WITH WARNING (/home/howard/dd/mount-pve)
  Synchronization: IN SYNC
```

For the issue #306 false-negative shape:

```text
Personal pre-run script
  Running daemon: READY (/path/pre.sh)
  Current config: NOT CONFIGURED
  Synchronization: OUT OF SYNC — restart the daemon to apply the current file
```

At `DEBUG`, the output additionally lists every recorded startup component and
every component observed by the current inspection, including UID and octal
mode. Dynamic text continues through the existing sanitizer before reaching
logs or the dashboard.

When no authoritative runtime snapshot exists, the output says so explicitly:

```text
Running daemon personal-script state: UNAVAILABLE
Current configuration pre-run: READY (/path/pre.sh)
Synchronization: UNKNOWN — restart an updated daemon to publish runtime state
```

The current-file result is never relabeled as the running-daemon result.

## Error Handling and Compatibility

- Missing runtime file: report `runtime-state-unavailable`.
- Malformed JSON: report the parse failure in sanitized form and do not trust
  partial fields.
- PID or start mismatch: report stale runtime state and ignore its verdicts.
- Unsupported schema: report unsupported runtime state and retain only the
  current-configuration view.
- Runtime write failure: warn at daemon startup and continue scheduling.
- Old daemon without runtime publication: status remains usable but explicitly
  degraded until that daemon is updated and restarted.
- Dead daemon: show current configuration as prospective configuration only.
- Clean shutdown: remove the runtime file; missing removal remains best-effort.

Personal-script states do not alter the existing daemon-health exit code of
`--daemon-status`. A running, aligned daemon retains its current exit behavior;
refusals, warnings, and drift are visible diagnostic findings.

## Testing Strategy

### Policy tests

- A root-owned executable below a UID-1000, mode-`0700` home produces
  `ready-with-warning` and remains configured.
- The same script is actually invoked by daemon pre/post execution.
- A target owned by neither root nor daemon remains `refused`.
- A group/other-writable target remains `refused`.
- A group/other-writable non-sticky ancestor remains `refused`.
- Symlink traversal, missing targets, and non-executable targets remain
  `refused`.
- A fully trusted path remains `ready` without a warning.
- A non-root daemon still accepts root-owned and daemon-owned targets.

### Persistence tests

- Runtime-state JSON round trip preserves every field and component.
- Atomic replacement never exposes partial JSON.
- The file is created with restrictive permissions.
- Missing, empty, malformed, and unsupported snapshots degrade safely.
- Removal is idempotent.

### Collector tests

- A matching PID and start timestamp produces an authoritative daemon view.
- A PID mismatch or timestamp mismatch rejects a stale snapshot.
- Different configured paths produce `configuration-drift`.
- Empty current values with daemon-loaded paths reproduce and correctly explain
  the machines 1/3 scenario.
- Equal paths with changed ownership or mode produce `path-state-changed`.
- A live old daemon without a snapshot never causes current config to be
  mislabeled as daemon state.
- UID resolution preserves its live `/proc` source and explicit fallback source.

### Renderer and behavioral tests

- CLI and dashboard render all four policy states and all synchronization
  states from the same model.
- Paths and reasons containing terminal control bytes are sanitized.
- `INFO` carries source distinction and synchronization; `DEBUG` carries full
  component evidence.
- `--daemon-status` never executes either script.
- Runtime snapshot data never enters healthcheck payloads, notifications, or
  normal backup logs.
- The existing two-line normal daemon run-log contract remains unchanged.
- Status exit-code behavior remains unchanged.

## Documentation Changes

Daemon and troubleshooting documentation will explain:

- user-owned, non-loosely-writable ancestors are permitted with a warning;
- the target itself must remain owned by root or the daemon UID and must not be
  writable by group or others;
- the warning represents an explicit trust boundary, not an inability of root
  to access the directory;
- `Running daemon` and `Current configuration` are different diagnostic views;
- configuration drift requires a daemon restart;
- a missing runtime snapshot indicates an old, not-yet-restarted, or degraded
  daemon rather than an unconfigured script.

## Acceptance Criteria

The change is complete when:

1. The machine-4 layout (`daemon UID 0`, root-owned scripts below a UID-1000
   mode-`0700` home) executes both scripts and reports
   `READY WITH WARNING`.
2. A status process whose current config has empty script values can still show
   the non-empty values loaded by the resident daemon and reports personal-script
   configuration drift.
3. No unavailable or stale runtime record is ever presented as authoritative.
4. CLI and dashboard agree on policy and synchronization states.
5. Hard validation failures and the personal-script silence contract remain
   enforced.
6. Focused and full test suites pass.
