package orchestrator

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The boot category is common to PVE and PBS, part of FULL, selectable in CUSTOM,
// and outside BASE and STORAGE. Its paths are the source (the backed-up host's
// /proc/cmdline) and the two live files it may write, so the safety backup the
// restore already takes covers them.
func TestBootCategoryDefinition(t *testing.T) {
	cat := GetCategoryByID("boot", GetAllCategories())
	if cat == nil {
		t.Fatal("boot category missing")
	}
	if cat.Type != CategoryTypeCommon || cat.ExportOnly {
		t.Fatalf("boot must be a common, non export-only category: %+v", *cat)
	}
	want := []string{bootKernelCmdlineArchivePath, "./etc/default/grub", "./etc/kernel/cmdline"}
	if strings.Join(cat.Paths, " ") != strings.Join(want, " ") {
		t.Fatalf("boot paths = %v, want %v", cat.Paths, want)
	}
	for _, c := range GetBaseModeCategories() {
		if c.ID == "boot" {
			t.Fatal("boot must not be part of BASE")
		}
	}
	for _, st := range []string{"pve", "pbs", "dual"} {
		for _, c := range GetStorageModeCategories(st) {
			if c.ID == "boot" {
				t.Fatalf("boot must not be part of STORAGE (%s)", st)
			}
		}
	}
	available := AnalyzeArchivePaths([]string{"var/lib/proxsave-info/commands/system/kernel_cmdline.txt"}, GetAllCategories())
	full := GetCategoriesForMode(RestoreModeFull, SystemTypePVE, available)
	if !hasCategoryID(full, "boot") {
		t.Fatalf("FULL must include boot when the archive holds the kernel command line: %v", full)
	}
}

type bootRestoreRun struct {
	fs  *FakeFS
	cmd *FakeCommandRunner
	log string
	err error
}

// runBootRestore runs the real restore workflow on a PVE host whose live files are
// live, from an archive holding archive, with commands answered by outputs/errs.
func runBootRestore(t *testing.T, live, archive, outputs map[string]string, errs map[string]error, mode RestoreMode, categoryIDs ...string) bootRestoreRun {
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

	for path, content := range live {
		if err := fakeFS.AddFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &FakeCommandRunner{Outputs: map[string][]byte{}, Errors: errs}
	for k, v := range outputs {
		cmd.Outputs[k] = []byte(v)
	}
	restoreCmd = cmd

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
		ClusterMode:   "standalone",
		ProxmoxType:   "pve",
		ScriptVersion: "vtest",
	})

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	ui := &fakeRestoreWorkflowUI{mode: mode, confirmRestore: true, confirmCompatible: true}
	for _, id := range categoryIDs {
		ui.categories = append(ui.categories, mustCategoryByID(t, id))
	}
	runErr := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logger, "vtest", ui, "")
	return bootRestoreRun{fs: fakeFS, cmd: cmd, log: buf.String(), err: runErr}
}

func (r bootRestoreRun) read(t *testing.T, path string) string {
	t.Helper()
	data, err := r.fs.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func (r bootRestoreRun) assertAbsent(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if _, err := r.fs.Lstat(p); err == nil {
			t.Errorf("%s was written to the live system", p)
		}
	}
}

func (r bootRestoreRun) count(call string) int {
	n := 0
	for _, c := range r.cmd.CallsList() {
		if c == call {
			n++
		}
	}
	return n
}

func (r bootRestoreRun) index(call string) int {
	for i, c := range r.cmd.CallsList() {
		if c == call {
			return i
		}
	}
	return -1
}

