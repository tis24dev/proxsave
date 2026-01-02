package identity

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"errors"
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
	baseDir = strings.TrimSpace(baseDir)
	logDebug(logger, "Identity: starting detection (baseDir=%q)", baseDir)

	macs := collectMACAddresses()
	info.MACAddresses = macs
	if len(macs) > 0 {
		info.PrimaryMAC = macs[0]
	}
	logDebug(logger, "Identity: detected %d MAC addresses (primary: %s)", len(macs), info.PrimaryMAC)

	if baseDir == "" {
		err := fmt.Errorf("base directory is empty; cannot resolve identity file location")
		logWarning(logger, "Identity: %v", err)
		return info, err
	}
	identityDir := filepath.Join(baseDir, identityDirName)
	identityPath := filepath.Join(identityDir, identityFileName)
	info.IdentityFile = identityPath
	logDebug(logger, "Identity: baseDir=%q identityDir=%q identityFile=%q", baseDir, identityDir, identityPath)

	// Attempt to load an existing ID first.
	logDebug(logger, "Identity: attempting to load existing identity from %s", identityPath)
	if id, boundMAC, err := loadServerID(identityPath, macs, logger); err == nil {
		if id != "" {
			logDebug(logger, "Identity: loaded existing server ID %s from %s (boundMAC=%s)", id, identityPath, boundMAC)
			info.ServerID = id
			if strings.TrimSpace(boundMAC) != "" {
				info.PrimaryMAC = boundMAC
			}
			return info, nil
		}
		logDebug(logger, "Identity: identity file %s returned empty server ID; generating new one", identityPath)
	} else {
		if errors.Is(err, os.ErrNotExist) {
			logDebug(logger, "Identity: identity file %s not found; generating new server ID", identityPath)
		} else {
			logWarning(logger, "Identity: failed to load identity file %s: %v (will generate a new server ID)", identityPath, err)
		}
	}

	logDebug(logger, "Identity: generating a new server ID (identity file missing/invalid)")
	serverID, encodedFile, err := generateServerID(macs, info.PrimaryMAC, logger)
	if err != nil {
		return info, err
	}
	info.ServerID = serverID
	logDebug(logger, "Identity: generated new server ID %s", serverID)

	logDebug(logger, "Identity: ensuring identity directory exists at %s", identityDir)
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		logWarning(logger, "Identity: failed to create identity directory %s: %v (server ID will NOT be persisted)", identityDir, err)
		return info, nil
	}
	logDebug(logger, "Identity: identity directory ready: %s", identityDir)

	logDebug(logger, "Identity: persisting identity file (0600 + immutable) to %s", identityPath)
	if err := writeIdentityFile(identityPath, encodedFile, logger); err != nil {
		logWarning(logger, "Identity: failed to write server identity file %s: %v (server ID will NOT be persisted)", identityPath, err)
		return info, nil
	}
	logDebug(logger, "Identity: persisted server ID to %s", identityPath)

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

