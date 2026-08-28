package identity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// machineIDPath, dbusMachineIDPath and productUUIDPath are the single source of
	// truth for the host-fact paths. They used to be repeated literals (machine-id
	// twice, product_uuid three times), which is the crudest way for the write side
	// and the read side of the identity key to drift apart.
	machineIDPath     = "/etc/machine-id"
	dbusMachineIDPath = "/var/lib/dbus/machine-id"
	productUUIDPath   = "/sys/class/dmi/id/product_uuid"
	procVersionPath   = "/proc/version"
	// machineIDKeyLabel names the key arm derived from /etc/machine-id alone, and
	// uuidKeyLabel / uuidNoHostKeyLabel name the DMI pair that predates it. They follow
	// the existing lowercase family scheme (mac=, mac_nohost=, mac_altN=, uuid=).
	//
	// The machine-id arm has NO "_nohost" twin on purpose: it is hostname free by
	// construction, so a hostname bearing twin could only ever match where the hostname
	// free one already matches.
	machineIDKeyLabel  = "machineid"
	uuidKeyLabel       = "uuid"
	uuidNoHostKeyLabel = "uuid_nohost"
	// identityRejectedSuffix names a preserved identity payload, and
	// quarantineNameHexLen is how much of the payload's sha256 goes into that name.
	// The name is CONTENT ADDRESSED, so preserving the same payload twice reuses one
	// file instead of accumulating one per run.
	identityRejectedSuffix = ".rejected-"
	quarantineNameHexLen   = 16
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
	collectMACCandidatesFunc                 = collectMACCandidates
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

	ifaceCandidates, macs := collectMACCandidatesFunc(logger)
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

	// ONE fact snapshot per run, threaded into every decision below, so the write side
	// and the read side cannot see different readings even if the underlying files
	// change mid run.
	facts := readHostFacts(logger)

	// Attempt to load an existing ID first.
	logDebug(logger, "Identity: attempting to load existing identity from %s", identityPath)
	if res, err := loadServerID(identityPath, macs, info.PrimaryMAC, facts, logger); err == nil {
		if res.ServerID != "" {
			logDebug(logger, "Identity: loaded existing server ID %s from %s (verified=%v)", res.ServerID, identityPath, res.Verified)
			info.ServerID = res.ServerID
			if err := maybeRewriteIdentityFile(ctx, identityPath, res, info.PrimaryMAC, macs, facts, logger); err != nil {
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
	serverID, encodedFile, err := generateServerID(macs, info.PrimaryMAC, facts, logger)
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

	// ONE read decides everything about the payload that is about to be replaced.
	// Reading it is also the only way to PRESERVE it, because preservation is a copy,
	// so a payload this run cannot read is a payload it cannot preserve, and replacing
	// it would destroy it.
	existing, readErr := readFileUnderRoot(identityDir, identityFileName)
	switch {
	case readErr == nil:
		// A run that cannot read machine-id computes poisoned prefixes on BOTH sides,
		// so it has no standing to call the stored file foreign. Keep it, keep its ID
		// on disk, and let the next healthy run decide.
		if !facts.writesAllowed() {
			logWarning(logger, "Identity: machine-id could not be read, so this run cannot judge %s; keeping the existing file (the new server ID will NOT be persisted)", identityPath)
			return info, nil
		}
		// NEVER DESTROY: preserve the previous payload byte for byte before the write,
		// and refuse to write at all when that preservation fails.
		if _, qErr := quarantineIdentityPayload(identityDir, existing, logger); qErr != nil {
			logWarning(logger, "Identity: refusing to replace %s because the existing payload could not be preserved: %v (server ID will NOT be persisted)", identityPath, qErr)
			return info, nil
		}
	case errors.Is(readErr, os.ErrNotExist):
		// First file on this host: there is nothing to preserve.
	default:
		// A directory, a symlink pointing outside the identity directory, an EIO on a
		// failing disk. Nothing downstream of this read runs, which also keeps the
		// immutable-attribute handling off a path this process cannot vouch for.
		logWarning(logger, "Identity: refusing to replace %s because the existing payload could not be read: %v; move it aside or delete it (server ID will NOT be persisted)", identityPath, readErr)
		return info, nil
	}

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
	value, _ := readFirstLineFunc(path, 16)
	raw := strings.TrimSpace(value)
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

// identityDecodeResult carries the outcome of ONE decode of ONE payload.
//
// Verified is false when the payload was accepted WITHOUT a real prefix match, which no
// rewrite may launder into a binding. KeyField is the stored key field, handed onward so
// no later step has to re-read or re-parse the file and see a different one. Reason is an
// already formatted warning, empty when the load was verified.
type identityDecodeResult struct {
	ServerID string
	Verified bool
	KeyField string
	Reason   string
}

// loadServerID reads the identity file and decodes it ONCE.
//
// There is no per candidate MAC loop: identityKeyPrefixesFor already computes the union
// of every candidate MAC's prefixes in one pass, which is exactly the set such a loop
// would explore one MAC at a time, so one decode replaces N and the payload is parsed,
// checksummed and format checked once instead of once per NIC.
func loadServerID(path string, macs []string, primaryMAC string, f hostFacts, logger *logging.Logger) (identityDecodeResult, error) {
	if stat, err := os.Stat(path); err == nil {
		logDebug(logger, "Identity: identity file stat: path=%s mode=%s size=%d mtime=%s", path, stat.Mode().String(), stat.Size(), stat.ModTime().Format(time.RFC3339))
	} else {
		logDebug(logger, "Identity: identity file stat failed: path=%s err=%v", path, err)
	}

	// Read confined to the identity directory via os.Root (structural gosec G304
	// fix, no #nosec); see readFileUnderRoot.
	data, err := readFileUnderRoot(filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return identityDecodeResult{}, err
	}
	logDebug(logger, "Identity: read identity file %s (%d bytes)", path, len(data))

	res, err := decodeProtectedServerID(string(data), macs, primaryMAC, f, logger)
	if err != nil {
		return identityDecodeResult{}, err
	}
	if res.Reason != "" {
		logWarning(logger, "%s", res.Reason)
	}
	return res, nil
}

func generateServerID(macs []string, primaryMAC string, f hostFacts, logger *logging.Logger) (string, string, error) {
	logDebug(logger, "Identity: generateServerID: starting (primaryMAC=%s macCount=%d)", primaryMAC, len(macs))
	systemData := buildSystemData(macs, f, logger)
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

	encoded := encodeProtectedServerIDWithKeyField(serverID, identityKeyFieldFor(f, macs, primaryMAC), logger)
	logDebug(logger, "Identity: generateServerID: encoded identity file bytes=%d", len(encoded))

	return serverID, encoded, nil
}

// buildSystemData is the hash SEED for a brand new server ID. It rejects nothing and
// binds nothing, so it takes the fact snapshot rather than re-reading machine-id and
// product_uuid, and an unreadable fact is no worse here than an absent one.
func buildSystemData(macs []string, f hostFacts, logger *logging.Logger) string {
	var builder strings.Builder
	timestamp := time.Now().UTC().Format("20060102150405")
	builder.WriteString(timestamp)
	logDebug(logger, "Identity: buildSystemData: timestamp=%s", timestamp)

	if f.MachineID != "" {
		builder.WriteString(f.MachineID)
		logDebug(logger, "Identity: buildSystemData: machine-id present len=%d", len(f.MachineID))
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

	if f.UUID != "" {
		builder.WriteString(f.UUID)
		logDebug(logger, "Identity: buildSystemData: product_uuid present len=%d", len(f.UUID))
	} else {
		logDebug(logger, "Identity: buildSystemData: product_uuid missing")
	}

	if version, _ := readFirstLineFunc(procVersionPath, maxProcVersionBytes); version != "" {
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

// encodeProtectedServerIDWithKeyField is the single place that turns a server ID plus an
// already decided key field into file content, so every caller produces the same format
// and the same checksum coverage.
func encodeProtectedServerIDWithKeyField(serverID, keyField string, logger *logging.Logger) string {
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
	return content
}

// decodeProtectedServerID parses a payload, verifies its checksum and server ID format
// exactly as before, then compares its stored prefixes against this host's set.
//
// On no match, exactly THREE acceptance branches run, in this order, and anything else is
// a rejection. Each one names a state in which this run cannot judge the payload at all;
// none of them is a guess about which machine wrote it.
func decodeProtectedServerID(fileContent string, macs []string, primaryMAC string, f hostFacts, logger *logging.Logger) (identityDecodeResult, error) {
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
		return identityDecodeResult{}, fmt.Errorf("identity data not found")
	}

	decodedBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		logDebug(logger, "Identity: decodeProtectedServerID: base64 decode failed: %v", err)
		return identityDecodeResult{}, fmt.Errorf("invalid encoded identity data: %w", err)
	}
	logDebug(logger, "Identity: decodeProtectedServerID: decoded payload bytes=%d", len(decodedBytes))

	parts := strings.Split(string(decodedBytes), ":")
	logDebug(logger, "Identity: decodeProtectedServerID: payload parts=%d", len(parts))
	if len(parts) != 4 {
		return identityDecodeResult{}, fmt.Errorf("invalid identity payload format")
	}

	serverID, timestamp, keyField, checksum := parts[0], parts[1], parts[2], parts[3]
	logDebug(logger, "Identity: decodeProtectedServerID: parsed serverID=%q ts=%q keyFieldLen=%d checksumPrefix=%q", serverID, timestamp, len(keyField), checksum)
	data := fmt.Sprintf("%s:%s:%s", serverID, timestamp, keyField)
	expectedChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(data)))[:systemKeyPrefixLength]
	if checksum != expectedChecksum {
		logDebug(logger, "Identity: decodeProtectedServerID: checksum mismatch (stored=%s expected=%s)", checksum, expectedChecksum)
		return identityDecodeResult{}, fmt.Errorf("identity checksum mismatch")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: checksum ok (%s)", expectedChecksum)

	currentPrefixes := identityKeyPrefixesFor(f, macs, primaryMAC)
	matched := false
	for _, prefix := range parseKeyFieldPrefixes(keyField) {
		if prefix != "" && currentPrefixes[prefix] {
			matched = true
			break
		}
	}

	verified := true
	reason := ""
	switch {
	case matched:
		// A real prefix match. Nothing to decide.
	case f.MachineIDState == systemFileUnreadable:
		// B1. machine-id is a factor of EVERY prefix on both sides, so a run that could
		// not read it computed nothing comparable and may judge no payload at all.
		verified = false
		reason = "Identity: machine-id could not be read, so the existing identity file cannot be checked against this host; keeping the stored server ID"
	case len(currentPrefixes) == 0:
		// B2. This run produced no arm of any kind (no machine-id, no product_uuid and
		// no MAC), so it has nothing to judge with. Refusing here would mint a new
		// server ID on every single run of an evidence-free host.
		verified = false
		reason = "Identity: this host reports no machine-id, no product_uuid and no MAC address, so the existing identity file cannot be checked against it; keeping the stored server ID"
	case f.UUIDState == systemFileUnreadable && uuidBlindnessExplains(keyField, f):
		// B3. product_uuid is one arm among several, so it excuses only the payloads
		// whose binding it could actually be.
		verified = false
		reason = "Identity: product_uuid could not be read, so the existing identity file cannot be checked against this host; keeping the stored server ID"
	default:
		logDebug(logger, "Identity: decodeProtectedServerID: no matching identity key prefix found")
		return identityDecodeResult{}, fmt.Errorf("identity file does not belong to this host")
	}
	if !verified {
		logDebug(logger, "Identity: decodeProtectedServerID: accepted without host verification (machine-id=%s product_uuid=%s currentPrefixes=%d)", f.MachineIDState, f.UUIDState, len(currentPrefixes))
	}

	// The format checks still run in EVERY accept branch: only the BINDING check is
	// skipped, so a corrupt payload is still refused.
	if len(serverID) != serverIDLength || !isAllDigits(serverID) {
		logDebug(logger, "Identity: decodeProtectedServerID: invalid server ID format (len=%d digits=%v)", len(serverID), isAllDigits(serverID))
		return identityDecodeResult{}, fmt.Errorf("invalid server ID format")
	}
	logDebug(logger, "Identity: decodeProtectedServerID: server ID format ok (len=%d)", len(serverID))
	return identityDecodeResult{ServerID: serverID, Verified: verified, KeyField: keyField, Reason: reason}, nil
}

// uuidBlindnessExplains reports whether an unreadable product_uuid can explain why none
// of a stored key field's prefixes match this host. Two rules, and nothing else.
//
// RULE ONE, A FACT EXCUSES ONLY THE PREFIXES IT IS AN INPUT TO. product_uuid is an input
// to the uuid arms and to nothing else, so only a payload carrying a uuid arm can be
// excused by not having read it. A payload of MAC arms alone was never keyed on it. A
// legacy v1 payload, whose single bare prefix is computeSystemKey(machineID,
// hostnamePart, macPart), was never keyed on it either: it is a mac-arm-only payload with
// the label stripped, so it gets no excuse here.
//
// RULE TWO, POSITIVE PROOF BEATS AN EXCUSE ABOUT A DIFFERENT FACT. If this run KNOWS its
// own machine-id situation and the payload carries a machineid arm that did not match,
// then the payload was written under a different machine-id, and blindness on product_uuid
// excuses nothing. Knowing it covers both "present and it differs" and "absent, so no
// machineid arm of ours could ever match": absent is a durable fact about this host, not
// blindness, which is the same distinction change 3 draws everywhere else. Only an
// UNREADABLE machine-id is blindness, and that case never reaches here because it is
// caught earlier by branch B1.
func uuidBlindnessExplains(keyField string, f hostFacts) bool {
	hasUUID := false
	hasMachineID := false
	for _, entry := range splitKeyFieldEntries(keyField) {
		switch entry.Label {
		case uuidKeyLabel, uuidNoHostKeyLabel:
			hasUUID = true
		case machineIDKeyLabel:
			hasMachineID = true
		}
	}
	if hasMachineID && f.MachineIDState != systemFileUnreadable {
		return false
	}
	return hasUUID
}

// identityKeyArm is one labelled key prefix.
type identityKeyArm struct {
	Label  string
	Prefix string
}

// identityKeyArms is the SINGLE place that decides what this host's identity key is.
// identityKeyFieldFor and identityKeyPrefixesFor are two projections of its result, so
// the write side and the read side cannot disagree, within a run or across runs.
//
// The arms are ADDITIVE: a host emits its MAC arms, plus the uuid pair when it can read
// product_uuid, plus the machine-id arm when it can read a machine-id. There is no
// exclusive switch between the last two.
//
// THE MACHINE-ID ARM IS GATED ON NOTHING BUT HAVING READ A MACHINE-ID, which is already
// a precondition of every other prefix. It exists so a host with no usable DMI survives
// an ordinary MAC change (pct set hwaddr, a restore, a recreate, an SDN reassignment) the
// way a DMI host survives one through its product_uuid arm. Two hosts CAN share an
// /etc/machine-id (a distribution image, systemd's "uninitialized" literal, a template),
// but a wrong adoption also needs one of them to physically hold the other's payload, and
// <baseDir>/identity/.server_identity lives on THE NODE'S OWN DISK. Every route by which
// one host comes to hold another's copy of it (a container clone, a pct restore, a
// template instantiation, an imaged disk) is exactly the class the maintainer has ruled
// correct and expected, so the shared value cannot be spent.
//
// THAT ARGUMENT RESTS ON ONE PREMISE: the identity directory is node local. If baseDir
// ever sits on shared storage, this arm removes the last discriminator between two nodes
// installed from the same image, where the uuid arm separates them today.
//
// The arm is hostname free, which is why it has no "_nohost" twin and why a hostname
// change cannot touch it.
func identityKeyArms(f hostFacts, macs []string, primaryMAC string) []identityKeyArm {
	primaryMAC = normalizeMAC(primaryMAC)

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

	arms := make([]identityKeyArm, 0, len(orderedMACs)*2+3)
	altIndex := 1
	for _, mac := range orderedMACs {
		macPart := strings.ReplaceAll(mac, ":", "")
		prefix := computeSystemKey(f.MachineID, f.HostnamePart, macPart)[:systemKeyPrefixLength]
		prefixNoHost := computeSystemKey(f.MachineID, "", macPart)[:systemKeyPrefixLength]

		if primaryMAC != "" && mac == primaryMAC {
			arms = append(arms,
				identityKeyArm{Label: "mac", Prefix: prefix},
				identityKeyArm{Label: "mac_nohost", Prefix: prefixNoHost},
			)
			continue
		}
		arms = append(arms,
			identityKeyArm{Label: fmt.Sprintf("mac_alt%d", altIndex), Prefix: prefix},
			identityKeyArm{Label: fmt.Sprintf("mac_alt%d_nohost", altIndex), Prefix: prefixNoHost},
		)
		altIndex++
	}

	if f.UUIDState == systemFilePresent {
		arms = append(arms,
			identityKeyArm{Label: uuidKeyLabel, Prefix: computeSystemKey(f.MachineID, f.HostnamePart, f.UUID)[:systemKeyPrefixLength]},
			identityKeyArm{Label: uuidNoHostKeyLabel, Prefix: computeSystemKey(f.MachineID, "", f.UUID)[:systemKeyPrefixLength]},
		)
	}
	if f.MachineIDState == systemFilePresent {
		arms = append(arms,
			identityKeyArm{Label: machineIDKeyLabel, Prefix: computeSystemKey(f.MachineID, "", f.MachineID)[:systemKeyPrefixLength]},
		)
	}

	seen := make(map[string]struct{}, len(arms))
	out := make([]identityKeyArm, 0, len(arms))
	for _, arm := range arms {
		if arm.Label == "" || arm.Prefix == "" {
			continue
		}
		token := arm.Label + "=" + arm.Prefix
		if _, ok := seen[token]; ok {
			continue
		}
		seen[token] = struct{}{}
		out = append(out, arm)
	}
	return out
}

// identityKeyFieldFor is the WRITE side projection of identityKeyArms.
func identityKeyFieldFor(f hostFacts, macs []string, primaryMAC string) string {
	arms := identityKeyArms(f, macs, primaryMAC)
	entries := make([]string, 0, len(arms))
	for _, arm := range arms {
		entries = append(entries, arm.Label+"="+arm.Prefix)
	}
	return strings.Join(entries, ",")
}

// identityKeyPrefixesFor is the READ side projection of identityKeyArms. It covers every
// candidate MAC in one pass, which is why the decoder needs no per MAC loop.
func identityKeyPrefixesFor(f hostFacts, macs []string, primaryMAC string) map[string]bool {
	arms := identityKeyArms(f, macs, primaryMAC)
	out := make(map[string]bool, len(arms))
	for _, arm := range arms {
		out[arm.Prefix] = true
	}
	return out
}

// splitKeyFieldEntries parses a key field into its entries, keeping the LABELS that
// parseKeyFieldPrefixes throws away. A v1 style bare prefix yields an entry with an empty
// Label, which every labelled rule then skips.
func splitKeyFieldEntries(keyField string) []identityKeyArm {
	keyField = strings.TrimSpace(keyField)
	if keyField == "" {
		return nil
	}
	tokens := strings.Split(keyField, ",")
	out := make([]identityKeyArm, 0, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		idx := strings.IndexByte(token, '=')
		if idx < 0 {
			out = append(out, identityKeyArm{Prefix: token})
			continue
		}
		out = append(out, identityKeyArm{
			Label:  strings.TrimSpace(token[:idx]),
			Prefix: strings.TrimSpace(token[idx+1:]),
		})
	}
	return out
}

// parseKeyFieldPrefixes yields the bare prefixes of a key field, on top of the ONE parser
// so the two cannot drift apart.
func parseKeyFieldPrefixes(keyField string) []string {
	entries := splitKeyFieldEntries(keyField)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Prefix)
	}
	return out
}

// keyFieldHasLabel reports whether a key field carries an entry with the given label.
func keyFieldHasLabel(keyField, label string) bool {
	for _, entry := range splitKeyFieldEntries(keyField) {
		if entry.Label == label {
			return true
		}
	}
	return false
}

// unionKeyFields returns the derived key field plus every LABELLED stored entry it does
// not already carry, deduped by the whole "label=prefix" token. The rewrite is therefore
// ADDITIVE UNCONDITIONALLY: a stored uuid arm survives a run that could not read
// product_uuid, and stored MAC arms of departed NICs survive too, harmlessly, because
// every prefix is keyed on the machine-id, so matching a stale MAC arm still requires the
// same machine-id.
//
// UNLABELLED v1 ENTRIES ARE DELIBERATELY NOT CARRIED. A rewrite only ever runs on a
// VERIFIED load, a verified load means some stored prefix is in identityKeyPrefixesFor,
// and that set and the derived key field are two projections of one arm list. So the bare
// entry that matched is already in the derived field under its proper mac= or mac_altN=
// label, and dropping it loses nothing.
func unionKeyFields(derived, stored string) string {
	seen := make(map[string]bool, 8)
	out := make([]string, 0, 8)
	appendEntries := func(keyField string) {
		for _, entry := range splitKeyFieldEntries(keyField) {
			if entry.Label == "" || entry.Prefix == "" {
				continue
			}
			token := entry.Label + "=" + entry.Prefix
			if seen[token] {
				continue
			}
			seen[token] = true
			out = append(out, token)
		}
	}
	appendEntries(derived)
	appendEntries(stored)
	return strings.Join(out, ",")
}

// maybeRewriteIdentityFile is the ONE in place rewrite of a payload that was just loaded.
// It carries the stored server ID through unchanged, so it can never change the machine's
// identity, and it takes the stored key field from the decode result rather than re-reading
// the file, so the load and the rewrite cannot see two different files.
//
// Two triggers share it: the v1 to v2 format upgrade, and adding the machine-id arm to a
// payload written before that arm existed. Both are falsified by the labels the rewrite
// itself writes, so a payload is rewritten at most once.
func maybeRewriteIdentityFile(ctx context.Context, path string, res identityDecodeResult, primaryMAC string, macs []string, f hostFacts, logger *logging.Logger) error {
	if ctx == nil {
		ctx = context.Background()
	}
	needsLabels := res.KeyField != "" && !strings.Contains(res.KeyField, "=")
	needsArm := f.MachineIDState == systemFilePresent && !keyFieldHasLabel(res.KeyField, machineIDKeyLabel)
	if !needsLabels && !needsArm {
		return nil
	}
	// An acceptance this run could not verify must never be laundered into a rewrite
	// that rebinds a possibly foreign payload to this host, and a blind run must not
	// write at all. An empty stored key field can only have been accepted by branch B2,
	// hence unverified, hence it never reaches the rewrite.
	if !res.Verified || !f.writesAllowed() {
		logDebug(logger, "Identity: skipping identity file rewrite for %s (verified=%v machineIDState=%s)", path, res.Verified, f.MachineIDState)
		return nil
	}

	keyField := unionKeyFields(identityKeyFieldFor(f, macs, primaryMAC), res.KeyField)
	logDebug(logger, "Identity: rewriting %s (needsLabels=%v needsArm=%v keyFieldLen=%d)", path, needsLabels, needsArm, len(keyField))
	updated := encodeProtectedServerIDWithKeyField(res.ServerID, keyField, logger)
	if err := writeIdentityFileWithContext(ctx, path, updated, logger); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// No landed/not-landed flag is needed here: the only steps after the rename are
		// the trailing chmod and a relock deliberately invoked with context.Background(),
		// so no context error can be returned once the rename has landed. Cancellation
		// therefore always means nothing changed, and every other failure means the
		// stored server ID is intact either way, so warning and carrying on is right in
		// both halves rather than making Detect discard a good Info.
		logWarning(logger, "Identity: could not rewrite the identity key of %s: %v; the stored server ID is unchanged", path, err)
		return nil
	}
	return nil
}

