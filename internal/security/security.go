package security

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/environment"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

type issueSeverity string

const (
	severityWarning issueSeverity = "warning"
	severityError   issueSeverity = "error"
)

type Issue struct {
	Severity issueSeverity
	Message  string
}

type Result struct {
	Issues []Issue
}

func (r *Result) add(sev issueSeverity, msg string) {
	r.Issues = append(r.Issues, Issue{Severity: sev, Message: msg})
}

func (r *Result) HasErrors() bool {
	for _, issue := range r.Issues {
		if issue.Severity == severityError {
			return true
		}
	}
	return false
}

func (r *Result) ErrorCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == severityError {
			count++
		}
	}
	return count
}

func (r *Result) WarningCount() int {
	count := 0
	for _, issue := range r.Issues {
		if issue.Severity == severityWarning {
			count++
		}
	}
	return count
}

func (r *Result) TotalIssues() int {
	return len(r.Issues)
}

type Checker struct {
	logger     *logging.Logger
	cfg        *config.Config
	configPath string
	execPath   string
	envInfo    *environment.EnvironmentInfo
	result     *Result
	lookPath   func(string) (string, error)
}

type dependencyEntry struct {
	Name     string
	Required bool
	Reason   string
	Check    func() (bool, string)
}

// Run executes the security checks and returns the aggregated result.
func Run(ctx context.Context, logger *logging.Logger, cfg *config.Config, configPath, execPath string, envInfo *environment.EnvironmentInfo) (*Result, error) {
	if !cfg.SecurityCheckEnabled {
		logger.Debug("Security checks disabled via configuration")
		return &Result{}, nil
	}

	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	if abs, err := filepath.Abs(execPath); err == nil {
		execPath = abs
	}

	checker := &Checker{
		logger:     logger,
		cfg:        cfg,
		configPath: configPath,
		execPath:   execPath,
		envInfo:    envInfo,
		result:     &Result{},
		lookPath:   exec.LookPath,
	}

	logger.Step("Security preflight checks")
	logger.Debug("Security options: auto_fix=%v, auto_update_hashes=%v, continue_on_issues=%v, check_network=%v, firewall=%v, open_ports=%v, suspicious_processes=%d, safe_bracket=%d",
		cfg.AutoFixPermissions, cfg.AutoUpdateHashes, cfg.ContinueOnSecurityIssues, cfg.CheckNetworkSecurity, cfg.CheckFirewall, cfg.CheckOpenPorts,
		len(cfg.SuspiciousProcesses), len(cfg.SafeBracketProcesses))
	checker.checkDependencies()
	checker.verifyBinaryIntegrity()
	checker.verifyConfigFile()
	checker.verifySensitiveFiles()
	checker.verifyDirectories()
	checker.verifySecureAccountFiles()
	checker.detectPrivateAgeKeys()

	if cfg.CheckNetworkSecurity {
		checker.checkFirewall(ctx)
		if cfg.CheckOpenPorts {
			checker.checkOpenPorts(ctx)
		}
		checker.checkOpenPortsAgainstSuspiciousList(ctx)
	}

	checker.checkSuspiciousProcesses(ctx)

	warnings := checker.result.WarningCount()
	errorsCount := checker.result.ErrorCount()
	logger.Info("Security checks completed: %d warning(s), %d error(s)",
		warnings, errorsCount)

	if !cfg.ContinueOnSecurityIssues && errorsCount > 0 {
		return checker.result, fmt.Errorf("security checks reported %d error(s); set CONTINUE_ON_SECURITY_ISSUES=true to bypass", errorsCount)
	}

	return checker.result, nil
}

