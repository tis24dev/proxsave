package security

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

	if commPath, err := safeexec.ProcPath(pid, "comm"); err == nil {
		if data, err := os.ReadFile(commPath); err == nil {
			info.comm = strings.TrimSpace(string(data))
		}
	}

	if statusPath, err := safeexec.ProcPath(pid, "status"); err == nil {
		if data, err := os.ReadFile(statusPath); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PPid:") {
					_, _ = fmt.Sscanf(line, "PPid:\t%d", &info.ppid)
					break
				}
			}
		}
	}

	if exePath, err := safeexec.ProcPath(pid, "exe"); err == nil {
		if target, err := filepath.EvalSymlinks(exePath); err == nil {
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
	for _, safe := range safeList {
		if safe == name {
			return true
		}
	}

	return false
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
