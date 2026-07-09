package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
	"github.com/tis24dev/proxsave/internal/types"
	buildinfo "github.com/tis24dev/proxsave/internal/version"
)

const (
	githubRepo = "tis24dev/proxsave"

	maxUpgradeConfigJSONPreviewLength = 4000
)

// releaseSigningPubKeyPEM is the pinned ECDSA P-256 public key used to verify the
// signature of SHA256SUMS during an upgrade. The matching private key lives only
// in the project's GitHub Actions secret (PROXSAVE_KEY_SIGNATURE).
// Fingerprint (sha256 of DER): fdbbba66cdb770b85a728c8aee0b920b4cd244c84f4fc5a0065188fbe9a5eddb
const releaseSigningPubKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAElks05mPtm1vm0YtHlSGX1HlgdXjn
liDJEnB+RgiWOQR+6xLWeX7PyauuMxUh/HNnvBQAokK91fLWes4r9Xlwzw==
-----END PUBLIC KEY-----
`

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
}

// upgradeRestartsDaemon gates whether runUpgrade performs the automatic post-upgrade
// daemon restart+verify itself (folding the outcome into the footer). The dashboard
// upgrade wrapper sets it false for the duration of its run so IT drives the restart
// inside a spinner and renders a notice instead -- runUpgrade's stdout (and thus the
// footer) is muted in the dashboard, so its inline restart would be invisible and would
// double-restart. Default true = the CLI --upgrade path restarts inline.
var upgradeRestartsDaemon = true

// upgradeHeartbeatInterval loads the heartbeat interval from the config, best-effort
// (0 when unreadable). The interval only shapes the daemon-state Diagnosis shown for
// display; the restart-verify SUCCESS gate (process-alive + aligned + fresh) does not
// depend on it, so a 0 fallback never breaks verification.
func upgradeHeartbeatInterval(configPath, baseDir string) time.Duration {
	if cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir); err == nil && cfg != nil {
		return cfg.HealthcheckHeartbeatInterval
	}
	return 0
}

// upgradeBackupLockPath resolves the REAL backup lock file path (honouring a custom
// LOCK_PATH), best-effort, mirroring upgradeHeartbeatInterval's config load. The
// daemon-restart backup-wait probe MUST inspect this exact path -- the same
// <cfg.LockPath>/.backup.lock the orchestrator's Checker acquires -- or a restart on a
// custom-LOCK_PATH host would find no lock and could kill an in-progress backup. An
// unreadable config falls back to the base-dir default lock path.
func upgradeBackupLockPath(configPath, baseDir string) string {
	cfg, err := config.LoadConfigWithBaseDir(configPath, baseDir)
	if err != nil {
		cfg = nil
	}
	return backupLockFilePath(cfg, baseDir)
}

// logUpgradeDaemonRestart mirrors the restart outcome to the bootstrap log (the CLI
// path). It never fails the upgrade -- every outcome is informational.
func logUpgradeDaemonRestart(bootstrap *logging.BootstrapLogger, rv *RestartVerifyResult) {
	switch {
	case rv == nil:
		return
	case rv.Err != nil:
		bootstrap.Warning("Daemon restart failed: %v (it may still run the old binary; restart it manually).", rv.Err)
	case rv.BackupWaitTimedOut:
		bootstrap.Warning("A backup is running; daemon restart deferred. Restart when idle or the daemon stays on the old binary.")
	case rv.TimedOut:
		bootstrap.Warning("Daemon restarted but alignment check timeout")
	case rv.Restarted && rv.ProcessAlive && rv.Aligned && rv.FreshInfo:
		bootstrap.Println("Daemon restarted and now aligned with the new binary.")
	default:
		bootstrap.Warning("Daemon restarted but alignment could not be confirmed")
	}
}

// runUpgrade orchestrates the upgrade flow:
//   - downloads and installs the latest binary release
//   - upgrades backup.env by adding missing keys from the new template (preserving existing values)
//   - refreshes symlinks/cron/docs and normalizes permissions/ownership
func runUpgrade(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) int {
	baseDir, _ := detectedBaseDirOrFallback()
	_ = os.Setenv("BASE_DIR", baseDir)

	logLevel := types.LogLevelInfo
	if args.LogLevel != types.LogLevelNone {
		logLevel = args.LogLevel
	}
	if bootstrap != nil {
		bootstrap.SetLevel(logLevel)
	}

	sessionLogger, logPath, closeLog, logErr := logging.StartSessionLogger("upgrade", logLevel, false)
	if logErr == nil {
		if bootstrap != nil {
			bootstrap.Info("UPGRADE log: %s", logPath)
			bootstrap.SetMirrorLogger(sessionLogger)
		}
		sessionLogger.SetOutput(io.Discard)
		defer closeLog()
	}

	var workflowErr error
	done := logging.DebugStartBootstrap(bootstrap, "upgrade workflow", "config=%s base=%s", args.ConfigPath, baseDir)
	defer func() { done(workflowErr) }()

	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("ERROR: %v", err)
		workflowErr = err
		return types.ExitConfigError.Int()
	}

	// Print version/banner header for upgrade mode
	currentVersion := buildinfo.String()
	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave")
	bootstrap.Printf("  Version: %s", currentVersion)
	if sig := buildSignature(); strings.TrimSpace(sig) != "" {
		bootstrap.Printf("  Build Signature: %s", sig)
	}
	bootstrap.Println("  Mode: Upgrade")
	bootstrap.Println("===========================================")
	bootstrap.Printf("Configuration file: %s", args.ConfigPath)
	bootstrap.Printf("Base directory: %s", baseDir)
	bootstrap.Println("")

	_, err := config.LoadConfigWithBaseDir(args.ConfigPath, baseDir)
	if err != nil {
		bootstrap.Error("ERROR: Failed to load configuration: %v", err)
		workflowErr = err
		return types.ExitConfigError.Int()
	}

	// Discover the latest available release on GitHub and compare with the
	// currently installed version before proceeding.
	logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "fetching latest release info")
	tag, latestVersion, _, err := fetchLatestRelease(ctx)
	if err != nil {
		bootstrap.Error("ERROR: Failed to check latest release: %v", err)
		workflowErr = err
		return types.ExitConfigError.Int()
	}

	logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "current=%s latest=%s", currentVersion, latestVersion)
	switch compareVersions(currentVersion, latestVersion) {
	case 0:
		bootstrap.Printf("You are already running the latest version: %s", currentVersion)
		workflowErr = nil
		return types.ExitSuccess.Int()
	case 1:
		bootstrap.Printf("Installed version (%s) is newer than latest release (%s); aborting upgrade.", currentVersion, latestVersion)
		workflowErr = fmt.Errorf("current version newer than latest")
		return types.ExitConfigError.Int()
	}

	bootstrap.Printf("Latest available version: %s (current: %s)", latestVersion, currentVersion)

	confirm := args.UpgradeAutoYes
	if confirm {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "auto-confirm enabled (--upgrade y)")
	} else {
		reader := bufio.NewReader(os.Stdin)
		var err error
		confirm, err = promptYesNo(ctx, reader, "Do you want to download and install this version now? (backup.env will be updated with any missing keys; a backup will be created) [y/N]: ", false)
		if err != nil {
			bootstrap.Error("ERROR: %v", err)
			workflowErr = err
			return types.ExitConfigError.Int()
		}
	}
	if !confirm {
		bootstrap.Println("Upgrade cancelled by user; no changes were made.")
		workflowErr = nil
		return types.ExitSuccess.Int()
	}

	// Download + install latest binary (confirmed)
	execInfo := getExecInfo()
	execPath := execInfo.ExecPath
	logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "executing upgrade for %s", execPath)
	versionInstalled, upgradeErr := downloadAndInstallLatest(ctx, execPath, bootstrap, tag, latestVersion)
	if upgradeErr != nil {
		bootstrap.Error("ERROR: Upgrade failed: %v", upgradeErr)
		// Continue to footer to show guidance and permission status, but exit with error.
		workflowErr = upgradeErr
	}

	var cfgUpgradeResult *config.UpgradeResult
	var cfgUpgradeErr error
	if upgradeErr == nil {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "upgrading configuration with newly installed binary")
		cfgUpgradeResult, cfgUpgradeErr = upgradeConfigWithBinary(ctx, execPath, args.ConfigPath)
		if cfgUpgradeErr != nil {
			bootstrap.Warning("Upgrade: configuration upgrade failed: %v", cfgUpgradeErr)
		}
	}
	if sessionLogger != nil && cfgUpgradeResult != nil && len(cfgUpgradeResult.MissingKeys) > 0 {
		sessionLogger.Info("Upgrade: configuration updated with %d missing key(s): %s", len(cfgUpgradeResult.MissingKeys), strings.Join(cfgUpgradeResult.MissingKeys, ", "))
	}

	// Refresh docs/symlinks/cron/identity (configuration upgrade is handled separately)
	logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "refreshing docs and symlinks")
	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		bootstrap.Warning("Upgrade: failed to refresh documentation: %v", err)
	}
	ensureGoSymlink(execPath, bootstrap)

	// Auto-migrate cron installs to the resident daemon now that the new binary +
	// config keys are in place. Honours the DAEMON_OPT_OUT tombstone so a manual
	// --daemon-remove is never undone. Best-effort: a failure stays on cron and is
	// only warned. (When staying on cron, the canonical /usr/local/bin/proxsave
	// entry created at install keeps working across binary upgrades.)
	var daemonRestart *RestartVerifyResult
	if upgradeErr == nil {
		maybeAutoMigrateDaemon(ctx, args.ConfigPath, baseDir, execPath, bootstrap)
		// The new binary is on disk, but the resident daemon still runs the OLD one
		// (systemd keeps the process alive across an in-place replace). Restart+verify
		// it so the upgrade ends with the daemon aligned. This is automatic (no extra
		// prompt) and NEVER changes the upgrade exit code -- the upgrade already
		// succeeded. It first waits out any in-progress backup rather than killing it,
		// and only runs when the daemon is actually active (a cron install has none).
		if upgradeRestartsDaemon && daemonIsActive(ctx) {
			bootstrap.Println("Restarting the resident daemon to load the new binary...")
			rv := restartAndVerifyDaemon(ctx, baseDir, upgradeBackupLockPath(args.ConfigPath, baseDir), upgradeHeartbeatInterval(args.ConfigPath, baseDir))
			daemonRestart = &rv
			logUpgradeDaemonRestart(bootstrap, &rv)
		}
	} else {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "binary install failed; leaving scheduler unchanged")
	}

	telegramCode := ""
	if info, err := identity.DetectWithContext(ctx, baseDir, nil); err == nil {
		if code := strings.TrimSpace(info.ServerID); code != "" {
			telegramCode = code
		}
	}
	if telegramCode != "" {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "telegram identity detected")
	} else {
		logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "telegram identity not found")
	}

	logging.DebugStepBootstrap(bootstrap, "upgrade workflow", "normalizing permissions")
	permStatus, permMessage := fixPermissionsAfterInstall(ctx, args.ConfigPath, baseDir, bootstrap, nil)

	printUpgradeFooter(upgradeErr, versionInstalled, args.ConfigPath, baseDir, telegramCode, permStatus, permMessage, cfgUpgradeResult, cfgUpgradeErr, daemonRestart)

	// A configuration-upgrade failure after a successful binary install must also be
	// reflected in the exit code (the footer already shows "Configuration: ERROR"),
	// so automation does not treat the run as fully successful.
	if upgradeErr == nil && cfgUpgradeErr != nil {
		workflowErr = cfgUpgradeErr
	}
	return upgradeExitCode(upgradeErr, cfgUpgradeErr)
}

// upgradeExitCode maps the binary-install and config-upgrade outcomes to a process
// exit code: any failure on either yields a non-zero exit.
func upgradeExitCode(upgradeErr, cfgUpgradeErr error) int {
	if upgradeErr != nil || cfgUpgradeErr != nil {
		return types.ExitGenericError.Int()
	}
	return types.ExitSuccess.Int()
}

// downloadAndInstallLatest downloads the specified release archive from GitHub,
// verifies the checksum, extracts the proxsave binary, and installs it to execPath.
func downloadAndInstallLatest(ctx context.Context, execPath string, bootstrap *logging.BootstrapLogger, tag, version string) (versionInstalled string, err error) {
	done := logging.DebugStartBootstrap(bootstrap, "upgrade download/install", "tag=%s version=%s", tag, version)
	// Named return so the deferred trace reflects the ACTUAL returned error. The
	// per-step `if err := <call>; err != nil { return "", ... }` blocks shadow a
	// local err, but a `return` assigns this named err on the way out, so done(err)
	// no longer logs the span as "ok" when a download/verify/install step failed.
	defer func() { done(err) }()

	osName, arch, err := detectOSArch()
	if err != nil {
		return "", err
	}
	logging.DebugStepBootstrap(bootstrap, "upgrade download/install", "platform=%s/%s", osName, arch)

	filename := fmt.Sprintf("proxsave_%s_%s_%s.tar.gz", version, osName, arch)
	archiveURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, tag, filename)
	checksumURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS", githubRepo, tag)
	signatureURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS.sig", githubRepo, tag)

	bootstrap.Info("Downloading latest release: %s (%s/%s)", tag, osName, arch)

	tmpDir, err := os.MkdirTemp("", "proxsave-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp dir: %w", err)
	}
	defer func() {
		if removeErr := os.RemoveAll(tmpDir); removeErr != nil {
			bootstrap.Debug("Failed to remove temporary upgrade directory %s: %v", tmpDir, removeErr)
		}
	}()
	logging.DebugStepBootstrap(bootstrap, "upgrade download/install", "temp dir=%s", tmpDir)

	// All downloads land in the process-private temp dir; operate on it through an
	// *os.Root so every read/write is confined there at the syscall level.
	tmpRoot, err := os.OpenRoot(tmpDir)
	if err != nil {
		return "", fmt.Errorf("cannot open temp dir: %w", err)
	}
	defer func() { _ = tmpRoot.Close() }()

	const checksumName, signatureName, binaryName = "SHA256SUMS", "SHA256SUMS.sig", "proxsave"

	if err := downloadFile(ctx, archiveURL, tmpRoot, filename, bootstrap); err != nil {
		return "", fmt.Errorf("failed to download archive: %w", err)
	}
	if err := downloadFile(ctx, checksumURL, tmpRoot, checksumName, bootstrap); err != nil {
		return "", fmt.Errorf("failed to download checksum file: %w", err)
	}
	if err := downloadFile(ctx, signatureURL, tmpRoot, signatureName, bootstrap); err != nil {
		return "", fmt.Errorf("failed to download signature file (release may be unsigned): %w", err)
	}

	// Authenticity first: SHA256SUMS must be signed by the project's release key;
	// the checksum then ties the archive to that authenticated file.
	if err := verifyReleaseSignature(tmpRoot, checksumName, signatureName, releaseSigningPubKeyPEM); err != nil {
		return "", err
	}
	bootstrap.Info("Upgrade: SHA256SUMS signature verified")

	if err := verifyChecksum(tmpRoot, checksumName, filename, bootstrap); err != nil {
		return "", err
	}

	if err := extractBinaryFromTar(tmpRoot, filename, binaryName, binaryName, bootstrap); err != nil {
		return "", err
	}

	if err := installBinary(tmpRoot, binaryName, execPath, bootstrap); err != nil {
		return "", err
	}

	bootstrap.Info("Upgrade: installed proxsave %s to %s", version, execPath)
	return version, nil
}

func detectOSArch() (string, string, error) {
	return resolveReleaseTarget(runtime.GOOS, runtime.GOARCH)
}

// resolveReleaseTarget maps the running platform to the OS/arch of a published
// release archive. Releases are built for linux/amd64 ONLY (see
// .github/.goreleaser.yml), so any other architecture is rejected up front:
// advertising it would build a download URL for an archive that does not exist and
// fail later with a confusing 404.
func resolveReleaseTarget(goos, goarch string) (string, string, error) {
	osName := strings.ToLower(goos)
	if osName != "linux" {
		return "", "", fmt.Errorf("unsupported OS: %s (only linux is supported)", osName)
	}
	if goarch != "amd64" {
		return "", "", fmt.Errorf("no prebuilt release for architecture %s: only linux/amd64 binaries are published; build from source to upgrade on this host", goarch)
	}
	return osName, "amd64", nil
}

// fetchLatestRelease returns (tag, version, body, err) for the latest GitHub release.
// body is the raw release description (used to surface the release notes); it is remote-
// controlled, so every consumer that displays it MUST sanitize it first.
func fetchLatestRelease(ctx context.Context) (string, string, string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", "", "", fmt.Errorf("failed to fetch latest release: status %d, body: %s", resp.StatusCode, string(body))
	}

	var info releaseInfo
	// Bound the JSON read: a release body (notes/changelog) is small, but the response
	// is remote-controlled, so cap it defensively.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 512*1024)).Decode(&info); err != nil {
		return "", "", "", fmt.Errorf("failed to parse release response: %w", err)
	}

	tag := strings.TrimSpace(info.TagName)
	if tag == "" {
		return "", "", "", errors.New("empty tag_name in latest release response")
	}

	version := strings.TrimPrefix(tag, "v")
	return tag, version, info.Body, nil
}

// compareVersions compares two semantic version strings (e.g. "0.11.2") and
// returns -1 if current < latest, 0 if equal, 1 if current > latest. Numeric core
// segments are compared first; when they are equal, a pre-release identifier (e.g.
// "-rc1") ranks BELOW the same-numeric stable release (matching isNewerVersion).
// Build metadata ("+...") is ignored.
func compareVersions(current, latest string) int {
	normalize := func(v string) ([]int, bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			return []int{0}, false
		}
		// A "-" suffix marks a semver pre-release (e.g. "-rc1"); "+" marks build
		// metadata. Record whether this is a pre-release and strip the suffix so
		// the numeric core can be compared; the flag breaks numeric ties below.
		prerelease := false
		if idx := strings.IndexAny(v, "-+"); idx >= 0 {
			prerelease = v[idx] == '-'
			v = v[:idx]
		}
		parts := strings.Split(v, ".")
		out := make([]int, 0, len(parts))
		for _, p := range parts {
			if p == "" {
				out = append(out, 0)
				continue
			}
			n, err := strconv.Atoi(p)
			if err != nil {
				out = append(out, 0)
			} else {
				out = append(out, n)
			}
		}
		if len(out) == 0 {
			return []int{0}, prerelease
		}
		return out, prerelease
	}

	a, aPre := normalize(current)
	b, bPre := normalize(latest)

	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}

	for i := 0; i < maxLen; i++ {
		var av, bv int
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av < bv {
			return -1
		}
		if av > bv {
			return 1
		}
	}

	// Numeric cores are equal: a stable release outranks the same-numeric
	// pre-release, matching isNewerVersion (the update check) so the upgrade gate
	// and the update nag agree on the rc -> stable transition instead of leaving
	// rc users stranded ("already running the latest version").
	switch {
	case aPre && !bPre:
		return -1
	case !aPre && bPre:
		return 1
	default:
		return 0
	}
}

func downloadFile(ctx context.Context, url string, root *os.Root, name string, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "upgrade download", "url=%s dest=%s", url, name)
	defer func() { done(err) }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("cannot create request: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	logging.DebugStepBootstrap(bootstrap, "upgrade download", "status=%s", resp.Status)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024))
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	out, err := root.Create(name)
	if err != nil {
		return fmt.Errorf("cannot create file %s: %w", name, err)
	}
	defer closeIntoErr(&err, out, "close downloaded file")

	written, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("cannot write file %s: %w", name, err)
	}
	logging.DebugStepBootstrap(bootstrap, "upgrade download", "bytes=%d", written)
	return nil
}

func verifyChecksum(root *os.Root, checksumName, filename string, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "upgrade checksum", "file=%s", filename)
	defer func() { done(err) }()
	checksums, err := root.ReadFile(checksumName)
	if err != nil {
		return fmt.Errorf("cannot read checksum file: %w", err)
	}

	expected := ""
	lines := bytes.Split(checksums, []byte("\n"))
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		parts := bytes.Fields(line)
		if len(parts) < 2 {
			continue
		}
		// Require an exact filename match (normalizing only sha256sum's optional
		// binary-mode '*' marker); a mere suffix match would wrongly accept an
		// overlapping entry such as "artifacts/<filename>".
		name := strings.TrimPrefix(string(parts[len(parts)-1]), "*")
		if name == filename {
			expected = string(parts[0])
			break
		}
	}

	if expected == "" {
		return fmt.Errorf("checksum entry not found for %s", filename)
	}

	f, err := root.Open(filename)
	if err != nil {
		return fmt.Errorf("cannot open archive for checksum: %w", err)
	}
	defer closeIntoErr(&err, f, "close archive for checksum")

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("cannot compute checksum: %w", err)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	logging.DebugStepBootstrap(bootstrap, "upgrade checksum", "expected=%s", expected)
	logging.DebugStepBootstrap(bootstrap, "upgrade checksum", "computed=%s", sum)

	if !strings.EqualFold(sum, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filename, expected, sum)
	}
	return nil
}

// verifyReleaseSignature verifies that the SHA256SUMS file was signed with the
// project's release key. The signature is an ECDSA-P256/SHA-256 signature in
// ASN.1 DER form, as produced by `openssl dgst -sha256 -sign`.
func verifyReleaseSignature(root *os.Root, checksumName, signatureName, pubKeyPEM string) error {
	pub, err := parseECDSAPublicKey(pubKeyPEM)
	if err != nil {
		return err
	}
	data, err := root.ReadFile(checksumName)
	if err != nil {
		return fmt.Errorf("cannot read checksum file: %w", err)
	}
	sig, err := root.ReadFile(signatureName)
	if err != nil {
		return fmt.Errorf("cannot read signature file: %w", err)
	}
	digest := sha256.Sum256(data)
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return errors.New("SHA256SUMS signature verification failed; refusing to upgrade")
	}
	return nil
}

func parseECDSAPublicKey(pubKeyPEM string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(pubKeyPEM))
	if block == nil {
		return nil, errors.New("invalid public key PEM")
	}
	keyAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("cannot parse public key: %w", err)
	}
	pub, ok := keyAny.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("pinned public key is not ECDSA")
	}
	return pub, nil
}

func extractBinaryFromTar(root *os.Root, archiveName, targetName, destName string, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "upgrade extract", "archive=%s target=%s", archiveName, targetName)
	defer func() { done(err) }()
	f, err := root.Open(archiveName)
	if err != nil {
		return fmt.Errorf("cannot open archive: %w", err)
	}
	defer closeIntoErr(&err, f, "close release archive")

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("cannot create gzip reader: %w", err)
	}
	defer closeIntoErr(&err, gzr, "close release gzip reader")

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("cannot read tar entry: %w", err)
		}
		if hdr == nil {
			continue
		}
		if strings.TrimSpace(hdr.Name) != targetName {
			continue
		}

		logging.DebugStepBootstrap(bootstrap, "upgrade extract", "extracting to %s", destName)
		tmpFile, err := root.Create(destName)
		if err != nil {
			return fmt.Errorf("cannot create extracted binary: %w", err)
		}
		// Bound the copy to the entry's declared size: the release archive is
		// already signature- and checksum-verified, and io.CopyN keeps gosec G110
		// (decompression bomb) satisfied while rejecting a truncated entry.
		if _, err := io.CopyN(tmpFile, tr, hdr.Size); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("cannot write extracted binary: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("cannot close extracted binary: %w", err)
		}
		return nil
	}

	return fmt.Errorf("binary %s not found inside archive", targetName)
}

func installBinary(srcRoot *os.Root, srcName, destPath string, bootstrap *logging.BootstrapLogger) (err error) {
	done := logging.DebugStartBootstrap(bootstrap, "upgrade install", "src=%s dest=%s", srcName, destPath)
	defer func() { done(err) }()
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return fmt.Errorf("cannot create target directory: %w", err)
	}

	// Write the replacement binary through an *os.Root on the install directory so
	// the create/replace is confined there.
	destRoot, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("cannot open target directory: %w", err)
	}
	defer func() { _ = destRoot.Close() }()

	destName := filepath.Base(destPath)
	tmpName := destName + ".tmp"

	src, err := srcRoot.Open(srcName)
	if err != nil {
		return fmt.Errorf("cannot open extracted binary: %w", err)
	}
	defer closeIntoErr(&err, src, "close extracted binary")

	// Install the conventional executable mode 0755 (owner rwx, group/other r-x). The
	// binary runs as root and only root can replace it; the security check verifies it
	// is root-owned and not group/other-writable, not an exact mode, so 0755 is fine.
	dst, err := destRoot.OpenFile(tmpName, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("cannot create temp target binary: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("cannot copy binary to temp target: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("cannot close temp target binary: %w", err)
	}

	// Rename through the same *os.Root (paths relative to it) so the swap stays
	// confined to the directory fd opened above — no re-resolution of destDir as a
	// string, which would reopen a TOCTOU window on a mutable path.
	if err := destRoot.Rename(tmpName, destName); err != nil {
		return fmt.Errorf("cannot replace binary at %s: %w", destPath, err)
	}
	return nil
}

func closeIntoErr(errp *error, closer io.Closer, operation string) {
	if errp == nil || closer == nil {
		return
	}
	if closeErr := closer.Close(); closeErr != nil && *errp == nil {
		*errp = fmt.Errorf("%s: %w", operation, closeErr)
	}
}

func printUpgradeFooter(upgradeErr error, version, configPath, baseDir, telegramCode, permStatus, permMessage string, cfgUpgradeResult *config.UpgradeResult, cfgUpgradeErr error, daemonRestart *RestartVerifyResult) {
	colorReset := "\033[0m"

	title := "Upgrade completed"
	color := "\033[32m" // green

	if upgradeErr != nil {
		color = "\033[31m"
		title = "Upgrade failed"
	}

	fmt.Println()
	fmt.Printf("%s================================================\n", color)
	fmt.Printf(" %s\n", title)
	fmt.Printf("================================================%s\n", colorReset)
	fmt.Println()

	if strings.TrimSpace(version) != "" {
		fmt.Printf("Version installed: %s\n", version)
	}

	if permStatus != "" {
		switch permStatus {
		case "ok":
			fmt.Printf("Permissions: %s\n", permMessage)
		case "warning":
			fmt.Printf("Permissions: WARNING (non blocking) - %s\n", permMessage)
		case "error":
			fmt.Printf("Permissions: ERROR (non blocking) - %s\n", permMessage)
		case "skipped":
			fmt.Printf("Permissions: %s\n", permMessage)
		default:
			fmt.Printf("Permissions: %s\n", permMessage)
		}
		fmt.Println()
	}

	if cfgUpgradeErr != nil {
		fmt.Printf("Configuration: ERROR - failed to upgrade %s\n", configPath)
		fmt.Printf("  Details: %v\n", cfgUpgradeErr)
		fmt.Println("  Action: Review the configuration file and run: proxsave --upgrade-config")
		fmt.Println()
	} else if cfgUpgradeResult != nil {
		if cfgUpgradeResult.Changed {
			if len(cfgUpgradeResult.MissingKeys) > 0 {
				fmt.Printf("Configuration: updated (added %d missing key(s))\n", len(cfgUpgradeResult.MissingKeys))
				fmt.Printf("  Added keys: %s\n", strings.Join(cfgUpgradeResult.MissingKeys, ", "))
				fmt.Println("  Action: Review these keys in backup.env and adjust values as needed.")
			} else {
				fmt.Println("Configuration: updated (no new keys were required)")
				if len(cfgUpgradeResult.ExtraKeys) > 0 {
					fmt.Printf("  Preserved %d custom key(s) not present in the template.\n", len(cfgUpgradeResult.ExtraKeys))
				}
				if len(cfgUpgradeResult.CaseConflictKeys) > 0 {
					fmt.Printf("  Preserved %d key(s) that differ only by case from the template.\n", len(cfgUpgradeResult.CaseConflictKeys))
				}
			}
			if cfgUpgradeResult.BackupPath != "" {
				fmt.Printf("  Backup saved to: %s\n", cfgUpgradeResult.BackupPath)
			}
			fmt.Println()
		} else {
			fmt.Println("Configuration: already up to date with the latest template (no changes).")
			fmt.Println()
		}
	}

	if cfgUpgradeResult != nil && len(cfgUpgradeResult.Warnings) > 0 {
		fmt.Printf("Configuration warnings (%d):\n", len(cfgUpgradeResult.Warnings))
		for _, warning := range cfgUpgradeResult.Warnings {
			fmt.Printf("  - %s\n", warning)
		}
		fmt.Println()
	}

	if line, warn := summarizeRestartVerify(daemonRestart, version); line != "" {
		color := "\033[32m" // green (success)
		if warn {
			color = "\033[33m" // yellow (non-blocking warning)
		}
		fmt.Printf("%s%s%s\n", color, line, colorReset)
		fmt.Println()
	}

	fmt.Println("Next steps:")
	if strings.TrimSpace(configPath) != "" {
		fmt.Printf("1. Verify configuration: %s\n", configPath)
	} else {
		fmt.Println("1. Verify configuration")
	}
	if strings.TrimSpace(baseDir) != "" {
		fmt.Println("2. Run backup: proxsave")
		fmt.Printf("3. Logs: tail -f %s/log/*.log\n", baseDir)
	} else {
		fmt.Println("2. Run backup: proxsave")
		fmt.Println("3. Logs: tail -f /opt/proxsave/log/*.log")
	}
	if strings.TrimSpace(telegramCode) != "" {
		fmt.Printf("4. Telegram: Open @ProxmoxAN_bot and enter code: %s\n", telegramCode)
	}
	fmt.Println()

	fmt.Println("Commands:")
	fmt.Println("  proxsave (alias: proxmox-backup) - Open the interactive dashboard (runs the backup directly when non-interactive, e.g. cron)")
	fmt.Println("  --backup           - Run a backup now (what bare proxsave does when non-interactive)")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (also adds missing keys to backup.env)")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep build/env/identity) then run installer")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println()

	if upgradeErr != nil {
		fmt.Println("Upgrade reported an error; please review the log above.")
	}
}

func upgradeConfigWithBinary(ctx context.Context, execPath, configPath string) (*config.UpgradeResult, error) {
	execPath = strings.TrimSpace(execPath)
	configPath = strings.TrimSpace(configPath)
	if execPath == "" {
		return nil, fmt.Errorf("exec path is empty")
	}
	if configPath == "" {
		return nil, fmt.Errorf("configuration path is empty")
	}

	cmd, err := safeexec.TrustedCommandContext(ctx, execPath, "--config", configPath, "--upgrade-config-json")
	if err != nil {
		return nil, err
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		details := strings.TrimSpace(stderr.String())
		if details == "" {
			details = strings.TrimSpace(stdout.String())
		}
		if details != "" {
			return nil, fmt.Errorf("upgrade-config-json failed: %w: %s", err, details)
		}
		return nil, fmt.Errorf("upgrade-config-json failed: %w", err)
	}

	var result config.UpgradeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		preview := strings.TrimSpace(stdout.String())
		if len(preview) > maxUpgradeConfigJSONPreviewLength {
			preview = preview[:maxUpgradeConfigJSONPreviewLength] + "…"
		}
		return nil, fmt.Errorf("invalid JSON from upgrade-config-json: %w (stdout=%q)", err, preview)
	}
	return &result, nil
}
