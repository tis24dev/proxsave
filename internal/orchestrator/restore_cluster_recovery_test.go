package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// clusterRecoveryServices are the services a cluster RECOVERY stops, in stop order.
var clusterRecoveryServices = []string{"pve-ha-lrm", "pve-ha-crm", "pve-cluster", "pvedaemon", "pveproxy", "pvestatd"}

type clusterRecoveryOptions struct {
	live       map[string]string
	archive    map[string]string
	outputs    map[string]string
	errs       map[string]error
	categories []string
}

type clusterRecoveryRun struct {
	fs  *FakeFS
	cmd *FakeCommandRunner
	log string
	err error
}

// runClusterRecoveryRestore runs the real restore workflow on a PVE host, from a
// cluster archive holding config.db, with the operator choosing RECOVERY. The live
// config.db is "db-live", the archive's is "db-from-backup".
func runClusterRecoveryRestore(t *testing.T, opts clusterRecoveryOptions) clusterRecoveryRun {
	t.Helper()
	origRestoreFS, origRestoreCmd, origRestoreSystem := restoreFS, restoreCmd, restoreSystem
	origRestoreTime, origCompatFS, origPrepare := restoreTime, compatFS, prepareRestoreBundleFunc
	origSafetyFS, origSafetyNow := safetyFS, safetyNow
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem = origRestoreFS, origRestoreCmd, origRestoreSystem
		restoreTime, compatFS, prepareRestoreBundleFunc = origRestoreTime, origCompatFS, origPrepare
		safetyFS, safetyNow = origSafetyFS, origSafetyNow
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	fakeNow := &FakeTime{Current: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}
	restoreTime, safetyNow = fakeNow, fakeNow.Now
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}

	live := map[string]string{"/var/lib/pve-cluster/config.db": "db-live\n"}
	for path, content := range opts.live {
		live[path] = content
	}
	for path, content := range live {
		if err := fakeFS.AddFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{"umount /etc/pve": []byte("not mounted\n")},
		Errors:  map[string]error{"umount /etc/pve": errors.New("not mounted")},
	}
	for _, svc := range clusterRecoveryServices {
		cmd.Outputs["systemctl stop --no-block "+svc] = []byte("ok")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
		cmd.Outputs["systemctl reset-failed "+svc] = []byte("ok")
		cmd.Outputs["systemctl start "+svc] = []byte("ok")
	}
	for k, v := range opts.outputs {
		cmd.Outputs[k] = []byte(v)
	}
	for k, v := range opts.errs {
		cmd.Errors[k] = v
	}
	restoreCmd = cmd

	archive := map[string]string{"var/lib/pve-cluster/config.db": "db-from-backup\n"}
	for path, content := range opts.archive {
		archive[path] = content
	}
	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, archive); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	prepareRestoreBundleFunc = stubPreparedRestoreBundle("/bundle.tar", &backup.Manifest{
		CreatedAt:     fakeNow.Now(),
		ClusterMode:   "cluster",
		ProxmoxType:   "pve",
		ScriptVersion: "vtest",
	})

	categories := opts.categories
	if len(categories) == 0 {
		categories = []string{"pve_cluster"}
	}
	ui := &fakeRestoreWorkflowUI{
		mode:              RestoreModeCustom,
		confirmRestore:    true,
		confirmCompatible: true,
		clusterMode:       ClusterRestoreRecovery,
	}
	for _, id := range categories {
		ui.categories = append(ui.categories, mustCategoryByID(t, id))
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	runErr := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logger, "vtest", ui, "")
	return clusterRecoveryRun{fs: fakeFS, cmd: cmd, log: buf.String(), err: runErr}
}

func (r clusterRecoveryRun) configDB(t *testing.T) string {
	t.Helper()
	data, err := r.fs.ReadFile("/var/lib/pve-cluster/config.db")
	if err != nil {
		t.Fatalf("read config.db: %v", err)
	}
	return string(data)
}

func (r clusterRecoveryRun) count(call string) int {
	n := 0
	for _, c := range r.cmd.CallsList() {
		if c == call {
			n++
		}
	}
	return n
}

func (r clusterRecoveryRun) index(call string) int {
	for i, c := range r.cmd.CallsList() {
		if c == call {
			return i
		}
	}
	return -1
}

// assertCallsInOrder fails unless want appears in calls as a subsequence, in order.
func assertCallsInOrder(t *testing.T, calls []string, want ...string) {
	t.Helper()
	next := 0
	for _, w := range want {
		found := false
		for next < len(calls) {
			c := calls[next]
			next++
			if c == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("calls out of order: %q not found after the previous expected call; want order %q; calls=%q", w, want, calls)
		}
	}
}

