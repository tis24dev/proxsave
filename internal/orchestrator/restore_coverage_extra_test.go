package orchestrator

import (
	"archive/tar"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
)

type runOnlyRunner struct{}

func (runOnlyRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected command: %s", commandKey(name, args))
}

type recordingRunner struct {
	calls []string
}

func (r *recordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, commandKey(name, args))
	return []byte("ok"), nil
}

func TestGetModeName_CoversAllModes(t *testing.T) {
	tests := []struct {
		mode RestoreMode
		want string
	}{
		{mode: RestoreModeFull, want: "FULL restore (all files)"},
		{mode: RestoreModeStorage, want: "STORAGE/DATASTORE only"},
		{mode: RestoreModeBase, want: "SYSTEM BASE only"},
		{mode: RestoreModeCustom, want: "CUSTOM selection"},
		{mode: RestoreMode("unknown"), want: "Unknown mode"},
	}

	for _, tt := range tests {
		if got := getModeName(tt.mode); got != tt.want {
			t.Fatalf("getModeName(%v)=%q want %q", tt.mode, got, tt.want)
		}
	}
}

func TestDetectImportableZFSPools_ReturnsPoolsAndErrorWhenCommandFails(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"zpool import": []byte("pool: tank\n"),
		},
		Errors: map[string]error{
			"zpool import": fmt.Errorf("boom"),
		},
	}
	restoreCmd = fake

	pools, output, err := detectImportableZFSPools()
	if err == nil {
		t.Fatalf("expected error")
	}
	if len(pools) != 1 || pools[0] != "tank" {
		t.Fatalf("unexpected pools: %#v", pools)
	}
	if !strings.Contains(output, "pool: tank") {
		t.Fatalf("unexpected output: %q", output)
	}
}

func TestCheckZFSPoolsAfterRestore_ReturnsNilWhenZpoolMissing(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Errors: map[string]error{
			"which zpool": fmt.Errorf("missing"),
		},
	}
	restoreCmd = fake

	if err := checkZFSPoolsAfterRestore(newTestLogger()); err != nil {
		t.Fatalf("expected nil error when zpool missing, got %v", err)
	}
	if len(fake.Calls) != 1 || fake.Calls[0] != "which zpool" {
		t.Fatalf("unexpected calls: %#v", fake.Calls)
	}
}

func TestCheckZFSPoolsAfterRestore_ConfiguredPools_NoImportables(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	origGlob := restoreGlob
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
		restoreGlob = origGlob
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreGlob = func(pattern string) ([]string, error) { return nil, nil }

	for _, unit := range []string{
		"/etc/systemd/system/zfs-import.target.wants/zfs-import@tank.service",
		"/etc/systemd/system/multi-user.target.wants/import@backup_ext.service",
	} {
		if err := fakeFS.AddFile(unit, []byte("x")); err != nil {
			t.Fatalf("add unit %s: %v", unit, err)
		}
	}

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which zpool":             []byte("/sbin/zpool\n"),
			"zpool import":            []byte(""),
			"zpool status tank":       []byte("ok"),
			"zpool status backup_ext": []byte("missing"),
		},
		Errors: map[string]error{
			"zpool status backup_ext": fmt.Errorf("not found"),
		},
	}
	restoreCmd = fake

	if err := checkZFSPoolsAfterRestore(newTestLogger()); err != nil {
		t.Fatalf("checkZFSPoolsAfterRestore error: %v", err)
	}

	for _, want := range []string{"which zpool", "zpool import", "zpool status tank", "zpool status backup_ext"} {
		found := false
		for _, call := range fake.Calls {
			if call == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected call %q, got calls=%#v", want, fake.Calls)
		}
	}
}

func TestCheckZFSPoolsAfterRestore_ReportsImportablePools(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	origGlob := restoreGlob
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
		restoreGlob = origGlob
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreGlob = func(pattern string) ([]string, error) { return nil, nil }

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"which zpool":  []byte("/sbin/zpool\n"),
			"zpool import": []byte("pool: tank\n"),
		},
	}
	restoreCmd = fake

	if err := checkZFSPoolsAfterRestore(newTestLogger()); err != nil {
		t.Fatalf("checkZFSPoolsAfterRestore error: %v", err)
	}

	for _, want := range []string{"which zpool", "zpool import"} {
		found := false
		for _, call := range fake.Calls {
			if call == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected call %q, got calls=%#v", want, fake.Calls)
		}
	}
	for _, call := range fake.Calls {
		if strings.HasPrefix(call, "zpool status ") {
			t.Fatalf("did not expect zpool status calls when pools are importable; calls=%#v", fake.Calls)
		}
	}
}

