package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"testing"
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
// If a legitimate refactor moves one of these calls, update the row to name the new
// file, function or argument position. Do not delete a row: the argument still has
// to arrive, and this is the only thing that checks it does.
const (
	retentionConsequence     = "retention then reads os.Hostname() only, so every archive written under the FQDN scopes out as foreign and nothing is ever pruned"
	accessControlConsequence = "the access control host check then reads os.Hostname() only, so every same-host restore of an access control bundle warns about a hostname change that did not happen"
)

func TestRunHostnameWiredIntoPinnedCallSites(t *testing.T) {
	sites := []runHostnameCallSite{
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
		})
	}
}

// TestBackupModeOptionsCarryTheRunHostname pins the ORIGIN of the plumb, which the
// call-site guard above cannot see: opts.hostname only carries the run's name
// because dispatchBackupMode copies rt.hostname into the literal.
//
// Deleting that one key is compile-clean AND invisible in the archives, because
// runConfiguredBackup falls back to resolveHostname() when opts.hostname is empty
// (backup_execution.go). The names on disk would stay correct while retention
// silently lost its alias, which is discussion #292 all over again.
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
