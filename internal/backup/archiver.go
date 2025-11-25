package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
	"github.com/tis24dev/proxmox-backup/internal/logging"
	"github.com/tis24dev/proxmox-backup/internal/types"
)

var lookPath = exec.LookPath

// ArchiverDeps groups external dependencies used by Archiver.
type ArchiverDeps struct {
	LookPath       func(string) (string, error)
	CommandContext func(context.Context, string, ...string) *exec.Cmd
}

func defaultArchiverDeps() ArchiverDeps {
	return ArchiverDeps{
		LookPath:       lookPath,
		CommandContext: exec.CommandContext,
	}
}

// WithLookPathOverride temporaneamente sostituisce lookPath (per i test) e
// restituisce una funzione di ripristino da invocare con defer.
func WithLookPathOverride(fn func(string) (string, error)) func() {
	original := lookPath
	lookPath = fn
	return func() {
		lookPath = original
	}
}

// Archiver handles tar archive creation with compression
type Archiver struct {
	logger               *logging.Logger
	compression          types.CompressionType
	compressionLevel     int
	compressionThreads   int
	compressionMode      string
	dryRun               bool
	requestedCompression types.CompressionType
	encryptArchive       bool
	ageRecipients        []age.Recipient
	deps                 ArchiverDeps
}

// ArchiverConfig holds configuration for archive creation
type ArchiverConfig struct {
	Compression        types.CompressionType
	CompressionLevel   int // 1-9 for gzip, 0-9 for xz, 1-22 for zstd
	CompressionThreads int
	CompressionMode    string
	DryRun             bool
	EncryptArchive     bool
	AgeRecipients      []age.Recipient
}

// CompressionError rappresenta un errore di compressione esterna (xz/zstd)
type CompressionError struct {
	Algorithm string
	Err       error
}

func (e *CompressionError) Error() string {
	return fmt.Sprintf("%s compression failed: %v", e.Algorithm, e.Err)
}

func (e *CompressionError) Unwrap() error {
	return e.Err
}

// Validate checks if the archiver configuration is valid
func (a *ArchiverConfig) Validate() error {
	// Validate compression type
	switch a.Compression {
	case types.CompressionNone,
		types.CompressionGzip,
		types.CompressionPigz,
		types.CompressionBzip2,
		types.CompressionXZ,
		types.CompressionLZMA,
		types.CompressionZstd:
		// Valid compression types
	default:
		return fmt.Errorf("invalid compression type: %s", a.Compression)
	}

	// Validate compression level based on type
	switch a.Compression {
	case types.CompressionGzip:
		if a.CompressionLevel < 1 || a.CompressionLevel > 9 {
			return fmt.Errorf("gzip compression level must be 1-9, got %d", a.CompressionLevel)
		}
	case types.CompressionPigz:
		if a.CompressionLevel < 1 || a.CompressionLevel > 9 {
			return fmt.Errorf("pigz compression level must be 1-9, got %d", a.CompressionLevel)
		}
	case types.CompressionXZ:
		if a.CompressionLevel < 0 || a.CompressionLevel > 9 {
			return fmt.Errorf("xz compression level must be 0-9, got %d", a.CompressionLevel)
		}
	case types.CompressionBzip2:
		if a.CompressionLevel < 1 || a.CompressionLevel > 9 {
			return fmt.Errorf("bzip2 compression level must be 1-9, got %d", a.CompressionLevel)
		}
	case types.CompressionLZMA:
		if a.CompressionLevel < 0 || a.CompressionLevel > 9 {
			return fmt.Errorf("lzma compression level must be 0-9, got %d", a.CompressionLevel)
		}
	case types.CompressionZstd:
		if a.CompressionLevel < 1 || a.CompressionLevel > 22 {
			return fmt.Errorf("zstd compression level must be 1-22, got %d", a.CompressionLevel)
		}
	case types.CompressionNone:
		// No level validation needed for no compression
	}

	if a.CompressionThreads < 0 {
		return fmt.Errorf("compression threads must be >= 0")
	}

	return nil
}

