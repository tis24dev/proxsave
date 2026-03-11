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

func resolveIntegrityExpectationValues(checksumFromFile, checksumFromManifest string) (*stagedIntegrityExpectation, error) {
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

	return resolveIntegrityExpectationValues(checksumFromFile, checksumFromManifest)
}

func resolveCandidateIntegrityExpectation(staged stagedFiles, cand *decryptCandidate) (*stagedIntegrityExpectation, error) {
	if cand != nil && cand.Integrity != nil && strings.TrimSpace(cand.Integrity.Checksum) != "" {
		normalized, err := backup.NormalizeChecksum(cand.Integrity.Checksum)
		if err != nil {
			return nil, fmt.Errorf("parse candidate checksum: %w", err)
		}
		expectation := &stagedIntegrityExpectation{
			Checksum: normalized,
			Source:   strings.TrimSpace(cand.Integrity.Source),
		}
		if expectation.Source == "" {
			expectation.Source = "candidate"
		}

		if strings.TrimSpace(staged.ChecksumPath) != "" {
			data, err := restoreFS.ReadFile(staged.ChecksumPath)
			if err != nil {
				return nil, fmt.Errorf("read checksum file: %w", err)
			}
			checksumFromFile, err := backup.ParseChecksumData(data)
			if err != nil {
				return nil, fmt.Errorf("parse checksum file %s: %w", staged.ChecksumPath, err)
			}
			if checksumFromFile != expectation.Checksum {
				return nil, fmt.Errorf("checksum mismatch between checksum file and selected candidate")
			}
		}

		return expectation, nil
	}

	var manifest *backup.Manifest
	if cand != nil {
		manifest = cand.Manifest
	}
	return resolveStagedIntegrityExpectation(staged, manifest)
}

func verifyStagedArchiveIntegrity(ctx context.Context, logger *logging.Logger, staged stagedFiles, cand *decryptCandidate) (string, error) {
	if staged.ArchivePath == "" {
		return "", fmt.Errorf("staged archive path is empty")
	}
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	expectation, err := resolveCandidateIntegrityExpectation(staged, cand)
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
