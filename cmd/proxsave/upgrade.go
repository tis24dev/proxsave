package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/identity"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

const (
	githubRepo = "tis24dev/proxsave"
)

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

// runUpgrade orchestrates the upgrade flow:
//   - downloads and installs the latest binary release
//   - keeps the existing backup.env untouched
//   - refreshes symlinks/cron/docs and normalizes permissions/ownership
func runUpgrade(ctx context.Context, args *cli.Args, bootstrap *logging.BootstrapLogger) int {
	baseDir := filepath.Dir(filepath.Dir(args.ConfigPath))
	if baseDir == "" || baseDir == "." || baseDir == string(filepath.Separator) {
		baseDir = "/opt/proxsave"
	}
	_ = os.Setenv("BASE_DIR", baseDir)

	if err := ensureConfigExists(args.ConfigPath, bootstrap); err != nil {
		bootstrap.Error("ERROR: %v", err)
		return types.ExitConfigError.Int()
	}

	bootstrap.Println("===========================================")
	bootstrap.Println("  ProxSave - Upgrade")
	bootstrap.Printf("  Config: %s", args.ConfigPath)
	bootstrap.Printf("  Base:   %s", baseDir)
	bootstrap.Println("===========================================")

	cfg, err := config.LoadConfig(args.ConfigPath)
	if err != nil {
		bootstrap.Error("ERROR: Failed to load configuration: %v", err)
		return types.ExitConfigError.Int()
	}
	if strings.TrimSpace(cfg.BaseDir) == "" {
		cfg.BaseDir = baseDir
	}

	// Download + install latest binary
	execInfo := getExecInfo()
	execPath := execInfo.ExecPath
	versionInstalled, upgradeErr := downloadAndInstallLatest(ctx, execPath, bootstrap)
	if upgradeErr != nil {
		bootstrap.Error("ERROR: Upgrade failed: %v", upgradeErr)
		// Continue to footer to show guidance and permission status, but exit with error.
	}

	// Refresh docs/symlinks/cron/identity without touching backup.env
	if err := installSupportDocs(baseDir, bootstrap); err != nil {
		bootstrap.Warning("Upgrade: failed to refresh documentation: %v", err)
	}
	cleanupLegacyBashSymlinks(baseDir, bootstrap)
	ensureGoSymlink(execPath, bootstrap)

	cronSchedule := resolveCronSchedule(nil)
	migrateLegacyCronEntries(ctx, baseDir, execPath, bootstrap, cronSchedule)

	telegramCode := ""
	if info, err := identity.Detect(baseDir, nil); err == nil {
		if code := strings.TrimSpace(info.ServerID); code != "" {
			telegramCode = code
		}
	}

	permStatus, permMessage := fixPermissionsAfterInstall(ctx, args.ConfigPath, baseDir, bootstrap)

	printUpgradeFooter(upgradeErr, versionInstalled, args.ConfigPath, baseDir, telegramCode, permStatus, permMessage)

	if upgradeErr != nil {
		return types.ExitGenericError.Int()
	}
	return types.ExitSuccess.Int()
}

