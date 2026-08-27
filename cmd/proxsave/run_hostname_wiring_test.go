package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// runHostnameCallSite names one call the run's own hostname has to reach.
type runHostnameCallSite struct {
	file      string
	enclosing string
	callee    string
	argIndex  int
	wantArg   string
	// consequence names what a dropped argument actually costs at THIS call site,
	// so a failure reads as a defect report rather than a style complaint.
	consequence string
	// unconditional additionally requires the call to be a top-level statement of
	// the enclosing function, so wrapping it in a branch, a loop, a defer or a
	// closure is rejected. Set only on rows where the seam is the CALL ITSELF
	// happening on every run, not where an argument arrives.
	unconditional bool
}

// TestRunHostnameWiredIntoPinnedCallSites is the wiring guard for discussion #292 on
// the writing side. The run resolves its name once (resolveHostname, which prefers
// "hostname -f") and every consumer has to be handed THAT value: the archives are
// stamped with it, so retention only recognises its own work if the same string
// reaches the storage constructors.
//
// A behavioural test cannot reach these call sites. initializeSecondaryStorage and
// initializeCloudStorage return only a *storage.FilesystemInfo and never hand the
// backend back, the cloud path discards it entirely when the remote cannot be
// probed, and hostAliases is unexported, so package main can observe none of it.
// Replacing opts.hostname with "" at any of these three sites therefore compiles,
// passes every test, and silently reinstates the reported bug on that backend.
//
// This guard is deliberately NOT exhaustive: main_restore_decrypt.go's
// restoreSupportStats calls resolveHostname() again instead of reading rt.hostname.
// That second resolution is pre-existing, harmless (resolveHostname is deterministic
// within a run) and out of scope here, so do not read a green run as proof that
// every consumer is wired.
//
// The FIRST row differs in kind from the other five and is listed first because it
// is the head of the chain. They pin that an ARGUMENT arrives at a call; it pins that
// the CALL HAPPENS AT ALL, because deleting bootstrapRuntime's call to
// initializeRunLogFile is compile clean and leaves rt.hostname empty for the whole
// run, which every one of the other five rows would still read as green. It
// therefore also sets unconditional, the same property TestWhatsnewWarnWiredInBootstrap
// already applies to a neighbouring call in the same function, for the same reason:
// a call wrapped in a branch is a call some runs skip.
//
// If a legitimate refactor moves one of these calls, update the row to name the new
// file, function or argument position. Do not delete a row: the argument still has
// to arrive, and this is the only thing that checks it does.
const (
	retentionConsequence     = "retention then reads os.Hostname() only, so every archive written under the FQDN scopes out as foreign and nothing is ever pruned"
	accessControlConsequence = "the access control host check then reads os.Hostname() only, so every same-host restore of an access control bundle warns about a hostname change that did not happen"
	bootstrapConsequence     = "rt.hostname is never assigned at all, so every storage backend falls back to os.Hostname() for retention while the writer keeps stamping the FQDN (discussion #292 on local, secondary and cloud at once) and the access control host check collapses to os.Hostname()"
)

