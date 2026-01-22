package orchestrator

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var ErrDecryptAborted = errors.New("decrypt workflow aborted by user")
var titleCaser = cases.Title(language.English)

type decryptSourceType int

const (
	sourceBundle decryptSourceType = iota
	sourceRaw
)

type decryptCandidate struct {
	Manifest        *backup.Manifest
	Source          decryptSourceType
	BundlePath      string
	RawArchivePath  string
	RawMetadataPath string
	RawChecksumPath string
	DisplayBase     string
	IsRclone        bool
}

type stagedFiles struct {
	ArchivePath  string
	MetadataPath string
	ChecksumPath string
}

type preparedBundle struct {
	ArchivePath string
	Manifest    backup.Manifest
	Checksum    string
	cleanup     func()
}

func (p *preparedBundle) Cleanup() {
	if p == nil || p.cleanup == nil {
		return
	}
	p.cleanup()
}

// RunDecryptWorkflowWithDeps executes the decrypt workflow using injected dependencies.
func RunDecryptWorkflowWithDeps(ctx context.Context, deps *Deps, version string) (err error) {
	if deps == nil || deps.Config == nil {
		return fmt.Errorf("configuration not available")
	}
	cfg := deps.Config
	logger := deps.Logger
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	done := logging.DebugStart(logger, "decrypt workflow", "version=%s", version)
	defer func() { done(err) }()
	defer func() {
		if err == nil {
			return
		}
		if errors.Is(err, input.ErrInputAborted) || errors.Is(err, context.Canceled) {
			err = ErrDecryptAborted
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	_, prepared, err := prepareDecryptedBackup(ctx, reader, cfg, logger, version, true)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	// Ask for destination directory (where the final decrypted bundle will live)
	destDir, err := promptDestinationDir(ctx, reader, cfg)
	if err != nil {
		return err
	}
	if err := restoreFS.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	destDir, _ = filepath.Abs(destDir)
	logger.Info("Destination directory: %s", destDir)

	// Determine the logical decrypted archive path for naming purposes.
	// This keeps the same defaults and prompts as before, but the archive
	// itself stays in the temporary working directory.
	destArchivePath := filepath.Join(destDir, filepath.Base(prepared.ArchivePath))
	destArchivePath, err = ensureWritablePath(ctx, reader, destArchivePath, "decrypted archive")
	if err != nil {
		return err
	}

	// Work exclusively inside the temporary directory created by preparePlainBundle.
	workDir := filepath.Dir(prepared.ArchivePath)
	archiveBase := filepath.Base(destArchivePath)
	tempArchivePath := filepath.Join(workDir, archiveBase)

	// Ensure the staged archive in the temp dir has the desired basename.
	if tempArchivePath != prepared.ArchivePath {
		if err := moveFileSafe(prepared.ArchivePath, tempArchivePath); err != nil {
			return fmt.Errorf("move decrypted archive within temp dir: %w", err)
		}
	}

	manifestCopy := prepared.Manifest
	// Keep manifest path consistent with previous behavior: it refers to the
	// archive location in the destination directory, even though the archive
	// itself is not written there during the decrypt process.
	manifestCopy.ArchivePath = destArchivePath

	metadataPath := tempArchivePath + ".metadata"
	if err := backup.CreateManifest(ctx, logger, &manifestCopy, metadataPath); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	checksumPath := tempArchivePath + ".sha256"
	if err := restoreFS.WriteFile(checksumPath, []byte(fmt.Sprintf("%s  %s\n", prepared.Checksum, filepath.Base(tempArchivePath))), 0o640); err != nil {
		return fmt.Errorf("write checksum file: %w", err)
	}

	logger.Info("Creating decrypted bundle...")
	bundlePath, err := createBundle(ctx, logger, tempArchivePath)
	if err != nil {
		return err
	}

	// Only the final decrypted bundle is moved into the destination directory.
	// All temporary plain artifacts remain confined to the temp workdir and
	// are removed by prepared.Cleanup().
	logicalBundlePath := destArchivePath + ".bundle.tar"
	targetBundlePath := strings.TrimSuffix(logicalBundlePath, ".bundle.tar") + ".decrypted.bundle.tar"
	targetBundlePath, err = ensureWritablePath(ctx, reader, targetBundlePath, "decrypted bundle")
	if err != nil {
		return err
	}
	if err := restoreFS.Remove(targetBundlePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warning("Failed to remove existing bundle target: %v", err)
	}
	if err := moveFileSafe(bundlePath, targetBundlePath); err != nil {
		return fmt.Errorf("move decrypted bundle: %w", err)
	}

	logger.Info("Decrypted bundle created: %s", targetBundlePath)
	return nil
}

// RunDecryptWorkflow is the legacy entrypoint that builds default deps.
func RunDecryptWorkflow(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string) error {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	deps := defaultDeps(logger, cfg.DryRun)
	deps.Config = cfg
	return RunDecryptWorkflowWithDeps(ctx, &deps, version)
}

func selectDecryptCandidate(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, requireEncrypted bool) (candidate *decryptCandidate, err error) {
	done := logging.DebugStart(logger, "select backup candidate", "requireEncrypted=%v", requireEncrypted)
	defer func() { done(err) }()
	pathOptions := buildDecryptPathOptions(cfg, logger)
	if len(pathOptions) == 0 {
		return nil, fmt.Errorf("no backup paths configured in backup.env")
	}

	if logger != nil {
		for _, opt := range pathOptions {
			logger.Debug("Backup source option prepared: label=%q path=%q isRclone=%v", opt.Label, opt.Path, opt.IsRclone)
		}
	}

	var candidates []*decryptCandidate
	var selectedPath string

	for {
		option, err := promptPathSelection(ctx, reader, pathOptions)
		if err != nil {
			return nil, err
		}

		if logger != nil {
			logger.Debug("Backup source selected by user: label=%q path=%q isRclone=%v", option.Label, option.Path, option.IsRclone)
		}

			logger.Info("Scanning %s for backups...", option.Path)

		// Handle rclone remotes differently from filesystem paths
		if option.IsRclone {
			logging.DebugStep(logger, "select backup candidate", "scanning rclone remote: %s", option.Path)
			candidates, err = discoverRcloneBackups(ctx, option.Path, logger)
			if err != nil {
				logger.Warning("Failed to inspect cloud remote %s: %v", option.Path, err)
				// On persistent failures, remove this option so it is no longer offered.
				pathOptions = removeDecryptPathOption(pathOptions, option)
				if len(pathOptions) == 0 {
					return nil, fmt.Errorf("no usable backup sources available")
				}
				continue
			}
			if logger != nil {
				logger.Debug("Cloud (rclone): %d candidate bundle(s) returned for %s", len(candidates), option.Path)
			}
		} else {
			logging.DebugStep(logger, "select backup candidate", "scanning filesystem path: %s", option.Path)
			info, err := restoreFS.Stat(option.Path)
			if err != nil || !info.IsDir() {
				logger.Warning("Path %s is not accessible (%v)", option.Path, err)
				continue
			}

			candidates, err = discoverBackupCandidates(logger, option.Path)
			if err != nil {
				logger.Warning("Failed to inspect %s: %v", option.Path, err)
				continue
			}
		}
		if len(candidates) == 0 {
				logger.Warning("No backups found in %s – removing from source list", option.Path)
			if logger != nil {
				logger.Debug("Removing backup source %q (%s) due to empty candidate list", option.Label, option.Path)
			}
			pathOptions = removeDecryptPathOption(pathOptions, option)
			if len(pathOptions) == 0 {
				return nil, fmt.Errorf("no usable backup sources available")
			}
			continue
		}

		if requireEncrypted {
			encrypted := filterEncryptedCandidates(candidates)
			if len(encrypted) == 0 {
				logger.Warning("No encrypted backups found in %s – removing from source list", option.Path)
				if logger != nil {
					logger.Debug("Removing backup source %q (%s) because all candidates are plain (non-encrypted)", option.Label, option.Path)
				}
				pathOptions = removeDecryptPathOption(pathOptions, option)
				if len(pathOptions) == 0 {
					return nil, fmt.Errorf("no usable backup sources available")
				}
				continue
			}

			if logger != nil {
				logger.Debug("Backup candidates after encryption filter: total=%d encrypted=%d", len(candidates), len(encrypted))
			}

			candidates = encrypted
		}
		selectedPath = option.Path
		break
	}

	if requireEncrypted {
		logger.Info("Found %d encrypted backup(s) in %s", len(candidates), selectedPath)
	} else {
		logger.Info("Found %d backup(s) in %s", len(candidates), selectedPath)
	}
	candidate, err = promptCandidateSelection(ctx, reader, candidates)
	return candidate, err
}

func promptPathSelection(ctx context.Context, reader *bufio.Reader, options []decryptPathOption) (decryptPathOption, error) {
	for {
		fmt.Println("\nSelect the backup source:")
		for idx, option := range options {
			fmt.Printf("  [%d] %s (%s)\n", idx+1, option.Label, option.Path)
		}
		fmt.Println("  [0] Exit")

		fmt.Print("Choice: ")
		choiceLine, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return decryptPathOption{}, err
		}
		trimmed := strings.TrimSpace(choiceLine)
		if trimmed == "0" {
			return decryptPathOption{}, ErrDecryptAborted
		}
		if trimmed == "" {
			continue
		}
		idx, err := parseMenuIndex(trimmed, len(options))
		if err != nil {
			fmt.Println(err)
			continue
		}
		return options[idx], nil
	}
}

func inspectBundleManifest(bundlePath string) (*backup.Manifest, error) {
	file, err := restoreFS.Open(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("open bundle: %w", err)
	}
	defer file.Close()

	tr := tar.NewReader(file)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read bundle: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if strings.HasSuffix(hdr.Name, ".metadata") || strings.HasSuffix(hdr.Name, ".manifest.json") {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("read manifest entry: %w", err)
			}
			var manifest backup.Manifest
			if err := json.Unmarshal(data, &manifest); err != nil {
				return nil, fmt.Errorf("parse manifest: %w", err)
			}
			return &manifest, nil
		}
	}
	return nil, fmt.Errorf("manifest not found inside %s", filepath.Base(bundlePath))
}

