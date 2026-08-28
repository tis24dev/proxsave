package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	gotypes "go/types"
	"testing"
)

// TestRunHostnameWiredThroughTheRestoreChain pins every hop the run's own hostname
// takes from an exported entry point down to the workflow that reads it.
//
// The name arrives from package main and is the second name the access control host
// check answers to, next to os.Hostname(). Every hop below is a compile-clean drop
// point: substituting "" type-checks, and the rule then collapses to strict equality
// against os.Hostname() alone. On a default Proxmox box that is the short name while
// the archive carries the FQDN, so EVERY same-host access control restore would warn
// again, which is the false positive from discussion #292 on the restore side.
//
// Two of these hops cannot be reached behaviourally at all: RunRestoreWorkflow builds
// a CLI UI over os.Stdin and RunRestoreWorkflowTUI builds a real terminal session, so
// neither is drivable from a unit test. That is why this guard is structural.
//
// The third hop is ALSO covered behaviourally, by
// TestRunRestoreWorkflowWithUICarriesTheRunHostnameToTheAccessControlCheck. The
// redundancy is deliberate: this guard is precise but brittle to legitimate
// refactoring, the behavioural test survives refactoring but reaches only one hop.
//
// If a refactor genuinely restructures the chain, retarget the row rather than
// deleting it. The argument still has to arrive, and for hops 1 and 2 this is the
// only thing that checks it does.
func TestRunHostnameWiredThroughTheRestoreChain(t *testing.T) {
	hops := []struct {
		file      string
		enclosing string
		callee    string
		argIndex  int
		wantArg   string
	}{
		{file: "restore.go", enclosing: "RunRestoreWorkflow", callee: "runRestoreWorkflowWithUI", argIndex: 5, wantArg: "runHostname"},
		{file: "restore_tui.go", enclosing: "RunRestoreWorkflowTUI", callee: "runRestoreWorkflowWithUI", argIndex: 5, wantArg: "runHostname"},
		{file: "restore_workflow_ui.go", enclosing: "runRestoreWorkflowWithUI", callee: "newRestoreUIWorkflowRun", argIndex: 5, wantArg: "runHostname"},
	}

	for _, hop := range hops {
		t.Run(hop.enclosing+" passes "+hop.wantArg+" to "+hop.callee, func(t *testing.T) {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, hop.file, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", hop.file, err)
			}

			var fn *ast.FuncDecl
			for _, decl := range f.Decls {
				d, ok := decl.(*ast.FuncDecl)
				if ok && d.Recv == nil && d.Name.Name == hop.enclosing && d.Body != nil {
					fn = d
					break
				}
			}
			if fn == nil {
				t.Fatalf("%s: %s not found; this guard has gone stale", hop.file, hop.enclosing)
			}

			// Collect inside the walk, assert after it: t.Fatalf from an ast.Inspect
			// closure would abandon the walk mid-tree.
			found := false
			gotArg := ""
			argCount := 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || gotypes.ExprString(call.Fun) != hop.callee {
					return true
				}
				found = true
				argCount = len(call.Args)
				if hop.argIndex < len(call.Args) {
					gotArg = gotypes.ExprString(call.Args[hop.argIndex])
				}
				return true
			})

			if !found {
				t.Fatalf("%s: %s no longer calls %s; the run hostname has to reach it (discussion #292). If the chain was restructured, point this row at its new home rather than deleting it",
					hop.file, hop.enclosing, hop.callee)
			}
			if hop.argIndex >= argCount {
				t.Fatalf("%s: %s calls %s with %d argument(s), so there is no argument %d to carry the run hostname (discussion #292)",
					hop.file, hop.enclosing, hop.callee, argCount, hop.argIndex)
			}
			if gotArg != hop.wantArg {
				t.Fatalf("%s: %s calls %s with arg %d = %q, want %q; dropping it collapses the access control host check to os.Hostname() only and reinstates the false positive from discussion #292",
					hop.file, hop.enclosing, hop.callee, hop.argIndex, gotArg, hop.wantArg)
			}
		})
	}
}
