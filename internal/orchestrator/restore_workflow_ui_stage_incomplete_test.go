package orchestrator

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// A staged extraction that fails part way must not hand its stage to any step after
// the staged apply batch: the batch is skipped, and so must be the network install and
// apply, the firewall and HA applies and the PBS notifications repair. Each test
// below builds a real partial stage: the archive is extracted for real and either one
// staged file fails to land, the way a full /tmp makes it fail, or the run is
// cancelled part way.

// stageWriteFailFS fails the rename that puts one staged file in place, and can
// cancel the run once another staged file has landed. It records the staged files
// that did land, so a test can check the stage really was partial.
type stageWriteFailFS struct {
	*FakeFS
	failSuffix   string
	cancelSuffix string
	cancel       context.CancelFunc
	failed       []string
	staged       []string
}

func (f *stageWriteFailFS) Rename(oldpath, newpath string) error {
	inStage := strings.HasPrefix(newpath, "/tmp/proxsave/")
	if inStage && f.failSuffix != "" && strings.HasSuffix(newpath, f.failSuffix) {
		f.failed = append(f.failed, newpath)
		return &os.LinkError{Op: "rename", Old: oldpath, New: newpath, Err: syscall.ENOSPC}
	}
	if err := f.FakeFS.Rename(oldpath, newpath); err != nil {
		return err
	}
	if inStage {
		f.staged = append(f.staged, newpath)
		if f.cancelSuffix != "" && strings.HasSuffix(newpath, f.cancelSuffix) && f.cancel != nil {
			f.cancel()
		}
	}
	return nil
}

func (f *stageWriteFailFS) stagedFile(suffix string) bool {
	return slices.ContainsFunc(f.staged, func(p string) bool { return strings.HasSuffix(p, suffix) })
}

// stageConsumerSpies replaces the post-restore consumers that have no hooks of their
// own and records the stage each one is handed.
type stageConsumerSpies struct {
	networkInstall []string
	networkApply   []string
	pbsRepair      []string
}

func installStageConsumerSpies(t *testing.T) *stageConsumerSpies {
	t.Helper()
	spies := &stageConsumerSpies{}
	origInstall := installNetworkConfigFromStageFunc
	origApply := applyNetworkConfigWithUIFunc
	origRepair := verifyAndRepairPBSNotificationsFunc
	t.Cleanup(func() {
		installNetworkConfigFromStageFunc = origInstall
		applyNetworkConfigWithUIFunc = origApply
		verifyAndRepairPBSNotificationsFunc = origRepair
	})
	installNetworkConfigFromStageFunc = func(_ context.Context, _ *logging.Logger, _ *RestorePlan, stageRoot, _ string, _ *SafetyBackupResult, _ bool) (bool, error) {
		spies.networkInstall = append(spies.networkInstall, stageRoot)
		return false, nil
	}
	applyNetworkConfigWithUIFunc = func(_ context.Context, _ RestoreWorkflowUI, _ *logging.Logger, req networkConfigUIApplyRequest) error {
		spies.networkApply = append(spies.networkApply, req.stageRoot)
		return nil
	}
	verifyAndRepairPBSNotificationsFunc = func(_ context.Context, _ *logging.Logger, _ *RestorePlan, stageRoot string, _ bool) error {
		spies.pbsRepair = append(spies.pbsRepair, stageRoot)
		return nil
	}
	return spies
}

func stagedCategoriesByID(t *testing.T, ids ...string) []Category {
	t.Helper()
	all := GetAllCategories()
	categories := make([]Category, 0, len(ids))
	for _, id := range ids {
		cat := GetCategoryByID(id, all)
		if cat == nil {
			t.Fatalf("unknown category %q", id)
		}
		categories = append(categories, *cat)
	}
	return categories
}

func regularEntries(files ...[2]string) []tarEntry {
	entries := make([]tarEntry, 0, len(files))
	for _, f := range files {
		entries = append(entries, tarEntry{Name: f[0], Typeflag: tar.TypeReg, Mode: 0o644, Data: []byte(f[1])})
	}
	return entries
}

// newStageRun wires a selective restore run to a fake filesystem holding the given
// archive. failSuffix names the staged file whose write fails ("" for none).
func newStageRun(t *testing.T, systemType SystemType, categoryIDs []string, entries []tarEntry, failSuffix string) (*restoreUIWorkflowRun, *stageWriteFailFS) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })

	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	fs := &stageWriteFailFS{FakeFS: fake, failSuffix: failSuffix}
	restoreFS = fs
	restoreCmd = &FakeCommandRunner{}

	writeTarToFakeFS(t, fake, "/backup.tar", entries)

	w := &restoreUIWorkflowRun{
		ctx:      context.Background(),
		cfg:      &config.Config{},
		logger:   newTestLogger(),
		ui:       &fakeRestoreWorkflowUI{},
		destRoot: "/",
		plan: &RestorePlan{
			SystemType:       systemType,
			StagedCategories: stagedCategoriesByID(t, categoryIDs...),
		},
		prepared: &preparedBundle{ArchivePath: "/backup.tar"},
	}
	return w, fs
}