// inspectRcloneBundleManifest reads the manifest from a bundle stored on an
// rclone remote by streaming it through "rclone cat" and parsing the tar
// stream until the manifest entry is found.
func inspectRcloneBundleManifest(ctx context.Context, remotePath string, logger *logging.Logger) (manifest *backup.Manifest, err error) {
	done := logging.DebugStart(logger, "inspect rclone bundle manifest", "remote=%s", remotePath)
	defer func() { done(err) }()
	start := time.Now()

	// Use a child context so we can stop rclone once the manifest is found.
	// This avoids a deadlock when the manifest is near the beginning of the tar:
	// if we stop reading stdout early and still Wait(), rclone can block writing
	// the remaining bytes into a full pipe.
	cmdCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	logging.DebugStep(logger, "inspect rclone bundle manifest", "executing: rclone cat %s", remotePath)
	cmd := exec.CommandContext(cmdCtx, "rclone", "cat", remotePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open rclone stream: %w", err)
	}
	defer stdout.Close()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start rclone cat: %w", err)
	}

	tr := tar.NewReader(stdout)
	cancelledEarly := false

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return nil, fmt.Errorf("read bundle from remote: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if strings.HasSuffix(hdr.Name, ".metadata") || strings.HasSuffix(hdr.Name, ".manifest.json") {
			data, err := io.ReadAll(tr)
			if err != nil {
				_ = cmd.Wait()
				return nil, fmt.Errorf("read manifest entry from remote: %w", err)
			}
			var m backup.Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				_ = cmd.Wait()
				return nil, fmt.Errorf("parse manifest from remote: %w", err)
			}
			manifest = &m
			logging.DebugStep(logger, "inspect rclone bundle manifest", "manifest entry=%s bytes=%d", hdr.Name, len(data))
			cancelledEarly = true
			cancel()
			break
		}
	}

	// We intentionally ignore the error from Wait() if we already have the manifest,
	// because stopping early may cause rclone to see a broken pipe.
	waitErr := cmd.Wait()
	if manifest == nil {
		stderrMsg := strings.TrimSpace(stderr.String())
		if waitErr != nil {
			if stderrMsg != "" {
				return nil, fmt.Errorf("manifest not found inside remote bundle (rclone exited with error): %w (stderr: %s)", waitErr, stderrMsg)
			}
			return nil, fmt.Errorf("manifest not found inside remote bundle (rclone exited with error): %w", waitErr)
		}
		if stderrMsg != "" {
			return nil, fmt.Errorf("manifest not found inside remote bundle %s (stderr: %s)", filepath.Base(remotePath), stderrMsg)
		}
		return nil, fmt.Errorf("manifest not found inside remote bundle %s", filepath.Base(remotePath))
	}
	if waitErr != nil {
		// If we cancelled early, rclone is expected to exit non-zero (broken pipe / killed).
		if cancelledEarly {
			logDebug(logger, "rclone cat %s stopped early after manifest read: %v (elapsed=%s stderr=%q)", remotePath, waitErr, time.Since(start), strings.TrimSpace(stderr.String()))
		} else {
			logDebug(logger, "rclone cat %s completed with non-zero status after manifest read: %v (elapsed=%s stderr=%q)", remotePath, waitErr, time.Since(start), strings.TrimSpace(stderr.String()))
		}
	}

	return manifest, nil
}