func TestRunHostnameWiredIntoPinnedCallSites(t *testing.T) {
	sites := []runHostnameCallSite{
		{file: "main_runtime.go", enclosing: "bootstrapRuntime", callee: "initializeRunLogFile", argIndex: 0, wantArg: "rt", consequence: bootstrapConsequence, unconditional: true},
		{file: "backup_storage.go", enclosing: "initializePrimaryStorage", callee: "storage.NewLocalStorage", argIndex: 2, wantArg: "opts.hostname", consequence: retentionConsequence},
		{file: "backup_storage.go", enclosing: "initializeSecondaryStorage", callee: "storage.NewSecondaryStorage", argIndex: 2, wantArg: "opts.hostname", consequence: retentionConsequence},
		{file: "backup_storage.go", enclosing: "initializeCloudStorage", callee: "storage.NewCloudStorage", argIndex: 2, wantArg: "opts.hostname", consequence: retentionConsequence},
		{file: "main_restore_decrypt.go", enclosing: "runRestoreCLI", callee: "orchestrator.RunRestoreWorkflow", argIndex: 4, wantArg: "rt.hostname", consequence: accessControlConsequence},
		{file: "main_restore_decrypt.go", enclosing: "runRestoreTUI", callee: "orchestrator.RunRestoreWorkflowTUI", argIndex: 6, wantArg: "rt.hostname", consequence: accessControlConsequence},
	}

	for _, site := range sites {
		t.Run(site.enclosing+" passes "+site.wantArg+" to "+site.callee, func(t *testing.T) {
			fn := findFuncDecl(t, site.file, site.enclosing)

			// Collect inside the walk, assert after it: t.Fatalf from an ast.Inspect
			// closure would abandon the walk mid-tree.
			found := false
			gotArg := ""
			argCount := 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || gotypes.ExprString(call.Fun) != site.callee {
					return true
				}
				found = true
				argCount = len(call.Args)
				if site.argIndex < len(call.Args) {
					gotArg = gotypes.ExprString(call.Args[site.argIndex])
				}
				return true
			})

			if !found {
				t.Fatalf("%s: %s no longer calls %s; the run hostname has to reach it (discussion #292). If the call moved, point this row at its new home rather than deleting it",
					site.file, site.enclosing, site.callee)
			}
			if site.argIndex >= argCount {
				t.Fatalf("%s: %s calls %s with %d argument(s), so there is no argument %d to carry the run hostname (discussion #292)",
					site.file, site.enclosing, site.callee, argCount, site.argIndex)
			}
			if gotArg != site.wantArg {
				t.Fatalf("%s: %s calls %s with arg %d = %q, want %q. Dropping it means %s (discussion #292)",
					site.file, site.enclosing, site.callee, site.argIndex, gotArg, site.wantArg, site.consequence)
			}
			if site.unconditional && !callIsTopLevelStatement(fn, site.callee) {
				t.Fatalf("%s: %s calls %s, but not as an unconditional top-level statement: it is wrapped in a branch, a loop, a defer or a closure, so some runs skip it, and then %s (discussion #292)",
					site.file, site.enclosing, site.callee, site.consequence)
			}
		})
	}
}

// TestBackupModeOptionsCarryTheRunHostname pins the ORIGIN of the plumb, which the
// call-site guard above cannot see: opts.hostname only carries the run's name
// because dispatchBackupMode copies rt.hostname into the literal.
//
// Deleting that one key is compile-clean and still invisible in the archives, because
// runConfiguredBackup recomputes the name with resolveHostname() when opts.hostname is
// empty (backup_execution.go). The names on disk stay correct while retention loses
// its alias, which is discussion #292 all over again. That recompute is no longer
// silent, runHostnameOrReport warns and the run cannot finish green, but a warning is
// a report and not a pin: this test is what keeps the origin wired in CI.
func TestBackupModeOptionsCarryTheRunHostname(t *testing.T) {
	fn := findFuncDecl(t, "main_restore_decrypt.go", "dispatchBackupMode")

	found := false
	gotValue := ""
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || gotypes.ExprString(lit.Type) != "backupModeOptions" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "hostname" {
				found = true
				gotValue = gotypes.ExprString(kv.Value)
			}
		}
		return true
	})

	if !found {
		t.Fatal("main_restore_decrypt.go: dispatchBackupMode no longer sets hostname: rt.hostname on backupModeOptions. Without it every storage backend falls back to os.Hostname() for retention while the archives keep the FQDN, which is discussion #292")
	}
	if gotValue != "rt.hostname" {
		t.Fatalf("main_restore_decrypt.go: dispatchBackupMode sets hostname: %s, want rt.hostname (discussion #292)", gotValue)
	}
}

