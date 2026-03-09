package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

type archiveDecryptFunc func(ctx context.Context, encryptedPath, outputPath, displayName string) error

func preparePlainBundleCommon(ctx context.Context, cand *decryptCandidate, version string, logger *logging.Logger, decryptArchive archiveDecryptFunc) (bundle *preparedBundle, err error) {
	if cand == nil || cand.Manifest == nil {
		return nil, fmt.Errorf("invalid backup candidate")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	var rcloneCleanup func()
	if cand.IsRclone && cand.Source == sourceBundle {
		logger.Debug("Detected rclone backup, downloading...")
		localPath, cleanup, err := downloadRcloneBackup(ctx, cand.BundlePath, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to download rclone backup: %w", err)
		}
		rcloneCleanup = cleanup
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

	sourceChecksum, err := verifyStagedArchiveIntegrity(ctx, logger, staged, cand.Manifest)
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
		if decryptArchive == nil {
			cleanup()
			return nil, fmt.Errorf("decrypt function not available")
		}
		displayName := cand.DisplayBase
		if strings.TrimSpace(displayName) == "" {
			displayName = filepath.Base(manifestCopy.ArchivePath)
		}
		if err := decryptArchive(ctx, staged.ArchivePath, plainArchivePath, displayName); err != nil {
			cleanup()
			return nil, err
		}
	} else if staged.ArchivePath != plainArchivePath {
		if err := copyFile(restoreFS, staged.ArchivePath, plainArchivePath); err != nil {
			cleanup()
			return nil, fmt.Errorf("copy archive: %w", err)
		}
	}

	archiveInfo, err := restoreFS.Stat(plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("stat decrypted archive: %w", err)
	}

	plainChecksum, err := backup.GenerateChecksum(ctx, logger, plainArchivePath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("generate checksum: %w", err)
	}

	manifestCopy.ArchivePath = plainArchivePath
	manifestCopy.ArchiveSize = archiveInfo.Size()
	manifestCopy.SHA256 = plainChecksum
	manifestCopy.EncryptionMode = "none"
	if version != "" {
		manifestCopy.ScriptVersion = version
	}

	return &preparedBundle{
		ArchivePath:    plainArchivePath,
		Manifest:       manifestCopy,
		Checksum:       plainChecksum,
		SourceChecksum: sourceChecksum,
		cleanup:        cleanup,
	}, nil
}
