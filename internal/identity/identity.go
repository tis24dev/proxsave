package identity

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

const (
	identityDirName          = "identity"
	identityFileName         = ".server_identity"
	notifySecretFileName     = ".notify_secret"
	notifySecretLockFileName = ".notify_secret.lock"
	maxProcVersionBytes      = 100
	maxMachineIDBytes        = 32
	systemKeyPrefixLength    = 8
	serverIDLength           = 16
	// NotifySecretMinLen mirrors logging.secretMinRegister (6): a secret shorter than this
	// is NOT masked in logs, so a too-short value must never reach disk (and later a log
	// line). Enforced at the single sink (PersistNotifySecret) so every caller is covered;
	// exported so the relay provisioner shares the one floor instead of duplicating it.
	NotifySecretMinLen = 6
)

// notifySecretFormat matches the server's generate_notify_secret output: lowercase
// alphanumeric blocks separated by single dashes (e.g. 3h64-dyi8-q3d6-wcm5). It is
// used only to reject a corrupted file, never to reject server-issued values strictly.
var notifySecretFormat = regexp.MustCompile(`^[0-9a-z]+(-[0-9a-z]+)*$`)

// NotifySecretPath returns the immutable identity-file path for the relay secret.
func NotifySecretPath(baseDir string) string {
	return filepath.Join(strings.TrimSpace(baseDir), identityDirName, notifySecretFileName)
}

// PersistNotifySecret writes the per-server relay secret into the same immutable
// identity mechanism used for .server_identity (0600 + chattr +i), reusing
// writeIdentityFileWithContext. Overwrite-safe: the helper clears +i first.
func PersistNotifySecret(ctx context.Context, baseDir, secret string, logger *logging.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		logDebug(logger, "Identity: PersistNotifySecret: empty baseDir, refusing")
		return fmt.Errorf("base directory is empty; cannot persist notify secret")
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		logDebug(logger, "Identity: PersistNotifySecret: empty secret, refusing")
		return fmt.Errorf("refusing to persist an empty notify secret")
	}
	// Validate against the SAME format LoadNotifySecret enforces, so a persisted
	// secret always reloads (otherwise a non-conforming value would be written but
	// silently dropped on the next run, forcing reprovisioning).
	if !notifySecretFormat.MatchString(secret) {
		logDebug(logger, "Identity: PersistNotifySecret: malformed secret, refusing (len=%d)", len(secret))
		return fmt.Errorf("refusing to persist a malformed notify secret")
	}
	// Length floor at the single sink: a secret below NotifySecretMinLen is not masked in
	// logs (redact.go secretMinRegister), so refuse it here so NO caller - the new relay
	// provisioner and the legacy Telegram path alike - can write an unmaskable value. The
	// server format is 19 chars, so a real secret never trips this; it is a defensive floor.
	if n := len([]rune(secret)); n < NotifySecretMinLen {
		logDebug(logger, "Identity: PersistNotifySecret: secret below min length, refusing (len=%d min=%d)", n, NotifySecretMinLen)
		return fmt.Errorf("refusing to persist a notify secret shorter than %d runes", NotifySecretMinLen)
	}
	dir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil { // same mode as Detect
		logDebug(logger, "Identity: PersistNotifySecret: mkdir %s failed: %v", dir, err)
		return fmt.Errorf("failed to create identity directory %s: %w", dir, err)
	}
	path := filepath.Join(dir, notifySecretFileName)
	logDebug(logger, "Identity: PersistNotifySecret: writing immutable secret file %s (len=%d)", path, len(secret))
	if err := writeIdentityFileWithContext(ctx, path, secret+"\n", logger); err != nil {
		logDebug(logger, "Identity: PersistNotifySecret: write failed for %s: %v", path, err)
		return err
	}
	logDebug(logger, "Identity: PersistNotifySecret: persisted (0600 + immutable) to %s", path)
	return nil
}

