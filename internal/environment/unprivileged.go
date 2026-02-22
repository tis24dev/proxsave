package environment

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	selfUIDMapPath       = "/proc/self/uid_map"
	selfGIDMapPath       = "/proc/self/gid_map"
	systemdContainerPath = "/run/systemd/container"
)

// UnprivilegedContainerInfo describes whether the current process appears to be
// running in an unprivileged (shifted user namespace) environment.
//
// This is commonly true for unprivileged LXC containers and rootless containers.
type UnprivilegedContainerInfo struct {
	Detected bool
	Details  string
}

// DetectUnprivilegedContainer detects whether the current process appears to be
// running in an unprivileged (shifted user namespace) environment.
//
// Detection is based on /proc/self/{uid_map,gid_map}. When the mapping for
// inside-ID 0 maps to a non-zero outside-ID, the process is in a shifted user
// namespace and likely lacks low-level hardware/block-device privileges.
//
// The return value is intentionally best-effort and never returns an error.
func DetectUnprivilegedContainer() UnprivilegedContainerInfo {
	uidOutside0, uidLen, uidOK := readIDMapOutsideZero(selfUIDMapPath)
	gidOutside0, gidLen, gidOK := readIDMapOutsideZero(selfGIDMapPath)

	detected := (uidOK && uidOutside0 != 0) || (gidOK && gidOutside0 != 0)
	details := make([]string, 0, 3)

	if uidOK {
		details = append(details, fmt.Sprintf("uid_map=0->%d(len=%d)", uidOutside0, uidLen))
	} else {
		details = append(details, "uid_map=unavailable")
	}
	if gidOK {
		details = append(details, fmt.Sprintf("gid_map=0->%d(len=%d)", gidOutside0, gidLen))
	} else {
		details = append(details, "gid_map=unavailable")
	}

	if container := strings.TrimSpace(readAndTrim(systemdContainerPath)); container != "" {
		details = append(details, fmt.Sprintf("container=%s", container))
	}

	out := UnprivilegedContainerInfo{Detected: detected}
	if len(details) > 0 {
		out.Details = strings.Join(details, ", ")
	}
	return out
}

func readIDMapOutsideZero(path string) (outside0, length int64, ok bool) {
	data, err := readFileFunc(path)
	if err != nil {
		return 0, 0, false
	}
	return parseIDMapOutsideZero(string(data))
}

func parseIDMapOutsideZero(content string) (outside0, length int64, ok bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		insideStart, err1 := strconv.ParseInt(fields[0], 10, 64)
		outsideStart, err2 := strconv.ParseInt(fields[1], 10, 64)
		lengthVal, err3 := strconv.ParseInt(fields[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}

		// We only care about the mapping for inside ID 0.
		if insideStart == 0 && lengthVal > 0 {
			return outsideStart, lengthVal, true
		}
	}
	return 0, 0, false
}
