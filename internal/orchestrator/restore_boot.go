package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// The boot restore category carries the kernel parameters an operator wrote on the
// backed-up host (IOMMU, VFIO, ...) onto the host being restored, which is usually a
// new machine. Its source is the old host's EFFECTIVE command line, /proc/cmdline at
// backup time, never its boot files: those name its root device and its ESPs, and
// GRUB settings, /etc/kernel/cmdline and the ESP list stay export-only.
//
// Where the parameters go and how they take effect follow the Proxmox VE admin guide,
// "Host Bootloader" > "Editing the Kernel Commandline": with GRUB they belong in
// GRUB_CMDLINE_LINUX_DEFAULT in /etc/default/grub (applied by update-grub), with
// systemd-boot as one line in /etc/kernel/cmdline (applied by proxmox-boot-tool
// refresh). Which bootloader a proxmox-boot-tool host uses is read from
// `proxmox-boot-tool status`, which prints one "<uuid> is configured with: uefi (...)"
// and/or "grub (...)" line per ESP (proxmox-kernel-helper, src/bin/proxmox-boot-tool).

// bootKernelCmdlineArchivePath is where collectKernelInfo stores /proc/cmdline.
const bootKernelCmdlineArchivePath = "./var/lib/proxsave-info/commands/system/kernel_cmdline.txt"

// bootSystemParams are never carried: they describe the backed-up host's root
// device and boot image, which the target has its own of.
var bootSystemParams = map[string]bool{
	"root":       true,
	"boot":       true,
	"ro":         true,
	"rw":         true,
	"BOOT_IMAGE": true,
	"initrd":     true,
}

// bootNeverLivePaths are archive entries, and the trees under them, that no restore
// writes to the live system, whatever collected them: the old host's GRUB settings
// and kernel command line name its root device and pool, and its ESP list names its
// partitions. The collector keeps them under proxsave-info/boot; CUSTOM_BACKUP_PATHS
// can also put them at these natural paths, which proxsave_info exports. Reproduced
// on a PVE 9.2 VM: the grub.d/zfs.cfg of a ZFS host, written on an LVM host, put a
// second root= on the command line at the next update-grub, and the boot stopped
// in the initramfs.
var bootNeverLivePaths = []string{
	"etc/default/grub",
	"etc/default/grub.d",
	"etc/kernel/cmdline",
	"etc/kernel/proxmox-boot-uuids",
}

func isBootNeverLivePath(clean string) bool {
	return matchesAnyArchivePrefix(clean, bootNeverLivePaths)
}

// bootRebuildInputs are the paths whose restore makes the initramfs stale: module
// options and lists, and the ZFS host id and pool cache, are copied into it.
var bootRebuildInputs = []string{"etc/modprobe.d", "etc/modules", "etc/hostid", "etc/zfs"}

// bootRebuildInput returns the bootRebuildInputs entry an archive entry falls under.
func bootRebuildInput(entryName string) (string, bool) {
	clean := normalizeArchiveEntryPath(entryName)
	for _, p := range bootRebuildInputs {
		if clean == p || strings.HasPrefix(clean, p+"/") {
			return p, true
		}
	}
	return "", false
}

