package config

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// loaderReadNames returns every literal variable name config.go passes to a getter,
// with the line it appears on. Runtime-built names (the WEBHOOK_<name>_<field> prefixes
// BuildWebhookConfig assembles) are not literals and are not seen here; the audit has
// its own rule for those.
func loaderReadNames(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config.go", nil, 0)
	if err != nil {
		t.Fatalf("parse config.go: %v", err)
	}
	names := map[string]string{}
	record := func(expr ast.Expr) {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil || !isEnvVariableName(value) || value != strings.ToUpper(value) {
			return
		}
		names[value] = fmt.Sprintf("config.go:%d", fset.Position(lit.Pos()).Line)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "get") || len(call.Args) == 0 {
			return true
		}
		if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
			for _, elt := range lit.Elts {
				record(elt)
			}
			return true
		}
		record(call.Args[0])
		return true
	})
	if len(names) == 0 {
		t.Fatal("no getter call sites found in config.go: the AST walk stopped matching")
	}
	return names
}

// The audit's whole premise is that "unknown" means the loader does not read it, so any
// name it reads and the audit rejects is a line telling the operator to delete working
// configuration: a variable the loader reads and the template never mentions.
//
// The remedy for a name found here is to say so in the template - a commented example
// is enough, and it is what the audit already accepts - NOT to make it an active
// assignment, which would make it Absent in every existing file and turn a clean run
// into a WARNING for every operator who never set it.
func TestEveryNameTheLoaderReadsIsAcceptedByTheAudit(t *testing.T) {
	template := DefaultEnvTemplate()
	templateScan, err := scanEnvAssignments(bufio.NewScanner(strings.NewReader(template)))
	if err != nil {
		t.Fatalf("scan the embedded template: %v", err)
	}
	documented := scanDocumentedVariables(bufio.NewScanner(strings.NewReader(template)))

	rejected := map[string]string{}
	for name, site := range loaderReadNames(t) {
		if _, known := templateKnows(name, templateScan.assignedAt, documented); known {
			continue
		}
		rejected[name] = site
	}
	if len(rejected) == 0 {
		return
	}
	names := make([]string, 0, len(rejected))
	for name := range rejected {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, fmt.Sprintf("  %s (%s)", name, rejected[name]))
	}
	t.Fatalf("%d name(s) the loader reads are rejected by the audit, which reports each as %q:\n%s",
		len(names), "not a known variable and is ignored", strings.Join(lines, "\n"))
}

// The variable names of the Bash version are gone from the loader for good: none of them
// is read from the file or from the environment, and none is in the template, so the
// only code that still knows them is the rename in legacyVariables.
func TestTheLoaderReadsNoLegacyName(t *testing.T) {
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	template := DefaultEnvTemplate()
	read := loaderReadNames(t)
	for _, legacy := range legacyVariables {
		if strings.Contains(string(source), strconv.Quote(legacy.Name)) {
			t.Errorf("config.go still names %s", legacy.Name)
		}
		if site, ok := read[legacy.Name]; ok {
			t.Errorf("the loader still reads %s (%s)", legacy.Name, site)
		}
		if strings.Contains(template, legacy.Name+"=") {
			t.Errorf("the template still assigns %s", legacy.Name)
		}
		if !strings.Contains(template, legacy.Current+"=") {
			t.Errorf("%s is renamed to %s, which the template does not assign", legacy.Name, legacy.Current)
		}
	}
}
