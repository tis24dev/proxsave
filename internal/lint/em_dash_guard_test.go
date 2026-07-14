package lint

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

const (
	emDash rune = '—' // —
	enDash rune = '–' // –
)

func hasBannedDash(s string) bool {
	return strings.ContainsRune(s, emDash) || strings.ContainsRune(s, enDash)
}

// repoRoot walks up from the test working directory to the module root (the dir
// holding go.mod), so the guard scans the whole repository regardless of where the
// test binary runs from.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test directory")
		}
		dir = parent
	}
}

// TestNoEmDashInUserFacingText enforces the project rule that no em/en dash (a tell
// of AI-written text) appears in any USER-FACING string: Go string literals in
// non-test .go files (log/UI/error/notification copy), the installer script, and the
// embedded config templates the operator reads and edits. Use a comma, period, or
// parenthesis instead.
//
// Source-code COMMENTS are deliberately OUT of scope (a different category the repo
// tolerates), so the .go scan inspects only STRING literals via the parser, never
// comments. _test.go files are skipped too: a test may legitimately embed a dash as
// hostile input (e.g. an escape-injection fixture).
func TestNoEmDashInUserFacingText(t *testing.T) {
	root := repoRoot(t)
	var offenders []string

	// 1. Go string literals (comments excluded: only BasicLit STRING is inspected).
	fset := token.NewFileSet()
	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Fast path: a banned dash is the same UTF-8 bytes whether it lands in a
		// string literal or a comment, so if the file has none it cannot have an
		// offending string literal and we skip the (expensive) parse. Files that DO
		// contain the bytes are parsed, and only STRING literals are flagged there.
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil // unreadable: the build/vet will surface it, not this guard
		}
		if !hasBannedDash(string(src)) {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			// Not this guard's job to report unparsable Go; the build/vet will.
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			// A STRING literal from a file the parser accepted is always well-formed,
			// so Unquote cannot fail here; a failure would be a bug, not bad input.
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				t.Fatalf("unquote %s at %s: %v", lit.Value, fset.Position(lit.Pos()), uerr)
			}
			if hasBannedDash(s) {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+strconv.Itoa(fset.Position(lit.Pos()).Line)+" (Go string literal)")
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk: %v", walkErr)
	}

	// 2. Non-Go assets read in full by the operator: the installer and the embedded
	// config templates. Here the whole file counts (installer echoes, template
	// comments the operator reads), so a plain content scan is the right check.
	for _, p := range userFacingAssets(root) {
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			continue
		}
		if hasBannedDash(string(data)) {
			rel, _ := filepath.Rel(root, p)
			offenders = append(offenders, rel+" (user-facing asset)")
		}
	}

	if len(offenders) > 0 {
		t.Fatalf("em/en dash (a banned AI tell) found in %d user-facing location(s); replace with a comma, period, or parenthesis:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

func userFacingAssets(root string) []string {
	var out []string
	if p := filepath.Join(root, "install.sh"); fileExists(p) {
		out = append(out, p)
	}
	// Live config templates the operator reads and edits.
	out = appendFilesWithSuffix(out, filepath.Join(root, "internal", "config", "templates"), ".env")
	// Installer characterization goldens: the rendered backup.env and the captured
	// installer transcript the operator sees. They live under a testdata/ dir the .go
	// walk SkipDirs, so scan them explicitly here; the rest of testdata stays exempt.
	out = appendFilesWithSuffix(out, filepath.Join(root, "cmd", "proxsave", "testdata", "install_characterization"), ".env", ".transcript")
	return out
}

// appendFilesWithSuffix appends every file directly under dir whose name ends with
// one of the given suffixes. A missing dir is not an error (scan what exists).
func appendFilesWithSuffix(out []string, dir string, suffixes ...string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		for _, suf := range suffixes {
			if strings.HasSuffix(e.Name(), suf) {
				out = append(out, filepath.Join(dir, e.Name()))
				break
			}
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