func (c *Checker) checkDependencies() {
	deps := c.buildDependencyList()
	c.logger.Info("Checking dependencies...")

	if len(deps) == 0 {
		c.logger.Info("Dependencies check completed: no external dependencies required")
		return
	}

	var missing []dependencyEntry
	var optionalMissing []dependencyEntry

	for _, dep := range deps {
		present, detail := dep.Check()
		if present {
			if detail != "" {
				c.logger.Debug("Dependency %s: present (%s) - %s", dep.Name, detail, dep.Reason)
			} else {
				c.logger.Debug("Dependency %s: present - %s", dep.Name, dep.Reason)
			}
			continue
		}

		if dep.Required {
			c.logger.Debug("Dependency %s: missing - %s", dep.Name, dep.Reason)
			missing = append(missing, dep)
		} else {
			c.logger.Debug("Dependency %s: missing (optional) - %s", dep.Name, dep.Reason)
			optionalMissing = append(optionalMissing, dep)
		}
	}

	if len(missing) == 0 {
		if len(optionalMissing) > 0 {
			names := make([]string, len(optionalMissing))
			for i, dep := range optionalMissing {
				names[i] = dep.Name
			}
			c.logger.Info("Dependencies check completed: all required dependencies available (optional missing: %s)", strings.Join(names, ", "))
			for _, dep := range optionalMissing {
				c.addWarning("Optional dependency %s missing: %s", dep.Name, dep.Reason)
			}
		} else {
			c.logger.Info("Dependencies check completed: all required dependencies available")
		}
		return
	}

	c.logger.Warning("Dependencies check: missing required dependencies")
	for _, dep := range missing {
		c.logger.Warning(" - %s (%s)", dep.Name, dep.Reason)
		c.addError("Required dependency %s missing: %s", dep.Name, dep.Reason)
	}
}

func (c *Checker) buildDependencyList() []dependencyEntry {
	deps := []dependencyEntry{
		c.binaryDependency("tar", []string{"tar"}, true, "required for archive verification and bundle handling"),
	}

	switch c.cfg.CompressionType {
	case types.CompressionXZ:
		deps = append(deps, c.binaryDependency("xz", []string{"xz"}, true, "compression type set to xz"))
	case types.CompressionZstd:
		deps = append(deps, c.binaryDependency("zstd", []string{"zstd"}, true, "compression type set to zstd"))
	case types.CompressionPigz:
		deps = append(deps, c.binaryDependency("pigz", []string{"pigz"}, true, "compression type set to pigz"))
	case types.CompressionBzip2:
		deps = append(deps, c.binaryDependency("pbzip2/bzip2", []string{"pbzip2", "bzip2"}, true, "compression type set to bzip2"))
	case types.CompressionLZMA:
		deps = append(deps, c.binaryDependency("lzma", []string{"lzma"}, true, "compression type set to lzma"))
	}

	if c.cfg.CloudEnabled && strings.TrimSpace(c.cfg.CloudRemote) != "" {
		deps = append(deps, c.binaryDependency("rclone", []string{"rclone"}, false, "cloud storage uploads enabled"))
	}

	emailMethod := strings.ToLower(strings.TrimSpace(c.cfg.EmailDeliveryMethod))
	if emailMethod == "" {
		emailMethod = "relay"
	}
	if emailMethod == "sendmail" {
		deps = append(deps, c.binaryDependency("sendmail", []string{"sendmail"}, true, "email delivery method set to sendmail"))
	} else if c.cfg.EmailFallbackSendmail {
		deps = append(deps, c.binaryDependency("sendmail", []string{"sendmail"}, false, "email relay fallback to sendmail enabled"))
	}

	if c.cfg.BackupCephConfig {
		deps = append(deps, c.binaryDependency("ceph", []string{"ceph"}, false, "Ceph configuration collection enabled"))
	}
	if c.cfg.BackupZFSConfig {
		deps = append(deps,
			c.binaryDependency("zpool", []string{"zpool"}, false, "ZFS configuration collection enabled"),
			c.binaryDependency("zfs", []string{"zfs"}, false, "ZFS configuration collection enabled"),
		)
	}

	if c.envInfo != nil {
		switch c.envInfo.Type {
		case types.ProxmoxVE:
			deps = append(deps,
				c.binaryDependency("pveversion", []string{"pveversion"}, false, "Proxmox VE command availability"),
				c.binaryDependency("pvecm", []string{"pvecm"}, false, "PVE cluster management command"),
			)
		case types.ProxmoxBS:
			deps = append(deps,
				c.binaryDependency("proxmox-backup-manager", []string{"proxmox-backup-manager"}, false, "PBS management CLI"),
			)
			if c.cfg.BackupTapeConfigs {
				deps = append(deps, c.binaryDependency("proxmox-tape", []string{"proxmox-tape"}, false, "PBS tape configuration collection enabled"))
			}
		}
	}

	return deps
}

