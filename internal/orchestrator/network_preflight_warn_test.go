package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// When a staged apply has already written /etc but preflight fails and there is
// no network rollback backup, handlePreflightFailure must warn honestly that the
// managed network config under /etc may be written, then return the preflight error.
func TestHandlePreflightFailureWarnsWhenNoRollbackBackup(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	f := &networkRollbackUIApplyFlow{
		ctx:                 context.Background(),
		logger:              logger,
		stageRoot:           "/stage",
		networkRollbackPath: "", // no rollback backup captured
	}

	err := f.handlePreflightFailure(networkPreflightResult{})
	if err == nil {
		t.Fatal("expected a preflight-failure error")
	}
	if !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("error must report preflight failure, got: %v", err)
	}
	if !strings.Contains(buf.String(), "/etc") {
		t.Fatalf("expected an honest warning naming /etc left written, log was:\n%s", buf.String())
	}
}
