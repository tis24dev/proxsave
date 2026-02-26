package environment

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	selfUIDMapPath       = "/proc/self/uid_map"
	selfGIDMapPath       = "/proc/self/gid_map"
	systemdContainerPath = "/run/systemd/container"
)

// IDMapOutsideZeroInfo describes the mapping information for inside ID 0
// parsed from /proc/self/{uid_map,gid_map}.
type IDMapOutsideZeroInfo struct {
	OK          bool
	Outside0    int64
	Length      int64
	ReadError   string
	ParseError  string
	SourcePath  string
	RawEvidence string // best-effort, sanitized single-line hint
}

// FileValueInfo describes a best-effort file read with a trimmed value.
type FileValueInfo struct {
	OK        bool
	Value     string
	ReadError string
	Source    string
}

// UnprivilegedContainerInfo describes whether the current process appears to be
// running in an unprivileged (shifted user namespace) environment.
//
// This is commonly true for unprivileged LXC containers and rootless containers.
type UnprivilegedContainerInfo struct {
	Detected bool
	Details  string

	UIDMap           IDMapOutsideZeroInfo
	GIDMap           IDMapOutsideZeroInfo
	SystemdContainer FileValueInfo
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
	uidInfo := readIDMapOutsideZeroInfo(selfUIDMapPath)
	gidInfo := readIDMapOutsideZeroInfo(selfGIDMapPath)
	containerInfo := readFileValueInfo(systemdContainerPath)

	detected := (uidInfo.OK && uidInfo.Outside0 != 0) || (gidInfo.OK && gidInfo.Outside0 != 0)
	details := make([]string, 0, 3)

	details = append(details, formatIDMapDetails("uid_map", uidInfo))
	details = append(details, formatIDMapDetails("gid_map", gidInfo))
	details = append(details, formatFileValueDetails("container", containerInfo))

	out := UnprivilegedContainerInfo{
		Detected:         detected,
		UIDMap:           uidInfo,
		GIDMap:           gidInfo,
		SystemdContainer: containerInfo,
	}
	out.Details = strings.Join(details, ", ")
	return out
}

func readIDMapOutsideZeroInfo(path string) IDMapOutsideZeroInfo {
	info := IDMapOutsideZeroInfo{SourcePath: path}
	data, err := readFileFunc(path)
	if err != nil {
		info.ReadError = summarizeReadError(err)
		return info
	}

	outside0, length, ok := parseIDMapOutsideZero(string(data))
	if ok {
		info.OK = true
		info.Outside0 = outside0
		info.Length = length
		info.RawEvidence = fmt.Sprintf("0 %d %d", outside0, length)
		return info
	}

	info.ParseError = "no mapping for inside ID 0"
	return info
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

func readFileValueInfo(path string) FileValueInfo {
	info := FileValueInfo{Source: path}
	data, err := readFileFunc(path)
	if err != nil {
		info.ReadError = summarizeReadError(err)
		return info
	}
	info.OK = true
	info.Value = strings.TrimSpace(string(data))
	return info
}

func summarizeReadError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "not found"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	default:
		// Keep it stable; avoid leaking potentially noisy OS-specific details.
		return "error"
	}
}

func formatIDMapDetails(label string, info IDMapOutsideZeroInfo) string {
	if strings.TrimSpace(label) == "" {
		label = "id_map"
	}
	switch {
	case info.OK:
		return fmt.Sprintf("%s=0->%d(len=%d)", label, info.Outside0, info.Length)
	case info.ReadError != "":
		return fmt.Sprintf("%s=unavailable(err=%s)", label, info.ReadError)
	case info.ParseError != "":
		return fmt.Sprintf("%s=unparseable(err=%s)", label, info.ParseError)
	default:
		return fmt.Sprintf("%s=unavailable", label)
	}
}

func formatFileValueDetails(label string, info FileValueInfo) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "value"
	}
	switch {
	case info.OK && strings.TrimSpace(info.Value) != "":
		return fmt.Sprintf("%s=%s", label, strings.TrimSpace(info.Value))
	case info.OK:
		return fmt.Sprintf("%s=empty", label)
	case info.ReadError != "":
		return fmt.Sprintf("%s=unavailable(err=%s)", label, info.ReadError)
	default:
		return fmt.Sprintf("%s=unavailable", label)
	}
}
