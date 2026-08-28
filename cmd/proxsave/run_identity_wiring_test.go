package main

import (
	"go/ast"
	gotypes "go/types"
	"testing"
)

// TestServerIdentityResolvedBeforeTheStorageBackends is the wiring guard for the
// second ownership signal, and it pins a property the hostname guard's argIndex rows
// cannot express.
//
// Retention reads this host's server identity from cfg.ServerID inside the three
// storage constructors rather than from a new constructor argument, so there is no
// argument to check. What has to hold is a CALL ORDER in this package:
// initializeServerIdentity must run, unconditionally, before the mode dispatch that
// builds the backends. It is the only writer of rt.cfg.ServerID.
//
// Breaking it is compile clean and test clean everywhere else. Move the call below
// dispatchBackupMode, or wrap it in a branch, and every backend reads an empty
// cfg.ServerID: the adoption arm can never fire, so a host that has lost FQDN
// resolution silently stops rotating its own archives on local, secondary and cloud at
// once, which is discussion #292 all over again with no other symptom. The archives
// keep being written correctly the whole time, because the writer takes the identity
// from a different field.
//
// If a legitimate refactor moves the call, point this guard at its new home rather
// than deleting it: the ordering still has to hold, and this is the only thing that
// checks it does.
func TestServerIdentityResolvedBeforeTheStorageBackends(t *testing.T) {
	const (
		identityCall = "initializeServerIdentity"
		dispatchCall = "dispatchBackupMode"
	)

	fn := findFuncDecl(t, "main_lifecycle.go", "runRuntime")

	if !callIsTopLevelStatement(fn, identityCall) {
		t.Fatalf("main_lifecycle.go: runRuntime does not call %s as an unconditional top-level statement: it is missing, or wrapped in a branch, a loop, a defer or a closure, so some runs skip it. Every storage backend then reads an empty cfg.ServerID and stops recognising archives written under a hostname this machine no longer resolves (discussion #292)", identityCall)
	}
	if reason := callIsSkippedBeforeItRuns(fn, identityCall); reason != "" {
		t.Fatalf("main_lifecycle.go: runRuntime can finish a successful run without reaching %s: %s. The storage backends then read an empty cfg.ServerID (discussion #292)", identityCall, reason)
	}

	identityAt := topLevelCallIndex(fn, identityCall)
	dispatchAt := topLevelCallIndex(fn, dispatchCall)
	if dispatchAt < 0 {
		// The dispatch is the last statement of the body, so it is reached through a
		// return rather than as a bare call. Fall back to a whole-body search, which
		// still establishes the ordering because the identity call is a top-level
		// statement and this walk visits the body in source order.
		dispatchAt = statementIndexOfCall(fn, dispatchCall)
	}
	if dispatchAt < 0 {
		t.Fatalf("main_lifecycle.go: runRuntime no longer calls %s; this guard has gone stale and can no longer prove the identity is resolved before the storage backends are built", dispatchCall)
	}
	if identityAt < 0 || identityAt >= dispatchAt {
		t.Fatalf("main_lifecycle.go: runRuntime calls %s at statement %d and %s at statement %d. The identity has to be resolved BEFORE the backends are constructed, or every one of them reads an empty cfg.ServerID and the adoption rule can never fire (discussion #292)", identityCall, identityAt, dispatchCall, dispatchAt)
	}
}

// TestInitializeServerIdentityAssignsTheConfigField pins the other end of the plumb.
// The call order above is worth nothing if the call writes the identity somewhere the
// storage constructors do not read: they are handed cfg and nothing else, so
// rt.cfg.ServerID is the field that has to be assigned. Emptying that one assignment
// while leaving rt.serverIDValue in place keeps the notifications and the operator
// email correct and leaves retention on the hostname rule alone.
func TestInitializeServerIdentityAssignsTheConfigField(t *testing.T) {
	fn := findFuncDecl(t, "main_identity.go", "initializeServerIdentity")

	if !assignsField(fn, "rt.cfg", "ServerID") {
		t.Fatal("main_identity.go: initializeServerIdentity never assigns rt.cfg.ServerID. That field is the only route this host's server identity takes into the storage backends, which are handed cfg and nothing else, so retention falls back to the hostname rule on every location (discussion #292)")
	}
}

// topLevelCallIndex returns the index in fn.Body.List of the first direct,
// unconditional call to callee, or -1. Same statement shapes callIsTopLevelStatement
// accepts, reported by position so two calls can be ordered against each other.
func topLevelCallIndex(fn *ast.FuncDecl, callee string) int {
	for i, stmt := range fn.Body.List {
		if directCallName(stmt) == callee {
			return i
		}
	}
	return -1
}

