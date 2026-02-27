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

	proc1CgroupPath = "/proc/1/cgroup"
	selfCgroupPath  = "/proc/self/cgroup"

	dockerMarkerPath = "/.dockerenv"
	podmanMarkerPath = "/run/.containerenv"
)

var (
	getEUIDFunc   = os.Geteuid
	lookupEnvFunc = os.LookupEnv
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

// FilePresenceInfo describes whether a file exists, based on a best-effort
// stat call.
type FilePresenceInfo struct {
	OK        bool
	Present   bool
	StatError string
	Source    string
}

// EnvVarInfo describes a best-effort environment variable lookup.
type EnvVarInfo struct {
	Key   string
	Set   bool
	Value string
}

// UnprivilegedContainerInfo describes whether the current process appears to be
// running in an environment with limited system privileges.
//
// This can be true for unprivileged LXC containers (shifted user namespace),
// rootless containers, non-root execution, and many container runtimes where
// access to low-level system interfaces is intentionally restricted.
type UnprivilegedContainerInfo struct {
	Detected bool
	Details  string

	UIDMap           IDMapOutsideZeroInfo
	GIDMap           IDMapOutsideZeroInfo
	SystemdContainer FileValueInfo

	EnvContainer EnvVarInfo

	DockerMarker FilePresenceInfo
	PodmanMarker FilePresenceInfo

	Proc1CgroupHint FileValueInfo
	SelfCgroupHint  FileValueInfo

	ContainerRuntime string
	ContainerSource  string

	EUID int
}

// DetectUnprivilegedContainer detects whether the current process appears to be
// running in an environment with limited system privileges.
//
// Detection is based on /proc/self/{uid_map,gid_map}. When the mapping for
// inside-ID 0 maps to a non-zero outside-ID, the process is in a shifted user
// namespace and likely lacks low-level hardware/block-device privileges.
//
// In addition, best-effort container heuristics are used to reduce false
// negatives on container runtimes where uid_map is not shifted (for example,
// rootful Docker/Podman or privileged LXC with restricted device access).
//
// The return value is intentionally best-effort and never returns an error.
func DetectUnprivilegedContainer() UnprivilegedContainerInfo {
	uidInfo := readIDMapOutsideZeroInfo(selfUIDMapPath)
	gidInfo := readIDMapOutsideZeroInfo(selfGIDMapPath)
	systemdContainer := readFileValueInfo(systemdContainerPath)
	envContainer := readEnvVarInfo("container")
	dockerMarker := readFilePresenceInfo(dockerMarkerPath)
	podmanMarker := readFilePresenceInfo(podmanMarkerPath)
	proc1CgroupHint := readCgroupHintInfo(proc1CgroupPath)
	selfCgroupHint := readCgroupHintInfo(selfCgroupPath)

	containerRuntime, containerSource := computeContainerRuntime(systemdContainer, envContainer, dockerMarker, podmanMarker, proc1CgroupHint, selfCgroupHint)
	containerRuntime = sanitizeToken(containerRuntime)
	containerSource = sanitizeToken(containerSource)

	euid := getEUIDFunc()
	shifted := (uidInfo.OK && uidInfo.Outside0 != 0) || (gidInfo.OK && gidInfo.Outside0 != 0)
	detected := shifted || euid != 0 || strings.TrimSpace(containerRuntime) != ""

	details := make([]string, 0, 6)
	details = append(details, formatIDMapDetails("uid_map", uidInfo))
	details = append(details, formatIDMapDetails("gid_map", gidInfo))
	details = append(details, formatSimpleDetails("container", containerRuntime, "none"))
	details = append(details, formatSimpleDetails("container_src", containerSource, "none"))
	details = append(details, formatSimpleDetails("euid", fmt.Sprintf("%d", euid), "unknown"))

	out := UnprivilegedContainerInfo{
		Detected:         detected,
		UIDMap:           uidInfo,
		GIDMap:           gidInfo,
		SystemdContainer: systemdContainer,
		EnvContainer:     envContainer,
		DockerMarker:     dockerMarker,
		PodmanMarker:     podmanMarker,
		Proc1CgroupHint:  proc1CgroupHint,
		SelfCgroupHint:   selfCgroupHint,
		ContainerRuntime: containerRuntime,
		ContainerSource:  containerSource,
		EUID:             euid,
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

func readFilePresenceInfo(path string) FilePresenceInfo {
	info := FilePresenceInfo{Source: path}
	if strings.TrimSpace(path) == "" {
		info.StatError = "invalid path"
		return info
	}
	_, err := statFunc(path)
	switch {
	case err == nil:
		info.OK = true
		info.Present = true
		return info
	case errors.Is(err, os.ErrNotExist):
		info.OK = true
		info.Present = false
		return info
	default:
		info.StatError = summarizeReadError(err)
		return info
	}
}

func readEnvVarInfo(key string) EnvVarInfo {
	key = strings.TrimSpace(key)
	if key == "" {
		return EnvVarInfo{}
	}
	value, ok := lookupEnvFunc(key)
	return EnvVarInfo{
		Key:   key,
		Set:   ok,
		Value: strings.TrimSpace(value),
	}
}

func readCgroupHintInfo(path string) FileValueInfo {
	info := FileValueInfo{Source: path}
	data, err := readFileFunc(path)
	if err != nil {
		info.ReadError = summarizeReadError(err)
		return info
	}
	info.OK = true
	info.Value = strings.TrimSpace(containerHintFromCgroup(string(data)))
	return info
}

func computeContainerRuntime(systemdContainer FileValueInfo, envContainer EnvVarInfo, dockerMarker FilePresenceInfo, podmanMarker FilePresenceInfo, proc1CgroupHint FileValueInfo, selfCgroupHint FileValueInfo) (runtime, source string) {
	if systemdContainer.OK {
		if v := strings.TrimSpace(systemdContainer.Value); v != "" {
			return v, "systemd"
		}
	}

	if envContainer.Set {
		if v := strings.TrimSpace(envContainer.Value); v != "" {
			return v, "env"
		}
	}

	if dockerMarker.OK && dockerMarker.Present {
		return "docker", "marker"
	}
	if podmanMarker.OK && podmanMarker.Present {
		return "podman", "marker"
	}

	if proc1CgroupHint.OK {
		if v := strings.TrimSpace(proc1CgroupHint.Value); v != "" {
			return v, "cgroup"
		}
	}
	if selfCgroupHint.OK {
		if v := strings.TrimSpace(selfCgroupHint.Value); v != "" {
			return v, "cgroup"
		}
	}

	return "", ""
}

func containerHintFromCgroup(content string) string {
	lower := strings.ToLower(content)
	switch {
	case strings.Contains(lower, "kubepods"):
		return "kubernetes"
	case strings.Contains(lower, "docker"):
		return "docker"
	case strings.Contains(lower, "libpod"):
		return "podman"
	case strings.Contains(lower, "podman"):
		return "podman"
	case strings.Contains(lower, "lxc"):
		return "lxc"
	case strings.Contains(lower, "containerd"):
		return "containerd"
	default:
		return ""
	}
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

func formatSimpleDetails(label, value, emptyValue string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "value"
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = strings.TrimSpace(emptyValue)
	}
	if value == "" {
		value = "unknown"
	}
	return fmt.Sprintf("%s=%s", label, value)
}

func sanitizeToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToLower(value)

	const maxLen = 64
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		if b.Len() >= maxLen {
			break
		}
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == ':' || r == '/':
			b.WriteRune(r)
		default:
			// Normalize all other characters.
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}