// safetyBackupEntries reads the safety backup the restore took before writing.
func (r bootRestoreRun) safetyBackupEntries(t *testing.T) map[string]string {
	t.Helper()
	entries, err := r.fs.ReadDir(RestoreRunDir())
	if err != nil {
		t.Fatalf("read restore dir: %v", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "restore_backup_") || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		f, err := r.fs.Open(filepath.Join(RestoreRunDir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		out := map[string]string{}
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return out
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(tr)
			out[strings.TrimPrefix(h.Name, "./")] = string(data)
		}
	}
	t.Fatalf("no safety backup in %s: %v", RestoreRunDir(), entries)
	return nil
}

// The archive a ZFS-root systemd-boot host (mik-pve1) produces: its /proc/cmdline,
// its boot files under proxsave-info, and a VFIO module option.
func mikPve1Archive() map[string]string {
	return map[string]string{
		"var/lib/proxsave-info/commands/system/kernel_cmdline.txt": mikPve1ProcCmdline + "\n",
		"var/lib/proxsave-info/boot/etc/default/grub":              "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet intel_iommu=on iommu=pt pcie_aspm=off intremap=off\"\n",
		"var/lib/proxsave-info/boot/etc/default/grub.d/zfs.cfg":    "GRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX root=ZFS=rpool/ROOT/pve-1 boot=zfs\"\n",
		"var/lib/proxsave-info/boot/etc/kernel/cmdline":            mikPve1KernelCmdlin + "\n",
		"var/lib/proxsave-info/boot/etc/kernel/proxmox-boot-uuids": "936E-9A3F\n936F-0211\n",
		"etc/kernel/proxmox-boot-uuids":                            "936E-9A3F\n936F-0211\n",
		"etc/default/zfs":                                          "ZFS_MOUNT=yes\n",
		"etc/modprobe.d/vfio.conf":                                 "options vfio-pci ids=10de:1b80\n",
	}
}

// pve-test's live boot files: root ext4 on LVM, plain GRUB.
func pveTestLive() map[string]string {
	return map[string]string{
		"/etc/default/grub":   stockGrubDefault,
		"/boot/grub/grub.cfg": "menuentry\n",
	}
}

var grubHostOutputs = map[string]string{"which update-grub": "/usr/sbin/update-grub\n"}

// mik-pve1's backup restored onto pve-test: the IOMMU parameters reach
// GRUB_CMDLINE_LINUX_DEFAULT, nothing of the old host's boot files reaches the live
// system, and initramfs and GRUB are rebuilt once, in that order.
func TestBootRestoreMergesIntoGrubAndRebuildsOnce(t *testing.T) {
	r := runBootRestore(t, pveTestLive(), mikPve1Archive(), grubHostOutputs, nil, RestoreModeCustom, "services", "boot")
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}

	wantGrub := strings.Replace(stockGrubDefault, `GRUB_CMDLINE_LINUX_DEFAULT="quiet"`,
		`GRUB_CMDLINE_LINUX_DEFAULT="quiet intel_iommu=on iommu=pt pcie_aspm=off"`, 1)
	if got := r.read(t, "/etc/default/grub"); got != wantGrub {
		t.Fatalf("/etc/default/grub:\n%s\nwant:\n%s", got, wantGrub)
	}
	r.assertAbsent(t, "/etc/default/grub.d/zfs.cfg", "/etc/kernel/cmdline", "/etc/kernel/proxmox-boot-uuids", "/var/lib/proxsave-info")
	if got := r.read(t, "/etc/default/zfs"); got != "ZFS_MOUNT=yes\n" {
		t.Fatalf("services must still restore /etc/default: %q", got)
	}

	if n := r.count("update-initramfs -u -k all"); n != 1 {
		t.Fatalf("update-initramfs ran %d times; calls=%v", n, r.cmd.CallsList())
	}
	if n := r.count("update-grub"); n != 1 {
		t.Fatalf("update-grub ran %d times; calls=%v", n, r.cmd.CallsList())
	}
	if r.index("update-initramfs -u -k all") > r.index("update-grub") {
		t.Fatalf("initramfs must be rebuilt before GRUB; calls=%v", r.cmd.CallsList())
	}
	if n := r.count("proxmox-boot-tool refresh"); n != 0 {
		t.Fatalf("proxmox-boot-tool refresh ran on a host without proxmox-boot-uuids")
	}

	backup := r.safetyBackupEntries(t)
	if backup["etc/default/grub"] != stockGrubDefault {
		t.Fatalf("safety backup must hold the pre-restore /etc/default/grub, got %q", backup["etc/default/grub"])
	}
	for _, want := range []string{
		"Boot configuration - kernel command line of the backed-up host: " + mikPve1ProcCmdline,
		"Boot configuration - bootloader GRUB, kernel command line in /etc/default/grub",
		"Boot configuration - kernel parameters added to /etc/default/grub: intel_iommu=on iommu=pt pcie_aspm=off",
		"Boot configuration - running update-initramfs -u -k all",
		"Boot configuration - running update-grub",
	} {
		if !strings.Contains(r.log, want) {
			t.Errorf("log misses %q:\n%s", want, r.log)
		}
	}
}

