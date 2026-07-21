package environment

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/types"
)

const (
	defaultPVEVersionFile = "/etc/pve-manager/version"
	defaultPVELegacyFile  = "/etc/pve/pve.version"
	defaultPBSVersionFile = "/etc/proxmox-backup/version"
)

var (
	pveVersionFile = defaultPVEVersionFile
	pveLegacyFile  = defaultPVELegacyFile
	pbsVersionFile = defaultPBSVersionFile

	// dpkgStatusFile is the Debian package database. Under a mounted host prefix it
	// is the persistent, authoritative record of what is installed AND its version,
	// unlike the pmxcfs-backed version files which vanish when the /etc/pve bind is
	// not mounted. It is the reliable offline version source for both PVE and PBS.
	dpkgStatusFile = "/var/lib/dpkg/status"

	// pveClusterDB is the pmxcfs SQLite backing store. It exists on every PVE host
	// (clustered or standalone), lives on the persistent root filesystem, and
	// survives with no pmxcfs FUSE bind, so it identifies a mounted PVE host even
	// when /etc/pve is an empty mountpoint. Nothing but PVE creates it.
	pveClusterDB = "/var/lib/pve-cluster/config.db"

	// pveBinaryCandidates and pbsBinaryCandidates are product-specific binaries
	// installed under /usr, present whenever the mount carries /usr even if /etc and
	// /var are excluded. They are only stat-probed, never executed (executing a host
	// binary from inside the backup appliance would answer for the appliance).
	pveBinaryCandidates = []string{"/usr/bin/pmxcfs", "/usr/bin/pveversion", "/usr/bin/pvesh", "/usr/sbin/qm", "/usr/sbin/pct"}
	// PBS server binaries only (proxmox-backup-proxy/manager); the client
	// (proxmox-backup-client) ships on PVE hosts too, so it is excluded to avoid a
	// false PBS positive on a PVE-only host.
	pbsBinaryCandidates = []string{"/usr/sbin/proxmox-backup-proxy", "/usr/bin/proxmox-backup-manager", "/usr/sbin/proxmox-backup-manager"}

	// Package data directories, on-disk and product-specific.
	pveShareDir = "/usr/share/pve-manager"
	pbsShareDir = "/usr/share/proxmox-backup"

	additionalPaths = []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin"}

	pveDirCandidates = []string{
		"/etc/pve",
		"/var/lib/pve-cluster",
	}

	pbsDirCandidates = []string{
		"/etc/proxmox-backup",
		"/var/lib/proxmox-backup",
	}

	pveSourceFiles = []string{
		"/etc/apt/sources.list.d/proxmox.list",
	}

	pbsSourceFiles = []string{
		"/etc/apt/sources.list.d/pbs.list",
		"/etc/apt/sources.list.d/proxmox.list",
	}

	lookPathFunc = exec.LookPath

	readFileFunc  = os.ReadFile
	statFunc      = os.Stat
	mkdirAllFunc  = os.MkdirAll
	writeFileFunc = os.WriteFile
	getwdFunc     = os.Getwd

	userCurrentFunc = user.Current
	timeNowFunc     = time.Now

	commandTimeout = 5 * time.Second
	debugBaseDir   = "/tmp"

	runCommandFunc = runCommand

	// rootPrefix re-anchors the hardcoded detection paths under a mounted host
	// filesystem (SYSTEM_ROOT_PREFIX). Empty means detect against the real root,
	// the historical behavior. It is a package-level seam, like statFunc and
	// readFileFunc above, set only by DetectWith for the duration of a single
	// bootstrap detection, which runs before any concurrent goroutine exists.
	rootPrefix string
)

// DetectProxmoxType detects whether the system is running Proxmox VE or Proxmox Backup Server
func DetectProxmoxType() types.ProxmoxType {
	info, _ := detectEnvironmentInfo()
	return info.Type
}

