package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestRestoreExtractionLogWriteSummaryIncludesFailedFiles(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "restore.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}

	extractionLog := &restoreExtractionLog{
		logger:  newTestLogger(),
		logFile: logFile,
	}
	extractionLog.writeSummary(restoreExtractionStats{
		filesExtracted: 2,
		filesSkipped:   3,
		filesFailed:    4,
	})
	if err := logFile.Close(); err != nil {
		t.Fatalf("close log file: %v", err)
	}

	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "Total files failed: 4") {
		t.Fatalf("summary missing failed count:\n%s", text)
	}
	if !strings.Contains(text, "Total files in archive: 9") {
		t.Fatalf("summary total should include failed files:\n%s", text)
	}
}

func TestLogRestoreExtractionSummaryOmitsDetailedLogHintWithoutLogPath(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)

	logRestoreExtractionSummary(restoreArchiveOptions{logger: logger}, restoreExtractionStats{
		filesExtracted: 2,
		filesSkipped:   1,
		filesFailed:    1,
	})

	output := buf.String()
	if strings.Contains(output, "see detailed log") {
		t.Fatalf("did not expect detailed log hint without log path:\n%s", output)
	}
	if !strings.Contains(output, "1 item(s) failed") {
		t.Fatalf("expected failed count in summary:\n%s", output)
	}
}