func (c *Checker) binaryDependency(name string, binaries []string, required bool, reason string) dependencyEntry {
	return dependencyEntry{
		Name:     name,
		Required: required,
		Reason:   reason,
		Check: func() (bool, string) {
			lookPath := c.lookPath
			if lookPath == nil {
				lookPath = exec.LookPath
			}
			for _, binary := range binaries {
				if path, err := lookPath(binary); err == nil {
					return true, fmt.Sprintf("%s at %s", binary, path)
				}
			}
			return false, ""
		},
	}
}

func (c *Checker) addWarning(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	c.logger.Warning("%s", msg)
	c.result.add(severityWarning, msg)
}

func (c *Checker) addError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	c.logger.Error("%s", msg)
	c.result.add(severityError, msg)
}

func (c *Checker) bannerWarning(message string) {
	c.logger.Warning("Security warning: %s", message)
}

func (c *Checker) verifyBinaryIntegrity() {
	if c.execPath == "" {
		c.addWarning("Executable path not available for integrity check")
		return
	}

	f, err := os.Open(c.execPath)
	if err != nil {
		c.addError("Cannot open executable %s: %v", c.execPath, err)
		return
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		c.addError("Cannot stat executable %s: %v", c.execPath, err)
		return
	}

	if info.Mode()&os.ModeSymlink != 0 {
		c.addError("Executable %s is a symlink", c.execPath)
		return
	}

	c.ensureOwnershipAndPerm(c.execPath, info, 0o700, fmt.Sprintf("Executable %s", c.execPath))

	hashFile := c.execPath + ".md5"
	currentHash, err := checksumReader(f)
	if err != nil {
		c.addWarning("Unable to calculate hash for %s: %v", c.execPath, err)
		return
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		c.addWarning("Unable to rewind file for %s: %v", c.execPath, err)
		return
	}

	if _, err := os.Stat(hashFile); errors.Is(err, os.ErrNotExist) {
		if c.cfg.AutoUpdateHashes {
			if err := os.WriteFile(hashFile, []byte(currentHash), 0o600); err != nil {
				c.addWarning("Failed to create hash file %s: %v", hashFile, err)
			} else {
				c.logger.Info("Created new hash file for executable: %s", hashFile)
			}
		} else {
			c.bannerWarning(fmt.Sprintf("hash file %s missing", hashFile))
			c.addWarning("Hash file %s missing (AUTO_UPDATE_HASHES=false)", hashFile)
		}
		return
	}

	stored, err := os.ReadFile(hashFile)
	if err != nil {
		c.addWarning("Unable to read hash file %s: %v", hashFile, err)
		return
	}

	if strings.TrimSpace(string(stored)) != currentHash {
		c.bannerWarning(fmt.Sprintf("executable hash mismatch for %s", c.execPath))
		if c.cfg.AutoUpdateHashes {
			if err := os.WriteFile(hashFile, []byte(currentHash), 0o600); err != nil {
				c.addWarning("Failed to update hash file %s: %v", hashFile, err)
			} else {
				c.logger.Info("Regenerated hash file: %s", hashFile)
			}
		} else {
			c.addWarning("Executable hash mismatch for %s (expected %s, current %s)", c.execPath, strings.TrimSpace(string(stored)), currentHash)
		}
	}
}

func (c *Checker) verifyConfigFile() {
	if c.configPath == "" {
		c.addWarning("Configuration path not provided")
		return
	}

	info, err := os.Stat(c.configPath)
	if err != nil {
		c.addError("Cannot stat configuration file %s: %v", c.configPath, err)
		return
	}

	c.ensureOwnershipAndPerm(c.configPath, info, 0o600, fmt.Sprintf("Config file %s", c.configPath))
}

func (c *Checker) verifySensitiveFiles() {
	files := []struct {
		path        string
		perm        os.FileMode
		description string
		optional    bool
	}{
		{filepath.Join(c.cfg.BaseDir, "identity", ".server_identity"), 0o600, "server identity file", true},
	}

	ageRecipientPath := c.cfg.AgeRecipientFile
	if ageRecipientPath == "" {
		ageRecipientPath = filepath.Join(c.cfg.BaseDir, "identity", "age", "recipient.txt")
	}
	if ageRecipientPath != "" {
		files = append(files, struct {
			path        string
			perm        os.FileMode
			description string
			optional    bool
		}{
			path:        ageRecipientPath,
			perm:        0o600,
			description: "AGE recipient file",
			optional:    true,
		})
	}

	for _, entry := range files {
		info, err := os.Stat(entry.path)
		if errors.Is(err, os.ErrNotExist) && entry.optional {
			if entry.description == "AGE recipient file" && c.cfg.EncryptArchive {
				c.logger.Debug("Security check: AGE recipient file %s not present yet (wizard will create it)", entry.path)
			}
			continue
		}
		if err != nil {
			c.addWarning("Cannot stat %s (%s): %v", entry.path, entry.description, err)
			continue
		}
		c.ensureOwnershipAndPerm(entry.path, info, entry.perm, entry.description)
	}
}