// stageAndExpectIncomplete runs the staging step and checks the stage really is
// partial: the write of fs.failSuffix was refused while goodSuffix landed.
func stageAndExpectIncomplete(t *testing.T, w *restoreUIWorkflowRun, fs *stageWriteFailFS, goodSuffix string) {
	t.Helper()
	if err := w.stageAndApplySensitiveCategories(); err != nil {
		t.Fatalf("stageAndApplySensitiveCategories error: %v", err)
	}
	if len(fs.failed) == 0 {
		t.Fatalf("setup: the write of %s was never attempted", fs.failSuffix)
	}
	if !fs.stagedFile(goodSuffix) {
		t.Fatalf("setup: %s was not staged; staged=%v", goodSuffix, fs.staged)
	}
	if !w.restoreHadWarnings {
		t.Fatalf("setup: the incomplete staging was not reported")
	}
}

func readFakeFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	data, err := restoreFS.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data), true
}

func TestIncompleteStagingLeavesLiveFirewallConfigAlone(t *testing.T) {
	origGeteuid, origMounted, origRealFS := firewallApplyGeteuid, firewallIsMounted, firewallIsRealRestoreFS
	origRestart, origHostname := firewallRestartService, firewallHostname
	t.Cleanup(func() {
		firewallApplyGeteuid, firewallIsMounted, firewallIsRealRestoreFS = origGeteuid, origMounted, origRealFS
		firewallRestartService, firewallHostname = origRestart, origHostname
	})
	firewallApplyGeteuid = func() int { return 0 }
	firewallIsMounted = func(string) (bool, error) { return true, nil }
	firewallIsRealRestoreFS = func(FS) bool { return true }
	firewallRestartService = func(context.Context) error { return nil }
	firewallHostname = func() (string, error) { return "node1", nil }

	w, fs := newStageRun(t, SystemTypePVE, []string{"pve_firewall"}, regularEntries(
		[2]string{"./etc/pve/firewall/cluster.fw", "[OPTIONS]\nenable: 0\n"},
		[2]string{"./etc/pve/firewall/100.fw", "[RULES]\nIN ACCEPT\n"},
	), "/etc/pve/firewall/100.fw")
	const liveCluster = "[OPTIONS]\nenable: 1\n"
	const liveVM = "[RULES]\nIN DROP -source 0.0.0.0/0\n"
	if err := fs.AddFile("/etc/pve/firewall/cluster.fw", []byte(liveCluster)); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFile("/etc/pve/firewall/100.fw", []byte(liveVM)); err != nil {
		t.Fatal(err)
	}
	// An operator who answers yes to every firewall prompt.
	ui := &scriptedRestoreWorkflowUI{
		fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
		script:                []scriptedConfirmAction{{ok: true}, {ok: true}},
	}
	w.ui = ui

	stageAndExpectIncomplete(t, w, fs, "/etc/pve/firewall/cluster.fw")
	if err := w.applyFirewallConfig(); err != nil {
		t.Fatalf("applyFirewallConfig error: %v", err)
	}

	if got, ok := readFakeFile(t, "/etc/pve/firewall/100.fw"); !ok {
		t.Fatalf("live /etc/pve/firewall/100.fw was deleted by a firewall apply from an incomplete stage")
	} else if got != liveVM {
		t.Fatalf("live 100.fw changed: %q", got)
	}
	if got, _ := readFakeFile(t, "/etc/pve/firewall/cluster.fw"); got != liveCluster {
		t.Fatalf("live cluster.fw overwritten from an incomplete stage: %q", got)
	}
	if ui.calls != 0 {
		t.Fatalf("the operator was asked %d time(s) to apply a firewall configuration from an incomplete stage", ui.calls)
	}
}

