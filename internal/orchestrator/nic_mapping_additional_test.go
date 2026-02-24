package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

type tarEntry struct {
	Name     string
	Typeflag byte
	Mode     int64
	Data     []byte
}

type mkdirAllFailFS struct {
	FS
	failPath string
	err      error
}

func (f mkdirAllFailFS) MkdirAll(path string, perm os.FileMode) error {
	if filepath.Clean(path) == filepath.Clean(f.failPath) {
		return f.err
	}
	return f.FS.MkdirAll(path, perm)
}

func writeTarToFakeFS(t *testing.T, fs *FakeFS, archivePath string, entries []tarEntry) {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, entry := range entries {
		hdr := &tar.Header{
			Name:     entry.Name,
			Typeflag: entry.Typeflag,
			Mode:     entry.Mode,
			Size:     int64(len(entry.Data)),
		}
		if entry.Typeflag == tar.TypeDir {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %s: %v", entry.Name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write(entry.Data); err != nil {
				t.Fatalf("Write %s: %v", entry.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := fs.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write archive: %v", err)
	}
}

func TestNICMappingResult_RenameMapAndDetails(t *testing.T) {
	r := nicMappingResult{
		Entries: []nicMappingEntry{
			{OldName: "eno1", NewName: "enp3s0"},
			{OldName: "", NewName: "enp4s0"},
			{OldName: "ens2", NewName: ""},
			{OldName: "eno1", NewName: "enp3s1"},
		},
	}
	m := r.RenameMap()
	if len(m) != 1 || m["eno1"] != "enp3s1" {
		t.Fatalf("RenameMap=%v; want {eno1:enp3s1}", m)
	}

	if got := (nicMappingResult{}).Details(); got != "NIC mapping: none" {
		t.Fatalf("empty Details=%q", got)
	}

	details := nicMappingResult{
		Entries: []nicMappingEntry{
			{OldName: "b", NewName: "B", Method: nicMatchMAC, Identifier: "m2"},
			{OldName: "a", NewName: "A", Method: nicMatchPermanentMAC, Identifier: "m1"},
		},
	}.Details()
	want := strings.Join([]string{
		"NIC mapping (backup -> current):",
		"- a -> A (permanent_mac=m1)",
		"- b -> B (mac=m2)",
	}, "\n")
	if details != want {
		t.Fatalf("Details=%q; want %q", details, want)
	}
}

func TestNICNameConflict_Details(t *testing.T) {
	c := nicNameConflict{
		Mapping: nicMappingEntry{
			OldName:    "eno1",
			NewName:    "eth0",
			Method:     nicMatchMAC,
			Identifier: "aa:bb",
		},
		Existing: archivedNetworkInterface{
			Name:         "eno1",
			PermanentMAC: "AA:BB:CC:DD:EE:FF",
			MAC:          "mac:11:22:33:44:55:66 ",
			PCIPath:      "/pci/0000:00:1f.6",
		},
	}
	got := c.Details()
	if !strings.Contains(got, "permMAC=aa:bb:cc:dd:ee:ff") || !strings.Contains(got, "mac=11:22:33:44:55:66") || !strings.Contains(got, "pci=/pci/0000:00:1f.6") {
		t.Fatalf("Details=%q; want identifiers included", got)
	}
	if !strings.Contains(got, "but current eno1 exists") {
		t.Fatalf("Details=%q; want conflict message", got)
	}

	none := nicNameConflict{
		Mapping: nicMappingEntry{OldName: "eno1", NewName: "eth0", Method: nicMatchPCIPath, Identifier: "pci0"},
		Existing: archivedNetworkInterface{
			Name: "eno1",
		},
	}.Details()
	if !strings.Contains(none, "no identifiers") {
		t.Fatalf("Details=%q; want no identifiers", none)
	}
}

func TestNICRepairPlan_HasWork(t *testing.T) {
	if (nicRepairPlan{}).HasWork() {
		t.Fatalf("expected HasWork=false")
	}
	if !(nicRepairPlan{SafeMappings: []nicMappingEntry{{OldName: "a", NewName: "b"}}}.HasWork()) {
		t.Fatalf("expected HasWork=true with safe mappings")
	}
	if !(nicRepairPlan{Conflicts: []nicNameConflict{{Mapping: nicMappingEntry{OldName: "a", NewName: "b"}}}}.HasWork()) {
		t.Fatalf("expected HasWork=true with conflicts")
	}
}

func TestNICRepairResult_SummaryAndDetails(t *testing.T) {
	r := nicRepairResult{SkippedReason: "test"}
	if got := r.Summary(); got != "NIC name repair skipped: test" {
		t.Fatalf("Summary=%q", got)
	}
	if r.Applied() {
		t.Fatalf("Applied=true; want false")
	}

	r = nicRepairResult{}
	if got := r.Summary(); got != "NIC name repair: no changes needed" {
		t.Fatalf("Summary=%q", got)
	}

	r = nicRepairResult{
		ChangedFiles: []string{"/etc/network/interfaces"},
		BackupDir:    "/tmp/proxsave/nic_repair_test",
		AppliedNICMap: []nicMappingEntry{
			{OldName: "eno1", NewName: "eth0", Method: nicMatchMAC, Identifier: "aa"},
		},
	}
	if got := r.Summary(); got != "NIC name repair applied: 1 file(s) updated" {
		t.Fatalf("Summary=%q", got)
	}
	if !r.Applied() {
		t.Fatalf("Applied=false; want true")
	}
	details := r.Details()
	if !strings.Contains(details, "Backup of pre-repair files: /tmp/proxsave/nic_repair_test") || !strings.Contains(details, "Updated files:\n- /etc/network/interfaces") {
		t.Fatalf("Details=%q; want backup and updated files", details)
	}
	if !strings.Contains(details, "NIC mapping (backup -> current):") || !strings.Contains(details, "- eno1 -> eth0 (mac=aa)") {
		t.Fatalf("Details=%q; want mapping details", details)
	}
}

func TestReadArchiveEntry_ErrorsAndNotFound(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	archivePath := "/backup.tar"
	writeTarToFakeFS(t, fakeFS, archivePath, []tarEntry{
		{Name: "./other.txt", Typeflag: tar.TypeReg, Mode: 0o644, Data: []byte("ok")},
	})

	_, _, err := readArchiveEntry(context.Background(), archivePath, []string{"./missing.txt"}, 16)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v; want os.ErrNotExist", err)
	}

	_, _, err = readArchiveEntry(context.Background(), "/does-not-exist.tar", []string{"./x"}, 16)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v; want open os.ErrNotExist", err)
	}

	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = readArchiveEntry(cctx, archivePath, []string{"./other.txt"}, 16)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want context.Canceled", err)
	}

	if err := fakeFS.WriteFile("/backup.zip", []byte("not a tar"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err = readArchiveEntry(context.Background(), "/backup.zip", []string{"./other.txt"}, 16)
	if err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("err=%v; want unsupported archive format", err)
	}

	writeTarToFakeFS(t, fakeFS, "/nonregular.tar", []tarEntry{
		{Name: "./entry", Typeflag: tar.TypeDir, Mode: 0o755},
	})
	_, _, err = readArchiveEntry(context.Background(), "/nonregular.tar", []string{"./entry"}, 16)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err=%v; want not a regular file", err)
	}

	writeTarToFakeFS(t, fakeFS, "/toolarge.tar", []tarEntry{
		{Name: "./big", Typeflag: tar.TypeReg, Mode: 0o644, Data: []byte("0123456789")},
	})
	_, _, err = readArchiveEntry(context.Background(), "/toolarge.tar", []string{"./big"}, 4)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err=%v; want too large", err)
	}
}

