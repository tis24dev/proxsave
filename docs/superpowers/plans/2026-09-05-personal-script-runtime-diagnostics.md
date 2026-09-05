# Personal Script Runtime Diagnostics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Permit root/daemon-owned pre/post scripts below a trusted administrator's user-owned home with an explicit warning, and make daemon diagnostics compare the resident daemon's startup decision with the current configuration and filesystem.

**Architecture:** The personal-script inspector gains a non-blocking ready-with-warning state for foreign-owned ancestors while preserving hard checks on the target and writable paths. The daemon atomically publishes its startup verdicts to a root-only .daemon_runtime.json; the shared diagnostic collector binds that record to the live PID/start timestamp, independently inspects the current config, and gives CLI/dashboard renderers one authoritative comparison model.

**Tech Stack:** Go 1.25.13, standard library JSON/filesystem/process APIs, existing ProxSave internal/health, logging, dashboard components, and GitNexus MCP.

**Spec:** docs/superpowers/specs/2026-09-05-personal-script-runtime-diagnostics-design.md

## Global Constraints

- Do not add dependencies, configuration keys, or an IPC/control socket.
- The script target must remain owned by root or the effective daemon UID and must not be group/other writable.
- Symlinks, missing/non-regular/non-executable targets, indeterminate ownership, and group/other-writable non-sticky ancestors remain hard refusals.
- A foreign-owned ancestor that passes the writability rule is advisory: retain the path, execute it, and emit one startup warning.
- Runtime state lives at <base-dir>/identity/.daemon_runtime.json, is atomic and mode 0600, and contains no script contents, output, or configuration secrets.
- Runtime state is authoritative only when PID and start timestamp match the live daemon and .daemon_info.json.
- Synchronization claims apply only to personal-script configuration.
- daemon-status must not execute scripts and its daemon-health exit-code contract must not change.
- Script stdout, stderr, and exit status remain absent from backup logs, notifications, healthchecks, metrics, and outcomes.
- Before editing an existing symbol, run GitNexus impact upstream, report direct callers/processes/risk, and warn the user before proceeding if risk is HIGH or CRITICAL.
- Before every commit, stage only that task's files and run GitNexus detect_changes with scope staged and worktree /opt/proxsave-git.
- Preserve unrelated user changes.

## File Map

New files:

- internal/health/daemon_runtime.go: persisted runtime schema and atomic store.
- internal/health/daemon_runtime_test.go: persistence contract.
- cmd/proxsave/daemon_runtime.go: conversion and daemon publication.
- cmd/proxsave/daemon_runtime_test.go: conversion, warning, and cleanup tests.

Modified files:

- cmd/proxsave/personal_scripts_inspection.go and tests: advisory policy state.
- cmd/proxsave/personal_scripts_gate.go and audited tests: retain/warn behavior.
- cmd/proxsave/daemon.go and lifecycle tests: runtime publication/removal.
- cmd/proxsave/daemon_diagnostics.go and tests: runtime binding and comparison.
- cmd/proxsave/dashboard.go and tests: dashboard rendering.
- docs/DAEMON.md, docs/TROUBLESHOOTING.md, docs/SECURITY.md: operator contract.

---

### Task 1: Add the Advisory Trusted-Home Policy

**Files:**
- Modify: cmd/proxsave/personal_scripts_inspection.go:12-160
- Modify: cmd/proxsave/personal_scripts_gate.go:15-48
- Test: cmd/proxsave/personal_scripts_inspection_test.go:52-273
- Test: cmd/proxsave/personal_scripts_audited_test.go:446-595

**Interfaces:**
- Consumes: personalScriptDiagnostic and current trusted-path checks.
- Produces: personalScriptReadyWithWarning and validatePersonalScripts(*config.Config) personalScriptsDiagnostics.
- Guarantees: ready and ready-with-warning retain normalized paths; refused blanks them.

- [ ] **Step 1: Run impact analysis**

Invoke:

~~~json
{"target":"inspectPersonalScript","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/personal_scripts_inspection.go","includeTests":true}
{"target":"validatePersonalScripts","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/personal_scripts_gate.go","includeTests":true}
{"target":"applyPersonalScriptDiagnostic","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/personal_scripts_gate.go","includeTests":true}
~~~

Report callers, processes, and risk before editing.

- [ ] **Step 2: Write failing policy tests**

Change the existing foreign-parent table case to:

~~~go
{
    name: "foreign owned parent is advisory",
    path: "/home/operator/script.sh",
    eval: func(path string) (string, error) { return path, nil },
    stat: func(path string) (os.FileInfo, error) {
        switch path {
        case "/home/operator":
            return personalScriptInspectionFileInfo{
                name: "operator", mode: os.ModeDir | 0o700, uid: 4242, dir: true,
            }, nil
        case "/":
            return rootDir, nil
        default:
            return trustedFile, nil
        }
    },
    validate:   func(string) error { return nil },
    wantState:  personalScriptReadyWithWarning,
    wantReason: "/home/operator is owned by uid 4242",
    wantParts:  4,
},
~~~

Keep/add the foreign-target case as a refusal:

~~~go
{
    name: "foreign owned target stays refused",
    path: "/home/operator/script.sh",
    eval: func(path string) (string, error) { return path, nil },
    stat: func(string) (os.FileInfo, error) {
        return personalScriptInspectionFileInfo{name: "script.sh", mode: 0o700, uid: 4242}, nil
    },
    validate:   func(string) error { return nil },
    wantState:  personalScriptRefused,
    wantReason: "owned by uid 4242",
    wantParts:  1,
},
~~~

Add:

~~~go
func TestApplyPersonalScriptDiagnosticKeepsAdvisoryPathAndWarnsOnce(t *testing.T) {
    logger := logging.New(types.LogLevelDebug, false)
    buf := &bytes.Buffer{}
    logger.SetOutput(buf)
    previous := logging.GetDefaultLogger()
    logging.SetDefaultLogger(logger)
    t.Cleanup(func() { logging.SetDefaultLogger(previous) })

    diagnostic := personalScriptDiagnostic{
        Key:    "PERSONAL_SCRIPT_PRE_RUN",
        Path:   "/home/operator/script.sh",
        State:  personalScriptReadyWithWarning,
        Reason: "/home/operator is owned by uid 4242; that owner can replace descendants executed as daemon uid 0",
    }
    if got := applyPersonalScriptDiagnostic(diagnostic); got != diagnostic.Path {
        t.Fatalf("advisory path = %q, want %q", got, diagnostic.Path)
    }
    if got := strings.Count(buf.String(), "PERSONAL_SCRIPT_PRE_RUN enabled with administrator trust warning:"); got != 1 {
        t.Fatalf("warning count = %d, want 1\n%s", got, buf.String())
    }
}
~~~

- [ ] **Step 3: Prove the tests fail for the intended reason**

Run:

~~~bash
go test ./cmd/proxsave -run 'TestInspectPersonalScriptStatesAndReasons|TestApplyPersonalScriptDiagnosticKeepsAdvisoryPathAndWarnsOnce' -count=1
~~~

Expected: FAIL because the fourth state is absent and a foreign ancestor is still refused.

- [ ] **Step 4: Implement the fourth state and advisory accumulation**

Add:

~~~go
const (
    personalScriptNotConfigured    personalScriptState = "not-configured"
    personalScriptReady            personalScriptState = "ready"
    personalScriptReadyWithWarning personalScriptState = "ready-with-warning"
    personalScriptRefused          personalScriptState = "refused"
)
~~~