// pve-test's backup restored in FULL onto a systemd-boot host: the parameter lands
// in /etc/kernel/cmdline, the host's own ESP list is left alone, the backup's goes
// to the export directory only, and proxmox-boot-tool refresh replaces update-grub.
func TestBootRestoreFullMergesIntoKernelCmdline(t *testing.T) {
	live := map[string]string{
		"/etc/kernel/proxmox-boot-uuids": "AAAA-1111\n",
		"/etc/kernel/cmdline":            "root=ZFS=rpool/ROOT/pve-1 boot=zfs quiet\n",
	}
	archive := map[string]string{
		"var/lib/proxsave-info/commands/system/kernel_cmdline.txt": pveTestProcCmdline + " amd_iommu=on iommu=pt\n",
		"var/lib/proxsave-info/boot/etc/kernel/proxmox-boot-uuids": "936E-9A3F\n",
		"var/lib/proxsave-info/boot/etc/default/grub":              "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n",
	}
	outputs := map[string]string{"proxmox-boot-tool status": "System currently booted with uefi\nAAAA-1111 is configured with: uefi (versions: 6.14.8-2-pve)\n"}
	r := runBootRestore(t, live, archive, outputs, nil, RestoreModeFull)
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}

	if got := r.read(t, "/etc/kernel/cmdline"); got != "root=ZFS=rpool/ROOT/pve-1 boot=zfs quiet amd_iommu=on iommu=pt\n" {
		t.Fatalf("/etc/kernel/cmdline = %q", got)
	}
	if got := r.read(t, "/etc/kernel/proxmox-boot-uuids"); got != "AAAA-1111\n" {
		t.Fatalf("the live ESP list was overwritten: %q", got)
	}
	r.assertAbsent(t, "/etc/default/grub", "/var/lib/proxsave-info")
	if !r.exportHolds(t, "var/lib/proxsave-info/boot/etc/kernel/proxmox-boot-uuids") {
		t.Fatal("the backup's ESP list must reach the export directory")
	}
	if r.count("update-initramfs -u -k all") != 1 || r.count("proxmox-boot-tool refresh") != 1 || r.count("update-grub") != 0 {
		t.Fatalf("rebuild commands: %v", r.cmd.CallsList())
	}
	if r.index("update-initramfs -u -k all") > r.index("proxmox-boot-tool refresh") {
		t.Fatalf("initramfs must be rebuilt before the ESPs are refreshed; calls=%v", r.cmd.CallsList())
	}
	backup := r.safetyBackupEntries(t)
	if backup["etc/kernel/cmdline"] != "root=ZFS=rpool/ROOT/pve-1 boot=zfs quiet\n" {
		t.Fatalf("safety backup must hold the pre-restore /etc/kernel/cmdline, got %q", backup["etc/kernel/cmdline"])
	}
}

// exportHolds reports whether an export directory under /base holds rel.
func (r bootRestoreRun) exportHolds(t *testing.T, rel string) bool {
	t.Helper()
	entries, err := r.fs.ReadDir("/base")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if _, err := r.fs.Stat(filepath.Join("/base", e.Name(), rel)); err == nil {
			return true
		}
	}
	return false
}

// Without the boot category nothing about boot happens, even when the restore
// writes module options: BASE and CUSTOM without boot behave as before.
func TestBootRestoreNotSelectedTouchesNothing(t *testing.T) {
	r := runBootRestore(t, pveTestLive(), mikPve1Archive(), grubHostOutputs, nil, RestoreModeCustom, "services")
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/default/grub"); got != stockGrubDefault {
		t.Fatalf("/etc/default/grub changed without the boot category:\n%s", got)
	}
	for _, c := range []string{"update-initramfs -u -k all", "update-grub", "proxmox-boot-tool refresh", "proxmox-boot-tool status"} {
		if r.count(c) != 0 {
			t.Fatalf("%s ran without the boot category; calls=%v", c, r.cmd.CallsList())
		}
	}
	if strings.Contains(r.log, "Boot configuration") {
		t.Fatalf("boot lines logged without the boot category:\n%s", r.log)
	}
}

