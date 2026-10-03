package checks

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func newPBSDiskChecker(t *testing.T) (*Checker, *bytes.Buffer) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	out := &bytes.Buffer{}
	logger.SetOutput(out)
	tmpDir := t.TempDir()
	return NewChecker(logger, &CheckerConfig{
		BackupPath:       tmpDir,
		LogPath:          tmpDir,
		LockDirPath:      tmpDir,
		MinDiskPrimaryGB: 0.001,
		SafetyFactor:     1.5,
		MaxLockAge:       time.Minute,
	}), out
}

func visibleCheckLines(out *bytes.Buffer) []string {
	var lines []string
	for _, line := range strings.Split(out.String(), "\n") {
		if _, rest, ok := strings.Cut(line, "] "); ok && !strings.HasPrefix(rest, "DEBUG") {
			lines = append(lines, rest)
		}
	}
	return lines
}

func requireCheckLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The PBS storage is in both checks like a copy destination, with the free space asked
// to the server at the moment of each check.
func TestPBSDiskSpaceBlocks(t *testing.T) {
	checker, out := newPBSDiskChecker(t)
	asked := 0
	checker.SetPBS("pbs-main", 5, func() (float64, string) { asked++; return 2, "" })
	if result := checker.CheckDiskSpace(); !result.Passed || result.Message != "Primary disk space OK" {
		t.Fatalf("CheckDiskSpace = %+v", result)
	}
	requireCheckLines(t, visibleCheckLines(out),
		"INFO     PBS disk space: pbs-main",
		"INFO       Available: 2.00 GB",
		"INFO       Required: 5.00 GB",
		"WARNING  ⚠ PBS disk space: insufficient, upload may fail",
		"INFO     Disk space warnings: PBS",
	)

	out.Reset()
	if result := checker.CheckDiskSpaceForEstimate(3); !result.Passed {
		t.Fatalf("CheckDiskSpaceForEstimate = %+v", result)
	}
	requireCheckLines(t, visibleCheckLines(out),
		"INFO     PBS disk space: pbs-main",
		"INFO       Available: 2.00 GB",
		"INFO       Required: 5.00 GB",
		"INFO       Rule: larger of 5.00 GB minimum and 3.00 GB collected x 1.5",
		"WARNING  ⚠ PBS disk space: insufficient, upload may fail",
	)
	if asked != 2 {
		t.Fatalf("server asked %d times, want once per check", asked)
	}

	checker, out = newPBSDiskChecker(t)
	checker.SetPBS("pbs-main", 1, func() (float64, string) { return 0, "connection refused" })
	checker.CheckDiskSpace()
	requireCheckLines(t, visibleCheckLines(out),
		"INFO     PBS disk space: pbs-main",
		"INFO       PBS server: connection refused",
		"WARNING  ⚠ PBS disk space: not checked",
		"INFO     Disk space warnings: PBS",
	)

	checker, out = newPBSDiskChecker(t)
	checker.SetPBS("pbs-main", 1, func() (float64, string) { return 50, "" })
	checker.CheckDiskSpace()
	checker.CheckDiskSpaceForEstimate(1)
	if got := visibleCheckLines(out); len(got) != 0 {
		t.Fatalf("enough space printed %q, want DEBUG only", got)
	}

	// Not initialized at startup: left out of both checks, DEBUG only.
	checker, out = newPBSDiskChecker(t)
	checker.SetPBS("pbs-main", 1, nil)
	checker.CheckDiskSpace()
	if got := visibleCheckLines(out); len(got) != 0 || !strings.Contains(out.String(), "pbs disk space: skipped, storage not initialized") {
		t.Fatalf("not initialized: visible %q\n%s", got, out.String())
	}
}