Keep personalScriptOwnerError on the target. Replace the owner refusal inside the ancestor loop with:

~~~go
var advisories []string

uid, err := personalScriptOwnerUID(dir, dirInfo)
if err != nil {
    return refuse(err)
}
if uid != 0 && int(uid) != daemonUID {
    advisories = append(advisories, fmt.Sprintf(
        "%s is owned by uid %d; that owner can replace descendants executed as daemon uid %d",
        dir, uid, daemonUID,
    ))
}
~~~

Keep the existing group/other-writable check. At the root return:

~~~go
diagnostic.Path = clean
if len(advisories) > 0 {
    diagnostic.State = personalScriptReadyWithWarning
    diagnostic.Reason = strings.Join(advisories, "; ")
} else {
    diagnostic.State = personalScriptReady
}
return diagnostic
~~~

Return the inspection from the gate and retain advisory paths:

~~~go
func validatePersonalScripts(cfg *config.Config) personalScriptsDiagnostics {
    diagnostics := inspectPersonalScripts(cfg, os.Geteuid())
    if cfg == nil {
        return diagnostics
    }
    cfg.PersonalScriptPreRun = applyPersonalScriptDiagnostic(diagnostics.Pre)
    cfg.PersonalScriptPostRun = applyPersonalScriptDiagnostic(diagnostics.Post)
    return diagnostics
}

func applyPersonalScriptDiagnostic(diagnostic personalScriptDiagnostic) string {
    switch diagnostic.State {
    case personalScriptReady:
        return diagnostic.Path
    case personalScriptReadyWithWarning:
        logging.Warning("%s enabled with administrator trust warning: %s", diagnostic.Key, diagnostic.Reason)
        return diagnostic.Path
    case personalScriptRefused:
        logging.Warning("%s disabled for this daemon: %s", diagnostic.Key, diagnostic.Reason)
        return ""
    default:
        return ""
    }
}
~~~

Update comments to describe administrator trust rather than an OS permission denial.

- [ ] **Step 5: Format and run focused tests**

~~~bash
gofmt -w cmd/proxsave/personal_scripts_inspection.go cmd/proxsave/personal_scripts_gate.go cmd/proxsave/personal_scripts_inspection_test.go cmd/proxsave/personal_scripts_audited_test.go
go test ./cmd/proxsave -run 'TestInspectPersonalScript|TestPersonalScriptValidation|TestPersonalScriptsAreInvisible|TestTheDaemonRunsTheTrustedPathGate' -count=1
~~~

Expected: PASS; hard target/writability/symlink refusals stay green and advisory paths survive.

- [ ] **Step 6: Detect changes and commit**

~~~bash
git add cmd/proxsave/personal_scripts_inspection.go cmd/proxsave/personal_scripts_gate.go cmd/proxsave/personal_scripts_inspection_test.go cmd/proxsave/personal_scripts_audited_test.go
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Review scope, then:

~~~bash
git commit -m "fix: allow trusted personal scripts below user homes"
~~~

---

### Task 2: Add the Root-Only Runtime Store

**Files:**
- Create: internal/health/daemon_runtime.go
- Create: internal/health/daemon_runtime_test.go

**Interfaces:**
- Consumes: writeJSONAtomic(path string, v any) error.
- Produces: DaemonRuntimeSchemaVersion, DaemonRuntimeState, DaemonRuntimeScripts, DaemonRuntimeScript, DaemonRuntimePathComponent, and path/read/write/remove functions.

- [ ] **Step 1: Write failing persistence tests**

Create internal/health/daemon_runtime_test.go:

~~~go
package health

import (
    "os"
    "path/filepath"
    "reflect"
    "testing"
)

func daemonRuntimeFixture() DaemonRuntimeState {
    return DaemonRuntimeState{
        SchemaVersion: DaemonRuntimeSchemaVersion,
        PID:           4321,
        StartTS:       1_700_000_000,
        ConfigPath:    "/opt/proxsave/configs/backup.env",
        DaemonUID:     0,
        PersonalScripts: DaemonRuntimeScripts{
            Pre: DaemonRuntimeScript{
                Path: "/home/operator/pre.sh",
                State: "ready-with-warning",
                Reason: "/home/operator is owned by uid 1000",
                Components: []DaemonRuntimePathComponent{
                    {Path: "/home/operator/pre.sh", UID: 0, Mode: 0o755},
                },
            },
            Post: DaemonRuntimeScript{State: "not-configured"},
        },
    }
}

func TestDaemonRuntimeRoundTripIsAtomicAndPrivate(t *testing.T) {
    base := t.TempDir()
    want := daemonRuntimeFixture()
    if err := WriteDaemonRuntime(base, want); err != nil {
        t.Fatalf("write: %v", err)
    }
    got, found, err := ReadDaemonRuntime(base)
    if err != nil || !found || !reflect.DeepEqual(got, want) {
        t.Fatalf("read = (%+v, %v, %v), want (%+v, true, nil)", got, found, err, want)
    }
    info, err := os.Stat(DaemonRuntimePath(base))
    if err != nil {
        t.Fatal(err)
    }
    if info.Mode().Perm() != 0o600 {
        t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
    }
    if _, err := os.Stat(DaemonRuntimePath(base) + ".tmp"); !os.IsNotExist(err) {
        t.Fatalf("temporary file survived: %v", err)
    }
}

func TestReadDaemonRuntimeMissingEmptyAndMalformed(t *testing.T) {
    base := t.TempDir()
    zero := DaemonRuntimeState{}
    got, found, err := ReadDaemonRuntime(base)
    if err != nil || found || !reflect.DeepEqual(got, zero) {
        t.Fatalf("missing = (%+v, %v, %v)", got, found, err)
    }

    path := DaemonRuntimePath(base)
    if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
        t.Fatal(err)
    }
    if err := os.WriteFile(path, nil, 0o600); err != nil {
        t.Fatal(err)
    }
    got, found, err = ReadDaemonRuntime(base)
    if err != nil || found || !reflect.DeepEqual(got, zero) {
        t.Fatalf("empty = (%+v, %v, %v)", got, found, err)
    }

    if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
        t.Fatal(err)
    }
    got, found, err = ReadDaemonRuntime(base)
    if err == nil || found || !reflect.DeepEqual(got, zero) {
        t.Fatalf("malformed = (%+v, %v, %v), want zero/false/error", got, found, err)
    }
}

func TestRemoveDaemonRuntimeIsIdempotent(t *testing.T) {
    base := t.TempDir()
    if err := WriteDaemonRuntime(base, daemonRuntimeFixture()); err != nil {
        t.Fatal(err)
    }
    if err := RemoveDaemonRuntime(base); err != nil {
        t.Fatal(err)
    }
    if err := RemoveDaemonRuntime(base); err != nil {
        t.Fatalf("second removal: %v", err)
    }
}
~~~

- [ ] **Step 2: Prove the API is absent**

~~~bash
go test ./internal/health -run 'TestDaemonRuntime|TestReadDaemonRuntime|TestRemoveDaemonRuntime' -count=1
~~~

Expected: FAIL with undefined runtime symbols.

- [ ] **Step 3: Implement schema and storage**

Create internal/health/daemon_runtime.go:

~~~go
package health

import (
    "encoding/json"
    "fmt"
    "os"
    "path/filepath"
)

const DaemonRuntimeSchemaVersion = 1

type DaemonRuntimePathComponent struct {
    Path string `json:"path"`
    UID  uint32 `json:"uid"`
    Mode uint32 `json:"mode"`
}