func TestIncompleteStagingLeavesLiveHAConfigAlone(t *testing.T) {
	origGeteuid, origMounted, origRealFS := haApplyGeteuid, haIsMounted, haIsRealRestoreFS
	t.Cleanup(func() { haApplyGeteuid, haIsMounted, haIsRealRestoreFS = origGeteuid, origMounted, origRealFS })
	haApplyGeteuid = func() int { return 0 }
	haIsMounted = func(string) (bool, error) { return true, nil }
	haIsRealRestoreFS = func(FS) bool { return true }

	w, fs := newStageRun(t, SystemTypePVE, []string{"pve_ha"}, regularEntries(
		[2]string{"./etc/pve/ha/resources.cfg", "vm: 100\n\tstate started\n"},
		[2]string{"./etc/pve/ha/rules.cfg", "node-affinity: keep-100\n\tresources vm:100\n\tnodes node1\n"},
	), "/etc/pve/ha/rules.cfg")
	const liveResources = "vm: 200\n\tstate started\n"
	const liveRules = "node-affinity: keep-200\n\tresources vm:200\n\tnodes node2\n"
	if err := fs.AddFile("/etc/pve/ha/resources.cfg", []byte(liveResources)); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFile("/etc/pve/ha/rules.cfg", []byte(liveRules)); err != nil {
		t.Fatal(err)
	}
	ui := &scriptedRestoreWorkflowUI{
		fakeRestoreWorkflowUI: &fakeRestoreWorkflowUI{},
		script:                []scriptedConfirmAction{{ok: true}, {ok: true}},
	}
	w.ui = ui

	stageAndExpectIncomplete(t, w, fs, "/etc/pve/ha/resources.cfg")
	if err := w.applyHAConfig(); err != nil {
		t.Fatalf("applyHAConfig error: %v", err)
	}

	if got, ok := readFakeFile(t, "/etc/pve/ha/rules.cfg"); !ok {
		t.Fatalf("live /etc/pve/ha/rules.cfg was deleted by an HA apply from an incomplete stage")
	} else if got != liveRules {
		t.Fatalf("live rules.cfg changed: %q", got)
	}
	if got, _ := readFakeFile(t, "/etc/pve/ha/resources.cfg"); got != liveResources {
		t.Fatalf("live resources.cfg overwritten from an incomplete stage: %q", got)
	}
	if ui.calls != 0 {
		t.Fatalf("the operator was asked %d time(s) to apply an HA configuration from an incomplete stage", ui.calls)
	}
}

func TestIncompleteStagingSkipsNetworkInstallAndApply(t *testing.T) {
	spies := installStageConsumerSpies(t)
	w, fs := newStageRun(t, SystemTypePVE, []string{"network"}, regularEntries(
		[2]string{"./etc/network/interfaces", "auto vmbr0\niface vmbr0 inet static\n\taddress 10.0.0.2/24\n"},
		[2]string{"./etc/hosts", "10.0.0.2 node1\n"},
	), "/etc/hosts")

	stageAndExpectIncomplete(t, w, fs, "/etc/network/interfaces")
	if err := w.installNetworkConfigFromStage(); err != nil {
		t.Fatalf("installNetworkConfigFromStage error: %v", err)
	}
	if err := w.applyNetworkConfig(); err != nil {
		t.Fatalf("applyNetworkConfig error: %v", err)
	}

	if len(spies.networkInstall) != 0 {
		t.Fatalf("network install was handed an incomplete stage: %v", spies.networkInstall)
	}
	if len(spies.networkApply) != 0 {
		t.Fatalf("network apply ran after an incomplete staging (stage=%v)", spies.networkApply)
	}
}

func TestIncompleteStagingSkipsPBSNotificationsRepair(t *testing.T) {
	entries := regularEntries(
		[2]string{"./etc/proxmox-backup/notifications.cfg", "smtp: mail\n\tfrom-address pbs@example.com\n\tserver smtp.example.com\n"},
		[2]string{"./etc/proxmox-backup/notifications-priv.cfg", "smtp: mail\n\tpassword secret\n"},
	)

	t.Run("services stopped: deferred cleanup", func(t *testing.T) {
		spies := installStageConsumerSpies(t)
		w, fs := newStageRun(t, SystemTypePBS, []string{"pbs_notifications"}, entries, "/etc/proxmox-backup/notifications-priv.cfg")
		cmd := restoreCmd.(*FakeCommandRunner)
		w.pbsServicesStopped = true
		cleanup := w.restartPBSServicesCleanup()

		stageAndExpectIncomplete(t, w, fs, "/etc/proxmox-backup/notifications.cfg")
		w.verifyPBSNotificationsAfterRestore()
		cleanup()

		if len(spies.pbsRepair) != 0 {
			t.Fatalf("PBS notifications repair was handed an incomplete stage: %v", spies.pbsRepair)
		}
		if !slices.Contains(cmd.Calls, "systemctl start proxmox-backup") {
			t.Fatalf("PBS services were not restarted; calls=%v", cmd.Calls)
		}
	})

	t.Run("services running: post-restore step", func(t *testing.T) {
		spies := installStageConsumerSpies(t)
		w, fs := newStageRun(t, SystemTypePBS, []string{"pbs_notifications"}, entries, "/etc/proxmox-backup/notifications-priv.cfg")

		stageAndExpectIncomplete(t, w, fs, "/etc/proxmox-backup/notifications.cfg")
		w.verifyPBSNotificationsAfterRestore()

		if len(spies.pbsRepair) != 0 {
			t.Fatalf("PBS notifications repair was handed an incomplete stage: %v", spies.pbsRepair)
		}
	})
}

