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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"filippo.io/age"
	"github.com/tis24dev/proxmox-backup/internal/backup"
	"github.com/tis24dev/proxmox-backup/internal/config"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var ErrDecryptAborted = errors.New("decrypt workflow aborted by user")
var titleCaser = cases.Title(language.English)

type decryptPathOption struct {
	Label string
	Path  string
}

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
func RunDecryptWorkflowWithDeps(ctx context.Context, deps *Deps, version string) error {
	if deps == nil || deps.Config == nil {
		return fmt.Errorf("configuration not available")
	}
	cfg := deps.Config
	logger := deps.Logger
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	reader := bufio.NewReader(os.Stdin)
	_, prepared, err := prepareDecryptedBackup(ctx, reader, cfg, logger, version)
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

func buildDecryptPathOptions(cfg *config.Config) []decryptPathOption {
	options := make([]decryptPathOption, 0, 3)

	if clean := strings.TrimSpace(cfg.BackupPath); clean != "" {
		options = append(options, decryptPathOption{
			Label: "Local backups",
			Path:  clean,
		})
	}

	if cfg.SecondaryEnabled {
		if clean := strings.TrimSpace(cfg.SecondaryPath); clean != "" {
			options = append(options, decryptPathOption{
				Label: "Secondary backups",
				Path:  clean,
			})
		}
	}

	if cfg.CloudEnabled && isLocalFilesystemPath(cfg.CloudRemote) {
		options = append(options, decryptPathOption{
			Label: "Cloud backups",
			Path:  strings.TrimSpace(cfg.CloudRemote),
		})
	}

	return options
}

func selectDecryptCandidate(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger) (*decryptCandidate, error) {
	pathOptions := buildDecryptPathOptions(cfg)
	if len(pathOptions) == 0 {
		return nil, fmt.Errorf("no backup paths configured in backup.env")
	}

	var candidates []*decryptCandidate
	var selectedPath string

	for {
		option, err := promptPathSelection(ctx, reader, pathOptions)
		if err != nil {
			return nil, err
		}

		logger.Info("Scanning %s for backup bundles...", option.Path)
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
		if len(candidates) == 0 {
			logger.Warning("No backup bundles found in %s", option.Path)
			continue
		}

		// Align CLI behavior with the TUI decrypt flow:
		// only encrypted backups are valid candidates for decrypt.
		encrypted := filterEncryptedCandidates(candidates)
		if len(encrypted) == 0 {
			logger.Warning("No encrypted backups found in %s", option.Path)
			continue
		}

		candidates = encrypted
		selectedPath = option.Path
		break
	}

	logger.Info("Found %d encrypted backup(s) in %s", len(candidates), selectedPath)
	return promptCandidateSelection(ctx, reader, candidates)
}

func promptPathSelection(ctx context.Context, reader *bufio.Reader, options []decryptPathOption) (decryptPathOption, error) {
	for {
		fmt.Println("\nSelect the backup source:")
		for idx, option := range options {
			fmt.Printf("  [%d] %s (%s)\n", idx+1, option.Label, option.Path)
		}
		fmt.Println("  [0] Exit")

		fmt.Print("Choice: ")
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return decryptPathOption{}, err
		}
		trimmed := strings.TrimSpace(input)
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

func discoverBackupCandidates(logger *logging.Logger, root string) ([]*decryptCandidate, error) {
	entries, err := restoreFS.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", root, err)
	}

	candidates := make([]*decryptCandidate, 0)
	rawBases := make(map[string]struct{})

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		fullPath := filepath.Join(root, name)

		switch {
		case strings.HasSuffix(name, ".bundle.tar"):
			manifest, err := inspectBundleManifest(fullPath)
			if err != nil {
				logger.Warning("Skipping bundle %s: %v", name, err)
				continue
			}
			candidates = append(candidates, &decryptCandidate{
				Manifest:    manifest,
				Source:      sourceBundle,
				BundlePath:  fullPath,
				DisplayBase: filepath.Base(manifest.ArchivePath),
			})
		case strings.HasSuffix(name, ".metadata"):
			baseName := strings.TrimSuffix(name, ".metadata")
			if _, ok := rawBases[baseName]; ok {
				continue
			}
			archivePath := filepath.Join(root, baseName)
			if _, err := restoreFS.Stat(archivePath); err != nil {
				continue
			}
			checksumPath := archivePath + ".sha256"
			hasChecksum := true
			if _, err := restoreFS.Stat(checksumPath); err != nil {
				// Checksum missing - allow but warn
				logger.Warning("Backup %s is missing .sha256 checksum file", baseName)
				checksumPath = ""
				hasChecksum = false
			}
			manifest, err := backup.LoadManifest(fullPath)
			if err != nil {
				logger.Warning("Skipping metadata %s: %v", name, err)
				continue
			}

			// If checksum is missing from both file and manifest, warn user
			if !hasChecksum && manifest.SHA256 == "" {
				logger.Warning("Backup %s has no checksum verification available", baseName)
			}

			rawBases[baseName] = struct{}{}
			candidates = append(candidates, &decryptCandidate{
				Manifest:        manifest,
				Source:          sourceRaw,
				RawArchivePath:  archivePath,
				RawMetadataPath: fullPath,
				RawChecksumPath: checksumPath,
				DisplayBase:     filepath.Base(manifest.ArchivePath),
			})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Manifest.CreatedAt.After(candidates[j].Manifest.CreatedAt)
	})

	return candidates, nil
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
		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(input)
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
	input, err := readLineWithContext(ctx, reader)
	if err != nil {
		return "", err
	}
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		trimmed = defaultDir
	}
	return filepath.Clean(trimmed), nil
}