// LoadNotifySecret returns the persisted relay secret, or ("", nil) when the file
// is absent, empty, or fails the format check (junk is ignored rather than fed
// into the auth header).
func LoadNotifySecret(baseDir string, logger ...*logging.Logger) (string, error) {
	var lg *logging.Logger
	if len(logger) > 0 {
		lg = logger[0]
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return "", nil
	}
	dir := filepath.Join(baseDir, identityDirName)
	path := filepath.Join(dir, notifySecretFileName)
	// Read the secret confined to the identity directory via os.Root so the path is
	// no longer a raw variable sink and a symlink or ".." cannot escape it, resolving
	// the gosec G304 finding structurally (no #nosec). The basename is a constant; a
	// missing directory or file still surfaces as os.ErrNotExist below.
	data, err := readFileUnderRoot(dir, notifySecretFileName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			logDebug(lg, "Identity: LoadNotifySecret: no secret file at %s", path)
			return "", nil
		}
		logDebug(lg, "Identity: LoadNotifySecret: read failed for %s: %v", path, err)
		return "", err
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		logDebug(lg, "Identity: LoadNotifySecret: secret file empty at %s", path)
		return "", nil
	}
	if !notifySecretFormat.MatchString(secret) {
		logDebug(lg, "Identity: LoadNotifySecret: ignoring malformed secret at %s (len=%d)", path, len(secret))
		return "", nil
	}
	logDebug(lg, "Identity: LoadNotifySecret: loaded secret from %s (len=%d)", path, len(secret))
	return secret, nil
}

// readFileUnderRoot reads name (a single basename) from dir through an *os.Root on
// dir, confining the read there at the syscall level: the path is no longer a raw
// variable sink and a symlink or ".." in name cannot escape the directory. This
// mirrors checks.readLockFileContent and resolves the gosec G304 finding
// structurally rather than with a suppression. A missing directory or file
// surfaces as os.ErrNotExist, matching os.ReadFile.
func readFileUnderRoot(dir, name string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	return io.ReadAll(f)
}