// An operator abort during staging ends the run, but the deferred services cleanup
// still runs: it must restart PBS without repairing notifications from the stage.
func TestAbortedStagingSkipsPBSNotificationsRepairInCleanup(t *testing.T) {
	spies := installStageConsumerSpies(t)
	w, fs := newStageRun(t, SystemTypePBS, []string{"pbs_notifications"}, regularEntries(
		[2]string{"./etc/proxmox-backup/notifications.cfg", "smtp: mail\n\tfrom-address pbs@example.com\n\tserver smtp.example.com\n"},
		[2]string{"./etc/proxmox-backup/notifications-priv.cfg", "smtp: mail\n\tpassword secret\n"},
	), "")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.ctx = ctx
	fs.cancel = cancel
	fs.cancelSuffix = "/etc/proxmox-backup/notifications.cfg"
	cmd := restoreCmd.(*FakeCommandRunner)
	w.pbsServicesStopped = true
	cleanup := w.restartPBSServicesCleanup()

	err := w.stageAndApplySensitiveCategories()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stageAndApplySensitiveCategories err=%v; want context.Canceled", err)
	}
	if !fs.stagedFile("/etc/proxmox-backup/notifications.cfg") || fs.stagedFile("/etc/proxmox-backup/notifications-priv.cfg") {
		t.Fatalf("setup: stage not partial; staged=%v", fs.staged)
	}
	cleanup()

	if len(spies.pbsRepair) != 0 {
		t.Fatalf("PBS notifications repair ran from an aborted staging: %v", spies.pbsRepair)
	}
	if !slices.Contains(cmd.Calls, "systemctl start proxmox-backup") {
		t.Fatalf("PBS services were not restarted; calls=%v", cmd.Calls)
	}
}

// Control: a staging that completes still reaches every post-restore consumer.
func TestCompleteStagingReachesPostRestoreConsumers(t *testing.T) {
	spies := installStageConsumerSpies(t)
	w, fs := newStageRun(t, SystemTypeDual, []string{"network", "pbs_notifications"}, regularEntries(
		[2]string{"./etc/network/interfaces", "auto lo\niface lo inet loopback\n"},
		[2]string{"./etc/proxmox-backup/notifications.cfg", "smtp: mail\n\tserver smtp.example.com\n"},
	), "")
	w.restoreHadWarnings = false

	if err := w.stageAndApplySensitiveCategories(); err != nil {
		t.Fatalf("stageAndApplySensitiveCategories error: %v", err)
	}
	if w.stageRoot == "" || w.stageLogPath == "" {
		t.Fatalf("setup: staging did not complete (stageRoot=%q log=%q)", w.stageRoot, w.stageLogPath)
	}
	if !fs.stagedFile("/etc/network/interfaces") {
		t.Fatalf("setup: interfaces not staged; staged=%v", fs.staged)
	}
	w.verifyPBSNotificationsAfterRestore()
	if err := w.installNetworkConfigFromStage(); err != nil {
		t.Fatalf("installNetworkConfigFromStage error: %v", err)
	}
	if err := w.applyNetworkConfig(); err != nil {
		t.Fatalf("applyNetworkConfig error: %v", err)
	}

	for name, got := range map[string][]string{
		"PBS notifications repair": spies.pbsRepair,
		"network install":          spies.networkInstall,
		"network apply":            spies.networkApply,
	} {
		if len(got) != 1 || got[0] != w.stageRoot {
			t.Fatalf("%s got stage %v; want [%s]", name, got, w.stageRoot)
		}
	}
}

// The files that did land hold secrets in the clear: the partial stage goes as soon
// as the staging fails, not when the run ends.
func TestIncompleteStagingRemovesStageAtOnce(t *testing.T) {
	w, fs := newStageRun(t, SystemTypePVE, []string{"pve_firewall"}, regularEntries(
		[2]string{"./etc/pve/firewall/cluster.fw", "[OPTIONS]\nenable: 1\n"},
		[2]string{"./etc/pve/firewall/100.fw", "[RULES]\n"},
	), "/etc/pve/firewall/100.fw")

	stageAndExpectIncomplete(t, w, fs, "/etc/pve/firewall/cluster.fw")

	if w.stageRoot != "" {
		t.Fatalf("stageRoot=%q; want it cleared once the stage is discarded", w.stageRoot)
	}
	staged := fs.staged[0]
	stageDir := strings.Join(strings.SplitN(staged, "/", 5)[:4], "/")
	if !strings.HasPrefix(stageDir, "/tmp/proxsave/restore-stage-") {
		t.Fatalf("setup: unexpected stage dir %q (from %q)", stageDir, staged)
	}
	if _, err := restoreFS.Stat(stageDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial stage %s still on disk (stat err=%v)", stageDir, err)
	}
}
