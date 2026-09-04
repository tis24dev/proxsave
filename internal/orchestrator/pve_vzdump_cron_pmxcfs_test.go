package orchestrator

import (
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// vzdump.cron is collected under pve_jobs (categories.go) and RESTORE_GUIDE.md
// says it is staged-applied, but no restore path ever read it back: legacy cron
// backup jobs silently vanished from every staged restore (fable-check bug 5).
// There is no pvesh endpoint for it; the file lives in pmxcfs and the write IS
// the cluster-wide apply.
func TestApplyPVEVzdumpCronFromStageWritesThroughPmxcfs(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	seamPmxcfs(t, "/etc/pve", true, nil)

	const cron = "0 2 * * 6 root vzdump 101 --storage local --mode snapshot\n"
	if err := fakeFS.AddFile("/stage/etc/pve/vzdump.cron", []byte(cron)); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	if err := applyPVEVzdumpCronFromStage(logger, "/stage"); err != nil {
		t.Fatalf("applyPVEVzdumpCronFromStage: %v", err)
	}
	got, err := fakeFS.ReadFile("/etc/pve/vzdump.cron")
	if err != nil || string(got) != cron {
		t.Fatalf("vzdump.cron not written into pmxcfs: %q err=%v", got, err)
	}
}

func TestApplyPVEVzdumpCronFromStageSkipsAbsentAndEmpty(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	seamPmxcfs(t, "/etc/pve", true, nil)
	logger := logging.New(types.LogLevelDebug, false)

	if err := applyPVEVzdumpCronFromStage(logger, "/stage"); err != nil {
		t.Fatalf("absent vzdump.cron must be a silent skip: %v", err)
	}
	if err := fakeFS.AddFile("/stage/etc/pve/vzdump.cron", []byte(" \n\t")); err != nil {
		t.Fatal(err)
	}
	if err := applyPVEVzdumpCronFromStage(logger, "/stage"); err != nil {
		t.Fatalf("empty vzdump.cron must be a silent skip: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/vzdump.cron"); err == nil {
		t.Fatal("a skip must not write anything")
	}
}

func TestApplyPVEVzdumpCronFromStageRefusesWithoutPmxcfs(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	seamPmxcfs(t, "/etc/pve", false, nil)

	if err := fakeFS.AddFile("/stage/etc/pve/vzdump.cron", []byte("0 2 * * 6 root vzdump 101\n")); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	err := applyPVEVzdumpCronFromStage(logger, "/stage")
	if err == nil || !strings.Contains(err.Error(), "not mounted") {
		t.Fatalf("want the mounted-guard refusal, got %v", err)
	}
}

// The arm is worthless unwired: this pins that maybeApplyPVEConfigsFromStage
// runs it in the pve_jobs branch, beside jobs.cfg, gated off cluster RECOVERY
// like its sibling (config.db owns the file there).
func TestVzdumpCronArmIsWiredIntoTheStagedApply(t *testing.T) {
	data, err := os.ReadFile("pve_staged_apply.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	jobs := strings.Index(src, `if plan.HasCategoryID("pve_jobs")`)
	if jobs < 0 {
		t.Fatal("pve_jobs branch not found")
	}
	if !strings.Contains(src[jobs:], "applyPVEVzdumpCronFromStage(") {
		t.Fatal("applyPVEVzdumpCronFromStage is not wired into the pve_jobs branch: the arm is dead code")
	}
}