// NewArchiver creates a new archiver
func NewArchiver(logger *logging.Logger, config *ArchiverConfig) *Archiver {
	mode := normalizeCompressionMode(config.CompressionMode)
	return &Archiver{
		logger:               logger,
		compression:          config.Compression,
		compressionLevel:     config.CompressionLevel,
		compressionThreads:   config.CompressionThreads,
		compressionMode:      mode,
		dryRun:               config.DryRun,
		requestedCompression: config.Compression,
		encryptArchive:       config.EncryptArchive,
		ageRecipients:        append([]age.Recipient(nil), config.AgeRecipients...),
		deps:                 defaultArchiverDeps(),
	}
}

func (a *Archiver) cmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	if a.deps.CommandContext != nil {
		return a.deps.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func (a *Archiver) findPath(name string) (string, error) {
	if a.deps.LookPath != nil {
		return a.deps.LookPath(name)
	}
	return exec.LookPath(name)
}

// RequestedCompression returns the compression algorithm requested via configuration.
func (a *Archiver) RequestedCompression() types.CompressionType {
	return a.requestedCompression
}

// EffectiveCompression returns the compression algorithm currently in use.
func (a *Archiver) EffectiveCompression() types.CompressionType {
	return a.compression
}

// CompressionLevel returns the current compression level (already normalized).
func (a *Archiver) CompressionLevel() int {
	return a.compressionLevel
}

// CompressionMode returns the active compression mode (fast/standard/maximum/ultra).
func (a *Archiver) CompressionMode() string {
	if a.compressionMode == "" {
		return "standard"
	}
	return a.compressionMode
}

// CompressionThreads returns the number of threads requested for compression.
func (a *Archiver) CompressionThreads() int {
	return a.compressionThreads
}

// ResolveCompression ensures the configured compression is available and normalizes
// the compression level. If the requested algorithm is unavailable it falls back
// to gzip, keeping the caller informed via logs.
func (a *Archiver) ResolveCompression() types.CompressionType {
	a.logger.Debug("Resolving compression (requested=%s level=%d mode=%s)", a.requestedCompression, a.compressionLevel, a.CompressionMode())
	switch a.compression {
	case types.CompressionXZ:
		if _, err := a.findPath("xz"); err != nil {
			a.logger.Warning("xz command not available: %v", err)
			a.compression = types.CompressionGzip
			a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
		}
	case types.CompressionZstd:
		if _, err := a.findPath("zstd"); err != nil {
			a.logger.Warning("zstd command not available: %v", err)
			a.compression = types.CompressionGzip
			a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
		} else {
			a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
		}
	case types.CompressionPigz:
		if _, err := a.findPath("pigz"); err != nil {
			a.logger.Warning("pigz command not available: %v", err)
			a.compression = types.CompressionGzip
			a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
		}
	case types.CompressionBzip2:
		if _, err := a.findPath("pbzip2"); err != nil {
			if _, err := a.findPath("bzip2"); err != nil {
				a.logger.Warning("bzip2 command not available: %v", err)
				a.compression = types.CompressionGzip
				a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
			}
		}
	case types.CompressionLZMA:
		if _, err := a.findPath("lzma"); err != nil {
			a.logger.Warning("lzma command not available: %v", err)
			a.compression = types.CompressionGzip
			a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
		}
	case types.CompressionGzip:
		a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
	case types.CompressionNone:
		a.compressionLevel = 0
	default:
		a.logger.Warning("Unknown compression type %s, using gzip fallback", a.compression)
		a.compression = types.CompressionGzip
		a.compressionLevel = normalizeLevelForCompression(a.compression, a.compressionLevel)
	}
	a.logger.Debug("Compression resolved to %s (level %d, threads %d)", a.compression, a.compressionLevel, a.compressionThreads)
	return a.compression
}

func normalizeLevelForCompression(comp types.CompressionType, level int) int {
	switch comp {
	case types.CompressionGzip:
		if level < 1 || level > 9 {
			return 6
		}
	case types.CompressionPigz:
		if level < 1 || level > 9 {
			return 6
		}
	case types.CompressionXZ:
		if level < 0 || level > 9 {
			return 6
		}
	case types.CompressionBzip2:
		if level < 1 || level > 9 {
			return 6
		}
	case types.CompressionLZMA:
		if level < 0 || level > 9 {
			return 6
		}
	case types.CompressionZstd:
		if level < 1 || level > 22 {
			return 6
		}
	case types.CompressionNone:
		return 0
	default:
		return 6
	}
	return level
}

func normalizeCompressionMode(mode string) string {
	switch strings.ToLower(mode) {
	case "fast", "maximum", "ultra":
		return strings.ToLower(mode)
	default:
		return "standard"
	}
}

func requiresExtremeMode(mode string) bool {
	switch strings.ToLower(mode) {
	case "maximum", "ultra":
		return true
	default:
		return false
	}
}

func buildPigzArgs(level, threads int, mode string) []string {
	args := make([]string, 0, 4)
	if threads > 0 {
		args = append(args, fmt.Sprintf("-p%d", threads))
	}
	args = append(args, fmt.Sprintf("-%d", level))
	if requiresExtremeMode(mode) {
		args = append(args, "--best")
	}
	args = append(args, "-c")
	return args
}

func buildXZArgs(level, threads int, mode string) []string {
	args := []string{fmt.Sprintf("-%d", level)}
	if threads > 0 {
		args = append(args, fmt.Sprintf("-T%d", threads))
	} else {
		args = append(args, "-T0")
	}
	if requiresExtremeMode(mode) {
		args = append(args, "--extreme")
	}
	args = append(args, "-c")
	return args
}

func buildZstdArgs(level, threads int) []string {
	args := make([]string, 0, 5)
	if level > 19 {
		args = append(args, "--ultra")
	}
	args = append(args, fmt.Sprintf("-%d", level))
	if threads > 0 {
		args = append(args, fmt.Sprintf("-T%d", threads))
	} else {
		args = append(args, "-T0")
	}
	args = append(args, "-q", "-c")
	return args
}

// writeTar writes the directory contents to the provided writer as a tar archive
func (a *Archiver) writeTar(ctx context.Context, sourceDir string, w io.Writer) error {
	tarWriter := tar.NewWriter(w)
	err := a.addToTar(ctx, tarWriter, sourceDir, "")
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	return err
}

// GetDefaultArchiverConfig returns default archiver configuration
func GetDefaultArchiverConfig() *ArchiverConfig {
	return &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 6, // Balanced compression
		CompressionMode:  "standard",
		DryRun:           false,
	}
}

