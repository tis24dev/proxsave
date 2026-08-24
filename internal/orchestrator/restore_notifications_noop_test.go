package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The whole point of returning `applied` is what the operator reads afterwards. This drives
// the real entry point, with an empty stage, and asserts the message: before this change the
// same run logged "PBS notifications applied via API".
func TestMaybeApplyNotificationsFromStage_EmptyStageDoesNotClaimSuccess(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	origEuid := notificationsApplyGeteuid
	origAPIEuid := pbsAPIApplyGeteuid
	origVerify := serviceVerifyTimeout
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
		notificationsApplyGeteuid = origEuid
		pbsAPIApplyGeteuid = origAPIEuid
		serviceVerifyTimeout = origVerify
	})

	// maybeApplyNotificationsFromStage and ensurePBSServicesForAPI both refuse to run
	// against a non-system filesystem, so this exercises the real osFS against a temp dir.
	// Nothing on the path writes: an empty stage returns before any apply.
	restoreFS = osFS{}
	notificationsApplyGeteuid = func() int { return 0 }
	pbsAPIApplyGeteuid = func() int { return 0 }
	serviceVerifyTimeout = 50 * time.Millisecond

	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"proxmox-backup-manager version":           []byte("proxmox-backup-manager 4.0.0"),
			"systemctl start proxmox-backup":           {},
			"systemctl start proxmox-backup-proxy":     {},
			"systemctl is-active proxmox-backup":       []byte("active"),
			"systemctl is-active proxmox-backup-proxy": []byte("active"),
		},
	}

	logger := logging.New(types.LogLevelDebug, false)
	var out bytes.Buffer
	logger.SwapOutput(&out)

	plan := &RestorePlan{
		SystemType:         SystemTypePBS,
		StagedCategories:   []Category{{ID: "pbs_notifications"}},
		PBSRestoreBehavior: PBSRestoreBehaviorMerge,
	}

	if err := maybeApplyNotificationsFromStage(context.Background(), logger, plan, t.TempDir(), false); err != nil {
		t.Fatalf("an empty stage is not an error: %v", err)
	}

	logged := out.String()
	if !strings.Contains(logged, "this backup contains no notifications.cfg") {
		t.Fatalf("the operator must be told nothing was applied, got:\n%s", logged)
	}
	if strings.Contains(logged, "PBS notifications applied via API") {
		t.Fatalf("nothing was applied, so success must not be claimed, got:\n%s", logged)
	}
}
