package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// The level of a storage-init headline used to travel INSIDE the summary string, as a
// "⚠" that logStorageInitSummary read back with strings.HasPrefix to choose between
// logging.Warning and logging.Info. That made a glyph that reads as decoration
// load-bearing: deleting it downgraded the line to INFO in silence, which drops it from
// warningCount and stops it promoting an otherwise clean run to exit 1
// (internal/orchestrator/extensions.go applyIssueExitCode).
//
// These tests pin the replacement: the level is a value the formatter returns and the
// logger is told, and the string carries no severity of its own.
// levelColumnOf returns the level token a rendered line carries in its COLUMN, which
// is the only place the logger writes it. Matching "WARNING" anywhere in the line is
// not the same test: a message body can carry the word on its own - a wrapped error, a
// quoted command, a path - so a line demoted to INFO still contains it. A first version
// of these assertions did exactly that and passed under the mutation it was written to
// catch. It was a StorageError doing it at the time; that one no longer says it, but the
// reason the assertion reads the column has not changed.
func levelColumnOf(line string) string {
	_, rest, found := strings.Cut(line, "] ")
	if !found {
		return ""
	}
	return strings.Fields(rest)[0]
}

func renderInitSummary(t *testing.T, summary string, warn bool) string {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)
	logStorageInitSummary(summary, warn)
	return buf.String()
}

func TestStorageInitSummaryReportsItsOwnLevel(t *testing.T) {
	cfg := &config.Config{LocalRetentionDays: 7, RetentionPolicy: "simple"}

	summary, warn := formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, nil, nil)
	if !warn {
		t.Fatalf("a summary built without stats must report warn=true, got false: %s", summary)
	}

	okSummary, okWarn := formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, &storage.StorageStats{TotalBackups: 2}, nil)
	if okWarn {
		t.Fatalf("a summary built WITH stats must report warn=false, got true: %s", okSummary)
	}
}

// The glyphs stay on the line; what must not come back is the COUPLING. These two
// render a string whose glyph says one thing and whose flag says the other, and the
// column has to follow the flag.
func TestStorageInitSummaryLevelIsNotReadBackFromTheGlyph(t *testing.T) {
	warned := strings.TrimRight(renderInitSummary(t, "⚠ Local storage initialized with warnings (unable to gather stats)", false), "\n")
	if got := levelColumnOf(warned); got != "INFO" {
		t.Fatalf("a ⚠ in the text pulled the column to %s: the glyph is being read back:\n%s", got, warned)
	}

	plain := strings.TrimRight(renderInitSummary(t, "✓ Local storage initialized (present 2 backups)", true), "\n")
	if got := levelColumnOf(plain); got != "WARNING" {
		t.Fatalf("a ✓ in the text pushed the column to %s: the glyph is being read back:\n%s", got, plain)
	}
}

func TestStorageInitSummaryLevelComesFromTheFlagNotTheText(t *testing.T) {
	// Same text, both levels: proves the logger reads the flag and nothing else.
	const text = "Local storage initialized (present 2 backups)"

	warned := strings.TrimRight(renderInitSummary(t, text, true), "\n")
	if got := levelColumnOf(warned); got != "WARNING" {
		t.Fatalf("warn=true rendered level column %q, want WARNING:\n%s", got, warned)
	}

	plain := strings.TrimRight(renderInitSummary(t, text, false), "\n")
	if got := levelColumnOf(plain); got != "INFO" {
		t.Fatalf("warn=false rendered level column %q, want INFO:\n%s", got, plain)
	}
}

func TestStorageInitSummaryDetailLinesStayBelowTheHeadline(t *testing.T) {
	// A GFS summary is a headline plus indented detail. Only the headline takes the
	// warn level; the estimate line stays at Debug so it never reaches the footer.
	summary := "Local storage initialized (present 2 backups)\n  Daily: 1/1\n  Kept (est.): 1, To delete (est.): 1"
	out := renderInitSummary(t, summary, true)

	var levels []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		levels = append(levels, levelColumnOf(line))
	}
	want := []string{"WARNING", "INFO", "DEBUG"}
	if len(levels) != len(want) {
		t.Fatalf("rendered %d lines, want %d:\n%s", len(levels), len(want), out)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("line %d rendered at %s, want %s:\n%s", i+1, levels[i], want[i], out)
		}
	}
}

// The cloud-disabled path builds its own headline instead of borrowing
// formatStorageInitSummary, so its level is a literal true at the call site and nothing
// else pins it. A mutation flipping it to false left every test green, which means the
// line could silently become INFO and leave warningCount.
//
// The path is reached through a real DetectFilesystem, which looks rclone up on
// PATH. Pointing PATH at an empty directory forces the not-found arm on every
// host, so the test no longer skips exactly where the cloud backend actually
// runs - a skip the phase-one audit flagged as a hole.
func TestCloudUnavailableHeadlineIsAWarning(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)

	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote"}
	initializeCloudStorage(backupModeOptions{
		ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node",
	}, nil, nil)

	out := buf.String()
	var headline string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Cloud storage initialized with warnings") {
			headline = line
		}
	}
	if headline == "" {
		t.Fatalf("the disabled path wrote no headline at all:\n%s", out)
	}
	if got := levelColumnOf(headline); got != "WARNING" {
		t.Fatalf("the cloud-unavailable headline rendered at %s, so it never reaches warningCount:\n%s", got, headline)
	}
	// The headline this path borrows carries the ⚠ and the retention figure; both
	// vanished once when this call stopped borrowing it, and the maintainer reverted
	// that. The glyph is content here, not the level: the level is pinned above.
	if !strings.Contains(headline, "⚠") || !strings.Contains(headline, "retention") {
		t.Fatalf("the cloud-unavailable headline lost its ⚠ or its retention figure:\n%s", headline)
	}
	if !strings.Contains(headline, "; filesystem detection") {
		t.Fatalf("the cloud-unavailable headline no longer appends the cause:\n%s", headline)
	}
}

// The GFS branch has its own stats==nil return, and a mutation flipping only that one
// left every test green: the simple branch was the only one covered.
func TestGFSSummaryWithoutStatsIsAWarningToo(t *testing.T) {
	cfg := &config.Config{RetentionPolicy: "gfs", RetentionDaily: 2, RetentionWeekly: 1}

	summary, warn := formatStorageInitSummary("Local storage", cfg, storage.LocationPrimary, nil, nil)
	if !warn {
		t.Fatalf("a GFS summary built without stats must report warn=true, got false: %s", summary)
	}
	if !strings.Contains(summary, "GFS retention") {
		t.Fatalf("expected the GFS branch, got: %s", summary)
	}
}