// quarantineIdentityPayload preserves payload under a ".rejected-" sibling before a
// replacement is written over the canonical path. It returns the name it used, and an
// error means the payload could NOT be preserved, which makes the caller refuse to write.
//
// IT IS A COPY, never a rename and never a hard link. A copy never empties the canonical
// path, so no window exists in which the machine has no identity file, and it never
// touches the immutable attribute: reading a +i file is always permitted, whereas hard
// linking one returns EPERM. The copy target is a fresh inode in a directory that is not
// itself immutable, so no chattr is involved anywhere in the quarantine.
//
// THE NAME IS CONTENT ADDRESSED, so preserving the same payload repeatedly is idempotent:
// a run that fails its write over and over leaves exactly one preserved copy. That is why
// there is no discard step and no pruner, and why accumulation is bounded by the number
// of DISTINCT payloads this host has ever rejected. A name that already exists holding
// DIFFERENT bytes is an error rather than an overwrite: a preserved payload is never
// destroyed.
//
// The preserved file is 0600 and deliberately not immutable, so a human can read it and
// delete it.
func quarantineIdentityPayload(dir string, payload []byte, logger *logging.Logger) (string, error) {
	sum := sha256.Sum256(payload)
	name := identityFileName + identityRejectedSuffix + fmt.Sprintf("%x", sum)[:quarantineNameHexLen]
	target := filepath.Join(dir, name)

	existing, err := readFileUnderRoot(dir, name)
	switch {
	case err == nil && bytes.Equal(existing, payload):
		logDebug(logger, "Identity: quarantine: %s already holds this payload", target)
		logWarning(logger, "Identity: the existing identity file did not match this host; preserved it as %s before writing a new server ID", target)
		return name, nil
	case err == nil:
		return "", fmt.Errorf("%s already holds a different preserved payload", target)
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}

	if err := atomicWriteIdentityFile(target, payload, 0o600); err != nil {
		return "", err
	}
	logWarning(logger, "Identity: the existing identity file did not match this host; preserved it as %s before writing a new server ID", target)
	return name, nil
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

// writeIdentityFileWithContext writes content over path, clearing and restoring the
// immutable attribute around the write.
//
// THE RELOCK DEFER IS ARMED BEFORE THE INITIAL CLEAR. It used to be armed after it, so a
// clear that failed or was cancelled mid chattr returned before arming anything and left
// the canonical identity file permanently mutable: the next healthy run loads the file,
// rewrites nothing and never relocks it. The immutable bit is what stops a stray rm or a
// packaging script from deleting a server ID that cannot be recomputed.
func writeIdentityFileWithContext(ctx context.Context, path, content string, logger *logging.Logger) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	logDebug(logger, "Identity: writeIdentityFile: start path=%s contentBytes=%d", path, len(content))

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

	// Ensure file is writable even if immutable was previously set
	if err := writeIdentityFileWithContextSetImmutable(ctx, path, false, logger); err != nil {
		return err
	}

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

// systemFileState classifies WHY a host fact file produced no value. An absent file is a
// fact about the host; a file that exists but cannot be read is blindness on this run.
// Collapsing the two (the old behaviour, a bare "") made one transient read failure
// reject a perfectly good identity file and mint a replacement, which the next healthy
// run rejected in turn: one blip cost two identity changes.
type systemFileState int

const (
	// systemFilePresent means a non-empty value was read. It always implies a
	// non-empty value, which every arm gate relies on.
	systemFilePresent systemFileState = iota
	// systemFileAbsent means the file does not exist, or exists and is empty. An empty
	// /etc/machine-id is the documented "not yet provisioned" state, a fact about the
	// host rather than a failure.
	systemFileAbsent
	// systemFileDenied means the read failed with a permission error. It is a DURABLE
	// fact about what this process can see, not a blip, and each reader prices it for
	// what its own fact is worth. It never escapes readMachineID or readProductUUID.
	systemFileDenied
	// systemFileUnreadable means the file exists but could not be read for any other
	// reason (EIO, EISDIR, ELOOP, descriptor exhaustion). This is the conservative side.
	systemFileUnreadable
)

func (s systemFileState) String() string {
	switch s {
	case systemFilePresent:
		return "present"
	case systemFileAbsent:
		return "absent"
	case systemFileDenied:
		return "denied"
	case systemFileUnreadable:
		return "unreadable"
	default:
		return "invalid"
	}
}

// classifyReadError is the whole of the classification, in one place so no caller can
// invent a fourth rule.
func classifyReadError(err error) systemFileState {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return systemFileAbsent
	case errors.Is(err, fs.ErrPermission):
		return systemFileDenied
	default:
		return systemFileUnreadable
	}
}

