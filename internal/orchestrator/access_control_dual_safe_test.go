package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Dual host, cluster backup, SAFE, both access control categories. The PBS access
// control was not applied at all once the PVE one ended with an error, and the log
// said the PVE apply was skipped whatever the operator chose. The PBS one is applied
// anyway, and the SAFE warning is gone (the PVE step reports its own outcome).
func TestDualSafeAccessControlAppliesPBSAfterAPVEError(t *testing.T) {
	origFS, origReal, origEuid, origMounted := restoreFS, accessControlIsRealRestoreFS, accessControlApplyGeteuid, accessControlIsMounted
	t.Cleanup(func() {
		restoreFS, accessControlIsRealRestoreFS, accessControlApplyGeteuid, accessControlIsMounted = origFS, origReal, origEuid, origMounted
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	accessControlIsRealRestoreFS = func(FS) bool { return true }
	accessControlApplyGeteuid = func() int { return 0 }
	accessControlIsMounted = func(string) (bool, error) { return true, nil }
	if err := fakeFS.WriteFile("/stage/etc/pve/user.cfg", []byte("user:alice@pve:1:0::::::\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	uiErr := errors.New("ui broke")
	ui := &fakeRestoreWorkflowUI{confirmActionErr: uiErr}
	plan := &RestorePlan{SystemType: SystemTypeDual, ClusterBackup: true,
		NormalCategories: []Category{{ID: "pve_access_control"}, {ID: "pbs_access_control"}}}

	err := maybeApplyAccessControlWithUI(context.Background(), ui, logger, plan, nil, nil, "/stage", false)
	if !errors.Is(err, uiErr) {
		t.Fatalf("err = %v, want the PVE error returned", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Skipping staged access control apply: non-system filesystem in use") {
		t.Fatalf("the PBS access control apply was not attempted after the PVE error:\n%s", out)
	}
	if strings.Contains(out, "skipping 1:1 access control apply in SAFE mode") {
		t.Fatalf("the SAFE warning is still logged:\n%s", out)
	}
}
