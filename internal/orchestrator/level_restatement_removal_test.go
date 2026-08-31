package orchestrator

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

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestRemovingARestatementLeavesExactlyOneLevelWord is the bench the level-restatement cleanup
// runs on. The rule the maintainer set for that work: run the REAL code, look at the REAL line it
// builds, remove the duplication, run it AGAIN, and confirm that ONE of the two words survived -
// not both, and not neither.
//
// "Neither" is the failure this exists to catch. Dropping "WARNING: " from the message is only
// correct because the level column already says WARNING; if an edit ever removed the column's
// word too, or if a line reached a surface that prints no column, the severity would vanish
// silently and a warning would read as a remark.
//
// WHAT IS REAL HERE AND WHAT IS NOT. The literals are read out of the source with go/ast, never
// retyped, so the string under test is the string the product ships. The line is then built by
// the REAL logger (logging.New -> Warning/Error/Info -> assembleConsoleLine) and the REAL
// ParseLogCounts re-reads it. What is NOT reproduced is each call site's trigger condition: this
// does not fail a secondary storage to reach secondary.go:101. That gap is deliberate and stated
// rather than hidden - the trigger decides WHETHER the line is written, and this bench is about
// what the line IS once written.
//
// It runs over every violating call site in the tree, so it reports which of the 143 are safe to
// clean and, if any is not, names it instead of leaving it to be discovered later.
func TestRemovingARestatementLeavesExactlyOneLevelWord(t *testing.T) {
	sites := scanRestatementSites(t)
	if len(sites) == 0 {
		t.Skip("no restatement sites left; the cleanup is finished and this bench has nothing to measure")
	}

	var unsafe []string
	for _, s := range sites {
		before := renderThroughRealLogger(t, s.level, s.literal)
		stripped, ok := stripRestatement(s.level, s.literal, s.kind)
		if !ok {
			unsafe = append(unsafe, fmt.Sprintf("%s:%d cannot be stripped mechanically: %q", s.file, s.line, s.literal))
			continue
		}
		after := renderThroughRealLogger(t, s.level, stripped)

		// 1. The column's word must SURVIVE. This is the "not neither" half of the rule.
		if !strings.Contains(after, levelColumnWord(s.level)) {
			unsafe = append(unsafe, fmt.Sprintf("%s:%d the level word vanished from the line entirely:\n    before=%q\n    after =%q",
				s.file, s.line, before, after))
			continue
		}

		// 2. For a word restatement the count must go from two to one, measured on the line.
		if s.kind == "word" {
			b, a := strings.Count(before, levelColumnWord(s.level)), strings.Count(after, levelColumnWord(s.level))
			if b < 2 || a != 1 {
				unsafe = append(unsafe, fmt.Sprintf("%s:%d level word count went %d -> %d, want 2 -> 1:\n    before=%q\n    after =%q",
					s.file, s.line, b, a, before, after))
				continue
			}
		}

		// 3. Nothing but the restatement may leave the message. The stripped text must be the
		//    ORIGINAL with a recognised opening removed and nothing else, so a strip that eats
		//    one character too many is caught here rather than shipped.
		if why := whatElseLeft(s.level, s.literal, stripped, s.kind); why != "" {
			unsafe = append(unsafe, fmt.Sprintf("%s:%d more than the restatement changed (%s):\n    before=%q\n    after =%q",
				s.file, s.line, why, s.literal, stripped))
			continue
		}

		// 4. The run's accounting must not move: the counts come from the column, and this
		//    proves it on the real parser rather than asserting it from the code.
		bc, ac := countThroughRealParser(t, before), countThroughRealParser(t, after)
		if bc != ac {
			unsafe = append(unsafe, fmt.Sprintf("%s:%d ParseLogCounts changed %v -> %v:\n    before=%q\n    after =%q",
				s.file, s.line, bc, ac, before, after))
		}
	}

	sort.Strings(unsafe)
	if len(unsafe) > 0 {
		t.Errorf("%d of %d restatement site(s) are NOT safe to clean mechanically:\n\n%s",
			len(unsafe), len(sites), strings.Join(unsafe, "\n"))
	}
	t.Logf("%d restatement site(s) measured; %d safe to clean", len(sites), len(sites)-len(unsafe))
}

type parsedCounts struct{ errors, warnings, notify int }

// renderThroughRealLogger builds the line with the production logger, not with a copy of its
// format string. Colour is off so the comparison is about words, not escape codes.
func renderThroughRealLogger(t *testing.T, level types.LogLevel, format string) string {
	t.Helper()
	lg := logging.New(types.LogLevelDebug, false)
	var b strings.Builder
	lg.SetOutput(&b)
	switch level {
	case types.LogLevelError:
		lg.Error("%s", format)
	case types.LogLevelWarning:
		lg.Warning("%s", format)
	default:
		lg.Info("%s", format)
	}
	return strings.TrimRight(b.String(), "\n")
}