func readFirstLine(path string, limit int) (string, systemFileState) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", classifyReadError(err)
	}
	line := strings.TrimSpace(string(data))
	if limit > 0 && len(line) > limit {
		line = line[:limit]
	}
	if line == "" {
		return "", systemFileAbsent
	}
	return line, systemFilePresent
}

// readMachineID returns the host machine-id and how it was classified.
//
// A PRIMARY WE COULD NOT READ IS NEVER SILENTLY REPLACED BY A DIFFERENT IDENTIFIER. The
// dbus copy is consulted ONLY when /etc/machine-id is genuinely ABSENT. The machine-id is
// a factor of every key prefix on both sides, so substituting a different value for one
// we could not read rekeys the entire payload and costs two on-disk identity changes for
// one transient failure. The two files really can hold different values: an /etc/machine-id
// regenerated after a clone while the dbus copy is not, or an old container template that
// ships a real file rather than the symlink.
//
// A PERMISSION DENIAL ON THE MACHINE-ID IS STILL BLINDNESS, NOT A HOST FACT. Classifying
// it absent would make the run compute prefixes keyed on the empty string, reject a good
// payload and mint a replacement, which is the exact defect this classification exists to
// remove.
func readMachineID(logger *logging.Logger) (string, systemFileState) {
	value, state := readFirstLineFunc(machineIDPath, maxMachineIDBytes)
	source := machineIDPath
	if state == systemFileAbsent {
		value, state = readFirstLineFunc(dbusMachineIDPath, maxMachineIDBytes)
		source = dbusMachineIDPath
	}
	if state == systemFileDenied {
		value, state = "", systemFileUnreadable
	}
	if state == systemFilePresent {
		logDebug(logger, "Identity: machine-id source=%s len=%d", source, len(value))
	} else {
		logDebug(logger, "Identity: machine-id unavailable (state=%s)", state)
	}
	return value, state
}