// inspectRcloneMetadataManifest reads a sidecar metadata file from an rclone
// remote by streaming it through "rclone cat" and parsing it as either the
// JSON manifest format or the legacy KEY=VALUE format.
func inspectRcloneMetadataManifest(ctx context.Context, remoteMetadataPath, remoteArchivePath string, logger *logging.Logger) (manifest *backup.Manifest, err error) {
	done := logging.DebugStart(logger, "inspect rclone metadata manifest", "remote=%s", remoteMetadataPath)
	defer func() { done(err) }()
	logging.DebugStep(logger, "inspect rclone metadata manifest", "executing: rclone cat %s", remoteMetadataPath)

	cmd := exec.CommandContext(ctx, "rclone", "cat", remoteMetadataPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("rclone cat %s failed: %w (output: %s)", remoteMetadataPath, err, strings.TrimSpace(string(output)))
	}
	data := bytes.TrimSpace(output)
	if len(data) == 0 {
		return nil, fmt.Errorf("metadata file is empty")
	}

	var parsed backup.Manifest
	if err := json.Unmarshal(data, &parsed); err == nil {
		manifest = &parsed
		if strings.TrimSpace(manifest.ArchivePath) == "" {
			manifest.ArchivePath = remoteArchivePath
		}
		return manifest, nil
	}

	// Legacy KEY=VALUE format (best-effort, without archive stat/checksum).
	legacy := &backup.Manifest{
		ArchivePath: remoteArchivePath,
	}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "COMPRESSION_TYPE":
			legacy.CompressionType = value
		case "COMPRESSION_LEVEL":
			if lvl, err := strconv.Atoi(value); err == nil {
				legacy.CompressionLevel = lvl
			}
		case "PROXMOX_TYPE":
			legacy.ProxmoxType = value
		case "HOSTNAME":
			legacy.Hostname = value
		case "SCRIPT_VERSION":
			legacy.ScriptVersion = value
		case "ENCRYPTION_MODE":
			legacy.EncryptionMode = value
		}
	}
	if strings.TrimSpace(legacy.EncryptionMode) == "" {
		if strings.HasSuffix(remoteArchivePath, ".age") {
			legacy.EncryptionMode = "age"
		} else {
			legacy.EncryptionMode = "plain"
		}
	}
	// Keep CreatedAt stable (zero) rather than guessing.
	legacy.CreatedAt = time.Time{}
	return legacy, nil
}