type DaemonRuntimeScript struct {
    Path       string                       `json:"path"`
    State      string                       `json:"state"`
    Reason     string                       `json:"reason,omitempty"`
    Components []DaemonRuntimePathComponent `json:"components,omitempty"`
}

type DaemonRuntimeScripts struct {
    Pre  DaemonRuntimeScript `json:"pre"`
    Post DaemonRuntimeScript `json:"post"`
}

type DaemonRuntimeState struct {
    SchemaVersion   int                  `json:"schema_version"`
    PID             int                  `json:"pid"`
    StartTS         int64                `json:"start_ts"`
    ConfigPath      string               `json:"config_path"`
    DaemonUID       int                  `json:"daemon_uid"`
    PersonalScripts DaemonRuntimeScripts `json:"personal_scripts"`
}

func DaemonRuntimePath(baseDir string) string {
    return filepath.Join(baseDir, "identity", ".daemon_runtime.json")
}

func WriteDaemonRuntime(baseDir string, state DaemonRuntimeState) error {
    return writeJSONAtomic(DaemonRuntimePath(baseDir), state)
}

func ReadDaemonRuntime(baseDir string) (DaemonRuntimeState, bool, error) {
    data, err := os.ReadFile(DaemonRuntimePath(baseDir))
    if err != nil {
        if os.IsNotExist(err) {
            return DaemonRuntimeState{}, false, nil
        }
        return DaemonRuntimeState{}, false, fmt.Errorf("read daemon runtime: %w", err)
    }
    if len(data) == 0 {
        return DaemonRuntimeState{}, false, nil
    }
    var state DaemonRuntimeState
    if err := json.Unmarshal(data, &state); err != nil {
        return DaemonRuntimeState{}, false, fmt.Errorf("parse daemon runtime: %w", err)
    }
    return state, true, nil
}

func RemoveDaemonRuntime(baseDir string) error {
    if err := os.Remove(DaemonRuntimePath(baseDir)); err != nil && !os.IsNotExist(err) {
        return fmt.Errorf("remove daemon runtime: %w", err)
    }
    return nil
}
~~~

Unsupported schema is valid JSON here; Task 4 classifies it separately.

- [ ] **Step 4: Format and test the health package**

~~~bash
gofmt -w internal/health/daemon_runtime.go internal/health/daemon_runtime_test.go
go test ./internal/health -count=1
~~~

Expected: PASS.

- [ ] **Step 5: Detect changes and commit**

~~~bash
git add internal/health/daemon_runtime.go internal/health/daemon_runtime_test.go
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Then:

~~~bash
git commit -m "feat: persist daemon personal script runtime state"
~~~

---

### Task 3: Publish Runtime State from the Daemon

**Files:**
- Create: cmd/proxsave/daemon_runtime.go
- Create: cmd/proxsave/daemon_runtime_test.go
- Modify: cmd/proxsave/daemon.go:312-388,999-1030
- Test: cmd/proxsave/daemon_abandon_test.go:1688-1714

**Interfaces:**
- Consumes: Task 1 diagnostics and Task 2 runtime store.
- Produces: daemonRuntimeScriptFromDiagnostic, buildDaemonRuntimeState, (*daemon).publishDaemonRuntime, and lifecycle cleanup.

- [ ] **Step 1: Run lifecycle impact analysis**

~~~json
{"target":"run","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon.go","kind":"Method","includeTests":true,"summaryOnly":true}
{"target":"removeDaemonFiles","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon.go","includeTests":true}
~~~

Report hub risk and processes before editing.

- [ ] **Step 2: Write failing conversion/publication tests**

Create cmd/proxsave/daemon_runtime_test.go:

~~~go
package main

import (
    "bytes"
    "errors"
    "os"
    "strings"
    "testing"

    "github.com/tis24dev/proxsave/internal/config"
    "github.com/tis24dev/proxsave/internal/health"
    "github.com/tis24dev/proxsave/internal/logging"
    "github.com/tis24dev/proxsave/internal/types"
)

func TestBuildDaemonRuntimeStatePreservesStartupVerdicts(t *testing.T) {
    d := &daemon{cfg: &config.Config{BaseDir: "/opt/proxsave"}, configPath: "/etc/proxsave.env"}
    daemonUID := os.Geteuid()
    scripts := personalScriptsDiagnostics{
        Pre: personalScriptDiagnostic{
            Path: "/home/operator/pre.sh",
            State: personalScriptReadyWithWarning,
            Reason: "trusted administrator",
            DaemonUID: daemonUID,
            Components: []personalScriptPathComponent{
                {Path: "/home/operator/pre.sh", UID: 0, Mode: 0o755},
            },
        },
        Post: personalScriptDiagnostic{State: personalScriptNotConfigured, DaemonUID: daemonUID},
    }
    got := buildDaemonRuntimeState(d, 1_700_000_000, scripts)
    if got.SchemaVersion != health.DaemonRuntimeSchemaVersion ||
        got.PID != os.Getpid() || got.StartTS != 1_700_000_000 {
        t.Fatalf("runtime identity mismatch: %+v", got)
    }
    if got.ConfigPath != d.configPath || got.DaemonUID != daemonUID {
        t.Fatalf("runtime source mismatch: %+v", got)
    }
    if got.PersonalScripts.Pre.State != string(personalScriptReadyWithWarning) ||
        len(got.PersonalScripts.Pre.Components) != 1 {
        t.Fatalf("script mismatch: %+v", got.PersonalScripts.Pre)
    }
}

func TestPublishDaemonRuntimeWarnsButDoesNotFail(t *testing.T) {
    original := daemonRuntimeWrite
    daemonRuntimeWrite = func(string, health.DaemonRuntimeState) error {
        return errors.New("disk unavailable")
    }
    t.Cleanup(func() { daemonRuntimeWrite = original })

    logger := logging.New(types.LogLevelDebug, false)
    buf := &bytes.Buffer{}
    logger.SetOutput(buf)
    previous := logging.GetDefaultLogger()
    logging.SetDefaultLogger(logger)
    t.Cleanup(func() { logging.SetDefaultLogger(previous) })

    d := &daemon{cfg: &config.Config{BaseDir: t.TempDir()}, configPath: "/etc/proxsave.env"}
    d.publishDaemonRuntime(100, personalScriptsDiagnostics{})
    if out := buf.String(); !strings.Contains(out, "WARNING") || !strings.Contains(out, "disk unavailable") {
        t.Fatalf("missing warning:\n%s", out)
    }
}
~~~

Add a cleanup test:

~~~go
func TestDaemonFileCleanupRemovesRuntimeState(t *testing.T) {
    base := t.TempDir()
    if err := health.WriteDaemonPID(base, os.Getpid()); err != nil {
        t.Fatal(err)
    }
    if err := health.WriteDaemonInfo(base, health.DaemonInfo{PID: os.Getpid(), StartTS: 100}); err != nil {
        t.Fatal(err)
    }
    if err := health.WriteDaemonRuntime(base, health.DaemonRuntimeState{
        SchemaVersion: health.DaemonRuntimeSchemaVersion, PID: os.Getpid(), StartTS: 100,
    }); err != nil {
        t.Fatal(err)
    }
    d := &daemon{cfg: &config.Config{BaseDir: base}}
    d.removeDaemonFiles()
    paths := []string{
        health.DaemonPIDPath(base),
        health.DaemonInfoPath(base),
        health.DaemonRuntimePath(base),
    }
    for _, path := range paths {
        if _, err := os.Stat(path); !os.IsNotExist(err) {
            t.Errorf("cleanup left %s: %v", path, err)
        }
    }
}
~~~

