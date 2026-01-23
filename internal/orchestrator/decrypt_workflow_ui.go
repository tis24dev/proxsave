package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/input"
	"github.com/tis24dev/proxsave/internal/logging"
)

func selectBackupCandidateWithUI(ctx context.Context, ui BackupSelectionUI, cfg *config.Config, logger *logging.Logger, requireEncrypted bool) (candidate *decryptCandidate, err error) {
	done := logging.DebugStart(logger, "select backup candidate (ui)", "requireEncrypted=%v", requireEncrypted)
	defer func() { done(err) }()

	pathOptions := buildDecryptPathOptions(cfg, logger)
	if len(pathOptions) == 0 {
		return nil, fmt.Errorf("no backup paths configured in backup.env")
	}

	for {
		option, err := ui.SelectBackupSource(ctx, pathOptions)
		if err != nil {
			return nil, err
		}

		logger.Info("Scanning %s for backups...", option.Path)

		var candidates []*decryptCandidate
		scanErr := ui.RunTask(ctx, "Scanning backups", "Scanning backup source...", func(scanCtx context.Context, report ProgressReporter) error {
			if option.IsRclone {
				found, err := discoverRcloneBackups(scanCtx, cfg, option.Path, logger, report)
				if err != nil {
					return err
				}
				candidates = found
				return nil
			}

			if report != nil {
				report(fmt.Sprintf("Listing local path: %s", option.Path))
			}
			found, err := discoverBackupCandidates(logger, option.Path)
			if err != nil {
				return err
			}
			candidates = found
			return nil
		})

		if scanErr != nil {
			logger.Warning("Failed to inspect %s: %v", option.Path, scanErr)
			_ = ui.ShowError(ctx, "Backup scan failed", fmt.Sprintf("Failed to inspect %s: %v", option.Path, scanErr))
			if option.IsRclone {
				// For rclone remotes, persistent failures are unlikely to self-heal,
				// so remove the option to avoid a broken loop.
				pathOptions = removeDecryptPathOption(pathOptions, option)
				if len(pathOptions) == 0 {
					return nil, fmt.Errorf("no usable backup sources available")
				}
			}
			continue
		}

		if len(candidates) == 0 {
			logger.Warning("No backups found in %s", option.Path)
			_ = ui.ShowError(ctx, "No backups found", fmt.Sprintf("No backups found in %s.", option.Path))
			pathOptions = removeDecryptPathOption(pathOptions, option)
			if len(pathOptions) == 0 {
				return nil, fmt.Errorf("no usable backup sources available")
			}
			continue
		}

		if requireEncrypted {
			encrypted := filterEncryptedCandidates(candidates)
			if len(encrypted) == 0 {
				logger.Warning("No encrypted backups found in %s", option.Path)
				_ = ui.ShowError(ctx, "No encrypted backups", fmt.Sprintf("No encrypted backups found in %s.", option.Path))
				pathOptions = removeDecryptPathOption(pathOptions, option)
				if len(pathOptions) == 0 {
					return nil, fmt.Errorf("no usable backup sources available")
				}
				continue
			}
			candidates = encrypted
		}

		candidate, err = ui.SelectBackupCandidate(ctx, candidates)
		if err != nil {
			return nil, err
		}
		return candidate, nil
	}
}

func ensureWritablePathWithUI(ctx context.Context, ui DecryptWorkflowUI, targetPath, description string) (string, error) {
	current := filepath.Clean(targetPath)
	failure := ""

	for {
		if _, err := restoreFS.Stat(current); errors.Is(err, os.ErrNotExist) {
			return current, nil
		} else if err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("stat %s: %w", current, err)
		}

		decision, newPath, err := ui.ResolveExistingPath(ctx, current, description, failure)
		if err != nil {
			return "", err
		}

		switch decision {
		case PathDecisionOverwrite:
			if err := restoreFS.Remove(current); err != nil {
				failure = fmt.Sprintf("Failed to remove existing %s: %v", description, err)
				continue
			}
			return current, nil
		case PathDecisionNewPath:
			trimmed := strings.TrimSpace(newPath)
			if trimmed == "" {
				failure = fmt.Sprintf("New %s path cannot be empty", description)
				continue
			}
			current = filepath.Clean(trimmed)
			failure = ""
		default:
			return "", ErrDecryptAborted
		}
	}
}

