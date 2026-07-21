package main

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/types"
)

// stubWhatsnewShouldWarn saves and restores the whatsnewShouldWarn seam so a test can
// drive the gate verdict without real disk or a GitHub fetch, and leak nothing.
func stubWhatsnewShouldWarn(t *testing.T, fn func(baseDir, current string) (bool, string, error)) {
	t.Helper()
	orig := whatsnewShouldWarn
	t.Cleanup(func() { whatsnewShouldWarn = orig })
	whatsnewShouldWarn = fn
}

// captureLogger builds a Debug-level logger writing into a fresh buffer, so a test can
// assert both WarningCount and the emitted line text.
func captureLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)
	return logger, buf
}

// singleEmittedWarning asserts the logger captured EXACTLY ONE warning/error issue line and
// returns its message BYTE-EXACT. IssueLines uses the fixed "[<ts>] %-8s %s" capture format
// (logger.go): after the "WARNING" token comes the one-space %-8s pad plus the one-space field
// separator, i.e. exactly two spaces, then the verbatim message. Stripping exactly that
// two-space separator (never TrimSpace) means ANY addition to the copy -- including a leading
// or trailing whitespace edit -- changes the returned message and fails the equality check.
func singleEmittedWarning(t *testing.T, logger *logging.Logger) string {
	t.Helper()
	lines := logger.IssueLines()
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 captured issue line, got %d: %q", len(lines), lines)
	}
	i := strings.Index(lines[0], "WARNING")
	if i < 0 {
		t.Fatalf("captured issue is not a WARNING: %q", lines[0])
	}
	rest := lines[0][i+len("WARNING"):]
	if !strings.HasPrefix(rest, "  ") {
		t.Fatalf("unexpected issue-line separator, want two spaces after WARNING: %q", lines[0])
	}
	return strings.TrimPrefix(rest, "  ")
}

const lockedWarnCopy = "ProxSave 0.30.0 has unseen release notes. Open proxsave to view the new features."