// countThroughRealParser writes the line to a real file and runs the real ParseLogCounts, the
// same function the run epilogue and the notifications use.
func countThroughRealParser(t *testing.T, line string) parsedCounts {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(p, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write probe log: %v", err)
	}
	_, e, w, n := ParseLogCounts(p, 10)
	return parsedCounts{e, w, n}
}

func levelColumnWord(level types.LogLevel) string { return level.String() }

// whatElseLeft re-derives the removal from the ORIGINAL text and reports how the candidate
// differs from it. Comparing the rendered lines against the messages they were built from proves
// nothing (a line always contains its own message), so the comparison has to be message to
// message, with the removed opening reconstructed independently here.
func whatElseLeft(level types.LogLevel, original, stripped, kind string) string {
	if kind == "word" {
		lower := strings.ToLower(original)
		for _, prefix := range restatedPrefixesFor(level) {
			if !strings.HasPrefix(lower, prefix) {
				continue
			}
			want := strings.TrimLeft(original[len(prefix):], " ")
			if stripped != want {
				return fmt.Sprintf("want %q after dropping %q", want, original[:len(prefix)])
			}
			return ""
		}
		return "no recognised opening to drop"
	}
	for _, g := range severityGlyphList {
		idx := strings.Index(original, g)
		if idx < 0 {
			continue
		}
		want := strings.TrimLeft(original[:idx]+original[idx+len(g):], " ")
		if stripped != want {
			return fmt.Sprintf("want %q after dropping %q", want, g)
		}
		return ""
	}
	return "no severity glyph to drop"
}

// stripRestatement removes the opening the level already prints, or the severity glyph, and
// reports whether it could do so without guessing.
func stripRestatement(level types.LogLevel, literal, kind string) (string, bool) {
	if kind == "word" {
		lower := strings.ToLower(literal)
		for _, prefix := range restatedPrefixesFor(level) {
			if strings.HasPrefix(lower, prefix) {
				return strings.TrimLeft(literal[len(prefix):], " "), true
			}
		}
		return "", false
	}
	for _, g := range severityGlyphList {
		if idx := strings.Index(literal, g); idx >= 0 {
			out := literal[:idx] + literal[idx+len(g):]
			return strings.TrimLeft(out, " "), true
		}
	}
	return "", false
}

func restatedPrefixesFor(level types.LogLevel) []string {
	switch level {
	case types.LogLevelError:
		return []string{"error:", "error -", "[error]", "fatal:", "critical:", "[critical]"}
	case types.LogLevelWarning:
		return []string{"warning:", "warn:", "[warning]", "[warn]"}
	default:
		return []string{"info:", "[info]"}
	}
}

var severityGlyphList = []string{"✓", "⚠", "✗", "ℹ", "✅", "❌", "➖"}

type restatementSite struct {
	file    string
	line    int
	level   types.LogLevel
	literal string
	kind    string
}

var loggerMethodLevel = map[string]types.LogLevel{
	"Error":       types.LogLevelError,
	"NotifyError": types.LogLevelError,
	"Warning":     types.LogLevelWarning,
	"Info":        types.LogLevelInfo,
	"Skip":        types.LogLevelInfo,
	"Step":        types.LogLevelInfo,
	"Phase":       types.LogLevelInfo,
}

// scanRestatementSites reads the literals out of the tree, so no string under test is one this
// file typed. Same scan the guard in internal/logging performs, repeated here because that guard
// cannot import this package (orchestrator already imports logging).
func scanRestatementSites(t *testing.T) []restatementSite {
	t.Helper()
	root := moduleRootFromTest(t)
	var out []restatementSite
	fset := token.NewFileSet()

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return fmt.Errorf("parse %s: %w", path, perr)
		}
		rel, _ := filepath.Rel(root, path)
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
			level, ok := loggerMethodLevel[sel.Sel.Name]
			if !ok {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if kind := kindOfRestatement(level, text); kind != "" {
				out = append(out, restatementSite{
					file: rel, line: fset.Position(lit.Pos()).Line, level: level, literal: text, kind: kind,
				})
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out
}

func kindOfRestatement(level types.LogLevel, literal string) string {
	lower := strings.ToLower(strings.TrimSpace(literal))
	for _, prefix := range restatedPrefixesFor(level) {
		if strings.HasPrefix(lower, prefix) {
			return "word"
		}
	}
	for _, g := range severityGlyphList {
		if strings.Contains(literal, g) {
			return "glyph"
		}
	}
	return ""
}

func moduleRootFromTest(t *testing.T) string {
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