- [ ] **Step 3: Prove the tests fail**

~~~bash
go test ./cmd/proxsave -run 'TestBuildDaemonRuntimeState|TestPublishDaemonRuntime|TestDaemonFileCleanupRemovesRuntimeState' -count=1
~~~

Expected: FAIL because publication helpers are absent and cleanup omits runtime state.

- [ ] **Step 4: Implement conversion and publication**

Create cmd/proxsave/daemon_runtime.go:

~~~go
package main

import (
    "os"

    "github.com/tis24dev/proxsave/internal/health"
    "github.com/tis24dev/proxsave/internal/logging"
)

var daemonRuntimeWrite = health.WriteDaemonRuntime

func daemonRuntimeScriptFromDiagnostic(in personalScriptDiagnostic) health.DaemonRuntimeScript {
    components := make([]health.DaemonRuntimePathComponent, 0, len(in.Components))
    for _, component := range in.Components {
        components = append(components, health.DaemonRuntimePathComponent{
            Path: component.Path,
            UID:  component.UID,
            Mode: uint32(component.Mode),
        })
    }
    return health.DaemonRuntimeScript{
        Path: in.Path, State: string(in.State), Reason: in.Reason, Components: components,
    }
}

func buildDaemonRuntimeState(d *daemon, startTS int64, scripts personalScriptsDiagnostics) health.DaemonRuntimeState {
    return health.DaemonRuntimeState{
        SchemaVersion: health.DaemonRuntimeSchemaVersion,
        PID:           os.Getpid(),
        StartTS:       startTS,
        ConfigPath:    d.configPath,
        DaemonUID:     os.Geteuid(),
        PersonalScripts: health.DaemonRuntimeScripts{
            Pre:  daemonRuntimeScriptFromDiagnostic(scripts.Pre),
            Post: daemonRuntimeScriptFromDiagnostic(scripts.Post),
        },
    }
}

func (d *daemon) publishDaemonRuntime(startTS int64, scripts personalScriptsDiagnostics) {
    state := buildDaemonRuntimeState(d, startTS, scripts)
    if err := daemonRuntimeWrite(d.cfg.BaseDir, state); err != nil {
        logging.Warning("daemon: runtime diagnostics unavailable because state publication failed: %v", err)
    }
}
~~~

- [ ] **Step 5: Wire one startup timestamp and bounded cleanup**

In daemon.run:

~~~go
scriptDiagnostics := validatePersonalScripts(d.cfg)
daemonStartTS := d.now().Unix()
~~~

Use daemonStartTS in the existing DaemonInfo, then call:

~~~go
d.publishDaemonRuntime(daemonStartTS, scriptDiagnostics)
~~~

inside the existing identity publication block before scheduling begins.

Inside the existing bounded removeDaemonFiles closure, add:

~~~go
if err := health.RemoveDaemonRuntime(d.cfg.BaseDir); err != nil {
    logging.Debug("daemon: remove daemon runtime failed: %v", err)
}
~~~

Update lifecycle comments to name all three files. Keep the same runWithin timeout and seam.

- [ ] **Step 6: Format and run lifecycle tests**

~~~bash
gofmt -w cmd/proxsave/daemon_runtime.go cmd/proxsave/daemon_runtime_test.go cmd/proxsave/daemon.go cmd/proxsave/daemon_abandon_test.go
go test ./cmd/proxsave -run 'TestBuildDaemonRuntimeState|TestPublishDaemonRuntime|TestDaemonFileCleanup|TestTheDaemonRunsTheTrustedPathGate|TestDaemonRefusesSecondInstance' -count=1
~~~

Expected: PASS, including deadline-bounded cleanup.

- [ ] **Step 7: Detect changes and commit**

~~~bash
git add cmd/proxsave/daemon_runtime.go cmd/proxsave/daemon_runtime_test.go cmd/proxsave/daemon.go cmd/proxsave/daemon_abandon_test.go cmd/proxsave/personal_scripts_audited_test.go
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Review lifecycle processes, then:

~~~bash
git commit -m "feat: publish daemon personal script runtime state"
~~~

---

### Task 4: Compare Resident and Current Script State

**Files:**
- Modify: cmd/proxsave/daemon_diagnostics.go:18-155
- Test: cmd/proxsave/daemon_diagnostics_test.go:1-100
- Test: cmd/proxsave/daemon_status_cli_test.go:116-157

**Interfaces:**
- Consumes: health.ReadDaemonRuntime, health.DaemonState, Config.ConfigPath, inspectPersonalScripts.
- Produces: daemonRuntimeDiagnostic, personalScriptComparison, personalScriptComparisons, availability/synchronization enums, expanded daemonDiagnostics.

- [ ] **Step 1: Run shared-model impact analysis**

~~~json
{"target":"daemonDiagnostics","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon_diagnostics.go","includeTests":true}
{"target":"collectDaemonDiagnostics","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon_diagnostics.go","includeTests":true}
~~~

Report CLI/dashboard consumers and risk.

- [ ] **Step 2: Write failing binding and drift tests**

Add:

~~~go
func TestResolveDaemonRuntimeRequiresLiveMatchingIdentity(t *testing.T) {
    original := daemonRuntimeReader
    t.Cleanup(func() { daemonRuntimeReader = original })

    record := health.DaemonRuntimeState{
        SchemaVersion: health.DaemonRuntimeSchemaVersion,
        PID: 42, StartTS: 100, ConfigPath: "/daemon.env", DaemonUID: 0,
        PersonalScripts: health.DaemonRuntimeScripts{
            Pre: health.DaemonRuntimeScript{Path: "/pre", State: "ready"},
            Post: health.DaemonRuntimeScript{State: "not-configured"},
        },
    }
    daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) {
        return record, true, nil
    }

    runtime, scripts := resolveDaemonRuntime(
        health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
        "/base",
    )
    if runtime.Availability != daemonRuntimeAvailable || scripts.Pre.Path != "/pre" {
        t.Fatalf("matching runtime rejected: runtime=%+v scripts=%+v", runtime, scripts)
    }

    stale, _ := resolveDaemonRuntime(
        health.DaemonState{ProcessAlive: true, PID: 43, HaveInfo: true, StartTS: 100},
        "/base",
    )
    if stale.Availability != daemonRuntimeStale {
        t.Fatalf("PID mismatch = %q, want stale", stale.Availability)
    }
}

func TestComparePersonalScriptDistinguishesConfigAndPathDrift(t *testing.T) {
    runtime := daemonRuntimeDiagnostic{
        Availability: daemonRuntimeAvailable,
        ConfigPath:   "/daemon.env",
    }
    running := personalScriptDiagnostic{Path: "/pre", State: personalScriptReady}
    current := running

    if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptInSync {
        t.Fatalf("equal comparison = %+v", got)
    }
    if got := comparePersonalScript(runtime, "/current.env", running, current); got.Synchronization != personalScriptConfigurationDrift {
        t.Fatalf("config source drift = %+v", got)
    }
    current = personalScriptDiagnostic{State: personalScriptNotConfigured}
    if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptConfigurationDrift {
        t.Fatalf("configured-to-empty drift = %+v", got)
    }
    current = running
    current.State = personalScriptReadyWithWarning
    current.Reason = "ownership changed"
    if got := comparePersonalScript(runtime, "/daemon.env", running, current); got.Synchronization != personalScriptPathStateChanged {
        t.Fatalf("path-state drift = %+v", got)
    }
}
~~~

Add the degraded-state table:

