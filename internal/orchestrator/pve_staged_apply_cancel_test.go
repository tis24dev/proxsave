package orchestrator

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// runStagedApplySteps re-checks ctx BETWEEN staged-apply steps, but the PVE step is
// one step and its arms write straight into pmxcfs. Without a gate inside it, an
// aborted restore still applied datacenter.cfg, vzdump.cron and the storage
// definitions cluster-wide - the exact harm runStagedApplySteps' own comment
// describes for the steps after it. The abort must surface as context.Canceled, not
// as the failedItems aggregate: input.IsAborted matches context.Canceled, so the
// caller ends the run as aborted instead of "completed with warnings".
func TestMaybeApplyPVEConfigsFromStageRefusesACancelledContext(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreCmd = &FakeCommandRunner{}
	seamPmxcfs(t, "/etc/pve", true, nil)

	if err := fakeFS.AddFile("/stage/etc/pve/datacenter.cfg", []byte("keyboard: it\n")); err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.AddFile("/stage/etc/pve/vzdump.cron", []byte("0 2 * * 6 root vzdump 101\n")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	logger := logging.New(types.LogLevelDebug, false)

	err := maybeApplyPVEConfigsFromStage(ctx, logger, pvePlan(false, "storage_pve", "pve_jobs"), "/stage", "/", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled restore must return context.Canceled, got %v", err)
	}
	for _, written := range []string{"/etc/pve/datacenter.cfg", "/etc/pve/vzdump.cron"} {
		if _, readErr := fakeFS.ReadFile(written); readErr == nil {
			t.Fatalf("%s was applied after the operator aborted", written)
		}
	}
}

// The behavioural test above can only reach the FIRST gate: every arm sits behind a
// root check this suite cannot pass. What keeps the remaining arms covered is the
// shape - each one is invoked through applyArm, which owns the ctx re-check - and
// only a structural assertion keeps it that way. Adding an eighth arm as a bare
// `if err := applyX(...); err != nil` is the natural way to write it and no
// behavioural test in this package would fail.
func TestEveryPVEStagedApplyArmGoesThroughApplyArm(t *testing.T) {
	const owner = "maybeApplyPVEConfigsFromStage"
	const gate = "applyArm"

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pve_staged_apply.go", nil, 0)
	if err != nil {
		t.Fatalf("parse pve_staged_apply.go: %v", err)
	}

	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == owner && fn.Body != nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s not found in pve_staged_apply.go", owner)
	}

	// Every argument subtree of an applyArm call is gated; anything outside them is not.
	type span struct{ from, to token.Pos }
	var gated []span
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == gate {
			gated = append(gated, span{call.Lparen, call.Rparen})
		}
		return true
	})
	if len(gated) == 0 {
		t.Fatalf("%s no longer routes any arm through %s", owner, gate)
	}

	inGate := func(pos token.Pos) bool {
		for _, s := range gated {
			if pos > s.from && pos < s.to {
				return true
			}
		}
		return false
	}

	var escaped []string
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name == owner || !strings.HasSuffix(ident.Name, "FromStage") {
			return true
		}
		if !inGate(ident.Pos()) {
			escaped = append(escaped, ident.Name+" at "+fset.Position(ident.Pos()).String())
		}
		return true
	})
	if len(escaped) > 0 {
		t.Fatalf("these arms bypass the %s cancellation gate: %s", gate, strings.Join(escaped, ", "))
	}
}