// statementIndexOfCall returns the index in fn.Body.List of the first top-level
// statement that CONTAINS a call to callee at any depth, or -1. It is the fallback for
// a call that is not a statement of its own, such as one inside a return expression,
// and it is only ever used to establish that something happens after something else.
func statementIndexOfCall(fn *ast.FuncDecl, callee string) int {
	for i, stmt := range fn.Body.List {
		found := false
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && gotypes.ExprString(call.Fun) == callee {
				found = true
				return false
			}
			return true
		})
		if found {
			return i
		}
	}
	return -1
}

// TestOrchestratorIsHandedTheRunsServerIdentity is the second half of the wiring
// guard, and it pins the one hop nothing else can reach.
//
// The identity travels two separate roads out of this package. The first is
// cfg.ServerID, which the storage constructors read for the RETENTION side, and
// TestServerIdentityResolvedBeforeTheStorageBackends above pins its ordering. The
// second is this call, which is the only way the value reaches the WRITER: the
// orchestrator stamps o.serverID into every manifest it produces, through
// InitializeBackupStats and then newArchiveManifest.
//
// Deleting this line is compile clean, and it was test clean too: the whole suite
// stayed green with it removed, because every test below this layer hands the
// orchestrator an identity of its own. What breaks in the field is that every archive
// this host writes from then on records no server identity at all, so the adoption
// rule has nothing to read years later when the machine stops resolving its own name.
// Nothing fails, nothing warns, and the damage is only visible on a host that has
// already lost its FQDN, which is the population the whole feature exists for.
//
// The argument is checked by name, not merely the call: SetIdentity takes two strings
// in a fixed order, so passing serverMACValue twice, or passing "" for the identity,
// is exactly as quiet as deleting the line.
func TestOrchestratorIsHandedTheRunsServerIdentity(t *testing.T) {
	const call = "SetIdentity"

	fn := findFuncDecl(t, "backup_mode.go", "configureBackupOrchestrator")

	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		callExpr, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := callExpr.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != call {
			return true
		}
		found = true
		if len(callExpr.Args) != 2 {
			t.Errorf("backup_mode.go: configureBackupOrchestrator calls orch.%s with %d argument(s), want 2. The identity and the MAC are two strings in a fixed order, so a changed arity means this guard can no longer tell which one carries the identity", call, len(callExpr.Args))
			return false
		}
		if got := gotypes.ExprString(callExpr.Args[0]); got != "opts.serverIDValue" {
			t.Errorf("backup_mode.go: configureBackupOrchestrator passes %q as the server identity to orch.%s, want \"opts.serverIDValue\". Every archive this host writes then records no identity, and the adoption rule has nothing to read when this machine later stops resolving its own name (discussion #292). Nothing fails at the time", got, call)
		}
		return false
	})

	if !found {
		t.Fatalf("backup_mode.go: configureBackupOrchestrator no longer calls orch.%s. That call is the ONLY way the run's server identity reaches the writer, so every archive produced from now on records none, silently. If a refactor moved it, point this guard at its new home rather than deleting it", call)
	}

	// The configure step itself has to run on every backup, or the call above is
	// unreachable and pinning its arguments proves nothing.
	//
	// The chain is two frames deep: runBackupModeSteps calls initializeBackupOrchestrator,
	// which calls configureBackupOrchestrator. Asserting the second link against
	// runBackupModeSteps, as this guard first did, asked about a call that frame has never
	// contained, and callIsSkippedBeforeItRuns reports NO reason for a call it cannot find.
	// The guard was therefore inert from the day it was written, and a deleted call would
	// have passed it just as quietly. Presence is now asserted directly, per link.
	for _, link := range []struct{ caller, callee string }{
		{"runBackupModeSteps", "initializeBackupOrchestrator"},
		{"initializeBackupOrchestrator", "configureBackupOrchestrator"},
	} {
		caller := findFuncDecl(t, "backup_mode.go", link.caller)
		if !callIsTopLevelStatement(caller, link.callee) {
			t.Fatalf("backup_mode.go: %s no longer calls %s as an unconditional top-level statement. The orchestrator then writes archives with no server identity", link.caller, link.callee)
		}
		if reason := callIsSkippedBeforeItRuns(caller, link.callee); reason != "" {
			t.Fatalf("backup_mode.go: %s can return before it calls %s: %s. The orchestrator then writes archives with no server identity", link.caller, link.callee, reason)
		}
	}
}