func preparePlainBundle(ctx context.Context, reader *bufio.Reader, cand *decryptCandidate, version string, logger *logging.Logger) (*preparedBundle, error) {
	tempRoot := filepath.Join("/tmp", "proxmox-backup")
	if err := restoreFS.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create temp root: %w", err)
	}
	workDir, err := restoreFS.MkdirTemp(tempRoot, "proxmox-decrypt-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() {
		_ = restoreFS.RemoveAll(workDir)
	}

	var staged stagedFiles
	switch cand.Source {
	case sourceBundle:
		logger.Info("Extracting bundle %s", filepath.Base(cand.BundlePath))
		staged, err = extractBundleToWorkdir(cand.BundlePath, workDir)
	case sourceRaw:
		logger.Info("Staging raw artifacts for %s", filepath.Base(cand.RawArchivePath))
		staged, err = copyRawArtifactsToWorkdir(cand, workDir)
	default:
		err = fmt.Errorf("unsupported candidate source")
	}
	if err != nil {
		cleanup()
		return nil, err
	}

	manifestCopy := *cand.Manifest
	currentEncryption := strings.ToLower(manifestCopy.EncryptionMode)

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

	manifestCopy.ArchivePath = plainArchivePath
	manifestCopy.ArchiveSize = archiveInfo.Size()
	manifestCopy.SHA256 = checksum
	manifestCopy.EncryptionMode = "none"
	if version != "" {
		manifestCopy.ScriptVersion = version
	}

	return &preparedBundle{
		ArchivePath: plainArchivePath,
		Manifest:    manifestCopy,
		Checksum:    checksum,
		cleanup:     cleanup,
	}, nil
}

func prepareDecryptedBackup(ctx context.Context, reader *bufio.Reader, cfg *config.Config, logger *logging.Logger, version string) (*decryptCandidate, *preparedBundle, error) {
	candidate, err := selectDecryptCandidate(ctx, reader, cfg, logger)
	if err != nil {
		return nil, nil, err
	}

	prepared, err := preparePlainBundle(ctx, reader, candidate, version, logger)
	if err != nil {
		return nil, nil, err
	}

	return candidate, prepared, nil
}

// sanitizeBundleEntryName ensures the tar entry name cannot escape the working directory.
func sanitizeBundleEntryName(name string) (string, error) {
	cleaned := path.Clean(strings.TrimSpace(name))
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

func extractBundleToWorkdir(bundlePath, workDir string) (stagedFiles, error) {
	file, err := restoreFS.Open(bundlePath)
	if err != nil {
		return stagedFiles{}, fmt.Errorf("open bundle: %w", err)
	}
	defer file.Close()

	tr := tar.NewReader(file)
	var staged stagedFiles

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
		out, err := restoreFS.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return stagedFiles{}, fmt.Errorf("extract %s: %w", hdr.Name, err)
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return stagedFiles{}, fmt.Errorf("write %s: %w", hdr.Name, err)
		}
		out.Close()

		switch {
		case strings.HasSuffix(target, ".metadata"):
			staged.MetadataPath = target
		case strings.HasSuffix(target, ".sha256"):
			staged.ChecksumPath = target
		default:
			staged.ArchivePath = target
		}
	}

	if staged.ArchivePath == "" || staged.MetadataPath == "" || staged.ChecksumPath == "" {
		return stagedFiles{}, fmt.Errorf("bundle missing required files")
	}
	return staged, nil
}

func copyRawArtifactsToWorkdir(cand *decryptCandidate, workDir string) (stagedFiles, error) {
	archiveDest := filepath.Join(workDir, filepath.Base(cand.RawArchivePath))
	if err := copyFile(restoreFS, cand.RawArchivePath, archiveDest); err != nil {
		return stagedFiles{}, fmt.Errorf("copy archive: %w", err)
	}
	metadataDest := filepath.Join(workDir, filepath.Base(cand.RawMetadataPath))
	if err := copyFile(restoreFS, cand.RawMetadataPath, metadataDest); err != nil {
		return stagedFiles{}, fmt.Errorf("copy metadata: %w", err)
	}
	checksumDest := filepath.Join(workDir, filepath.Base(cand.RawChecksumPath))
	if err := copyFile(restoreFS, cand.RawChecksumPath, checksumDest); err != nil {
		return stagedFiles{}, fmt.Errorf("copy checksum: %w", err)
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
		inputBytes, err := readPasswordWithContext(ctx)
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

		identity, err := parseIdentityInput(input)
		resetString(&input)
		if err != nil {
			logger.Warning("Invalid key/passphrase: %v", err)
			continue
		}

		if err := decryptWithIdentity(encryptedPath, outputPath, identity); err != nil {
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

func parseIdentityInput(input string) (age.Identity, error) {
	if strings.HasPrefix(strings.ToUpper(input), "AGE-SECRET-KEY-") {
		return age.ParseX25519Identity(strings.ToUpper(input))
	}
	return deriveDeterministicIdentityFromPassphrase(input)
}

func decryptWithIdentity(src, dst string, identity age.Identity) error {
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

	reader, err := age.Decrypt(in, identity)
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

func isLocalFilesystemPath(path string) bool {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return false
	}
	if strings.Contains(clean, ":") && !filepath.IsAbs(clean) {
		return false
	}
	return filepath.IsAbs(clean)
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

		input, err := readLineWithContext(ctx, reader)
		if err != nil {
			return "", err
		}
		switch strings.TrimSpace(input) {
		case "1":
			if err := restoreFS.Remove(current); err != nil {
				fmt.Printf("Failed to remove existing file: %v\n", err)
				continue
			}
			return current, nil
		case "2":
			fmt.Print("Enter new path: ")
			newPath, err := readLineWithContext(ctx, reader)
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