func (a *Archiver) wrapEncryptionWriter(base io.Writer) (io.Writer, func() error, error) {
	if !a.encryptArchive {
		return base, func() error { return nil }, nil
	}

	recipients := a.ageRecipients
	if len(recipients) == 0 {
		return nil, nil, fmt.Errorf("encryption enabled but no AGE recipients configured")
	}

	writer, err := age.Encrypt(base, recipients...)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize age encryption: %w", err)
	}

	a.logger.Debug("Encrypting archive via age (streaming)")
	return writer, writer.Close, nil
}

// CreateArchive creates a compressed tar archive from a directory
func (a *Archiver) CreateArchive(ctx context.Context, sourceDir, outputPath string) error {
	actualCompression := a.ResolveCompression()
	if a.requestedCompression != actualCompression {
		a.logger.Warning("Requested compression %s unavailable, using %s instead",
			a.requestedCompression, actualCompression)
	}

	threadInfo := "auto"
	if a.compressionThreads > 0 {
		threadInfo = fmt.Sprintf("%d", a.compressionThreads)
	}
	a.logger.Info("Creating compressed archive with %s (level %d, mode %s, threads %s)",
		actualCompression, a.compressionLevel, a.CompressionMode(), threadInfo)

	a.logger.Debug("Creating archive: %s -> %s (compression: %s)",
		sourceDir, outputPath, actualCompression)

	if a.dryRun {
		a.logger.Info("[DRY RUN] Would create archive: %s", outputPath)
		return nil
	}

	// Ensure output directory exists
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Choose compression method
	switch actualCompression {
	case types.CompressionGzip:
		return a.createGzipArchive(ctx, sourceDir, outputPath)
	case types.CompressionPigz:
		return a.createPigzArchive(ctx, sourceDir, outputPath)
	case types.CompressionXZ:
		return a.createXZArchive(ctx, sourceDir, outputPath)
	case types.CompressionBzip2:
		return a.createBzip2Archive(ctx, sourceDir, outputPath)
	case types.CompressionLZMA:
		return a.createLzmaArchive(ctx, sourceDir, outputPath)
	case types.CompressionZstd:
		return a.createZstdArchive(ctx, sourceDir, outputPath)
	case types.CompressionNone:
		return a.createTarArchive(ctx, sourceDir, outputPath)
	default:
		return fmt.Errorf("unsupported compression type: %s", actualCompression)
	}
}

