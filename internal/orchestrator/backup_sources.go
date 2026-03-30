package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
)

// decryptPathOption describes a logical backup source (local, secondary, cloud)
// presented to the user during decrypt/restore workflows.
type decryptPathOption struct {
	Label    string
	Path     string
	IsRclone bool
}

// buildDecryptPathOptions builds the list of available backup sources
// (primary, secondary, cloud) from the loaded configuration.
func buildDecryptPathOptions(cfg *config.Config, logger *logging.Logger) (options []decryptPathOption) {
	if cfg == nil {
		logging.DebugStep(logger, "build backup source options", "skip (cfg=nil)")
		return nil
	}
	done := logging.DebugStart(logger, "build backup source options", "secondary_enabled=%v cloud_enabled=%v", cfg.SecondaryEnabled, cfg.CloudEnabled)
	defer func() { done(nil) }()
	options = make([]decryptPathOption, 0, 3)

	if clean := strings.TrimSpace(cfg.BackupPath); clean != "" {
		logging.DebugStep(logger, "build backup source options", "add local path=%q", clean)
		options = append(options, decryptPathOption{
			Label: "Local backups",
			Path:  clean,
		})
	} else {
		logging.DebugStep(logger, "build backup source options", "skip local (empty)")
	}

	if cfg.SecondaryEnabled {
		if clean := strings.TrimSpace(cfg.SecondaryPath); clean != "" {
			logging.DebugStep(logger, "build backup source options", "add secondary path=%q", clean)
			options = append(options, decryptPathOption{
				Label: "Secondary backups",
				Path:  clean,
			})
		} else {
			logging.DebugStep(logger, "build backup source options", "skip secondary (enabled but path empty)")
		}
	} else {
		logging.DebugStep(logger, "build backup source options", "skip secondary (disabled)")
	}

	if strings.TrimSpace(cfg.CloudRemote) != "" || strings.TrimSpace(cfg.CloudRemotePath) != "" {
		cloudRoot := buildCloudRemotePath(cfg.CloudRemote, cfg.CloudRemotePath)
		logging.DebugStep(logger, "build backup source options", "cloud root=%q", cloudRoot)
		if isRcloneRemote(cloudRoot) {
			options = append(options, decryptPathOption{
				Label:    "Cloud backups (rclone)",
				Path:     cloudRoot,
				IsRclone: true,
			})
		} else if isLocalFilesystemPath(cloudRoot) {
			options = append(options, decryptPathOption{
				Label:    "Cloud backups",
				Path:     cloudRoot,
				IsRclone: false,
			})
		} else {
			logging.DebugStep(logger, "build backup source options", "skip cloud (unrecognized root)")
		}
	} else {
		logging.DebugStep(logger, "build backup source options", "skip cloud (not configured)")
	}

	logging.DebugStep(logger, "build backup source options", "final options=%d", len(options))
	return options
}