func TestLoadBackupNetworkInventoryFromArchive_SuccessAndBadJSON(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	type invJSON struct {
		Interfaces []archivedNetworkInterface `json:"interfaces"`
	}
	payload, err := json.Marshal(invJSON{
		Interfaces: []archivedNetworkInterface{{Name: "eth0", MAC: "aa:bb"}},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeTarToFakeFS(t, fakeFS, "/inv.tar", []tarEntry{
		{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: payload},
	})

	inv, used, err := loadBackupNetworkInventoryFromArchive(context.Background(), "/inv.tar")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if used != "./commands/network_inventory.json" {
		t.Fatalf("used=%q", used)
	}
	if inv == nil || len(inv.Interfaces) != 1 || inv.Interfaces[0].Name != "eth0" {
		t.Fatalf("inv=%+v", inv)
	}

	writeTarToFakeFS(t, fakeFS, "/bad.tar", []tarEntry{
		{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: []byte("{")},
	})
	_, _, err = loadBackupNetworkInventoryFromArchive(context.Background(), "/bad.tar")
	if err == nil || !strings.Contains(err.Error(), "parse network inventory json") {
		t.Fatalf("err=%v; want parse network inventory json", err)
	}
}

func TestParseAndReadPermanentMAC(t *testing.T) {
	output := "some header\nPermanent Address: AA:BB:CC:DD:EE:FF \n"
	if got := parsePermanentMAC(output); got != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("parsePermanentMAC=%q", got)
	}
	if got := parsePermanentMAC("nope"); got != "" {
		t.Fatalf("parsePermanentMAC=%q; want empty", got)
	}

	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"ethtool -P eth0": []byte(output),
		},
	}
	restoreCmd = cmd

	got, err := readPermanentMAC(context.Background(), "eth0")
	if err != nil {
		t.Fatalf("readPermanentMAC: %v", err)
	}
	if got != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("readPermanentMAC=%q", got)
	}

	cmd.Errors = map[string]error{"ethtool -P eth0": errors.New("boom")}
	_, err = readPermanentMAC(context.Background(), "eth0")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestReadUdevProperties(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"udevadm info -q property -p /sys/class/net/eth0": []byte(strings.Join([]string{
				"ID_SERIAL=abc",
				"ID_PATH= pci-0000:00:1f.6 ",
				"BADLINE",
				"FOO=",
				"=bar",
				"",
			}, "\n")),
		},
	}
	restoreCmd = cmd

	props, err := readUdevProperties(context.Background(), "/sys/class/net/eth0")
	if err != nil {
		t.Fatalf("readUdevProperties: %v", err)
	}
	if props["ID_SERIAL"] != "abc" || props["ID_PATH"] != "pci-0000:00:1f.6" {
		t.Fatalf("props=%v", props)
	}
	if _, ok := props["FOO"]; ok {
		t.Fatalf("expected empty-value key to be skipped: %v", props)
	}

	cmd.Errors = map[string]error{"udevadm info -q property -p /sys/class/net/eth0": errors.New("boom")}
	_, err = readUdevProperties(context.Background(), "/sys/class/net/eth0")
	if err == nil {
		t.Fatalf("expected error")
	}
}

func TestReadTrimmedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "line")
	if err := os.WriteFile(path, []byte("  HELLO WORLD  \n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := readTrimmedLine(path, 0); got != "HELLO WORLD" {
		t.Fatalf("readTrimmedLine=%q", got)
	}
	if got := readTrimmedLine(path, 5); got != "HELLO" {
		t.Fatalf("readTrimmedLine=%q", got)
	}
	if got := readTrimmedLine(filepath.Join(dir, "missing"), 5); got != "" {
		t.Fatalf("readTrimmedLine=%q; want empty", got)
	}
}

func TestCollectCurrentNetworkInventory_WithFakeSysfs(t *testing.T) {
	origSys := sysClassNetPath
	t.Cleanup(func() { sysClassNetPath = origSys })
	t.Setenv("PATH", "")

	root := t.TempDir()
	netDir := filepath.Join(root, "sys/class/net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir netDir: %v", err)
	}
	sysClassNetPath = netDir
	if err := os.MkdirAll(filepath.Join(netDir, " "), 0o755); err != nil {
		t.Fatalf("mkdir blank-name entry: %v", err)
	}

	writePhysical := func(name, mac, pciDevice, driver string) {
		t.Helper()
		ifaceDir := filepath.Join(netDir, name)
		if err := os.MkdirAll(ifaceDir, 0o755); err != nil {
			t.Fatalf("mkdir ifaceDir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ifaceDir, "address"), []byte(mac+"\n"), 0o644); err != nil {
			t.Fatalf("write address: %v", err)
		}

		devDir := filepath.Join(root, "devices", pciDevice)
		driverDir := filepath.Join(root, "drivers", driver)
		if err := os.MkdirAll(devDir, 0o755); err != nil {
			t.Fatalf("mkdir devDir: %v", err)
		}
		if err := os.MkdirAll(driverDir, 0o755); err != nil {
			t.Fatalf("mkdir driverDir: %v", err)
		}
		if err := os.Symlink(devDir, filepath.Join(ifaceDir, "device")); err != nil {
			t.Fatalf("symlink device: %v", err)
		}
		if err := os.Symlink(driverDir, filepath.Join(devDir, "driver")); err != nil {
			t.Fatalf("symlink driver: %v", err)
		}
	}
	writePhysical("eth0", "MAC:AA:BB:CC:DD:EE:01", "pci0000:00/0000:00:1f.6", "e1000")
	writePhysical("eno1", "aa:bb:cc:dd:ee:02", "pci0000:00/0000:00:1c.0", "igb")

	ifaceLo := filepath.Join(netDir, "lo")
	if err := os.MkdirAll(ifaceLo, 0o755); err != nil {
		t.Fatalf("mkdir lo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ifaceLo, "address"), []byte("00:00:00:00:00:00\n"), 0o644); err != nil {
		t.Fatalf("write lo address: %v", err)
	}

	virtualTarget := filepath.Join(root, "devices/virtual/net/vmbr0")
	if err := os.MkdirAll(virtualTarget, 0o755); err != nil {
		t.Fatalf("mkdir virtualTarget: %v", err)
	}
	if err := os.WriteFile(filepath.Join(virtualTarget, "address"), []byte("aa:aa:aa:aa:aa:aa\n"), 0o644); err != nil {
		t.Fatalf("write vmbr0 address: %v", err)
	}
	if err := os.Symlink(virtualTarget, filepath.Join(netDir, "vmbr0")); err != nil {
		t.Fatalf("symlink vmbr0: %v", err)
	}

	inv, err := collectCurrentNetworkInventory(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if inv == nil || len(inv.Interfaces) != 4 {
		t.Fatalf("inv=%+v", inv)
	}
	if inv.Interfaces[0].Name != "eno1" || inv.Interfaces[1].Name != "eth0" || inv.Interfaces[2].Name != "lo" || inv.Interfaces[3].Name != "vmbr0" {
		t.Fatalf("sorted names=%v", []string{inv.Interfaces[0].Name, inv.Interfaces[1].Name, inv.Interfaces[2].Name, inv.Interfaces[3].Name})
	}
	var gotEth0, gotVmbr0 archivedNetworkInterface
	for _, iface := range inv.Interfaces {
		switch iface.Name {
		case "eth0":
			gotEth0 = iface
		case "vmbr0":
			gotVmbr0 = iface
		}
	}
	if gotEth0.MAC != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("eth0 MAC=%q", gotEth0.MAC)
	}
	if gotEth0.Driver != "e1000" {
		t.Fatalf("eth0 Driver=%q", gotEth0.Driver)
	}
	if !strings.Contains(gotEth0.PCIPath, "devices/pci0000:00/0000:00:1f.6") {
		t.Fatalf("eth0 PCIPath=%q", gotEth0.PCIPath)
	}
	if !gotVmbr0.IsVirtual {
		t.Fatalf("vmbr0 IsVirtual=false")
	}
}

func TestPlanAndApplyNICNameRepair_WithFakeInventory(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSys := sysClassNetPath
	origSeq := atomic.LoadUint64(&nicRepairSequence)
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		sysClassNetPath = origSys
		atomic.StoreUint64(&nicRepairSequence, origSeq)
	})
	t.Setenv("PATH", "")

	// Fake current inventory via fake sysfs.
	root := t.TempDir()
	netDir := filepath.Join(root, "sys/class/net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir netDir: %v", err)
	}
	sysClassNetPath = netDir
	mustWriteFile := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWriteFile(filepath.Join(netDir, "eth0/address"), "aa:bb:cc:dd:ee:01\n")
	mustWriteFile(filepath.Join(netDir, "eno1/address"), "aa:bb:cc:dd:ee:02\n")
	mustWriteFile(filepath.Join(netDir, "lo/address"), "00:00:00:00:00:00\n")

	// Fake backup archive.
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)}

	backupInv := archivedNetworkInventory{
		GeneratedAt: time.Now().Format(time.RFC3339),
		Hostname:    "backup-host",
		Interfaces: []archivedNetworkInterface{
			{Name: "eno1", MAC: "aa:bb:cc:dd:ee:01"},  // maps to current eth0 -> conflict (current eno1 exists)
			{Name: "ens20", MAC: "aa:bb:cc:dd:ee:02"}, // maps to current eno1 -> safe
		},
	}
	payload, err := json.Marshal(backupInv)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeTarToFakeFS(t, fakeFS, "/backup.tar", []tarEntry{
		{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: payload},
	})

	plan, err := planNICNameRepair(context.Background(), "/backup.tar")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan == nil {
		t.Fatalf("plan=nil")
	}
	if plan.SkippedReason != "" {
		t.Fatalf("SkippedReason=%q", plan.SkippedReason)
	}
	if plan.Mapping.BackupSourcePath != "./commands/network_inventory.json" {
		t.Fatalf("BackupSourcePath=%q", plan.Mapping.BackupSourcePath)
	}
	if len(plan.SafeMappings) != 1 || plan.SafeMappings[0].OldName != "ens20" || plan.SafeMappings[0].NewName != "eno1" {
		t.Fatalf("SafeMappings=%+v", plan.SafeMappings)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Mapping.OldName != "eno1" || plan.Conflicts[0].Mapping.NewName != "eth0" {
		t.Fatalf("Conflicts=%+v", plan.Conflicts)
	}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(bytes.NewBuffer(nil))

	// Prepare config to exercise both safe mapping and conflict mapping.
	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte(strings.Join([]string{
		"auto ens20",
		"iface ens20 inet manual",
		"auto eno1",
		"iface eno1 inet manual",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("write interfaces: %v", err)
	}

	// includeConflicts=false: applies only safe mapping (ens2 -> eno1).
	res, err := applyNICNameRepair(logger, plan, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res == nil || res.SkippedReason != "" {
		t.Fatalf("result=%+v", res)
	}
	if len(res.ChangedFiles) != 1 || res.ChangedFiles[0] != "/etc/network/interfaces" {
		t.Fatalf("ChangedFiles=%v", res.ChangedFiles)
	}
	data, err := fakeFS.ReadFile("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "ens20") {
		t.Fatalf("expected ens20 to be replaced:\n%s", string(data))
	}
	if !strings.Contains(string(data), "auto eno1") {
		t.Fatalf("expected eno1 to remain:\n%s", string(data))
	}

	// includeConflicts=true: also applies eno1 -> eth0.
	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte(strings.Join([]string{
		"auto ens20",
		"iface ens20 inet manual",
		"auto eno1",
		"iface eno1 inet manual",
		"",
	}, "\n")), 0o644); err != nil {
		t.Fatalf("rewrite interfaces: %v", err)
	}
	res, err = applyNICNameRepair(logger, plan, true)
	if err != nil {
		t.Fatalf("apply conflicts: %v", err)
	}
	data, err = fakeFS.ReadFile("/etc/network/interfaces")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "ens20") || strings.Contains(string(data), "auto eno1\n") {
		t.Fatalf("expected ens20 and eno1 to be replaced:\n%s", string(data))
	}
	if !strings.Contains(string(data), "auto eth0") {
		t.Fatalf("expected eth0:\n%s", string(data))
	}
}

func TestPlanNICNameRepair_SkipAndErrorBranches(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	plan, err := planNICNameRepair(context.Background(), " ")
	if err != nil || plan == nil || plan.SkippedReason == "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	writeTarToFakeFS(t, fakeFS, "/missing_inv.tar", []tarEntry{
		{Name: "./unrelated", Typeflag: tar.TypeReg, Mode: 0o644, Data: []byte("x")},
	})
	plan, err = planNICNameRepair(context.Background(), "/missing_inv.tar")
	if err != nil || plan == nil || !strings.Contains(plan.SkippedReason, "backup does not include network inventory") {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}

	if err := fakeFS.WriteFile("/bad.zip", []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err = planNICNameRepair(context.Background(), "/bad.zip")
	if err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("err=%v; want unsupported archive format", err)
	}
}