// createGzipArchive creates a gzip-compressed tar archive using Go's stdlib
func (a *Archiver) createGzipArchive(ctx context.Context, sourceDir, outputPath string) (err error) {
	a.logger.Debug("Creating gzip archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())

	// Create output file
	outFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	writer, finalizeEncryption, err := a.wrapEncryptionWriter(outFile)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := finalizeEncryption(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("finalize encrypted archive: %w", cerr)
			} else {
				a.logger.Warning("Failed to finalize encrypted archive: %v", cerr)
			}
		}
	}()

	// Create gzip writer targeting final writer (possibly encrypted)
	gzWriter, err := gzip.NewWriterLevel(writer, a.compressionLevel)
	if err != nil {
		return fmt.Errorf("failed to create gzip writer: %w", err)
	}
	defer gzWriter.Close()

	// Stream tar content into gzip writer
	if err := a.writeTar(ctx, sourceDir, gzWriter); err != nil {
		return fmt.Errorf("failed to write tar stream: %w", err)
	}

	return nil
}

func (a *Archiver) createPigzArchive(ctx context.Context, sourceDir, outputPath string) error {
	a.logger.Debug("Creating pigz archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())
	args := buildPigzArgs(a.compressionLevel, a.compressionThreads, a.CompressionMode())
	cmd := a.cmd(ctx, "pigz", args...)
	return a.pipeTarThroughCommand(ctx, sourceDir, outputPath, cmd, "pigz")
}

// createTarArchive creates an uncompressed tar archive
func (a *Archiver) createTarArchive(ctx context.Context, sourceDir, outputPath string) (err error) {
	a.logger.Debug("Creating uncompressed tar archive")

	// Create output file
	outFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	writer, finalizeEncryption, err := a.wrapEncryptionWriter(outFile)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := finalizeEncryption(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("finalize encrypted archive: %w", cerr)
			} else {
				a.logger.Warning("Failed to finalize encrypted archive: %v", cerr)
			}
		}
	}()

	if err := a.writeTar(ctx, sourceDir, writer); err != nil {
		return fmt.Errorf("failed to write tar archive: %w", err)
	}
	return nil
}

func (a *Archiver) createBzip2Archive(ctx context.Context, sourceDir, outputPath string) error {
	a.logger.Debug("Creating bzip2 archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())
	var cmd *exec.Cmd
	if a.compressionThreads > 1 {
		if _, err := a.findPath("pbzip2"); err == nil {
			cmd = a.cmd(ctx, "pbzip2",
				fmt.Sprintf("-%d", a.compressionLevel),
				fmt.Sprintf("-p%d", a.compressionThreads),
				"-c",
			)
		}
	}
	if cmd == nil {
		cmd = a.cmd(ctx, "bzip2",
			fmt.Sprintf("-%d", a.compressionLevel),
			"-c",
		)
	}
	return a.pipeTarThroughCommand(ctx, sourceDir, outputPath, cmd, "bzip2")
}

func (a *Archiver) createLzmaArchive(ctx context.Context, sourceDir, outputPath string) error {
	a.logger.Debug("Creating lzma archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())
	levelFlag := fmt.Sprintf("-%d", a.compressionLevel)
	if requiresExtremeMode(a.CompressionMode()) {
		levelFlag += "e"
	}
	cmd := a.cmd(ctx, "lzma",
		levelFlag,
		"-c",
	)
	return a.pipeTarThroughCommand(ctx, sourceDir, outputPath, cmd, "lzma")
}

// createXZArchive creates an xz-compressed tar archive using external xz command
func (a *Archiver) createXZArchive(ctx context.Context, sourceDir, outputPath string) (err error) {
	a.logger.Debug("Creating xz archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())

	args := buildXZArgs(a.compressionLevel, a.compressionThreads, a.CompressionMode())
	cmd := a.cmd(ctx, "xz", args...)
	if err := a.attachStderrLogger(cmd, "xz"); err != nil {
		return fmt.Errorf("capture xz output: %w", err)
	}

	outFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	pr, pw := io.Pipe()
	cmd.Stdin = pr
	writer, finalizeEncryption, err := a.wrapEncryptionWriter(outFile)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := finalizeEncryption(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("finalize encrypted archive: %w", cerr)
			} else {
				a.logger.Warning("Failed to finalize encrypted archive: %v", cerr)
			}
		}
	}()
	cmd.Stdout = writer

	errChan := make(chan error, 1)
	go func() {
		defer close(errChan)
		err := a.writeTar(ctx, sourceDir, pw)
		if err != nil {
			pw.CloseWithError(err)
		} else {
			pw.Close()
		}
		errChan <- err
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		if startErr := <-errChan; startErr != nil {
			return startErr
		}
		return fmt.Errorf("failed to start xz: %w", err)
	}

	tarErr := <-errChan
	if tarErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return tarErr
	}

	if err := cmd.Wait(); err != nil {
		return &CompressionError{Algorithm: "xz", Err: err}
	}

	a.logger.Debug("XZ compression completed successfully")
	return nil
}