~~~go
func TestResolveDaemonRuntimeClassifiesDegradedStates(t *testing.T) {
    matching := health.DaemonRuntimeState{
        SchemaVersion: health.DaemonRuntimeSchemaVersion,
        PID: 42,
        StartTS: 100,
        PersonalScripts: health.DaemonRuntimeScripts{
            Pre: health.DaemonRuntimeScript{State: "not-configured"},
            Post: health.DaemonRuntimeScript{State: "not-configured"},
        },
    }
    tests := []struct {
        name      string
        state     health.DaemonState
        record    health.DaemonRuntimeState
        found     bool
        readErr   error
        want      daemonRuntimeAvailability
    }{
        {
            name: "missing", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
            found: false, want: daemonRuntimeMissing,
        },
        {
            name: "malformed", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
            readErr: errors.New("bad json"), want: daemonRuntimeInvalid,
        },
        {
            name: "unsupported", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 100},
            record: func() health.DaemonRuntimeState { r := matching; r.SchemaVersion = 99; return r }(),
            found: true, want: daemonRuntimeUnsupported,
        },
        {
            name: "PID mismatch", state: health.DaemonState{ProcessAlive: true, PID: 43, HaveInfo: true, StartTS: 100},
            record: matching, found: true, want: daemonRuntimeStale,
        },
        {
            name: "start mismatch", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: true, StartTS: 101},
            record: matching, found: true, want: daemonRuntimeStale,
        },
        {
            name: "missing identity", state: health.DaemonState{ProcessAlive: true, PID: 42, HaveInfo: false},
            record: matching, found: true, want: daemonRuntimeMissing,
        },
        {
            name: "no live daemon", state: health.DaemonState{},
            record: matching, found: true, want: daemonRuntimeNotApplicable,
        },
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            original := daemonRuntimeReader
            daemonRuntimeReader = func(string) (health.DaemonRuntimeState, bool, error) {
                return tc.record, tc.found, tc.readErr
            }
            t.Cleanup(func() { daemonRuntimeReader = original })
            got, _ := resolveDaemonRuntime(tc.state, "/base")
            if got.Availability != tc.want {
                t.Fatalf("availability = %q, want %q; runtime=%+v", got.Availability, tc.want, got)
            }
        })
    }
}
~~~

- [ ] **Step 3: Prove the model is absent**

~~~bash
go test ./cmd/proxsave -run 'TestResolveDaemonRuntime|TestComparePersonalScript|TestCollectDaemonDiagnostics' -count=1
~~~

Expected: FAIL with undefined comparison symbols.

- [ ] **Step 4: Define exact presentation-neutral types**

Add filepath and reflect to the existing imports, retain fmt and os, then add:

~~~go
type daemonRuntimeAvailability string

const (
    daemonRuntimeAvailable     daemonRuntimeAvailability = "available"
    daemonRuntimeMissing       daemonRuntimeAvailability = "missing"
    daemonRuntimeStale         daemonRuntimeAvailability = "stale"
    daemonRuntimeInvalid       daemonRuntimeAvailability = "invalid"
    daemonRuntimeUnsupported   daemonRuntimeAvailability = "unsupported"
    daemonRuntimeNotApplicable daemonRuntimeAvailability = "not-applicable"
)

type personalScriptSynchronization string

const (
    personalScriptInSync             personalScriptSynchronization = "in-sync"
    personalScriptConfigurationDrift personalScriptSynchronization = "configuration-drift"
    personalScriptPathStateChanged   personalScriptSynchronization = "path-state-changed"
    personalScriptRuntimeUnavailable personalScriptSynchronization = "runtime-state-unavailable"
    personalScriptSyncNotApplicable  personalScriptSynchronization = "not-applicable"
)

type daemonRuntimeDiagnostic struct {
    Availability daemonRuntimeAvailability
    Reason       string
    ConfigPath   string
    StartTS      int64
    DaemonUID    int
}

type personalScriptComparison struct {
    Running         personalScriptDiagnostic
    Current         personalScriptDiagnostic
    Synchronization personalScriptSynchronization
    SyncReason      string
}

type personalScriptComparisons struct {
    Pre  personalScriptComparison
    Post personalScriptComparison
}
~~~

Add these fields to daemonDiagnostics while temporarily retaining the existing Scripts personalScriptsDiagnostics field so Task 4 remains compilable before renderer migration:

~~~go
Runtime           daemonRuntimeDiagnostic
ScriptComparisons personalScriptComparisons
~~~

The transitional Scripts field continues to carry currentScripts during this task. Task 5 removes it after every renderer and fixture uses ScriptComparisons.

Add the seam:

~~~go
daemonRuntimeReader = health.ReadDaemonRuntime
~~~

- [ ] **Step 5: Bind and convert runtime records strictly**

Implement:

~~~go
func resolveDaemonRuntime(state health.DaemonState, baseDir string) (daemonRuntimeDiagnostic, personalScriptsDiagnostics) {
    if !state.ProcessAlive {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeNotApplicable,
            Reason: "daemon process is not live",
        }, personalScriptsDiagnostics{}
    }
    if !state.HaveInfo {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeMissing,
            Reason: "daemon identity record is unavailable",
        }, personalScriptsDiagnostics{}
    }

    record, found, err := daemonRuntimeReader(baseDir)
    if err != nil {
        return daemonRuntimeDiagnostic{Availability: daemonRuntimeInvalid, Reason: err.Error()}, personalScriptsDiagnostics{}
    }
    if !found {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeMissing,
            Reason: "running daemon did not publish runtime state",
        }, personalScriptsDiagnostics{}
    }
    if record.SchemaVersion != health.DaemonRuntimeSchemaVersion {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeUnsupported,
            Reason: fmt.Sprintf("runtime schema %d is unsupported", record.SchemaVersion),
        }, personalScriptsDiagnostics{}
    }
    if record.PID != state.PID || record.StartTS != state.StartTS {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeStale,
            Reason: "runtime PID/start does not match the live daemon",
        }, personalScriptsDiagnostics{}
    }

    pre, okPre := personalScriptDiagnosticFromRuntime(
        "PERSONAL_SCRIPT_PRE_RUN", record.DaemonUID, record.PersonalScripts.Pre,
    )
    post, okPost := personalScriptDiagnosticFromRuntime(
        "PERSONAL_SCRIPT_POST_RUN", record.DaemonUID, record.PersonalScripts.Post,
    )
    if !okPre || !okPost {
        return daemonRuntimeDiagnostic{
            Availability: daemonRuntimeInvalid,
            Reason: "runtime record contains an unknown personal-script state",
        }, personalScriptsDiagnostics{}
    }
    return daemonRuntimeDiagnostic{
        Availability: daemonRuntimeAvailable,
        ConfigPath: record.ConfigPath,
        StartTS: record.StartTS,
        DaemonUID: record.DaemonUID,
    }, personalScriptsDiagnostics{Pre: pre, Post: post}
}
~~~

Implement the conversion without accepting arbitrary persisted state strings:

~~~go
func personalScriptDiagnosticFromRuntime(
    key string,
    daemonUID int,
    in health.DaemonRuntimeScript,
) (personalScriptDiagnostic, bool) {
    state := personalScriptState(in.State)
    switch state {
    case personalScriptNotConfigured,
        personalScriptReady,
        personalScriptReadyWithWarning,
        personalScriptRefused:
    default:
        return personalScriptDiagnostic{}, false
    }
    components := make([]personalScriptPathComponent, 0, len(in.Components))
    for _, component := range in.Components {
        components = append(components, personalScriptPathComponent{
            Path: component.Path,
            UID:  component.UID,
            Mode: os.FileMode(component.Mode),
        })
    }
    return personalScriptDiagnostic{
        Key: key,
        Path: in.Path,
        State: state,
        Reason: in.Reason,
        DaemonUID: daemonUID,
        Components: components,
    }, true
}
~~~