// pvecmStatusOutput is what pvecm status prints on PVE 9 for a cluster with nodes
// members online out of expected.
func pvecmStatusOutput(nodes, expected int, quorate bool) string {
	quorateText := "No"
	flags := ""
	if quorate {
		quorateText = "Yes"
		flags = "Quorate"
	}
	return fmt.Sprintf(`Cluster information
-------------------
Name:             lab
Config Version:   3
Transport:        knet
Secure auth:      on

Quorum information
------------------
Date:             Sun Sep 27 10:00:00 2026
Quorum provider:  corosync_votequorum
Nodes:            %d
Node ID:          0x00000001
Ring ID:          1.2d
Quorate:          %s

Votequorum information
----------------------
Expected votes:   %d
Highest expected: %d
Total votes:      %d
Quorum:           %d
Flags:            %s
`, nodes, quorateText, expected, expected, nodes, expected/2+1, flags)
}

var corosyncLive = map[string]string{"/etc/pve/corosync.conf": "totem {\n  cluster_name: lab\n}\n"}

// On a member of a quorate cluster, the config.db RECOVERY writes is replaced by the
// leader's copy when pve-cluster starts again (measured on a 3-node PVE 9.2.2
// cluster), while the restore reported success. The quorum is probed right after the
// operator picks RECOVERY: a quorate cluster with other nodes online is refused before
// any service is stopped or file written; every other case proceeds.
func TestClusterRecoveryQuorumProbe(t *testing.T) {
	cases := []struct {
		name        string
		live        map[string]string
		outputs     map[string]string
		errs        map[string]error
		wantErr     string
		wantWarning string
		wantPvecm   bool
	}{
		{
			name:      "quorate cluster with 3 nodes online is refused",
			live:      corosyncLive,
			outputs:   map[string]string{"pvecm status": pvecmStatusOutput(3, 3, true)},
			wantErr:   "Cluster RECOVERY refused - quorate cluster, 3 nodes online: its copy would replace the restored config.db",
			wantPvecm: true,
		},
		{
			name:      "quorate cluster with 1 node online proceeds",
			live:      corosyncLive,
			outputs:   map[string]string{"pvecm status": pvecmStatusOutput(1, 1, true)},
			wantPvecm: true,
		},
		{
			name:      "cluster without quorum proceeds",
			live:      corosyncLive,
			outputs:   map[string]string{"pvecm status": pvecmStatusOutput(2, 5, false)},
			wantPvecm: true,
		},
		{
			name: "standalone node without corosync.conf proceeds silently",
		},
		{
			// The documented isolation: with corosync stopped pvecm cannot read CMAP.
			name: "quorum unknown with corosync stopped proceeds with a warning",
			live: corosyncLive,
			outputs: map[string]string{
				"pvecm status":                 "Cannot initialize CMAP service\n",
				"systemctl is-active corosync": "inactive\n",
			},
			errs: map[string]error{
				"pvecm status":                 errors.New("exit status 2"),
				"systemctl is-active corosync": errors.New("exit status 3"),
			},
			wantWarning: "Cluster RECOVERY - quorum unknown (could not determine quorum: Cannot initialize CMAP service), corosync inactive, proceeding",
			wantPvecm:   true,
		},
		{
			name: "quorum unknown with corosync failed proceeds with a warning",
			live: corosyncLive,
			outputs: map[string]string{
				"systemctl is-active corosync": "failed\n",
			},
			errs: map[string]error{
				"pvecm status":                 errors.New("exit status 2"),
				"systemctl is-active corosync": errors.New("exit status 3"),
			},
			wantWarning: "Cluster RECOVERY - quorum unknown (pvecm status failed: exit status 2), corosync failed, proceeding",
			wantPvecm:   true,
		},
		{
			// pmxcfs down, corosync up and quorate with its peers: pvecm dies on the
			// missing /etc/pve/corosync.conf, and pve-cluster would sync from the leader.
			name: "quorum unknown with corosync active is refused",
			live: corosyncLive,
			outputs: map[string]string{
				"pvecm status":                 "Error: Corosync config '/etc/pve/corosync.conf' does not exist - is this node part of a cluster?\n",
				"systemctl is-active corosync": "active\n",
			},
			errs:      map[string]error{"pvecm status": errors.New("exit status 2")},
			wantErr:   "Cluster RECOVERY refused - quorum unknown (could not determine quorum: Error: Corosync config '/etc/pve/corosync.conf' does not exist - is this node part of a cluster?), corosync active: in a quorate cluster, its copy would replace the restored config.db",
			wantPvecm: true,
		},
		{
			name: "quorum unknown with corosync starting is refused",
			live: corosyncLive,
			outputs: map[string]string{
				"systemctl is-active corosync": "activating\n",
			},
			errs:      map[string]error{"pvecm status": errors.New("exit status 2")},
			wantErr:   "Cluster RECOVERY refused - quorum unknown (pvecm status failed: exit status 2), corosync activating: in a quorate cluster, its copy would replace the restored config.db",
			wantPvecm: true,
		},
		{
			name:      "quorum unknown with an unreadable corosync state is refused",
			live:      corosyncLive,
			errs:      map[string]error{"pvecm status": errors.New("exit status 2")},
			wantErr:   "Cluster RECOVERY refused - quorum unknown (pvecm status failed: exit status 2), corosync state unknown (systemctl returned no output): in a quorate cluster, its copy would replace the restored config.db",
			wantPvecm: true,
		},
		{
			name: "quorate with an unreadable node count and corosync active is refused",
			live: corosyncLive,
			outputs: map[string]string{
				"pvecm status": strings.Replace(pvecmStatusOutput(3, 3, true),
					"Nodes:            3", "Nodes:            three", 1),
				"systemctl is-active corosync": "active\n",
			},
			wantErr:   "Cluster RECOVERY refused - quorum unknown (node count unreadable), corosync active: in a quorate cluster, its copy would replace the restored config.db",
			wantPvecm: true,
		},
		{
			name:        "pvecm not installed proceeds with a warning",
			live:        corosyncLive,
			errs:        map[string]error{"pvecm status": &exec.Error{Name: "pvecm", Err: exec.ErrNotFound}},
			wantWarning: "Cluster RECOVERY - quorum unknown (pvecm not available), proceeding",
			wantPvecm:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runClusterRecoveryRestore(t, clusterRecoveryOptions{live: tc.live, outputs: tc.outputs, errs: tc.errs})

			if got := r.count("pvecm status") > 0; got != tc.wantPvecm {
				t.Fatalf("pvecm status called=%v, want %v; calls=%q", got, tc.wantPvecm, r.cmd.CallsList())
			}
			if tc.wantWarning != "" {
				if !strings.Contains(r.log, tc.wantWarning) {
					t.Fatalf("missing warning %q in log:\n%s", tc.wantWarning, r.log)
				}
			} else if strings.Contains(r.log, "quorum unknown") {
				t.Fatalf("unexpected quorum warning in log:\n%s", r.log)
			}

			if tc.wantErr != "" {
				if r.err == nil || r.err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", r.err, tc.wantErr)
				}
				for _, c := range r.cmd.CallsList() {
					if strings.HasPrefix(c, "systemctl stop") || c == "umount /etc/pve" {
						t.Fatalf("refused RECOVERY still ran %q; calls=%q", c, r.cmd.CallsList())
					}
				}
				if got := r.configDB(t); got != "db-live\n" {
					t.Fatalf("refused RECOVERY changed the live config.db: %q", got)
				}
				return
			}

			if r.err != nil {
				t.Fatalf("restore: %v", r.err)
			}
			if r.count("systemctl stop --no-block pve-cluster") != 1 {
				t.Fatalf("RECOVERY did not stop pve-cluster; calls=%q", r.cmd.CallsList())
			}
			if got := r.configDB(t); got != "db-from-backup\n" {
				t.Fatalf("RECOVERY did not restore config.db: %q", got)
			}
		})
	}
}