// createZstdArchive creates a zstd-compressed tar archive using external zstd command
func (a *Archiver) createZstdArchive(ctx context.Context, sourceDir, outputPath string) (err error) {
	a.logger.Debug("Creating zstd archive with level %d (mode %s)", a.compressionLevel, a.CompressionMode())

	args := buildZstdArgs(a.compressionLevel, a.compressionThreads)
	cmd := a.cmd(ctx, "zstd", args...)
	if err := a.attachStderrLogger(cmd, "zstd"); err != nil {
		return fmt.Errorf("capture zstd output: %w", err)
	}

	outFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	pr, pw := io.Pipe()
	cmd.Stdin = pr
	writer, finalizeEncryption, err := a.wrapEncryptionWriter(outFile)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := finalizeEncryption(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("finalize encrypted archive: %w", cerr)
			} else {
				a.logger.Warning("Failed to finalize encrypted archive: %v", cerr)
			}
		}
	}()
	cmd.Stdout = writer

	errChan := make(chan error, 1)
	go func() {
		defer close(errChan)
		err := a.writeTar(ctx, sourceDir, pw)
		if err != nil {
			pw.CloseWithError(err)
		} else {
			pw.Close()
		}
		errChan <- err
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		if startErr := <-errChan; startErr != nil {
			return startErr
		}
		return fmt.Errorf("failed to start zstd: %w", err)
	}

	tarErr := <-errChan
	if tarErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return tarErr
	}

	if err := cmd.Wait(); err != nil {
		return &CompressionError{Algorithm: "zstd", Err: err}
	}

	a.logger.Debug("Zstd compression completed successfully")
	return nil
}

func (a *Archiver) attachStderrLogger(cmd *exec.Cmd, algo string) error {
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	tag := strings.ToUpper(algo)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			a.logger.Info("[%s] %s", tag, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			a.logger.Debug("[%s] stderr read error: %v", tag, err)
		}
	}()

	return nil
}

func (a *Archiver) pipeTarThroughCommand(ctx context.Context, sourceDir, outputPath string, cmd *exec.Cmd, algo string) (err error) {
	outFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	pr, pw := io.Pipe()
	cmd.Stdin = pr
	writer, finalizeEncryption, err := a.wrapEncryptionWriter(outFile)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := finalizeEncryption(); cerr != nil {
			if err == nil {
				err = fmt.Errorf("finalize encrypted archive: %w", cerr)
			} else {
				a.logger.Warning("Failed to finalize encrypted archive: %v", cerr)
			}
		}
	}()
	cmd.Stdout = writer
	if err := a.attachStderrLogger(cmd, algo); err != nil {
		return fmt.Errorf("capture %s output: %w", algo, err)
	}

	errChan := make(chan error, 1)
	go func() {
		defer close(errChan)
		if err := a.writeTar(ctx, sourceDir, pw); err != nil {
			pw.CloseWithError(err)
			errChan <- err
			return
		}
		pw.Close()
		errChan <- nil
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		if startErr := <-errChan; startErr != nil {
			return startErr
		}
		return fmt.Errorf("failed to start %s: %w", algo, err)
	}

	if tarErr := <-errChan; tarErr != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return tarErr
	}

	if err := cmd.Wait(); err != nil {
		return &CompressionError{Algorithm: algo, Err: err}
	}

	a.logger.Debug("%s compression completed successfully", strings.ToUpper(algo))
	return nil
}

