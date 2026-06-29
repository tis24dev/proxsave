package backup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// Manifest represents backup archive metadata with checksums
type Manifest struct {
	ArchivePath      string    `json:"archive_path"`
	ArchiveSize      int64     `json:"archive_size"`
	SHA256           string    `json:"sha256"`
	CreatedAt        time.Time `json:"created_at"`
	CompressionType  string    `json:"compression_type"`
	CompressionLevel int       `json:"compression_level"`
	CompressionMode  string    `json:"compression_mode,omitempty"`
	ProxmoxType      string    `json:"proxmox_type"`
	ProxmoxTargets   []string  `json:"proxmox_targets,omitempty"`
	ProxmoxVersion   string    `json:"proxmox_version,omitempty"`
	PVEVersion       string    `json:"pve_version,omitempty"`
	PBSVersion       string    `json:"pbs_version,omitempty"`
	Hostname         string    `json:"hostname"`
	ScriptVersion    string    `json:"script_version,omitempty"`
	EncryptionMode   string    `json:"encryption_mode,omitempty"`
	ClusterMode      string    `json:"cluster_mode,omitempty"`
	// PassphraseSalt is the per-installation random salt used to derive a
	// passphrase-based AGE recipient. It is a public value embedded so the
	// archive stays decryptable from the passphrase alone on any host. Empty
	// for X25519/SSH recipients and for legacy archives (which used a fixed salt).
	PassphraseSalt string `json:"passphrase_salt,omitempty"`
}

// NormalizeChecksum validates and normalizes a SHA256 checksum string.
func NormalizeChecksum(value string) (string, error) {
	checksum := strings.ToLower(strings.TrimSpace(value))
	if checksum == "" {
		return "", fmt.Errorf("checksum is empty")
	}
	if len(checksum) != sha256.Size*2 {
		return "", fmt.Errorf("checksum must be %d hex characters, got %d", sha256.Size*2, len(checksum))
	}
	if _, err := hex.DecodeString(checksum); err != nil {
		return "", fmt.Errorf("checksum is not valid hex: %w", err)
	}
	return checksum, nil
}

// ParseChecksumData extracts a SHA256 checksum from checksum file contents.
func ParseChecksumData(data []byte) (string, error) {
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("checksum file is empty")
	}
	return NormalizeChecksum(fields[0])
}

// checksumOpen is the file-open seam for the checksum path; tests inject
// blocking/slow/counting doubles. *os.File satisfies io.ReadCloser.
var checksumOpen = func(ctx context.Context, path string, timeout time.Duration) (io.ReadCloser, error) {
	return safefs.Open(ctx, path, timeout)
}

// GenerateChecksum calculates the SHA256 checksum of a file with NO FS I/O
// timeout (the FS_IO_TIMEOUT=0 opt-out / legacy behaviour). Prefer
// GenerateChecksumBounded with the configured FS_IO_TIMEOUT on any path that
// may touch a removable or network mount.
func GenerateChecksum(ctx context.Context, logger *logging.Logger, filePath string) (string, error) {
	return GenerateChecksumBounded(ctx, logger, filePath, 0)
}

// GenerateChecksumBounded is GenerateChecksum with a per-chunk no-progress
// (stall) budget on the open, every read+hash chunk, and the close, so a
// dead/stale mount cannot wedge any of them in an uninterruptible (D-state)
// syscall. The budget re-arms on every chunk that makes progress, so a healthy
// large file over a slow-but-alive link still hashes in full. timeout<=0 disables
// bounding (a plain synchronous read loop, identical to the legacy behaviour).
// On a stalled read the safefs.CopyBounded worker is abandoned while still
// holding the fd, so the close is skipped and the *os.File finalizer reclaims the
// fd once the wedged kernel call returns.
func GenerateChecksumBounded(ctx context.Context, logger *logging.Logger, filePath string, timeout time.Duration) (checksum string, err error) {
	logger.Debug("Generating SHA256 checksum for: %s", filePath)

	// Never open on an already-cancelled context (nothing to leak), and preserve
	// the unwrapped context error identity callers assert on.
	if cerr := ctx.Err(); cerr != nil {
		return "", cerr
	}

	file, err := checksumOpen(ctx, filePath, timeout)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer func() {
		// A worker abandoned mid-read may still hold this fd: do not close it
		// (the close could itself re-wedge on a dead mount). Drop the reference
		// and let the os.File finalizer reclaim the fd once the call returns.
		if file == nil || safefs.IsAbandoned(err) {
			return
		}
		// A close that is itself abandoned (ctx cancel / timeout short-circuits
		// safefs.Run before Close runs) must not clobber an already-computed
		// checksum: the hash succeeded, the fd is reclaimed by the finalizer.
		if _, closeErr := safefs.Run(ctx, "close checksum source file", filePath, timeout, func() (struct{}, error) {
			return struct{}{}, file.Close()
		}); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) && !safefs.IsAbandoned(closeErr) && err == nil {
			err = fmt.Errorf("close checksum source file: %w", closeErr)
		}
	}()

	hash := sha256.New() // fresh per call; never pooled/shared (abandon-safe as CopyBounded dst)
	// bufSize 0 inherits safefs.CopyBounded's 1 MiB default (the stall floor is
	// then a few tens of KB/s, harmless for a healthy mount). crypto/sha256.Write
	// is in-memory and never blocks, so only the file read side can wedge.
	if _, err = safefs.CopyBounded(ctx, hash, file, 0, timeout, "read checksum source file", filePath); err != nil {
		if safefs.IsAbandoned(err) {
			return "", err // ErrTimeout / context.Canceled verbatim (close skipped above)
		}
		return "", fmt.Errorf("failed to read file: %w", err)
	}

	checksum = hex.EncodeToString(hash.Sum(nil))
	logger.Debug("Generated checksum: %s", checksum)
	return checksum, nil
}