func promptCandidateSelection(ctx context.Context, reader *bufio.Reader, candidates []*decryptCandidate) (*decryptCandidate, error) {
	for {
		fmt.Println("\nAvailable backups:")
		for idx, cand := range candidates {
			created := cand.Manifest.CreatedAt.Format("2006-01-02 15:04:05")
			enc := strings.ToUpper(statusFromManifest(cand.Manifest))
			toolVersion := cand.Manifest.ScriptVersion
			if toolVersion == "" {
				toolVersion = "unknown"
			}
			targetSummary := formatTargetSummary(cand.Manifest)
			fmt.Printf("  [%d] %s • %s • Tool v%s • %s\n", idx+1, created, enc, toolVersion, targetSummary)
		}
		fmt.Println("  [0] Exit")

		fmt.Print("Choice: ")
		choiceLine, err := input.ReadLineWithContext(ctx, reader)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(choiceLine)
		if trimmed == "0" {
			return nil, ErrDecryptAborted
		}
		if trimmed == "" {
			continue
		}
		idx, err := parseMenuIndex(trimmed, len(candidates))
		if err != nil {
			fmt.Println(err)
			continue
		}
		return candidates[idx], nil
	}
}

func promptDestinationDir(ctx context.Context, reader *bufio.Reader, cfg *config.Config) (string, error) {
	defaultDir := "./decrypt"
	if cfg != nil {
		if base := strings.TrimSpace(cfg.BaseDir); base != "" {
			defaultDir = filepath.Join(base, "decrypt")
		}
	}
	fmt.Printf("\nEnter destination directory for decrypted bundle [press Enter to use %s]: ", defaultDir)
	inputLine, err := input.ReadLineWithContext(ctx, reader)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(inputLine)
	if trimmed == "" {
		trimmed = defaultDir
	}
	return filepath.Clean(trimmed), nil
}