// discoverRcloneBackups lists backup candidates from an rclone remote and returns
// backup candidates backed by that remote (bundles and raw archives).
func discoverRcloneBackups(ctx context.Context, cfg *config.Config, remotePath string, logger *logging.Logger, report ProgressReporter) (candidates []*backupCandidate, err error) {
	done := logging.DebugStart(logger, "discover rclone backups", "remote=%s", remotePath)
	defer func() { done(err) }()
	start := time.Now()

	timeout := 30 * time.Second
	if cfg != nil && cfg.RcloneTimeoutConnection > 0 {
		timeout = time.Duration(cfg.RcloneTimeoutConnection) * time.Second
	}
	logging.DebugStep(logger, "discover rclone backups", "per_command_timeout=%s", timeout)
	// Build full remote path - ensure it ends with ":" if it's just a remote name
	fullPath := strings.TrimSpace(remotePath)
	if !strings.Contains(fullPath, ":") {
		fullPath = fullPath + ":"
	}

	logging.DebugStep(logger, "discover rclone backups", "listing remote: %s", fullPath)
	logging.DebugStep(logger, "discover rclone backups", "filters=bundle.tar and raw .metadata")
	logDebug(logger, "Cloud (rclone): listing backups under %s", fullPath)
	logDebug(logger, "Cloud (rclone): executing: rclone lsf %s", fullPath)
	if report != nil {
		report(fmt.Sprintf("Listing cloud path: %s", fullPath))
	}

	// Use rclone lsf to list files inside the backup directory
	lsfCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(lsfCtx, "rclone", "lsf", fullPath)
	lsfStart := time.Now()
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(lsfCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("timed out while listing rclone remote %s (timeout=%s). Increase RCLONE_TIMEOUT_CONNECTION if needed: %w (output: %s)", fullPath, timeout, err, strings.TrimSpace(string(output)))
		}
		return nil, fmt.Errorf("failed to list rclone remote %s: %w (output: %s)", fullPath, err, string(output))
	}
	logging.DebugStep(logger, "discover rclone backups", "rclone lsf output bytes=%d elapsed=%s", len(output), time.Since(lsfStart))

	candidates = make([]*backupCandidate, 0)
	lines := strings.Split(string(output), "\n")

	totalEntries := len(lines)
	emptyEntries := 0
	nonCandidateEntries := 0
	manifestErrors := 0
	integrityMissing := 0
	logDebug(logger, "Cloud (rclone): scanned %d entries from rclone lsf output", totalEntries)

	snapshot := make(map[string]struct{}, len(lines))
	ordered := make([]string, 0, len(lines))
	for _, line := range lines {
		filename := strings.TrimSpace(line)
		if filename == "" {
			emptyEntries++
			continue
		}
		if _, ok := snapshot[filename]; ok {
			continue
		}
		snapshot[filename] = struct{}{}
		ordered = append(ordered, filename)
	}

	joinRemote := func(base, rel string) string {
		remoteFile := base
		if !strings.HasSuffix(remoteFile, ":") && !strings.HasSuffix(remoteFile, "/") {
			remoteFile += "/"
		}
		return remoteFile + rel
	}

	type inspectItem struct {
		kind           decryptSourceType
		filename       string
		remoteBundle   string
		remoteArchive  string
		remoteMetadata string
		remoteChecksum string
	}

	items := make([]inspectItem, 0)
	for _, filename := range ordered {
		switch {
		case strings.HasSuffix(filename, ".bundle.tar"):
			items = append(items, inspectItem{
				kind:         sourceBundle,
				filename:     filename,
				remoteBundle: joinRemote(fullPath, filename),
			})
		case strings.HasSuffix(filename, ".metadata"):
			archiveName := strings.TrimSuffix(filename, ".metadata")
			if !strings.Contains(archiveName, ".tar") {
				nonCandidateEntries++
				continue
			}
			if _, ok := snapshot[archiveName]; !ok {
				nonCandidateEntries++
				continue
			}

			remoteArchive := joinRemote(fullPath, archiveName)
			remoteMetadata := joinRemote(fullPath, filename)
			remoteChecksum := ""
			if _, ok := snapshot[archiveName+".sha256"]; ok {
				remoteChecksum = joinRemote(fullPath, archiveName+".sha256")
			}
			items = append(items, inspectItem{
				kind:           sourceRaw,
				filename:       filename,
				remoteArchive:  remoteArchive,
				remoteMetadata: remoteMetadata,
				remoteChecksum: remoteChecksum,
			})
		default:
			nonCandidateEntries++
		}
	}

	if report != nil {
		report(fmt.Sprintf("Inspecting %d candidate(s)...", len(items)))
	}

	for idx, item := range items {
		if report != nil {
			report(fmt.Sprintf("Inspecting %d/%d: %s", idx+1, len(items), item.filename))
		}

		switch item.kind {
		case sourceBundle:
			bundleCtx, cancel := context.WithTimeout(ctx, timeout)
			manifest, perr := inspectRcloneBundleManifest(bundleCtx, item.remoteBundle, logger)
			cancel()
			if perr != nil {
				if errors.Is(perr, context.DeadlineExceeded) {
					return nil, fmt.Errorf("timed out while inspecting %s (timeout=%s). Increase RCLONE_TIMEOUT_CONNECTION if needed: %w", item.filename, timeout, perr)
				}
				if errors.Is(perr, context.Canceled) {
					return nil, perr
				}
				manifestErrors++
				logWarning(logger, "Skipping rclone bundle %s: %v", item.filename, perr)
				continue
			}

			displayBase := filepath.Base(manifest.ArchivePath)
			if strings.TrimSpace(displayBase) == "" {
				displayBase = filepath.Base(item.filename)
			}
			candidates = append(candidates, &backupCandidate{
				Manifest:    manifest,
				Source:      sourceBundle,
				BundlePath:  item.remoteBundle,
				DisplayBase: displayBase,
				IsRclone:    true,
			})
			logDebug(logger, "Cloud (rclone): accepted backup bundle: %s", item.filename)

		case sourceRaw:
			manifestCtx, manifestCancel := context.WithTimeout(ctx, timeout)
			manifest, perr := inspectRcloneMetadataManifest(manifestCtx, item.remoteMetadata, item.remoteArchive, logger)
			manifestCancel()
			if perr != nil {
				if errors.Is(perr, context.DeadlineExceeded) {
					return nil, fmt.Errorf("timed out while inspecting %s (timeout=%s). Increase RCLONE_TIMEOUT_CONNECTION if needed: %w", item.filename, timeout, perr)
				}
				if errors.Is(perr, context.Canceled) {
					return nil, perr
				}
				manifestErrors++
				logWarning(logger, "Skipping rclone metadata %s: %v", item.filename, perr)
				continue
			}
			manifestChecksum, perr := normalizeCandidateManifestChecksum(manifest)
			if perr != nil {
				integrityMissing++
				logWarning(logger, "Skipping rclone backup %s: invalid manifest checksum: %v", item.filename, perr)
				continue
			}
			checksumFromFile := ""
			if item.remoteChecksum != "" {
				checksumCtx, checksumCancel := context.WithTimeout(ctx, timeout)
				checksumFromFile, perr = inspectRcloneChecksumFile(checksumCtx, item.remoteChecksum, logger)
				checksumCancel()
				if perr != nil {
					if errors.Is(perr, context.DeadlineExceeded) {
						return nil, fmt.Errorf("timed out while inspecting %s checksum (timeout=%s). Increase RCLONE_TIMEOUT_CONNECTION if needed: %w", item.filename, timeout, perr)
					}
					if errors.Is(perr, context.Canceled) {
						return nil, perr
					}
					integrityMissing++
					logWarning(logger, "Skipping rclone backup %s: invalid checksum file: %v", item.filename, perr)
					continue
				}
			}
			expectation, perr := resolveIntegrityExpectationValues(checksumFromFile, manifestChecksum)
			if perr != nil {
				integrityMissing++
				logWarning(logger, "Skipping rclone backup %s: %v", item.filename, perr)
				continue
			}
			displayBase := filepath.Base(manifest.ArchivePath)
			if strings.TrimSpace(displayBase) == "" {
				displayBase = filepath.Base(baseNameFromRemoteRef(item.remoteArchive))
			}
			candidates = append(candidates, &backupCandidate{
				Manifest:        manifest,
				Source:          sourceRaw,
				RawArchivePath:  item.remoteArchive,
				RawMetadataPath: item.remoteMetadata,
				RawChecksumPath: item.remoteChecksum,
				Integrity:       expectation,
				DisplayBase:     displayBase,
				IsRclone:        true,
			})
		default:
			continue
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
		if a == nil || a.Manifest == nil {
			return false
		}
		if b == nil || b.Manifest == nil {
			return true
		}
		if !a.Manifest.CreatedAt.Equal(b.Manifest.CreatedAt) {
			return a.Manifest.CreatedAt.After(b.Manifest.CreatedAt)
		}
		return a.DisplayBase < b.DisplayBase
	})

	logging.DebugStep(
		logger,
		"discover rclone backups",
		"summary entries=%d empty=%d non_candidate=%d manifest_errors=%d integrity_missing=%d accepted=%d elapsed=%s",
		totalEntries,
		emptyEntries,
		nonCandidateEntries,
		manifestErrors,
		integrityMissing,
		len(candidates),
		time.Since(start),
	)
	logDebug(logger, "Cloud (rclone): scanned %d entries, found %d valid backup candidate(s)", len(lines), len(candidates))
	logDebug(logger, "Cloud (rclone): discovered %d bundle candidate(s) in %s", len(candidates), fullPath)

	if manifestErrors > 0 {
		if len(candidates) > 0 {
			logWarning(logger, "Cloud scan summary: %d usable backup(s), %d candidate(s) skipped due to manifest/metadata errors (see warnings above)", len(candidates), manifestErrors)
		} else if len(items) > 0 {
			return nil, fmt.Errorf("no usable cloud backups found under %s: %d candidate(s) skipped due to manifest/metadata read errors (timeout=%s). This can happen with slow remotes, rclone failures, or older bundle layouts where metadata is not stored at the beginning. Consider creating a fresh backup or increasing RCLONE_TIMEOUT_CONNECTION; see warnings above for details", fullPath, manifestErrors, timeout)
		}
	}

	return candidates, nil
}

