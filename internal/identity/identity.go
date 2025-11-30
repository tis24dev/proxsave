package identity

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	identityDirName       = "identity"
	identityFileName      = ".server_identity"
	fallbackIdentityDir   = "/tmp/proxsave"
	fallbackIdentityPath  = fallbackIdentityDir + "/.proxmox_backup_identity"
	maxProcVersionBytes   = 100
	maxMachineIDBytes     = 32
	systemKeyPrefixLength = 8
	serverIDLength        = 16
)

// Info contains server identity information.
type Info struct {
	ServerID     string
	PrimaryMAC   string
	MACAddresses []string
	IdentityFile string
}

// Detect resolves the server identity (ID + MAC address) and ensures persistence.
func Detect(baseDir string, logger *logging.Logger) (*Info, error) {
	info := &Info{}

	macs := collectMACAddresses()
	info.MACAddresses = macs
	if len(macs) > 0 {
		info.PrimaryMAC = macs[0]
	}
	logDebug(logger, "Identity: detected %d MAC addresses (primary: %s)", len(macs), info.PrimaryMAC)

	identityPath := fallbackIdentityPath
	if strings.TrimSpace(baseDir) != "" {
		identityDir := filepath.Join(baseDir, identityDirName)
		if err := os.MkdirAll(identityDir, 0o755); err != nil {
			logWarning(logger, "Failed to create identity directory %s: %v (falling back to %s)", identityDir, err, fallbackIdentityPath)
			_ = os.MkdirAll(fallbackIdentityDir, 0o755)
		} else {
			identityPath = filepath.Join(identityDir, identityFileName)
		}
	} else {
		_ = os.MkdirAll(fallbackIdentityDir, 0o755)
	}
	info.IdentityFile = identityPath

	// Attempt to load an existing ID first.
	if id, err := loadServerID(identityPath, info.PrimaryMAC); err == nil && id != "" {
		logDebug(logger, "Identity: loaded existing server ID %s from %s", id, identityPath)
		info.ServerID = id
		return info, nil
	}

	serverID, encodedFile, err := generateServerID(macs, info.PrimaryMAC)
	if err != nil {
		return info, err
	}
	info.ServerID = serverID
	logDebug(logger, "Identity: generated new server ID %s", serverID)

	if err := writeIdentityFile(identityPath, encodedFile); err != nil {
		if identityPath != fallbackIdentityPath {
			logWarning(logger, "Failed to write server identity file %s: %v (retrying in %s)", identityPath, err, fallbackIdentityPath)
			info.IdentityFile = fallbackIdentityPath
			_ = os.MkdirAll(fallbackIdentityDir, 0o755)
			if err2 := writeIdentityFile(fallbackIdentityPath, encodedFile); err2 != nil {
				return info, fmt.Errorf("failed to persist server identity: %w", err2)
			}
			return info, nil
		}
		return info, fmt.Errorf("failed to persist server identity: %w", err)
	}

	return info, nil
}

func collectMACAddresses() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	var macs []string
	for _, iface := range ifaces {
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		if (iface.Flags & net.FlagLoopback) != 0 {
			continue
		}
		mac := strings.ToLower(iface.HardwareAddr.String())
		if _, ok := seen[mac]; ok || mac == "" {
			continue
		}
		seen[mac] = struct{}{}
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	return macs
}

func loadServerID(path, primaryMAC string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return decodeProtectedServerID(string(data), primaryMAC)
}

func generateServerID(macs []string, primaryMAC string) (string, string, error) {
	systemData := buildSystemData(macs)
	hash := sha256.Sum256([]byte(systemData))
	hexString := fmt.Sprintf("%x", hash)
	if len(hexString) < serverIDLength {
		hexString = hexString + strings.Repeat("0", serverIDLength-len(hexString))
	}

	hexPart := hexString[:serverIDLength]
	decimalID := hexToDecimal(hexPart)
	if decimalID == "" {
		decimalID = sanitizeDigits(hexPart)
	}

	serverID := normalizeServerID(decimalID, hash[:])
	if serverID == "" {
		return "", "", fmt.Errorf("unable to compute server ID")
	}

	encoded, err := encodeProtectedServerID(serverID, primaryMAC)
	if err != nil {
		return "", "", err
	}

	return serverID, encoded, nil
}

func buildSystemData(macs []string) string {
	var builder strings.Builder
	builder.WriteString(time.Now().UTC().Format("20060102150405"))

	if machineID := readFirstLine("/etc/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
	} else if machineID := readFirstLine("/var/lib/dbus/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
	}

	if len(macs) > 0 {
		builder.WriteString(strings.Join(macs, ":"))
	}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		builder.WriteString(hostname)
	}

	if uuid := readFirstLine("/sys/class/dmi/id/product_uuid", maxMachineIDBytes); uuid != "" {
		builder.WriteString(uuid)
	}

	if version := readFirstLine("/proc/version", maxProcVersionBytes); version != "" {
		builder.WriteString(version)
	}

	if builder.Len() == 0 {
		builder.WriteString(fmt.Sprintf("fallback-%d-%d", time.Now().Unix(), os.Getpid()))
	}

	return builder.String()
}

