package orchestrator

import (
	"bytes"
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
		// Same opening sentence as the schema refusal, entirely different meaning:
		// the staged conf carries a value the API rejects. Writing it verbatim into
		// pmxcfs is precisely what the fallback must never do.
		{"value error wearing the same prefix", errString("exit status 2"), "400 Parameter verification failed. cores: value 999 does not match the regex pattern", false},
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

// filterGuestCreateOnlyArgs knows two create-only keys by name. The set schema
// varies by PVE version, so a third one appearing costs the WHOLE config apply on
// a guest the conf file cannot rescue - a running one. The refusal names the key,
// so drop it and retry: the guest loses that key instead of every key, and the
// line says which one it lost.
func TestRunningGuestKeepsEveryKeyTheSchemaAccepts(t *testing.T) {
	fakeFS, pvesh, logger, node := guestApplyFixture(t)
	pvesh.guests["100"] = true
	pvesh.running["100"] = true
	pvesh.refuseSetKeys = map[string]bool{"balloon": true}

	buf := &strings.Builder{}
	logger.SetOutput(buf)

	conf := "name: vm100\nballoon: 0\ncores: 4\n"
	stagePath := "/export/etc/pve/nodes/" + node() + "/qemu-server/100.conf"
	if err := fakeFS.AddFile(stagePath, []byte(conf)); err != nil {
		t.Fatal(err)
	}

	applied, failed := applyVMConfigs(context.Background(), []vmEntry{{
		VMID: "100", Kind: "qemu", Name: "vm100", Path: stagePath,
	}}, logger)
	if applied != 1 || failed != 0 {
		t.Fatalf("applied=%d failed=%d, want 1/0: the accepted keys must still land", applied, failed)
	}
	if _, err := fakeFS.ReadFile("/etc/pve/nodes/" + node() + "/qemu-server/100.conf"); err == nil {
		t.Fatal("the conf file was written under a running guest")
	}
	calls := strings.Join(pvesh.calls, "\n")
	if !strings.Contains(calls, "--cores=4") || strings.Contains(calls[strings.LastIndex(calls, "\n"):], "--balloon=") {
		t.Fatalf("the retry did not resend the accepted keys without --balloon:\n%s", calls)
	}
	out := buf.String()
	if !strings.Contains(out, "without balloon") {
		t.Fatalf("the outcome does not name the key whose staged value was dropped:\n%s", out)
	}
}

func TestPveshRefusedKeyFromNamesTheKeyAcrossBothShapes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		out  string
		want string
	}{
		{"storage shape on the output", errString("exit status 255"), "Unknown option: path\n400 unable to parse option", "path"},
		{"guest shape in the error", errString("400 Parameter verification failed. meta: property is not defined in schema and the schema does not allow additional properties"), "", "meta"},
		{"guest shape on the output", errString("exit status 255"), "400 Parameter verification failed. balloon: property is not defined in schema", "balloon"},
		// Same opening sentence, a VALUE error: dropping the key here would discard
		// a staged value the operator asked to restore.
		{"value error names nothing", errString("exit status 2"), "400 Parameter verification failed. cores: value 999 does not match the regex pattern", ""},
		{"unrelated failure", errString("500 unable to apply configuration"), "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pveshRefusedKeyFrom(tc.err, []byte(tc.out)); got != tc.want {
				t.Fatalf("pveshRefusedKeyFrom = %q, want %q", got, tc.want)
			}
		})
	}
}

// refusingStorageRunner refuses the named keys one at a time, the way pvesh does:
// a set carrying a key the schema does not accept fails naming that ONE key, and
// the caller has to drop it and try again.
type refusingStorageRunner struct {
	refuse map[string]bool
	// live is what `pvesh get /storage/<id>` answers, as PVE returns it: a flat JSON
	// object of the definition's current values. Empty means the read fails.
	live string
}

func (r *refusingStorageRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "get" {
		if r.live == "" {
			return nil, errors.New("exit status 2")
		}
		return []byte(r.live), nil
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--") {
			continue
		}
		key := strings.TrimPrefix(strings.SplitN(arg, "=", 2)[0], "--")
		if r.refuse[key] {
			return []byte("Parameter verification failed.\n" + key + ": property is not defined in schema and the schema does not allow additional properties\n"),
				errors.New("exit status 2")
		}
	}
	return nil, nil
}

// The storage arm and the guest arm share pveshSetDroppingRefusedKeys, whose own
// doc comment says a caller that stays silent about the dropped keys loses the fact
// that their staged values were NOT applied. The guest arm reports them; the storage
// arm discarded the list and announced an update anyway, so an operator read
// "Updated existing storage definition" over a definition that is not in the staged
// state, and "already matches every settable key" over one where nothing could be
// sent at all.
func TestStorageApplyNamesTheKeysThatWereRefused(t *testing.T) {
	cases := map[string]struct {
		refuse   []string
		wantLine string
	}{
		"some keys refused": {
			refuse:   []string{"server"},
			wantLine: "Updated existing storage definition nfs-backup without server: the update schema refuses those keys, so their staged values are not applied",
		},
		"every key refused": {
			refuse:   []string{"server", "export", "content"},
			wantLine: "Applied nothing for storage nfs-backup: every staged key refused, not comparable (server, export, content)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			origCmd := restoreCmd
			t.Cleanup(func() { restoreCmd = origCmd })
			refuse := make(map[string]bool, len(tc.refuse))
			for _, key := range tc.refuse {
				refuse[key] = true
			}
			restoreCmd = &refusingStorageRunner{refuse: refuse}

			cfg := filepath.Join(t.TempDir(), "storage.cfg")
			body := "nfs: nfs-backup\n\tserver 1.2.3.4\n\texport /srv/nfs\n\tcontent backup\n"
			if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
				t.Fatalf("write storage.cfg: %v", err)
			}

			buf := &bytes.Buffer{}
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(buf)

			if _, _, _, err := applyStorageCfg(context.Background(), cfg, logger); err != nil {
				t.Fatalf("applyStorageCfg: %v", err)
			}
			if !strings.Contains(buf.String(), tc.wantLine) {
				t.Fatalf("missing %q in:\n%s", tc.wantLine, buf.String())
			}
		})
	}
}

