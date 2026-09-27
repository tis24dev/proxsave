package orchestrator

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// Command lines measured on the two reference hosts (todo-boot-config section 7 and
// the pve-test measures): mik-pve1 is root ZFS with systemd-boot through
// proxmox-boot-tool, pve-test is root ext4 on LVM with plain GRUB.
const (
	mikPve1ProcCmdline  = "root=ZFS=rpool/ROOT/pve-1 boot=zfs quiet intel_iommu=on iommu=pt pcie_aspm=off"
	pveTestProcCmdline  = "BOOT_IMAGE=/boot/vmlinuz-7.0.2-2-pve root=/dev/mapper/pve-root ro quiet"
	pveTestGrubDefault  = "quiet"
	mikPve1KernelCmdlin = "root=ZFS=rpool/ROOT/pve-1 boot=zfs quiet intel_iommu=on iommu=pt pcie_aspm=off"
)

func TestSplitKernelCmdline(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  quiet   splash \n", []string{"quiet", "splash"}},
		{`quiet dyndbg="file drivers/pci/* +p" iommu=pt`, []string{"quiet", `dyndbg="file drivers/pci/* +p"`, "iommu=pt"}},
		{"a\tb", []string{"a", "b"}},
	}
	for _, tc := range cases {
		if got := splitKernelCmdline(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitKernelCmdline(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMergeKernelCmdline(t *testing.T) {
	cases := []struct {
		name       string
		source     string
		target     string
		wantMerged string
		wantAdded  []string
		wantKept   []string
	}{
		{
			name:       "mik-pve1 backup onto pve-test GRUB default",
			source:     mikPve1ProcCmdline,
			target:     pveTestGrubDefault,
			wantMerged: "quiet intel_iommu=on iommu=pt pcie_aspm=off",
			wantAdded:  []string{"intel_iommu=on", "iommu=pt", "pcie_aspm=off"},
		},
		{
			name:       "pve-test backup onto mik-pve1 kernel cmdline",
			source:     pveTestProcCmdline,
			target:     mikPve1KernelCmdlin,
			wantMerged: mikPve1KernelCmdlin,
		},
		{
			name:       "system tokens never carried",
			source:     `BOOT_IMAGE=/boot/vmlinuz root=/dev/sda1 boot=zfs ro rw initrd=\EFI\proxmox\6.8.12-4-pve\initrd.img-6.8.12-4-pve`,
			target:     "root=ZFS=rpool/ROOT/pve-1 boot=zfs",
			wantMerged: "root=ZFS=rpool/ROOT/pve-1 boot=zfs",
		},
		{
			name:       "key on both sides with different values: target wins",
			source:     "quiet iommu=off intel_iommu=on",
			target:     "quiet iommu=pt",
			wantMerged: "quiet iommu=pt intel_iommu=on",
			wantAdded:  []string{"intel_iommu=on"},
			wantKept:   []string{"iommu=off"},
		},
		{
			name:       "hyphen and underscore name the same kernel parameter",
			source:     "vfio-pci.ids=10de:1b80,10de:10f0",
			target:     "quiet vfio_pci.ids=10de:1b81",
			wantMerged: "quiet vfio_pci.ids=10de:1b81",
			wantKept:   []string{"vfio-pci.ids=10de:1b80,10de:10f0"},
		},
		{
			name:       "token repeated in the backup added once",
			source:     "quiet quiet nomodeset",
			target:     "",
			wantMerged: "quiet nomodeset",
			wantAdded:  []string{"quiet", "nomodeset"},
		},
		{
			name:       "repeated key with distinct values carried when the target lacks it",
			source:     "console=tty0 console=ttyS0,115200",
			target:     "quiet",
			wantMerged: "quiet console=tty0 console=ttyS0,115200",
			wantAdded:  []string{"console=tty0", "console=ttyS0,115200"},
		},
		{
			name:       "quoted value kept whole",
			source:     `quiet dyndbg="file drivers/pci/* +p"`,
			target:     "quiet",
			wantMerged: `quiet dyndbg="file drivers/pci/* +p"`,
			wantAdded:  []string{`dyndbg="file drivers/pci/* +p"`},
		},
		{
			name:       "arguments for init after -- are not kernel parameters",
			source:     "quiet intel_iommu=on -- single",
			target:     "quiet",
			wantMerged: "quiet intel_iommu=on",
			wantAdded:  []string{"intel_iommu=on"},
		},
		{
			name:       "empty backup leaves the target untouched",
			source:     "",
			target:     "  quiet  ",
			wantMerged: "  quiet  ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeKernelCmdline(tc.source, tc.target)
			if got.merged != tc.wantMerged {
				t.Errorf("merged = %q, want %q", got.merged, tc.wantMerged)
			}
			if !reflect.DeepEqual(got.added, tc.wantAdded) {
				t.Errorf("added = %q, want %q", got.added, tc.wantAdded)
			}
			if !reflect.DeepEqual(got.kept, tc.wantKept) {
				t.Errorf("kept = %q, want %q", got.kept, tc.wantKept)
			}
		})
	}
}

const stockGrubDefault = `# If you change this file, run 'update-grub' afterwards to update
# /boot/grub/grub.cfg.

GRUB_DEFAULT=0
GRUB_TIMEOUT=5
GRUB_DISTRIBUTOR=` + "`( . /etc/os-release && echo ${NAME} )`" + `
#GRUB_CMDLINE_LINUX_DEFAULT="quiet splash"
GRUB_CMDLINE_LINUX_DEFAULT="quiet"  # set by the installer
GRUB_CMDLINE_LINUX=""
`

func TestSetGrubCmdlineDefault(t *testing.T) {
	merge := func(current string) string { return mergeKernelCmdline(mikPve1ProcCmdline, current).merged }

	got, current, err := setGrubCmdlineDefault(stockGrubDefault, merge)
	if err != nil {
		t.Fatalf("stock file: %v", err)
	}
	if current != "quiet" {
		t.Fatalf("current = %q, want quiet", current)
	}
	want := strings.Replace(stockGrubDefault,
		`GRUB_CMDLINE_LINUX_DEFAULT="quiet"  # set by the installer`,
		`GRUB_CMDLINE_LINUX_DEFAULT="quiet intel_iommu=on iommu=pt pcie_aspm=off"  # set by the installer`, 1)
	if got != want {
		t.Fatalf("rewritten file:\n%s\nwant:\n%s", got, want)
	}

	single, _, err := setGrubCmdlineDefault("GRUB_CMDLINE_LINUX_DEFAULT='quiet'\n", merge)
	if err != nil || single != "GRUB_CMDLINE_LINUX_DEFAULT='quiet intel_iommu=on iommu=pt pcie_aspm=off'\n" {
		t.Fatalf("single quotes: %q, %v", single, err)
	}
	bare, _, err := setGrubCmdlineDefault("GRUB_CMDLINE_LINUX_DEFAULT=quiet\n", merge)
	if err != nil || bare != "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet intel_iommu=on iommu=pt pcie_aspm=off\"\n" {
		t.Fatalf("unquoted: %q, %v", bare, err)
	}

	// Anything but one literal assignment is not recognized with certainty.
	for name, content := range map[string]string{
		"missing":              "GRUB_TIMEOUT=5\n",
		"two assignments":      "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\nGRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n",
		"expansion":            "GRUB_CMDLINE_LINUX_DEFAULT=\"$GRUB_CMDLINE_LINUX_DEFAULT quiet\"\n",
		"command substitution": "GRUB_CMDLINE_LINUX_DEFAULT=\"`cat /x`\"\n",
		"export":               "export GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n",
		"append":               "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\nGRUB_CMDLINE_LINUX_DEFAULT+=\" splash\"\n",
		"unterminated":         "GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\n",
	} {
		if _, _, err := setGrubCmdlineDefault(content, merge); err == nil {
			t.Errorf("%s: want an error, file would have been rewritten", name)
		}
	}

	// A token the value's quoting cannot carry is refused, not mangled.
	quoted := func(current string) string { return current + ` dyndbg="file x +p"` }
	if _, _, err := setGrubCmdlineDefault("GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n", quoted); err == nil {
		t.Fatal("double quote inside a double-quoted value must be refused")
	}
}

func TestGrubDropInOverridesCmdlineDefault(t *testing.T) {
	cases := map[string]bool{
		"GRUB_DISTRIBUTOR=\"Proxmox VE\"\nGRUB_DISABLE_OS_PROBER=true\n":                       false,
		"GRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX root=ZFS=rpool/ROOT/pve-1 boot=zfs\"\n":      false,
		"# GRUB_CMDLINE_LINUX_DEFAULT=\"quiet\"\n":                                             false,
		"GRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n":                                        true,
		"GRUB_CMDLINE_LINUX_DEFAULT=\"$GRUB_CMDLINE_LINUX_DEFAULT init=/lib/sysvinit/init\"\n": true,
	}
	for content, want := range cases {
		if got := grubDropInSetsCmdlineDefault(content); got != want {
			t.Errorf("grubDropInSetsCmdlineDefault(%q) = %v, want %v", content, got, want)
		}
	}
}

// bootFixture installs a fake filesystem and command runner for detectBootTarget.
func bootFixture(t *testing.T, files map[string]string, outputs map[string]string, errs map[string]error) *FakeCommandRunner {
	t.Helper()
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	for path, content := range files {
		if err := fake.AddFile(path, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &FakeCommandRunner{Outputs: map[string][]byte{}, Errors: errs}
	for k, v := range outputs {
		cmd.Outputs[k] = []byte(v)
	}
	restoreFS = fake
	restoreCmd = cmd
	return cmd
}

const (
	statusUEFI = "System currently booted with uefi\n" +
		"936E-9A3F is configured with: uefi (versions: 6.8.12-4-pve, 6.14.8-2-pve)\n" +
		"936F-0211 is configured with: uefi (versions: 6.8.12-4-pve, 6.14.8-2-pve)\n"
	statusGRUB = "System currently booted with legacy bios\n" +
		"936E-9A3F is configured with: grub (versions: 6.8.12-4-pve)\n"
)

func TestDetectBootTarget(t *testing.T) {
	grubHost := map[string]string{
		"/etc/default/grub":   stockGrubDefault,
		"/boot/grub/grub.cfg": "menuentry\n",
	}
	pbtHost := func(extra map[string]string) map[string]string {
		files := map[string]string{"/etc/kernel/proxmox-boot-uuids": "936E-9A3F\n936F-0211\n"}
		for k, v := range extra {
			files[k] = v
		}
		return files
	}
	which := map[string]string{"which update-grub": "/usr/sbin/update-grub\n"}

	cases := []struct {
		name     string
		files    map[string]string
		outputs  map[string]string
		errs     map[string]error
		wantKind bootLoaderKind
		wantFile string
		wantPBT  bool
	}{
		{name: "plain GRUB (pve-test)", files: grubHost, outputs: which, wantKind: bootLoaderGRUB, wantFile: "/etc/default/grub"},
		{name: "GRUB without grub.cfg", files: map[string]string{"/etc/default/grub": stockGrubDefault}, outputs: which, wantKind: bootLoaderUnknown},
		{name: "GRUB without update-grub", files: grubHost, errs: map[string]error{"which update-grub": errors.New("exit status 1")}, wantKind: bootLoaderUnknown},
		{name: "nothing recognizable", files: map[string]string{}, wantKind: bootLoaderUnknown},
		{
			name:     "systemd-boot via proxmox-boot-tool (mik-pve1)",
			files:    pbtHost(map[string]string{"/etc/kernel/cmdline": mikPve1KernelCmdlin + "\n"}),
			outputs:  map[string]string{"proxmox-boot-tool status": statusUEFI},
			wantKind: bootLoaderSystemdBoot, wantFile: "/etc/kernel/cmdline", wantPBT: true,
		},
		{
			name:     "systemd-boot without /etc/kernel/cmdline",
			files:    pbtHost(nil),
			outputs:  map[string]string{"proxmox-boot-tool status": statusUEFI},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "systemd-boot with a cmdline lacking root=",
			files:    pbtHost(map[string]string{"/etc/kernel/cmdline": "quiet\n"}),
			outputs:  map[string]string{"proxmox-boot-tool status": statusUEFI},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "systemd-boot with a two-line cmdline",
			files:    pbtHost(map[string]string{"/etc/kernel/cmdline": "root=ZFS=rpool/ROOT/pve-1\nquiet\n"}),
			outputs:  map[string]string{"proxmox-boot-tool status": statusUEFI},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "GRUB via proxmox-boot-tool",
			files:    pbtHost(map[string]string{"/etc/default/grub": stockGrubDefault}),
			outputs:  map[string]string{"proxmox-boot-tool status": statusGRUB},
			wantKind: bootLoaderGRUB, wantFile: "/etc/default/grub", wantPBT: true,
		},
		{
			name:  "ESPs disagree",
			files: pbtHost(map[string]string{"/etc/default/grub": stockGrubDefault, "/etc/kernel/cmdline": mikPve1KernelCmdlin}),
			outputs: map[string]string{"proxmox-boot-tool status": "936E-9A3F is configured with: uefi (versions: 6.8.12-4-pve)\n" +
				"936F-0211 is configured with: grub (versions: 6.8.12-4-pve)\n"},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "one ESP with both",
			files:    pbtHost(map[string]string{"/etc/default/grub": stockGrubDefault, "/etc/kernel/cmdline": mikPve1KernelCmdlin}),
			outputs:  map[string]string{"proxmox-boot-tool status": "936E-9A3F is configured with: uefi (versions: 6.8.12-4-pve), grub (versions: 6.8.12-4-pve)\n"},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "status fails",
			files:    pbtHost(map[string]string{"/etc/kernel/cmdline": mikPve1KernelCmdlin}),
			errs:     map[string]error{"proxmox-boot-tool status": errors.New("exit status 2")},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
		{
			name:     "status lists no ESP",
			files:    pbtHost(map[string]string{"/etc/kernel/cmdline": mikPve1KernelCmdlin}),
			outputs:  map[string]string{"proxmox-boot-tool status": "System currently booted with uefi\n"},
			wantKind: bootLoaderUnknown, wantPBT: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bootFixture(t, tc.files, tc.outputs, tc.errs)
			got := detectBootTarget(context.Background(), "/")
			if got.kind != tc.wantKind || got.file != tc.wantFile || got.proxmoxBootTool != tc.wantPBT {
				t.Fatalf("detectBootTarget = %+v, want kind=%v file=%q pbt=%v", got, tc.wantKind, tc.wantFile, tc.wantPBT)
			}
			if got.kind == bootLoaderUnknown && strings.TrimSpace(got.reason) == "" {
				t.Fatal("an unrecognized bootloader must carry the reason")
			}
		})
	}
}

// Every command the boot category and the ZFS host-file checks run must pass the
// safeexec allowlist the production runner goes through: the fakes these tests use
// accept any name, so a command missing from the list fails only on a real host,
// with "command not allowed".
func TestBootAndZFSCommandsAreAllowedBySafeexec(t *testing.T) {
	var names []string
	for _, target := range []bootTarget{
		{kind: bootLoaderGRUB},
		{kind: bootLoaderGRUB, proxmoxBootTool: true},
		{kind: bootLoaderSystemdBoot, proxmoxBootTool: true},
		{kind: bootLoaderUnknown},
	} {
		for _, argv := range bootRebuildCommands(target) {
			names = append(names, argv[0])
		}
	}
	// detectBootTarget and listImportedZFSPools.
	names = append(names, "proxmox-boot-tool", "which", "zpool")
	for _, name := range names {
		if _, err := safeexec.CommandContext(context.Background(), name, "--version"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