func decryptArchiveWithSecretPrompt(ctx context.Context, encryptedPath, outputPath, displayName string, logger *logging.Logger, prompt func(ctx context.Context, displayName, previousError string) (string, error)) error {
	promptError := ""
	for {
		secret, err := prompt(ctx, displayName, promptError)
		if err != nil {
			if errors.Is(err, ErrDecryptAborted) || errors.Is(err, input.ErrInputAborted) {
				return ErrDecryptAborted
			}
			return err
		}

		secret = strings.TrimSpace(secret)
		if secret == "" {
			resetString(&secret)
			promptError = "Input cannot be empty."
			continue
		}

		identities, err := parseIdentityInput(secret)
		resetString(&secret)
		if err != nil {
			promptError = fmt.Sprintf("Invalid key or passphrase: %v", err)
			continue
		}

		if err := decryptWithIdentity(encryptedPath, outputPath, identities...); err != nil {
			var noMatch *age.NoIdentityMatchError
			if errors.Is(err, age.ErrIncorrectIdentity) || errors.As(err, &noMatch) {
				promptError = "Provided key or passphrase does not match this archive."
				continue
			}
			return err
		}
		return nil
	}
}

func preparePlainBundleWithUI(ctx context.Context, cand *decryptCandidate, version string, logger *logging.Logger, ui interface {
	PromptDecryptSecret(ctx context.Context, displayName, previousError string) (string, error)
}) (bundle *preparedBundle, err error) {
	done := logging.DebugStart(logger, "prepare plain bundle (ui)", "source=%v rclone=%v", cand.Source, cand.IsRclone)
	defer func() { done(err) }()

	if cand == nil || cand.Manifest == nil {
		return nil, fmt.Errorf("invalid backup candidate")
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

	manifestCopy := *cand.Manifest
	currentEncryption := strings.ToLower(manifestCopy.EncryptionMode)
	logger.Info("Preparing archive %s for decryption (mode: %s)", manifestCopy.ArchivePath, statusFromManifest(&manifestCopy))

	plainArchiveName := strings.TrimSuffix(filepath.Base(staged.ArchivePath), ".age")
	plainArchivePath := filepath.Join(workDir, plainArchiveName)

	if currentEncryption == "age" {
		displayName := cand.DisplayBase
		if strings.TrimSpace(displayName) == "" {
			displayName = filepath.Base(manifestCopy.ArchivePath)
		}
		if err := decryptArchiveWithSecretPrompt(ctx, staged.ArchivePath, plainArchivePath, displayName, logger, ui.PromptDecryptSecret); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		if staged.ArchivePath != plainArchivePath {
			if err := copyFile(restoreFS, staged.ArchivePath, plainArchivePath); err != nil {
				cleanup()
				return nil, fmt.Errorf("copy archive: %w", err)
			}
		}
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

	bundle = &preparedBundle{
		ArchivePath: plainArchivePath,
		Manifest:    manifestCopy,
		Checksum:    checksum,
		cleanup:     cleanup,
	}
	return bundle, nil
}

func runDecryptWorkflowWithUI(ctx context.Context, cfg *config.Config, logger *logging.Logger, version string, ui DecryptWorkflowUI) (err error) {
	if cfg == nil {
		return fmt.Errorf("configuration not available")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}
	done := logging.DebugStart(logger, "decrypt workflow (ui)", "version=%s", version)
	defer func() { done(err) }()
	defer func() {
		if err == nil {
			return
		}
		if errors.Is(err, input.ErrInputAborted) || errors.Is(err, context.Canceled) {
			err = ErrDecryptAborted
		}
	}()

	candidate, err := selectBackupCandidateWithUI(ctx, ui, cfg, logger, true)
	if err != nil {
		return err
	}

	prepared, err := preparePlainBundleWithUI(ctx, candidate, version, logger, ui)
	if err != nil {
		return err
	}
	defer prepared.Cleanup()

	defaultDir := "./decrypt"
	if strings.TrimSpace(cfg.BaseDir) != "" {
		defaultDir = filepath.Join(strings.TrimSpace(cfg.BaseDir), "decrypt")
	}
	destDir, err := ui.PromptDestinationDir(ctx, defaultDir)
	if err != nil {
		return err
	}

	if err := restoreFS.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	destDir, _ = filepath.Abs(destDir)
	logger.Info("Destination directory: %s", destDir)

	destArchivePath := filepath.Join(destDir, filepath.Base(prepared.ArchivePath))
	destArchivePath, err = ensureWritablePathWithUI(ctx, ui, destArchivePath, "decrypted archive")
	if err != nil {
		return err
	}

	workDir := filepath.Dir(prepared.ArchivePath)
	archiveBase := filepath.Base(destArchivePath)
	tempArchivePath := filepath.Join(workDir, archiveBase)
	if tempArchivePath != prepared.ArchivePath {
		if err := moveFileSafe(prepared.ArchivePath, tempArchivePath); err != nil {
			return fmt.Errorf("move decrypted archive within temp dir: %w", err)
		}
	}

	manifestCopy := prepared.Manifest
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

	logicalBundlePath := destArchivePath + ".bundle.tar"
	targetBundlePath := strings.TrimSuffix(logicalBundlePath, ".bundle.tar") + ".decrypted.bundle.tar"
	targetBundlePath, err = ensureWritablePathWithUI(ctx, ui, targetBundlePath, "decrypted bundle")
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