// GetVersion returns the version string of the detected Proxmox system
func GetVersion(pType types.ProxmoxType) (string, error) {
	extendPath()

	switch pType {
	case types.ProxmoxVE:
		if version, ok := detectPVE(); ok && version != "" && version != "unknown" {
			return version, nil
		}
		return "", fmt.Errorf("unable to determine Proxmox VE version")
	case types.ProxmoxBS:
		if version, ok := detectPBS(); ok && version != "" && version != "unknown" {
			return version, nil
		}
		return "", fmt.Errorf("unable to determine Proxmox Backup Server version")
	case types.ProxmoxDual:
		info, err := detectEnvironmentInfo()
		if err != nil {
			return "", err
		}
		if info.Type != types.ProxmoxDual || info.Version == "" || info.Version == "unknown" {
			return "", fmt.Errorf("unable to determine dual Proxmox versions")
		}
		return info.Version, nil
	default:
		return "", fmt.Errorf("unknown proxmox type: %s", pType)
	}
}

// EnvironmentInfo holds information about the current Proxmox environment
type EnvironmentInfo struct {
	Type       types.ProxmoxType
	Version    string
	PVEVersion string
	PBSVersion string
}

// Detect detects the Proxmox environment and returns detailed information
func Detect() (*EnvironmentInfo, error) {
	return DetectWith(DetectOptions{})
}

// DetectOptions tunes detection. RootPrefix, when set, points detection at a
// Proxmox host filesystem mounted read-only under that prefix (SYSTEM_ROOT_PREFIX),
// so ProxSave running inside an HA-LXC backup appliance detects the host rather
// than the container it runs in (issue #255).
type DetectOptions struct {
	RootPrefix string
}

// DetectWith detects the Proxmox environment under the given options. With an
// empty RootPrefix it is identical to the historical Detect(): detection reads the
// real root and probes host commands. With a RootPrefix it re-anchors the
// detection paths under the prefix and skips the host command probes, because
// pveversion/proxmox-backup-manager executed inside the container answer for the
// container, not for the mounted host.
func DetectWith(opts DetectOptions) (*EnvironmentInfo, error) {
	prev := rootPrefix
	rootPrefix = strings.TrimSpace(opts.RootPrefix)
	defer func() { rootPrefix = prev }()

	info, err := detectEnvironmentInfo()
	if info.Type == types.ProxmoxUnknown {
		if err != nil {
			return info, err
		}
		return info, fmt.Errorf("unable to detect Proxmox environment")
	}
	return info, err
}

// hostRooted reports whether detection is re-anchored under a host prefix.
func hostRooted() bool {
	return rootPrefix != "" && rootPrefix != string(filepath.Separator)
}

// resolveUnderPrefix re-anchors an absolute detection path under rootPrefix. The
// detection paths are fixed literals (never attacker-controlled), so a plain join
// mirroring collector.systemPath is sufficient and matches how the rest of
// collection resolves system paths under the prefix.
func resolveUnderPrefix(path string) string {
	if !hostRooted() {
		return path
	}
	return filepath.Join(rootPrefix, strings.TrimPrefix(path, string(filepath.Separator)))
}

func detectEnvironmentInfo() (*EnvironmentInfo, error) {
	extendPath()

	pveVersion, hasPVE := detectPVE()
	pbsVersion, hasPBS := detectPBS()

	info := &EnvironmentInfo{
		Type:       resolveType(hasPVE, hasPBS),
		PVEVersion: normalizedDetectedVersion(pveVersion),
		PBSVersion: normalizedDetectedVersion(pbsVersion),
	}
	info.Version = combineVersions(info.PVEVersion, info.PBSVersion)

	if info.Type != types.ProxmoxUnknown {
		return info, nil
	}

	debugPath := writeDetectionDebug()
	if debugPath != "" {
		return info, fmt.Errorf("unable to detect Proxmox environment (debug saved to %s)", debugPath)
	}
	return info, fmt.Errorf("unable to detect Proxmox environment")
}

func resolveType(hasPVE, hasPBS bool) types.ProxmoxType {
	switch {
	case hasPVE && hasPBS:
		return types.ProxmoxDual
	case hasPVE:
		return types.ProxmoxVE
	case hasPBS:
		return types.ProxmoxBS
	default:
		return types.ProxmoxUnknown
	}
}

func normalizedDetectedVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || strings.EqualFold(version, "unknown") {
		return ""
	}
	return version
}

func combineVersions(pveVersion, pbsVersion string) string {
	switch {
	case pveVersion != "" && pbsVersion != "":
		return fmt.Sprintf("pve=%s,pbs=%s", pveVersion, pbsVersion)
	case pveVersion != "":
		return pveVersion
	case pbsVersion != "":
		return pbsVersion
	default:
		return "unknown"
	}
}