// splitKernelCmdline splits a command line the way the kernel does: on whitespace,
// except inside double quotes, which stay part of the token.
func splitKernelCmdline(line string) []string {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case !inQuote && (r == ' ' || r == '\t' || r == '\n' || r == '\r'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return tokens
}

// splitAtInitArgs separates kernel parameters from the arguments after "--", which
// the kernel hands to init. after is nil when there is no "--".
func splitAtInitArgs(tokens []string) (before, after []string) {
	for i, tok := range tokens {
		if tok == "--" {
			return tokens[:i], append([]string{}, tokens[i+1:]...)
		}
	}
	return tokens, nil
}

// kernelParamKey is the parameter name of a token. The kernel treats dashes and
// underscores in parameter names as the same character, so vfio-pci.ids and
// vfio_pci.ids are one key.
func kernelParamKey(token string) string {
	name := strings.TrimPrefix(token, `"`)
	if i := strings.IndexByte(name, '='); i >= 0 {
		name = name[:i]
	}
	return strings.ReplaceAll(strings.TrimSuffix(name, `"`), "-", "_")
}

type kernelCmdlineMerge struct {
	merged string   // the target line with the carried parameters appended
	added  []string // parameters carried from the backup
	kept   []string // backup parameters not carried: the target sets the same key differently
}

// mergeKernelCmdline carries every parameter of source that is not a system one and
// whose key the target does not already set. When both set a key, the target's
// value stays. A target with nothing to add is returned byte for byte.
func mergeKernelCmdline(source, target string) kernelCmdlineMerge {
	targetParams, targetInit := splitAtInitArgs(splitKernelCmdline(target))
	targetKeys := map[string]bool{}
	targetTokens := map[string]bool{}
	for _, tok := range targetParams {
		targetKeys[kernelParamKey(tok)] = true
		targetTokens[tok] = true
	}

	sourceParams, _ := splitAtInitArgs(splitKernelCmdline(source))
	res := kernelCmdlineMerge{merged: target}
	seen := map[string]bool{}
	for _, tok := range sourceParams {
		key := kernelParamKey(tok)
		if bootSystemParams[key] || seen[tok] {
			continue
		}
		seen[tok] = true
		if targetKeys[key] {
			if !targetTokens[tok] {
				res.kept = append(res.kept, tok)
			}
			continue
		}
		res.added = append(res.added, tok)
	}
	if len(res.added) == 0 {
		return res
	}
	parts := append(append([]string{}, targetParams...), res.added...)
	if targetInit != nil {
		parts = append(append(parts, "--"), targetInit...)
	}
	res.merged = strings.Join(parts, " ")
	return res
}

const grubCmdlineDefaultVar = "GRUB_CMDLINE_LINUX_DEFAULT"

var grubCmdlineDefaultLine = regexp.MustCompile(`^(\s*` + grubCmdlineDefaultVar + `=)(.*)$`)

// grubUnsafeInDoubleQuotes are the characters the shell would expand or end the
// value on inside double quotes; /etc/default/grub is sourced by update-grub.
const grubUnsafeInDoubleQuotes = "\"$`\\"

// grubMentionsCmdlineDefault reports whether an uncommented line names the variable.
func grubMentionsCmdlineDefault(line string) bool {
	trimmed := strings.TrimSpace(line)
	return !strings.HasPrefix(trimmed, "#") && strings.Contains(line, grubCmdlineDefaultVar)
}

// grubDropInSetsCmdlineDefault reports whether a /etc/default/grub.d/*.cfg touches
// GRUB_CMDLINE_LINUX_DEFAULT. update-grub reads the drop-ins after /etc/default/grub,
// so a value written there could be overridden or rebuilt.
func grubDropInSetsCmdlineDefault(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		if grubMentionsCmdlineDefault(line) {
			return true
		}
	}
	return false
}

// setGrubCmdlineDefault rewrites the value of GRUB_CMDLINE_LINUX_DEFAULT with
// merge(current) and leaves every other byte of the file as it was. It refuses
// anything but one plain assignment of a literal value: the file is shell, and a
// value built from expansions cannot be edited with certainty.
func setGrubCmdlineDefault(content string, merge func(current string) string) (updated, current string, err error) {
	lines := strings.Split(content, "\n")
	index := -1
	for i, line := range lines {
		if !grubMentionsCmdlineDefault(line) {
			continue
		}
		if index >= 0 {
			return "", "", fmt.Errorf("%s is set more than once", grubCmdlineDefaultVar)
		}
		index = i
	}
	if index < 0 {
		return "", "", fmt.Errorf("%s is not set", grubCmdlineDefaultVar)
	}
	m := grubCmdlineDefaultLine.FindStringSubmatch(lines[index])
	if m == nil {
		return "", "", fmt.Errorf("%s is not a plain assignment", grubCmdlineDefaultVar)
	}
	prefix, rest := m[1], m[2]

	quote := ""
	value, suffix := rest, ""
	switch {
	case strings.HasPrefix(rest, `"`), strings.HasPrefix(rest, `'`):
		quote = rest[:1]
		end := strings.Index(rest[1:], quote)
		if end < 0 {
			return "", "", fmt.Errorf("%s has an unterminated quote", grubCmdlineDefaultVar)
		}
		value, suffix = rest[1:1+end], rest[2+end:]
	default:
		if i := strings.IndexAny(rest, " \t"); i >= 0 {
			value, suffix = rest[:i], rest[i:]
		}
		if strings.ContainsAny(value, `'"();&|<>`) {
			return "", "", fmt.Errorf("%s is not a literal value", grubCmdlineDefaultVar)
		}
	}
	if quote != `'` && strings.ContainsAny(value, grubUnsafeInDoubleQuotes) {
		return "", "", fmt.Errorf("%s is not a literal value", grubCmdlineDefaultVar)
	}
	if s := strings.TrimSpace(suffix); s != "" && !strings.HasPrefix(s, "#") {
		return "", "", fmt.Errorf("%s is followed by more shell on its line", grubCmdlineDefaultVar)
	}

	next := merge(value)
	if next == value {
		return content, value, nil
	}
	if quote == "" {
		quote = `"`
	}
	if (quote == `'` && strings.Contains(next, `'`)) || (quote == `"` && strings.ContainsAny(next, grubUnsafeInDoubleQuotes)) {
		return "", "", fmt.Errorf("the merged value cannot be quoted in %s", grubCmdlineDefaultVar)
	}
	lines[index] = prefix + quote + next + quote + suffix
	return strings.Join(lines, "\n"), value, nil
}