// readProductUUID returns the DMI product_uuid and how it was classified.
//
// A PERMISSION DENIAL IS MAPPED TO ABSENT, which is what replaces the /proc/self/uid_map
// discriminator the earlier rounds carried. A denial means the DMI table is not visible
// to this process and will not be next run either: measured on an unprivileged Proxmox
// container the file exists, is mode 0400 and is owned by an unmapped host uid, so every
// uid inside gets EACCES; a masked /sys giving ENOENT lands in the same state. Treating
// that as blindness would keep the acceptance excuse open forever inside a container,
// which is exactly how a foreign payload carrying a uuid arm was adopted there.
//
// A non-root run on bare metal lands here too, correctly: it then has no uuid arm and its
// machine-id arm carries it, and in any case it cannot open the 0600 identity file at all.
func readProductUUID(logger *logging.Logger) (string, systemFileState) {
	uuid, state := readFirstLineFunc(productUUIDPath, maxMachineIDBytes)
	if state == systemFileDenied {
		uuid, state = "", systemFileAbsent
	}
	logDebug(logger, "Identity: product_uuid state=%s len=%d", state, len(uuid))
	return uuid, state
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

// hostFacts is ONE snapshot of everything the identity key depends on, read once per run
// and threaded into every decision, so the write side and the read side provably see
// identical readings even if the underlying files change mid run.
type hostFacts struct {
	MachineID      string
	MachineIDState systemFileState
	UUID           string
	UUIDState      systemFileState
	HostnamePart   string
}

func readHostFacts(logger *logging.Logger) hostFacts {
	machineID, machineIDState := readMachineID(logger)
	uuid, uuidState := readProductUUID(logger)
	f := hostFacts{
		MachineID:      machineID,
		MachineIDState: machineIDState,
		UUID:           uuid,
		UUIDState:      uuidState,
		HostnamePart:   readHostnamePart(logger),
	}
	logDebug(logger, "Identity: host facts: machine-id=%s product_uuid=%s", f.MachineIDState, f.UUIDState)
	return f
}

// writesAllowed reports whether this run may MUTATE an existing identity payload.
// machine-id is a factor of every key prefix on both sides, so a run that cannot read it
// computes poisoned prefixes and can judge nothing: it must not replace and must not
// rewrite. It MAY still create a first file where none exists, because creating destroys
// nothing and the created file is accepted back by the next equally blind run through
// branch B1, which turns a per-run churning ID into a stable one.
func (f hostFacts) writesAllowed() bool {
	return f.MachineIDState != systemFileUnreadable
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