// TestMaybeWarnWhatsnewUnseen: an unseen verdict emits EXACTLY ONE WARNING (the locked
// copy) and the buffer carries it, bracketed by DEBUG lines.
func TestMaybeWarnWhatsnewUnseen(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 1 {
		t.Fatalf("WarningCount = %d, want 1", got)
	}
	if got := singleEmittedWarning(t, logger); got != lockedWarnCopy {
		t.Fatalf("emitted warning = %q, want exactly the locked copy %q", got, lockedWarnCopy)
	}
	if !strings.Contains(buf.String(), "Release notes check done") {
		t.Fatalf("missing DEBUG close bracket in buffer\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewSeen: a seen verdict emits no WARNING and a bare-fact DEBUG line.
func TestMaybeWarnWhatsnewSeen(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return false, "", nil
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 0 {
		t.Fatalf("WarningCount = %d, want 0", got)
	}
	if strings.Contains(buf.String(), lockedWarnCopy) {
		t.Fatalf("seen verdict must not emit the WARNING copy\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "already seen") {
		t.Fatalf("seen verdict missing the bare-fact DEBUG line\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewGateError: a gate error fails toward silence (no WARNING) and emits
// a bare-fact DEBUG skip line carrying the error.
func TestMaybeWarnWhatsnewGateError(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return false, "", errors.New("boom")
	})
	logger, buf := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	if got := logger.WarningCount(); got != 0 {
		t.Fatalf("WarningCount = %d, want 0 (fail toward silence)", got)
	}
	if strings.Contains(buf.String(), lockedWarnCopy) {
		t.Fatalf("gate error must not emit the WARNING copy\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("gate error DEBUG line missing the error text\n%s", buf.String())
	}
}

// TestMaybeWarnWhatsnewCopy: the emitted WARNING equals the locked single line with the
// normalized version and is pure ASCII (no em dash U+2014, no en dash U+2013, no emoji).
func TestMaybeWarnWhatsnewCopy(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	logger, _ := captureLogger(t)

	maybeWarnWhatsnew(logger, "/base", "0.30.0")

	got := singleEmittedWarning(t, logger)
	if got != lockedWarnCopy {
		t.Fatalf("emitted warning = %q, want EXACTLY the locked copy %q (Contains is insufficient: additions must fail)", got, lockedWarnCopy)
	}
	if i := strings.IndexFunc(got, func(r rune) bool { return r > 127 }); i != -1 {
		t.Fatalf("emitted warning carries a non-ASCII rune at %d: %q", i, got)
	}
}

// TestMaybeWarnWhatsnewNilLogger: a nil logger is a no-op and must not panic.
func TestMaybeWarnWhatsnewNilLogger(t *testing.T) {
	stubWhatsnewShouldWarn(t, func(baseDir, current string) (bool, string, error) {
		return true, "0.30.0", nil
	})
	// Must not panic.
	maybeWarnWhatsnew(nil, "/base", "0.30.0")
}

// TestMaybeWarnWhatsnewDeliveredToEmailCategories exercises the REAL gate (no stub) and the
// REAL run-log FILE sink, then parses that file exactly as the backup completion path does,
// proving the WARNING is captured into a notify.LogCategory (the email/webhook surface). This
// closes the console-only gap: the other tests assert the in-memory console buffer, not the
// file sink that ParseLogCounts reads. It also integrates the real ShouldWarn v-strip
// (v0.30.0 -> 0.30.0) into the emitted copy.
func TestMaybeWarnWhatsnewDeliveredToEmailCategories(t *testing.T) {
	base := t.TempDir() // no identity/.whatsnew_seen.json -> unseen upgrader
	logPath := filepath.Join(t.TempDir(), "run.log")
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&bytes.Buffer{}) // silence the console sink; we read the file sink
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}

	maybeWarnWhatsnew(logger, base, "v0.30.0") // real ShouldWarn; v-prefix exercises the v-strip

	if err := logger.CloseLogFile(); err != nil {
		t.Fatalf("CloseLogFile: %v", err)
	}

	cats, _, warningCount, _ := orchestrator.ParseLogCounts(logPath, 10)
	if warningCount != 1 {
		t.Fatalf("ParseLogCounts warningCount = %d, want 1", warningCount)
	}
	// The delivered label is byte-exact on the file-sink path. Reconstruct exactly what
	// ParseLogCounts derives from the message (splitCategoryAndExample: split on " - ", then
	// truncate to 120) and require EQUALITY, so a corruption of the file-sink message fails
	// here (a mere substring would not), while a future reword of the copy auto-tracks. For
	// the current copy (no " - ", < 120 chars) expectLabel == lockedWarnCopy.
	expectLabel := strings.TrimSpace(strings.SplitN(lockedWarnCopy, " - ", 2)[0])
	if len(expectLabel) > 120 {
		expectLabel = expectLabel[:117] + "..."
	}
	found := false
	for _, c := range cats {
		if c.Type == "WARNING" && c.Label == expectLabel {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("delivered WARNING LogCategory label != expected %q: %+v", expectLabel, cats)
	}
}

// TestWhatsnewWarnWiredInBootstrap is a STRUCTURAL (AST) guard for the single wiring line. The
// behavioral call lives inside bootstrapRuntime, which cannot be unit-invoked cheaply (it runs
// a real GitHub update probe and a full config load). Parsing the AST -- not scanning text --
// pins that maybeWarnWhatsnew is an UNCONDITIONAL top-level statement of bootstrapRuntime,
// AFTER the checkForUpdates assignment. This catches what a substring scan cannot: a
// commented-out call (not an AST node), a call wrapped in an if/branch (not a direct body
// statement), a call relocated into another function, and outright deletion -- all of which
// would break NOTF-01 (nudge on every non-interactive run, independent of update state).
func TestWhatsnewWarnWiredInBootstrap(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main_runtime.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main_runtime.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "bootstrapRuntime" {
			fn = fd
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("bootstrapRuntime not found in main_runtime.go")
	}

	// directCallName returns the callee name only when s is a DIRECT call statement (a bare
	// call, or an assignment whose single RHS is a call). A call nested inside an if/for/etc.
	// is not a direct body statement, so it yields "" -- which is exactly how a branch-wrapped
	// nudge gets rejected.
	directCallName := func(s ast.Stmt) string {
		var call *ast.CallExpr
		switch st := s.(type) {
		case *ast.ExprStmt:
			call, _ = st.X.(*ast.CallExpr)
		case *ast.AssignStmt:
			if len(st.Rhs) == 1 {
				call, _ = st.Rhs[0].(*ast.CallExpr)
			}
		}
		if call == nil {
			return ""
		}
		if id, ok := call.Fun.(*ast.Ident); ok {
			return id.Name
		}
		return ""
	}

	idxUpd, idxWarn := -1, -1
	for i, s := range fn.Body.List {
		switch directCallName(s) {
		case "checkForUpdates":
			idxUpd = i
		case "maybeWarnWhatsnew":
			idxWarn = i
		}
	}

	if idxUpd < 0 {
		t.Fatal("checkForUpdates is not a direct statement in bootstrapRuntime")
	}
	if idxWarn < 0 {
		t.Fatal("maybeWarnWhatsnew is not an unconditional top-level call in bootstrapRuntime " +
			"(deleted, commented out, wrapped in a branch, or moved) -> NOTF-01 delivery not guaranteed")
	}
	if idxWarn <= idxUpd {
		t.Fatal("maybeWarnWhatsnew must be a direct statement AFTER checkForUpdates in bootstrapRuntime")
	}
}
