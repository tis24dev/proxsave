package logging

import (
	"os"
	"path/filepath"
	"testing"
)

// The gate reads its rules from two package variables. Emptying severityGlyphs made
// TestLoggerCallersDoNotRestateTheirOwnLevel PASS over 58 real glyph sites, and
// nothing noticed: the scanner had no fixture proving it still sees anything. This
// plants one known word violation and one known glyph violation in a throwaway tree
// and demands the scanner report exactly those.
func TestScannerStillSeesAPlantedViolationOfEachKind(t *testing.T) {
	root := t.TempDir()
	src := `package fixture

type fakeLogger struct{}

func (fakeLogger) Warning(string, ...any) {}
func (fakeLogger) Error(string, ...any)   {}
func (fakeLogger) Info(string, ...any)    {}

func plant() {
	var l fakeLogger
	l.Warning("WARNING: planted word restatement")
	l.Info("✓ planted glyph restatement")
	l.Error("clean line, no violation")
}
`
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	found := scanForLevelRestatement(t, root)
	words, glyphs := 0, 0
	for _, v := range found {
		switch v.kind {
		case "word":
			words++
		case "glyph":
			glyphs++
		}
	}
	if words != 1 || glyphs != 1 || len(found) != 2 {
		t.Fatalf("planted 1 word + 1 glyph violation, scanner reported %d word(s) and %d glyph(s) in %d finding(s): the gate can no longer be trusted on the real tree\n%v", words, glyphs, len(found), found)
	}
}
