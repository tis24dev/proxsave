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

func (fakeLogger) Warning(string, ...any)  {}
func (fakeLogger) Error(string, ...any)    {}
func (fakeLogger) Info(string, ...any)     {}
func (fakeLogger) Debug(string, ...any)    {}
func (fakeLogger) Critical(string, ...any) {}

func logBootstrapWarning(l fakeLogger, format string, args ...any) {}

func plant() {
	var l fakeLogger
	l.Warning("WARNING: planted word restatement")
	l.Info("✓ planted glyph restatement")
	l.Error("clean line, no violation")

	// The five shapes the phase-one audit planted unseen, plus the wrapper:
	l.Warning("Warning - dash spelling")
	l.Error("ERROR opening the archive, no punctuation")
	l.Critical("CRITICAL: method was not in the map")
	l.Debug("WARNING: level the column contradicts")
	l.Warning("%s", "ERROR: literal in argument 1")
	logBootstrapWarning(l, "WARNING: planted through the wrapper")
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
	if words != 7 || glyphs != 1 || len(found) != 8 {
		t.Fatalf("planted 7 word + 1 glyph violations, scanner reported %d word(s) and %d glyph(s) in %d finding(s): the gate can no longer be trusted on the real tree\n%v", words, glyphs, len(found), found)
	}
}
