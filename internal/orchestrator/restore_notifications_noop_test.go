package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

// Merge mode has no file-based fallback (allowFileFallback is Clean-only), so an API failure
// there only logs and returns. That arm writes nothing, which is what makes it safe to drive
// against the real osFS the surrounding guards insist on.
func TestMaybeApplyNotificationsFromStage_MergeModeSkipsOnAPIFailure(t *testing.T) {
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

	restoreFS = osFS{}
	notificationsApplyGeteuid = func() int { return 0 }
	pbsAPIApplyGeteuid = func() int { return 0 }
	serviceVerifyTimeout = 50 * time.Millisecond

	stageRoot := t.TempDir()
	cfgDir := filepath.Join(stageRoot, "etc", "proxmox-backup")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir staged config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "notifications.cfg"), []byte(
		"matcher: default-matcher\n"+
			"    target mail-to-root\n",
	), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}

	boom := errors.New("proxmox-backup-manager exploded")
	restoreCmd = &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which systemctl":                          {},
			"proxmox-backup-manager version":           []byte("proxmox-backup-manager 4.0.0"),
			"systemctl start proxmox-backup":           {},
			"systemctl start proxmox-backup-proxy":     {},
			"systemctl is-active proxmox-backup":       []byte("active"),
			"systemctl is-active proxmox-backup-proxy": []byte("active"),
		},
		Errors: map[string]error{
			"proxmox-backup-manager notification matcher create default-matcher --target mail-to-root": boom,
			"proxmox-backup-manager notification matcher update default-matcher --target mail-to-root": boom,
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

	// Merge mode swallows the failure by design: the run continues, nothing is written.
	if err := maybeApplyNotificationsFromStage(context.Background(), logger, plan, stageRoot, false); err != nil {
		t.Fatalf("merge mode must not abort the restore: %v", err)
	}

	logged := out.String()
	if !strings.Contains(logged, "skipping apply (merge mode)") {
		t.Fatalf("the operator must be told the apply was skipped, got:\n%s", logged)
	}
	if strings.Contains(logged, "applied via API") || strings.Contains(logged, "contains no notifications.cfg") {
		t.Fatalf("a failed apply must not read as success or as an empty stage, got:\n%s", logged)
	}
}

// The success arm, for symmetry: with a real staged config and every command succeeding, the
// operator must still read the "applied via API" line. Asserting only the no-op line would
// leave nothing proving the change did not silence the success case too.
func TestMaybeApplyNotificationsFromStage_AppliedReportsSuccess(t *testing.T) {
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

	restoreFS = osFS{}
	notificationsApplyGeteuid = func() int { return 0 }
	pbsAPIApplyGeteuid = func() int { return 0 }
	serviceVerifyTimeout = 50 * time.Millisecond

	stageRoot := t.TempDir()
	cfgDir := filepath.Join(stageRoot, "etc", "proxmox-backup")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatalf("mkdir staged config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "notifications.cfg"), []byte(
		"matcher: default-matcher\n"+
			"    target mail-to-root\n",
	), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}

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

	if err := maybeApplyNotificationsFromStage(context.Background(), logger, plan, stageRoot, false); err != nil {
		t.Fatalf("a clean apply must not error: %v", err)
	}

	logged := out.String()
	if !strings.Contains(logged, "PBS notifications applied via API") {
		t.Fatalf("a real apply must still report success, got:\n%s", logged)
	}
	if strings.Contains(logged, "contains no notifications.cfg") {
		t.Fatalf("something was applied, so the no-op line must not appear, got:\n%s", logged)
	}
}
