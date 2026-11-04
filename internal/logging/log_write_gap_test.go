package logging

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

// A log-file write error other than a timeout (disk full, EIO, read-only) was dropped
// without a word: the run log lost its WARNING and ERROR lines and nothing said so. The
// first lost line now prints a WARNING on stdout (counted), and the first line that
// lands afterwards is preceded by a marker naming how many are missing.
func TestLogFileWriteErrorsAreMarked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	var out bytes.Buffer
	logger := New(types.LogLevelDebug, false)
	logger.SetOutput(&out)
	if err := logger.OpenLogFile(path); err != nil {
		t.Fatal(err)
	}
	failing := 2
	prev := fileWrite
	fileWrite = func(w io.Writer, s string) (int, error) {
		if failing > 0 {
			failing--
			return 0, syscall.ENOSPC
		}
		return prev(w, s)
	}
	t.Cleanup(func() { fileWrite = prev })

	logger.Warning("first lost")
	logger.Error("second lost")
	logger.Info("lands")
	_ = logger.CloseLogFile()

	stdout := out.String()
	if n := strings.Count(stdout, "log file write failed (no space left on device); continuing on stdout, lines are missing from the on-disk log"); n != 1 {
		t.Fatalf("stdout WARNING count = %d, want 1; stdout:\n%s", n, stdout)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	file := string(data)
	marker := "log file writes failed (no space left on device); 2 lines missing from the on-disk log here"
	if !strings.Contains(file, marker) || strings.Index(file, marker) > strings.Index(file, "lands") {
		t.Fatalf("file lacks the marker before the next line; file:\n%s", file)
	}
	if !logger.FileIncomplete() || logger.WarningCount() != 2 || logger.ErrorCount() != 1 {
		t.Fatalf("incomplete=%v warnings=%d errors=%d, want true 2 1", logger.FileIncomplete(), logger.WarningCount(), logger.ErrorCount())
	}
}