// With pmxcfs down for 60 s, a running pve-ha-lrm lets its watchdog expire and the
// node is fenced; a real FULL RECOVERY died that way 54 s after the stop. The HA
// services are stopped before pve-cluster and started again after it, CRM before LRM.
func TestClusterRecoveryStopsHAServicesFirstAndStartsThemLast(t *testing.T) {
	r := runClusterRecoveryRestore(t, clusterRecoveryOptions{})
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	calls := r.cmd.CallsList()
	assertCallsInOrder(t, calls,
		"systemctl stop --no-block pve-ha-lrm",
		"systemctl stop --no-block pve-ha-crm",
		"systemctl stop --no-block pve-cluster",
		"systemctl stop --no-block pvedaemon",
		"systemctl stop --no-block pveproxy",
		"systemctl stop --no-block pvestatd",
		"systemctl start pve-cluster",
		"systemctl start pvedaemon",
		"systemctl start pveproxy",
		"systemctl start pvestatd",
		"systemctl start pve-ha-crm",
		"systemctl start pve-ha-lrm",
	)
	for _, svc := range clusterRecoveryServices {
		if n := r.count("systemctl start " + svc); n != 1 {
			t.Fatalf("%s started %d times, want 1; calls=%q", svc, n, calls)
		}
	}
}

// Only the extraction that writes config.db needs pmxcfs down. The services come
// back right after it, before the later steps (here the boot rebuild), and the
// deferred cleanup does not start them a second time.
func TestClusterRecoveryRestartsPVEServicesBeforeLaterSteps(t *testing.T) {
	archive := mikPve1Archive()
	r := runClusterRecoveryRestore(t, clusterRecoveryOptions{
		live:       pveTestLive(),
		archive:    archive,
		outputs:    grubHostOutputs,
		categories: []string{"pve_cluster", "boot"},
	})
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	start := r.index("systemctl start pve-cluster")
	rebuild := r.index("update-initramfs -u -k all")
	if start < 0 || rebuild < 0 {
		t.Fatalf("start pve-cluster at %d, update-initramfs at %d; calls=%q", start, rebuild, r.cmd.CallsList())
	}
	if start > rebuild {
		t.Fatalf("pve-cluster started after the boot rebuild; calls=%q", r.cmd.CallsList())
	}
	if n := r.count("systemctl start pve-cluster"); n != 1 {
		t.Fatalf("pve-cluster started %d times, want 1; calls=%q", n, r.cmd.CallsList())
	}
	if got := r.configDB(t); got != "db-from-backup\n" {
		t.Fatalf("RECOVERY did not restore config.db: %q", got)
	}
}