// callIsTopLevelStatement reports whether fn's body contains a direct, unconditional
// call to callee: a bare call statement, or an assignment whose single right-hand
// side is that call. A call nested inside an if, a for, a defer or a closure is not a
// statement of fn.Body.List, so it yields false, which is exactly how a
// branch-wrapped call gets rejected.
//
// It deliberately says nothing about WHERE in the body the statement sits. Statement
// order is not the seam here, and an assertion over statement indexes would go red on
// any unrelated insertion into a bootstrap function that keeps growing, which is a
// false-positive generator rather than a pin.
//
// It also says nothing about REACHABILITY, and that gap is real rather than
// theoretical: an early return inserted ABOVE the call leaves the call a top-level
// statement, so this predicate stays green while every run taking that branch skips
// it, and with such a return in place the entire repository suite passes. That is
// covered behaviourally by TestBootstrapRuntimeNamesEveryRunItHandsBack, which calls
// bootstrapRuntime and reads the name it hands back.
//
// Do NOT grow this into a reachability analyser. It was measured against a battery
// of thirteen compile-clean mutations: the behavioural test catches eleven, a
// hand-written reachability pass catches six, and the three it cannot reach at all
// are an early exit inside a helper, a non-terminating branch, and the name being
// unset one line AFTER the call. A static pass would also have to know that
// bootstrapRuntime's two existing config-error bails are legitimate, since they
// abort the process rather than produce a nameless run, which means hard-coding
// which return result is the ok flag. Keep this predicate for what it is cheap and
// certain about: it still catches a call moved into a defer, or wrapped in a branch
// whose condition happens to hold under test and not in production, which is the one
// class the behavioural test structurally cannot see.
//
// whatsnew_warn_test.go keeps its own local copy of this predicate on purpose rather
// than calling this one: that guard ALSO pins statement order (maybeWarnWhatsnew has
// to run after checkForUpdates), which this one must not, and rewiring a green
// existing pin to share a helper buys nothing while putting it at risk.
func callIsTopLevelStatement(fn *ast.FuncDecl, callee string) bool {
	for _, stmt := range fn.Body.List {
		var call *ast.CallExpr
		switch s := stmt.(type) {
		case *ast.ExprStmt:
			call, _ = s.X.(*ast.CallExpr)
		case *ast.AssignStmt:
			if len(s.Rhs) == 1 {
				call, _ = s.Rhs[0].(*ast.CallExpr)
			}
		}
		if call != nil && gotypes.ExprString(call.Fun) == callee {
			return true
		}
	}
	return false
}

// findFuncDecl parses one production file of this package and returns the named
// top-level function. Parsing a single named file matches the idiom the other
// structural guards in this package use (see whatsnew_wiring_test.go).
func findFuncDecl(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	t.Fatalf("%s: %s not found; this guard has gone stale", file, name)
	return nil
}

// TestInitializeRunLogFileAssignsTheRunHostnameInBothModes is the head of the
// hostname chain, the one link the structural guards above cannot reach: they pin
// that rt.hostname is PASSED to the consumers, never that it was ever given a
// value. Two compile-clean drops leave the whole module suite green without it.
// Emptying the assignment reinstates discussion #292 on local, secondary and cloud
// at once, because every backend then falls back to os.Hostname() for retention
// while the writer keeps stamping the FQDN into the archives. Moving the same
// assignment below the "if rt.args.Restore { return }" guard is a plausible
// refactor that leaves backup runs untouched and every restore run nameless, so
// the access control host check collapses to os.Hostname() and warns about a
// hostname change that did not happen.
//
// It does NOT pin which function resolves the name: swapping resolveHostname for
// os.Hostname keeps this green on any host where the two agree. That is what
// TestRunHostnameComesFromResolveHostname below is for.
func TestInitializeRunLogFileAssignsTheRunHostnameInBothModes(t *testing.T) {
	// initializeRunLogFile's last step logs "Log file opened" through the package
	// DEFAULT logger, not rt.logger, so swap in a quiet one for the duration. Same
	// save-and-restore idiom as backup_stream_test.go.
	prevDefault := logging.GetDefaultLogger()
	logging.SetDefaultLogger(logging.New(types.LogLevelNone, false))
	t.Cleanup(func() { logging.SetDefaultLogger(prevDefault) })

	modes := []struct {
		name    string
		restore bool
	}{
		{name: "backup mode", restore: false},
		{name: "restore mode", restore: true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			// initializeRunLogFile publishes the run's log path through the
			// environment in backup mode; t.Setenv restores what this process had.
			// It also forbids t.Parallel, which is correct here.
			t.Setenv("LOG_FILE", "")

			logger := logging.New(types.LogLevelNone, false)
			t.Cleanup(func() { _ = logger.CloseLogFile() })

			rt := &appRuntime{
				ctx:    context.Background(),
				args:   &cli.Args{Restore: mode.restore},
				deps:   defaultAppDeps(),
				cfg:    &config.Config{LogPath: t.TempDir()},
				logger: logger,
			}

			// Bracket the call: equality is asserted only when the two resolutions
			// around it agree, so a resolver that flaps mid-test cannot fail this
			// pin for an environment reason. The non-empty assertion, which is what
			// catches both drops, is unconditional.
			before := resolveHostname()
			initializeRunLogFile(rt)
			after := resolveHostname()

			if rt.hostname == "" {
				t.Fatalf("initializeRunLogFile left rt.hostname empty in %s. Every storage backend then falls back to os.Hostname() for retention while the writer keeps stamping the FQDN, which is discussion #292 on local, secondary and cloud at once, and the access control host check collapses to os.Hostname() so every same-host restore of an access control bundle warns about a hostname change that did not happen", mode.name)
			}
			if before == after && rt.hostname != before {
				t.Fatalf("rt.hostname = %q in %s, want %q: the run's name must come from resolveHostname, the same function that spells the archives (discussion #292)", rt.hostname, mode.name, before)
			}
			if before != after {
				t.Logf("resolveHostname returned %q then %q, so the equality half is skipped for this run; TestRunHostnameComesFromResolveHostname covers the same ground without depending on the resolver", before, after)
			}
		})
	}
}