func TestRunFullRestore_ExtractsArchiveToDestination(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	destRoot := t.TempDir()
	archivePath := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(archivePath, map[string]string{
		"etc/test.txt": "hello",
	}); err != nil {
		t.Fatalf("writeTarFile: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("RESTORE\n"))
	cand := &decryptCandidate{
		DisplayBase: "test",
		Manifest:    &backup.Manifest{CreatedAt: time.Now()},
	}
	prepared := &preparedBundle{ArchivePath: archivePath}

	if err := runFullRestore(context.Background(), reader, cand, prepared, destRoot, newTestLogger(), false); err != nil {
		t.Fatalf("runFullRestore error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(destRoot, "etc", "test.txt"))
	if err != nil {
		t.Fatalf("expected extracted file: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("extracted content=%q want %q", string(data), "hello")
	}
}

func TestApplyStorageCfg_NoBlocksReturnsNil(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	cfgPath := filepath.Join(t.TempDir(), "storage.cfg")
	if err := os.WriteFile(cfgPath, []byte("# empty\n"), 0o640); err != nil {
		t.Fatalf("write storage.cfg: %v", err)
	}

	applied, failed, err := applyStorageCfg(context.Background(), cfgPath, newTestLogger())
	if err != nil {
		t.Fatalf("applyStorageCfg error: %v", err)
	}
	if applied != 0 || failed != 0 {
		t.Fatalf("expected (0,0), got (%d,%d)", applied, failed)
	}
}

func TestRunSafeClusterApply_AppliesVMStorageAndDatacenterConfigs(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	pveshPath := filepath.Join(pathDir, "pvesh")
	if err := os.WriteFile(pveshPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	node, _ := os.Hostname()
	node = shortHost(node)
	if node == "" {
		node = "localhost"
	}

	qemuDir := filepath.Join(exportRoot, "etc", "pve", "nodes", node, "qemu-server")
	lxcDir := filepath.Join(exportRoot, "etc", "pve", "nodes", node, "lxc")
	for _, dir := range []string{qemuDir, lxcDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(qemuDir, "100.conf"), []byte("name: vm100\n"), 0o640); err != nil {
		t.Fatalf("write vm config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lxcDir, "101.conf"), []byte("hostname: ct101\n"), 0o640); err != nil {
		t.Fatalf("write ct config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(qemuDir, "notes.txt"), []byte("skip"), 0o640); err != nil {
		t.Fatalf("write extra file: %v", err)
	}

	if err := os.WriteFile(filepath.Join(exportRoot, "etc", "pve", "storage.cfg"), []byte(strings.Join([]string{
		"storage: local",
		"    type dir",
		"    path /var/lib/vz",
		"",
		"storage: backup_ext",
		"    type nfs",
		"    server 10.0.0.1",
		"",
	}, "\n")), 0o640); err != nil {
		t.Fatalf("write storage.cfg: %v", err)
	}

	if err := os.WriteFile(filepath.Join(exportRoot, "etc", "pve", "datacenter.cfg"), []byte("keyboard: it\n"), 0o640); err != nil {
		t.Fatalf("write datacenter.cfg: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("yes\nyes\nyes\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefixes := []string{
		"pvesh set /nodes/" + node + "/qemu/100/config --filename ",
		"pvesh set /nodes/" + node + "/lxc/101/config --filename ",
		"pvesh set /cluster/storage/local -conf ",
		"pvesh set /cluster/storage/backup_ext -conf ",
		"pvesh set /cluster/config -conf ",
	}
	for _, prefix := range wantPrefixes {
		found := false
		for _, call := range runner.calls {
			if strings.HasPrefix(call, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected a call with prefix %q; calls=%#v", prefix, runner.calls)
		}
	}
}

func TestRunSafeClusterApply_AppliesPoolsFromUserCfg(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	for _, name := range []string{"pvesh", "pveum"} {
		binPath := filepath.Join(pathDir, name)
		if err := os.WriteFile(binPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	userCfgPath := filepath.Join(exportRoot, "etc", "pve", "user.cfg")
	if err := os.MkdirAll(filepath.Dir(userCfgPath), 0o755); err != nil {
		t.Fatalf("mkdir user.cfg dir: %v", err)
	}
	userCfg := strings.Join([]string{
		"pool: dev",
		"    comment Dev pool",
		"    vms 100,101",
		"    storage local,backup_ext",
		"",
	}, "\n")
	if err := os.WriteFile(userCfgPath, []byte(userCfg), 0o640); err != nil {
		t.Fatalf("write user.cfg: %v", err)
	}

	// Prompts:
	// - Apply pools? yes
	// - Allow move? no
	reader := bufio.NewReader(strings.NewReader("yes\nno\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefixes := []string{
		"pveum pool add dev",
		"pveum pool modify dev --comment Dev pool",
		"pveum pool modify dev --vms 100,101 --storage backup_ext,local",
	}
	for _, prefix := range wantPrefixes {
		found := false
		for _, call := range runner.calls {
			if strings.HasPrefix(call, prefix) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected a call with prefix %q; calls=%#v", prefix, runner.calls)
		}
	}
}

func TestRunSafeClusterApply_AppliesResourceMappingsFromProxsaveInfo(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	pveshPath := filepath.Join(pathDir, "pvesh")
	if err := os.WriteFile(pveshPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	mappingPath := filepath.Join(exportRoot, "var", "lib", "proxsave-info", "commands", "pve", "mapping_pci.json")
	if err := os.MkdirAll(filepath.Dir(mappingPath), 0o755); err != nil {
		t.Fatalf("mkdir mapping dir: %v", err)
	}
	if err := os.WriteFile(mappingPath, []byte(strings.TrimSpace(`[
  {"id":"device1","comment":"GPU","map":[{"node":"pve01","path":"0000:01:00.0"}]}
]`)), 0o640); err != nil {
		t.Fatalf("write mapping_pci.json: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("yes\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefix := "pvesh create /cluster/mapping/pci --id device1 --comment GPU --map node=pve01,path=0000:01:00.0"
	found := false
	for _, call := range runner.calls {
		if strings.HasPrefix(call, wantPrefix) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a call with prefix %q; calls=%#v", wantPrefix, runner.calls)
	}
}

func TestRunSafeClusterApply_UsesSingleExportedNodeWhenHostnameMismatch(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	pveshPath := filepath.Join(pathDir, "pvesh")
	if err := os.WriteFile(pveshPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	targetNode, _ := os.Hostname()
	targetNode = shortHost(targetNode)
	if targetNode == "" {
		targetNode = "localhost"
	}
	sourceNode := targetNode + "-old"

	qemuDir := filepath.Join(exportRoot, "etc", "pve", "nodes", sourceNode, "qemu-server")
	if err := os.MkdirAll(qemuDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", qemuDir, err)
	}
	if err := os.WriteFile(filepath.Join(qemuDir, "100.conf"), []byte("name: vm100\n"), 0o640); err != nil {
		t.Fatalf("write vm config: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("yes\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefix := "pvesh set /nodes/" + targetNode + "/qemu/100/config --filename "
	wantSourceSuffix := filepath.Join("etc", "pve", "nodes", sourceNode, "qemu-server", "100.conf")
	found := false
	for _, call := range runner.calls {
		if strings.HasPrefix(call, wantPrefix) && strings.Contains(call, wantSourceSuffix) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a call with prefix %q using source %q; calls=%#v", wantPrefix, sourceNode, runner.calls)
	}
}

func TestRunSafeClusterApply_PromptsForSourceNodeWhenMultipleExportNodes(t *testing.T) {
	origCmd := restoreCmd
	origFS := restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})
	restoreFS = osFS{}

	pathDir := t.TempDir()
	pveshPath := filepath.Join(pathDir, "pvesh")
	if err := os.WriteFile(pveshPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write pvesh: %v", err)
	}
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	runner := &recordingRunner{}
	restoreCmd = runner

	exportRoot := t.TempDir()
	targetNode, _ := os.Hostname()
	targetNode = shortHost(targetNode)
	if targetNode == "" {
		targetNode = "localhost"
	}

	sourceNode1 := targetNode + "-a"
	sourceNode2 := targetNode + "-b"

	qemuDir1 := filepath.Join(exportRoot, "etc", "pve", "nodes", sourceNode1, "qemu-server")
	qemuDir2 := filepath.Join(exportRoot, "etc", "pve", "nodes", sourceNode2, "qemu-server")
	for _, dir := range []string{qemuDir1, qemuDir2} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(qemuDir1, "100.conf"), []byte("name: vm100\n"), 0o640); err != nil {
		t.Fatalf("write vm config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(qemuDir2, "101.conf"), []byte("name: vm101\n"), 0o640); err != nil {
		t.Fatalf("write vm config: %v", err)
	}

	reader := bufio.NewReader(strings.NewReader("2\nyes\n"))
	if err := runSafeClusterApply(context.Background(), reader, exportRoot, newTestLogger()); err != nil {
		t.Fatalf("runSafeClusterApply error: %v", err)
	}

	wantPrefix := "pvesh set /nodes/" + targetNode + "/qemu/101/config --filename "
	wantSourceSuffix := filepath.Join("etc", "pve", "nodes", sourceNode2, "qemu-server", "101.conf")
	found := false
	for _, call := range runner.calls {
		if strings.HasPrefix(call, wantPrefix) && strings.Contains(call, wantSourceSuffix) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a call with prefix %q using source %q; calls=%#v", wantPrefix, sourceNode2, runner.calls)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "/qemu/100/config") {
			t.Fatalf("expected not to apply vmid=100 from %s; call=%q", sourceNode1, call)
		}
	}
}

func TestApplyVMConfigs_RespectsContextCancellation(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	restoreCmd = &FakeCommandRunner{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	applied, failed := applyVMConfigs(ctx, []vmEntry{{VMID: "100", Kind: "qemu", Path: "/tmp/100.conf"}}, newTestLogger())
	if applied != 0 || failed != 0 {
		t.Fatalf("expected (0,0), got (%d,%d)", applied, failed)
	}
}

func TestRunPvesh_ReturnsErrorOnFailure(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"pvesh set /cluster/config -conf /tmp/dc.cfg": []byte("permission denied"),
		},
		Errors: map[string]error{
			"pvesh set /cluster/config -conf /tmp/dc.cfg": fmt.Errorf("exit 1"),
		},
	}
	restoreCmd = fake

	logger := newTestLogger()
	err := runPvesh(context.Background(), logger, []string{"set", "/cluster/config", "-conf", "/tmp/dc.cfg"})
	if err == nil || !strings.Contains(err.Error(), "pvesh") {
		t.Fatalf("expected pvesh error, got %v", err)
	}
}

func TestRunRestoreCommandStream_FallsBackToExecCommand(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })

	restoreCmd = runOnlyRunner{}

	reader, err := runRestoreCommandStream(context.Background(), "cat", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("runRestoreCommandStream error: %v", err)
	}
	rc, ok := reader.(io.ReadCloser)
	if !ok {
		t.Fatalf("expected io.ReadCloser, got %T", reader)
	}
	defer rc.Close()

	out, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(out) != "hello" {
		t.Fatalf("unexpected output: %q", string(out))
	}
}

func TestRunRestoreWorkflow_ReturnsErrorWhenConfigMissing(t *testing.T) {
	err := RunRestoreWorkflow(context.Background(), nil, newTestLogger(), "")
	if err == nil || !strings.Contains(err.Error(), "configuration not available") {
		t.Fatalf("expected config missing error, got %v", err)
	}
}

func TestExtractTarEntry_SkipsSensitiveSystemPathsOnRootRestore(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })
	restoreFS = osFS{}

	logger := newTestLogger()

	// Guarded /etc/pve path.
	if err := extractTarEntry(nil, &tar.Header{Name: "etc/pve/local.cfg", Typeflag: tar.TypeReg}, string(os.PathSeparator), logger); err != nil {
		t.Fatalf("expected nil for /etc/pve guard, got %v", err)
	}

	// Guarded PBS auth files.
	if err := extractTarEntry(nil, &tar.Header{Name: "etc/proxmox-backup/user.cfg", Typeflag: tar.TypeReg}, string(os.PathSeparator), logger); err != nil {
		t.Fatalf("expected nil for PBS auth guard, got %v", err)
	}
}

func writeTarFile(path string, files map[string]string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	for name, content := range files {
		b := []byte(content)
		hdr := &tar.Header{
			Name:    name,
			Mode:    0o640,
			Size:    int64(len(b)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(b); err != nil {
			return err
		}
	}
	return nil
}
