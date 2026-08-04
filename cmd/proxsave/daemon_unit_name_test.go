package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDaemonUnitNameHasOneSpelling is the structural pin for D6. The systemd unit name is a
// deployment fact the command owns once, in daemonUnitName -- but seven production sites wrote
// it out by hand, and the split ran along the front-end boundary: the CLI took "Daemon service
// (%s)" from the constant while the dashboard hardcoded the same line, and the CLI's
// "Daemon mode enabled: %s is active..." existed twice with the constant and once without.
//
// The damage from that is not cosmetic. Renaming or versioning the unit would move the
// systemctl calls (which all use the constant) while leaving the on-screen text naming a unit
// that no longer exists, and it would do so on ONE front-end -- the operator would be told two
// different things depending on which one they opened.
//
// A grep-based check would pass on a comment or a doc string, so this walks the AST and looks
// only at real string literals. Test files are excluded on purpose: a test asserting the
// rendered line SHOULD name the string the operator sees, which is the whole point of pinning
// it here.
func TestDaemonUnitNameHasOneSpelling(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var declPos token.Pos
	var offenders []string

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}

		// Remember where the constant declares the name, so it is not its own offender.
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, ident := range value.Names {
					if ident.Name == "daemonUnitName" && i < len(value.Values) {
						declPos = value.Values[i].Pos()
					}
				}
			}
		}

		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.Contains(text, "proxsave-daemon.service") {
				return true
			}
			if lit.Pos() == declPos {
				return true
			}
			offenders = append(offenders, fset.Position(lit.Pos()).String())
			return true
		})
	}

	if declPos == token.NoPos {
		t.Fatal("daemonUnitName is not declared as a constant any more; this test can no longer tell the declaration from a stray literal")
	}
	if len(offenders) > 0 {
		t.Fatalf("the unit name must come from daemonUnitName, not a literal; found %d:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestDaemonUnitPathDerivesFromTheName pins the unit PATH to the same source. The path used to
// spell the unit name a second time, so a rename would have written the new unit to the old
// file name -- systemctl would then enable a unit the writer never created.
func TestDaemonUnitPathDerivesFromTheName(t *testing.T) {
	if got, want := filepath.Base(daemonUnitPath), daemonUnitName; got != want {
		t.Fatalf("daemonUnitPath names %q, want the unit %q", got, want)
	}
	if dir := filepath.Dir(daemonUnitPath); dir != daemonUnitDir {
		t.Fatalf("daemonUnitPath lives in %q, want %q", dir, daemonUnitDir)
	}
}