// addToTar recursively adds files and directories to a tar archive
// Preserves symlinks instead of following them
func (a *Archiver) addToTar(ctx context.Context, tarWriter *tar.Writer, sourceDir, baseInArchive string) error {
	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err != nil {
			a.logger.Warning("Error accessing path %s: %v", path, err)
			return nil // Continue with other files
		}

		// Calculate relative path for archive
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		// Skip the root directory itself
		if relPath == "." {
			return nil
		}

		// Use Lstat to get symlink info without following it
		linkInfo, err := os.Lstat(path)
		if err != nil {
			a.logger.Warning("Failed to stat path %s: %v", path, err)
			return nil
		}

		// Create archive path
		archivePath := filepath.Join(baseInArchive, relPath)

		// Determine link target for symlinks
		var linkTarget string
		if linkInfo.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				a.logger.Warning("Failed to read symlink %s: %v", path, err)
				return nil
			}
		}

		// Create tar header (using linkInfo for accurate type)
		header, err := tar.FileInfoHeader(linkInfo, linkTarget)
		if err != nil {
			a.logger.Warning("Failed to create header for %s: %v", path, err)
			return nil
		}

		// Preserve uid/gid from original file (critical for restore)
		if stat, ok := linkInfo.Sys().(*syscall.Stat_t); ok {
			header.Uid = int(stat.Uid)
			header.Gid = int(stat.Gid)
			// Preserve access and modification times
			header.AccessTime = time.Unix(stat.Atim.Sec, stat.Atim.Nsec)
			header.ChangeTime = time.Unix(stat.Ctim.Sec, stat.Ctim.Nsec)
			// ModTime is already set by FileInfoHeader, but ensure it's accurate
			header.ModTime = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec)
		} else {
			// Fallback: at least log that we couldn't preserve ownership
			a.logger.Warning("Could not extract uid/gid for %s, using defaults", path)
		}

		// Force PAX format to preserve atime/ctime in extended headers
		// PAX format supports extended timestamps that USTAR does not
		header.Format = tar.FormatPAX

		// Use forward slashes in tar (Unix convention) and prefix with "./" for compatibility
		name := strings.ReplaceAll(archivePath, string(filepath.Separator), "/")
		if !strings.HasPrefix(name, "./") && !strings.HasPrefix(name, "../") {
			name = "./" + name
		}
		header.Name = name

		// Write header
		if err := tarWriter.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		// If it's a regular file (not symlink, dir, etc), write its content
		if linkInfo.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				a.logger.Warning("Failed to open file %s: %v", path, err)
				return nil
			}
			defer file.Close()

			if _, err := io.Copy(tarWriter, file); err != nil {
				a.logger.Warning("Failed to write file %s to archive: %v", path, err)
				return nil
			}

			a.logger.Debug("Added file to archive: %s", archivePath)
		} else if linkInfo.Mode()&os.ModeSymlink != 0 {
			a.logger.Debug("Added symlink to archive: %s -> %s", archivePath, linkTarget)
		}

		return nil
	})
}

// GetArchiveExtension returns the appropriate file extension for the compression type
func (a *Archiver) GetArchiveExtension() string {
	var ext string
	switch a.compression {
	case types.CompressionGzip, types.CompressionPigz:
		ext = ".tar.gz"
	case types.CompressionBzip2:
		ext = ".tar.bz2"
	case types.CompressionXZ:
		ext = ".tar.xz"
	case types.CompressionLZMA:
		ext = ".tar.lzma"
	case types.CompressionZstd:
		ext = ".tar.zst"
	case types.CompressionNone:
		ext = ".tar"
	default:
		ext = ".tar"
	}
	if a.encryptArchive {
		ext += ".age"
	}
	return ext
}

// EstimateCompressionRatio returns an estimated compression ratio for the compression type
func (a *Archiver) EstimateCompressionRatio() float64 {
	switch a.compression {
	case types.CompressionGzip, types.CompressionPigz:
		return 0.3 // ~30% of original size
	case types.CompressionBzip2:
		return 0.28
	case types.CompressionXZ:
		return 0.2 // ~20% of original size (better compression)
	case types.CompressionLZMA:
		return 0.22
	case types.CompressionZstd:
		return 0.25 // ~25% of original size (good balance)
	case types.CompressionNone:
		return 1.0 // No compression
	default:
		return 0.5
	}
}

