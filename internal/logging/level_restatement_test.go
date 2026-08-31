package logging

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The console renders every line as "[timestamp] <colour>LEVEL   <reset> message"
// (assembleConsoleLine), and BootstrapLogger.Warning/.Error go through the same
// FormatConsoleLogLine. So in the plain CLI the SEVERITY IS THE LOGGER'S JOB: it is already on
// the line, coloured, in its own column, before the caller's message starts.
//
// A caller that opens its message with the level says it twice:
//
//	bootstrap.Error("ERROR: %v", err)   ->   [2026-08-31 22:30:00] ERROR    ERROR: permission denied
//
// and a caller that draws a glyph says it twice in a different alphabet:
//
//	logging.Warning("⚠ Cloud log directory not configured")
//	                              ->   [2026-08-31 22:30:00] WARNING  ⚠ Cloud log directory not configured
//
// The glyphs belong to the TUI, where there is no timestamp and no level column, so the severity
// has nowhere else to travel (internal/ui/theme/theme.go:60-63, RenderStatusLevel). The rule this
// test enforces is the one the maintainer stated for the whole screen campaign: the TUI lends the
// CLI its SENTENCE, never its glyph and never its colour.
//
// WHAT THIS TEST CANNOT SEE. It reads string LITERALS at the call site. A message built into a
// variable, or assembled with fmt.Sprintf before the call, is invisible to it. That is a real gap
// and not a bug in the rule: cmd/proxsave/runtime_helpers.go:453 and :457 build "⚠ %s initialized
// with warnings" with fmt.Sprintf, and logStorageInitSummary at :497 then reads that very glyph
// back with strings.HasPrefix to CHOOSE between logging.Warning and logging.Info. There the glyph
// is load-bearing, and removing it would silently downgrade the line (and a WARNING is what
// promotes an otherwise clean run to exit 1, through ParseLogCounts and applyIssueExitCode). This
// test does not reach that pair, so the comment there has to carry the warning instead.
//
// THE BASELINE. The rule arrived after the violations: 131 call sites already break it. Rather
// than hold the rule hostage to that cleanup, the known ones are frozen in
// testdata/level_restatement_baseline.txt and this test fails in BOTH directions - on a violation
// that is not in the file (a new one), and on a baseline entry that no longer matches anything (a
// fixed one whose line must now be deleted). That second half is what makes the file a ratchet
// instead of a dumping ground: it can only shrink. When it reaches zero, delete it and this test
// becomes a plain rule.
//
// Regenerate after a deliberate change with: UPDATE_LEVEL_BASELINE=1 go test ./internal/logging/
// and read the diff before committing it.
//
// ONE ENTRY PER (file, literal), NOT per call site. cmd/proxsave/main_modes.go calls
// Error("ERROR: %v") five times; they collapse to one baseline line, so the entry disappears only
// when all five are fixed. That is deliberate: the unit of work is the pattern in a file, not an
// individual call, and a per-line key would churn on every unrelated edit above it.

// levelOfLoggerMethod maps a logger method to the level its column will print. Skip, Step and
// Phase are labelled INFO lines (logWithLabel with types.LogLevelInfo), and NotifyError renders
// as an error, so both restate ERROR.
var levelOfLoggerMethod = map[string]string{
	"Error":       "ERROR",
	"NotifyError": "ERROR",
	"Warning":     "WARNING",
	"Info":        "INFO",
	"Skip":        "INFO",
	"Step":        "INFO",
	"Phase":       "INFO",
}

// restatedWordPrefixes are the openings that repeat the level in words. Prefix-only on purpose:
// the doubled token is what the reader sees at the start of the message, and matching anywhere
// would flag a sentence that merely mentions an error it is reporting about something else.
var restatedWordPrefixes = map[string][]string{
	"ERROR":   {"error:", "error -", "[error]", "fatal:", "critical:", "[critical]"},
	"WARNING": {"warning:", "warn:", "[warning]", "[warn]"},
	"INFO":    {"info:", "[info]"},
}

// severityGlyphs are the TUI's severity alphabet plus the emoji family that three files use
// instead. Matched ANYWHERE in the literal: a glyph mid-line still states a severity.
// U+FE0F (the emoji variation selector in "⚠️") follows the base rune, so matching the base
// covers both spellings.
var severityGlyphs = []string{"✓", "⚠", "✗", "ℹ", "✅", "❌", "➖"}