func loadServerID(path string, macs []string, logger *logging.Logger) (string, string, error) {
	if stat, err := os.Stat(path); err == nil {
		logDebug(logger, "Identity: identity file stat: path=%s mode=%s size=%d mtime=%s", path, stat.Mode().String(), stat.Size(), stat.ModTime().Format(time.RFC3339))
	} else {
		logDebug(logger, "Identity: identity file stat failed: path=%s err=%v", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	logDebug(logger, "Identity: read identity file %s (%d bytes)", path, len(data))

	content := string(data)
	if len(macs) == 0 {
		id, err := decodeProtectedServerID(content, "", logger)
		if err != nil {
			return "", "", err
		}
		return id, "", nil
	}

	var lastErr error
	for idx, mac := range macs {
		id, err := decodeProtectedServerID(content, mac, logger)
		if err == nil {
			return id, mac, nil
		}
		lastErr = err
		logDebug(logger, "Identity: decode attempt failed for mac[%d]=%s: %v", idx, mac, err)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("unable to decode identity payload")
	}
	return "", "", lastErr
}

func generateServerID(macs []string, primaryMAC string, logger *logging.Logger) (string, string, error) {
	logDebug(logger, "Identity: generateServerID: starting (primaryMAC=%s macCount=%d)", primaryMAC, len(macs))
	systemData := buildSystemData(macs, logger)
	logDebug(logger, "Identity: generateServerID: systemData length=%d", len(systemData))

	hash := sha256.Sum256([]byte(systemData))
	hexString := fmt.Sprintf("%x", hash)
	logDebug(logger, "Identity: generateServerID: sha256=%s", hexString)
	if len(hexString) < serverIDLength {
		hexString = hexString + strings.Repeat("0", serverIDLength-len(hexString))
	}

	hexPart := hexString[:serverIDLength]
	logDebug(logger, "Identity: generateServerID: hexPart=%s", hexPart)
	decimalID := hexToDecimal(hexPart)
	if decimalID == "" {
		logDebug(logger, "Identity: generateServerID: hexToDecimal failed; falling back to sanitizeDigits")
		decimalID = sanitizeDigits(hexPart)
	} else {
		logDebug(logger, "Identity: generateServerID: hexToDecimal ok (len=%d)", len(decimalID))
	}

	serverID := normalizeServerID(decimalID, hash[:])
	if serverID == "" {
		return "", "", fmt.Errorf("unable to compute server ID")
	}
	logDebug(logger, "Identity: generateServerID: normalized serverID=%s", serverID)

	encoded, err := encodeProtectedServerID(serverID, primaryMAC, logger)
	if err != nil {
		logDebug(logger, "Identity: generateServerID: encodeProtectedServerID failed: %v", err)
		return "", "", err
	}
	logDebug(logger, "Identity: generateServerID: encoded identity file bytes=%d", len(encoded))

	return serverID, encoded, nil
}

func buildSystemData(macs []string, logger *logging.Logger) string {
	var builder strings.Builder
	timestamp := time.Now().UTC().Format("20060102150405")
	builder.WriteString(timestamp)
	logDebug(logger, "Identity: buildSystemData: timestamp=%s", timestamp)

	if machineID := readFirstLine("/etc/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
		logDebug(logger, "Identity: buildSystemData: machine-id source=/etc/machine-id len=%d", len(machineID))
	} else if machineID := readFirstLine("/var/lib/dbus/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
		logDebug(logger, "Identity: buildSystemData: machine-id source=/var/lib/dbus/machine-id len=%d", len(machineID))
	} else {
		logDebug(logger, "Identity: buildSystemData: machine-id missing")
	}

	if len(macs) > 0 {
		joined := strings.Join(macs, ":")
		builder.WriteString(joined)
		logDebug(logger, "Identity: buildSystemData: macs count=%d joinedLen=%d", len(macs), len(joined))
		for idx, mac := range macs {
			logDebug(logger, "Identity: buildSystemData: mac[%d]=%s", idx, mac)
		}
	} else {
		logDebug(logger, "Identity: buildSystemData: no MAC addresses detected")
	}

	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		builder.WriteString(hostname)
		logDebug(logger, "Identity: buildSystemData: hostname=%q len=%d", hostname, len(hostname))
	} else {
		logDebug(logger, "Identity: buildSystemData: hostname unavailable (err=%v)", err)
	}

	if uuid := readFirstLine("/sys/class/dmi/id/product_uuid", maxMachineIDBytes); uuid != "" {
		builder.WriteString(uuid)
		logDebug(logger, "Identity: buildSystemData: product_uuid present len=%d", len(uuid))
	} else {
		logDebug(logger, "Identity: buildSystemData: product_uuid missing")
	}

	if version := readFirstLine("/proc/version", maxProcVersionBytes); version != "" {
		builder.WriteString(version)
		logDebug(logger, "Identity: buildSystemData: /proc/version present len=%d", len(version))
	} else {
		logDebug(logger, "Identity: buildSystemData: /proc/version missing")
	}

	if builder.Len() == 0 {
		builder.WriteString(fmt.Sprintf("fallback-%d-%d", time.Now().Unix(), os.Getpid()))
		logDebug(logger, "Identity: buildSystemData: WARNING: used fallback seed (unexpected)")
	}

	logDebug(logger, "Identity: buildSystemData: final length=%d", builder.Len())
	return builder.String()
}

func encodeProtectedServerID(serverID, primaryMAC string, logger *logging.Logger) (string, error) {
	logDebug(logger, "Identity: encodeProtectedServerID: start (serverID=%s primaryMAC=%s)", serverID, primaryMAC)
	systemKey := generateSystemKey(primaryMAC, logger)
	timestamp := time.Now().Unix()
	data := fmt.Sprintf("%s:%d:%s", serverID, timestamp, systemKey[:systemKeyPrefixLength])
	checksum := sha256.Sum256([]byte(data))
	finalData := fmt.Sprintf("%s:%s", data, fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength])
	encoded := base64.StdEncoding.EncodeToString([]byte(finalData))
	logDebug(logger, "Identity: encodeProtectedServerID: timestamp=%d keyPrefix=%s checksumPrefix=%s payloadLen=%d b64Len=%d", timestamp, systemKey[:systemKeyPrefixLength], fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength], len(finalData), len(encoded))

	var builder strings.Builder
	builder.WriteString("# ProxSave Backup System Configuration\n")
	builder.WriteString(fmt.Sprintf("# Generated: %s\n", time.Now().Format(time.RFC3339)))
	builder.WriteString("# DO NOT MODIFY THIS FILE MANUALLY\n")
	builder.WriteString(fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded))
	builder.WriteString("# End of configuration\n")

	content := builder.String()
	logDebug(logger, "Identity: encodeProtectedServerID: generated identity file content bytes=%d", len(content))
	return content, nil
}