func (c *Checker) verifySecureAccountFiles() {
	if c.cfg.SecureAccount == "" {
		return
	}

	matches, err := filepath.Glob(filepath.Join(c.cfg.SecureAccount, "*.json"))
	if err != nil {
		c.addWarning("Failed to enumerate secure account files: %v", err)
		return
	}

	for _, file := range matches {
		info, err := os.Stat(file)
		if err != nil {
			c.addWarning("Cannot stat secure account file %s: %v", file, err)
			continue
		}
		c.ensureOwnershipAndPerm(file, info, 0o600, fmt.Sprintf("Secure account file %s", file))
	}
}

func (c *Checker) verifyDirectories() {
	dirs := []struct {
		path        string
		perm        os.FileMode
		allowBackup bool
	}{
		{c.cfg.BackupPath, 0o755, true},
		{c.cfg.LogPath, 0o755, true},
		{c.cfg.SecondaryPath, 0o755, true},
		{c.cfg.SecondaryLogPath, 0o755, true},
		{c.cfg.LockPath, 0o755, false},
		{c.cfg.SecureAccount, 0o700, false},
		{filepath.Join(c.cfg.BaseDir, "identity"), 0o700, false},
		{filepath.Join(c.cfg.BaseDir, "identity", "age"), 0o700, false},
	}

	for _, dir := range dirs {
		if dir.path == "" {
			continue
		}
		info, err := os.Stat(dir.path)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(dir.path, dir.perm); err != nil {
				c.addError("Failed to create directory %s: %v", dir.path, err)
				continue
			}
			c.logger.Info("Created missing directory: %s", dir.path)
			info, err = os.Stat(dir.path)
			if err != nil {
				c.addWarning("Cannot verify permissions for %s: %v", dir.path, err)
				continue
			}
		} else if err != nil {
			c.addWarning("Cannot stat directory %s: %v", dir.path, err)
			continue
		}

		if dir.allowBackup && c.shouldSkipOwnershipChecks(dir.path) {
			c.logger.Debug("Security check: skipping root ownership enforcement for %s (managed by SET_BACKUP_PERMISSIONS)", dir.path)
			continue
		}

		c.ensureOwnershipAndPerm(dir.path, info, dir.perm, fmt.Sprintf("Directory %s", dir.path))
	}
}

func (c *Checker) detectPrivateAgeKeys() {
	identityDir := filepath.Join(c.cfg.BaseDir, "identity")
	if identityDir == "" {
		return
	}

	if _, err := os.Stat(identityDir); err != nil {
		return
	}

	privateKeyMarkers := []string{"AGE-SECRET-KEY-", "BEGIN AGE PRIVATE KEY", "OPENSSH PRIVATE KEY"}
	if err := filepath.WalkDir(identityDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			c.logger.Debug("Security: cannot access %s: %v", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".md" || ext == ".txt" || ext == ".example" {
			return nil
		}
		hasMarker, scanErr := fileContainsMarker(path, privateKeyMarkers, 64*1024)
		if scanErr != nil {
			c.logger.Debug("Security: skipped private key scan for %s: %v", path, scanErr)
			return nil
		}
		if hasMarker {
			c.addWarning("Possible private AGE/SSH key detected: %s (review manually)", path)
		}
		return nil
	}); err != nil {
		c.logger.Debug("Security: private key detection walk error: %v", err)
	}
}