// downloadRcloneBackup downloads a backup bundle from an rclone remote to a local temp file
func downloadRcloneBackup(ctx context.Context, remotePath string, logger *logging.Logger) (tmpPath string, cleanup func(), err error) {
	done := logging.DebugStart(logger, "download rclone backup", "remote=%s", remotePath)
	defer func() { done(err) }()
	// Ensure /tmp/proxsave exists
	tempRoot := filepath.Join("/tmp", "proxsave")
	if err := restoreFS.MkdirAll(tempRoot, 0o755); err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	// Create temp file for download in /tmp/proxsave/
	tmpFile, err := os.CreateTemp(tempRoot, "proxsave-rclone-*.bundle.tar")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath = tmpFile.Name()
	tmpFile.Close()

	cleanup = func() {
		logger.Debug("Removing temporary rclone download: %s", tmpPath)
		os.Remove(tmpPath)
	}

	logger.Info("Downloading backup from cloud storage: %s", remotePath)
	logging.DebugStep(logger, "download rclone backup", "local temp file=%s", tmpPath)

	// Use rclone copyto to download with progress
	cmd := exec.CommandContext(ctx, "rclone", "copyto", remotePath, tmpPath, "--progress")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("rclone download failed: %w", err)
	}

	logger.Info("Download complete: %s", filepath.Base(remotePath))
	return tmpPath, cleanup, nil
}

func preparePlainBundle(ctx context.Context, reader *bufio.Reader, cand *decryptCandidate, version string, logger *logging.Logger) (bundle *preparedBundle, err error) {
	done := logging.DebugStart(logger, "prepare plain bundle", "source=%v rclone=%v", cand.Source, cand.IsRclone)
	defer func() { done(err) }()
	// If this is an rclone backup, download it first
	var rcloneCleanup func()
	if cand.IsRclone && cand.Source == sourceBundle {
		logger.Debug("Detected rclone backup, downloading...")
		localPath, cleanup, err := downloadRcloneBackup(ctx, cand.BundlePath, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to download rclone backup: %w", err)
		}
		rcloneCleanup = cleanup
		// Update candidate to use local path
		cand.BundlePath = localPath
	}

	tempRoot := filepath.Join("/tmp", "proxsave")
	if err := restoreFS.MkdirAll(tempRoot, 0o755); err != nil {
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
		return nil, fmt.Errorf("create temp root: %w", err)
	}
	workDir, err := restoreFS.MkdirTemp(tempRoot, "proxmox-decrypt-*")
	if err != nil {
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	logging.DebugStep(logger, "prepare plain bundle", "workdir=%s", workDir)
	cleanup := func() {
		_ = restoreFS.RemoveAll(workDir)
		if rcloneCleanup != nil {
			rcloneCleanup()
		}
	}

	var staged stagedFiles
	switch cand.Source {
	case sourceBundle:
		logger.Info("Extracting bundle %s", filepath.Base(cand.BundlePath))
		staged, err = extractBundleToWorkdirWithLogger(cand.BundlePath, workDir, logger)
	case sourceRaw:
		logger.Info("Staging raw artifacts for %s", filepath.Base(cand.RawArchivePath))
		staged, err = copyRawArtifactsToWorkdirWithLogger(ctx, cand, workDir, logger)
	default:
		err = fmt.Errorf("unsupported candidate source")
	}
	if err != nil {
		cleanup()
		return nil, err
	}

	manifestCopy := *cand.Manifest
	currentEncryption := strings.ToLower(manifestCopy.EncryptionMode)

	logging.DebugStep(logger, "prepare plain bundle", "encryption=%s", currentEncryption)
	logger.Info("Preparing archive %s for decryption (mode: %s)", manifestCopy.ArchivePath, statusFromManifest(&manifestCopy))

	plainArchiveName := strings.TrimSuffix(filepath.Base(staged.ArchivePath), ".age")
	plainArchivePath := filepath.Join(workDir, plainArchiveName)

	if currentEncryption == "age" {
		if err := decryptArchiveWithPrompts(ctx, reader, staged.ArchivePath, plainArchivePath, logger); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		// For plain archives, only copy if source and destination are different
		// to avoid truncating the file when copying to itself
		if staged.ArchivePath != plainArchivePath {
			if err := copyFile(restoreFS, staged.ArchivePath, plainArchivePath); err != nil {
				cleanup()
				return nil, fmt.Errorf("copy archive: %w", err)
			}
		}
		// If paths are identical, file is already in the correct location
	}

	archiveInfo, err := restoreFS.Stat(plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("stat decrypted archive: %w", err)
	}

	checksum, err := backup.GenerateChecksum(ctx, logger, plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("generate checksum: %w", err)
	}
	logging.DebugStep(logger, "prepare plain bundle", "checksum computed")

	manifestCopy.ArchivePath = plainArchivePath
	manifestCopy.ArchiveSize = archiveInfo.Size()
	manifestCopy.SHA256 = checksum
	manifestCopy.EncryptionMode = "none"
	if version != "" {
		manifestCopy.ScriptVersion = version
	}

	bundle = &preparedBundle{
		ArchivePath: plainArchivePath,
		Manifest:    manifestCopy,
		Checksum:    checksum,
		cleanup:     cleanup,
	}
	return bundle, nil
}

func prepareDecryptedBackup(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string, requireEncrypted bool) (candidate *decryptCandidate, prepared *preparedBundle, err error) {
	done := logging.DebugStart(logger, "prepare decrypted backup", "requireEncrypted=%v", requireEncrypted)
	defer func() { done(err) }()
	candidate, err = selectDecryptCandidate(ctx, reader, cfg, logger, requireEncrypted)
	if err != nil {
		return nil, nil, err
	}

	prepared, err = preparePlainBundle(ctx, reader, candidate, version, logger)
	if err != nil {
		return nil, nil, err
	}

	return candidate, prepared, nil
}

// sanitizeBundleEntryName ensures the tar entry name cannot escape the working directory.
func sanitizeBundleEntryName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("invalid archive entry name %q", name)
	}

	// Normalize Windows-style separators to POSIX style so path.Clean can catch traversal attempts.
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	cleaned := path.Clean(normalized)
	if cleaned == "" || cleaned == "." {
		return "", fmt.Errorf("invalid archive entry name %q", name)
	}
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("archive entry escapes workdir: %q", name)
	}

	base := path.Base(cleaned)
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("invalid base name in archive entry %q", name)
	}
	return base, nil
}

