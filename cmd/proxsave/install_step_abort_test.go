package main

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/installer"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// TestOptionalInstallStepAbortsKnowsBothDiscriminators is the matrix both drivers now
// share. Each front-end used to know only one column of it: the CLI tested ctx.Err() and
// was blind to a Charm session death, the TUI tested shell.ErrClosed and was blind to a
// cancelled run context. The two are genuinely different errors -- the shell resolves a
// raw-mode Ctrl+C to ErrClosed, not to context.Canceled -- so neither test subsumes the
// other and a step guarded by only one of them has a hole.
func TestOptionalInstallStepAbortsKnowsBothDiscriminators(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	live := context.Background()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"no error is never an abort", live, nil, false},
		{"no error is not an abort even on a dead context", cancelled, nil, false},
		{"a benign EOF at a prompt continues", live, io.EOF, false},
		{"a user cancel continues", live, installer.ErrInstallCancelled, false},
		{"an unreachable relay continues", live, errors.New("dial tcp: connection refused"), false},
		{"a signal aborts", cancelled, io.EOF, true},
		{"a session death aborts", live, shell.ErrClosed, true},
		{"a wrapped session death aborts", live, fmt.Errorf("telegram: %w", shell.ErrClosed), true},
		{"a nil context is tolerated", nil, io.EOF, false},
		{"a nil context still sees a session death", nil, shell.ErrClosed, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := optionalInstallStepAborts(tc.ctx, tc.err); got != tc.want {
				t.Fatalf("optionalInstallStepAborts = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAbortInstallOnOptionalStepReturnsTheCanonicalError checks the TUI-side action: the
// abort must be the SAME error the mandatory steps raise, because that is what the
// deferred footer keys off to print "Installation aborted" instead of "completed".
func TestAbortInstallOnOptionalStepReturnsTheCanonicalError(t *testing.T) {
	bootstrap, buf := captureBootstrapLog(t)
	bootstrap.SetConsoleQuiet(true)

	if err := abortInstallOnOptionalStep(context.Background(), bootstrap, "Telegram setup", io.EOF); err != nil {
		t.Fatalf("a benign error must not abort, got %v", err)
	}
	if buf.String() != "" {
		t.Fatalf("the benign case must stay silent (the step logs its own outcome), got %q", buf.String())
	}

	err := abortInstallOnOptionalStep(context.Background(), bootstrap, "Telegram setup", shell.ErrClosed)
	if err == nil || !errors.Is(err, errInteractiveAborted) {
		t.Fatalf("want errInteractiveAborted, got %v", err)
	}
	if !isInstallAbortedError(err) {
		t.Fatalf("the footer's own predicate must recognise it: %v", err)
	}
	if got := buf.String(); !strings.Contains(got, "Telegram setup aborted") ||
		!strings.Contains(got, "stopping the install before finalization") {
		t.Fatalf("abort not recorded in the install log: %q", got)
	}
}

// TestEveryTUIInstallStepIsGuarded is the structural pin. The original defect was not a
// wrong rule, it was three steps that had no rule at all: RunPostInstallAudit,
// RunTelegramSetup and RunHealthcheckSetup captured an error, logged it as
// "non-blocking", and let the driver walk on to a finalization that then ran on a dead
// context and installed no scheduler -- while the run still ended on the "Installation
// completed" banner.
//
// A test of the rule cannot catch that; only a test of the WIRING can. This walks
// runInstallTUI, collects the error identifier of every flow call it makes, and requires
// each one to reach an abort decision. A newly added step that forgets the guard fails
// here by name.
func TestEveryTUIInstallStepIsGuarded(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "install_tui.go", nil, 0)
	if err != nil {
		t.Fatalf("parse install_tui.go: %v", err)
	}

	var driver *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "runInstallTUI" {
			driver = fn
		}
	}
	if driver == nil {
		t.Fatal("runInstallTUI not found; this test can no longer see the driver it pins")
	}

	// Every flowinstall step error, and every identifier reaching a decision, kept as
	// (name, position) OCCURRENCES rather than as a set of names.
	//
	// Names alone are not identifiers here. runInstallTUI reuses `err` for several
	// flowinstall calls, so a single map keyed by name collapsed them into one entry --
	// the count was wrong, only the last position survived, and one guarded `err`
	// anywhere in the function marked every other `err` guarded too. A step whose error
	// never reached a decision passed unnoticed, which is the whole thing this test
	// exists to catch.
	//
	// An occurrence is guarded when a decision mentions that name after the assignment
	// and before the NEXT assignment to the same name: the window in which the value
	// still belongs to this step. That is positional, so a guard sitting in a different
	// branch of the same window still counts -- an approximation, but a strictly tighter
	// one than treating the whole function as a single scope.
	type occurrence struct {
		name string
		pos  token.Pos
	}
	var stepErrors, guards []occurrence

	ast.Inspect(driver, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			// <x>, <errIdent> := flowinstall.Something(...)
			if len(n.Rhs) != 1 || len(n.Lhs) != 2 {
				return true
			}
			call, ok := n.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "flowinstall" {
				return true
			}
			if ident, ok := n.Lhs[1].(*ast.Ident); ok && ident.Name != "_" {
				stepErrors = append(stepErrors, occurrence{name: ident.Name, pos: n.Pos()})
			}
		case *ast.CallExpr:
			// abortInstallOnOptionalStep(ctx, bootstrap, "step", <errIdent>) or mapUIDeath(<errIdent>)
			name := ""
			switch fn := n.Fun.(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				name = fn.Sel.Name
			}
			if name != "abortInstallOnOptionalStep" && name != "mapUIDeath" {
				return true
			}
			for _, arg := range n.Args {
				if ident, ok := arg.(*ast.Ident); ok {
					guards = append(guards, occurrence{name: ident.Name, pos: n.Pos()})
				}
			}
		}
		return true
	})

	if len(stepErrors) == 0 {
		t.Fatal("no flowinstall step calls found in runInstallTUI; the matcher has gone stale")
	}
	var unguarded []string
	for _, step := range stepErrors {
		// The window this value owns: from its assignment to the next assignment that
		// rebinds the same name, or to the end of the driver.
		windowEnd, bounded := token.Pos(0), false
		for _, other := range stepErrors {
			if other.name != step.name || other.pos <= step.pos {
				continue
			}
			if !bounded || other.pos < windowEnd {
				windowEnd, bounded = other.pos, true
			}
		}
		decided := false
		for _, g := range guards {
			if g.name != step.name || g.pos <= step.pos {
				continue
			}
			if bounded && g.pos >= windowEnd {
				continue
			}
			decided = true
			break
		}
		if !decided {
			unguarded = append(unguarded, fmt.Sprintf("%s (%s)", step.name, fset.Position(step.pos)))
		}
	}
	if len(unguarded) > 0 {
		t.Fatalf("every install step's error must reach an abort decision; %d do not:\n  %s",
			len(unguarded), strings.Join(unguarded, "\n  "))
	}
}