// Nothing to add and no initramfs input written: no rebuild.
func TestBootRestoreNothingChangedSkipsRebuild(t *testing.T) {
	live := pveTestLive()
	live["/etc/default/grub"] = strings.Replace(stockGrubDefault, `"quiet"`, `"quiet intel_iommu=on iommu=pt pcie_aspm=off"`, 1)
	archive := map[string]string{"var/lib/proxsave-info/commands/system/kernel_cmdline.txt": mikPve1ProcCmdline + "\n"}
	r := runBootRestore(t, live, archive, grubHostOutputs, nil, RestoreModeCustom, "boot")
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if r.count("update-initramfs -u -k all")+r.count("update-grub") != 0 {
		t.Fatalf("rebuilt with nothing changed; calls=%v", r.cmd.CallsList())
	}
	if !strings.Contains(r.log, "Boot configuration - no kernel parameters to add to /etc/default/grub") {
		t.Fatalf("log:\n%s", r.log)
	}
}

// Nothing to add, but the zfs category wrote /etc/hostid: the initramfs copies it,
// so the rebuild still runs, once.
func TestBootRestoreRebuildsWhenAnInitramfsInputWasWritten(t *testing.T) {
	live := pveTestLive()
	live["/etc/default/grub"] = strings.Replace(stockGrubDefault, `"quiet"`, `"quiet intel_iommu=on iommu=pt pcie_aspm=off"`, 1)
	archive := map[string]string{
		"var/lib/proxsave-info/commands/system/kernel_cmdline.txt": mikPve1ProcCmdline + "\n",
		"etc/hostid": "abcd\n",
	}
	r := runBootRestore(t, live, archive, grubHostOutputs, nil, RestoreModeCustom, "boot", "zfs")
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if r.count("update-initramfs -u -k all") != 1 || r.count("update-grub") != 1 {
		t.Fatalf("rebuild commands: %v", r.cmd.CallsList())
	}
	if !strings.Contains(r.log, "Boot configuration - restore wrote /etc/hostid") {
		t.Fatalf("log:\n%s", r.log)
	}
}

// A bootloader that cannot be recognized with certainty gets nothing written: the
// parameters go to the log as a warning. Initramfs inputs written by other
// categories still get update-initramfs, but the bootloader is not rebuilt.
func TestBootRestoreUnknownBootloaderWritesNothing(t *testing.T) {
	live := map[string]string{"/etc/default/grub": stockGrubDefault} // no grub.cfg, no proxmox-boot-uuids
	r := runBootRestore(t, live, mikPve1Archive(), grubHostOutputs, nil, RestoreModeCustom, "services", "boot")
	if r.err != nil {
		t.Fatalf("restore: %v", r.err)
	}
	if got := r.read(t, "/etc/default/grub"); got != stockGrubDefault {
		t.Fatalf("/etc/default/grub written on an unrecognized bootloader:\n%s", got)
	}
	want := "Boot configuration - bootloader not recognized (/etc/default/grub exists but /boot/grub/grub.cfg does not): nothing written; kernel parameters from the backup: quiet intel_iommu=on iommu=pt pcie_aspm=off"
	if !strings.Contains(r.log, want) {
		t.Fatalf("log misses %q:\n%s", want, r.log)
	}
	if r.count("update-initramfs -u -k all") != 1 {
		t.Fatalf("modprobe.d was written, initramfs must be rebuilt; calls=%v", r.cmd.CallsList())
	}
	if r.count("update-grub")+r.count("proxmox-boot-tool refresh") != 0 {
		t.Fatalf("an unrecognized bootloader must not be rebuilt; calls=%v", r.cmd.CallsList())
	}
	if !strings.Contains(r.log, "Boot configuration - bootloader not rebuilt: not recognized") {
		t.Fatalf("log:\n%s", r.log)
	}
}

// A failed rebuild command is a warning with the command and its error; the next
// command still runs and the restore completes.
func TestBootRestoreRebuildFailureIsAWarning(t *testing.T) {
	errs := map[string]error{"update-initramfs -u -k all": errors.New("exit status 1")}
	r := runBootRestore(t, pveTestLive(), mikPve1Archive(), grubHostOutputs, errs, RestoreModeCustom, "services", "boot")
	if r.err != nil {
		t.Fatalf("a failed rebuild must not fail the restore: %v", r.err)
	}
	if !strings.Contains(r.log, "Boot configuration - update-initramfs -u -k all failed: exit status 1") {
		t.Fatalf("log:\n%s", r.log)
	}
	if r.count("update-grub") != 1 {
		t.Fatalf("update-grub must still run; calls=%v", r.cmd.CallsList())
	}
	if !strings.Contains(r.log, "Restore completed with warnings.") {
		t.Fatalf("the restore must end with warnings:\n%s", r.log)
	}
}

