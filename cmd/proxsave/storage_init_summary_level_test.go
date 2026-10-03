package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestStorageInitSummaryOnlyTheOutcomeTakesTheWarnLevel(t *testing.T) {
	// A GFS summary is indented fact lines closed by the outcome. Only the outcome, the
	// last line, takes the warn level; the estimate line stays at Debug so it never
	// reaches the footer, wherever it sits.
	summary := "  Backups: 2\n  Daily: 1/1\n  Kept (est.): 1, To delete (est.): 1\nLocal storage: initialized"
	out := renderInitSummary(t, summary, true)

	var levels []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		levels = append(levels, levelColumnOf(line))
	}
	want := []string{"INFO", "INFO", "DEBUG", "WARNING"}
	if len(levels) != len(want) {
		t.Fatalf("rendered %d lines, want %d:\n%s", len(levels), len(want), out)
	}
	for i := range want {
		if levels[i] != want[i] {
			t.Fatalf("line %d rendered at %s, want %s:\n%s", i+1, levels[i], want[i], out)
		}
	}
}

// The cloud-disabled path builds its own block instead of borrowing
// formatStorageInitSummary, so the level of its outcome is fixed at the call site and
// nothing else pins it. A mutation turning it into Info left every test green, which
// means the line could silently become INFO and leave warningCount.
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
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	outcome, cause, skip := -1, -1, -1
	for i, line := range lines {
		switch {
		case strings.Contains(line, "✗ Cloud storage: not initialized"):
			outcome = i
		case strings.Contains(line, "INFO       rclone: not found in PATH"):
			cause = i
		case strings.Contains(line, "Path Cloud: disabled"):
			skip = i
		}
	}
	if outcome < 0 {
		t.Fatalf("the disabled path wrote no outcome at all:\n%s", out)
	}
	if got := levelColumnOf(lines[outcome]); got != "WARNING" {
		t.Fatalf("the cloud-unavailable outcome rendered at %s, so it never reaches warningCount:\n%s", got, lines[outcome])
	}
	// The cause is a fact line BEFORE the outcome, never appended to it, and the SKIP
	// closes the block after it.
	if cause < 0 || cause > outcome {
		t.Fatalf("the cause line is missing or follows the outcome:\n%s", out)
	}
	if got := levelColumnOf(lines[cause]); got != "INFO" {
		t.Fatalf("the cause line rendered at %s, want INFO:\n%s", got, lines[cause])
	}
	if skip < outcome || levelColumnOf(lines[skip]) != "SKIP" {
		t.Fatalf("the SKIP line is missing or precedes the outcome:\n%s", out)
	}
	// The path is read first, then checked: the path and its configuration open the
	// block, the check follows with what it found under it. No filesystem line: the
	// check never reached one.
	requireOrder(t, out,
		"INFO     Path Cloud: remote\n",
		"INFO       Retention policy: simple (keep 0 newest)\n",
		"INFO     Checking cloud remote accessibility...\n",
		"INFO       rclone: not found in PATH\n",
		"WARNING  ✗ Cloud storage: not initialized\n",
		"SKIP     Path Cloud: disabled\n",
	)
	for _, gone := range []string{"Filesystem:", "install rclone", "setup - rclone"} {
		if strings.Contains(out, gone) {
			t.Fatalf("the cloud-unavailable block still carries %q:\n%s", gone, out)
		}
	}
	if cfg.CloudEnabled || cfg.CloudLogPath != "" {
		t.Fatalf("the unavailable cloud must be disabled for the run: enabled=%v logPath=%q", cfg.CloudEnabled, cfg.CloudLogPath)
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
	// Without stats there are no tiers to count: the GFS block carries the unknown
	// count and the outcome, and no policy line anywhere (the limits are in DEBUG).
	if want := "  Backups: unknown, statistics unavailable\n⚠ Local storage: initialized, statistics unavailable"; summary != want {
		t.Fatalf("GFS summary without stats = %q, want %q", summary, want)
	}
	if got := formatRetentionPolicyLine(cfg, storage.LocationPrimary); got != "" {
		t.Fatalf("GFS policy line = %q, want none", got)
	}
}

