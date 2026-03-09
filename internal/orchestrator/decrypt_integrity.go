package orchestrator

import (
	"context"
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
)

type stagedIntegrityExpectation struct {
	Checksum string
	Source   string
}

func resolveStagedIntegrityExpectation(staged stagedFiles, manifest *backup.Manifest) (*stagedIntegrityExpectation, error) {
	var (
		checksumFromFile     string
		checksumFromManifest string
	)

	if strings.TrimSpace(staged.ChecksumPath) != "" {
		data, err := restoreFS.ReadFile(staged.ChecksumPath)
		if err != nil {
			return nil, fmt.Errorf("read checksum file: %w", err)
		}
		checksumFromFile, err = backup.ParseChecksumData(data)
		if err != nil {
			return nil, fmt.Errorf("parse checksum file %s: %w", staged.ChecksumPath, err)
		}
	}

	if manifest != nil && strings.TrimSpace(manifest.SHA256) != "" {
		normalized, err := backup.NormalizeChecksum(manifest.SHA256)
		if err != nil {
			return nil, fmt.Errorf("parse manifest checksum: %w", err)
		}
		checksumFromManifest = normalized
	}

	switch {
	case checksumFromFile != "" && checksumFromManifest != "" && checksumFromFile != checksumFromManifest:
		return nil, fmt.Errorf("checksum mismatch between checksum file and manifest")
	case checksumFromFile != "" && checksumFromManifest != "":
		return &stagedIntegrityExpectation{Checksum: checksumFromFile, Source: "checksum file and manifest"}, nil
	case checksumFromFile != "":
		return &stagedIntegrityExpectation{Checksum: checksumFromFile, Source: "checksum file"}, nil
	case checksumFromManifest != "":
		return &stagedIntegrityExpectation{Checksum: checksumFromManifest, Source: "manifest"}, nil
	default:
		return nil, fmt.Errorf("backup has no checksum verification available")
	}
}

func verifyStagedArchiveIntegrity(ctx context.Context, logger *logging.Logger, staged stagedFiles, manifest *backup.Manifest) (string, error) {
	if staged.ArchivePath == "" {
		return "", fmt.Errorf("staged archive path is empty")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	expectation, err := resolveStagedIntegrityExpectation(staged, manifest)
	if err != nil {
		return "", err
	}

	logger.Info("Verifying staged archive integrity using %s", expectation.Source)
	ok, err := backup.VerifyChecksum(ctx, logger, staged.ArchivePath, expectation.Checksum)
	if err != nil {
		return "", fmt.Errorf("verify staged archive: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("staged archive checksum mismatch")
	}
	return expectation.Checksum, nil
}
