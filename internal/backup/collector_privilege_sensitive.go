package backup

import (
	"os"
	"strconv"
	"strings"
)

const (
	uidMapPath           = "/proc/self/uid_map"
	gidMapPath           = "/proc/self/gid_map"
	systemdContainerPath = "/run/systemd/container"
)

type unprivilegedContainerContext struct {
	Detected bool
	Details  string
}

func (c *Collector) depDetectUnprivilegedContainer() unprivilegedContainerContext {
	if c == nil {
		return unprivilegedContainerContext{}
	}
	if c.deps.DetectUnprivilegedContainer != nil {
		ok, details := c.deps.DetectUnprivilegedContainer()
		return unprivilegedContainerContext{Detected: ok, Details: strings.TrimSpace(details)}
	}
	ok, details := detectUnprivilegedContainer()
	return unprivilegedContainerContext{Detected: ok, Details: details}
}

// detectUnprivilegedContainer attempts to determine whether ProxSave is running in an
// "unprivileged container"-like context where low-level hardware/block access is typically
// restricted.
//
// Implementation note:
//   - We primarily rely on user-namespace UID/GID maps. When UID/GID 0 inside maps to a
//     non-zero host ID, we treat it as "unprivileged" (common for LXC unprivileged containers).
//   - Container flavor is best-effort via /run/systemd/container (if present).
//
// The detection is intentionally conservative in what it changes: it is only used to
// downgrade *known privilege-sensitive command failures* from WARNING to SKIP.
func detectUnprivilegedContainer() (bool, string) {
	uidShifted, uidHost := parseRootIDMapShift(readSmallFile(uidMapPath))
	gidShifted, gidHost := parseRootIDMapShift(readSmallFile(gidMapPath))
	if !uidShifted && !gidShifted {
		return false, ""
	}

	var parts []string
	if uidShifted {
		parts = append(parts, "uid_map=0->"+strconv.FormatUint(uidHost, 10))
	}
	if gidShifted {
		parts = append(parts, "gid_map=0->"+strconv.FormatUint(gidHost, 10))
	}

	if container := strings.TrimSpace(readSmallFile(systemdContainerPath)); container != "" {
		parts = append(parts, "container="+container)
	}

	return true, strings.Join(parts, " ")
}

func readSmallFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	// Avoid leaking NUL-separated content (e.g., /proc/*/environ).
	return strings.ReplaceAll(string(data), "\x00", " ")
}

// parseRootIDMapShift checks whether the mapping for UID/GID 0 is shifted (i.e., maps to a
// non-zero host ID). Returns (true, hostStart) when shifted.
func parseRootIDMapShift(content string) (bool, uint64) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, 0
	}
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		insideStart, err1 := strconv.ParseUint(fields[0], 10, 64)
		hostStart, err2 := strconv.ParseUint(fields[1], 10, 64)
		length, err3 := strconv.ParseUint(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if length == 0 {
			continue
		}
		// We only care about the range that covers "root" inside the namespace (UID/GID 0).
		if insideStart == 0 {
			if hostStart == 0 {
				return false, 0
			}
			return true, hostStart
		}
	}
	return false, 0
}

func isPrivilegeSensitiveFailureCandidate(command string) bool {
	switch command {
	case "dmidecode", "blkid", "sensors", "smartctl":
		return true
	default:
		return false
	}
}

func privilegeSensitiveFailureReason(command string, exitCode int, outputText string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if !isPrivilegeSensitiveFailureCandidate(command) {
		return ""
	}

	lower := strings.ToLower(strings.TrimSpace(outputText))
	hasPerm := containsAny(lower,
		"permission denied",
		"operation not permitted",
		"not permitted",
		"access denied",
	)

	switch command {
	case "dmidecode":
		// dmidecode typically fails due to restricted access to DMI tables (/sys/firmware/dmi or /dev/mem).
		if hasPerm || strings.Contains(lower, "/dev/mem") || strings.Contains(lower, "/sys/firmware/dmi") {
			return "DMI tables not accessible"
		}
	case "blkid":
		// In unprivileged LXC, blkid often exits 2 with empty output when block devices are not accessible.
		if exitCode == 2 && lower == "" {
			return "block devices not accessible; restore hint: automated fstab device remap (UUID/PARTUUID/LABEL) may be limited"
		}
		if hasPerm {
			return "block devices not accessible; restore hint: automated fstab device remap (UUID/PARTUUID/LABEL) may be limited"
		}
	case "sensors":
		// "No sensors found!" is common in virtualized/containerized environments.
		if strings.Contains(lower, "no sensors found") {
			return "no hardware sensors available"
		}
		if hasPerm {
			return "hardware sensors not accessible"
		}
	case "smartctl":
		if hasPerm {
			return "SMART devices not accessible"
		}
	}

	return ""
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle == "" {
			continue
		}
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}