func detectPVE() (string, bool) {
	if !hostRooted() {
		if version, ok := detectPVEViaCommand(); ok {
			return version, true
		}
	}

	if version, ok := detectPVEViaVersionFiles(); ok {
		return version, true
	}

	// dpkg is version-bearing and reliable offline, so it precedes the version-less
	// markers below and recovers the real version even when the pmxcfs version files
	// are absent.
	if version, ok := dpkgPackageInstalled("pve-manager"); ok {
		return version, true
	}

	if fileExists(pveClusterDB) {
		return "unknown", true
	}

	if fileExistsAny(pveBinaryCandidates) {
		return "unknown", true
	}

	if dirExists(pveShareDir) {
		return "unknown", true
	}

	if ok := detectPVEViaSources(); ok {
		return "unknown", true
	}

	if ok := detectViaDirectories(pveDirCandidates); ok {
		return "unknown", true
	}

	return "", false
}

func detectPBS() (string, bool) {
	if !hostRooted() {
		if version, ok := detectPBSViaCommand(); ok {
			return version, true
		}
	}

	if version, ok := detectPBSViaVersionFile(); ok {
		return version, true
	}

	if version, ok := dpkgPackageInstalled("proxmox-backup-server"); ok {
		return version, true
	}

	if fileExistsAny(pbsBinaryCandidates) {
		return "unknown", true
	}

	if dirExists(pbsShareDir) {
		return "unknown", true
	}

	if ok := detectPBSViaSources(); ok {
		return "unknown", true
	}

	if ok := detectViaDirectories(pbsDirCandidates); ok {
		return "unknown", true
	}

	return "", false
}

// fileExistsAny reports whether any candidate resolves to a regular file under the
// active prefix (fileExists applies resolveUnderPrefix).
func fileExistsAny(paths []string) bool {
	for _, p := range paths {
		if fileExists(p) {
			return true
		}
	}
	return false
}

// dpkgPackageInstalled reports whether the dpkg status database under the active
// prefix records pkg as installed, returning its Version. It matches an anchored
// "Package:" field plus a "Status: ... installed" line so a Depends: mention in
// another stanza, or a residual "deinstall ok config-files" entry, never counts as
// installed. Stanzas are blank-line separated.
func dpkgPackageInstalled(pkg string) (string, bool) {
	data, err := readFileFunc(resolveUnderPrefix(dpkgStatusFile))
	if err != nil {
		return "", false
	}
	for _, stanza := range strings.Split(string(data), "\n\n") {
		if dpkgStanzaField(stanza, "Package") != pkg {
			continue
		}
		if !strings.HasSuffix(dpkgStanzaField(stanza, "Status"), " installed") {
			return "", false
		}
		version := dpkgStanzaField(stanza, "Version")
		if version == "" {
			version = "unknown"
		}
		return version, true
	}
	return "", false
}

// dpkgStanzaField returns the trimmed value of the first "Key: value" line in a
// stanza, anchored at column 0 so an indented continuation line never matches.
func dpkgStanzaField(stanza, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(stanza, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(line[len(prefix):])
		}
	}
	return ""
}

func detectPVEViaCommand() (string, bool) {
	cmdPath, err := lookPathFunc("pveversion")
	if err != nil {
		return "", false
	}

	output, err := runCommandFunc(cmdPath)
	if err != nil {
		return "unknown", true
	}

	version := extractPVEVersion(output)
	if version == "" {
		return "unknown", true
	}
	return version, true
}

func detectPBSViaCommand() (string, bool) {
	cmdPath, err := lookPathFunc("proxmox-backup-manager")
	if err != nil {
		return "", false
	}

	output, err := runCommandFunc(cmdPath, "version")
	if err != nil {
		return "unknown", true
	}

	version := extractPBSVersion(output)
	if version == "" {
		return "unknown", true
	}
	return version, true
}

func detectPVEViaVersionFiles() (string, bool) {
	if fileExists(pveVersionFile) {
		if version := readAndTrim(pveVersionFile); version != "" {
			return version, true
		}
	}

	if fileExists(pveLegacyFile) {
		data := readAndTrim(pveLegacyFile)
		if version := extractPVEVersion(data); version != "" {
			return version, true
		}
		return "unknown", true
	}

	return "", false
}

