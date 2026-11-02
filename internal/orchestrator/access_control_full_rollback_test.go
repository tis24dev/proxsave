package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A PVE rollback armed on the full safety backup re-extracts the PBS files too: the
// PBS access control is not applied then, and the log says so.
func TestPBSAccessControlNotAppliedUnderAFullSafetyBackupRollback(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypeDual, ClusterBackup: true,
		NormalCategories: []Category{{ID: "pve_access_control"}, {ID: "pbs_access_control"}}}
	pveErr := &AccessControlApplyNotCommittedError{RollbackArmed: true, FullSafetyBackup: true}

	err := applyPBSAccessControlAfterPVEError(context.Background(), logger, plan, "/stage", false, pveErr)
	if !errors.Is(err, ErrAccessControlApplyNotCommitted) {
		t.Fatalf("err = %v, want the not-committed error", err)
	}
	out := buf.String()
	if !strings.Contains(out, "PBS access control: not applied; the armed rollback restores the full safety backup") {
		t.Fatalf("missing the not-applied line:\n%s", out)
	}
	if strings.Contains(out, "Skipping staged access control apply") {
		t.Fatalf("the PBS apply was attempted:\n%s", out)
	}
}