// discoverBackupCandidates scans a local or mounted directory for backup
// candidates (bundle or raw triplet: archive + metadata + checksum).
func discoverBackupCandidates(logger *logging.Logger, root string) (candidates []*backupCandidate, err error) {
	done := logging.DebugStart(logger, "discover backup candidates", "root=%s", root)
	defer func() { done(err) }()
	entries, err := restoreFS.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", root, err)
	}
	logging.DebugStep(logger, "discover backup candidates", "entries=%d", len(entries))

	candidates = make([]*backupCandidate, 0)
	rawBases := make(map[string]struct{})
	filesSeen := 0
	dirsSkipped := 0
	bundleSeen := 0
	bundleManifestErrors := 0
	metadataSeen := 0
	metadataDuplicate := 0
	metadataMissingArchive := 0
	metadataManifestErrors := 0
	checksumMissing := 0
	integrityUnavailable := 0

	for _, entry := range entries {
		if entry.IsDir() {
			dirsSkipped++
			continue
		}
		filesSeen++
		name := entry.Name()
		fullPath := filepath.Join(root, name)

		switch {
		case strings.HasSuffix(name, ".bundle.tar"):
			bundleSeen++
			logging.DebugStep(logger, "discover backup candidates", "inspect bundle manifest: %s", name)
			manifest, err := inspectBundleManifest(fullPath)
			if err != nil {
				bundleManifestErrors++
				logWarning(logger, "Skipping bundle %s: %v", name, err)
				continue
			}
			logging.DebugStep(logger, "discover backup candidates", "bundle accepted: %s created_at=%s", name, manifest.CreatedAt.Format(time.RFC3339))
			candidates = append(candidates, &backupCandidate{
				Manifest:    manifest,
				Source:      sourceBundle,
				BundlePath:  fullPath,
				DisplayBase: filepath.Base(manifest.ArchivePath),
			})
		case strings.HasSuffix(name, ".metadata"):
			metadataSeen++
			baseName := strings.TrimSuffix(name, ".metadata")
			if _, ok := rawBases[baseName]; ok {
				metadataDuplicate++
				continue
			}
			archivePath := filepath.Join(root, baseName)
			if _, err := restoreFS.Stat(archivePath); err != nil {
				metadataMissingArchive++
				logging.DebugStep(logger, "discover backup candidates", "skip metadata %s (missing archive %s)", name, baseName)
				continue
			}
			checksumPath := archivePath + ".sha256"
			hasChecksum := true
			if _, err := restoreFS.Stat(checksumPath); err != nil {
				// Checksum missing - allow but warn
				checksumMissing++
				logWarning(logger, "Backup %s is missing .sha256 checksum file", baseName)
				checksumPath = ""
				hasChecksum = false
			}
			logging.DebugStep(logger, "discover backup candidates", "load manifest: %s", name)
			manifest, err := backup.LoadManifest(fullPath)
			if err != nil {
				metadataManifestErrors++
				logWarning(logger, "Skipping metadata %s: %v", name, err)
				continue
			}
			manifestChecksum, err := normalizeCandidateManifestChecksum(manifest)
			if err != nil {
				integrityUnavailable++
				logWarning(logger, "Skipping backup %s: invalid manifest checksum: %v", baseName, err)
				continue
			}
			checksumFromFile := ""
			if hasChecksum {
				checksumFromFile, err = parseLocalChecksumFile(checksumPath)
				if err != nil {
					integrityUnavailable++
					logWarning(logger, "Skipping backup %s: invalid checksum file: %v", baseName, err)
					continue
				}
			}
			expectation, err := resolveIntegrityExpectationValues(checksumFromFile, manifestChecksum)
			if err != nil {
				integrityUnavailable++
				logWarning(logger, "Skipping backup %s: %v", baseName, err)
				continue
			}

			rawBases[baseName] = struct{}{}
			candidates = append(candidates, &backupCandidate{
				Manifest:        manifest,
				Source:          sourceRaw,
				RawArchivePath:  archivePath,
				RawMetadataPath: fullPath,
				RawChecksumPath: checksumPath,
				Integrity:       expectation,
				DisplayBase:     filepath.Base(manifest.ArchivePath),
			})
			logging.DebugStep(logger, "discover backup candidates", "raw candidate accepted: %s created_at=%s", name, manifest.CreatedAt.Format(time.RFC3339))
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Manifest.CreatedAt.After(candidates[j].Manifest.CreatedAt)
	})

	logging.DebugStep(
		logger,
		"discover backup candidates",
		"summary entries=%d files=%d dirs=%d bundles=%d bundle_manifest_errors=%d metadata=%d metadata_duplicate=%d metadata_missing_archive=%d metadata_manifest_errors=%d checksum_missing=%d integrity_unavailable=%d candidates=%d",
		len(entries),
		filesSeen,
		dirsSkipped,
		bundleSeen,
		bundleManifestErrors,
		metadataSeen,
		metadataDuplicate,
		metadataMissingArchive,
		metadataManifestErrors,
		checksumMissing,
		integrityUnavailable,
		len(candidates),
	)
	return candidates, nil
}