// TestRunHostnameComesFromResolveHostname is the machine-independent half. A third
// compile-clean drop replaces resolveHostname() with a bare os.Hostname(), which
// the behavioural pin above can only see on a host where "hostname -f" differs
// from "hostname" - false in most CI containers.
//
// It pins WHICH function resolves the run's name and deliberately NOT where the
// assignment sits: moving the assignment below the restore guard leaves this green
// by design, which is why the two pins are complementary rather than duplicates.
func TestRunHostnameComesFromResolveHostname(t *testing.T) {
	fn := findFuncDecl(t, "main_runtime.go", "initializeRunLogFile")

	// Collect inside the walk, assert after it: t.Fatalf from an ast.Inspect
	// closure would abandon the walk mid-tree.
	var assigned []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || gotypes.ExprString(assign.Lhs[0]) != "rt.hostname" {
			return true
		}
		if len(assign.Rhs) != 1 {
			assigned = append(assigned, "<multi-value assignment>")
			return true
		}
		assigned = append(assigned, gotypes.ExprString(assign.Rhs[0]))
		return true
	})

	if len(assigned) != 1 {
		t.Fatalf("main_runtime.go: initializeRunLogFile assigns rt.hostname %d time(s) (%q), want exactly one. The run's name is resolved once and handed down; a second assignment means two sources of truth for the string the archives are stamped with (discussion #292)", len(assigned), assigned)
	}
	if assigned[0] != "resolveHostname()" {
		t.Fatalf("main_runtime.go: initializeRunLogFile assigns rt.hostname = %s, want resolveHostname(). os.Hostname() reports the kernel short name while the writer stamps what \"hostname -f\" returns, so retention would scope every archive out as foreign and prune nothing (discussion #292)", assigned[0])
	}
}