- [ ] **Step 6: Implement comparison and wire the collector**

~~~go
func comparePersonalScript(
    runtime daemonRuntimeDiagnostic,
    currentConfigPath string,
    running personalScriptDiagnostic,
    current personalScriptDiagnostic,
) personalScriptComparison {
    comparison := personalScriptComparison{Running: running, Current: current}
    if runtime.Availability == daemonRuntimeNotApplicable {
        comparison.Synchronization = personalScriptSyncNotApplicable
        comparison.SyncReason = "no live daemon exists; current configuration is prospective"
        return comparison
    }
    if runtime.Availability != daemonRuntimeAvailable {
        comparison.Synchronization = personalScriptRuntimeUnavailable
        comparison.SyncReason = runtime.Reason
        return comparison
    }
    if filepath.Clean(runtime.ConfigPath) != filepath.Clean(currentConfigPath) ||
        running.Path != current.Path {
        comparison.Synchronization = personalScriptConfigurationDrift
        comparison.SyncReason = "restart the daemon to apply current personal-script configuration"
        return comparison
    }
    if running.State != current.State || running.Reason != current.Reason ||
        !reflect.DeepEqual(running.Components, current.Components) {
        comparison.Synchronization = personalScriptPathStateChanged
        comparison.SyncReason = "path ownership or mode changed after daemon startup"
        return comparison
    }
    comparison.Synchronization = personalScriptInSync
    return comparison
}
~~~

Wire collectDaemonDiagnostics:

~~~go
daemonUID := daemonUIDResolver(state)
currentScripts := personalScriptsInspector(cfg, daemonUID.Value)
runtimeDiagnostic, runningScripts := resolveDaemonRuntime(state, baseDir)

currentConfigPath := ""
if cfg != nil {
    currentConfigPath = cfg.ConfigPath
}
scripts := personalScriptComparisons{
    Pre: comparePersonalScript(
        runtimeDiagnostic, currentConfigPath, runningScripts.Pre, currentScripts.Pre,
    ),
    Post: comparePersonalScript(
        runtimeDiagnostic, currentConfigPath, runningScripts.Post, currentScripts.Post,
    ),
}
~~~

Return Runtime: runtimeDiagnostic, ScriptComparisons: scripts, and Scripts: currentScripts. Keep /proc UID as the independent live-process fact; do not replace it with persisted UID.

- [ ] **Step 7: Format and run collector tests**

~~~bash
gofmt -w cmd/proxsave/daemon_diagnostics.go cmd/proxsave/daemon_diagnostics_test.go cmd/proxsave/daemon_status_cli_test.go
go test ./cmd/proxsave -run 'TestResolveDaemonRuntime|TestComparePersonalScript|TestCollectDaemonDiagnostics|TestResolveDaemonEffectiveUID' -count=1
~~~

Expected: PASS, including daemon-ready/current-empty drift.

- [ ] **Step 8: Detect changes and commit**

~~~bash
git add cmd/proxsave/daemon_diagnostics.go cmd/proxsave/daemon_diagnostics_test.go cmd/proxsave/daemon_status_cli_test.go
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Then:

~~~bash
git commit -m "feat: compare daemon and current personal script state"
~~~

---

### Task 5: Render Authoritative CLI and Dashboard Diagnostics

**Files:**
- Modify: cmd/proxsave/daemon_diagnostics.go:157-228
- Modify: cmd/proxsave/dashboard.go:951-1030
- Test: cmd/proxsave/daemon_status_cli_test.go:160-261
- Test: cmd/proxsave/dashboard_test.go:328-420

**Interfaces:**
- Consumes: Task 4 runtime/comparison model.
- Produces: source-labeled running/current/synchronization output at INFO and dual component evidence at DEBUG.

- [ ] **Step 1: Run renderer impact analysis**

~~~json
{"target":"logDaemonDiagnostics","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon_diagnostics.go","includeTests":true}
{"target":"logPersonalScriptDiagnostic","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/daemon_diagnostics.go","includeTests":true}
{"target":"buildDaemonStatusPrompt","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/dashboard.go","includeTests":true}
{"target":"buildDashboardPersonalScriptLine","direction":"upstream","repo":"proxsave","file_path":"cmd/proxsave/dashboard.go","includeTests":true}
~~~

Report risk and both renderer consumers.

- [ ] **Step 2: Write failing output tests**

Build a daemonDiagnostics fixture with:

~~~go
Runtime: daemonRuntimeDiagnostic{
    Availability: daemonRuntimeAvailable,
    ConfigPath: "/opt/proxsave/configs/backup.env",
    StartTS: 1_700_000_000,
    DaemonUID: 1001,
},
ScriptComparisons: personalScriptComparisons{
    Pre: personalScriptComparison{
        Running: personalScriptDiagnostic{
            Path: script, State: personalScriptReady, DaemonUID: 1001,
        },
        Current: personalScriptDiagnostic{
            State: personalScriptNotConfigured, DaemonUID: 1001,
        },
        Synchronization: personalScriptConfigurationDrift,
        SyncReason: "restart the daemon to apply current personal-script configuration",
    },
    Post: personalScriptComparison{
        Running: personalScriptDiagnostic{
            Path: "/home/operator/post.sh",
            State: personalScriptReadyWithWarning,
            Reason: reason,
            DaemonUID: 1001,
        },
        Current: personalScriptDiagnostic{
            Path: "/home/operator/post.sh",
            State: personalScriptReadyWithWarning,
            Reason: reason,
            DaemonUID: 1001,
        },
        Synchronization: personalScriptInSync,
    },
},
~~~

Require CLI output:

~~~go
for _, want := range []string{
    "Running daemon configuration: /opt/proxsave/configs/backup.env",
    "Personal pre-run script:",
    "Running daemon: READY",
    "Current configuration: NOT CONFIGURED",
    "Synchronization: OUT OF SYNC",
    "Personal post-run script:",
    "READY WITH WARNING",
    "Synchronization: IN SYNC",
} {
    if !strings.Contains(out, want) {
        t.Errorf("missing %q:\n%s", want, out)
    }
}
~~~

Add the degraded CLI assertion:

~~~go
func TestLogDaemonDiagnosticsDoesNotCallUnavailableRuntimeNotConfigured(t *testing.T) {
    diagnostics := daemonDiagnostics{
        Runtime: daemonRuntimeDiagnostic{
            Availability: daemonRuntimeMissing,
            Reason: "running daemon did not publish runtime state",
        },
        ScriptComparisons: personalScriptComparisons{
            Pre: personalScriptComparison{
                Current: personalScriptDiagnostic{
                    Path: "/current/pre.sh",
                    State: personalScriptReady,
                },
                Synchronization: personalScriptRuntimeUnavailable,
                SyncReason: "running daemon did not publish runtime state",
            },
        },
    }
    logger := logging.New(types.LogLevelDebug, false)
    buf := &bytes.Buffer{}
    logger.SetOutput(buf)
    logDaemonDiagnostics(logger, diagnostics)
    out := buf.String()
    if !strings.Contains(out, "Running daemon state: UNAVAILABLE") {
        t.Fatalf("missing unavailable state:\n%s", out)
    }
    if strings.Contains(out, "Running daemon: NOT CONFIGURED") {
        t.Fatalf("unavailable runtime was mislabeled:\n%s", out)
    }
}
~~~