type levelViolation struct {
	file    string // module-relative
	line    int
	method  string
	level   string
	literal string
	kind    string // "word" or "glyph"
}

// key identifies a violation across edits. The LITERAL is the key, not the line number: a line
// number moves whenever anything above it changes, which would turn the baseline into churn.
func (v levelViolation) key() string {
	lit := v.literal
	if len(lit) > 60 {
		lit = lit[:60]
	}
	return v.file + "\t" + v.kind + "\t" + lit
}

func (v levelViolation) String() string {
	return fmt.Sprintf("%s:%d %s(%q) restates %s as a %s", v.file, v.line, v.method, truncate(v.literal, 60), v.level, v.kind)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestLoggerCallersDoNotRestateTheirOwnLevel(t *testing.T) {
	root := moduleRoot(t)
	found := scanForLevelRestatement(t, root)

	baselinePath := filepath.Join(root, "internal", "logging", "testdata", "level_restatement_baseline.txt")

	if os.Getenv("UPDATE_LEVEL_BASELINE") != "" {
		writeBaseline(t, baselinePath, found)
		t.Logf("baseline rewritten with %d entries; read the diff before committing", len(found))
		return
	}

	baseline := readBaseline(t, baselinePath)

	seen := make(map[string]bool, len(found))
	var added []string
	for _, v := range found {
		k := v.key()
		seen[k] = true
		if !baseline[k] {
			added = append(added, v.String())
		}
	}
	sort.Strings(added)
	if len(added) > 0 {
		t.Errorf("%d logger call(s) restate the level their own column already prints.\n"+
			"The console writes \"[ts] LEVEL   message\"; opening the message with the level, or with a\n"+
			"severity glyph, says it twice. Drop it from the message and let the column carry it.\n\n%s",
			len(added), strings.Join(added, "\n"))
	}

	var fixed []string
	for k := range baseline {
		if !seen[k] {
			fixed = append(fixed, strings.ReplaceAll(k, "\t", " | "))
		}
	}
	sort.Strings(fixed)
	if len(fixed) > 0 {
		t.Errorf("%d baseline entr(ies) no longer match any call site. Good - they were fixed.\n"+
			"Now delete these lines from internal/logging/testdata/level_restatement_baseline.txt,\n"+
			"so the file can only ever shrink:\n\n%s", len(fixed), strings.Join(fixed, "\n"))
	}
}

// scanForLevelRestatement parses every non-test .go file under root and reports each logger call
// whose FIRST argument is a string literal that restates the call's own level.
func scanForLevelRestatement(t *testing.T, root string) []levelViolation {
	t.Helper()
	var out []levelViolation
	fset := token.NewFileSet()

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", ".gitnexus", "vendor", "testdata", "build", "gsd", "diagnostics", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			level, ok := levelOfLoggerMethod[sel.Sel.Name]
			if !ok {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}
			line := fset.Position(lit.Pos()).Line
			if kind := restatementKind(level, text); kind != "" {
				out = append(out, levelViolation{
					file: rel, line: line, method: sel.Sel.Name, level: level, literal: text, kind: kind,
				})
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out
}

// restatementKind reports "word" when the literal opens with its own level, "glyph" when it
// carries a severity glyph anywhere, and "" when it is clean. The word check wins when both
// apply, so one call site produces one baseline entry.
func restatementKind(level, literal string) string {
	lower := strings.ToLower(strings.TrimSpace(literal))
	for _, prefix := range restatedWordPrefixes[level] {
		if strings.HasPrefix(lower, prefix) {
			return "word"
		}
	}
	for _, glyph := range severityGlyphs {
		if strings.Contains(literal, glyph) {
			return "glyph"
		}
	}
	return ""
}

// moduleRoot walks up from the test's directory to the go.mod, so the scan covers the whole
// module rather than a path spelled relative to this package.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test directory")
		}
		dir = parent
	}
}

func readBaseline(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}
		}
		t.Fatalf("read baseline: %v", err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out
}

func writeBaseline(t *testing.T, path string, violations []levelViolation) {
	t.Helper()
	keys := make([]string, 0, len(violations))
	seen := map[string]bool{}
	for _, v := range violations {
		k := v.key()
		if seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	sort.Strings(keys)
	header := "# Logger calls that restate their own level. See level_restatement_test.go.\n" +
		"# Format: <file>\\t<word|glyph>\\t<first 60 chars of the literal>\n" +
		"# This file may only SHRINK. Fix a call site, then delete its line here.\n"
	body := header + strings.Join(keys, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
}