// VerifyArchive performs comprehensive verification of the created archive
func (a *Archiver) VerifyArchive(ctx context.Context, archivePath string) error {
	a.logger.Debug("Verifying archive: %s", archivePath)

	if a.dryRun {
		a.logger.Info("[DRY RUN] Would verify archive: %s", archivePath)
		return nil
	}

	if a.encryptArchive {
		// With streaming encryption enabled, we cannot verify the tar/compression without plaintext.
		// Keep metadata/checksum verification at higher layers; here we skip detailed verification.
		a.logger.Debug("Archive verification skipped (encrypted archive)")
		// Basic existence/size checks still apply below
	}

	// Check if file exists
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("archive not found: %w", err)
	}

	// Check file size
	if info.Size() == 0 {
		return fmt.Errorf("archive is empty")
	}

	a.logger.Debug("Archive size: %d bytes", info.Size())

	if a.encryptArchive {
		// Encrypted: skip detailed verification
		return nil
	}

	// Test archive integrity based on compression type
	switch a.compression {
	case types.CompressionXZ:
		return a.verifyXZArchive(ctx, archivePath)
	case types.CompressionZstd:
		return a.verifyZstdArchive(ctx, archivePath)
	case types.CompressionGzip:
		return a.verifyGzipArchive(ctx, archivePath)
	case types.CompressionNone:
		return a.verifyTarArchive(ctx, archivePath)
	default:
		a.logger.Warning("Unknown compression type, skipping detailed verification")
		return nil
	}
}

// verifyXZArchive tests XZ compression and tar integrity
func (a *Archiver) verifyXZArchive(ctx context.Context, archivePath string) error {
	a.logger.Debug("Testing XZ compression integrity")

	// Test XZ compression integrity
	cmd := a.cmd(ctx, "xz", "--test", archivePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("xz integrity test failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("XZ compression test passed")

	// Test tar listing (decompress and list without extracting)
	cmd = a.cmd(ctx, "tar", "-tJf", archivePath)
	cmd.Stdout = nil // Discard output
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar listing failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("Archive verification passed: XZ compression and tar structure are valid")
	return nil
}

// verifyZstdArchive tests Zstd compression and tar integrity
func (a *Archiver) verifyZstdArchive(ctx context.Context, archivePath string) error {
	a.logger.Debug("Testing Zstd compression integrity")

	// Test Zstd compression integrity
	cmd := a.cmd(ctx, "zstd", "--test", archivePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zstd integrity test failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("Zstd compression test passed")

	// Test tar listing (decompress and list without extracting)
	cmd = a.cmd(ctx, "tar", "--use-compress-program=zstd", "-tf", archivePath)
	cmd.Stdout = nil // Discard output
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar listing failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("Archive verification passed: Zstd compression and tar structure are valid")
	return nil
}

// verifyGzipArchive tests Gzip compression and tar integrity
func (a *Archiver) verifyGzipArchive(ctx context.Context, archivePath string) error {
	a.logger.Debug("Testing Gzip compression integrity")

	// Test tar listing (tar will test gzip integrity automatically)
	cmd := a.cmd(ctx, "tar", "-tzf", archivePath)
	cmd.Stdout = nil // Discard output
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar/gzip verification failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("Archive verification passed: Gzip compression and tar structure are valid")
	return nil
}

// verifyTarArchive tests uncompressed tar integrity
func (a *Archiver) verifyTarArchive(ctx context.Context, archivePath string) error {
	a.logger.Debug("Testing uncompressed tar integrity")

	// Test tar listing
	cmd := a.cmd(ctx, "tar", "-tf", archivePath)
	cmd.Stdout = nil // Discard output
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar verification failed: %w (output: %s)", err, string(output))
	}

	a.logger.Debug("Archive verification passed: Tar structure is valid")
	return nil
}

// GetArchiveSize returns the size of the archive in bytes
func (a *Archiver) GetArchiveSize(archivePath string) (int64, error) {
	info, err := os.Stat(archivePath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// FormatDuration formats a duration in human-readable format
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// FormatBytes formats bytes in human-readable format
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