Update TestBuildDaemonStatusPromptRendersSanitizedPersonalScripts with the same comparison shape and inject control bytes into every new dynamic source:

~~~go
diagnostics := daemonDiagnostics{
    Runtime: daemonRuntimeDiagnostic{
        Availability: daemonRuntimeAvailable,
        ConfigPath: "/bad\x1b]0;runtime-config\x07/backup.env",
        StartTS: 1_700_000_000,
        DaemonUID: 1001,
    },
    ScriptComparisons: personalScriptComparisons{
        Pre: personalScriptComparison{
            Running: personalScriptDiagnostic{
                Path: "/safe/pre.sh", State: personalScriptReady,
            },
            Current: personalScriptDiagnostic{
                Path: "/safe/pre.sh", State: personalScriptReady,
            },
            Synchronization: personalScriptInSync,
        },
        Post: personalScriptComparison{
            Running: personalScriptDiagnostic{
                Path: "/bad\x1b]0;script-path\x07/post.sh",
                State: personalScriptReadyWithWarning,
                Reason: "unsafe\x1b]0;reason\x07owner",
            },
            Current: personalScriptDiagnostic{
                Path: "/bad\x1b]0;script-path\x07/post.sh",
                State: personalScriptReadyWithWarning,
                Reason: "unsafe\x1b]0;reason\x07owner",
            },
            Synchronization: personalScriptConfigurationDrift,
            SyncReason: "restart\x1b]0;sync-reason\x07daemon",
        },
    },
}
prompt := buildDaemonStatusPrompt(diagnostics)
assertNoRawInjection(t, prompt)
plain := ansi.Strip(prompt)
for _, forbidden := range []string{"runtime-config", "script-path", "reason", "sync-reason"} {
    if strings.Contains(plain, forbidden) {
        t.Fatalf("sanitizer retained %q:\n%s", forbidden, plain)
    }
}
for _, want := range []string{"Running daemon", "Current configuration", "IN SYNC", "OUT OF SYNC"} {
    if !strings.Contains(plain, want) {
        t.Errorf("dashboard diagnostic missing %q:\n%s", want, plain)
    }
}
~~~

- [ ] **Step 3: Prove old renderers fail**

~~~bash
go test ./cmd/proxsave -run 'TestLogDaemonDiagnostics|TestBuildDaemonStatusPrompt|TestRunDaemonStatusExit' -count=1
~~~

Expected: FAIL because renderers still consume one script view.

- [ ] **Step 4: Render ready-with-warning**

CLI:

~~~go
case personalScriptReadyWithWarning:
    logger.Warning("%s: READY WITH WARNING (%s): %s", label, path, reason)
~~~

Dashboard:

~~~go
case personalScriptReadyWithWarning:
    line += theme.WarningText.Render("READY WITH WARNING")
    if path != "" {
        line += theme.Subtle.Render(" (" + path + ")")
    }
    if reason != "" {
        line += theme.Subtle.Render(": " + reason)
    }
~~~

- [ ] **Step 5: Render runtime source and each comparison**

Add:

~~~go
func logPersonalScriptComparison(
    logger *logging.Logger,
    label string,
    runtime daemonRuntimeDiagnostic,
    comparison personalScriptComparison,
) {
    logger.Info("%s:", label)
    if runtime.Availability == daemonRuntimeAvailable {
        logPersonalScriptDiagnostic(logger, "  Running daemon", comparison.Running)
    } else if runtime.Availability == daemonRuntimeNotApplicable {
        logger.Info("  Running daemon: NOT RUNNING")
    } else {
        logger.Warning(
            "  Running daemon state: UNAVAILABLE (%s)",
            daemonDiagnosticText(runtime.Reason),
        )
    }
    logPersonalScriptDiagnostic(logger, "  Current configuration", comparison.Current)
    logPersonalScriptSynchronization(logger, comparison)
}
~~~

Implement the synchronization renderer exactly:

~~~go
func logPersonalScriptSynchronization(logger *logging.Logger, comparison personalScriptComparison) {
    reason := daemonDiagnosticText(comparison.SyncReason)
    switch comparison.Synchronization {
    case personalScriptInSync:
        logger.Info("  Synchronization: IN SYNC")
    case personalScriptConfigurationDrift:
        logger.Warning("  Synchronization: OUT OF SYNC (%s)", reason)
    case personalScriptPathStateChanged:
        logger.Warning("  Synchronization: PATH STATE CHANGED SINCE STARTUP (%s)", reason)
    case personalScriptRuntimeUnavailable:
        logger.Warning("  Synchronization: UNKNOWN (%s)", reason)
    case personalScriptSyncNotApplicable:
        logger.Info("  Synchronization: NOT APPLICABLE")
    default:
        logger.Warning("  Synchronization: UNKNOWN")
    }
}
~~~

Before the script blocks:

~~~go
if diagnostics.Runtime.Availability == daemonRuntimeAvailable {
    logger.Info(
        "Running daemon configuration: %s",
        daemonDiagnosticText(diagnostics.Runtime.ConfigPath),
    )
    logger.Info(
        "Running daemon loaded at: %s",
        time.Unix(diagnostics.Runtime.StartTS, 0).Format(time.RFC3339),
    )
} else if diagnostics.Runtime.Availability != daemonRuntimeNotApplicable {
    logger.Warning(
        "Running daemon personal-script state: UNAVAILABLE (%s)",
        daemonDiagnosticText(diagnostics.Runtime.Reason),
    )
}
~~~

Call the comparison renderer and emit source-specific evidence:

~~~go
logPersonalScriptComparison(
    logger, "Personal pre-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Pre,
)
logPersonalScriptComparison(
    logger, "Personal post-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Post,
)
logPersonalScriptEvidence(logger, "running daemon pre-run", diagnostics.ScriptComparisons.Pre.Running)
logPersonalScriptEvidence(logger, "current config pre-run", diagnostics.ScriptComparisons.Pre.Current)
logPersonalScriptEvidence(logger, "running daemon post-run", diagnostics.ScriptComparisons.Post.Running)
logPersonalScriptEvidence(logger, "current config post-run", diagnostics.ScriptComparisons.Post.Current)
~~~

Implement the dashboard counterpart from the same model:

~~~go
func buildDashboardPersonalScriptComparison(
    label string,
    runtime daemonRuntimeDiagnostic,
    comparison personalScriptComparison,
) string {
    var b strings.Builder
    b.WriteString(theme.Text.Render(label + ":"))
    b.WriteString("\n")
    if runtime.Availability == daemonRuntimeAvailable {
        b.WriteString(buildDashboardPersonalScriptLine("  Running daemon", comparison.Running))
    } else if runtime.Availability == daemonRuntimeNotApplicable {
        b.WriteString(theme.Subtle.Render("  Running daemon: NOT RUNNING"))
    } else {
        reason := components.SanitizeText(runtime.Reason)
        b.WriteString(theme.WarningText.Render("  Running daemon state: UNAVAILABLE"))
        if reason != "" {
            b.WriteString(theme.Subtle.Render(" (" + reason + ")"))
        }
    }
    b.WriteString("\n")
    b.WriteString(buildDashboardPersonalScriptLine("  Current configuration", comparison.Current))
    b.WriteString("\n")
    b.WriteString(buildDashboardPersonalScriptSynchronization(comparison))
    return b.String()
}