type bootLoaderKind int

const (
	bootLoaderUnknown bootLoaderKind = iota
	bootLoaderGRUB
	bootLoaderSystemdBoot
)

func (k bootLoaderKind) String() string {
	switch k {
	case bootLoaderGRUB:
		return "GRUB"
	case bootLoaderSystemdBoot:
		return "systemd-boot"
	default:
		return "unknown"
	}
}

// bootTarget is what the restore found out about the live host's bootloader.
type bootTarget struct {
	kind            bootLoaderKind
	file            string // the file holding the kernel command line; empty when unknown
	proxmoxBootTool bool   // /etc/kernel/proxmox-boot-uuids exists
	reason          string // why kind is unknown
}

var proxmoxBootStatusLine = regexp.MustCompile(`^(\S+) is configured with: (.*)$`)

// detectBootTarget recognizes the live host's bootloader, and returns
// bootLoaderUnknown with the reason whenever the answer is not certain:
//
//   - no /etc/kernel/proxmox-boot-uuids: GRUB, when /etc/default/grub and
//     /boot/grub/grub.cfg exist and update-grub is installed;
//   - /etc/kernel/proxmox-boot-uuids: `proxmox-boot-tool status` must succeed and
//     report every ESP with the same single mode. uefi is systemd-boot, and needs a
//     one-line /etc/kernel/cmdline holding root= (refresh refuses one without it);
//     grub is GRUB, and needs /etc/default/grub.
func detectBootTarget(ctx context.Context, destRoot string) bootTarget {
	grubFile := filepath.Join(destRoot, "etc/default/grub")
	cmdlineFile := filepath.Join(destRoot, "etc/kernel/cmdline")
	unknown := func(t bootTarget, format string, args ...interface{}) bootTarget {
		t.kind, t.file, t.reason = bootLoaderUnknown, "", fmt.Sprintf(format, args...)
		return t
	}

	var t bootTarget
	if !bootPathExists(filepath.Join(destRoot, "etc/kernel/proxmox-boot-uuids")) {
		if !bootPathExists(grubFile) {
			return unknown(t, "neither /etc/default/grub nor /etc/kernel/proxmox-boot-uuids exists")
		}
		if !bootPathExists(filepath.Join(destRoot, "boot/grub/grub.cfg")) {
			return unknown(t, "/etc/default/grub exists but /boot/grub/grub.cfg does not")
		}
		if _, err := restoreCmd.Run(ctx, "which", "update-grub"); err != nil {
			return unknown(t, "update-grub is not installed")
		}
		return bootTarget{kind: bootLoaderGRUB, file: grubFile}
	}

	t.proxmoxBootTool = true
	out, err := runCommandStdout(ctx, "proxmox-boot-tool", "status")
	if err != nil {
		return unknown(t, "proxmox-boot-tool status failed: %v", err)
	}
	modes := map[string]bool{}
	esps := 0
	for _, line := range strings.Split(string(out), "\n") {
		m := proxmoxBootStatusLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		esps++
		uefi, grub := strings.Contains(m[2], "uefi ("), strings.Contains(m[2], "grub (")
		switch {
		case uefi && grub:
			return unknown(t, "ESP %s is configured for both uefi and grub", m[1])
		case uefi:
			modes["uefi"] = true
		case grub:
			modes["grub"] = true
		default:
			return unknown(t, "ESP %s is configured with no bootloader", m[1])
		}
	}
	switch {
	case esps == 0:
		return unknown(t, "proxmox-boot-tool status lists no configured ESP")
	case len(modes) > 1:
		return unknown(t, "the ESPs are configured with different bootloaders")
	case modes["uefi"]:
		data, err := restoreFS.ReadFile(cmdlineFile)
		if err != nil {
			return unknown(t, "systemd-boot without a readable /etc/kernel/cmdline: %v", err)
		}
		lines := nonEmptyLines(string(data))
		if len(lines) != 1 {
			return unknown(t, "/etc/kernel/cmdline holds %d lines, not one", len(lines))
		}
		if !hasRootParam(lines[0]) {
			return unknown(t, "/etc/kernel/cmdline has no root= parameter")
		}
		return bootTarget{kind: bootLoaderSystemdBoot, file: cmdlineFile, proxmoxBootTool: true}
	default:
		if !bootPathExists(grubFile) {
			return unknown(t, "the ESPs use GRUB but /etc/default/grub does not exist")
		}
		return bootTarget{kind: bootLoaderGRUB, file: grubFile, proxmoxBootTool: true}
	}
}

