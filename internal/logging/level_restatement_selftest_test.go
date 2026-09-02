package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The gate reads its rules from package variables and walks the tree itself.
// Emptying severityGlyphs once made it PASS over 58 real glyph sites; a SkipDir
// planted in the walk silenced all 102 findings while the first version of this
// self-test stayed green, because its fixture sat at the temp ROOT and never
// exercised recursion. So: the fixture lives two directories deep, and the
// assertion is the exact SET of findings, not a count a compensating regression
// could keep constant.
func TestScannerStillSeesAPlantedViolationOfEachKind(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "internal", "fixture")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package fixture

type fakeLogger struct{}

func (fakeLogger) Warning(string, ...any)     {}
func (fakeLogger) Error(string, ...any)       {}
func (fakeLogger) Info(string, ...any)        {}
func (fakeLogger) Debug(string, ...any)       {}
func (fakeLogger) Critical(string, ...any)    {}
func (fakeLogger) NotifyError(string, ...any) {}
func (fakeLogger) Skip(string, ...any)        {}
func (fakeLogger) Fatal(int, string, ...any)  {}

func logBootstrapWarning(l fakeLogger, format string, args ...any)          {}
func logWarning(l fakeLogger, format string, args ...any)                   {}
func logDebug(l fakeLogger, format string, args ...any)                     {}
func logTelegramRegistrationDebug(l fakeLogger, format string, args ...any) {}

func plant() {
	var l fakeLogger
	l.Warning("WARNING: planted word restatement")
	l.Info("✓ planted glyph restatement")
	l.Error("clean line, no violation")

	// The five shapes the phase-one audit planted unseen, plus the wrappers:
	l.Warning("Warning - dash spelling")
	l.Error("ERROR opening the archive, no punctuation")
	l.Critical("CRITICAL: method was not in the map")
	l.Debug("WARNING: level the column contradicts")
	l.Warning("%s", "ERROR: literal in argument 1")
	logBootstrapWarning(l, "WARNING: planted through the wrapper")

	// The second audit's holes:
	l.NotifyError("error: through NotifyError")
	l.Skip("info: through Skip")
	l.Warning("warn: the short spelling")
	l.Warning("WARNING: ⚠ word and glyph in one literal")
	l.Fatal(4, "CRITICAL: through Fatal, format at argument 1")
	logWarning(l, "WARNING: through the identity-shaped wrapper")
	logDebug(l, "✓ glyph through the debug wrapper")
	logTelegramRegistrationDebug(l, "WARNING: through the telegram wrapper")
}
`
	if err := os.WriteFile(filepath.Join(nested, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	found := scanForLevelRestatement(t, root)
	got := make([]string, 0, len(found))
	for _, v := range found {
		if !strings.HasSuffix(v.file, "internal/fixture/fixture.go") {
			t.Fatalf("finding outside the nested fixture: %v", v)
		}
		got = append(got, fmt.Sprintf("%s|%s|%.30s", v.kind, v.wordLevel, v.literal))
	}
	sort.Strings(got)
	want := []string{
		"glyph|DEBUG|✓ glyph through the debug wrap",
		"glyph|INFO|✓ planted glyph restatement",
		"word|CRITICAL|CRITICAL: method was not in th",
		"word|CRITICAL|CRITICAL: through Fatal, forma",
		"word|ERROR|ERROR opening the archive, no ",
		"word|ERROR|ERROR: literal in argument 1",
		"word|ERROR|error: through NotifyError",
		"word|INFO|info: through Skip",
		"word|WARNING|WARNING: level the column cont",
		"word|WARNING|WARNING: planted through the w",
		"word|WARNING|WARNING: planted word restatem",
		"word|WARNING|WARNING: through the identity-",
		"word|WARNING|WARNING: through the telegram ",
		"word|WARNING|WARNING: ⚠ word and glyph in o",
		"word|WARNING|Warning - dash spelling",
		"word|WARNING|warn: the short spelling",
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("planted %d violations, scanner reported %d: the gate can no longer be trusted on the real tree\ngot:  %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("finding %d differs:\n got  %q\n want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}

// moduleRoot is the other single point of failure the fixture cannot reach: the
// real gate scans whatever this returns. It must be the directory holding go.mod.
func TestModuleRootFindsTheModule(t *testing.T) {
	root := moduleRoot(t)
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("moduleRoot(%q) does not hold go.mod: %v", root, err)
	}
}
