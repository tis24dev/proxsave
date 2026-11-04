package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The run log re-parse read lines through a bufio.Scanner capped at 1 MiB and never
// checked its error: a longer line stopped the parse, so it and every line after it went
// uncounted, and an ERROR logged after it left the exit code and the healthchecks ping
// at success. Every line is now read, of any length.
func TestParseLogCountsReadsPastALineLongerThanOneMiB(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		errors  int
		warns   int
	}{
		{"long line in the middle",
			"[2026-11-02 10:00:00] WARNING  first warning\n" +
				"[2026-11-02 10:00:01] WARNING  long " + strings.Repeat("x", 2<<20) + "\n" +
				"[2026-11-02 10:00:02] ERROR    after the long line\r\n" +
				"[2026-11-02 10:00:03] WARNING  last line without a newline",
			1, 3},
		{"long line first",
			"[2026-11-02 10:00:00] WARNING  long " + strings.Repeat("x", 2<<20) + "\n" +
				"[2026-11-02 10:00:01] WARNING  after the long line\n",
			0, 2},
	} {
		logPath := filepath.Join(t.TempDir(), "backup.log")
		if err := os.WriteFile(logPath, []byte(tc.content), 0o600); err != nil {
			t.Fatal(err)
		}
		categories, errorCount, warningCount, _ := ParseLogCounts(logPath, 0)
		if errorCount != tc.errors || warningCount != tc.warns {
			t.Fatalf("%s: errors=%d warnings=%d, want errors=%d warnings=%d (categories %+v)",
				tc.name, errorCount, warningCount, tc.errors, tc.warns, categories)
		}
	}
}
