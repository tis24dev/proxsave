package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
//
// The search is package-wide and TRANSITIVE, and both matter. It used to parse
// guards_cleanup.go alone and look for a literal call to cleanupMountGuards, which left the
// easiest way to reintroduce the defect undetected twice over: a wrapper in any other file
// of this package was invisible, and a wrapper that delegates to the EXPORTED entry point
// -- `func X(...) error { r, err := CleanupMountGuardsReport(...); return err }`, the most
// natural way anyone would write it back -- calls cleanupMountGuards nowhere at all. That is
// the exact wrapper this test exists to forbid.
func TestGuardCleanupHasOneExportedEntryPoint(t *testing.T) {
	const engine = "cleanupMountGuards"

	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	// callees maps a declared name to the package-local names its body calls; decls keeps
	// the declaration so an entry point's signature can be checked. Methods are included:
	// an exported method that reaches the engine is a second way in just as much as a
	// function is.
	callees := map[string]map[string]bool{}
	decls := map[string]*ast.FuncDecl{}
	parsed := 0

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := declKey(fn)
			decls[key] = fn
			called := map[string]bool{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident: // packageLocalFunc(...)
					called[fun.Name] = true
				case *ast.SelectorExpr: // o.method(...) / pkg.Func(...) -- keep the selector
					called[fun.Sel.Name] = true
				}
				return true
			})
			callees[key] = called
		}
	}

	if parsed == 0 {
		t.Fatal("parsed no production files; the matcher has gone stale")
	}
	if _, ok := decls[engine]; !ok {
		t.Fatalf("%s not found in this package; the matcher has gone stale", engine)
	}

	var exported []string
	for key, fn := range decls {
		if !fn.Name.IsExported() || key == engine {
			continue
		}
		if !reachesEngine(key, engine, callees, map[string]bool{}) {
			continue
		}
		exported = append(exported, key)
		assertReportAndError(t, key, fn)
	}

	if len(exported) != 1 {
		sort := append([]string(nil), exported...)
		strSort(sort)
		t.Fatalf("guard cleanup must have exactly ONE exported entry point, found %d: %s",
			len(exported), strings.Join(sort, ", "))
	}
	if exported[0] != "CleanupMountGuardsReport" {
		t.Fatalf("the entry point is %s, want CleanupMountGuardsReport", exported[0])
	}
}

// declKey names a declaration: "Recv.Method" for a method, the bare name for a function.
func declKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return recvTypeName(fn.Recv.List[0].Type) + "." + fn.Name.Name
}

func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// reachesEngine reports whether key's body reaches engine, directly or through any chain of
// package-local calls. seen breaks recursion.
func reachesEngine(key, engine string, callees map[string]map[string]bool, seen map[string]bool) bool {
	if seen[key] {
		return false
	}
	seen[key] = true
	for callee := range callees[key] {
		if callee == engine {
			return true
		}
		// Selector calls are recorded by their final identifier, so a method chain is
		// followed by name; an unrelated name simply has no entry here.
		for candidate := range callees {
			if candidate == callee || strings.HasSuffix(candidate, "."+callee) {
				if reachesEngine(candidate, engine, callees, seen) {
					return true
				}
			}
		}
	}
	return false
}

// assertReportAndError checks the entry point hands back the report AND an error. Counting
// result FIELDS is not the same as counting results -- `(a, b T)` is one field with two
// names -- and checking only the first type would accept (GuardCleanupReport, string).
func assertReportAndError(t *testing.T, name string, fn *ast.FuncDecl) {
	t.Helper()
	want := "must return (GuardCleanupReport, error); a report-less entry point is what let " +
		"--cleanup-guards exit 0 with guards still in place"

	if fn.Type.Results == nil {
		t.Fatalf("%s returns nothing and %s", name, want)
	}
	var types []ast.Expr
	for _, field := range fn.Type.Results.List {
		n := len(field.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			types = append(types, field.Type)
		}
	}
	if len(types) != 2 {
		t.Fatalf("%s returns %d values and %s", name, len(types), want)
	}
	if id, ok := types[0].(*ast.Ident); !ok || id.Name != "GuardCleanupReport" {
		t.Fatalf("%s returns %v first, want GuardCleanupReport", name, types[0])
	}
	if id, ok := types[1].(*ast.Ident); !ok || id.Name != "error" {
		t.Fatalf("%s returns %v second, want error", name, types[1])
	}
}

// strSort is an insertion sort, kept local so the failure message is stable without pulling
// sort into a file that otherwise only walks an AST.
func strSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