func TestApplyNICNameRepair_SkipBranches(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(bytes.NewBuffer(nil))

	res, err := applyNICNameRepair(logger, nil, false)
	if err != nil || res == nil || res.SkippedReason == "" {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	res, err = applyNICNameRepair(logger, &nicRepairPlan{SkippedReason: "nope"}, false)
	if err != nil || res == nil || !strings.Contains(res.SkippedReason, "nope") {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	res, err = applyNICNameRepair(logger, &nicRepairPlan{
		Conflicts: []nicNameConflict{{Mapping: nicMappingEntry{OldName: "a", NewName: "b"}}},
	}, false)
	if err != nil || res == nil || !strings.Contains(res.SkippedReason, "conflicting NIC mappings") {
		t.Fatalf("res=%+v err=%v", res, err)
	}

	res, err = applyNICNameRepair(logger, &nicRepairPlan{
		SafeMappings: []nicMappingEntry{
			{OldName: "eno1", NewName: "eno1"},
		},
		Conflicts: []nicNameConflict{
			{Mapping: nicMappingEntry{OldName: "eno2", NewName: "eth0"}},
		},
	}, false)
	if err != nil || res == nil || res.SkippedReason != "conflicting NIC mappings detected; skipped by user" {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestMappingHelpersAndEdgeCases(t *testing.T) {
	if !computeNICMapping(nil, nil).IsEmpty() {
		t.Fatalf("expected empty mapping for nil inventories")
	}

	if !hasStableUdevIdentifiers(map[string]string{"ID_SERIAL": "  abc "}) {
		t.Fatalf("expected stable udev identifiers")
	}
	if hasStableUdevIdentifiers(map[string]string{"ID_SERIAL": " "}) {
		t.Fatalf("expected false for blank udev values")
	}

	if shouldAddMapping("a", "a", map[string]struct{}{}) {
		t.Fatalf("expected false for old==new")
	}
	if !shouldAddMapping("a", "b", nil) {
		t.Fatalf("expected true when usedCurrent nil")
	}
	if shouldAddMapping("a", "b", map[string]struct{}{"b": {}}) {
		t.Fatalf("expected false when usedCurrent already contains new")
	}

	if isCandidatePhysicalNIC(archivedNetworkInterface{Name: "lo", MAC: "aa"}) {
		t.Fatalf("expected lo to be non-candidate")
	}
	if isCandidatePhysicalNIC(archivedNetworkInterface{Name: "eth0", IsVirtual: true, MAC: "aa"}) {
		t.Fatalf("expected virtual to be non-candidate")
	}
	if isCandidatePhysicalNIC(archivedNetworkInterface{Name: "eth0"}) {
		t.Fatalf("expected no-identifiers to be non-candidate")
	}
	if !isCandidatePhysicalNIC(archivedNetworkInterface{Name: "eth0", UdevProps: map[string]string{"ID_PATH": "pci-1"}}) {
		t.Fatalf("expected udev identifiers to make candidate")
	}

	if out, changed := applyInterfaceRenameMap("", map[string]string{"a": "b"}); out != "" || changed {
		t.Fatalf("applyInterfaceRenameMap unexpected result: out=%q changed=%v", out, changed)
	}
	if out, changed := applyInterfaceRenameMap("auto a\n", map[string]string{}); out != "auto a\n" || changed {
		t.Fatalf("applyInterfaceRenameMap unexpected result: out=%q changed=%v", out, changed)
	}

	if out, changed := replaceInterfaceToken("", "a", "b"); out != "" || changed {
		t.Fatalf("replaceInterfaceToken unexpected: out=%q changed=%v", out, changed)
	}
	if _, changed := replaceInterfaceToken("auto a\n", "a", "a"); changed {
		t.Fatalf("replaceInterfaceToken should not change when old==new")
	}

	cases := map[byte]bool{
		'a': true,
		'Z': true,
		'0': true,
		'_': true,
		'-': true,
		'.': false,
		' ': false,
	}
	for ch, want := range cases {
		if got := isIfaceNameChar(ch); got != want {
			t.Fatalf("isIfaceNameChar(%q)=%v want %v", string(ch), got, want)
		}
	}

	backup := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "eno1", MAC: "aa:aa", PCIPath: "/pci/1"},
		},
	}
	current := &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "eth0", MAC: "aa:aa", PCIPath: "/pci/1"},
			{Name: "eth1", MAC: "aa:aa", PCIPath: "/pci/2"},
		},
	}
	got := computeNICMapping(backup, current)
	if got.IsEmpty() || got.Entries[0].Method != nicMatchPCIPath {
		t.Fatalf("got=%+v; want pci_path match due to MAC dupes", got)
	}

	backup = &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "eno1", MAC: "bb:bb"},
			{Name: "ens2", MAC: "bb:bb"},
		},
	}
	current = &archivedNetworkInventory{
		Interfaces: []archivedNetworkInterface{
			{Name: "eth0", MAC: "bb:bb"},
		},
	}
	got = computeNICMapping(backup, current)
	if len(got.Entries) != 1 {
		t.Fatalf("Entries=%+v; want 1 due to usedCurrent", got.Entries)
	}
}