// TestBootstrapRuntimeNamesEveryRunItHandsBack is the behavioural half of the FIRST
// row of TestRunHostnameWiredIntoPinnedCallSites, the only row whose seam is the
// CALL ITSELF happening on every run rather than an argument's spelling.
//
// That row leans on callIsTopLevelStatement, which asks only whether the call is a
// statement of bootstrapRuntime's body. It never looks at what runs BEFORE it, so an
// early return placed above initializeRunLogFile(rt) leaves the call a top-level
// statement and the structural guard green while every run taking that branch
// finishes nameless. Measured, not assumed: with such a return inserted, the ENTIRE
// repository suite passes. bootstrapRuntime already returns early twice above that
// line, so a third guard is a plausible edit rather than a contrived one.
//
// This calls the function for real and reads what the call exists to produce, which
// no AST check can see. toolVersion is deliberately empty: checkForUpdates returns
// before any HTTP on an empty version, and whatsnew treats it as a dev build and
// returns before any disk read, so one input choice keeps a live GitHub probe and a
// real seen-flag read out of a unit test without stubbing either.
func TestBootstrapRuntimeNamesEveryRunItHandsBack(t *testing.T) {
	// initializeRunLogger installs the run's logger as the package DEFAULT logger,
	// so save and restore it: same idiom as the test above.
	prevDefault := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prevDefault) })

	modes := []struct {
		name    string
		restore bool
	}{
		{name: "backup mode", restore: false},
		{name: "restore mode", restore: true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			// loadRunConfig republishes BASE_DIR and initializeRunLogFile publishes
			// LOG_FILE; t.Setenv restores what this process had and forbids
			// t.Parallel, which is correct here.
			t.Setenv("BASE_DIR", "")
			t.Setenv("LOG_FILE", "")

			dir := t.TempDir()
			configPath := filepath.Join(dir, "backup.env")
			// Everything network- or filesystem-shaped is off, so bootstrapRuntime
			// runs its config load and its logger setup and nothing else. DRY_RUN
			// keeps the whatsnew seen-flag write off the disk as well.
			configBody := strings.Join([]string{
				`BACKUP_PATH="` + filepath.Join(dir, "backups") + `"`,
				`LOG_PATH="` + filepath.Join(dir, "logs") + `"`,
				`DRY_RUN="true"`,
				`DEBUG_LEVEL="0"`,
				`SECONDARY_ENABLED="false"`,
				`CLOUD_ENABLED="false"`,
				`SET_BACKUP_PERMISSIONS="false"`,
				`PROFILING_ENABLED="false"`,
				"",
			}, "\n")
			if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}

			rt, exitCode, ok := bootstrapRuntime(
				context.Background(),
				&cli.Args{ConfigPath: configPath, Restore: mode.restore, DryRun: true},
				logging.NewBootstrapLogger(),
				&environment.EnvironmentInfo{},
				"",
			)
			t.Cleanup(func() {
				if rt == nil {
					return
				}
				if rt.sessionLogCloser != nil {
					rt.sessionLogCloser()
				}
				if rt.logger != nil {
					_ = rt.logger.CloseLogFile()
				}
			})

			// Reported separately from the hostname assertion on purpose. A config
			// load that this synthetic file no longer satisfies must not be read as
			// a dropped hostname, or the next person weakens this pin for the wrong
			// reason.
			if !ok {
				t.Fatalf("bootstrapRuntime refused the run in %s (exit code %d); this pin needs a run that gets past the config load, so fix the fixture rather than the guard", mode.name, exitCode)
			}
			if rt.hostname == "" {
				t.Fatalf("bootstrapRuntime handed back a named-nothing runtime in %s: nothing on the path to initializeRunLogFile may return before it. Every storage backend then falls back to os.Hostname() for retention while the writer keeps stamping the FQDN, which is discussion #292 on local, secondary and cloud at once, and the access control host check collapses to os.Hostname()", mode.name)
			}
		})
	}
}

// TestRunHostnameOrReportReportsADroppedPlumb pins the one place the run's identity
// used to go wrong in quiet.
//
// Every other consumer already fails loudly. An empty written name reaches the three
// storage constructors as "no alias", and applyRetentionHostScope then warns twice
// per backend on any host whose archives carry an FQDN. Only the writer papered over
// the drop by resolving the name a second time, which is what let the archives keep
// looking right while retention stopped recognising them (discussion #292).
func TestRunHostnameOrReportReportsADroppedPlumb(t *testing.T) {
	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(io.Discard)

	if got := runHostnameOrReport(logger, "pve.home.arpa"); got != "pve.home.arpa" {
		t.Fatalf("runHostnameOrReport returned %q, want the wired name passed through unchanged", got)
	}
	if n := logger.WarningCount(); n != 0 {
		t.Fatalf("a correctly wired run logged %d warning(s); only a dropped plumb may say anything, or every ordinary machine finishes on a non-zero exit code", n)
	}

	if got := runHostnameOrReport(logger, ""); got == "" {
		t.Fatal(`runHostnameOrReport("") returned an empty name: the archives would be written as "-backup-<ts>", which carries no host token, so no host could attribute them and no host would ever prune them`)
	}
	if n := logger.WarningCount(); n != 1 {
		t.Fatalf("a dropped hostname plumb logged %d warning(s), want 1: without one the run stays green while every storage backend has lost the alias retention needs (discussion #292), which is how the defect reached a release", n)
	}
}
