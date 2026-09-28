package backup

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

const pveRuntimeSkipLine = "PVE runtime commands - skipped under SYSTEM_ROOT_PREFIX, they would describe this system, not the host"

var pveRuntimeCommandBricks = []BrickID{
	brickPVERuntimeCore,
	brickPVERuntimeACL,
	brickPVERuntimeCluster,
	brickPVERuntimeStorage,
	brickPVEGuestInventory,
	brickPVEBackupJobDefs,
	brickPVEBackupJobHistory,
	brickPVEReplicationDefs,
	brickPVEReplicationStatus,
	brickPVEScheduleCrontab,
	brickPVEScheduleTimers,
	brickPVEVersionInfo,
}

// newCommandRecordingCollector returns a PVE collector whose every command is recorded
// instead of run. installed decides whether LookPath finds the commands: a stock Debian
// appliance has none of the PVE CLI, one with the PVE packages has all of it.
func newCommandRecordingCollector(t *testing.T, prefix string, installed bool) (*Collector, *[]string, *bytes.Buffer) {
	t.Helper()
	var ran []string
	record := func(name string) ([]byte, error) {
		ran = append(ran, name)
		return []byte("[]"), nil
	}
	cfg := GetDefaultCollectorConfig()
	cfg.SystemRootPrefix = prefix
	logger := logging.New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	c := NewCollector(logger, cfg, t.TempDir(), types.ProxmoxVE, false)
	c.deps.LookPath = func(name string) (string, error) {
		if installed {
			return "/usr/bin/" + name, nil
		}
		return "", fmt.Errorf("%s not installed", name)
	}
	c.deps.RunCommand = func(_ context.Context, name string, _ ...string) ([]byte, error) { return record(name) }
	c.deps.RunCommandWithEnv = func(_ context.Context, _ []string, name string, _ ...string) ([]byte, error) {
		return record(name)
	}
	c.deps.RunCommandCaptured = func(_ context.Context, _ []string, name string, _ ...string) ([]byte, []byte, error) {
		out, err := record(name)
		return out, nil, err
	}
	return c, &ran, &buf
}

func runPVERuntimeCommandBricks(t *testing.T, c *Collector) {
	t.Helper()
	runSelectedBricksForTest(t, context.Background(), c, newPVERecipe(),
		func(s *collectionState) { s.pve.clustered = true },
		pveRuntimeCommandBricks...,
	)
}

// Under SYSTEM_ROOT_PREFIX the PVE CLI, crontab and systemctl run in this system, not in
// the host under the prefix: measured on a Debian 13 appliance, the backup held the
// container's timers and no root crontab. They are skipped, with one line for the run.
func TestPVERuntimeCommandsSkippedUnderPrefix(t *testing.T) {
	c, ran, buf := newCommandRecordingCollector(t, t.TempDir(), true)
	runPVERuntimeCommandBricks(t, c)

	if len(*ran) != 0 {
		t.Fatalf("commands ran under the prefix: %v", *ran)
	}
	if got := strings.Count(buf.String(), pveRuntimeSkipLine); got != 1 {
		t.Fatalf("want the skip line once, got %d:\n%s", got, buf.String())
	}
}

// A stock appliance has no pveversion: under a prefix that used to end the run at exit 9
// with no archive, because pveversion stayed critical there.
func TestPVERuntimeWithoutPveversionUnderPrefixDoesNotAbort(t *testing.T) {
	c, ran, _ := newCommandRecordingCollector(t, t.TempDir(), false)
	runPVERuntimeCommandBricks(t, c)

	if len(*ran) != 0 {
		t.Fatalf("commands ran under the prefix: %v", *ran)
	}
}

// Without a prefix nothing changes: the commands run and no skip line is written.
func TestPVERuntimeCommandsRunWithoutPrefix(t *testing.T) {
	c, ran, buf := newCommandRecordingCollector(t, "", true)
	runPVERuntimeCommandBricks(t, c)

	for _, want := range []string{"pveversion", "pvesh", "pveum", "pvecm", "pvesm", "pvenode", "crontab", "systemctl"} {
		if !containsString(*ran, want) {
			t.Errorf("%s did not run without a prefix (ran %v)", want, *ran)
		}
	}
	if strings.Contains(buf.String(), pveRuntimeSkipLine) {
		t.Fatalf("skip line written without a prefix:\n%s", buf.String())
	}
}