func encodeProtectedServerID(serverID, primaryMAC string) (string, error) {
	systemKey := generateSystemKey(primaryMAC)
	timestamp := time.Now().Unix()
	data := fmt.Sprintf("%s:%d:%s", serverID, timestamp, systemKey[:systemKeyPrefixLength])
	checksum := sha256.Sum256([]byte(data))
	finalData := fmt.Sprintf("%s:%s", data, fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength])
	encoded := base64.StdEncoding.EncodeToString([]byte(finalData))

	var builder strings.Builder
	builder.WriteString("# ProxSave Backup System Configuration\n")
	builder.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().Format(time.RFC3339)))
	builder.WriteString("# DO NOT MODIFY THIS FILE MANUALLY\n")
	builder.WriteString(fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded))
	builder.WriteString("# End of configuration\n")

	return builder.String(), nil
}

func decodeProtectedServerID(fileContent, primaryMAC string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(fileContent))
	var encoded string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "SYSTEM_CONFIG_DATA=") {
			encoded = strings.Trim(line[len("SYSTEM_CONFIG_DATA="):], "\"")
			break
		}
	}
	if encoded == "" {
		return "", fmt.Errorf("identity data not found")
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("invalid encoded identity data: %w", err)
	}

	parts := strings.Split(string(decodedBytes), ":")
	if len(parts) != 4 {
		return "", fmt.Errorf("invalid identity payload format")
	}

	serverID, timestamp, systemKey, checksum := parts[0], parts[1], parts[2], parts[3]
	data := fmt.Sprintf("%s:%s:%s", serverID, timestamp, systemKey)
	expectedChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:systemKeyPrefixLength]
	if checksum != expectedChecksum {
		return "", fmt.Errorf("identity checksum mismatch")
	}

	currentKey := generateSystemKey(primaryMAC)
	if len(systemKey) > len(currentKey) {
		systemKey = systemKey[:len(currentKey)]
	}
	if systemKey != currentKey[:len(systemKey)] {
		return "", fmt.Errorf("identity file does not belong to this host")
	}

	if len(serverID) != serverIDLength || !isAllDigits(serverID) {
		return "", fmt.Errorf("invalid server ID format")
	}
	return serverID, nil
}

func generateSystemKey(primaryMAC string) string {
	var builder strings.Builder
	if machineID := readFirstLine("/etc/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
	} else if machineID := readFirstLine("/var/lib/dbus/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
	}

	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		if len(hostname) > 8 {
			builder.WriteString(hostname[:8])
		} else {
			builder.WriteString(hostname)
		}
	}

	builder.WriteString(strings.ReplaceAll(primaryMAC, ":", ""))

	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum)[:16]
}

func writeIdentityFile(path, content string) error {
	// Ensure file is writable even if immutable was previously set
	_ = setImmutableAttribute(path, false)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}

	_ = setImmutableAttribute(path, true)

	return nil
}

func readFirstLine(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if limit > 0 && len(line) > limit {
		line = line[:limit]
	}
	return line
}

func hexToDecimal(hexStr string) string {
	n := new(big.Int)
	if _, ok := n.SetString(hexStr, 16); !ok {
		return ""
	}
	return n.String()
}

func normalizeServerID(value string, hash []byte) string {
	value = sanitizeDigits(value)
	if value == "" {
		return fallbackServerID(hash)
	}

	switch {
	case len(value) > serverIDLength:
		return value[:serverIDLength]
	case len(value) < serverIDLength:
		return strings.Repeat("0", serverIDLength-len(value)) + value
	default:
		return value
	}
}

func fallbackServerID(hash []byte) string {
	timestamp := time.Now().Unix()
	hashDigits := sanitizeDigits(fmt.Sprintf("%x", hash))
	if hashDigits == "" {
		hashDigits = "0000000000"
	}
	candidate := fmt.Sprintf("%d%s000000", timestamp, hashDigits)
	candidate = sanitizeDigits(candidate)
	if len(candidate) < serverIDLength {
		candidate += strings.Repeat("0", serverIDLength-len(candidate))
	}
	return candidate[:serverIDLength]
}

func sanitizeDigits(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func logWarning(logger *logging.Logger, format string, args ...interface{}) {
	if logger != nil {
		logger.Warning(format, args...)
	}
}

func logDebug(logger *logging.Logger, format string, args ...interface{}) {
	if logger != nil {
		logger.Debug(format, args...)
	}
}

func setImmutableAttribute(path string, enable bool) error {
	if runtime.GOOS != "linux" {
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if !info.Mode().IsRegular() {
		return nil
	}

	chattrPath, err := exec.LookPath("chattr")
	if err != nil {
		return nil
	}

	flag := "+i"
	if !enable {
		flag = "-i"
	}

	cmd := exec.Command(chattrPath, flag, path)
	if err := cmd.Run(); err != nil {
		return nil
	}

	return nil
}