func normalizeCandidateManifestChecksum(manifest *backup.Manifest) (string, error) {
	if manifest == nil || strings.TrimSpace(manifest.SHA256) == "" {
		return "", nil
	}
	normalized, err := backup.NormalizeChecksum(manifest.SHA256)
	if err != nil {
		return "", err
	}
	manifest.SHA256 = normalized
	return normalized, nil
}

const checksumFileReadLimit = 4 * 1024

func readBoundedChecksumLine(reader io.Reader) ([]byte, bool, error) {
	limited := io.LimitReader(reader, checksumFileReadLimit+1)
	line, err := bufio.NewReaderSize(limited, checksumFileReadLimit+1).ReadSlice('\n')
	if err == nil {
		return append([]byte(nil), line...), true, nil
	}
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > checksumFileReadLimit {
		return nil, true, fmt.Errorf("checksum file exceeds %d bytes before newline", checksumFileReadLimit)
	}
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return nil, false, fmt.Errorf("checksum file is empty")
		}
		return append([]byte(nil), line...), false, nil
	}
	return nil, false, err
}

func parseLocalChecksumFile(checksumPath string) (string, error) {
	file, err := restoreFS.Open(checksumPath)
	if err != nil {
		return "", fmt.Errorf("read checksum file %s: %w", checksumPath, err)
	}
	defer file.Close()

	data, _, err := readBoundedChecksumLine(file)
	if err != nil {
		return "", fmt.Errorf("read checksum file %s: %w", checksumPath, err)
	}
	checksum, err := backup.ParseChecksumData(data)
	if err != nil {
		return "", fmt.Errorf("parse checksum file %s: %w", checksumPath, err)
	}
	return checksum, nil
}