// A run that fails while the services are still down (here the extraction of an
// unreadable archive) restarts them from the deferred cleanup, once each, as before.
func TestClusterRecoveryRestartsPVEServicesWhenExtractionFails(t *testing.T) {
	origAnalyze := analyzeRestoreArchiveFunc
	t.Cleanup(func() { analyzeRestoreArchiveFunc = origAnalyze })
	// The analysis reads the valid archive; the extraction then meets garbage.
	analyzeRestoreArchiveFunc = func(_ string, logger *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		if err := restoreFS.WriteFile("/bundle-valid.tar", mustReadFakeFile(t, "/bundle.tar"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := restoreFS.WriteFile("/bundle.tar", []byte("not a tar archive, not gzip either"), 0o640); err != nil {
			t.Fatal(err)
		}
		return origAnalyze("/bundle-valid.tar", logger)
	}

	r := runClusterRecoveryRestore(t, clusterRecoveryOptions{})
	if r.err == nil {
		t.Fatalf("restore of an unreadable archive succeeded; calls=%q", r.cmd.CallsList())
	}
	if r.count("systemctl stop --no-block pve-cluster") != 1 {
		t.Fatalf("RECOVERY did not stop pve-cluster; calls=%q", r.cmd.CallsList())
	}
	for _, svc := range clusterRecoveryServices {
		if n := r.count("systemctl start " + svc); n != 1 {
			t.Fatalf("%s started %d times after the failed extraction, want 1; calls=%q", svc, n, r.cmd.CallsList())
		}
	}
	if got := r.configDB(t); got != "db-live\n" {
		t.Fatalf("failed extraction changed config.db: %q", got)
	}
}

func mustReadFakeFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func countCalls(calls []string, want string) int {
	n := 0
	for _, c := range calls {
		if c == want {
			n++
		}
	}
	return n
}

// healthCapturingUI records the health report the network COMMIT prompt receives.
type healthCapturingUI struct {
	*fakeRestoreWorkflowUI
	health *networkHealthReport
}

func (u *healthCapturingUI) PromptNetworkCommit(ctx context.Context, remaining time.Duration, health networkHealthReport, nicRepair *nicRepairResult, diagnosticsDir string) (bool, error) {
	h := health
	u.health = &h
	return true, nil
}

// fastServiceTimers shrinks the service stop/start waits so a failing stop takes
// milliseconds, and makes the LoadState query keep the caller's deadline.
func fastServiceTimers(t *testing.T) {
	t.Helper()
	origVerify, origStatus, origPoll, origRetry := serviceVerifyTimeout, serviceStatusCheckTimeout, servicePollInterval, serviceRetryDelay
	t.Cleanup(func() {
		serviceVerifyTimeout, serviceStatusCheckTimeout, servicePollInterval, serviceRetryDelay = origVerify, origStatus, origPoll, origRetry
	})
	serviceVerifyTimeout = 50 * time.Millisecond
	serviceStatusCheckTimeout = 20 * time.Millisecond
	servicePollInterval = 5 * time.Millisecond
	serviceRetryDelay = time.Millisecond
}

// inactiveServicesRunner answers every PVE service as stopped and loaded.
func inactiveServicesRunner() *FakeCommandRunner {
	cmd := &FakeCommandRunner{Outputs: map[string][]byte{}, Errors: map[string]error{}}
	for _, svc := range clusterRecoveryServices {
		cmd.Outputs["systemctl show -p LoadState --value "+svc] = []byte("loaded\n")
		cmd.Outputs["systemctl is-active "+svc] = []byte("inactive\n")
		cmd.Errors["systemctl is-active "+svc] = errors.New("inactive")
	}
	return cmd
}

// A host without pve-ha-manager has no pve-ha-lrm/pve-ha-crm units: systemctl
// stop on them exits 5 on every attempt, which aborted the restore. A unit that
// is not installed (LoadState not-found) is now neither stopped nor started; the
// installed ones are handled as before.
func TestPVEClusterServicesSkipUnitsNotInstalled(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	cmd := inactiveServicesRunner()
	for _, svc := range []string{"pve-ha-lrm", "pve-ha-crm"} {
		cmd.Outputs["systemctl show -p LoadState --value "+svc] = []byte("not-found\n")
	}
	restoreCmd = cmd

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	if err := stopPVEClusterServices(context.Background(), logger); err != nil {
		t.Fatalf("stopPVEClusterServices: %v", err)
	}
	if err := startPVEClusterServices(context.Background(), logger); err != nil {
		t.Fatalf("startPVEClusterServices: %v", err)
	}
	calls := cmd.CallsList()
	for _, svc := range []string{"pve-ha-lrm", "pve-ha-crm"} {
		for _, c := range calls {
			if strings.HasPrefix(c, "systemctl ") && strings.HasSuffix(c, " "+svc) && !strings.HasPrefix(c, "systemctl show ") {
				t.Fatalf("unit not installed was acted on: %q; calls=%q", c, calls)
			}
		}
	}
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		if countCalls(calls, "systemctl stop --no-block "+svc) != 1 || countCalls(calls, "systemctl start "+svc) != 1 {
			t.Fatalf("installed unit %s not stopped and started once; calls=%q", svc, calls)
		}
	}
	if strings.Contains(buf.String(), "not installed") {
		t.Fatalf("skip logged above Debug:\n%s", buf.String())
	}
}

// When a stop fails after other services were stopped, the ones already stopped are
// started again, last stopped first, and the original error is returned. A failed
// restart is a warning and does not stop the others.
func TestStopPVEClusterServicesRestartsWhatItStoppedWhenAStopFails(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	cmd := inactiveServicesRunner()
	cmd.Outputs["systemctl is-active pve-cluster"] = []byte("active\n")
	delete(cmd.Errors, "systemctl is-active pve-cluster")
	for _, args := range []string{"start", "restart"} {
		cmd.Errors["systemctl "+args+" pve-ha-crm"] = errors.New("crm will not start")
	}
	restoreCmd = cmd

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	err := stopPVEClusterServices(context.Background(), logger)
	if err == nil || !strings.Contains(err.Error(), "failed to stop PVE services (pve-cluster)") {
		t.Fatalf("err = %v, want the pve-cluster stop error", err)
	}
	calls := cmd.CallsList()
	assertCallsInOrder(t, calls,
		"systemctl stop --no-block pve-ha-lrm",
		"systemctl stop --no-block pve-ha-crm",
		"systemctl stop --no-block pve-cluster",
		"systemctl start pve-ha-crm",
		"systemctl start pve-ha-lrm",
	)
	for _, svc := range []string{"pve-cluster", "pvedaemon", "pveproxy", "pvestatd"} {
		if countCalls(calls, "systemctl start "+svc) != 0 {
			t.Fatalf("%s started although it was never stopped; calls=%q", svc, calls)
		}
	}
	if countCalls(calls, "systemctl stop --no-block pvedaemon") != 0 {
		t.Fatalf("stop went on past the failure; calls=%q", calls)
	}
	if !strings.Contains(buf.String(), "Failed to restart PVE services (pve-ha-crm) after the stop failed") {
		t.Fatalf("missing restart warning for pve-ha-crm in log:\n%s", buf.String())
	}
}

// slowUnitRunner answers "systemctl is-active <unit>" as active for the first
// activeChecks queries and inactive after that (never, when activeChecks < 0);
// every other command goes to the embedded fake.
type slowUnitRunner struct {
	*FakeCommandRunner
	unit         string
	activeChecks int
	checks       int
	// cancelAtCheck, when > 0, calls cancel on that is-active query (the restore
	// being cancelled during the wait).
	cancelAtCheck int
	cancel        context.CancelFunc
	// failQuery makes every is-active query on unit hang until its own timeout.
	failQuery bool
	// startCtxErrs records ctx.Err() of each "systemctl start <unit>" when it runs.
	startCtxErrs []error
}

func (r *slowUnitRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name == "systemctl" && len(args) == 2 && args[0] == "start" && args[1] == r.unit {
		r.startCtxErrs = append(r.startCtxErrs, ctx.Err())
	}
	if name != "systemctl" || len(args) != 2 || args[0] != "is-active" || args[1] != r.unit {
		return r.FakeCommandRunner.Run(ctx, name, args...)
	}
	r.Calls = append(r.Calls, commandKey(name, args))
	r.Contexts = append(r.Contexts, ctx)
	r.checks++
	if r.failQuery {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if r.cancelAtCheck > 0 && r.checks == r.cancelAtCheck && r.cancel != nil {
		r.cancel()
	}
	if r.activeChecks < 0 || r.checks <= r.activeChecks {
		return []byte("active\n"), nil
	}
	return []byte("inactive\n"), errors.New("inactive")
}

func (r *slowUnitRunner) assertNoKill(t *testing.T) {
	t.Helper()
	for _, c := range r.CallsList() {
		if strings.HasPrefix(c, "systemctl kill") {
			t.Fatalf("pve-ha-lrm stop sent a signal: %q; calls=%q", c, r.CallsList())
		}
	}
}

func withHALRMStopTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := haLRMStopTimeout
	t.Cleanup(func() { haLRMStopTimeout = orig })
	haLRMStopTimeout = d
}

// Measured on an isolated node whose old HA master was powered off: the LRM took
// 93 s to stop, waiting for the master lock to time out, and the generic stop had
// sent SIGTERM at +76 s (SIGKILL would follow). A SIGKILL before the LRM closes its
// watchdog fences the node. pve-ha-lrm gets one no-block stop and a long wait, and
// never systemctl kill.
func TestStopHALRMWaitsForASlowStopWithoutSignals(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	withHALRMStopTimeout(t, 10*time.Second)
	// Well past the 4 x 50 ms verify windows of the generic stop.
	runner := &slowUnitRunner{FakeCommandRunner: inactiveServicesRunner(), unit: "pve-ha-lrm", activeChecks: 60}
	restoreCmd = runner

	if err := stopPVEClusterServices(context.Background(), newTestLogger()); err != nil {
		t.Fatalf("stopPVEClusterServices: %v", err)
	}
	runner.assertNoKill(t)
	calls := runner.CallsList()
	if n := countCalls(calls, "systemctl stop --no-block pve-ha-lrm"); n != 1 {
		t.Fatalf("pve-ha-lrm no-block stop issued %d times, want 1; calls=%q", n, calls)
	}
	if n := countCalls(calls, "systemctl stop pve-ha-lrm"); n != 0 {
		t.Fatalf("pve-ha-lrm got a blocking stop; calls=%q", calls)
	}
	if runner.checks <= runner.activeChecks {
		t.Fatalf("stop returned before pve-ha-lrm went inactive: %d checks", runner.checks)
	}
	if n := countCalls(calls, "systemctl start pve-ha-lrm"); n != 0 {
		t.Fatalf("successful pve-ha-lrm stop was followed by a start; calls=%q", calls)
	}
	assertCallsInOrder(t, calls,
		"systemctl stop --no-block pve-ha-lrm",
		"systemctl stop --no-block pve-ha-crm",
		"systemctl stop --no-block pve-cluster",
		"systemctl stop --no-block pvestatd",
	)
}

// An LRM still active after the limit fails the stop through the usual error, with no
// signal sent, and nothing else stopped: pve-ha-lrm is the first service stopped. The
// no-block stop job is still queued then, so one systemctl start replaces it and
// leaves the LRM running, instead of letting it stop later with nothing to restart it.
func TestStopHALRMFailsAfterTheLimitWithoutSignals(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	withHALRMStopTimeout(t, 150*time.Millisecond)
	runner := &slowUnitRunner{FakeCommandRunner: inactiveServicesRunner(), unit: "pve-ha-lrm", activeChecks: -1}
	restoreCmd = runner

	err := stopPVEClusterServices(context.Background(), newTestLogger())
	if err == nil || err.Error() != "failed to stop PVE services (pve-ha-lrm): pve-ha-lrm still active after 150ms" {
		t.Fatalf("err = %v, want the pve-ha-lrm stop failure after the 150ms limit", err)
	}
	runner.assertNoKill(t)
	calls := runner.CallsList()
	if n := countCalls(calls, "systemctl start pve-ha-lrm"); n != 1 {
		t.Fatalf("systemctl start pve-ha-lrm issued %d times after the timeout, want 1; calls=%q", n, calls)
	}
	assertCallsInOrder(t, calls, "systemctl stop --no-block pve-ha-lrm", "systemctl start pve-ha-lrm")
	for _, c := range calls {
		if (strings.HasPrefix(c, "systemctl stop") && !strings.HasSuffix(c, " pve-ha-lrm")) ||
			(strings.HasPrefix(c, "systemctl start") && c != "systemctl start pve-ha-lrm") ||
			strings.HasPrefix(c, "systemctl restart") {
			t.Fatalf("failed pve-ha-lrm stop went on to %q; calls=%q", c, calls)
		}
	}
}

// When the start that replaces the queued stop job fails, that is a warning in the
// format of the other restart warnings, and the stop error is still the one returned.
func TestStopHALRMTimeoutWarnsWhenTheStartFails(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	fastServiceTimers(t)
	withHALRMStopTimeout(t, 150*time.Millisecond)
	fake := inactiveServicesRunner()
	fake.Errors["systemctl start pve-ha-lrm"] = errors.New("exit status 1")
	runner := &slowUnitRunner{FakeCommandRunner: fake, unit: "pve-ha-lrm", activeChecks: -1}
	restoreCmd = runner

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	err := stopPVEClusterServices(context.Background(), logger)
	if err == nil || err.Error() != "failed to stop PVE services (pve-ha-lrm): pve-ha-lrm still active after 150ms" {
		t.Fatalf("err = %v, want the original pve-ha-lrm stop failure", err)
	}
	if !strings.Contains(buf.String(), "Failed to restart PVE services (pve-ha-lrm) after the stop failed: systemctl start pve-ha-lrm failed: exit status 1") {
		t.Fatalf("missing restart warning in log:\n%s", buf.String())
	}
	calls := runner.CallsList()
	if n := countCalls(calls, "systemctl start pve-ha-lrm"); n != 1 {
		t.Fatalf("systemctl start pve-ha-lrm issued %d times, want 1; calls=%q", n, calls)
	}
	if n := countCalls(calls, "systemctl restart pve-ha-lrm"); n != 0 {
		t.Fatalf("failed start was retried with a restart; calls=%q", calls)
	}
	runner.assertNoKill(t)
}

// Every failure of the LRM stop, not only the timeout, issues one systemctl start on
// its own context before the original error is returned: the no-block stop job may
// still be queued, and nothing else would start the LRM again.
func TestStopHALRMStartsItAgainOnEveryFailure(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(r *slowUnitRunner, cancel context.CancelFunc)
		wantErr string
		waited  bool
	}{
		{
			name: "restore cancelled during the wait",
			setup: func(r *slowUnitRunner, cancel context.CancelFunc) {
				r.activeChecks = -1
				r.cancelAtCheck = 3
				r.cancel = cancel
			},
			wantErr: "failed to stop PVE services (pve-ha-lrm): context canceled",
			waited:  true,
		},
		{
			name: "is-active query fails",
			setup: func(r *slowUnitRunner, _ context.CancelFunc) {
				r.failQuery = true
			},
			wantErr: "failed to stop PVE services (pve-ha-lrm): systemctl is-active pve-ha-lrm timed out after 20ms",
			waited:  true,
		},
		{
			name: "no-block stop command fails",
			setup: func(r *slowUnitRunner, _ context.CancelFunc) {
				r.Outputs["systemctl stop --no-block pve-ha-lrm"] = []byte("Failed to stop pve-ha-lrm.service: Access denied\n")
				r.Errors["systemctl stop --no-block pve-ha-lrm"] = errors.New("exit status 4")
			},
			wantErr: "failed to stop PVE services (pve-ha-lrm): systemctl stop --no-block pve-ha-lrm failed: Failed to stop pve-ha-lrm.service: Access denied",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origCmd := restoreCmd
			t.Cleanup(func() { restoreCmd = origCmd })
			fastServiceTimers(t)
			withHALRMStopTimeout(t, 10*time.Second)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			runner := &slowUnitRunner{FakeCommandRunner: inactiveServicesRunner(), unit: "pve-ha-lrm"}
			tc.setup(runner, cancel)
			restoreCmd = runner

			err := stopPVEClusterServices(ctx, newTestLogger())
			if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			calls := runner.CallsList()
			if n := countCalls(calls, "systemctl start pve-ha-lrm"); n != 1 {
				t.Fatalf("systemctl start pve-ha-lrm issued %d times, want 1; calls=%q", n, calls)
			}
			if runner.startCtxErrs[0] != nil {
				t.Fatalf("start ran on a dead context: %v", runner.startCtxErrs[0])
			}
			if got := runner.checks > 0; got != tc.waited {
				t.Fatalf("waited for pve-ha-lrm=%v, want %v; calls=%q", got, tc.waited, calls)
			}
			runner.assertNoKill(t)
			for _, c := range calls {
				if (strings.HasPrefix(c, "systemctl stop") && !strings.HasSuffix(c, " pve-ha-lrm")) ||
					(strings.HasPrefix(c, "systemctl start") && c != "systemctl start pve-ha-lrm") ||
					strings.HasPrefix(c, "systemctl restart") {
					t.Fatalf("failed pve-ha-lrm stop went on to %q; calls=%q", c, calls)
				}
			}
		})
	}
}