// Registering a guest that does not exist yet claims the VMID cluster-wide FIRST and
// writes the staged conf second. The helper's own cleanup only runs when that second
// write returns an error; a cancelled restore kills the process outright, so nothing
// runs and the claim can outlive the run as an empty, create-locked guest holding a
// VMID nobody can reuse. It cannot be made atomic - the process can die at any
// instant - so the run has to say what it may have left behind.
func TestAnAbortedRegistrationNamesTheVMIDItMayHaveLeftBehind(t *testing.T) {
	fakeFS, pvesh, _, _ := guestApplyFixture(t)
	_ = pvesh

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The loop's own ctx check passes, and the cancellation lands INSIDE the helper,
	// which is the only interleaving that can leave the claim behind.
	pveGuestLockedWriter = func(
		_ context.Context, _ *logging.Logger, _ string,
		_ vmEntry, precondition guestApplyPrecondition, _ []byte,
	) error {
		if precondition != guestMustBeAbsent {
			t.Fatalf("precondition = %q, want absent", precondition)
		}
		cancel()
		return context.Canceled
	}

	if err := fakeFS.AddFile("/stage/101.conf", []byte("name: webserver\n")); err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(buf)

	applyVMConfigs(ctx, []vmEntry{{VMID: "101", Kind: "qemu", Name: "webserver", Path: "/stage/101.conf"}}, logger)

	want := "Aborted while registering VM/CT config 101 (webserver): VMID 101 may be left reserved and locked on the cluster"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, buf.String())
	}
}

// When the set schema refuses EVERY key, the run cannot tell from the refusal alone
// whether anything was lost: the refused key may already hold the staged value, in
// which case there was nothing to do, or it may differ, in which case the restore
// could not put it back. Counting it either way is a guess. Reading the live
// definition and comparing the refused keys turns it into an answer - and where it
// cannot, the third count says so instead of picking one of the other two.
func TestAnAllRefusedStorageIsJudgedAgainstTheLiveDefinition(t *testing.T) {
	cases := map[string]struct {
		live        string
		wantApplied int
		wantUnknown int
		wantFailed  int
		wantLine    string
	}{
		"the refused key already holds the staged value": {
			live:        `{"storage":"nfs-backup","type":"nfs","server":"1.2.3.4"}`,
			wantApplied: 1,
			wantLine:    "Storage definition nfs-backup already matches every staged key the update schema refuses",
		},
		"the refused key differs and cannot be set": {
			live:       `{"storage":"nfs-backup","type":"nfs","server":"9.9.9.9"}`,
			wantFailed: 1,
			wantLine:   "Failed to apply storage nfs-backup: the update schema refuses server and the live definition does not match the staged value",
		},
		// This row used to want applied=1. Nothing here was applied and nothing was
		// shown to be wrong; the honest count is neither of those two.
		"the live definition cannot be read": {
			live:        "",
			wantUnknown: 1,
			wantLine:    "Applied nothing for storage nfs-backup: every staged key refused, not comparable (server)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			origCmd := restoreCmd
			t.Cleanup(func() { restoreCmd = origCmd })
			restoreCmd = &refusingStorageRunner{refuse: map[string]bool{"server": true}, live: tc.live}

			cfg := filepath.Join(t.TempDir(), "storage.cfg")
			if err := os.WriteFile(cfg, []byte("nfs: nfs-backup\n\tserver 1.2.3.4\n"), 0o600); err != nil {
				t.Fatalf("write storage.cfg: %v", err)
			}
			buf := &bytes.Buffer{}
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(buf)

			applied, unknown, failed, err := applyStorageCfg(context.Background(), cfg, logger)
			if err != nil {
				t.Fatalf("applyStorageCfg: %v", err)
			}
			if applied != tc.wantApplied || unknown != tc.wantUnknown || failed != tc.wantFailed {
				t.Fatalf("applied=%d unknown=%d failed=%d, want %d/%d/%d",
					applied, unknown, failed, tc.wantApplied, tc.wantUnknown, tc.wantFailed)
			}
			if !strings.Contains(buf.String(), tc.wantLine) {
				t.Fatalf("missing %q in:\n%s", tc.wantLine, buf.String())
			}
		})
	}
}

// guestConfDir maps a guest kind to its pmxcfs config directory.
func guestConfDir(kind string) string {
	if kind == "qemu" {
		return "qemu-server"
	}
	return kind
}
