package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func guestApplyFixture(t *testing.T) (*FakeFS, *schemaAwarePvesh, *logging.Logger, func() string) {
	t.Helper()
	origFS, origCmd, origLockedWriter := restoreFS, restoreCmd, pveGuestLockedWriter
	t.Cleanup(func() {
		restoreFS, restoreCmd, pveGuestLockedWriter = origFS, origCmd, origLockedWriter
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pvesh := newSchemaAwarePvesh()
	restoreCmd = pvesh
	seamPmxcfs(t, "/etc/pve", true, nil)
	logBuf := logging.New(types.LogLevelDebug, false)
	pveGuestLockedWriter = func(
		_ context.Context,
		logger *logging.Logger,
		currentNode string,
		vm vmEntry,
		precondition guestApplyPrecondition,
		data []byte,
	) error {
		// Run the test hook before the simulated locked decision. Race tests use
		// pmxcfsIsMounted to inject a create/start at this exact boundary.
		mounted, err := pmxcfsIsMounted(pmxcfsRoot)
		if err != nil {
			return err
		}
		if !mounted {
			return fmt.Errorf("%s is not mounted", pmxcfsRoot)
		}

		exists := pvesh.guests[vm.VMID]
		switch precondition {
		case guestMustBeAbsent:
			if exists {
				return fmt.Errorf("guest %s appeared before locked apply", vm.VMID)
			}
		case guestMustBeStopped:
			if !exists {
				return fmt.Errorf("guest %s disappeared before locked apply", vm.VMID)
			}
			owner := pvesh.guestNodes[vm.VMID]
			if owner == "" {
				owner = currentNode
			}
			if owner != currentNode {
				return fmt.Errorf("guest %s belongs to node %s", vm.VMID, owner)
			}
			kind := pvesh.guestKinds[vm.VMID]
			if kind == "" {
				kind = "qemu"
			}
			if kind != vm.Kind {
				return fmt.Errorf("guest %s is %s, not %s", vm.VMID, kind, vm.Kind)
			}
			if pvesh.running[vm.VMID] {
				return fmt.Errorf("guest %s is running", vm.VMID)
			}
		default:
			return fmt.Errorf("invalid guest apply precondition %q", precondition)
		}

		rel := filepath.Join("nodes", currentNode, guestConfDir(vm.Kind), vm.VMID+".conf")
		return pmxcfsWriteFile(logger, rel, data)
	}
	return fakeFS, pvesh, logBuf, localNodeName
}

// Every VM created on PVE >= 7.2 carries `meta:` in its conf, and the live
// node's qemu set-schema has no --meta ("meta: property is not defined in
// schema", probed 2026-09-02): one rejected key failed the WHOLE set, so no
// config was ever applied for a modern guest (fable-check bug 2). The API is
// still tried first minus the create-only keys; the staged conf file is the
// fidelity net.
func TestExistingGuestConfigSurvivesTheMetaRejection(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true

	conf := "name: vm100\nmeta: creation-qemu=10.1.2,ctime=1776092687\ncores: 2\n"
	if err := fakeFS.AddFile("/export/etc/pve/nodes/"+node()+"/qemu-server/100.conf", []byte(conf)); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100",
		Path: "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf",
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", applied, failed)
	}
	// The API attempt must NOT carry the create-only key the schema refuses.
	for _, call := range pvesh.calls {
		if strings.Contains(call, "set /nodes/") && strings.Contains(call, "--meta=") {
			t.Fatalf("the set still sends --meta, the key the live schema refuses: %s", call)
		}
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("the API path succeeded, the file net must stay unused")
	}
}

// A missing LXC could never be created: pveshCreateGuestArgs demanded an
// ostemplate no real conf carries (create-time-only parameter; the passing
// unit test hand-fed it). Writing the conf into pmxcfs IS how a guest comes
// into existence; disks are not part of a config restore and the log says so.
func TestMissingLxcIsRegisteredByWritingItsConf(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)

	conf := "hostname: ct101\nunprivileged: 1\nrootfs: local:101/vm-101-disk-0.raw,size=8G\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/lxc/101.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "101", Kind: "lxc", Name: "ct101", Path: stagePath,
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", applied, failed)
	}
	got, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/lxc/101.conf")
	if err != nil || string(got) != conf {
		t.Fatalf("conf not registered in pmxcfs: %q err=%v", got, err)
	}
	for _, call := range pvesh.calls {
		if strings.Contains(call, "create /nodes/") {
			t.Fatalf("pvesh create is a dead end (ostemplate) and must be gone: %s", call)
		}
	}
}