func fileContainsMarker(path string, markers []string, limit int) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	const bufSize = 4096
	maxMarkerLen := 0
	upperMarkers := make([]string, len(markers))
	for i, marker := range markers {
		upper := strings.ToUpper(marker)
		upperMarkers[i] = upper
		if len(upper) > maxMarkerLen {
			maxMarkerLen = len(upper)
		}
	}
	if maxMarkerLen == 0 {
		return false, nil
	}

	reader := bufio.NewReader(f)
	buffer := make([]byte, bufSize)
	overlap := make([]byte, 0, maxMarkerLen)
	totalRead := 0

	for {
		if limit > 0 && totalRead >= limit {
			return false, nil
		}

		n, err := reader.Read(buffer)
		if n > 0 {
			combined := append(overlap, buffer[:n]...)
			chunk := strings.ToUpper(string(combined))

			for _, marker := range upperMarkers {
				if strings.Contains(chunk, marker) {
					return true, nil
				}
			}

			if len(combined) >= maxMarkerLen {
				overlap = append([]byte{}, combined[len(combined)-maxMarkerLen:]...)
			} else {
				overlap = append([]byte{}, combined...)
			}

			totalRead += n
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				return false, nil
			}
			return false, err
		}
	}
}

func (c *Checker) checkFirewall(ctx context.Context) {
	if _, err := exec.LookPath("iptables"); err != nil {
		c.addWarning("iptables not found; firewall check skipped")
		return
	}

	cmd := exec.CommandContext(ctx, "iptables", "-L", "-n")
	output, err := cmd.Output()
	if err != nil {
		c.addWarning("Failed to run iptables -L -n: %v", err)
		return
	}

	lines := 0
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "Chain ") || strings.HasPrefix(line, "target") {
			continue
		}
		lines++
	}

	if lines == 0 {
		c.addWarning("No active iptables rules detected")
	} else {
		c.logger.Info("Firewall check: %d iptables rule entries detected", lines)
	}
}

func (c *Checker) checkOpenPorts(ctx context.Context) {
	if _, err := exec.LookPath("ss"); err != nil {
		c.addWarning("Command 'ss' not available; open ports check skipped")
		return
	}

	cmd := exec.CommandContext(ctx, "ss", "-tulnap")
	output, err := cmd.Output()
	if err != nil {
		c.addWarning("Failed to execute 'ss -tulnap': %v", err)
		return
	}

	lines := strings.Split(string(output), "\n")
	whitelist := buildWhitelistMap(c.cfg.PortWhitelist)
	suspicious := make(map[int]struct{}, len(c.cfg.SuspiciousPorts))
	for _, port := range c.cfg.SuspiciousPorts {
		suspicious[port] = struct{}{}
	}

	for _, line := range lines {
		if !strings.Contains(line, ":") {
			continue
		}
		entry := parseSSLine(line)
		if !entry.valid || !entry.public {
			continue
		}

		if _, ok := suspicious[entry.port]; ok {
			if entry.program != "" && whitelist.allowed(entry.port, entry.program) {
				continue
			}
			c.addWarning("Suspicious open port detected: %d (address=%s, program=%s)", entry.port, entry.address, entry.program)
		}
	}
}

func (c *Checker) checkOpenPortsAgainstSuspiciousList(ctx context.Context) {
	if _, err := exec.LookPath("ss"); err != nil {
		return
	}
	cmd := exec.CommandContext(ctx, "ss", "-tuln")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	totalPublic := 0
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		entry := parseSSLine(scanner.Text())
		if entry.valid && entry.public {
			totalPublic++
		}
	}

	if totalPublic > 0 {
		c.logger.Info("Detected %d services listening on public interfaces", totalPublic)
	}
}

func (c *Checker) checkSuspiciousProcesses(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "ps", "-eo", "user=,state=,vsz=,pid=,command=")
	output, err := cmd.Output()
	if err != nil {
		c.addWarning("Failed to execute 'ps' for process inspection: %v", err)
		return
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		user, state, vsz, pid, args := parsePSLine(line)
		if args == "" {
			continue
		}
		lowerArgs := strings.ToLower(args)

		for _, signature := range c.cfg.SuspiciousProcesses {
			sig := strings.ToLower(strings.TrimSpace(signature))
			if sig == "" {
				continue
			}
			if strings.Contains(lowerArgs, sig) {
				c.addWarning("Suspicious process detected: %s (PID %s, user %s)", strings.TrimSpace(args), pid, user)
				break
			}
		}

		trimmed := strings.TrimSpace(args)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			if !c.isSafeBracketProcess(name) {
				if intPID, err := strconv.Atoi(strings.TrimSpace(pid)); err == nil {
					if isHeuristicallySafeKernelProcess(intPID, name, c.cfg.SafeBracketProcesses) {
						continue
					}
				}
				c.addWarning("Suspicious kernel-style process: %s (PID %s, user %s)", name, pid, user)
			}
		}

		//lint:ignore SA4017 isZombieProxmoxProcess is intentionally used only for control flow
		if isZombieProxmoxProcess(user, state, vsz, trimmed) {
			continue
		}
	}
}