func detectPBSViaVersionFile() (string, bool) {
	if fileExists(pbsVersionFile) {
		if version := readAndTrim(pbsVersionFile); version != "" {
			return version, true
		}
		return "unknown", true
	}
	return "", false
}

func detectPVEViaSources() bool {
	for _, path := range pveSourceFiles {
		if containsAny(path, []string{"pve", "pve-enterprise"}) {
			return true
		}
	}
	return false
}

func detectPBSViaSources() bool {
	for _, path := range pbsSourceFiles {
		if containsAny(path, []string{"pbs", "proxmox-backup"}) {
			return true
		}
	}
	return false
}

func detectViaDirectories(paths []string) bool {
	for _, path := range paths {
		if dirExists(path) {
			return true
		}
	}
	return false
}

func extendPath() {
	currentPath := os.Getenv("PATH")
	pathSet := make(map[string]struct{})
	for _, part := range strings.Split(currentPath, string(os.PathListSeparator)) {
		pathSet[part] = struct{}{}
	}

	updated := currentPath
	for _, add := range additionalPaths {
		if _, ok := pathSet[add]; !ok {
			if updated == "" {
				updated = add
			} else {
				updated = updated + string(os.PathListSeparator) + add
			}
		}
	}

	if updated != currentPath {
		_ = os.Setenv("PATH", updated)
	}
}