// A GFS block opens on the path alone: no "  Retention policy:" line at INFO, the
// configured limits at DEBUG. The simple policy keeps its line under the path.
func TestStoragePathGFSPolicyGoesToDebug(t *testing.T) {
	render := func(cfg *config.Config) string {
		logger := logging.New(types.LogLevelDebug, false)
		buf := &bytes.Buffer{}
		logger.SetOutput(buf)
		prev := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(prev) })
		logging.SetDefaultLogger(logger)
		logStoragePath("Primary", "/backup", cfg, storage.LocationPrimary)
		return buf.String()
	}

	out := render(&config.Config{RetentionPolicy: "gfs", RetentionDaily: 2, RetentionWeekly: 1})
	if strings.Contains(out, "Retention policy:") {
		t.Fatalf("a GFS block must carry no policy line:\n%s", out)
	}
	requireOrder(t, out,
		"INFO     Path Primary: /backup\n",
		"DEBUG    storage init: primary retention policy=gfs daily=2 weekly=1 monthly=0 yearly=0\n",
	)

	out = render(&config.Config{RetentionPolicy: "simple", LocalRetentionDays: 7})
	requireOrder(t, out,
		"INFO     Path Primary: /backup\n",
		"INFO       Retention policy: simple (keep 7 newest)\n",
	)
	if strings.Contains(out, "retention policy=gfs") {
		t.Fatalf("a simple block has no GFS debug line:\n%s", out)
	}
}

// requireOrder fails unless every want appears in out, in that order.
func requireOrder(t *testing.T, out string, want ...string) {
	t.Helper()
	last := -1
	for _, w := range want {
		idx := strings.Index(out, w)
		if idx < 0 || idx < last {
			t.Fatalf("%q is missing or out of order in:\n%s", w, out)
		}
		last = idx
	}
}

// The filesystem facts: a filesystem without ownership says the permissions are
// skipped (the Primary and the Secondary; a cloud remote never takes ownership), and a
// detection that failed says so and makes the outcome name it.
func TestStorageFilesystemFacts(t *testing.T) {
	render := func(info *storage.FilesystemInfo, detectErr error) (string, string) {
		logger := logging.New(types.LogLevelInfo, false)
		buf := &bytes.Buffer{}
		logger.SetOutput(buf)
		prev := logging.GetDefaultLogger()
		t.Cleanup(func() { logging.SetDefaultLogger(prev) })
		logging.SetDefaultLogger(logger)
		problem := logStorageFilesystem(info, detectErr)
		return buf.String(), problem
	}

	out, problem := render(&storage.FilesystemInfo{Type: storage.FilesystemFAT32, MountPoint: "/mnt/usb"}, nil)
	requireOrder(t, out, "INFO       Filesystem: vfat (no ownership) [mount: /mnt/usb]\n", "INFO       Permissions: skipped, no ownership\n")
	if problem != "" {
		t.Fatalf("no ownership is not a problem for the outcome, got %q", problem)
	}

	out, _ = render(&storage.FilesystemInfo{Type: "rclone-remote", IsNetworkFS: true, MountPoint: "remote:", Device: "cloud"}, nil)
	if strings.Contains(out, "Permissions:") {
		t.Fatalf("a cloud remote has no permissions line:\n%s", out)
	}

	out, problem = render(&storage.FilesystemInfo{Type: storage.FilesystemUnknown}, errors.New("statfs failed"))
	requireOrder(t, out, "INFO       Filesystem: unknown, detection failed: statfs failed\n", "INFO       Permissions: skipped, filesystem unknown\n")
	if problem != "filesystem unknown" {
		t.Fatalf("problem = %q, want filesystem unknown", problem)
	}
}

// A secondary directory that cannot be created closes the block like an unreachable
// cloud: the path and its configuration, the cause, "✗ ... not initialized", the SKIP,
// and the destination is off for the run.
func TestSecondaryDirectoryNotCreatedDisablesIt(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	logger := logging.New(types.LogLevelInfo, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	prev := logging.GetDefaultLogger()
	t.Cleanup(func() { logging.SetDefaultLogger(prev) })
	logging.SetDefaultLogger(logger)

	path := filepath.Join(blocker, "secondary")
	cfg := &config.Config{SecondaryEnabled: true, SecondaryPath: path, SecondaryLogPath: "/logs", SecondaryRetentionDays: 5}
	initializeSecondaryStorage(backupModeOptions{ctx: context.Background(), cfg: cfg, logger: logger, hostname: "node"}, nil, nil)

	out := buf.String()
	requireOrder(t, out,
		"INFO     Path Secondary: "+path+"\n",
		"INFO       Retention policy: simple (keep 5 newest)\n",
		"INFO       Directory not created: not a directory\n",
		"WARNING  ✗ Secondary storage: not initialized\n",
		"SKIP     Path Secondary: disabled\n",
	)
	if strings.Contains(out, "Filesystem:") || strings.Count(out, "WARNING") != 1 {
		t.Fatalf("one outcome, no filesystem line:\n%s", out)
	}
	if cfg.SecondaryEnabled || cfg.SecondaryLogPath != "" {
		t.Fatalf("the secondary must be off for the run: enabled=%v logPath=%q", cfg.SecondaryEnabled, cfg.SecondaryLogPath)
	}
}
