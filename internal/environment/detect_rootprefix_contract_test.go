package environment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootPrefix is a package-level variable and there is no lock on it. What keeps that safe
// is that exactly two functions set it, both from the sequential process bootstrap, so the
// two calls never overlap. Nothing in the type system says so, and the guarantee was already
// broken once on paper: MarkerSnapshot became the second setter while the variable's comment
// still named only DetectWith.
//
// This test reads the package's own source and fails when a third function assigns
// rootPrefix. It is a prompt, not a prohibition: adding one is fine, as long as whoever adds
// it revisits the sequential-bootstrap argument and updates the comment. Passing a prefix
// explicitly through the detection helpers, instead of setting a global, is the other way
// out.
func TestRootPrefixHasExactlyTwoSetters(t *testing.T) {
	want := map[string]bool{"DetectWith": true, "MarkerSnapshot": true}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package directory: %v", err)
	}

	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		var enclosing string
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				enclosing = node.Name.Name
			case *ast.AssignStmt:
				for _, lhs := range node.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "rootPrefix" {
						found[enclosing] = append(found[enclosing], name)
					}
				}
			}
			return true
		})
	}

	for fn := range found {
		if !want[fn] {
			t.Fatalf("%s assigns rootPrefix. Exactly two functions may, %v, because nothing locks it "+
				"and the safety argument is that both run from the sequential bootstrap. Re-read that "+
				"argument for the new call site, then update the comment on rootPrefix and this test.",
				fn, keysOf(want))
		}
	}
	for fn := range want {
		if _, ok := found[fn]; !ok {
			t.Fatalf("%s no longer assigns rootPrefix; if the global is gone, drop this test with it", fn)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
