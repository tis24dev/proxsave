package backup

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectorCollectAll_UnknownContinuesOnSystemInfoError(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = t.TempDir()

	deps := defaultCollectorDeps()
	deps.LookPath = func(string) (string, error) {
		return "", errors.New("missing")
	}

	collector := NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxUnknown, false, deps)
	if err := collector.CollectAll(context.Background()); err != nil {
		t.Fatalf("CollectAll returned error: %v", err)
	}

	stats := collector.GetStats()
	if stats.FilesFailed == 0 {
		t.Fatalf("expected FilesFailed to be > 0 (system command collection should fail fast)")
	}
}
