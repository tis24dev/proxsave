package security

import (
	"fmt"
	"os"
	"regexp"
	"testing"
)

// TestIsKernelThread tests kernel thread detection
func TestIsKernelThread(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		expected bool
	}{
		{
			name: "kernel thread - ppid 2, no exe",
			info: procInfo{
				ppid: 2,
				exe:  "",
				comm: "kworker",
			},
			expected: true,
		},
		{
			name: "not kernel thread - ppid 1",
			info: procInfo{
				ppid: 1,
				exe:  "",
				comm: "init",
			},
			expected: false,
		},
		{
			name: "not kernel thread - has exe",
			info: procInfo{
				ppid: 2,
				exe:  "/usr/bin/test",
				comm: "test",
			},
			expected: false,
		},
		{
			name: "not kernel thread - ppid 100",
			info: procInfo{
				ppid: 100,
				exe:  "",
				comm: "user-process",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isKernelThread(tt.info)
			if result != tt.expected {
				t.Errorf("isKernelThread() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsDRBDWorker tests DRBD worker detection
func TestIsDRBDWorker(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		procName string
		expected bool
	}{
		{
			name: "drbd worker",
			info: procInfo{
				ppid: 2,
				comm: "drbd-worker",
			},
			procName: "drbd-worker",
			expected: true,
		},
		{
			name: "DRBD uppercase",
			info: procInfo{
				ppid: 2,
				comm: "DRBD-receiver",
			},
			procName: "DRBD-receiver",
			expected: true,
		},
		{
			name: "drbd but ppid != 2",
			info: procInfo{
				ppid: 1,
				comm: "drbd-worker",
			},
			procName: "drbd-worker",
			expected: false,
		},
		{
			name: "not drbd",
			info: procInfo{
				ppid: 2,
				comm: "kworker",
			},
			procName: "kworker",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDRBDWorker(tt.info, tt.procName)
			if result != tt.expected {
				t.Errorf("isDRBDWorker() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsGPUCRTCWorker tests GPU CRTC worker detection
func TestIsGPUCRTCWorker(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		procName string
		expected bool
	}{
		{
			name: "valid GPU CRTC worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "card0-crtc0",
			expected: true,
		},
		{
			name: "another valid GPU CRTC",
			info: procInfo{
				ppid: 2,
			},
			procName: "card1-crtc2",
			expected: true,
		},
		{
			name: "GPU CRTC but ppid != 2",
			info: procInfo{
				ppid: 1,
			},
			procName: "card0-crtc0",
			expected: false,
		},
		{
			name: "not GPU CRTC pattern",
			info: procInfo{
				ppid: 2,
			},
			procName: "kworker",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isGPUCRTCWorker(tt.info, tt.procName)
			if result != tt.expected {
				t.Errorf("isGPUCRTCWorker() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsKVMWorker tests KVM worker detection
func TestIsKVMWorker(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		procName string
		expected bool
	}{
		{
			name: "KVM worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "kvm-pit",
			expected: true,
		},
		{
			name: "KVM uppercase",
			info: procInfo{
				ppid: 2,
			},
			procName: "KVM-worker",
			expected: true,
		},
		{
			name: "kvm but ppid != 2",
			info: procInfo{
				ppid: 1,
			},
			procName: "kvm-worker",
			expected: false,
		},
		{
			name: "not kvm",
			info: procInfo{
				ppid: 2,
			},
			procName: "xfs-worker",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isKVMWorker(tt.info, tt.procName)
			if result != tt.expected {
				t.Errorf("isKVMWorker() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsZFSWorker tests ZFS worker detection
func TestIsZFSWorker(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		procName string
		expected bool
	}{
		{
			name: "ZFS worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "zfs-io",
			expected: true,
		},
		{
			name: "spa worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "spa_sync",
			expected: true,
		},
		{
			name: "arc worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "arc_prune",
			expected: true,
		},
		{
			name: "txg worker",
			info: procInfo{
				ppid: 2,
			},
			procName: "txg_sync",
			expected: true,
		},
		{
			name: "zfs but ppid != 2",
			info: procInfo{
				ppid: 1,
			},
			procName: "zfs-io",
			expected: false,
		},
		{
			name: "not zfs",
			info: procInfo{
				ppid: 2,
			},
			procName: "kworker",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isZFSWorker(tt.info, tt.procName)
			if result != tt.expected {
				t.Errorf("isZFSWorker() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestIsUserSpaceBracketedWorker tests user-space bracketed worker detection
func TestIsUserSpaceBracketedWorker(t *testing.T) {
	tests := []struct {
		name     string
		info     procInfo
		procName string
		safeList []string
		expected bool
	}{
		{
			name: "valid user-space worker in safe list",
			info: procInfo{
				ppid: 100,
				exe:  "/usr/bin/systemd",
			},
			procName: "systemd-journal",
			safeList: []string{"systemd-journal", "systemd-udevd"},
			expected: true,
		},
		{
			name: "user-space worker not in safe list",
			info: procInfo{
				ppid: 100,
				exe:  "/usr/bin/test",
			},
			procName: "test-worker",
			safeList: []string{"systemd-journal"},
			expected: false,
		},
		{
			name: "kernel thread (ppid 2)",
			info: procInfo{
				ppid: 2,
				exe:  "/usr/bin/systemd",
			},
			procName: "systemd-journal",
			safeList: []string{"systemd-journal"},
			expected: false,
		},
		{
			name: "no exe",
			info: procInfo{
				ppid: 100,
				exe:  "",
			},
			procName: "test-worker",
			safeList: []string{"test-worker"},
			expected: false,
		},
		{
			name: "exe not in trusted dir",
			info: procInfo{
				ppid: 100,
				exe:  "/home/user/malicious",
			},
			procName: "malicious-worker",
			safeList: []string{"malicious-worker"},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isUserSpaceBracketedWorker(tt.info, tt.procName, tt.safeList)
			if result != tt.expected {
				t.Errorf("isUserSpaceBracketedWorker() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// TestReadProcInfo tests reading process information
func TestReadProcInfo(t *testing.T) {
	// Test with current process
	currentPID := os.Getpid()

	info := readProcInfo(currentPID)

	// Current process should have comm set
	if info.comm == "" {
		t.Error("readProcInfo() should return non-empty comm for current process")
	}

	// Current process should have ppid > 0
	if info.ppid <= 0 {
		t.Error("readProcInfo() should return valid ppid for current process")
	}

	// Current process should have exe set
	if info.exe == "" {
		t.Error("readProcInfo() should return non-empty exe for current process")
	}
}

// TestReadProcInfo_InvalidPID tests reading info for invalid PID
func TestReadProcInfo_InvalidPID(t *testing.T) {
	// Use an invalid PID (999999 is unlikely to exist)
	info := readProcInfo(999999)

	// Should return empty info for invalid PID
	if info.comm != "" || info.ppid != 0 || info.exe != "" {
		t.Errorf("readProcInfo() should return empty info for invalid PID, got: comm=%q, ppid=%d, exe=%q",
			info.comm, info.ppid, info.exe)
	}
}

// TestIsHeuristicallySafeKernelProcess tests the main heuristic function
func TestIsHeuristicallySafeKernelProcess(t *testing.T) {
	// Get a real PID to test with (current process)
	currentPID := os.Getpid()

	tests := []struct {
		name       string
		pid        int
		procName   string
		safeBracket []string
		expectedSafe bool
	}{
		{
			name:       "empty name",
			pid:        currentPID,
			procName:   "",
			safeBracket: []string{},
			expectedSafe: false,
		},
		{
			name:       "whitespace only name",
			pid:        currentPID,
			procName:   "   ",
			safeBracket: []string{},
			expectedSafe: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isHeuristicallySafeKernelProcess(tt.pid, tt.procName, tt.safeBracket)
			// For empty/whitespace names, should return false
			if result != tt.expectedSafe {
				t.Errorf("isHeuristicallySafeKernelProcess() = %v, want %v", result, tt.expectedSafe)
			}
		})
	}
}

// TestProcessDetectionPatterns tests various process name patterns
func TestProcessDetectionPatterns(t *testing.T) {
	// Test regex patterns directly
	tests := []struct {
		name    string
		pattern string
		matches []string
		nonMatches []string
	}{
		{
			name:    "DRBD pattern",
			pattern: "drbd",
			matches: []string{"drbd-worker", "DRBD-receiver", "drbd0"},
			nonMatches: []string{"worker", "kvm"},
		},
		{
			name:    "Card CRTC pattern",
			matches: []string{"card0-crtc0", "card1-crtc2", "card10-crtc99"},
			nonMatches: []string{"card", "crtc", "card-crtc", "cardA-crtc0"},
		},
		{
			name:    "ZFS pattern",
			pattern: "zfs",
			matches: []string{"zfs-io", "spa_sync", "arc_prune", "txg_sync", "vdev", "zil"},
			nonMatches: []string{"worker", "kvm"},
		},
		{
			name:    "KVM pattern",
			pattern: "kvm",
			matches: []string{"kvm-pit", "KVM-worker", "kvm-vcpu"},
			nonMatches: []string{"worker", "zfs"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var regex *regexp.Regexp
			switch tt.pattern {
			case "drbd":
				regex = drbdGenericRegex
			case "zfs":
				regex = zfsGenericRegex
			case "kvm":
				regex = kvmGenericRegex
			}

			// Test Card CRTC separately as it doesn't have a named pattern
			if tt.name == "Card CRTC pattern" {
				regex = cardCrtcRegex
			}

			if regex != nil {
				for _, match := range tt.matches {
					if !regex.MatchString(match) {
						t.Errorf("Pattern should match %q", match)
					}
				}
				for _, nonMatch := range tt.nonMatches {
					if regex.MatchString(nonMatch) {
						t.Errorf("Pattern should not match %q", nonMatch)
					}
				}
			}
		})
	}
}

// TestProcInfoParsing tests the parsing of /proc files
func TestProcInfoParsing(t *testing.T) {
	// This test verifies the proc parsing works with the current process
	currentPID := os.Getpid()

	// Test comm file parsing
	commPath := fmt.Sprintf("/proc/%d/comm", currentPID)
	if _, err := os.Stat(commPath); err == nil {
		info := readProcInfo(currentPID)
		if info.comm == "" {
			t.Error("Failed to parse comm file")
		}
	}

	// Test status file parsing for ppid
	statusPath := fmt.Sprintf("/proc/%d/status", currentPID)
	if _, err := os.Stat(statusPath); err == nil {
		info := readProcInfo(currentPID)
		if info.ppid == 0 {
			t.Error("Failed to parse ppid from status file")
		}
	}

	// Test exe symlink reading
	exePath := fmt.Sprintf("/proc/%d/exe", currentPID)
	if _, err := os.Lstat(exePath); err == nil {
		info := readProcInfo(currentPID)
		if info.exe == "" {
			t.Error("Failed to read exe symlink")
		}
	}
}