// LockNotifySecret takes an exclusive advisory (flock LOCK_EX) lock on a sidecar lock
// file in the identity directory, creating the directory and lock file when absent, so
// relay-secret provisioning (issue -> persist -> confirm) is serialized ACROSS PROCESSES.
// It exists because a concurrent hook a (installer) and hook b (enable-now daemon) can run
// against the same server_id, and two DISTINCT minted secrets strand the host (last-write
// wins on disk vs confirm-locks-reissue on the server). It returns an unlock func the
// caller MUST defer and call exactly once. The lock file is opened confined to the identity
// directory via os.Root (the basename is a constant, mirroring LoadNotifySecret).
func LockNotifySecret(baseDir string) (unlock func(), err error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, fmt.Errorf("base directory is empty; cannot lock notify secret")
	}
	dir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("failed to create identity directory %s: %w", dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f, err := root.OpenFile(notifySecretLockFileName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		_ = root.Close()
		return nil, fmt.Errorf("flock notify secret: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = root.Close()
	}, nil
}

// RemoveNotifySecret deletes the persisted relay secret, first clearing the immutable
// (+i) attribute (unlinking an immutable file returns EPERM) and confining the unlink to
// the identity directory via os.Root (mirrors LoadNotifySecret). It is the remediation for
// a secret the server has DEFINITIVELY rejected (health.ErrHCAuth): clearing it lets the
// next provisioning cycle mint a fresh one, restoring the Telegram path's self-heal. An
// absent file (or absent identity dir) is a no-op, so this is idempotent.
func RemoveNotifySecret(baseDir string, logger ...*logging.Logger) error {
	var lg *logging.Logger
	if len(logger) > 0 {
		lg = logger[0]
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil
	}
	dir := filepath.Join(baseDir, identityDirName)
	path := filepath.Join(dir, notifySecretFileName)
	// Clear +i first so the unlink is permitted; best-effort, exactly like the write path.
	if err := writeIdentityFileWithContextSetImmutable(context.Background(), path, false, lg); err != nil {
		logDebug(lg, "Identity: RemoveNotifySecret: clear immutable failed for %s: %v", path, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(notifySecretFileName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	logDebug(lg, "Identity: RemoveNotifySecret: removed %s", path)
	return nil
}

// RemoveNotifySecretIfMatches deletes the persisted relay secret ONLY when the on-disk value
// still equals rejected, all UNDER LockNotifySecret. It is the value-guarded ErrHCAuth
// remediation: buildReporter loaded secret S_old and the server rejected it (403), so the
// daemon must clear S_old to trigger a re-provision - but NEVER a fresh confirmed S_new that a
// concurrent provisioner (hook a installer / hook c manual Check / hook b daemon) persisted in
// the meantime, since deleting that would strand the host with no centralized healthcheck
// until a manual server-side secret_confirmed reset. It re-reads the secret under the same
// lock the provisioners hold and constant-time-compares it to rejected; on a mismatch (or an
// empty rejected comparand, or an already-absent file) it leaves the file in place and returns
// removed=false. Returns removed=true only when it actually unlinked.
func RemoveNotifySecretIfMatches(baseDir, rejected string, logger ...*logging.Logger) (removed bool, err error) {
	var lg *logging.Logger
	if len(logger) > 0 {
		lg = logger[0]
	}
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return false, nil
	}
	rejected = strings.TrimSpace(rejected)
	if rejected == "" {
		// No comparand: never delete blindly (that is exactly the unconditional-remove
		// regression this function replaces).
		return false, nil
	}
	unlock, lerr := LockNotifySecret(baseDir)
	if lerr != nil {
		return false, lerr
	}
	defer unlock()
	// Re-read UNDER the lock: a concurrent provisioner may have replaced S_old with a fresh
	// confirmed S_new after the caller's rejected fetch.
	current, _ := LoadNotifySecret(baseDir, lg)
	if current == "" {
		return false, nil // already cleared by another path; nothing to do
	}
	if subtle.ConstantTimeCompare([]byte(current), []byte(rejected)) != 1 {
		logDebug(lg, "Identity: RemoveNotifySecretIfMatches: on-disk secret changed since rejection; keeping it")
		return false, nil
	}
	if rmErr := RemoveNotifySecret(baseDir, lg); rmErr != nil {
		return false, rmErr
	}
	return true, nil
}

// Info contains server identity information.
type Info struct {
	ServerID     string
	PrimaryMAC   string
	MACAddresses []string
	IdentityFile string
}

var (
	hostnameFunc                             = os.Hostname
	readFirstLineFunc                        = readFirstLine
	writeIdentityFileWithContextChmod        = os.Chmod
	writeIdentityFileWithContextSetImmutable = setImmutableAttributeWithContext
	identityCreateTempFunc                   = os.CreateTemp
	writeIdentityFileWithContextRename       = os.Rename
)

// Detect resolves the server identity (ID + MAC address) and ensures persistence.
func Detect(baseDir string, logger *logging.Logger) (*Info, error) {
	return DetectWithContext(context.Background(), baseDir, logger)
}

// DetectWithContext resolves the server identity (ID + MAC address) and ensures persistence.
func DetectWithContext(ctx context.Context, baseDir string, logger *logging.Logger) (*Info, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	info := &Info{}
	baseDir = strings.TrimSpace(baseDir)
	logDebug(logger, "Identity: starting detection (baseDir=%q)", baseDir)

	ifaceCandidates, macs := collectMACCandidates(logger)
	info.MACAddresses = macs
	if preferredMAC, preferredIface := selectPreferredMAC(ifaceCandidates); preferredMAC != "" {
		info.PrimaryMAC = preferredMAC
		logDebug(logger, "Identity: selected primary interface=%s mac=%s", preferredIface, preferredMAC)
	} else if len(macs) > 0 {
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
			// Keep the preferred MAC for display, but fall back to the bound MAC if needed.
			if strings.TrimSpace(info.PrimaryMAC) == "" && strings.TrimSpace(boundMAC) != "" {
				info.PrimaryMAC = boundMAC
			}
			if err := maybeUpgradeIdentityFileWithContext(ctx, identityPath, id, info.PrimaryMAC, macs, logger); err != nil {
				return info, err
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
	if err := os.MkdirAll(identityDir, 0o750); err != nil {
		logWarning(logger, "Identity: failed to create identity directory %s: %v (server ID will NOT be persisted)", identityDir, err)
		return info, nil
	}
	logDebug(logger, "Identity: identity directory ready: %s", identityDir)

	logDebug(logger, "Identity: persisting identity file (0600 + immutable) to %s", identityPath)
	if err := writeIdentityFileWithContext(ctx, identityPath, encodedFile, logger); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return info, err
		}
		logWarning(logger, "Identity: failed to write server identity file %s: %v (server ID will NOT be persisted)", identityPath, err)
		return info, nil
	}
	logDebug(logger, "Identity: persisted server ID to %s", identityPath)

	return info, nil
}

type macCandidate struct {
	Iface                 string
	MAC                   string
	AddrAssignType        int
	IsVirtual             bool
	IsBridge              bool
	IsWireless            bool
	IsLocallyAdministered bool
}

func collectMACCandidates(logger *logging.Logger) ([]macCandidate, []string) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var (
		candidates []macCandidate
		macs       []string
	)

	for _, iface := range ifaces {
		if len(iface.HardwareAddr) == 0 {
			continue
		}
		if (iface.Flags & net.FlagLoopback) != 0 {
			continue
		}
		mac := strings.ToLower(iface.HardwareAddr.String())
		if mac == "" {
			continue
		}

		candidates = append(candidates, macCandidate{
			Iface:                 iface.Name,
			MAC:                   mac,
			AddrAssignType:        readAddrAssignType(iface.Name, logger),
			IsVirtual:             isVirtualInterface(iface.Name, logger),
			IsBridge:              isBridgeInterface(iface.Name),
			IsWireless:            isWirelessInterface(iface.Name),
			IsLocallyAdministered: isLocallyAdministeredMAC(mac),
		})

		if _, ok := seen[mac]; ok {
			continue
		}
		seen[mac] = struct{}{}
		macs = append(macs, mac)
	}

	sort.Strings(macs)
	return candidates, macs
}

func selectPreferredMAC(candidates []macCandidate) (string, string) {
	var best *macCandidate
	for i := range candidates {
		c := candidates[i]
		if strings.TrimSpace(c.Iface) == "" || strings.TrimSpace(c.MAC) == "" {
			continue
		}
		if best == nil || isBetterMACCandidate(c, *best) {
			best = &candidates[i]
		}
	}
	if best == nil {
		return "", ""
	}
	return best.MAC, best.Iface
}

func isBetterMACCandidate(a, b macCandidate) bool {
	rankA := candidateRank(a)
	rankB := candidateRank(b)
	for i := 0; i < len(rankA) && i < len(rankB); i++ {
		if rankA[i] == rankB[i] {
			continue
		}
		return rankA[i] < rankB[i]
	}
	nameA := strings.ToLower(a.Iface)
	nameB := strings.ToLower(b.Iface)
	if nameA != nameB {
		return nameA < nameB
	}
	return a.MAC < b.MAC
}

func candidateRank(c macCandidate) []int {
	assignRank := addrAssignRank(c.AddrAssignType)
	virtualRank := 0
	if c.IsVirtual {
		virtualRank = 1
	}
	laaRank := 0
	if c.IsLocallyAdministered {
		laaRank = 1
	}
	return []int{ifaceCategory(c), assignRank, laaRank, virtualRank}
}

func ifaceCategory(c macCandidate) int {
	name := strings.ToLower(strings.TrimSpace(c.Iface))
	switch {
	case isPreferredWiredIface(name, c):
		return 0
	case strings.HasPrefix(name, "vmbr"):
		return 1
	case strings.HasPrefix(name, "bridge") || strings.HasPrefix(name, "br") || c.IsBridge:
		return 2
	case c.IsWireless || strings.HasPrefix(name, "wlp") || strings.HasPrefix(name, "wl"):
		return 3
	default:
		return 4
	}
}

func isPreferredWiredIface(name string, c macCandidate) bool {
	if c.IsWireless {
		return false
	}
	if strings.HasPrefix(name, "eth") || strings.HasPrefix(name, "en") || strings.HasPrefix(name, "bond") || strings.HasPrefix(name, "team") {
		return true
	}
	return false
}

func addrAssignRank(v int) int {
	switch v {
	case 0: // permanent
		return 0
	case 3: // set by userspace
		return 1
	case 2: // stolen
		return 2
	case 1: // random
		return 3
	default:
		return 4
	}
}

func readAddrAssignType(iface string, logger *logging.Logger) int {
	if runtime.GOOS != "linux" {
		return -1
	}
	path := filepath.Join("/sys/class/net", iface, "addr_assign_type")
	raw := strings.TrimSpace(readFirstLineFunc(path, 16))
	if raw == "" {
		return -1
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		logDebug(logger, "Identity: failed to parse addr_assign_type for %s: %v", iface, err)
		return -1
	}
	return v
}

func isVirtualInterface(iface string, logger *logging.Logger) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	link, err := os.Readlink(filepath.Join("/sys/class/net", iface))
	if err != nil {
		return false
	}
	if strings.Contains(link, "/virtual/") {
		logDebug(logger, "Identity: iface %s is virtual (%s)", iface, link)
		return true
	}
	return false
}

func isBridgeInterface(iface string) bool {
	if runtime.GOOS != "linux" {
		return isBridgeInterfaceByName(iface)
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", iface, "bridge"))
	return err == nil
}

func isBridgeInterfaceByName(iface string) bool {
	name := strings.ToLower(iface)
	return strings.HasPrefix(name, "vmbr") || strings.HasPrefix(name, "br") || strings.HasPrefix(name, "bridge")
}

func isWirelessInterface(iface string) bool {
	if runtime.GOOS != "linux" {
		return isWirelessInterfaceByName(iface)
	}
	_, err := os.Stat(filepath.Join("/sys/class/net", iface, "wireless"))
	return err == nil
}

func isWirelessInterfaceByName(iface string) bool {
	return strings.HasPrefix(strings.ToLower(iface), "wl")
}

func isLocallyAdministeredMAC(mac string) bool {
	fields := strings.Split(mac, ":")
	if len(fields) == 0 {
		return false
	}
	b, err := strconv.ParseUint(fields[0], 16, 8)
	if err != nil {
		return false
	}
	return (b & 0x02) == 0x02
}

func loadServerID(path string, macs []string, logger *logging.Logger) (string, string, error) {
	if stat, err := os.Stat(path); err == nil {
		logDebug(logger, "Identity: identity file stat: path=%s mode=%s size=%d mtime=%s", path, stat.Mode().String(), stat.Size(), stat.ModTime().Format(time.RFC3339))
	} else {
		logDebug(logger, "Identity: identity file stat failed: path=%s err=%v", path, err)
	}

	// Read confined to the identity directory via os.Root (structural gosec G304
	// fix, no #nosec); see readFileUnderRoot.
	data, err := readFileUnderRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", "", err
	}
	logDebug(logger, "Identity: read identity file %s (%d bytes)", path, len(data))

	content := string(data)
	if len(macs) == 0 {
		id, _, err := decodeProtectedServerID(content, "", logger)
		if err != nil {
			return "", "", err
		}
		return id, "", nil
	}

	var lastErr error
	var fallbackID string
	for idx, mac := range macs {
		id, matchedByMAC, err := decodeProtectedServerID(content, mac, logger)
		if err == nil {
			if matchedByMAC {
				return id, mac, nil
			}
			if fallbackID == "" {
				fallbackID = id
			}
			continue
		}
		lastErr = err
		logDebug(logger, "Identity: decode attempt failed for mac[%d]=%s: %v", idx, mac, err)
	}

	if fallbackID != "" {
		return fallbackID, "", nil
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

	encoded, err := encodeProtectedServerIDWithMACs(serverID, macs, primaryMAC, logger)
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

	if machineID := readFirstLineFunc("/etc/machine-id", maxMachineIDBytes); machineID != "" {
		builder.WriteString(machineID)
		logDebug(logger, "Identity: buildSystemData: machine-id source=/etc/machine-id len=%d", len(machineID))
	} else if machineID := readFirstLineFunc("/var/lib/dbus/machine-id", maxMachineIDBytes); machineID != "" {
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

	hostname, err := hostnameFunc()
	if err == nil && hostname != "" {
		builder.WriteString(hostname)
		logDebug(logger, "Identity: buildSystemData: hostname=%q len=%d", hostname, len(hostname))
	} else {
		logDebug(logger, "Identity: buildSystemData: hostname unavailable (err=%v)", err)
	}

	if uuid := readFirstLineFunc("/sys/class/dmi/id/product_uuid", maxMachineIDBytes); uuid != "" {
		builder.WriteString(uuid)
		logDebug(logger, "Identity: buildSystemData: product_uuid present len=%d", len(uuid))
	} else {
		logDebug(logger, "Identity: buildSystemData: product_uuid missing")
	}

	if version := readFirstLineFunc("/proc/version", maxProcVersionBytes); version != "" {
		builder.WriteString(version)
		logDebug(logger, "Identity: buildSystemData: /proc/version present len=%d", len(version))
	} else {
		logDebug(logger, "Identity: buildSystemData: /proc/version missing")
	}

	if builder.Len() == 0 {
		fmt.Fprintf(&builder, "fallback-%d-%d", time.Now().Unix(), os.Getpid())
		logDebug(logger, "Identity: buildSystemData: WARNING: used fallback seed (unexpected)")
	}

	logDebug(logger, "Identity: buildSystemData: final length=%d", builder.Len())
	return builder.String()
}

func encodeProtectedServerIDWithMACs(serverID string, macs []string, primaryMAC string, logger *logging.Logger) (string, error) {
	logDebug(logger, "Identity: encodeProtectedServerID: start (serverID=%s primaryMAC=%s)", serverID, primaryMAC)
	keyField := buildIdentityKeyField(macs, primaryMAC, logger)
	timestamp := time.Now().Unix()
	data := fmt.Sprintf("%s:%d:%s", serverID, timestamp, keyField)
	checksum := sha256.Sum256([]byte(data))
	finalData := fmt.Sprintf("%s:%s", data, fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength])
	encoded := base64.StdEncoding.EncodeToString([]byte(finalData))
	logDebug(logger, "Identity: encodeProtectedServerID: timestamp=%d keyFieldLen=%d checksumPrefix=%s payloadLen=%d b64Len=%d", timestamp, len(keyField), fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength], len(finalData), len(encoded))

	var builder strings.Builder
	builder.WriteString("# ProxSave Backup System Configuration\n")
	fmt.Fprintf(&builder, "# Generated: %s\n", time.Now().Format(time.RFC3339))
	builder.WriteString("# DO NOT MODIFY THIS FILE MANUALLY\n")
	builder.WriteString("# Format: proxsave-identity-v2\n")
	fmt.Fprintf(&builder, "SYSTEM_CONFIG_DATA=\"%s\"\n", encoded)
	builder.WriteString("# End of configuration\n")

	content := builder.String()
	logDebug(logger, "Identity: encodeProtectedServerID: generated identity file content bytes=%d", len(content))
	return content, nil
}

func decodeProtectedServerID(fileContent, primaryMAC string, logger *logging.Logger) (string, bool, error) {
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
		return "", false, fmt.Errorf("identity data not found")
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		logDebug(logger, "Identity: decodeProtectedServerID: base64 decode failed: %v", err)
		return "", false, fmt.Errorf("invalid encoded identity data: %w", err)
	}
	logDebug(logger, "Identity: decodeProtectedServerID: decoded payload bytes=%d", len(decodedBytes))

	parts := strings.Split(string(decodedBytes), ":")
	logDebug(logger, "Identity: decodeProtectedServerID: payload parts=%d", len(parts))
	if len(parts) != 4 {
		return "", false, fmt.Errorf("invalid identity payload format")
	}

	serverID, timestamp, keyField, checksum := parts[0], parts[1], parts[2], parts[3]
	logDebug(logger, "Identity: decodeProtectedServerID: parsed serverID=%q ts=%q keyFieldLen=%d checksumPrefix=%q", serverID, timestamp, len(keyField), checksum)
	data := fmt.Sprintf("%s:%s:%s", serverID, timestamp, keyField)
	expectedChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:systemKeyPrefixLength]
	if checksum != expectedChecksum {
		logDebug(logger, "Identity: decodeProtectedServerID: checksum mismatch (stored=%s expected=%s)", checksum, expectedChecksum)
		return "", false, fmt.Errorf("identity checksum mismatch")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: checksum ok (%s)", expectedChecksum)

	storedPrefixes := parseKeyFieldPrefixes(keyField)
	currentPrefixes := computeCurrentIdentityKeyPrefixes(primaryMAC, logger)

	macPrefixes := computeCurrentMACKeyPrefixes(primaryMAC, logger)

	matched := false
	matchedByMAC := false
	for _, prefix := range storedPrefixes {
		if prefix == "" {
			continue
		}
		if currentPrefixes[prefix] {
			matched = true
			if macPrefixes[prefix] {
				matchedByMAC = true
			}
			break
		}
	}
	if !matched {
		logDebug(logger, "Identity: decodeProtectedServerID: no matching identity key prefix found")
		return "", false, fmt.Errorf("identity file does not belong to this host")
	}

	if len(serverID) != serverIDLength || !isAllDigits(serverID) {
		logDebug(logger, "Identity: decodeProtectedServerID: invalid server ID format (len=%d digits=%v)", len(serverID), isAllDigits(serverID))
		return "", false, fmt.Errorf("invalid server ID format")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: server ID format ok (len=%d)", len(serverID))
	return serverID, matchedByMAC, nil
}

func buildIdentityKeyField(macs []string, primaryMAC string, logger *logging.Logger) string {
	machineID := readMachineID(logger)
	hostnamePart := readHostnamePart(logger)
	primaryMAC = normalizeMAC(primaryMAC)

	uuid := strings.TrimSpace(readFirstLineFunc("/sys/class/dmi/id/product_uuid", maxMachineIDBytes))

	uniqueMACs := make(map[string]struct{}, len(macs)+1)
	orderedMACs := make([]string, 0, len(macs)+1)
	if primaryMAC != "" {
		uniqueMACs[primaryMAC] = struct{}{}
		orderedMACs = append(orderedMACs, primaryMAC)
	}
	for _, mac := range macs {
		mac = normalizeMAC(mac)
		if mac == "" {
			continue
		}
		if _, ok := uniqueMACs[mac]; ok {
			continue
		}
		uniqueMACs[mac] = struct{}{}
		orderedMACs = append(orderedMACs, mac)
	}
	if len(orderedMACs) > 1 {
		sort.Strings(orderedMACs[1:])
	}

	entries := make([]string, 0, len(orderedMACs)*2+4)
	altIndex := 1
	for _, mac := range orderedMACs {
		macPart := strings.ReplaceAll(mac, ":", "")
		prefix := computeSystemKey(machineID, hostnamePart, macPart)[:systemKeyPrefixLength]
		prefixNoHost := computeSystemKey(machineID, "", macPart)[:systemKeyPrefixLength]

		if primaryMAC != "" && mac == primaryMAC {
			entries = append(entries, "mac="+prefix, "mac_nohost="+prefixNoHost)
			continue
		}
		entries = append(
			entries,
			fmt.Sprintf("mac_alt%d=%s", altIndex, prefix),
			fmt.Sprintf("mac_alt%d_nohost=%s", altIndex, prefixNoHost),
		)
		altIndex++
	}

	if uuid != "" {
		uuidPrefix := computeSystemKey(machineID, hostnamePart, uuid)[:systemKeyPrefixLength]
		uuidNoHostPrefix := computeSystemKey(machineID, "", uuid)[:systemKeyPrefixLength]
		entries = append(entries, "uuid="+uuidPrefix, "uuid_nohost="+uuidNoHostPrefix)
	}

	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		label := strings.TrimSpace(parts[0])
		prefix := strings.TrimSpace(parts[1])
		if label == "" || prefix == "" {
			continue
		}
		key := label + "=" + prefix
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	return strings.Join(out, ",")
}

func parseKeyFieldPrefixes(keyField string) []string {
	keyField = strings.TrimSpace(keyField)
	if keyField == "" {
		return nil
	}
	tokens := strings.Split(keyField, ",")
	out := make([]string, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if idx := strings.IndexByte(token, '='); idx >= 0 {
			token = strings.TrimSpace(token[idx+1:])
		}
		out = append(out, token)
	}
	return out
}

func computeCurrentIdentityKeyPrefixes(primaryMAC string, logger *logging.Logger) map[string]bool {
	machineID := readMachineID(logger)
	hostnamePart := readHostnamePart(logger)
	uuid := strings.TrimSpace(readFirstLineFunc("/sys/class/dmi/id/product_uuid", maxMachineIDBytes))

	primaryMAC = normalizeMAC(primaryMAC)
	macPart := strings.ReplaceAll(primaryMAC, ":", "")

	out := make(map[string]bool, 4)
	if macPart != "" {
		out[computeSystemKey(machineID, hostnamePart, macPart)[:systemKeyPrefixLength]] = true
		out[computeSystemKey(machineID, "", macPart)[:systemKeyPrefixLength]] = true
	}

	if uuid != "" {
		out[computeSystemKey(machineID, hostnamePart, uuid)[:systemKeyPrefixLength]] = true
		out[computeSystemKey(machineID, "", uuid)[:systemKeyPrefixLength]] = true
	}

	return out
}

func computeCurrentMACKeyPrefixes(primaryMAC string, logger *logging.Logger) map[string]bool {
	machineID := readMachineID(logger)
	hostnamePart := readHostnamePart(logger)
	primaryMAC = normalizeMAC(primaryMAC)
	macPart := strings.ReplaceAll(primaryMAC, ":", "")

	out := make(map[string]bool, 2)
	if macPart != "" {
		out[computeSystemKey(machineID, hostnamePart, macPart)[:systemKeyPrefixLength]] = true
		out[computeSystemKey(machineID, "", macPart)[:systemKeyPrefixLength]] = true
	}
	return out
}

func readMachineID(logger *logging.Logger) string {
	machineID := readFirstLineFunc("/etc/machine-id", maxMachineIDBytes)
	machineIDSource := "/etc/machine-id"
	if machineID == "" {
		machineID = readFirstLineFunc("/var/lib/dbus/machine-id", maxMachineIDBytes)
		machineIDSource = "/var/lib/dbus/machine-id"
	}
	if machineID != "" {
		logDebug(logger, "Identity: machine-id source=%s len=%d", machineIDSource, len(machineID))
	} else {
		logDebug(logger, "Identity: machine-id missing")
	}
	return machineID
}

func readHostnamePart(logger *logging.Logger) string {
	hostname, err := hostnameFunc()
	if err != nil || strings.TrimSpace(hostname) == "" {
		logDebug(logger, "Identity: hostname missing (err=%v)", err)
		return ""
	}
	hostnamePart := hostname
	if len(hostnamePart) > 8 {
		hostnamePart = hostnamePart[:8]
	}
	logDebug(logger, "Identity: hostnamePart=%q len=%d (origLen=%d)", hostnamePart, len(hostnamePart), len(hostname))
	return hostnamePart
}

func computeSystemKey(machineID, hostnamePart, extra string) string {
	sum := sha256.Sum256([]byte(machineID + hostnamePart + extra))
	return fmt.Sprintf("%x", sum)[:16]
}

func maybeUpgradeIdentityFileWithContext(ctx context.Context, path string, serverID string, primaryMAC string, macs []string, logger *logging.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Read confined to the identity directory via os.Root (structural gosec G304
	// fix, no #nosec); see readFileUnderRoot.
	data, err := readFileUnderRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return nil
	}
	if identityPayloadHasKeyLabels(string(data), logger) {
		return nil
	}
	updated, err := encodeProtectedServerIDWithMACs(serverID, macs, primaryMAC, logger)
	if err != nil {
		return err
	}
	if err := writeIdentityFileWithContext(ctx, path, updated, logger); err != nil {
		logDebug(logger, "Identity: failed to upgrade identity file format: %v", err)
		return err
	}
	return nil
}

func normalizeMAC(mac string) string {
	mac = strings.TrimSpace(strings.ToLower(mac))
	if mac == "" {
		return ""
	}
	if hw, err := net.ParseMAC(mac); err == nil {
		return strings.ToLower(hw.String())
	}
	return mac
}

func identityPayloadHasKeyLabels(fileContent string, logger *logging.Logger) bool {
	scanner := bufio.NewScanner(strings.NewReader(fileContent))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "SYSTEM_CONFIG_DATA=") {
			continue
		}
		encoded := strings.Trim(line[len("SYSTEM_CONFIG_DATA="):], "\"")
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return false
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 4 {
			return false
		}
		return strings.Contains(parts[2], "=")
	}
	if err := scanner.Err(); err != nil {
		logDebug(logger, "Identity: identityPayloadHasKeyLabels scanner error: %v", err)
	}
	return false
}