// The restart of the 6 services gets 30 s each plus 10 s: 190 s.
func TestPVEClusterRestartTimeoutCoversSixServices(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	origStatus := serviceStatusCheckTimeout
	t.Cleanup(func() { serviceStatusCheckTimeout = origStatus })
	// Longer than the restart budget, so the LoadState query carries its deadline.
	serviceStatusCheckTimeout = time.Hour
	cmd := inactiveServicesRunner()
	restoreCmd = cmd

	w := &restoreUIWorkflowRun{ctx: context.Background(), logger: newTestLogger(), clusterServicesStopped: true}
	before := time.Now()
	w.restartStoppedPVEClusterServices()

	for i, c := range cmd.Calls {
		if c != "systemctl show -p LoadState --value pve-cluster" {
			continue
		}
		deadline, ok := cmd.Contexts[i].Deadline()
		if !ok {
			t.Fatalf("restart context has no deadline")
		}
		budget := deadline.Sub(before)
		if budget < 189*time.Second || budget > 191*time.Second {
			t.Fatalf("restart budget = %s, want 190s", budget)
		}
		return
	}
	t.Fatalf("restart never queried pve-cluster; calls=%q", cmd.Calls)
}

// The PVE services are running again by the time the network is applied, so a
// cluster RECOVERY runs the same post-apply PVE checks as any other restore instead of
// reporting them as skipped.
func TestNetworkApplyInClusterRecoveryRunsPVEChecks(t *testing.T) {
	origFS, origCmd, origTime, origSeq := restoreFS, restoreCmd, restoreTime, networkDiagnosticsSequence
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreTime, networkDiagnosticsSequence = origFS, origCmd, origTime, origSeq
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}
	networkDiagnosticsSequence = 0
	if err := fakeFS.AddFile("/etc/pve/corosync.conf", []byte("totem {}\n")); err != nil {
		t.Fatal(err)
	}

	pathDir := t.TempDir()
	for _, tool := range []string{"ifquery", "ifup", "ifreload"} {
		writeExecutableTestTool(t, pathDir, tool)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ip route show default":           []byte("default via 192.168.1.1 dev vmbr0\n"),
			"systemctl is-active pve-cluster": []byte("active\n"),
			"systemctl is-active corosync":    []byte("active\n"),
			"pvecm status":                    []byte(pvecmStatusOutput(1, 1, true)),
		},
		Errors: map[string]error{},
	}
	restoreCmd = cmd

	ui := &healthCapturingUI{fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{}}
	flow := &networkConfigUIApplyFlow{
		ctx:                 context.Background(),
		ui:                  ui,
		logger:              newTestLogger(),
		plan:                &RestorePlan{SystemType: SystemTypePVE, NeedsClusterRestore: true},
		networkRollbackPath: "/tmp/proxsave/network_rollback_backup_20260927_100000.tar.gz",
	}
	_ = flow.runConfirmedNetworkApply()

	if ui.health == nil {
		t.Fatalf("network COMMIT prompt never reached; calls=%q", cmd.CallsList())
	}
	names := map[string]string{}
	for _, c := range ui.health.Checks {
		names[c.Name] = c.Message
		if strings.Contains(c.Message, "cluster database restore in progress") {
			t.Fatalf("PVE checks reported as skipped: %s: %s", c.Name, c.Message)
		}
	}
	for _, want := range []string{"Corosync config", "pve-cluster service", "Cluster quorum"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("health report lacks %q; checks=%v", want, names)
		}
	}
	if countCalls(cmd.CallsList(), "pvecm status") == 0 {
		t.Fatalf("pvecm status not run by the health check; calls=%q", cmd.CallsList())
	}
}

