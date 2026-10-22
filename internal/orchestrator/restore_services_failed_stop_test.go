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

// pbsServicesRunner answers systemctl for the two PBS units: stuck ones stay active
// through every stop attempt, the others are inactive once stopped. failStart lists
// the units whose start and restart fail.
func pbsServicesRunner(stuck []string, failStart []string) *FakeCommandRunner {
	cmd := &FakeCommandRunner{Outputs: map[string][]byte{"which systemctl": {}}, Errors: map[string]error{}}
	for _, svc := range []string{"proxmox-backup-proxy", "proxmox-backup"} {
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
	}
	for _, svc := range stuck {
		cmd.Outputs["systemctl is-active "+svc] = []byte("active\n")
		delete(cmd.Errors, "systemctl is-active "+svc)
	}
	for _, svc := range failStart {
		cmd.Errors["systemctl start "+svc] = errors.New(svc + " will not start")
		cmd.Errors["systemctl restart "+svc] = errors.New(svc + " will not start")
	}
	return cmd
}

func runStopPBSServices(t *testing.T, cmd *FakeCommandRunner) ([]string, string, error) {
	t.Helper()
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	restoreCmd = cmd
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	err := stopPBSServices(context.Background(), logger)
	return cmd.CallsList(), buf.String(), err
}

// proxmox-backup does not stop after the proxy did: its queued stop job is replaced
// by a start, the proxy is started again, and the error names proxmox-backup only.
func TestStopPBSServicesRestartsTheProxyWhenTheBackupServiceDoesNotStop(t *testing.T) {
	calls, log, err := runStopPBSServices(t, pbsServicesRunner([]string{"proxmox-backup"}, nil))
	if err == nil || !strings.HasPrefix(err.Error(), "proxmox-backup: ") {
		t.Fatalf("err = %v, want the proxmox-backup stop error", err)
	}
	assertCallsInOrder(t, calls,
		"systemctl stop --no-block proxmox-backup-proxy",
		"systemctl stop --no-block proxmox-backup",
		"systemctl kill --signal=SIGKILL --kill-who=all proxmox-backup",
		"systemctl start proxmox-backup",
		"systemctl start proxmox-backup-proxy",
	)
	if strings.Contains(log, "WARNING") {
		t.Fatalf("no warning expected when both units run again:\n%s", log)
	}
}

// The proxy does not stop: proxmox-backup is not stopped at all, and the proxy's
// queued stop job is replaced by a start.
func TestStopPBSServicesStopsNothingElseWhenTheProxyDoesNotStop(t *testing.T) {
	calls, _, err := runStopPBSServices(t, pbsServicesRunner([]string{"proxmox-backup-proxy"}, nil))
	if err == nil || !strings.HasPrefix(err.Error(), "proxmox-backup-proxy: ") {
		t.Fatalf("err = %v, want the proxy stop error", err)
	}
	if countCalls(calls, "systemctl stop --no-block proxmox-backup") != 0 {
		t.Fatalf("proxmox-backup stopped after the proxy stop failed; calls=%q", calls)
	}
	if countCalls(calls, "systemctl start proxmox-backup-proxy") != 1 {
		t.Fatalf("want one start of the proxy; calls=%q", calls)
	}
}

// A unit that cannot be started again gets the warning, with its name.
func TestStopPBSServicesWarnsWhenAUnitDoesNotStartAgain(t *testing.T) {
	cases := []struct {
		name      string
		failStart string
	}{
		{"the stopped proxy", "proxmox-backup-proxy"},
		{"the unit that did not stop", "proxmox-backup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, log, _ := runStopPBSServices(t, pbsServicesRunner([]string{"proxmox-backup"}, []string{tc.failStart}))
			want := "WARNING  Failed to restart PBS services (" + tc.failStart + ") after the stop failed: "
			if !strings.Contains(log, want) {
				t.Fatalf("missing %q in log:\n%s", want, log)
			}
		})
	}
}

// A PVE unit other than pve-ha-lrm that does not stop gets a start that replaces its
// queued stop job, before the units already stopped are started again; when that
// start fails, the warning names it.
func TestStopPVEClusterServicesStartsTheUnitThatDidNotStop(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	cmd := inactiveServicesRunner()
	cmd.Outputs["systemctl is-active pvedaemon"] = []byte("active\n")
	delete(cmd.Errors, "systemctl is-active pvedaemon")
	cmd.Errors["systemctl start pvedaemon"] = errors.New("pvedaemon will not start")
	restoreCmd = cmd

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	if err := stopPVEClusterServices(context.Background(), logger); err == nil {
		t.Fatal("want the pvedaemon stop error")
	}
	assertCallsInOrder(t, cmd.CallsList(),
		"systemctl stop --no-block pvedaemon",
		"systemctl start pvedaemon",
		"systemctl start pve-cluster",
	)
	if !strings.Contains(buf.String(), "WARNING  Failed to restart PVE services (pvedaemon) after the stop failed: ") {
		t.Fatalf("missing the pvedaemon warning:\n%s", buf.String())
	}
}