func buildDashboardPersonalScriptSynchronization(comparison personalScriptComparison) string {
    reason := components.SanitizeText(comparison.SyncReason)
    prefix := theme.Text.Render("  Synchronization: ")
    switch comparison.Synchronization {
    case personalScriptInSync:
        return prefix + theme.SuccessText.Render("IN SYNC")
    case personalScriptConfigurationDrift:
        return prefix + theme.WarningText.Render("OUT OF SYNC") + theme.Subtle.Render(" (" + reason + ")")
    case personalScriptPathStateChanged:
        return prefix + theme.WarningText.Render("PATH STATE CHANGED SINCE STARTUP") + theme.Subtle.Render(" (" + reason + ")")
    case personalScriptRuntimeUnavailable:
        return prefix + theme.WarningText.Render("UNKNOWN") + theme.Subtle.Render(" (" + reason + ")")
    case personalScriptSyncNotApplicable:
        return prefix + theme.Subtle.Render("NOT APPLICABLE")
    default:
        return prefix + theme.WarningText.Render("UNKNOWN")
    }
}
~~~

Call buildDashboardPersonalScriptComparison for pre and post:

~~~go
b.WriteString(buildDashboardPersonalScriptComparison(
    "Personal pre-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Pre,
))
b.WriteString("\n")
b.WriteString(buildDashboardPersonalScriptComparison(
    "Personal post-run script", diagnostics.Runtime, diagnostics.ScriptComparisons.Post,
))
~~~

Sanitize the runtime configuration path before rendering it above those blocks. After all CLI/dashboard code and test fixtures use ScriptComparisons, remove the transitional Scripts personalScriptsDiagnostics field from daemonDiagnostics and stop populating it in collectDaemonDiagnostics.

- [ ] **Step 6: Preserve non-execution, silence, and exit codes**

Move the refused test fixture in TestRunDaemonStatusExitRemainsBasedOnDaemonState into a personalScriptComparison and retain expected exit success.

Keep the marker assertion in TestLogDaemonDiagnosticsUsesStandardEnvelopeAndSeverity.

~~~bash
gofmt -w cmd/proxsave/daemon_diagnostics.go cmd/proxsave/daemon_status_cli_test.go cmd/proxsave/dashboard.go cmd/proxsave/dashboard_test.go
go test ./cmd/proxsave -run 'TestLogDaemonDiagnostics|TestBuildDaemonStatusPrompt|TestRunDaemonStatus|TestPersonalScriptsAreInvisible' -count=1
~~~

Expected: PASS; no marker is created, no control payload survives, and exit behavior is unchanged.

- [ ] **Step 7: Detect changes and commit**

~~~bash
git add cmd/proxsave/daemon_diagnostics.go cmd/proxsave/daemon_status_cli_test.go cmd/proxsave/dashboard.go cmd/proxsave/dashboard_test.go
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Then:

~~~bash
git commit -m "feat: report authoritative personal script diagnostics"
~~~

---

### Task 6: Document and Fully Verify the Contract

**Files:**
- Modify: docs/DAEMON.md:202-260
- Modify: docs/TROUBLESHOOTING.md:208-245
- Modify: docs/SECURITY.md:40-60
- Verify: every file changed by Tasks 1-5

**Interfaces:**
- Consumes: final policy/runtime/synchronization vocabulary.
- Produces: operator guidance and a release-ready verified branch.

- [ ] **Step 1: Update daemon documentation**

Use this exact semantic contract:

~~~markdown
The script file itself must be owned by root (or by the daemon UID when the
service deliberately runs as another user), executable, and not writable by
group or others. Symlinked paths and loosely writable non-sticky directories
are refused.

A non-loosely-writable parent owned by another UID, such as a mode-0700 user
home, is accepted with READY WITH WARNING. The configured path is an explicit
trust decision by the root administrator: that owner can replace descendants
which the daemon later executes with daemon privileges.
~~~

Document all four states and one startup warning per advisory/refusal.

- [ ] **Step 2: Update troubleshooting guidance**

Document:

~~~markdown
- Running daemon is the state captured and applied at daemon startup.
- Current configuration is what a restart would load and how that path looks now.
- OUT OF SYNC means the personal-script path or config source changed; restart
  proxsave-daemon.service.
- PATH STATE CHANGED SINCE STARTUP means ownership, mode, or verdict differs
  from the startup snapshot.
- RUNNING DAEMON STATE UNAVAILABLE indicates an old/not-restarted daemon or a
  runtime-publication failure. It does not mean the script is unconfigured.
~~~

Keep namei, journalctl, and restart commands. Remove the unconditional requirement to move a script out of a mode-0700 user home.

- [ ] **Step 3: Update the security model**

State that root can traverse a UID-1000 mode-0700 home. Explain that ProxSave intentionally trusts a foreign-owned, non-loosely-writable ancestor selected in root-owned config, while the script target itself remains root/daemon-owned and non-loosely-writable.

- [ ] **Step 4: Run complete verification**

~~~bash
gofmt -w cmd/proxsave/personal_scripts_inspection.go cmd/proxsave/personal_scripts_gate.go cmd/proxsave/daemon_runtime.go cmd/proxsave/daemon_runtime_test.go cmd/proxsave/daemon.go cmd/proxsave/daemon_diagnostics.go cmd/proxsave/daemon_diagnostics_test.go cmd/proxsave/daemon_status_cli_test.go cmd/proxsave/dashboard.go cmd/proxsave/dashboard_test.go internal/health/daemon_runtime.go internal/health/daemon_runtime_test.go
go test -race ./internal/health ./cmd/proxsave
go test ./...
make lint
make build
~~~

Expected: every command exits 0. If only a documented sandbox restriction blocks a system probe, rerun that exact test outside the sandbox and record both results; do not weaken it.

- [ ] **Step 5: Run issue #306 regressions explicitly**

~~~bash
go test ./cmd/proxsave -run 'TestInspectPersonalScriptStatesAndReasons|TestApplyPersonalScriptDiagnosticKeepsAdvisoryPathAndWarnsOnce|TestBuildDaemonRuntimeStatePreservesStartupVerdicts|TestResolveDaemonRuntimeRequiresLiveMatchingIdentity|TestComparePersonalScriptDistinguishesConfigAndPathDrift|TestLogDaemonDiagnosticsUsesStandardEnvelopeAndSeverity|TestBuildDaemonStatusPromptRendersSanitizedPersonalScripts' -count=1
~~~

Review fixtures to confirm:

- daemon UID 0 + root target below UID-1000 mode-0700 home -> retained with warning;
- daemon READY + current file NOT CONFIGURED -> out-of-sync, not false live state;
- stale/unavailable runtime -> explicit unknown, never live NOT CONFIGURED.

- [ ] **Step 6: Detect documentation and branch-wide changes, then commit**

~~~bash
git add docs/DAEMON.md docs/TROUBLESHOOTING.md docs/SECURITY.md
~~~

~~~json
{"scope":"staged","repo":"proxsave","worktree":"/opt/proxsave-git"}
{"scope":"compare","base_ref":"main","repo":"proxsave","worktree":"/opt/proxsave-git"}
~~~

Confirm staged changes are documentation-only and branch comparison contains the expected policy, persistence, daemon lifecycle, comparison, rendering, tests, and documentation. Then:

~~~bash
git commit -m "docs: explain personal script runtime diagnostics"
~~~

- [ ] **Step 7: Confirm repository state**

~~~bash
git status --short
git log --oneline -8
~~~

Expected: no tracked changes remain and the six implementation commits follow the approved design/plan artifacts.
