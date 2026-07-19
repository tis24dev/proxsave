package sourceguard

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestScanTextFlagsBidiAndZeroWidth is the mutation lever for the reject set.
// The crafted input carries U+202E (RIGHT-TO-LEFT OVERRIDE) and U+200B (ZERO
// WIDTH SPACE) written as \u escapes, so this test file itself holds no literal
// deceptive rune and the tree scan below stays clean.
func TestScanTextFlagsBidiAndZeroWidth(t *testing.T) {
	in := "line1\na\u202eb\u200bc\nline3"
	got := ScanText(in, true)
	if len(got) != 2 {
		t.Fatalf("ScanText found %d findings, want 2: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Line != 2 {
			t.Errorf("finding %#U reported line %d, want line 2", f.Rune, f.Line)
		}
		if f.Why != whyFormat {
			t.Errorf("finding %#U why = %q, want %q", f.Rune, f.Why, whyFormat)
		}
	}
	// Removing U+202E from the reject set drops this rune, turning the len==2
	// check above RED. Assert it explicitly so the mutation is unambiguous.
	var found202E, found200B bool
	for _, f := range got {
		switch f.Rune {
		case '\u202e':
			found202E = true
		case '\u200b':
			found200B = true
		}
	}
	if !found202E {
		t.Fatalf("ScanText did not flag U+202E: %+v", got)
	}
	if !found200B {
		t.Fatalf("ScanText did not flag U+200B: %+v", got)
	}
}

// TestScanTextHomoglyphGatedByFlag proves the homoglyph arm fires only when
// checkHomoglyphs is true. The Cyrillic small letter a (U+0430) is written as a
// \u escape so this file carries no literal homoglyph.
func TestScanTextHomoglyphGatedByFlag(t *testing.T) {
	in := "id\u0430b := 1"
	on := ScanText(in, true)
	if len(on) != 1 || on[0].Why != whyHomoglyph || on[0].Rune != '\u0430' {
		t.Fatalf("ScanText(homoglyph on) = %+v, want one %q finding for U+0430", on, whyHomoglyph)
	}
	if off := ScanText(in, false); len(off) != 0 {
		t.Fatalf("ScanText(homoglyph off) = %+v, want no findings", off)
	}
}

// TestScanTextCleanASCIIYieldsNothing guards against false positives on plain
// source.
func TestScanTextCleanASCIIYieldsNothing(t *testing.T) {
	if got := ScanText("package main\n\nfunc main() {}\n", true); len(got) != 0 {
		t.Fatalf("clean ASCII yielded findings: %+v", got)
	}
}

// TestTrackedSourceHasNoDeceptiveUnicode scans every tracked source file and
// fails, naming file:line, if any deceptive rune is present. This is the
// load-bearing guard: a future commit or fork import that smuggles in bidi,
// zero-width, or homoglyph Unicode names itself here.
func TestTrackedSourceHasNoDeceptiveUnicode(t *testing.T) {
	root := repoRoot(t)
	scanExts := map[string]bool{
		".go": true, ".md": true, ".sh": true, ".yml": true,
		".yaml": true, ".txt": true, ".tmpl": true, ".env": true,
	}
	for _, rel := range trackedFiles(t, root) {
		ext := filepath.Ext(rel)
		if !scanExts[ext] {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			// A path listed by git but absent from the worktree (for example
			// deleted or a submodule gitlink) is not a scan target.
			continue
		}
		for _, f := range ScanText(string(data), ext == ".go") {
			t.Errorf("%s:%d U+%04X %s", rel, f.Line, f.Rune, f.Why)
		}
	}
}

// New invisibles beyond the original set (line separator + a deprecated tag
// char) must be flagged. Runes as escapes so this file stays clean.
func TestScanTextFlagsExtendedInvisibles(t *testing.T) {
	got := ScanText("a\u2028b\U000e0041c", true)
	if len(got) != 2 {
		t.Fatalf("want 2 format findings (U+2028, U+E0041), got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Why != whyFormat {
			t.Errorf("finding %#U why = %q, want %q", f.Rune, f.Why, whyFormat)
		}
	}
}

// Each curated confusable block must be flagged as a homoglyph when
// checkHomoglyphs is true, and NOT when false. Runes as escapes.
func TestScanTextFlagsCuratedConfusableBlocks(t *testing.T) {
	// Armenian U+0561, Cherokee U+13A0, Coptic U+2C81, Fullwidth-Latin U+FF41.
	for _, r := range []rune{'\u0561', '\u13a0', '\u2c81', '\uff41'} {
		in := string(r)
		on := ScanText(in, true)
		if len(on) != 1 || on[0].Why != whyHomoglyph || on[0].Rune != r {
			t.Fatalf("ScanText(%#U, on) = %+v, want one %q finding", r, on, whyHomoglyph)
		}
		if off := ScanText(in, false); len(off) != 0 {
			t.Fatalf("ScanText(%#U, off) = %+v, want none", r, off)
		}
	}
}

// FP guard: legitimate non-ASCII that is NOT a confusable homoglyph (accented
// Latin, CJK) must never be flagged, even with checkHomoglyphs on. This pins the
// curated-blocks decision. U+00E9 e-acute, U+4E16 CJK.
func TestScanTextIgnoresLegitimateNonASCII(t *testing.T) {
	if got := ScanText("caf\u00e9 \u4e16", true); len(got) != 0 {
		t.Fatalf("legit non-ASCII (accented Latin, CJK) flagged: %+v", got)
	}
}

// repoRoot walks up from the package dir until it finds go.mod, mirroring
// repoRootForVersionTest in cmd/proxsave/main_go_version_test.go.
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
			t.Fatalf("go.mod not found above %s (runtime %s)", dir, runtime.Version())
		}
		dir = parent
	}
}

// trackedFiles returns repo-relative paths of git-tracked files. git ls-files
// yields only tracked files and already excludes gitignored and vendored trees
// (vendor/, diagnostics/, .claude/, .superpowers/). If git cannot run in this
// sandbox it falls back to a filepath.Walk that skips those trees, so the guard
// never silently degrades to scanning nothing.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	if out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output(); err == nil {
		var files []string
		for _, p := range strings.Split(string(out), "\x00") {
			if p != "" {
				files = append(files, p)
			}
		}
		return files
	}
	// Fallback: walk the worktree, skipping non-source trees.
	skip := map[string]bool{
		"vendor": true, ".git": true, "diagnostics": true,
		".claude": true, ".superpowers": true, "node_modules": true,
	}
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skip[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}