// A RECOVERY restarts the PVE services once, right after config.db. When a unit fails to
// start, the others are still started, except the HA units while pve-cluster is down (the
// LRM would arm the watchdog without pmxcfs). The closing advice names what is left down
// instead of "stopped and started again". The run itself carries on, as before.
func TestClusterRecoveryFailedRestartIsNamedAndStartsTheRest(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failing    string
		notStarted []string
		advice     string
	}{
		{
			name:       "pve-cluster fails",
			failing:    "pve-cluster",
			notStarted: []string{"pve-ha-crm", "pve-ha-lrm"},
			advice:     "PVE services - stopped for this restore and not running: pve-cluster (start failed); pve-ha-crm, pve-ha-lrm (not started while pve-cluster is down)",
		},
		{
			name:    "pvedaemon fails",
			failing: "pvedaemon",
			advice:  "PVE services - stopped for this restore and not running: pvedaemon (start failed)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fastServiceTimers(t)
			fail := errors.New("Job for " + tc.failing + ".service failed")
			r := runClusterRecoveryRestore(t, clusterRecoveryOptions{
				errs: map[string]error{
					"systemctl start " + tc.failing:   fail,
					"systemctl restart " + tc.failing: fail,
				},
			})
			if r.err != nil {
				t.Fatalf("restore stopped on a failed restart: %v", r.err)
			}
			if got := r.configDB(t); got != "db-from-backup\n" {
				t.Fatalf("config.db = %q, want the restored one", got)
			}
			skipped := map[string]bool{}
			for _, svc := range tc.notStarted {
				skipped[svc] = true
				if n := r.count("systemctl start " + svc); n != 0 {
					t.Errorf("%s started %d times with pve-cluster down", svc, n)
				}
			}
			for _, svc := range pveClusterStartOrder {
				if svc == tc.failing || skipped[svc] {
					continue
				}
				if n := r.count("systemctl start " + svc); n == 0 {
					t.Errorf("%s never started after %s failed", svc, tc.failing)
				}
			}
			if !strings.Contains(r.log, tc.advice) {
				t.Errorf("advice %q missing from the log:\n%s", tc.advice, r.log)
			}
			if strings.Contains(r.log, "stopped and started again during this restore") {
				t.Errorf("advice still says the services were started again:\n%s", r.log)
			}
		})
	}
}
