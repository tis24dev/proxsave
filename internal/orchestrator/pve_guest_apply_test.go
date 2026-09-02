package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func guestApplyFixture(t *testing.T) (*FakeFS, *schemaAwarePvesh, *logging.Logger, func() string) {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pvesh := newSchemaAwarePvesh()
	restoreCmd = pvesh
	seamPmxcfs(t, "/etc/pve", true, nil)
	logBuf := logging.New(types.LogLevelDebug, false)
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
	// Force the API to fail even without create-only keys: an exact-key error on
	// the filtered set would need the fake to know the filtered args; simplest is
	// a conf whose only content beyond the name is the rejected key, so the
	// filtered set still succeeds - instead make the guest set fail by pointing
	// the entry at a conf with a key the fake refuses on qemu: none exists, so
	// drive the failure through --meta NOT being filtered is impossible once the
	// filter works. The honest failure seam is the fake itself: mark the set as
	// failing via a bogus storage-style rejection is not available - so this test
	// plants meta AND teaches the fake that vm100's set always fails by using the
	// running flag only for the fallback decision and an explicit set failure:
	pvesh.failSet = map[string]bool{"100": true}

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

// A stopped guest whose set fails for any reason gets the file: fidelity net.
func TestStoppedGuestFallsBackToTheConfFile(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.failSet = map[string]bool{"100": true}

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
	_ = filepath.Join
}

func assertUncertainGuestStatusSkipsFileFallback(t *testing.T, statusOutput []byte, statusErr error, logMarkers ...string) {
	t.Helper()
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.failSet = map[string]bool{"100": true}
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