// The ESP list names the partitions of the host that made the backup. With
// CUSTOM_BACKUP_PATHS=/etc/kernel it sits at its natural path in the archive; the
// analysis-failure fallback extracts everything else, but never that.
func TestFullRestoreFallbackNeverWritesProxmoxBootUUIDs(t *testing.T) {
	origRestoreFS, origRestoreCmd, origRestoreSystem := restoreFS, restoreCmd, restoreSystem
	origCompatFS, origPrepare, origAnalyze, origSafetyFS := compatFS, prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS
	t.Cleanup(func() {
		restoreFS, restoreCmd, restoreSystem = origRestoreFS, origRestoreCmd, origRestoreSystem
		compatFS, prepareRestoreBundleFunc, analyzeRestoreArchiveFunc, safetyFS = origCompatFS, origPrepare, origAnalyze, origSafetyFS
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS, compatFS, safetyFS = fakeFS, fakeFS, fakeFS
	restoreCmd = runOnlyRunner{}
	restoreSystem = fakeSystemDetector{systemType: SystemTypePVE}
	if err := fakeFS.AddFile("/etc/kernel/proxmox-boot-uuids", []byte("AAAA-1111\n")); err != nil {
		t.Fatal(err)
	}

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/hosts":                     "127.0.0.1 localhost\n",
		"etc/kernel/proxmox-boot-uuids": "936E-9A3F\n",
	}); err != nil {
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
		CreatedAt: time.Unix(1700000000, 0), ClusterMode: "standalone", ProxmoxType: "pve", ScriptVersion: "vtest",
	})
	analyzeRestoreArchiveFunc = func(string, *logging.Logger) ([]Category, *RestoreDecisionInfo, error) {
		return nil, nil, errors.New("boom")
	}
	ui := &fakeRestoreWorkflowUI{confirmRestore: true, confirmCompatible: true}
	if err := runRestoreWorkflowWithUI(context.Background(), &config.Config{BaseDir: "/base"}, logging.New(types.LogLevelError, false), "vtest", ui, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/hosts"); err != nil {
		t.Fatalf("the fallback must still extract /etc/hosts: %v", err)
	}
	if got, _ := fakeFS.ReadFile("/etc/kernel/proxmox-boot-uuids"); string(got) != "AAAA-1111\n" {
		t.Fatalf("the backed-up ESP list reached the live system: %q", got)
	}
}

// The selective extraction drops the ESP list too, whatever category would match it.
func TestSystemPathExtractionNeverWritesProxmoxBootUUIDs(t *testing.T) {
	origRestoreFS := restoreFS
	t.Cleanup(func() { restoreFS = origRestoreFS })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	tmpTar := filepath.Join(t.TempDir(), "bundle.tar")
	if err := writeTarFile(tmpTar, map[string]string{
		"etc/kernel/proxmox-boot-uuids":            "936E-9A3F\n",
		"etc/kernel/postinst.d/zz-proxmox-example": "#!/bin/sh\n",
	}); err != nil {
		t.Fatal(err)
	}
	tarBytes, err := os.ReadFile(tmpTar)
	if err != nil {
		t.Fatal(err)
	}
	if err := fakeFS.WriteFile("/bundle.tar", tarBytes, 0o640); err != nil {
		t.Fatal(err)
	}
	w := &restoreUIWorkflowRun{
		ctx:      context.Background(),
		logger:   logging.New(types.LogLevelError, false),
		prepared: &preparedBundle{ArchivePath: "/bundle.tar"},
		destRoot: "/",
		mode:     RestoreModeCustom,
		plan:     &RestorePlan{NormalCategories: []Category{{ID: "kernel_dir", Paths: []string{"./etc/kernel/"}}}},
	}
	if err := w.extractNormalCategories(); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/kernel/postinst.d/zz-proxmox-example"); err != nil {
		t.Fatalf("control: the rest of the category must be written: %v", err)
	}
	if _, err := fakeFS.Lstat("/etc/kernel/proxmox-boot-uuids"); err == nil {
		t.Fatal("the backed-up ESP list reached the live system")
	}
}
