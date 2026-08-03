package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestGuardCleanupHasOneExportedEntryPoint pins the shape that fixed the --cleanup-guards
// exit-code defect, rather than the defect's symptom.
//
// cleanupMountGuards used to have two exported wrappers: one returning the report, one
// returning only an error. --cleanup-guards took the error-only one, so a run that removed
// nothing because the datastore was still mounted returned nil and the mode exited 0 --
// telling a gating script the storage was unlocked while guards were still holding it. The
// state that would have said otherwise was computed and then discarded at the call site.
//
// Deleting that wrapper is what makes the mistake unavailable, and only a structural test
// keeps it deleted: re-adding a convenience wrapper is an easy, well-meant change, and no
// behavioural test fails when an unused one appears. This asserts the engine offers exactly
// one exported way in and that it hands the caller the report.
func TestGuardCleanupHasOneExportedEntryPoint(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "guards_cleanup.go", nil, 0)
	if err != nil {
		t.Fatalf("parse guards_cleanup.go: %v", err)
	}

	var exported []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Body == nil {
			continue
		}
		calls := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "cleanupMountGuards" {
				calls = true
			}
			return true
		})
		if !calls {
			continue
		}
		exported = append(exported, fn.Name.Name)

		// The one entry point must return the report, not just an error.
		if fn.Type.Results == nil || len(fn.Type.Results.List) != 2 {
			t.Fatalf("%s must return (GuardCleanupReport, error); a report-less entry point is what let "+
				"--cleanup-guards exit 0 with guards still in place", fn.Name.Name)
		}
		first, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
		if !ok || first.Name != "GuardCleanupReport" {
			t.Fatalf("%s returns %v first, want GuardCleanupReport", fn.Name.Name, fn.Type.Results.List[0].Type)
		}
	}

	if len(exported) != 1 {
		t.Fatalf("guard cleanup must have exactly ONE exported entry point, found %d: %s",
			len(exported), strings.Join(exported, ", "))
	}
	if exported[0] != "CleanupMountGuardsReport" {
		t.Fatalf("the entry point is %s, want CleanupMountGuardsReport", exported[0])
	}
}
