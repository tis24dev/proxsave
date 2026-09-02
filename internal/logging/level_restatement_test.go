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
// NO BASELINE, ON PURPOSE. The rule arrived after the violations: 143 call sites already break
// it. Freezing them in a known-violations file would leave this test GREEN on a tree that has
// 143 of the defect, which tells a reader looking at the traffic light something false. The
// maintainer chose a red suite instead, accepted and expected until the list reaches zero, and a
// cleanup done one FILE per commit with the result of each edit MEASURED rather than reasoned
// about: see TestRemovingARestatementChangesNothingButTheLine in internal/orchestrator, which
// renders the line through the real logger and re-counts it through the real ParseLogCounts,
// before and after. Static reasoning that "the column classifies, so the word is free" is not
// the evidence this cleanup runs on.

// levelOfLoggerMethod maps a logger method to the level its column will print. Skip, Step and
// Phase are labelled INFO lines (logWithLabel with types.LogLevelInfo), and NotifyError renders
// as an error, so both restate ERROR.
var levelOfLoggerMethod = map[string]string{
	"Error":       "ERROR",
	"NotifyError": "ERROR",
	"Critical":    "CRITICAL",
	// Fatal's format string sits at argument 1 (argument 0 is the exit code); the
	// every-literal scan reaches it there.
	"Fatal":   "CRITICAL",
	"Warning": "WARNING",
	"Info":    "INFO",
	"Skip":    "INFO",
	"Step":    "INFO",
	"Phase":   "INFO",
	"Debug":   "DEBUG",
}

// wrapperFuncs are plain-function wrappers whose string literal sits past a leading
// logger argument. 33 production call sites route through logBootstrapWarning alone,
// and a restatement written there was structurally invisible to the scanner.
var wrapperFuncs = map[string]struct {
	level    string
	firstArg int
}{
	"logBootstrapWarning":          {"WARNING", 1},
	"logBootstrapInfo":             {"INFO", 1},
	"logBootstrapDebug":            {"DEBUG", 1},
	"logWarning":                   {"WARNING", 1}, // internal/identity, internal/orchestrator/backup_sources
	"logDebug":                     {"DEBUG", 1},   // same two packages
	"logTelegramRegistrationDebug": {"DEBUG", 1},
}

// restatedWordPrefixes are the openings that repeat the level in words. Prefix-only on purpose:
// the doubled token is what the reader sees at the start of the message, and matching anywhere
// would flag a sentence that merely mentions an error it is reporting about something else.
var restatedWordPrefixes = map[string][]string{
	"ERROR": {"error:", "error -", "error ", "[error]", "fatal:"},
	// No bare "critical " here: unlike error/warning, the word is an ordinary
	// adjective in this tree ("Critical files collected successfully"), and the
	// space form flags those. The punctuated forms are unambiguous.
	"CRITICAL": {"critical:", "critical -", "[critical]"},
	"WARNING":  {"warning:", "warning -", "warning ", "warn:", "[warning]", "[warn]"},
	"INFO":     {"info:", "info -", "[info]"},
	"DEBUG":    {"debug:", "debug -", "[debug]"},
}

// severityGlyphs are the TUI's severity alphabet plus the emoji family that three files use
// instead. Matched ANYWHERE in the literal: a glyph mid-line still states a severity.
// U+FE0F (the emoji variation selector in "⚠️") follows the base rune, so matching the base
// covers both spellings.
var severityGlyphs = []string{"✓", "⚠", "✗", "ℹ", "✅", "❌", "➖"}

type levelViolation struct {
	file      string // module-relative
	line      int
	method    string
	level     string // the level the column prints
	wordLevel string // the level the opening words spell (kind "word" only)
	literal   string
	kind      string // "word" or "glyph"
}