// atomicWriteIdentityFile writes data to path via a temp sibling + rename, so a write
// ERROR never leaves a truncated/zero-byte identity or secret file: on any error the
// temp is removed and the existing file is left untouched. It deliberately does NOT
// fsync (unlike the systemd-unit writer), so a power loss in the narrow window after
// the rename can still lose the NEW content; that is acceptable here because the secret
// is re-provisioned via TOFU and the server identity is re-derived on the next run. The
// caller has already cleared the +i immutable attribute on any existing target
// (renaming over an immutable file returns EPERM) and re-sets +i on the new inode
// afterward.
func atomicWriteIdentityFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := identityCreateTempFunc(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := writeIdentityFileWithContextRename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func writeIdentityFileWithContext(ctx context.Context, path, content string, logger *logging.Logger) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logDebug(logger, "Identity: writeIdentityFile: start path=%s contentBytes=%d", path, len(content))

	// Ensure file is writable even if immutable was previously set
	if err := writeIdentityFileWithContextSetImmutable(ctx, path, false, logger); err != nil {
		return err
	}
	defer func() {
		lockErr := writeIdentityFileWithContextSetImmutable(context.Background(), path, true, logger)
		if lockErr == nil {
			return
		}
		logDebug(logger, "Identity: writeIdentityFile: failed to restore immutable attribute: %v", lockErr)
		if err == nil {
			err = lockErr
		}
	}()

	if err := ctx.Err(); err != nil {
		logDebug(logger, "Identity: writeIdentityFile: context canceled before write for %s: %v", path, err)
		return err
	}

	if err := atomicWriteIdentityFile(path, []byte(content), 0o600); err != nil {
		logDebug(logger, "Identity: writeIdentityFile: atomic write failed: %v", err)
		return err
	}

	if err := writeIdentityFileWithContextChmod(path, 0o600); err != nil {
		logDebug(logger, "Identity: writeIdentityFile: os.Chmod failed: %v", err)
		return err
	}

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

func setImmutableAttributeWithContext(ctx context.Context, path string, enable bool, logger *logging.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		logDebug(logger, "Identity: immutable: context canceled before chattr for %s: %v", path, err)
		return err
	}

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

	cmd, err := safeexec.TrustedCommandContext(ctx, chattrPath, flag, path)
	if err != nil {
		logDebug(logger, "Identity: immutable: chattr path rejected for %s: %v", path, err)
		return nil
	}
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			logDebug(logger, "Identity: immutable: chattr canceled for %s: %v", path, ctxErr)
			return ctxErr
		}
		logDebug(logger, "Identity: immutable: chattr failed (ignored): %v", err)
		return nil
	}

	logDebug(logger, "Identity: immutable: applied %s on %s", flag, path)
	return nil
}