func isZombieProxmoxProcess(user, state, vsz, cmd string) bool {
	if !strings.HasPrefix(cmd, "proxmox-backup-") {
		return false
	}
	if state != "Z" {
		return false
	}
	if user != "root" && user != "backup" {
		return false
	}
	vsz = strings.TrimSpace(vsz)
	return vsz == "0" || vsz == ""
}

func checksumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return checksumReader(f)
}

func checksumReader(r io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, r); err != nil {
		return "", err
	}
	return strings.ToLower(hex.EncodeToString(hasher.Sum(nil))), nil
}

func isOwnedByRoot(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return stat.Uid == 0 && stat.Gid == 0
}

func (c *Checker) shouldSkipOwnershipChecks(path string) bool {
	if !c.cfg.SetBackupPermissions {
		return false
	}

	target := filepath.Clean(path)
	paths := []string{
		c.cfg.BackupPath,
		c.cfg.LogPath,
		c.cfg.SecondaryPath,
		c.cfg.SecondaryLogPath,
	}

	for _, candidate := range paths {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		if filepath.Clean(candidate) == target {
			return true
		}
	}
	return false
}

type ssEntry struct {
	valid   bool
	port    int
	address string
	public  bool
	program string
}

var ssProgramRegex = regexp.MustCompile(`users:\(\("([^"]+)"`)

func parseSSLine(line string) ssEntry {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return ssEntry{}
	}

	localAddr := fields[4]
	port, addr := extractPort(localAddr)
	if port == 0 {
		return ssEntry{}
	}

	program := ""
	if matches := ssProgramRegex.FindStringSubmatch(line); len(matches) == 2 {
		program = strings.ToLower(matches[1])
	}

	return ssEntry{
		valid:   true,
		port:    port,
		address: addr,
		public:  isPublicAddress(addr),
		program: program,
	}
}

func extractPort(address string) (int, string) {
	addr := strings.TrimSpace(address)
	if addr == "" {
		return 0, ""
	}

	// Remove wildcard notation like [::]:22
	if strings.HasPrefix(addr, "[") {
		if closing := strings.Index(addr, "]"); closing != -1 {
			addr = addr[1:closing] + addr[closing+1:]
		}
	}

	lastColon := strings.LastIndex(addr, ":")
	if lastColon == -1 {
		return 0, ""
	}

	portStr := addr[lastColon+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, ""
	}

	ip := addr[:lastColon]
	if ip == "" {
		ip = "0.0.0.0"
	}

	return port, ip
}

func isPublicAddress(address string) bool {
	addr := strings.Trim(address, "*")
	if addr == "" || addr == "0.0.0.0" || addr == ":::0" || addr == "::" || addr == "0" {
		return true
	}
	if strings.HasPrefix(addr, "127.") || strings.HasPrefix(addr, "::1") {
		return false
	}
	if strings.HasPrefix(strings.ToLower(addr), "local") {
		return false
	}
	return true
}

func parsePSLine(line string) (string, string, string, string, string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", "", "", "", ""
	}
	psRegex := regexp.MustCompile(`^(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s+(.*)$`)
	matches := psRegex.FindStringSubmatch(line)
	if len(matches) != 6 {
		return "", "", "", "", ""
	}
	return matches[1], matches[2], matches[3], matches[4], matches[5]
}

func matchesSafeProcessPattern(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}

	// Regex support (case-insensitive by default)
	if strings.HasPrefix(strings.ToLower(pattern), "regex:") {
		regexPattern := strings.TrimSpace(pattern[6:])
		if regexPattern == "" {
			return false
		}
		if !strings.HasPrefix(regexPattern, "(?i)") {
			regexPattern = "(?i)" + regexPattern
		}
		re, err := regexp.Compile(regexPattern)
		if err != nil {
			return false
		}
		return re.MatchString(name)
	}

	// Exact or prefix match (case-insensitive)
	lower := strings.ToLower(name)
	pattern = strings.ToLower(pattern)
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(lower, prefix)
	}
	return lower == pattern
}