func extractBundleToWorkdir(bundlePath, workDir string) (staged stagedFiles, err error) {
	return extractBundleToWorkdirWithLogger(bundlePath, workDir, nil)
}

func extractBundleToWorkdirWithLogger(bundlePath, workDir string, logger *logging.Logger) (staged stagedFiles, err error) {
	done := logging.DebugStart(logger, "extract bundle", "bundle=%s workdir=%s", bundlePath, workDir)
	defer func() { done(err) }()
	file, err := restoreFS.Open(bundlePath)
	if err != nil {
		return stagedFiles{}, fmt.Errorf("open bundle: %w", err)
	}
	defer file.Close()

	tr := tar.NewReader(file)
	extracted := 0

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stagedFiles{}, fmt.Errorf("read bundle: %w", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}

		safeName, err := sanitizeBundleEntryName(hdr.Name)
		if err != nil {
			return stagedFiles{}, fmt.Errorf("unsafe entry name %q in bundle: %w", hdr.Name, err)
		}

		target := filepath.Join(workDir, safeName)
		rel, err := filepath.Rel(workDir, target)
		if err != nil {
			return stagedFiles{}, fmt.Errorf("resolve %s: %w", hdr.Name, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return stagedFiles{}, fmt.Errorf("archive entry escapes workdir: %q", hdr.Name)
		}
		out, err := restoreFS.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return stagedFiles{}, fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return stagedFiles{}, fmt.Errorf("write %s: %w", hdr.Name, err)
		}
		out.Close()
		extracted++

		switch {
		case strings.HasSuffix(target, ".metadata"):
			staged.MetadataPath = target
			logging.DebugStep(logger, "extract bundle", "found metadata=%s", filepath.Base(target))
		case strings.HasSuffix(target, ".sha256"):
			staged.ChecksumPath = target
			logging.DebugStep(logger, "extract bundle", "found checksum=%s", filepath.Base(target))
		default:
			staged.ArchivePath = target
			logging.DebugStep(logger, "extract bundle", "found archive=%s", filepath.Base(target))
		}
	}

	if staged.ArchivePath == "" || staged.MetadataPath == "" || staged.ChecksumPath == "" {
		return stagedFiles{}, fmt.Errorf("bundle missing required files")
	}
	logging.DebugStep(logger, "extract bundle", "entries_extracted=%d", extracted)
	return staged, nil
}

func copyRawArtifactsToWorkdir(ctx context.Context, cand *decryptCandidate, workDir string) (staged stagedFiles, err error) {
	return copyRawArtifactsToWorkdirWithLogger(ctx, cand, workDir, nil)
}

func baseNameFromRemoteRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return filepath.Base(ref)
	}
	rel := strings.Trim(parts[1], "/")
	if rel == "" {
		return ""
	}
	return path.Base(rel)
}

