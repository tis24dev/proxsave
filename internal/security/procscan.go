package security

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

// Heuristic detection for safe kernel-style processes.
// This complements the configured SafeKernelProcesses/SafeBracketProcesses lists
// and helps avoid false positives for common kernel workers (DRBD, CRTC, KVM, ZFS).

var (
	drbdGenericRegex = regexp.MustCompile(`(?i)drbd`)
	cardCrtcRegex    = regexp.MustCompile(`^card[0-9]*-crtc[0-9]+$`)
	zfsGenericRegex  = regexp.MustCompile(`(?i)(zfs|spa|vdev|txg|zil|arc)`)
	kvmGenericRegex  = regexp.MustCompile(`(?i)kvm`)
)

type procInfo struct {
	comm string
	ppid int
	exe  string
}

func readProcInfo(pid int) procInfo {
	info := procInfo{}

	// comm, status and exe are read through one handle on /proc/<pid>, so the three
	// values describe the same process even if it exits and its pid is reused
	// between the reads.
	proc, err := safeexec.OpenProcDir(pid)
	if err != nil {
		return info
	}
	defer func() { _ = proc.Close() }()

	if data, err := proc.ReadFile("comm"); err == nil {
		info.comm = strings.TrimSpace(string(data))
	}

	if data, err := proc.ReadFile("status"); err == nil {
		lines := strings.SplitSeq(string(data), "\n")
		for line := range lines {
			if strings.HasPrefix(line, "PPid:") {
				_, _ = fmt.Sscanf(line, "PPid:\t%d", &info.ppid)
				break
			}
		}
	}

	// The kernel writes the exe link as an absolute path; resolving it gives the
	// binary's real path, and fails for a deleted binary ("<path> (deleted)").
	if link, err := proc.Readlink("exe"); err == nil {
		if target, err := filepath.EvalSymlinks(link); err == nil {
			info.exe = target
		}
	}

	return info
}

// isHeuristicallySafeKernelProcess applies a set of lightweight heuristics to
// decide if a bracket-style process name is a legitimate kernel or system worker.
// It is used as a fallback after configured safe lists have been checked.
func isHeuristicallySafeKernelProcess(pid int, name string, safeBracket []string) bool {
	info := readProcInfo(pid)
	normalized := strings.TrimSpace(name)

	if normalized == "" {
		return false
	}

	if isKernelThread(info) {
		return true
	}
	if isDRBDWorker(info, normalized) {
		return true
	}
	if isGPUCRTCWorker(info, normalized) {
		return true
	}
	if isKVMWorker(info, normalized) {
		return true
	}
	if isZFSWorker(info, normalized) {
		return true
	}

	// Controlled user-space bracketed fallback
	if isUserSpaceBracketedWorker(info, normalized, safeBracket) {
		return true
	}

	return false
}

// (1) Generic kernel thread: parent PID 2, no executable mapped.
func isKernelThread(info procInfo) bool {
	if info.ppid != 2 {
		return false
	}
	if info.exe != "" {
		return false
	}
	return true
}

// (2) DRBD kernel workers: match "drbd" in name with PPid=2.
func isDRBDWorker(info procInfo, name string) bool {
	if info.ppid != 2 {
		return false
	}
	return drbdGenericRegex.MatchString(name)
}

// (3) GPU CRTC workers: card*-crtc* pattern with PPid=2.
func isGPUCRTCWorker(info procInfo, name string) bool {
	if info.ppid != 2 {
		return false
	}
	return cardCrtcRegex.MatchString(name)
}

// (4) KVM workers.
func isKVMWorker(info procInfo, name string) bool {
	if info.ppid != 2 {
		return false
	}
	return kvmGenericRegex.MatchString(name)
}

// (5) ZFS workers.
func isZFSWorker(info procInfo, name string) bool {
	if info.ppid != 2 {
		return false
	}
	return zfsGenericRegex.MatchString(name)
}

// (6) User-space bracketed workers: parent != 2, exe exists, exe in trusted dir,
// and name explicitly whitelisted in SAFE_BRACKET_PROCESSES (higher-level).
func isUserSpaceBracketedWorker(info procInfo, name string, safeList []string) bool {
	if info.ppid == 2 { // kernel thread → non user-space
		return false
	}
	if info.exe == "" {
		return false
	}
	if !isExeInTrustedDir(info.exe) {
		return false
	}

	// must match user-defined safe bracket names
	return slices.Contains(safeList, name)
}

func isExeInTrustedDir(path string) bool {
	trusted := []string{
		"/usr/bin/",
		"/usr/sbin/",
		"/usr/libexec/",
		"/bin/",
		"/sbin/",
		"/lib/systemd/",
	}
	for _, prefix := range trusted {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