func inspectRcloneChecksumFile(ctx context.Context, remotePath string, logger *logging.Logger) (checksum string, err error) {
	done := logging.DebugStart(logger, "inspect rclone checksum", "remote=%s", remotePath)
	defer func() { done(err) }()
	logging.DebugStep(logger, "inspect rclone checksum", "executing: rclone cat %s", remotePath)

	cmd := exec.CommandContext(ctx, "rclone", "cat", remotePath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("start rclone cat %s: %w", remotePath, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start rclone cat %s: %w", remotePath, err)
	}

	data, closedEarly, readErr := readBoundedChecksumLine(stdout)
	if closedEarly {
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	stderrOutput := strings.TrimSpace(stderr.String())
	ignoreWaitErr := closedEarly && stderrOutput == ""
	if readErr != nil {
		if waitErr != nil && !ignoreWaitErr {
			return "", fmt.Errorf("rclone cat %s failed: %w (output: %s)", remotePath, waitErr, stderrOutput)
		}
		return "", fmt.Errorf("read checksum file %s: %w", remotePath, readErr)
	}
	if waitErr != nil && !ignoreWaitErr {
		return "", fmt.Errorf("rclone cat %s failed: %w (output: %s)", remotePath, waitErr, stderrOutput)
	}
	checksum, err = backup.ParseChecksumData(data)
	if err != nil {
		return "", fmt.Errorf("parse checksum file %s: %w", remotePath, err)
	}
	return checksum, nil
}

// isLocalFilesystemPath returns true if the given value represents an absolute
// local filesystem path (and not an rclone-style "remote:path" reference).
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

// isRcloneRemote checks if a path is an rclone remote (contains ":" but is not
// an absolute filesystem path).
func isRcloneRemote(path string) bool {
	clean := strings.TrimSpace(path)
	// Rclone remotes contain ":" and are not absolute filesystem paths
	return clean != "" &&
		strings.Contains(clean, ":") &&
		!filepath.IsAbs(clean)
}

// removeDecryptPathOption removes the first occurrence of target from options,
// matching on label, path and IsRclone flag. If not found, it returns options unchanged.
func removeDecryptPathOption(options []decryptPathOption, target decryptPathOption) []decryptPathOption {
	for i, opt := range options {
		if opt.Label == target.Label && opt.Path == target.Path && opt.IsRclone == target.IsRclone {
			return append(options[:i], options[i+1:]...)
		}
	}
	return options
}

// buildCloudRemotePath combines CLOUD_REMOTE and CLOUD_REMOTE_PATH into a single
// reference, matching the semantics used by the cloud storage backend.
//
// Examples:
//
//	CLOUD_REMOTE=gdrive:pbs-backups, CLOUD_REMOTE_PATH=server1         -> gdrive:pbs-backups/server1
//	CLOUD_REMOTE=gdrive, CLOUD_REMOTE_PATH=pbs-backups/server1         -> gdrive:pbs-backups/server1
//	CLOUD_REMOTE=gdrive:pbs, CLOUD_REMOTE_PATH=                        -> gdrive:pbs
//	CLOUD_REMOTE=/mnt/cloud/backups, CLOUD_REMOTE_PATH=server1         -> /mnt/cloud/backups/server1
func buildCloudRemotePath(cloudRemote, cloudRemotePath string) string {
	base := strings.TrimSpace(cloudRemote)
	if base == "" {
		return ""
	}

	// If CLOUD_REMOTE is an absolute filesystem path (mount point),
	// treat it as a local directory and combine using filepath.Join.
	if filepath.IsAbs(base) && !strings.Contains(base, ":") {
		prefix := strings.Trim(strings.TrimSpace(cloudRemotePath), "/")
		if prefix == "" {
			return filepath.Clean(base)
		}
		return filepath.Join(base, prefix)
	}

	parts := strings.SplitN(base, ":", 2)
	remoteName := parts[0]
	basePath := ""
	if len(parts) == 2 {
		basePath = strings.Trim(strings.TrimSpace(parts[1]), "/")
	}

	userPrefix := strings.Trim(strings.TrimSpace(cloudRemotePath), "/")
	fullPath := strings.Trim(path.Join(basePath, userPrefix), "/")

	if fullPath == "" {
		return remoteName + ":"
	}
	return fmt.Sprintf("%s:%s", remoteName, fullPath)
}

func logDebug(logger *logging.Logger, format string, args ...interface{}) {
	if logger == nil {
		return
	}
	logger.Debug(format, args...)
}

func logWarning(logger *logging.Logger, format string, args ...interface{}) {
	if logger == nil {
		return
	}
	logger.Warning(format, args...)
}
