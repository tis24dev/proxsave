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

// applyArm gates cancellation BETWEEN arms. Without a check inside the helpers the
// window in which a cancelled restore can still write is a whole arm: read the
// staged file, trim it, and only then write. These three drive each helper with an
// already-cancelled context and assert that the irreversible call did not happen.
//
// This narrows the window, it does not close it: cancellation can still land
// between the check and the write, and there is no atomic "write unless cancelled"
// available. The point is that "cancelled before the arm even started reading"
// must never reach a write, which is the case an aborted restore actually hits.
func TestCancelledContextSkipsEachPVEStagedWrite(t *testing.T) {
	t.Run("vzdump.conf write", func(t *testing.T) {
		fs := stagedCancelFS(t)
		if err := fs.AddFile("/stage/etc/vzdump.conf", []byte("tmpdir: /var/tmp\n")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := applyPVEVzdumpConfFromStage(ctx, logging.New(types.LogLevelError, false), "/stage")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, err := fs.ReadFile("/etc/vzdump.conf"); err == nil {
			t.Fatal("a cancelled restore wrote /etc/vzdump.conf anyway")
		}
	})

	// The empty-staged-file branch REMOVES the destination, which is irreversible in
	// the same way a write is and reaches a different call.
	t.Run("vzdump.conf removal", func(t *testing.T) {
		fs := stagedCancelFS(t)
		if err := fs.AddFile("/stage/etc/vzdump.conf", []byte("  \n")); err != nil {
			t.Fatal(err)
		}
		if err := fs.AddFile("/etc/vzdump.conf", []byte("tmpdir: /var/tmp\n")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := applyPVEVzdumpConfFromStage(ctx, logging.New(types.LogLevelError, false), "/stage")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, err := fs.ReadFile("/etc/vzdump.conf"); err != nil {
			t.Fatalf("a cancelled restore removed /etc/vzdump.conf anyway: %v", err)
		}
	})

	t.Run("datacenter.cfg pmxcfs write", func(t *testing.T) {
		fs := stagedCancelFS(t)
		seamPmxcfs(t, "/etc/pve", true, nil)
		if err := fs.AddFile("/stage/etc/pve/datacenter.cfg", []byte("keyboard: it\n")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := applyPVEDatacenterCfgFromStage(ctx, logging.New(types.LogLevelError, false), "/stage")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, err := fs.ReadFile("/etc/pve/datacenter.cfg"); err == nil {
			t.Fatal("a cancelled restore wrote datacenter.cfg into pmxcfs anyway")
		}
	})

	t.Run("vzdump.cron pmxcfs write", func(t *testing.T) {
		fs := stagedCancelFS(t)
		seamPmxcfs(t, "/etc/pve", true, nil)
		if err := fs.AddFile("/stage/etc/pve/vzdump.cron", []byte("0 2 * * 6 root vzdump 101\n")); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := applyPVEVzdumpCronFromStage(ctx, logging.New(types.LogLevelError, false), "/stage")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if _, err := fs.ReadFile("/etc/pve/vzdump.cron"); err == nil {
			t.Fatal("a cancelled restore wrote vzdump.cron into pmxcfs anyway")
		}
	})
}

func stagedCancelFS(t *testing.T) *FakeFS {
	t.Helper()
	orig := restoreFS
	fs := NewFakeFS()
	restoreFS = fs
	t.Cleanup(func() {
		restoreFS = orig
		_ = os.RemoveAll(fs.Root)
	})
	return fs
}

// Giving the helpers a ctx created a new way for an arm to fail: it can now return
// the caller's cancellation. Recorded as an ordinary failed item, the LAST arm's
// cancellation would surface as "1 PVE config item(s) failed to apply", because no
// later gate runs to set aborted. An operator abort would be reported back as a
// staged-apply failure, and input.IsAborted would not match it.
//
// This is a SHAPE test, and what it proves is limited: the arms sit behind
// isRealRestoreFS and a root check, so a behavioural test would need two
// production seams added for one assertion. What it pins is the ordering inside
// applyArm's error branch: ctx.Err() must be consulted and aborted set BEFORE
// anything is appended to failedItems. It does not prove the runtime path; the
// four subtests above prove the helpers return the cancellation that this branch
// then has to classify.
func TestApplyArmClassifiesACancellationAsAnAbort(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pve_staged_apply.go", nil, 0)
	if err != nil {
		t.Fatalf("parse pve_staged_apply.go: %v", err)
	}

	var errBranch *ast.BlockStmt
	ast.Inspect(file, func(n ast.Node) bool {
		stmt, ok := n.(*ast.IfStmt)
		if !ok || stmt.Init == nil {
			return true
		}
		assign, ok := stmt.Init.(*ast.AssignStmt)
		if !ok || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "run" && errBranch == nil {
			errBranch = stmt.Body
		}
		return true
	})
	if errBranch == nil {
		t.Fatal("the `if err := run(); err != nil` branch is gone from applyArm; this test needs rewriting")
	}

	ctxErr, setsAborted, appendsItem := -1, -1, -1
	ast.Inspect(errBranch, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Err" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == "ctx" && ctxErr < 0 {
					ctxErr = int(v.Pos())
				}
			}
		case *ast.AssignStmt:
			for _, lhs := range v.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if id.Name == "aborted" && setsAborted < 0 {
					setsAborted = int(v.Pos())
				}
				if id.Name == "failedItems" && appendsItem < 0 {
					appendsItem = int(v.Pos())
				}
			}
		}
		return true
	})

	if ctxErr < 0 {
		t.Fatal("applyArm's error branch never consults ctx.Err(): a cancelled arm is recorded as a failed item")
	}
	if setsAborted < 0 {
		t.Fatal("applyArm's error branch never sets aborted: the last arm's cancellation would be reported as an apply failure")
	}
	if appendsItem < 0 {
		t.Fatal("applyArm's error branch no longer records failed items; this test needs rewriting")
	}
	if ctxErr >= appendsItem || setsAborted >= appendsItem {
		t.Fatalf("the cancellation check must precede the failedItems append (ctxErr@%d aborted@%d append@%d)", ctxErr, setsAborted, appendsItem)
	}
}