func TestPlanNICNameRepair_NoMappingAndCurrentInventoryError(t *testing.T) {
	origFS := restoreFS
	origSys := sysClassNetPath
	t.Cleanup(func() {
		restoreFS = origFS
		sysClassNetPath = origSys
	})
	t.Setenv("PATH", "")

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	t.Run("no mapping", func(t *testing.T) {
		root := t.TempDir()
		netDir := filepath.Join(root, "sys/class/net")
		if err := os.MkdirAll(netDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		sysClassNetPath = netDir
		if err := os.MkdirAll(filepath.Join(netDir, "eth0"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(netDir, "eth0/address"), []byte("aa:bb:cc:dd:ee:01\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		backupInv := archivedNetworkInventory{
			Interfaces: []archivedNetworkInterface{
				{Name: "eth0", MAC: "aa:bb:cc:dd:ee:01"},
			},
		}
		payload, err := json.Marshal(backupInv)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		writeTarToFakeFS(t, fakeFS, "/nomap.tar", []tarEntry{
			{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: payload},
		})

		plan, err := planNICNameRepair(context.Background(), "/nomap.tar")
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if plan == nil || !strings.Contains(plan.SkippedReason, "no NIC rename mapping found") {
			t.Fatalf("plan=%+v", plan)
		}
	})

	t.Run("current inventory error", func(t *testing.T) {
		sysClassNetPath = filepath.Join(t.TempDir(), "does-not-exist")

		backupInv := archivedNetworkInventory{
			Interfaces: []archivedNetworkInterface{
				{Name: "eno1", MAC: "aa:bb"},
			},
		}
		payload, err := json.Marshal(backupInv)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		writeTarToFakeFS(t, fakeFS, "/inv.tar", []tarEntry{
			{Name: "./commands/network_inventory.json", Typeflag: tar.TypeReg, Mode: 0o644, Data: payload},
		})

		_, err = planNICNameRepair(context.Background(), "/inv.tar")
		if err == nil || !strings.Contains(err.Error(), "collect current network inventory") {
			t.Fatalf("err=%v; want collect current network inventory", err)
		}
	})
}

func TestApplyNICNameRepair_NoMatchesAndNoRenamesSelected(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto lo\niface lo inet loopback\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := applyNICNameRepair(nil, &nicRepairPlan{
		SafeMappings: []nicMappingEntry{
			{OldName: "eno1", NewName: "eth0", Method: nicMatchMAC, Identifier: "aa"},
		},
	}, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res == nil || !strings.Contains(res.SkippedReason, "no matching interface names found") {
		t.Fatalf("res=%+v", res)
	}

	res, err = applyNICNameRepair(nil, &nicRepairPlan{
		SafeMappings: []nicMappingEntry{
			{OldName: "eno1", NewName: "eno1"},
		},
	}, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res == nil || res.SkippedReason != "no NIC renames selected" {
		t.Fatalf("res=%+v", res)
	}
}

func TestApplyNICNameRepair_PropagatesRewriteErrors(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}

	if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	fakeFS.MkdirAllErr = errors.New("disk full")

	res, err := applyNICNameRepair(nil, &nicRepairPlan{
		SafeMappings: []nicMappingEntry{{OldName: "eno1", NewName: "eth0"}},
	}, false)
	if err == nil || res != nil {
		t.Fatalf("res=%+v err=%v; want nil result and error", res, err)
	}
}

func TestReadArchiveEntry_CorruptTar(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	if err := fakeFS.WriteFile("/corrupt.tar", []byte("not a tar"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, _, err := readArchiveEntry(context.Background(), "/corrupt.tar", []string{"./x"}, 16)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v; want tar parse error", err)
	}
}

func TestReadArchiveEntry_TruncatedEntryReadError(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	data := []byte("0123456789")
	if err := tw.WriteHeader(&tar.Header{Name: "./x", Mode: 0o644, Size: int64(len(data))}); err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	full := buf.Bytes()
	if len(full) <= 512+5 {
		t.Fatalf("unexpected tar size: %d", len(full))
	}
	truncated := full[:512+5]
	if err := fakeFS.WriteFile("/trunc.tar", truncated, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, _, err := readArchiveEntry(context.Background(), "/trunc.tar", []string{"./x"}, 16)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err=%v; want read error", err)
	}
}

func TestCollectCurrentNetworkInventory_UdevAndPermanentMAC(t *testing.T) {
	origSys := sysClassNetPath
	origCmd := restoreCmd
	t.Cleanup(func() {
		sysClassNetPath = origSys
		restoreCmd = origCmd
	})

	// Make commandAvailable() succeed.
	binDir := t.TempDir()
	for _, name := range []string{"udevadm", "ethtool"} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	root := t.TempDir()
	netDir := filepath.Join(root, "sys/class/net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	sysClassNetPath = netDir

	// eth0 directory.
	if err := os.MkdirAll(filepath.Join(netDir, "eth0"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "eth0/address"), []byte("aa:bb:cc:dd:ee:01\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// eth1 symlink (non-virtual).
	eth1Target := filepath.Join(root, "devices/pci0000:00/eth1")
	if err := os.MkdirAll(eth1Target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(eth1Target, "address"), []byte("aa:bb:cc:dd:ee:02\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(eth1Target, filepath.Join(netDir, "eth1")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cmd := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"udevadm info -q property -p " + filepath.Join(netDir, "eth0"): []byte("ID_SERIAL=abc\n"),
			"ethtool -P eth0": []byte("permanent address: AA:BB:CC:DD:EE:FF\n"),
			"udevadm info -q property -p " + filepath.Join(netDir, "eth1"): []byte("ID_SERIAL=ignored\n"),
			"ethtool -P eth1": []byte("permanent address: 11:22:33:44:55:66\n"),
		},
		Errors: map[string]error{
			"udevadm info -q property -p " + filepath.Join(netDir, "eth1"): errors.New("boom"),
			"ethtool -P eth1": errors.New("boom"),
		},
	}
	restoreCmd = cmd

	inv, err := collectCurrentNetworkInventory(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	var eth0, eth1 archivedNetworkInterface
	for _, iface := range inv.Interfaces {
		switch iface.Name {
		case "eth0":
			eth0 = iface
		case "eth1":
			eth1 = iface
		}
	}
	if eth0.PermanentMAC != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("eth0 PermanentMAC=%q", eth0.PermanentMAC)
	}
	if eth0.UdevProps == nil || eth0.UdevProps["ID_SERIAL"] != "abc" {
		t.Fatalf("eth0 UdevProps=%v", eth0.UdevProps)
	}
	if eth1.IsVirtual {
		t.Fatalf("eth1 IsVirtual=true; want false")
	}
	if eth1.PermanentMAC != "" || eth1.UdevProps != nil {
		t.Fatalf("eth1 should ignore cmd errors: %+v", eth1)
	}
}

func TestRewriteIfupdownConfigFiles_EdgeAndErrorPaths(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
	})

	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)}

	t.Run("empty rename map", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		paths, dir, err := rewriteIfupdownConfigFiles(nil, map[string]string{})
		if err != nil || len(paths) != 0 || dir != "" {
			t.Fatalf("paths=%v dir=%q err=%v", paths, dir, err)
		}
	})

	t.Run("interfaces.d missing but update succeeds", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\niface eno1 inet manual\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		paths, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err != nil || len(paths) != 1 {
			t.Fatalf("paths=%v err=%v", paths, err)
		}
	})

	t.Run("interfaces.d entries skipped", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := fakeFS.MkdirAll("/etc/network/interfaces.d/subdir", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := fakeFS.WriteFile("/etc/network/interfaces.d/ ", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := fakeFS.WriteFile("/etc/network/interfaces.d/extra", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		paths, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err != nil || len(paths) != 2 {
			t.Fatalf("paths=%v err=%v", paths, err)
		}
	})

	t.Run("stat failure skips", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		base := statFailFS{FS: fakeFS, failPath: "/etc/network/interfaces", err: errors.New("boom")}
		restoreFS = base

		paths, dir, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err != nil || len(paths) != 0 || dir != "" {
			t.Fatalf("paths=%v dir=%q err=%v", paths, dir, err)
		}
	})

	t.Run("not regular file skipped", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.MkdirAll("/etc/network/interfaces", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		paths, dir, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err != nil || len(paths) != 0 || dir != "" {
			t.Fatalf("paths=%v dir=%q err=%v", paths, dir, err)
		}
	})

	t.Run("read failure skipped", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		restoreFS = readFileFailFS{FS: fakeFS, failPath: "/etc/network/interfaces", err: errors.New("boom")}

		paths, dir, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err != nil || len(paths) != 0 || dir != "" {
			t.Fatalf("paths=%v dir=%q err=%v", paths, dir, err)
		}
	})

	t.Run("base dir create fails", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		restoreFS = fakeFS

		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		fakeFS.MkdirAllErr = errors.New("disk full")

		_, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err == nil || !strings.Contains(err.Error(), "create nic repair base directory") {
			t.Fatalf("err=%v; want create nic repair base directory", err)
		}
	})

	t.Run("write updated fails", func(t *testing.T) {
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		restoreFS = writeFileFailFS{FS: fakeFS, failPath: "/etc/network/interfaces", err: errors.New("disk full")}

		_, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err == nil || !strings.Contains(err.Error(), "write updated file") {
			t.Fatalf("err=%v; want write updated file", err)
		}
	})
}

func TestRewriteIfupdownConfigFiles_BackupStageErrors(t *testing.T) {
	origFS := restoreFS
	origTime := restoreTime
	origSeq := atomic.LoadUint64(&nicRepairSequence)
	t.Cleanup(func() {
		restoreFS = origFS
		restoreTime = origTime
		atomic.StoreUint64(&nicRepairSequence, origSeq)
	})

	restoreTime = &FakeTime{Current: time.Date(2025, 1, 1, 1, 2, 3, 0, time.UTC)}
	expectedBackupDir := "/tmp/proxsave/nic_repair_20250101_010203_1"
	expectedBackupPath := filepath.Join(expectedBackupDir, "etc/network/interfaces")
	expectedBackupPathDir := filepath.Dir(expectedBackupPath)

	t.Run("create backup dir fails", func(t *testing.T) {
		atomic.StoreUint64(&nicRepairSequence, 0)
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		restoreFS = mkdirAllFailFS{FS: fakeFS, failPath: expectedBackupDir, err: errors.New("boom")}

		_, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err == nil || !strings.Contains(err.Error(), "create nic repair backup directory") {
			t.Fatalf("err=%v; want create nic repair backup directory", err)
		}
	})

	t.Run("create backup path dir fails", func(t *testing.T) {
		atomic.StoreUint64(&nicRepairSequence, 0)
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		restoreFS = mkdirAllFailFS{FS: fakeFS, failPath: expectedBackupPathDir, err: errors.New("boom")}

		_, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err == nil || !strings.Contains(err.Error(), "create backup directory for") {
			t.Fatalf("err=%v; want create backup directory for", err)
		}
	})

	t.Run("write backup file fails", func(t *testing.T) {
		atomic.StoreUint64(&nicRepairSequence, 0)
		fakeFS := NewFakeFS()
		t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
		if err := fakeFS.WriteFile("/etc/network/interfaces", []byte("auto eno1\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		restoreFS = writeFileFailFS{FS: fakeFS, failPath: expectedBackupPath, err: errors.New("disk full")}

		_, _, err := rewriteIfupdownConfigFiles(nil, map[string]string{"eno1": "eth0"})
		if err == nil || !strings.Contains(err.Error(), "write backup file") {
			t.Fatalf("err=%v; want write backup file", err)
		}
	})
}

func TestMapToEntriesAndTokenBoundary(t *testing.T) {
	if got := mapToEntries(map[string]string{}); got != nil {
		t.Fatalf("mapToEntries=%v; want nil", got)
	}
	got := mapToEntries(map[string]string{"b": "B", "a": "A"})
	if len(got) != 2 || got[0].OldName != "a" || got[1].OldName != "b" {
		t.Fatalf("entries=%+v", got)
	}

	if isTokenBoundary("abc", -1, "a") {
		t.Fatalf("expected false for negative idx")
	}
	if isTokenBoundary("abc", 2, "zz") {
		t.Fatalf("expected false for token overflow")
	}
	if isTokenBoundary("xeno1", 1, "eno1") {
		t.Fatalf("expected false for iface-char prefix")
	}
	if isTokenBoundary("eno10", 0, "eno1") {
		t.Fatalf("expected false for iface-char suffix")
	}
	if !isTokenBoundary("eno1", 0, "eno1") {
		t.Fatalf("expected true for token covering full string")
	}

	if out, changed := applyInterfaceRenameMap("auto a\n", map[string]string{"a": "a"}); out != "auto a\n" || changed {
		t.Fatalf("applyInterfaceRenameMap unexpected: out=%q changed=%v", out, changed)
	}
}