func rcloneCopyTo(ctx context.Context, remotePath, localPath string, showProgress bool) error {
	args := []string{"copyto", remotePath, localPath}
	if showProgress {
		args = append(args, "--progress")
	}
	cmd := exec.CommandContext(ctx, "rclone", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyRawArtifactsToWorkdirWithLogger(ctx context.Context, cand *decryptCandidate, workDir string, logger *logging.Logger) (staged stagedFiles, err error) {
	done := logging.DebugStart(logger, "stage raw artifacts", "archive=%s workdir=%s rclone=%v", cand.RawArchivePath, workDir, cand.IsRclone)
	defer func() { done(err) }()
	if ctx == nil {
		ctx = context.Background()
	}
	if cand == nil {
		return stagedFiles{}, fmt.Errorf("candidate is nil")
	}

	archiveBase := filepath.Base(cand.RawArchivePath)
	metaBase := filepath.Base(cand.RawMetadataPath)
	sumBase := ""
	if cand.IsRclone {
		archiveBase = baseNameFromRemoteRef(cand.RawArchivePath)
		metaBase = baseNameFromRemoteRef(cand.RawMetadataPath)
		if cand.RawChecksumPath != "" {
			sumBase = baseNameFromRemoteRef(cand.RawChecksumPath)
		}
	} else if cand.RawChecksumPath != "" {
		sumBase = filepath.Base(cand.RawChecksumPath)
	}
	if archiveBase == "" || metaBase == "" {
		return stagedFiles{}, fmt.Errorf("invalid raw candidate paths")
	}

	archiveDest := filepath.Join(workDir, archiveBase)
	metadataDest := filepath.Join(workDir, metaBase)
	checksumDest := ""
	if sumBase != "" {
		checksumDest = filepath.Join(workDir, sumBase)
	}

	if cand.IsRclone {
		logging.DebugStep(logger, "stage raw artifacts", "download archive to %s", archiveDest)
		if err := rcloneCopyTo(ctx, cand.RawArchivePath, archiveDest, true); err != nil {
			return stagedFiles{}, fmt.Errorf("rclone download archive: %w", err)
		}
		logging.DebugStep(logger, "stage raw artifacts", "download metadata to %s", metadataDest)
		if err := rcloneCopyTo(ctx, cand.RawMetadataPath, metadataDest, false); err != nil {
			return stagedFiles{}, fmt.Errorf("rclone download metadata: %w", err)
		}
		if cand.RawChecksumPath != "" && checksumDest != "" {
			logging.DebugStep(logger, "stage raw artifacts", "download checksum to %s", checksumDest)
			if err := rcloneCopyTo(ctx, cand.RawChecksumPath, checksumDest, false); err != nil {
				logWarning(logger, "Failed to download checksum %s: %v", cand.RawChecksumPath, err)
				checksumDest = ""
			}
		}
	} else {
		logging.DebugStep(logger, "stage raw artifacts", "copy archive to %s", archiveDest)
		if err := copyFile(restoreFS, cand.RawArchivePath, archiveDest); err != nil {
			return stagedFiles{}, fmt.Errorf("copy archive: %w", err)
		}
		logging.DebugStep(logger, "stage raw artifacts", "copy metadata to %s", metadataDest)
		if err := copyFile(restoreFS, cand.RawMetadataPath, metadataDest); err != nil {
			return stagedFiles{}, fmt.Errorf("copy metadata: %w", err)
		}
		if cand.RawChecksumPath != "" && checksumDest != "" {
			logging.DebugStep(logger, "stage raw artifacts", "copy checksum to %s", checksumDest)
			if err := copyFile(restoreFS, cand.RawChecksumPath, checksumDest); err != nil {
				logWarning(logger, "Failed to copy checksum %s: %v", cand.RawChecksumPath, err)
				checksumDest = ""
			}
		}
	}

	return stagedFiles{
		ArchivePath:  archiveDest,
		MetadataPath: metadataDest,
		ChecksumPath: checksumDest,
	}, nil
}

func decryptArchiveWithPrompts(ctx context.Context, reader *bufio.Reader, encryptedPath, outputPath string, logger *logging.Logger) error {
	for {
		fmt.Print("Enter decryption key or passphrase (0 = exit): ")
		inputBytes, err := input.ReadPasswordWithContext(ctx, readPassword, int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return err
		}
		trimmed := bytes.TrimSpace(inputBytes)
		if len(trimmed) == 0 {
			zeroBytes(inputBytes)
			logger.Warning("Input cannot be empty")
			continue
		}
		input := string(trimmed)
		zeroBytes(trimmed)
		zeroBytes(inputBytes)
		if input == "0" {
			return ErrDecryptAborted
		}

		identities, err := parseIdentityInput(input)
		resetString(&input)
		if err != nil {
			logger.Warning("Invalid key/passphrase: %v", err)
			continue
		}

		if err := decryptWithIdentity(encryptedPath, outputPath, identities...); err != nil {
			var noMatch *age.NoIdentityMatchError
			if errors.Is(err, age.ErrIncorrectIdentity) || errors.As(err, &noMatch) {
				logger.Warning("Provided key or passphrase does not match this archive. Try again or press 0 to exit.")
				continue
			}
			return err
		}
		return nil
	}
}

func parseIdentityInput(input string) ([]age.Identity, error) {
	if strings.HasPrefix(strings.ToUpper(input), "AGE-SECRET-KEY-") {
		id, err := age.ParseX25519Identity(strings.ToUpper(input))
		if err != nil {
			return nil, err
		}
		return []age.Identity{id}, nil
	}
	return deriveDeterministicIdentitiesFromPassphrase(input)
}

func decryptWithIdentity(src, dst string, identities ...age.Identity) error {
	in, err := restoreFS.Open(src)
	if err != nil {
		return fmt.Errorf("open encrypted archive: %w", err)
	}
	defer in.Close()

	out, err := restoreFS.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create decrypted archive: %w", err)
	}
	defer out.Close()

	reader, err := age.Decrypt(in, identities...)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, reader); err != nil {
		return fmt.Errorf("write decrypted archive: %w", err)
	}
	return nil
}

