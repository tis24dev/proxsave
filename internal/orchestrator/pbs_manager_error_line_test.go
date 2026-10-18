package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Refusals printed by proxmox-backup-manager 4.2.8 on VM 201 (2026-10-18), in the
// shape osCommandRunner.RunStdout returns them, wrapped as runPBSManager wraps
// them.
const (
	pbsRefusalMultiLine = "exit status 255: Error: parameter verification failed:\n- 'help': missing parameter value.\n- 'type': missing argument\n- 'id': missing argument\nUsage: proxmox-backup-manager acme plugin add <type> <id> --api <string> --data <string> [OPTIONS]"
	pbsRefusalOneItem   = "exit status 255: Error: parameter verification failed:\n- 'task-log-max-days': missing parameter value."
	pbsRefusalOneLine   = "exit status 255: Error: parameter verification failed - 'task-log-max-days': value must have a minimum value of 0 (got -5)\nUsage: proxmox-backup-manager node update [OPTIONS]"
)

func TestPBSManagerErrorLine_CarriesEveryRefusalLine(t *testing.T) {
	wrap := func(s string) error { return fmt.Errorf("proxmox-backup-manager x failed: %w", errors.New(s)) }
	for _, tc := range []struct{ name, in, want string }{
		{"multi-line", pbsRefusalMultiLine, "parameter verification failed: 'help': missing parameter value.; 'type': missing argument; 'id': missing argument"},
		{"one item", pbsRefusalOneItem, "parameter verification failed: 'task-log-max-days': missing parameter value."},
		{"one line", pbsRefusalOneLine, "parameter verification failed - 'task-log-max-days': value must have a minimum value of 0 (got -5)"},
		{"no Error line", "signal: killed", "signal: killed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pbsManagerErrorLine(wrap(tc.in)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A section object refused with several parameter errors carries all of them in
// its event.
func TestPBSSectionRestoreBlock_MultiLineRefusalEvent(t *testing.T) {
	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/prune.cfg", "prune: p1\n\tstore ds1\n", 0o640)
	runner.errs = map[string]error{
		"proxmox-backup-manager prune-job create p1 --store ds1": errors.New(pbsRefusalMultiLine),
		"proxmox-backup-manager prune-job update p1 --store ds1": errors.New(pbsRefusalMultiLine),
	}
	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)
	_ = applyPBSPruneCfgViaAPI(context.Background(), logger, stageRoot, false)
	want := `INFO     Prune job p1 was rejected by PBS: "parameter verification failed: 'help': missing parameter value.; 'type': missing argument; 'id': missing argument"`
	for _, line := range pbsVisibleLines(logBuf.String()) {
		if strings.Contains(line, "was rejected by PBS") {
			if line != want {
				t.Fatalf("event %q, want %q", line, want)
			}
			return
		}
	}
	t.Fatalf("no rejection event, log:\n%s", logBuf.String())
}