// CreateManifest creates a manifest file with archive metadata and checksum
func CreateManifest(ctx context.Context, logger *logging.Logger, manifest *Manifest, outputPath string) error {
	logger.Debug("Creating manifest file: %s", outputPath)

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0750); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Marshal manifest to JSON with indentation
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	// Write manifest file (owner-only; the integrity manifest is verified by
	// proxsave itself, it carries no group/world reader).
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write manifest file: %w", err)
	}

	logger.Debug("Manifest created successfully")
	return nil
}

// VerifyChecksum verifies a file against a manifest's checksum with NO FS I/O
// timeout (legacy). Prefer VerifyChecksumBounded on potentially remote/stale paths.
func VerifyChecksum(ctx context.Context, logger *logging.Logger, filePath, expectedChecksum string) (bool, error) {
	return VerifyChecksumBounded(ctx, logger, filePath, expectedChecksum, 0)
}

// VerifyChecksumBounded is VerifyChecksum with a per-chunk FS I/O stall budget
// (see GenerateChecksumBounded).
func VerifyChecksumBounded(ctx context.Context, logger *logging.Logger, filePath, expectedChecksum string, timeout time.Duration) (bool, error) {
	logger.Debug("Verifying checksum for: %s", filePath)

	normalizedExpected, err := NormalizeChecksum(expectedChecksum)
	if err != nil {
		return false, fmt.Errorf("invalid expected checksum: %w", err)
	}

	actualChecksum, err := GenerateChecksumBounded(ctx, logger, filePath, timeout)
	if err != nil {
		return false, fmt.Errorf("failed to generate checksum: %w", err)
	}

	matches := actualChecksum == normalizedExpected
	if matches {
		logger.Debug("Checksum verification passed")
	} else {
		logger.Warning("Checksum mismatch! Expected: %s, Got: %s", normalizedExpected, actualChecksum)
	}

	return matches, nil
}

// LoadManifest loads a manifest from a JSON file
func LoadManifest(manifestPath string) (*Manifest, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file: %w", err)
	}

	var manifest Manifest
	unmarshalErr := json.Unmarshal(data, &manifest)
	if unmarshalErr == nil {
		return &manifest, nil
	}

	legacyManifest, legacyErr := loadLegacyManifest(manifestPath, data)
	if legacyErr == nil {
		return legacyManifest, nil
	}

	return nil, fmt.Errorf(
		"failed to parse manifest as JSON (%v) and legacy metadata (%v)",
		unmarshalErr,
		legacyErr,
	)
}

// loadLegacyManifest attempts to parse legacy Bash metadata files (KEY=VALUE format).
func loadLegacyManifest(manifestPath string, data []byte) (*Manifest, error) {
	// Legacy metadata exists only as sidecar files (*.tar.*.metadata)
	if !strings.HasSuffix(manifestPath, ".metadata") {
		return nil, fmt.Errorf("not a legacy metadata file")
	}

	archivePath := strings.TrimSuffix(manifestPath, ".metadata")
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("cannot stat archive %s: %w", archivePath, err)
	}

	legacy := &Manifest{
		ArchivePath: archivePath,
		ArchiveSize: info.Size(),
		CreatedAt:   info.ModTime(),
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	if err := parseLegacyMetadata(scanner, legacy); err != nil {
		return nil, fmt.Errorf("parse legacy metadata: %w", err)
	}

	loadLegacyChecksum(archivePath, legacy)
	inferEncryptionMode(archivePath, legacy)

	return legacy, nil
}

func parseLegacyMetadata(scanner *bufio.Scanner, legacy *Manifest) error {
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
		case "BACKUP_TARGETS", "PROXMOX_TARGETS":
			targets := strings.Split(value, ",")
			seenTargets := make(map[string]struct{}, len(legacy.ProxmoxTargets))
			for _, target := range legacy.ProxmoxTargets {
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				seenTargets[target] = struct{}{}
			}
			for _, target := range targets {
				target = strings.TrimSpace(target)
				if target != "" {
					if _, ok := seenTargets[target]; ok {
						continue
					}
					seenTargets[target] = struct{}{}
					legacy.ProxmoxTargets = append(legacy.ProxmoxTargets, target)
				}
			}
		case "PROXMOX_VERSION":
			legacy.ProxmoxVersion = value
		case "PVE_VERSION":
			legacy.PVEVersion = value
		case "PBS_VERSION":
			legacy.PBSVersion = value
		case "HOSTNAME":
			legacy.Hostname = value
		case "SCRIPT_VERSION":
			legacy.ScriptVersion = value
		case "ENCRYPTION_MODE":
			legacy.EncryptionMode = value
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan legacy metadata: %w", err)
	}
	return nil
}

func loadLegacyChecksum(archivePath string, legacy *Manifest) {
	// Attempt to load checksum from legacy .sha256 file
	if shaData, err := os.ReadFile(archivePath + ".sha256"); err == nil {
		if checksum, parseErr := ParseChecksumData(shaData); parseErr == nil {
			legacy.SHA256 = checksum
		}
	}
}

func inferEncryptionMode(archivePath string, legacy *Manifest) {
	// Fallback: infer encryption from file extension if not specified in metadata
	if legacy.EncryptionMode == "" {
		// Get the actual archive path (remove .metadata suffix)
		actualArchivePath := strings.TrimSuffix(archivePath, ".metadata")
		if strings.HasSuffix(actualArchivePath, ".age") {
			legacy.EncryptionMode = "age"
		} else {
			legacy.EncryptionMode = "plain"
		}
	}
}