func bootPathExists(path string) bool {
	_, err := restoreFS.Stat(path)
	return err == nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func hasRootParam(line string) bool {
	params, _ := splitAtInitArgs(splitKernelCmdline(line))
	for _, tok := range params {
		if kernelParamKey(tok) == "root" {
			return true
		}
	}
	return false
}

// carriedKernelParams lists what the backup would bring, for the log when nothing is
// written.
func carriedKernelParams(source string) string {
	added := mergeKernelCmdline(source, "").added
	if len(added) == 0 {
		return "none"
	}
	return strings.Join(added, " ")
}

// mergeBootKernelCmdline writes the backup's kernel parameters into the boot
// configuration detectBootTarget found. It reports whether a file changed and
// whether a warning was logged; nothing is written when the bootloader or the file
// is not recognized with certainty.
func mergeBootKernelCmdline(logger *logging.Logger, destRoot string, target bootTarget, source string) (changed, warned bool) {
	source = strings.TrimSpace(source)
	if source == "" {
		logger.Warning("Boot configuration - kernel command line missing from the backup: nothing merged")
		return false, true
	}
	logger.Info("Boot configuration - kernel command line of the backed-up host: %s", source)

	if target.kind == bootLoaderUnknown {
		logger.Warning("Boot configuration - bootloader not recognized (%s): nothing written; kernel parameters from the backup: %s", target.reason, carriedKernelParams(source))
		return false, true
	}
	how := ""
	if target.proxmoxBootTool {
		how = " via proxmox-boot-tool"
	}
	logger.Info("Boot configuration - bootloader %s%s, kernel command line in %s", target.kind, how, target.file)

	info, err := restoreFS.Stat(target.file)
	if err != nil {
		logger.Warning("Boot configuration - cannot read %s: %v; kernel parameters from the backup: %s", target.file, err, carriedKernelParams(source))
		return false, true
	}
	data, err := restoreFS.ReadFile(target.file)
	if err != nil {
		logger.Warning("Boot configuration - cannot read %s: %v; kernel parameters from the backup: %s", target.file, err, carriedKernelParams(source))
		return false, true
	}

	var merge kernelCmdlineMerge
	var updated string
	switch target.kind {
	case bootLoaderSystemdBoot:
		merge = mergeKernelCmdline(source, nonEmptyLines(string(data))[0])
		updated = merge.merged + "\n"
	default:
		if dropIn := grubDropInSettingCmdlineDefault(destRoot); dropIn != "" {
			logger.Warning("Boot configuration - %s sets %s after %s: nothing written; kernel parameters from the backup: %s", dropIn, grubCmdlineDefaultVar, target.file, carriedKernelParams(source))
			return false, true
		}
		updated, _, err = setGrubCmdlineDefault(string(data), func(current string) string {
			merge = mergeKernelCmdline(source, current)
			return merge.merged
		})
		if err != nil {
			logger.Warning("Boot configuration - %s in %s not recognized (%v): nothing written; kernel parameters from the backup: %s", grubCmdlineDefaultVar, target.file, err, carriedKernelParams(source))
			return false, true
		}
	}

	if len(merge.kept) > 0 {
		logger.Info("Boot configuration - backup parameters not carried, this host sets them differently: %s", strings.Join(merge.kept, " "))
	}
	if len(merge.added) == 0 {
		logger.Info("Boot configuration - no kernel parameters to add to %s", target.file)
		return false, false
	}
	if err := writeFileAtomic(target.file, []byte(updated), info.Mode().Perm()); err != nil {
		logger.Warning("Boot configuration - failed to write %s: %v; kernel parameters from the backup: %s", target.file, err, strings.Join(merge.added, " "))
		return false, true
	}
	logger.Info("Boot configuration - kernel parameters added to %s: %s", target.file, strings.Join(merge.added, " "))
	return true, false
}

// grubDropInSettingCmdlineDefault returns the first /etc/default/grub.d/*.cfg that
// touches GRUB_CMDLINE_LINUX_DEFAULT, or "".
func grubDropInSettingCmdlineDefault(destRoot string) string {
	dir := filepath.Join(destRoot, "etc/default/grub.d")
	entries, err := restoreFS.ReadDir(dir)
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".cfg") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		data, err := restoreFS.ReadFile(path)
		if err == nil && grubDropInSetsCmdlineDefault(string(data)) {
			return path
		}
	}
	return ""
}

