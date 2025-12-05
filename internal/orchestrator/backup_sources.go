package orchestrator

import (
	"context"
	"fmt"
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

	if cfg.CloudEnabled {
		cloudRoot := buildCloudRemotePath(cfg.CloudRemote, cfg.CloudRemotePath)
		if isRcloneRemote(cloudRoot) {
			// rclone remote (remote:path[/prefix])
			// Pre-scan: verify backups exist before adding option
			scanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			candidates, err := discoverRcloneBackups(scanCtx, cloudRoot, nil)
			if err == nil && len(candidates) > 0 {
				options = append(options, decryptPathOption{
					Label:    "Cloud backups (rclone)",
					Path:     cloudRoot,
					IsRclone: true,
				})
			}
		} else if isLocalFilesystemPath(cloudRoot) {
			// Local filesystem mount
			// Pre-scan: verify backups exist before adding option
			candidates, err := discoverBackupCandidates(nil, cloudRoot)
			if err == nil && len(candidates) > 0 {
				options = append(options, decryptPathOption{
					Label:    "Cloud backups",
					Path:     cloudRoot,
					IsRclone: false,
				})
			}
		}
	}

	return options
}

// discoverRcloneBackups lists backup bundles from an rclone remote and returns
// decrypt candidates backed by that remote.
func discoverRcloneBackups(ctx context.Context, remotePath string, logger *logging.Logger) ([]*decryptCandidate, error) {
	// Build full remote path - ensure it ends with ":" if it's just a remote name
	fullPath := strings.TrimSpace(remotePath)
	if !strings.Contains(fullPath, ":") {
		fullPath = fullPath + ":"
	}

	if logger != nil {
		logger.Debug("Cloud (rclone): listing backups under %s", fullPath)
		logger.Debug("Cloud (rclone): executing: rclone lsf %s", fullPath)
	}

	// Use rclone lsf to list files inside the backup directory
	cmd := exec.CommandContext(ctx, "rclone", "lsf", fullPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list rclone remote %s: %w (output: %s)", fullPath, err, string(output))
	}

	candidates := make([]*decryptCandidate, 0)
	lines := strings.Split(string(output), "\n")

	if logger != nil {
		logger.Debug("Cloud (rclone): scanned %d entries from rclone lsf output", len(lines))
	}

	for _, line := range lines {
		filename := strings.TrimSpace(line)
		if filename == "" {
			continue
		}

		// Only process bundle files (both plain and age-encrypted)
		// Valid patterns:
		//   - *.tar.{gz|xz|zst}.bundle.tar       (plain bundle)
		//   - *.tar.{gz|xz|zst}.age.bundle.tar   (age-encrypted bundle)
		if !strings.Contains(filename, ".bundle.tar") {
			continue
		}

		// Must contain backup indicator in filename
		isBackup := strings.Contains(filename, "-backup-") || strings.HasPrefix(filename, "proxmox-backup-")
		if !isBackup {
			if logger != nil {
				logger.Debug("Skipping non-backup bundle: %s", filename)
			}
			continue
		}

		// Join root reference and filename with a single separator.
		remoteFile := fullPath
		if !strings.HasSuffix(remoteFile, ":") && !strings.HasSuffix(remoteFile, "/") {
			remoteFile += "/"
		}
		remoteFile += filename

		manifest, err := inspectRcloneBundleManifest(ctx, remoteFile, logger)
		if err != nil {
			logger.Warning("Skipping rclone bundle %s: %v", filename, err)
			continue
		}

		candidates = append(candidates, &decryptCandidate{
			Manifest:    manifest,
			Source:      sourceBundle,
			BundlePath:  remoteFile,
			DisplayBase: filepath.Base(manifest.ArchivePath),
			IsRclone:    true,
		})
		if logger != nil {
			logger.Debug("Cloud (rclone): accepted backup bundle: %s", filename)
		}
	}

	if logger != nil {
		logger.Debug("Cloud (rclone): scanned %d files, found %d valid backup bundles", len(lines), len(candidates))
		logger.Debug("Cloud (rclone): discovered %d bundle candidate(s) in %s", len(candidates), fullPath)
	}

	return candidates, nil
}

// discoverBackupCandidates scans a local or mounted directory for backup
// candidates (bundle or raw triplet: archive + metadata + checksum).
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