// The maintainer's guard: a set that fails on a RUNNING guest does not get the
// file fallback - a config race with a live guest - and the warning names it.
func TestRunningGuestSkipsTheFileFallback(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.running["100"] = true

	buf := &strings.Builder{}
	logger.SetOutput(buf)

	conf := "name: vm100\nmeta: creation-qemu=10.1.2\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}
	// Force the filtered set to fail so the test reaches the running-status guard.
	pvesh.schemaRefuseSet = map[string]bool{"100": true}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("the file was written under a running guest")
	}
	out := buf.String()
	if !strings.Contains(out, "100") || !strings.Contains(out, "running") {
		t.Fatalf("the warning does not name the guest and the reason:\n%s", out)
	}
}

// A stopped guest whose set is refused BY SCHEMA gets the file: the fidelity net
// for a create-only key filterGuestCreateOnlyArgs did not know to drop.
func TestStoppedGuestFallsBackToTheConfFile(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.schemaRefuseSet = map[string]bool{"100": true}

	conf := "name: vm100\ncores: 4\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}
	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0", applied, failed)
	}
	got, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf")
	if err != nil || string(got) != conf {
		t.Fatalf("fallback conf not written: %q err=%v", got, err)
	}
}

func assertUncertainGuestStatusSkipsFileFallback(t *testing.T, statusOutput []byte, statusErr error, logMarkers ...string) {
	t.Helper()
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.schemaRefuseSet = map[string]bool{"100": true}
	if statusOutput != nil {
		pvesh.statusOutput["100"] = statusOutput
	}
	if statusErr != nil {
		pvesh.statusError["100"] = statusErr
	}

	buf := &strings.Builder{}
	logger.SetOutput(buf)
	conf := "name: vm100\ncores: 4\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("the file fallback ran without an explicit stopped status")
	}
	logOutput := buf.String()
	for _, marker := range append([]string{"100"}, logMarkers...) {
		if !strings.Contains(logOutput, marker) {
			t.Fatalf("warning does not contain %q:\n%s", marker, logOutput)
		}
	}
}

func TestGuestStatusProbeFailureSkipsFileFallback(t *testing.T) {
	assertUncertainGuestStatusSkipsFileFallback(t, nil, errors.New("status unavailable"), "status unavailable")
}

func TestMalformedGuestStatusSkipsFileFallback(t *testing.T) {
	assertUncertainGuestStatusSkipsFileFallback(t, []byte(`{"status":`), nil, "status")
}

func TestPrettyPrintedRunningStatusSkipsFileFallback(t *testing.T) {
	assertUncertainGuestStatusSkipsFileFallback(t, []byte("{\n  \"vmid\": 100,\n  \"status\": \"running\"\n}"), nil, "running")
}

func TestUnknownGuestStatusSkipsFileFallback(t *testing.T) {
	assertUncertainGuestStatusSkipsFileFallback(t, []byte(`{"status":"unknown","vmid":100}`), nil, "unknown")
}

func TestRemoteGuestIsNeverAppliedOnTheCurrentNode(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.guestNodes["100"] = "pve-remote"

	buf := &strings.Builder{}
	logger.SetOutput(buf)
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte("name: vm100\ncores: 4\n")); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("remote guest config was written on the current node")
	}
	for _, call := range pvesh.calls {
		if strings.Contains(call, "set /nodes/") || strings.Contains(call, "/status/current") {
			t.Fatalf("remote guest reached a mutating or fallback-probe call: %s", call)
		}
	}
	output := buf.String()
	for _, marker := range []string{"100", "pve-remote", "Start pve guest configs apply", "End pve guest configs apply"} {
		if !strings.Contains(output, marker) {
			t.Fatalf("log does not contain %q:\n%s", marker, output)
		}
	}
}