func runCommand(command string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	cmd, cmdErr := safeexec.TrustedCommandContext(ctx, command, args...)
	if cmdErr != nil {
		return "", cmdErr
	}
	output, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command %s timed out", command)
	}
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func extractPVEVersion(output string) string {
	re := regexp.MustCompile(`pve-manager/([0-9]+\.[0-9]+(?:[.-][0-9]+)*)`)
	match := re.FindStringSubmatch(output)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func extractPBSVersion(output string) string {
	re := regexp.MustCompile(`version:\s*([0-9]+\.[0-9]+(?:[.-][0-9]+)*)`)
	match := re.FindStringSubmatch(output)
	if len(match) >= 2 {
		return match[1]
	}
	return ""
}

func containsAny(path string, tokens []string) bool {
	data, err := readFileFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	lower := strings.ToLower(string(data))
	for _, token := range tokens {
		if strings.Contains(lower, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func readAndTrim(path string) string {
	data, err := readFileFunc(resolveUnderPrefix(path))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := statFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func dirExists(path string) bool {
	info, err := statFunc(resolveUnderPrefix(path))
	if err != nil {
		return false
	}
	return info.IsDir()
}

func writeDetectionDebug() string {
	debugDir := filepath.Join(debugBaseDir, "proxsave")
	if err := mkdirAllFunc(debugDir, 0o755); err != nil {
		return ""
	}
	now := timeNowFunc()
	path := filepath.Join(debugDir, fmt.Sprintf("proxmox_detection_debug_%d.log", now.Unix()))

	var builder strings.Builder
	fmt.Fprintf(&builder, "=== Proxmox Detection Failure Debug - %s ===\n", now.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&builder, "Current PATH: %s\n", os.Getenv("PATH"))

	if u, err := userCurrentFunc(); err == nil {
		fmt.Fprintf(&builder, "Current USER: %s\n", u.Username)
	} else {
		builder.WriteString("Current USER: unknown\n")
	}

	if cwd, err := getwdFunc(); err == nil {
		fmt.Fprintf(&builder, "Current PWD: %s\n", cwd)
	}
	fmt.Fprintf(&builder, "Shell: %s\n\n", os.Getenv("SHELL"))

	builder.WriteString("=== Command availability check ===\n")
	fmt.Fprintf(&builder, "command -v pveversion: %s\n", lookPathOrNotFound("pveversion"))
	fmt.Fprintf(&builder, "command -v proxmox-backup-manager: %s\n", lookPathOrNotFound("proxmox-backup-manager"))
	builder.WriteString("\n")

	builder.WriteString("=== File existence check ===\n")
	fmt.Fprintf(&builder, "%s exists: %s\n", "/usr/bin/pveversion", boolToYes(fileExists("/usr/bin/pveversion")))
	fmt.Fprintf(&builder, "%s executable: %s\n", "/usr/bin/pveversion", boolToYes(isExecutable("/usr/bin/pveversion")))
	fmt.Fprintf(&builder, "%s exists: %s\n", "/usr/sbin/pveversion", boolToYes(fileExists("/usr/sbin/pveversion")))
	fmt.Fprintf(&builder, "%s executable: %s\n", "/usr/sbin/pveversion", boolToYes(isExecutable("/usr/sbin/pveversion")))
	fmt.Fprintf(&builder, "%s exists: %s\n", "/usr/bin/proxmox-backup-manager", boolToYes(fileExists("/usr/bin/proxmox-backup-manager")))
	fmt.Fprintf(&builder, "%s executable: %s\n", "/usr/bin/proxmox-backup-manager", boolToYes(isExecutable("/usr/bin/proxmox-backup-manager")))
	builder.WriteString("\n")

	builder.WriteString("=== Directory existence check ===\n")
	for _, dir := range append(pveDirCandidates, pbsDirCandidates...) {
		fmt.Fprintf(&builder, "%s exists: %s\n", dir, boolToYes(dirExists(dir)))
	}
	builder.WriteString("\n")

	builder.WriteString("=== Version file check ===\n")
	fmt.Fprintf(&builder, "%s exists: %s\n", pveLegacyFile, boolToYes(fileExists(pveLegacyFile)))
	if content := readAndTrim(pveLegacyFile); content != "" {
		fmt.Fprintf(&builder, "%s content: %s\n", pveLegacyFile, content)
	}
	fmt.Fprintf(&builder, "%s exists: %s\n", pveVersionFile, boolToYes(fileExists(pveVersionFile)))
	if content := readAndTrim(pveVersionFile); content != "" {
		fmt.Fprintf(&builder, "%s content: %s\n", pveVersionFile, content)
	}
	fmt.Fprintf(&builder, "%s exists: %s\n", pbsVersionFile, boolToYes(fileExists(pbsVersionFile)))
	if content := readAndTrim(pbsVersionFile); content != "" {
		fmt.Fprintf(&builder, "%s content: %s\n", pbsVersionFile, content)
	}
	builder.WriteString("\n")

	builder.WriteString("=== APT source files check ===\n")
	for _, source := range append(pveSourceFiles, pbsSourceFiles...) {
		fmt.Fprintf(&builder, "%s exists: %s\n", source, boolToYes(fileExists(source)))
	}
	builder.WriteString("\n")

	builder.WriteString("=== Offline host markers check ===\n")
	fmt.Fprintf(&builder, "%s exists: %s\n", pveClusterDB, boolToYes(fileExists(pveClusterDB)))
	for _, bin := range append(append([]string{}, pveBinaryCandidates...), pbsBinaryCandidates...) {
		fmt.Fprintf(&builder, "%s exists: %s\n", bin, boolToYes(fileExists(bin)))
	}
	fmt.Fprintf(&builder, "%s exists: %s\n", pveShareDir, boolToYes(dirExists(pveShareDir)))
	fmt.Fprintf(&builder, "%s exists: %s\n", pbsShareDir, boolToYes(dirExists(pbsShareDir)))
	fmt.Fprintf(&builder, "%s exists: %s\n", dpkgStatusFile, boolToYes(fileExists(dpkgStatusFile)))
	if _, ok := dpkgPackageInstalled("pve-manager"); ok {
		builder.WriteString("dpkg pve-manager: installed\n")
	}
	if _, ok := dpkgPackageInstalled("proxmox-backup-server"); ok {
		builder.WriteString("dpkg proxmox-backup-server: installed\n")
	}
	builder.WriteString("\n")

	if err := writeFileFunc(path, []byte(builder.String()), 0640); err != nil {
		return ""
	}
	return path
}

func lookPathOrNotFound(binary string) string {
	if path, err := lookPathFunc(binary); err == nil {
		return path
	}
	return "NOT FOUND"
}

func boolToYes(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func isExecutable(path string) bool {
	info, err := statFunc(path)
	if err != nil {
		return false
	}
	return !info.IsDir() && info.Mode()&0111 != 0
}