func decodeProtectedServerID(fileContent, primaryMAC string, logger *logging.Logger) (string, error) {
	logDebug(logger, "Identity: decodeProtectedServerID: start (primaryMAC=%s fileBytes=%d)", primaryMAC, len(fileContent))

	scanner := bufio.NewScanner(strings.NewReader(fileContent))
	var encoded string
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "SYSTEM_CONFIG_DATA=") {
			encoded = strings.Trim(line[len("SYSTEM_CONFIG_DATA="):], "\"")
			logDebug(logger, "Identity: decodeProtectedServerID: found SYSTEM_CONFIG_DATA at line %d (b64Len=%d)", lineNo, len(encoded))
			break
		}
	}
	if err := scanner.Err(); err != nil {
		logDebug(logger, "Identity: decodeProtectedServerID: scanner error: %v", err)
	}
	if encoded == "" {
		logDebug(logger, "Identity: decodeProtectedServerID: SYSTEM_CONFIG_DATA not found")
		return "", fmt.Errorf("identity data not found")
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		logDebug(logger, "Identity: decodeProtectedServerID: base64 decode failed: %v", err)
		return "", fmt.Errorf("invalid encoded identity data: %w", err)
	}
	logDebug(logger, "Identity: decodeProtectedServerID: decoded payload bytes=%d", len(decodedBytes))

	parts := strings.Split(string(decodedBytes), ":")
	logDebug(logger, "Identity: decodeProtectedServerID: payload parts=%d", len(parts))
	if len(parts) != 4 {
		return "", fmt.Errorf("invalid identity payload format")
	}

	serverID, timestamp, systemKey, checksum := parts[0], parts[1], parts[2], parts[3]
	logDebug(logger, "Identity: decodeProtectedServerID: parsed serverID=%q ts=%q keyPrefix=%q checksumPrefix=%q", serverID, timestamp, systemKey, checksum)
	data := fmt.Sprintf("%s:%s:%s", serverID, timestamp, systemKey)
	expectedChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:systemKeyPrefixLength]
	if checksum != expectedChecksum {
		logDebug(logger, "Identity: decodeProtectedServerID: checksum mismatch (stored=%s expected=%s)", checksum, expectedChecksum)
		return "", fmt.Errorf("identity checksum mismatch")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: checksum ok (%s)", expectedChecksum)

	currentKey := generateSystemKey(primaryMAC, logger)
	if len(systemKey) > len(currentKey) {
		logDebug(logger, "Identity: decodeProtectedServerID: trimming stored keyPrefix from %d to %d", len(systemKey), len(currentKey))
		systemKey = systemKey[:len(currentKey)]
	}
	if systemKey != currentKey[:len(systemKey)] {
		logDebug(logger, "Identity: decodeProtectedServerID: system key mismatch (stored=%s current=%s)", systemKey, currentKey[:len(systemKey)])
		return "", fmt.Errorf("identity file does not belong to this host")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: system key ok (prefixLen=%d)", len(systemKey))

	if len(serverID) != serverIDLength || !isAllDigits(serverID) {
		logDebug(logger, "Identity: decodeProtectedServerID: invalid server ID format (len=%d digits=%v)", len(serverID), isAllDigits(serverID))
		return "", fmt.Errorf("invalid server ID format")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: server ID format ok (len=%d)", len(serverID))
	return serverID, nil
}

func generateSystemKey(primaryMAC string, logger *logging.Logger) string {
	var builder strings.Builder

	machineID := readFirstLine("/etc/machine-id", maxMachineIDBytes)
	machineIDSource := "/etc/machine-id"
	if machineID == "" {
		machineID = readFirstLine("/var/lib/dbus/machine-id", maxMachineIDBytes)
		machineIDSource = "/var/lib/dbus/machine-id"
	}
	if machineID != "" {
		builder.WriteString(machineID)
		logDebug(logger, "Identity: generateSystemKey: machine-id source=%s len=%d", machineIDSource, len(machineID))
	} else {
		logDebug(logger, "Identity: generateSystemKey: machine-id missing")
	}

	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		hostnamePart := hostname
		if len(hostnamePart) > 8 {
			hostnamePart = hostnamePart[:8]
		}
		builder.WriteString(hostnamePart)
		logDebug(logger, "Identity: generateSystemKey: hostnamePart=%q len=%d (origLen=%d)", hostnamePart, len(hostnamePart), len(hostname))
	} else {
		logDebug(logger, "Identity: generateSystemKey: hostname missing (err=%v)", err)
	}

	macPart := strings.ReplaceAll(primaryMAC, ":", "")
	builder.WriteString(macPart)
	logDebug(logger, "Identity: generateSystemKey: macPart=%q len=%d", macPart, len(macPart))

	materialLen := builder.Len()
	sum := sha256.Sum256([]byte(builder.String()))
	sumHex := fmt.Sprintf("%x", sum)
	systemKey := sumHex[:16]
	logDebug(logger, "Identity: generateSystemKey: materialLen=%d sha256=%s systemKey=%s", materialLen, sumHex, systemKey)
	return systemKey
}

func writeIdentityFile(path, content string, logger *logging.Logger) error {
	logDebug(logger, "Identity: writeIdentityFile: start path=%s contentBytes=%d", path, len(content))

	// Ensure file is writable even if immutable was previously set
	_ = setImmutableAttribute(path, false, logger)

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		logDebug(logger, "Identity: writeIdentityFile: os.WriteFile failed: %v", err)
		return err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		logDebug(logger, "Identity: writeIdentityFile: os.Chmod failed: %v", err)
		return err
	}

	_ = setImmutableAttribute(path, true, logger)

	logDebug(logger, "Identity: writeIdentityFile: done path=%s", path)
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

func setImmutableAttribute(path string, enable bool, logger *logging.Logger) error {
	if runtime.GOOS != "linux" {
		logDebug(logger, "Identity: immutable: skip (GOOS=%s)", runtime.GOOS)
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			logDebug(logger, "Identity: immutable: skip missing file %s", path)
			return nil
		}
		logDebug(logger, "Identity: immutable: stat failed for %s: %v", path, err)
		return err
	}

	if !info.Mode().IsRegular() {
		logDebug(logger, "Identity: immutable: skip non-regular file %s (mode=%s)", path, info.Mode().String())
		return nil
	}

	chattrPath, err := exec.LookPath("chattr")
	if err != nil {
		logDebug(logger, "Identity: immutable: chattr not found; skip (path=%s)", path)
		return nil
	}

	flag := "+i"
	if !enable {
		flag = "-i"
	}

	cmd := exec.Command(chattrPath, flag, path)
	if err := cmd.Run(); err != nil {
		logDebug(logger, "Identity: immutable: chattr failed (ignored): %v", err)
		return nil
	}

	logDebug(logger, "Identity: immutable: applied %s on %s", flag, path)
	return nil
}