// downloadAndInstallLatest downloads the latest release archive from GitHub,
// verifies the checksum, extracts the proxsave binary, and installs it to execPath.
func downloadAndInstallLatest(ctx context.Context, execPath string, bootstrap *logging.BootstrapLogger) (string, error) {
	osName, arch, err := detectOSArch()
	if err != nil {
		return "", err
	}

	tag, version, err := fetchLatestRelease(ctx)
	if err != nil {
		return "", err
	}

	filename := fmt.Sprintf("proxsave_%s_%s_%s.tar.gz", version, osName, arch)
	archiveURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", githubRepo, tag, filename)
	checksumURL := fmt.Sprintf("https://github.com/%s/releases/download/%s/SHA256SUMS", githubRepo, tag)

	bootstrap.Info("Downloading latest release: %s (%s/%s)", tag, osName, arch)

	tmpDir, err := os.MkdirTemp("", "proxsave-upgrade-*")
	if err != nil {
		return "", fmt.Errorf("cannot create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	archivePath := filepath.Join(tmpDir, filename)
	checksumPath := filepath.Join(tmpDir, "SHA256SUMS")

	if err := downloadFile(ctx, archiveURL, archivePath); err != nil {
		return "", fmt.Errorf("failed to download archive: %w", err)
	}
	if err := downloadFile(ctx, checksumURL, checksumPath); err != nil {
		return "", fmt.Errorf("failed to download checksum file: %w", err)
	}

	if err := verifyChecksum(archivePath, checksumPath, filename); err != nil {
		return "", err
	}

	extractedPath := filepath.Join(tmpDir, "proxsave")
	if err := extractBinaryFromTar(archivePath, "proxsave", extractedPath); err != nil {
		return "", err
	}

	if err := installBinary(extractedPath, execPath); err != nil {
		return "", err
	}

	bootstrap.Info("Upgrade: installed proxsave %s to %s", version, execPath)
	return version, nil
}

func detectOSArch() (string, string, error) {
	osName := strings.ToLower(runtime.GOOS)
	if osName != "linux" {
		return "", "", fmt.Errorf("unsupported OS: %s (only linux is supported)", osName)
	}

	var arch string
	switch runtime.GOARCH {
	case "amd64":
		arch = "amd64"
	case "arm64":
		arch = "arm64"
	default:
		return "", "", fmt.Errorf("unsupported architecture: %s (supported: amd64, arm64)", runtime.GOARCH)
	}
	return osName, arch, nil
}

func fetchLatestRelease(ctx context.Context) (string, string, error) {
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", githubRepo)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create request: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("failed to fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024))
		return "", "", fmt.Errorf("failed to fetch latest release: status %d, body: %s", resp.StatusCode, string(body))
	}

	var info releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", "", fmt.Errorf("failed to parse release response: %w", err)
	}

	tag := strings.TrimSpace(info.TagName)
	if tag == "" {
		return "", "", errors.New("empty tag_name in latest release response")
	}

	version := strings.TrimPrefix(tag, "v")
	return tag, version, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("cannot create request: %w", err)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024))
		return fmt.Errorf("download failed with status %d: %s", resp.StatusCode, string(body))
	}

	out, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("cannot create file %s: %w", dest, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("cannot write file %s: %w", dest, err)
	}
	return nil
}

func verifyChecksum(archivePath, checksumPath, filename string) error {
	checksums, err := os.ReadFile(checksumPath)
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
		name := string(parts[len(parts)-1])
		if strings.HasSuffix(name, filename) {
			expected = string(parts[0])
			break
		}
	}

	if expected == "" {
		return fmt.Errorf("checksum entry not found for %s", filename)
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("cannot open archive for checksum: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return fmt.Errorf("cannot compute checksum: %w", err)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))

	if !strings.EqualFold(sum, expected) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", filename, expected, sum)
	}
	return nil
}

func extractBinaryFromTar(archivePath, targetName, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("cannot open archive: %w", err)
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("cannot create gzip reader: %w", err)
	}
	defer gzr.Close()

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

		tmpFile, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("cannot create extracted binary: %w", err)
		}
		if _, err := io.Copy(tmpFile, tr); err != nil {
			tmpFile.Close()
			return fmt.Errorf("cannot write extracted binary: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("cannot close extracted binary: %w", err)
		}
		return nil
	}

	return fmt.Errorf("binary %s not found inside archive", targetName)
}

func installBinary(srcPath, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("cannot create target directory: %w", err)
	}

	tmpDest := destPath + ".tmp"
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("cannot open extracted binary: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(tmpDest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("cannot create temp target binary: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("cannot copy binary to temp target: %w", err)
	}
	if err := dst.Close(); err != nil {
		return fmt.Errorf("cannot close temp target binary: %w", err)
	}

	if err := os.Rename(tmpDest, destPath); err != nil {
		return fmt.Errorf("cannot replace binary at %s: %w", destPath, err)
	}
	return nil
}

func printUpgradeFooter(upgradeErr error, version, configPath, baseDir, telegramCode, permStatus, permMessage string) {
	colorReset := "\033[0m"

	title := "Go-based upgrade completed"
	color := "\033[32m" // green

	if upgradeErr != nil {
		color = "\033[31m"
		title = "Go-based upgrade failed"
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

	fmt.Println("Next steps:")
	if strings.TrimSpace(configPath) != "" {
		fmt.Printf("1. Verify configuration (unchanged): %s\n", configPath)
	} else {
		fmt.Println("1. Verify configuration (unchanged)")
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
	fmt.Println("  proxsave (alias: proxmox-backup) - Start backup")
	fmt.Println("  --upgrade          - Update proxsave binary to latest release (no config changes)")
	fmt.Println("  --install          - Re-run interactive installation/setup")
	fmt.Println("  --new-install      - Wipe installation directory (keep env/identity) then run installer")
	fmt.Println("  --upgrade-config   - Upgrade configuration file using the embedded template (run after installing a new binary)")
	fmt.Println()

	if upgradeErr != nil {
		fmt.Println("Upgrade reported an error; please review the log above.")
	}
}
