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
// WHAT THIS TEST CANNOT SEE. It resolves compile-time text: literals, "+" chains of them, inline
// fmt.Sprintf formats, and a SAME-FUNCTION variable last assigned one of those (that resolution
// caught runtime_helpers.go's warnExecPathMissing, whose "WARNING: " travelled through msg).
// Text that crosses a function boundary is still invisible: formatStorageInitSummary builds
// "⚠ %s initialized with warnings" and its callers log the returned string, so those glyphs never
// appear here. The old strings.HasPrefix("⚠") read-back that once made that glyph load-bearing is
// gone - the level is a returned flag now (storage_init_summary_level_test.go pins it) - but the
// cross-function gap itself remains a declared limitation of this gate.
//
// NO BASELINE, ON PURPOSE. The rule arrived after the violations: 143 call sites broke it when
// the campaign opened (85 in words, 58 as a glyph, under the original scanner; the extended
// scanner re-baselined the totals). Freezing them in a known-violations file would leave this
// test GREEN on a tree full of the defect, which tells a reader looking at the traffic light
// something false. The
// maintainer chose a red suite instead, accepted and expected until the list reaches zero, and a
// cleanup done one FILE per commit with the result of each edit MEASURED rather than reasoned
// about: see TestRemovingARestatementLeavesExactlyOneLevelWord in internal/orchestrator, which
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
	"Fatal": "CRITICAL",
	// AppendRaw writes "[ts] INFO     msg" into the file (logger.go): a severity
	// word or glyph in its literal restates that column.
	"AppendRaw": "INFO",
	"Warning":   "WARNING",
	"Info":      "INFO",
	"Skip":      "INFO",
	"Step":      "INFO",
	"Phase":     "INFO",
	"Debug":     "DEBUG",
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
		"with TestRemovingARestatementLeavesExactlyOneLevelWord before trusting it.\n",
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

		// varText tracks, per function and in lexical order, identifiers whose last
		// assignment resolves to compile-time text (a literal, a "+" chain, an
		// inline Sprintf): msg := fmt.Sprintf("WARNING: ..."); l.Warning(msg) was
		// structurally invisible. Best effort by design: a reassignment from
		// anything unresolvable clears the entry rather than keeping stale text.
		varText := map[string]string{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				varText = map[string]string{}
				return true
			case *ast.AssignStmt:
				if len(node.Lhs) == len(node.Rhs) {
					for i, lhs := range node.Lhs {
						id, ok := lhs.(*ast.Ident)
						if !ok || id.Name == "_" {
							continue
						}
						if text, ok := literalText(node.Rhs[i]); ok {
							varText[id.Name] = text
						} else {
							delete(varText, id.Name)
						}
					}
				}
				return true
			}
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
			// Every argument is inspected, not only the format: Warning("%s",
			// "ERROR: planted") restated through argument 1 and the scanner never
			// saw it. literalText also folds "+" chains and inline Sprintf, and
			// varText resolves a same-function variable.
			for i := firstArg; i < len(call.Args); i++ {
				text, resolvable := literalText(call.Args[i])
				if !resolvable {
					if id, isIdent := call.Args[i].(*ast.Ident); isIdent {
						text, resolvable = varText[id.Name], varText[id.Name] != ""
					}
				}
				if !resolvable {
					continue
				}
				kind, wordLevel := restatementKind(level, text)
				if kind == "" {
					continue
				}
				out = append(out, levelViolation{
					file: rel, line: fset.Position(call.Args[i].Pos()).Line, method: method,
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
// bracketedLevelTokens are flagged ANYWHERE in the literal, not only as a prefix:
// "[WARNING]" mid-sentence is the legacy Bash level marker, unambiguous wherever it
// sits, unlike the bare words the prefix rule deliberately confines to the opening.
var bracketedLevelTokens = map[string]string{
	"[error]": "ERROR", "[critical]": "CRITICAL", "[warning]": "WARNING",
	"[warn]": "WARNING", "[info]": "INFO", "[debug]": "DEBUG",
}

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
	for token, lvl := range bracketedLevelTokens {
		if strings.Contains(lower, token) {
			return "word", lvl
		}
	}
	for _, glyph := range severityGlyphs {
		if strings.Contains(literal, glyph) {
			return "glyph", level
		}
	}
	return "", ""
}

// literalText resolves an expression to compile-time string text where the AST
// alone allows it: a string literal, a parenthesised one, a "+" chain of them, or
// an inline fmt.Sprintf whose format is one of those. A literal head glued to a
// computed tail resolves to the head, which is all the prefix rule needs.
// Anything else stays a declared limitation of this gate.
func literalText(expr ast.Expr) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(e.Value)
		return text, err == nil
	case *ast.ParenExpr:
		return literalText(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, okL := literalText(e.X)
		if right, okR := literalText(e.Y); okL && okR {
			return left + right, true
		}
		if okL {
			return left, true
		}
		return "", false
	case *ast.CallExpr:
		if sel, ok := e.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(e.Args) > 0 {
			return literalText(e.Args[0])
		}
		return "", false
	default:
		return "", false
	}
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