func TestGuestKindMismatchIsNeverApplied(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.guestKinds["100"] = "lxc"

	buf := &strings.Builder{}
	logger.SetOutput(buf)
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte("name: vm100\n")); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("type-mismatched guest config was written")
	}
	for _, call := range pvesh.calls {
		if strings.Contains(call, "set /nodes/") || strings.Contains(call, "/status/current") {
			t.Fatalf("type-mismatched guest reached a mutating or fallback-probe call: %s", call)
		}
	}
	if output := buf.String(); !strings.Contains(output, "100") || !strings.Contains(output, "lxc") || !strings.Contains(output, "qemu") {
		t.Fatalf("warning does not explain the type mismatch:\n%s", output)
	}
}

func TestGuestInventoryFailureSkipsEveryMutation(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.guests["101"] = true
	pvesh.guestKinds["101"] = "lxc"
	pvesh.inventoryError = errors.New("cluster inventory unavailable")

	buf := &strings.Builder{}
	logger.SetOutput(buf)
	qemuPath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	lxcPath := "/export/etc/pve/nodes/" + node() + "/lxc/101.conf"
	if err := fakeFS.AddFile(qemuPath, []byte("name: vm100\n")); err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.AddFile(lxcPath, []byte("hostname: ct101\n")); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{
		{VMID: "100", Kind: "qemu", Name: "vm100", Path: qemuPath},
		{VMID: "101", Kind: "lxc", Name: "ct101", Path: lxcPath},
	}, logger)
	if applied != 0 || failed != 2 {
		t.Fatalf("applied=%d failed=%d, want 0/2", applied, failed)
	}
	if len(pvesh.calls) != 1 || !strings.Contains(pvesh.calls[0], "get /cluster/resources --type vm") {
		t.Fatalf("inventory failure must stop after one read-only query: %v", pvesh.calls)
	}
	if output := buf.String(); !strings.Contains(output, "cluster inventory unavailable") {
		t.Fatalf("warning does not report the inventory failure:\n%s", output)
	}
}

func TestMalformedGuestInventorySkipsEveryMutation(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.inventoryOutput = []byte(`{"vmid":100}`)

	buf := &strings.Builder{}
	logger.SetOutput(buf)
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte("name: vm100\n")); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if len(pvesh.calls) != 1 || !strings.Contains(pvesh.calls[0], "get /cluster/resources --type vm") {
		t.Fatalf("malformed inventory must stop after one read-only query: %v", pvesh.calls)
	}
	if output := buf.String(); !strings.Contains(output, "expected a JSON array") {
		t.Fatalf("warning does not report malformed inventory:\n%s", output)
	}
}

// The conf-file fallback answers exactly ONE failure: the update schema refusing
// a key. Everything else is the API failing to apply a payload it understood -
// no quorum, permission denied, a 500 - and the staged bytes are not a remedy
// for it. The API's rejection is the only validation those bytes ever get, so a
// fallback on every error is how an invalid conf reaches pmxcfs cluster-wide.
// This test pins the narrowing of a deliberate "fails for any reason" decision.
func TestNonSchemaSetFailureNeverReachesTheConfFile(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.failSet = map[string]bool{"100": true}

	buf := &strings.Builder{}
	logger.SetOutput(buf)

	conf := "name: vm100\ncores: 4\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 0 || failed != 1 {
		t.Fatalf("applied=%d failed=%d, want 0/1", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("a 500 from the API wrote the staged conf into pmxcfs")
	}
	if calls := strings.Join(pvesh.calls, "\n"); strings.Contains(calls, "/status/current") {
		t.Fatalf("a non-schema failure still probed the guest status:\n%s", calls)
	}
	if out := buf.String(); !strings.Contains(out, "refused the operation, not the payload") {
		t.Fatalf("the warning does not say why the file is not a remedy:\n%s", out)
	}
}

func TestPveshSchemaRefusalSeparatesPayloadFromOperation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		out  string
		want bool
	}{
		{"meta rejection in the error", errString("400 Parameter verification failed. meta: property is not defined in schema and the schema does not allow additional properties"), "", true},
		{"unknown option on the output", errString("exit status 255"), "Unknown option: path\n400 unable to parse option", true},
		{"apply failure", errString("500 unable to apply configuration"), "", false},
		{"no quorum", errString("exit status 2"), "cluster not ready - no quorum?", false},
		{"permission denied", errString("exit status 2"), "403 Permission check failed", false},
		{"no error at all", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pveshSchemaRefusal(tc.err, []byte(tc.out)); got != tc.want {
				t.Fatalf("pveshSchemaRefusal = %v, want %v", got, tc.want)
			}
		})
	}
}
