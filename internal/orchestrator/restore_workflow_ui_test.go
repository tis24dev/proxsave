package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestNormalizeRestoreWorkflowUIErrorWrappedEOFAbortsWithWarning(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(&buf)

	err := normalizeRestoreWorkflowUIError(context.Background(), logger, fmt.Errorf("prompt failed: %w", io.EOF))
	if err != ErrRestoreAborted {
		t.Fatalf("err=%v; want %v", err, ErrRestoreAborted)
	}
	if !strings.Contains(buf.String(), "Restore input closed unexpectedly (EOF).") {
		t.Fatalf("expected EOF warning, got log output: %q", buf.String())
	}
}