func parseMenuIndex(input string, max int) (int, error) {
	idx, err := strconv.Atoi(input)
	if err != nil || idx < 1 || idx > max {
		return 0, fmt.Errorf("please enter a value between 1 and %d", max)
	}
	return idx - 1, nil
}

func formatTargets(manifest *backup.Manifest) string {
	if len(manifest.ProxmoxTargets) > 0 {
		return strings.Join(manifest.ProxmoxTargets, "+")
	}
	if manifest.ProxmoxType != "" {
		return manifest.ProxmoxType
	}
	return "unknown target"
}

func formatTargetSummary(manifest *backup.Manifest) string {
	targets := formatTargets(manifest)
	version := strings.TrimSpace(manifest.ProxmoxVersion)
	if version == "" {
		version = "unknown"
	}
	if !strings.HasPrefix(strings.ToLower(version), "v") {
		version = "v" + version
	}
	summary := fmt.Sprintf("%s %s", targets, version)
	if cluster := formatClusterMode(manifest.ClusterMode); cluster != "" {
		summary = fmt.Sprintf("%s (%s)", summary, cluster)
	}
	return summary
}

func statusFromManifest(manifest *backup.Manifest) string {
	mode := strings.TrimSpace(manifest.EncryptionMode)
	if strings.EqualFold(mode, "age") {
		return "encrypted"
	}
	return "plain"
}

func moveFileSafe(src, dst string) error {
	if err := restoreFS.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyFile(restoreFS, src, dst); err != nil {
		return err
	}
	return restoreFS.Remove(src)
}

func ensureWritablePath(ctx context.Context, reader *bufio.Reader, path, description string) (string, error) {
	current := filepath.Clean(path)
	for {
		if _, err := restoreFS.Stat(current); errors.Is(err, os.ErrNotExist) {
			return current, nil
		} else if err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("stat %s: %w", current, err)
		}

		fmt.Printf("%s %s already exists.\n", titleCaser.String(description), current)
		fmt.Println("  [1] Overwrite")
			fmt.Println("  [2] Enter a different path")
			fmt.Println("  [0] Exit")
			fmt.Print("Choice: ")

			inputLine, err := input.ReadLineWithContext(ctx, reader)
			if err != nil {
				return "", err
			}
			switch strings.TrimSpace(inputLine) {
			case "1":
				if err := restoreFS.Remove(current); err != nil {
					fmt.Printf("Failed to remove existing file: %v\n", err)
					continue
				}
				return current, nil
			case "2":
				fmt.Print("Enter new path: ")
				newPath, err := input.ReadLineWithContext(ctx, reader)
				if err != nil {
					return "", err
				}
			trimmed := strings.TrimSpace(newPath)
			if trimmed == "" {
				continue
			}
			current = filepath.Clean(trimmed)
		case "0":
			return "", ErrDecryptAborted
		default:
			fmt.Println("Please enter 1, 2 or 0.")
		}
	}
}

func formatClusterMode(value string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	switch mode {
	case "cluster", "standalone":
		return mode
	default:
		return ""
	}
}