// bootRebuildCommands are run once, in order, at the end of a restore that changed
// the kernel command line or wrote a bootRebuildInputs path. The bootloader step
// runs only on a recognized bootloader: on any other, update-grub could write a
// grub.cfg nothing boots from, or fail.
func bootRebuildCommands(target bootTarget) [][]string {
	cmds := [][]string{{"update-initramfs", "-u", "-k", "all"}}
	switch {
	case target.kind == bootLoaderUnknown:
		return cmds
	case target.proxmoxBootTool:
		return append(cmds, []string{"proxmox-boot-tool", "refresh"})
	default:
		return append(cmds, []string{"update-grub"})
	}
}

// rebuildBootAfterRestore runs bootRebuildCommands. A failed command is a warning
// and the next one still runs; only a cancelled context stops it. The commands have
// no timeout of their own: an update-initramfs killed halfway leaves a truncated
// initrd behind.
func rebuildBootAfterRestore(ctx context.Context, logger *logging.Logger, target bootTarget) (warned bool, err error) {
	if target.kind == bootLoaderUnknown {
		logger.Info("Boot configuration - bootloader not rebuilt: not recognized (%s)", target.reason)
	}
	for _, argv := range bootRebuildCommands(target) {
		if err := ctx.Err(); err != nil {
			return warned, err
		}
		line := strings.Join(argv, " ")
		logger.Info("Boot configuration - running %s", line)
		out, runErr := restoreCmd.Run(ctx, argv[0], argv[1:]...)
		if len(out) > 0 {
			logger.Debug("%s output: %s", argv[0], strings.TrimSpace(string(out)))
		}
		if runErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return warned, ctxErr
			}
			logger.Warning("Boot configuration - %s failed: %v", line, runErr)
			warned = true
		}
	}
	return warned, nil
}

// readBackedUpKernelCmdline returns the backed-up host's /proc/cmdline, or "" when
// the archive does not hold it.
func readBackedUpKernelCmdline(ctx context.Context, logger *logging.Logger, archivePath string) (string, error) {
	data, _, err := readArchiveFile(ctx, logger, archivePath, bootKernelCmdlineArchivePath)
	return string(data), err
}

// readArchiveFile extracts one archive entry into a temporary directory and returns
// its content; found is false when the archive does not hold it.
func readArchiveFile(ctx context.Context, logger *logging.Logger, archivePath, entry string) (data []byte, found bool, err error) {
	dir, cleanup, err := extractArchiveSubset(ctx, logger, archivePath, []string{entry})
	if err != nil {
		return nil, false, err
	}
	defer cleanup()
	return readExtractedFile(dir, entry)
}

// extractArchiveSubset extracts the archive entries matching paths (category path
// syntax) into a temporary directory, in one pass over the archive. cleanup removes
// the directory.
func extractArchiveSubset(ctx context.Context, logger *logging.Logger, archivePath string, paths []string) (dir string, cleanup func(), err error) {
	dir, err = restoreFS.MkdirTemp("", "proxsave-read-")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary directory: %w", err)
	}
	cleanup = func() {
		if err := restoreFS.RemoveAll(dir); err != nil {
			logger.Debug("Failed to remove temporary directory %s: %v", dir, err)
		}
	}
	err = extractArchiveNative(ctx, restoreArchiveOptions{
		archivePath: archivePath,
		destRoot:    dir,
		logger:      logger,
		categories:  []Category{{ID: "read_archive_subset", Paths: paths}},
		mode:        RestoreModeCustom,
		readOnly:    true,
	})
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return dir, cleanup, nil
}

// readExtractedFile reads entry from a directory extractArchiveSubset filled.
func readExtractedFile(dir, entry string) (data []byte, found bool, err error) {
	data, err = restoreFS.ReadFile(filepath.Join(dir, normalizeArchiveEntryPath(entry)))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}