func (v levelViolation) String() string {
	if v.kind == "word" && v.wordLevel != v.level {
		return fmt.Sprintf("%s:%d %s(%q) opens with %s while its column prints %s", v.file, v.line, v.method, truncate(v.literal, 60), v.wordLevel, v.level)
	}
	return fmt.Sprintf("%s:%d %s(%q) restates %s as a %s", v.file, v.line, v.method, truncate(v.literal, 60), v.level, v.kind)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestLoggerCallersDoNotRestateTheirOwnLevel(t *testing.T) {
	found := scanForLevelRestatement(t, moduleRoot(t))
	if len(found) == 0 {
		return
	}

	// Grouped by file, because the cleanup is one file per commit: the report IS the worklist.
	byFile := map[string][]string{}
	for _, v := range found {
		byFile[v.file] = append(byFile[v.file], v.String())
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		if len(byFile[files[i]]) != len(byFile[files[j]]) {
			return len(byFile[files[i]]) > len(byFile[files[j]])
		}
		return files[i] < files[j]
	})

	var b strings.Builder
	words, glyphs := 0, 0
	for _, v := range found {
		if v.kind == "word" {
			words++
		} else {
			glyphs++
		}
	}
	fmt.Fprintf(&b, "%d logger call(s) in %d file(s) restate the level their own column already prints "+
		"(%d in words, %d as a glyph).\n"+
		"The console writes \"[ts] LEVEL   message\"; opening the message with the level, or drawing a\n"+
		"severity glyph, says it twice. Drop it and let the column carry it - but MEASURE each edit\n"+
		"with TestRemovingARestatementChangesNothingButTheLine before trusting it.\n",
		len(found), len(files), words, glyphs)
	for _, f := range files {
		fmt.Fprintf(&b, "\n%s (%d)\n", f, len(byFile[f]))
		for _, line := range byFile[f] {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	t.Error(b.String())
}

// scanForLevelRestatement parses every non-test .go file under root and reports each logger call
// whose FIRST argument is a string literal that restates the call's own level.
func scanForLevelRestatement(t *testing.T, root string) []levelViolation {
	t.Helper()
	var out []levelViolation
	fset := token.NewFileSet()

	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// An entry that vanished between readdir and lstat is not this scan's
			// problem: editors, hooks and other tooling write and remove temporary
			// files in the tree while the suite runs, and aborting on one turns the
			// worklist into a single unrelated error. Anything else still stops it.
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			// Skip dot directories and anything holding its own .git entry. Without this the
			// walk descends into the agent worktrees this repo keeps under .claude/, counts
			// every call site twice, and reports a tree twice the size of the real one. Same
			// guard as cmd/proxsave/personal_scripts_audited_test.go, for the same reason.
			if path != root && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, ".git")); statErr == nil {
					return filepath.SkipDir
				}
			}
			switch info.Name() {
			case "vendor", "testdata", "build", "gsd", "diagnostics", "node_modules":
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
			var method, level string
			firstArg := 0
			switch fun := call.Fun.(type) {
			case *ast.SelectorExpr:
				lvl, known := levelOfLoggerMethod[fun.Sel.Name]
				if !known {
					return true
				}
				method, level = fun.Sel.Name, lvl
			case *ast.Ident:
				w, known := wrapperFuncs[fun.Name]
				if !known {
					return true
				}
				method, level, firstArg = fun.Name, w.level, w.firstArg
			default:
				return true
			}
			// Every string literal in the call is inspected, not only the format:
			// Warning("%s", "ERROR: planted") restated through argument 1 and the
			// scanner never saw it.
			for i := firstArg; i < len(call.Args); i++ {
				lit, ok := call.Args[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				text, unquoteErr := strconv.Unquote(lit.Value)
				if unquoteErr != nil {
					continue
				}
				kind, wordLevel := restatementKind(level, text)
				if kind == "" {
					continue
				}
				out = append(out, levelViolation{
					file: rel, line: fset.Position(lit.Pos()).Line, method: method,
					level: level, wordLevel: wordLevel, literal: text, kind: kind,
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
// restatementKind flags a literal that opens with ANY level word, not only the
// call's own: the column owns severity words, and a Debug line opening "WARNING: "
// contradicts its column instead of repeating it, which is worse, not better.
// The second return names the level the words spell, for the report.
func restatementKind(level, literal string) (kind, wordLevel string) {
	lower := strings.ToLower(strings.TrimSpace(literal))
	for lvl, prefixes := range restatedWordPrefixes {
		for _, prefix := range prefixes {
			if strings.HasPrefix(lower, prefix) {
				return "word", lvl
			}
		}
	}
	for _, glyph := range severityGlyphs {
		if strings.Contains(literal, glyph) {
			return "glyph", level
		}
	}
	return "", ""
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
