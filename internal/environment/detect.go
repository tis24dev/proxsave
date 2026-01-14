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

	lookPathFunc       = exec.LookPath
	commandContextFunc = exec.CommandContext

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
)

// DetectProxmoxType detects whether the system is running Proxmox VE or Proxmox Backup Server
func DetectProxmoxType() types.ProxmoxType {
	pType, _, _ := detectProxmox()
	return pType
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
	default:
		return "", fmt.Errorf("unknown proxmox type: %s", pType)
	}
}

// EnvironmentInfo holds information about the current Proxmox environment
type EnvironmentInfo struct {
	Type    types.ProxmoxType
	Version string
}

// Detect detects the Proxmox environment and returns detailed information
func Detect() (*EnvironmentInfo, error) {
	pType, version, err := detectProxmox()
	if pType == types.ProxmoxUnknown {
		return &EnvironmentInfo{
			Type:    pType,
			Version: "unknown",
		}, fmt.Errorf("unable to detect Proxmox environment")
	}

	return &EnvironmentInfo{
		Type:    pType,
		Version: version,
	}, err
}

func detectProxmox() (types.ProxmoxType, string, error) {
	extendPath()

	if version, ok := detectPVE(); ok {
		return types.ProxmoxVE, version, nil
	}

	if version, ok := detectPBS(); ok {
		return types.ProxmoxBS, version, nil
	}

	debugPath := writeDetectionDebug()
	if debugPath != "" {
		return types.ProxmoxUnknown, "unknown", fmt.Errorf("unable to detect Proxmox environment (debug saved to %s)", debugPath)
	}
	return types.ProxmoxUnknown, "unknown", fmt.Errorf("unable to detect Proxmox environment")
}

func detectPVE() (string, bool) {
	if version, ok := detectPVEViaCommand(); ok {
		return version, true
	}

	if version, ok := detectPVEViaVersionFiles(); ok {
		return version, true
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
	if version, ok := detectPBSViaCommand(); ok {
		return version, true
	}

	if version, ok := detectPBSViaVersionFile(); ok {
		return version, true
	}

	if ok := detectPBSViaSources(); ok {
		return "unknown", true
	}

	if ok := detectViaDirectories(pbsDirCandidates); ok {
		return "unknown", true
	}

	return "", false
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

	cmd := commandContextFunc(ctx, command, args...)
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
	data, err := readFileFunc(path)
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
	data, err := readFileFunc(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	info, err := statFunc(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func dirExists(path string) bool {
	info, err := statFunc(path)
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
	builder.WriteString(fmt.Sprintf("=== Proxmox Detection Failure Debug - %s ===\n", now.Format("2006-01-02 15:04:05")))
	builder.WriteString(fmt.Sprintf("Current PATH: %s\n", os.Getenv("PATH")))

	if u, err := userCurrentFunc(); err == nil {
		builder.WriteString(fmt.Sprintf("Current USER: %s\n", u.Username))
	} else {
		builder.WriteString("Current USER: unknown\n")
	}

	if cwd, err := getwdFunc(); err == nil {
		builder.WriteString(fmt.Sprintf("Current PWD: %s\n", cwd))
	}
	builder.WriteString(fmt.Sprintf("Shell: %s\n\n", os.Getenv("SHELL")))

	builder.WriteString("=== Command availability check ===\n")
	builder.WriteString(fmt.Sprintf("command -v pveversion: %s\n", lookPathOrNotFound("pveversion")))
	builder.WriteString(fmt.Sprintf("command -v proxmox-backup-manager: %s\n", lookPathOrNotFound("proxmox-backup-manager")))
	builder.WriteString("\n")

	builder.WriteString("=== File existence check ===\n")
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", "/usr/bin/pveversion", boolToYes(fileExists("/usr/bin/pveversion"))))
	builder.WriteString(fmt.Sprintf("%s executable: %s\n", "/usr/bin/pveversion", boolToYes(isExecutable("/usr/bin/pveversion"))))
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", "/usr/sbin/pveversion", boolToYes(fileExists("/usr/sbin/pveversion"))))
	builder.WriteString(fmt.Sprintf("%s executable: %s\n", "/usr/sbin/pveversion", boolToYes(isExecutable("/usr/sbin/pveversion"))))
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", "/usr/bin/proxmox-backup-manager", boolToYes(fileExists("/usr/bin/proxmox-backup-manager"))))
	builder.WriteString(fmt.Sprintf("%s executable: %s\n", "/usr/bin/proxmox-backup-manager", boolToYes(isExecutable("/usr/bin/proxmox-backup-manager"))))
	builder.WriteString("\n")

	builder.WriteString("=== Directory existence check ===\n")
	for _, dir := range append(pveDirCandidates, pbsDirCandidates...) {
		builder.WriteString(fmt.Sprintf("%s exists: %s\n", dir, boolToYes(dirExists(dir))))
	}
	builder.WriteString("\n")

	builder.WriteString("=== Version file check ===\n")
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", pveLegacyFile, boolToYes(fileExists(pveLegacyFile))))
	if content := readAndTrim(pveLegacyFile); content != "" {
		builder.WriteString(fmt.Sprintf("%s content: %s\n", pveLegacyFile, content))
	}
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", pveVersionFile, boolToYes(fileExists(pveVersionFile))))
	if content := readAndTrim(pveVersionFile); content != "" {
		builder.WriteString(fmt.Sprintf("%s content: %s\n", pveVersionFile, content))
	}
	builder.WriteString(fmt.Sprintf("%s exists: %s\n", pbsVersionFile, boolToYes(fileExists(pbsVersionFile))))
	if content := readAndTrim(pbsVersionFile); content != "" {
		builder.WriteString(fmt.Sprintf("%s content: %s\n", pbsVersionFile, content))
	}
	builder.WriteString("\n")

	builder.WriteString("=== APT source files check ===\n")
	for _, source := range append(pveSourceFiles, pbsSourceFiles...) {
		builder.WriteString(fmt.Sprintf("%s exists: %s\n", source, boolToYes(fileExists(source))))
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