func isLegitimateKernelProcess(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range kernelProcessPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func (c *Checker) isSafeBracketProcess(name string) bool {
	if isLegitimateKernelProcess(name) {
		return true
	}
	if c.isSafeKernelProcess(name) {
		return true
	}
	for _, pattern := range c.cfg.SafeBracketProcesses {
		if matchesSafeProcessPattern(pattern, name) {
			return true
		}
	}
	return false
}

func (c *Checker) isSafeKernelProcess(name string) bool {
	for _, pattern := range c.cfg.SafeKernelProcesses {
		if matchesSafeProcessPattern(pattern, name) {
			return true
		}
	}
	return false
}

func (c *Checker) ensureOwnershipAndPerm(path string, info os.FileInfo, expectedPerm os.FileMode, description string) os.FileInfo {
	var err error
	if info == nil {
		info, err = os.Lstat(path)
		if err != nil {
			c.addWarning("Cannot stat %s: %v", path, err)
			return nil
		}
	}

	isSymlink := info.Mode()&os.ModeSymlink != 0

	if expectedPerm != 0 {
		if perm := info.Mode().Perm(); perm != expectedPerm {
			c.bannerWarning(fmt.Sprintf("incorrect permissions on %s (current %o, expected %o)", path, perm, expectedPerm))
			if c.cfg.AutoFixPermissions {
				if isSymlink {
					c.addError("Security: refusing to chmod symlink %s", path)
				} else if err := syscall.Chmod(path, uint32(expectedPerm)); err != nil {
					c.addWarning("Failed to adjust permissions on %s: %v", path, err)
				} else {
					c.logger.Info("Adjusted permissions on %s to %o", path, expectedPerm)
					info, _ = os.Lstat(path)
					isSymlink = info.Mode()&os.ModeSymlink != 0
				}
			} else {
				c.addWarning("%s should have permissions %o (current %o)", description, expectedPerm, perm)
			}
		}
	}

	if info != nil && !isOwnedByRoot(info) {
		c.bannerWarning(fmt.Sprintf("incorrect ownership on %s (required root:root)", path))
		if c.cfg.AutoFixPermissions {
			if isSymlink {
				c.addError("Security: refusing to chown symlink %s", path)
			} else if err := syscall.Lchown(path, 0, 0); err != nil {
				c.addWarning("Failed to set ownership root:root on %s: %v", path, err)
			} else {
				c.logger.Info("Adjusted ownership on %s to root:root", path)
				info, _ = os.Lstat(path)
			}
		} else {
			c.addWarning("%s should be owned by root:root", description)
		}
	}

	return info
}

var kernelProcessPrefixes = []string{
	"kworker", "kthreadd", "kswapd", "rcu_", "migration", "watchdog", "ksoftirqd", "khugepaged",
	"kcompactd", "khubd", "kdevtmpfs", "netns", "writeback", "crypto", "bioset", "kblockd",
	"ata_sff", "md", "edac-poller", "devfreq_wq", "jbd2", "ext4", "ipv6_addrconf", "scsi_eh",
	"kdmflush", "kcryptd", "ttm", "tls", "rpcio", "xprtiod", "charger_manager", "kstrp",
	"md_bio_submit", "blkcg_punt_bio", "tmp_dev_wq", "acpi_thermal_pm", "ipv6_mc", "kthrotld",
	"zswap", "khungtaskd", "oom_reaper", "ksmd", "kauditd", "cpuhp", "idle_inject", "irq/",
	"pool_workqueue", "spl_", "ecryptfs", "txg_", "mmp", "dp_", "z_", "arc_", "arc_reap",
	"zvol_tq", "dbu_", "dbuf_", "l2arc", "lockd", "nfsd", "nfsv4 callback",
}

type portWhitelist map[int]map[string]struct{}

func buildWhitelistMap(entries []string) portWhitelist {
	if len(entries) == 0 {
		return nil
	}
	result := make(portWhitelist)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) != 2 {
			continue
		}
		program := strings.ToLower(strings.TrimSpace(parts[0]))
		portStr := strings.TrimSpace(parts[1])
		port, err := strconv.Atoi(portStr)
		if err != nil || program == "" {
			continue
		}
		if _, ok := result[port]; !ok {
			result[port] = make(map[string]struct{})
		}
		result[port][program] = struct{}{}
	}
	return result
}

func (w portWhitelist) allowed(port int, program string) bool {
	if len(w) == 0 || program == "" {
		return false
	}
	if programs, ok := w[port]; ok {
		if _, ok := programs[strings.ToLower(program)]; ok {
			return true
		}
	}
	return false
}
