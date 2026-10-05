package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// renameErrFS fails every Rename, so the promote of the verified partial onto
// the final name fails while every other operation reaches the real filesystem.
type renameErrFS struct {
	FS
	err error
}

func (f renameErrFS) Rename(string, string) error { return f.err }

// discardedArchiveUncompressedSize is the staged payload size the ratio divides
// by. It is far above any tar these tests write, so a recorded archive yields a
// non-zero ratio, ratio percent and savings percent.
const discardedArchiveUncompressedSize = 1 << 20

type discardedArchiveFixture struct {
	orch      *Orchestrator
	run       *backupRunContext
	workspace *backupWorkspace
	artifacts *backupArtifacts
}

// newDiscardedArchiveFixture stands where createBackupArchive leaves a run: the
// archive is written at <final>.partial and stats.ArchivePath already names the
// final file. partial seeds the partial path; nil writes a real uncompressed tar
// through the same archiver that later verifies it. encrypt selects the archiver
// whose verification is only "exists and is not empty", which is what lets a
// test reach the checksum step with a partial that tar could not list.
func newDiscardedArchiveFixture(t *testing.T, ctx context.Context, encrypt bool, partial func(t *testing.T, path string)) *discardedArchiveFixture {
	t.Helper()

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "f2-host-backup-20261002-100000.tar")
	partialPath := archivePath + ".partial"
	archiver := backup.NewArchiver(newTestLogger(), &backup.ArchiverConfig{
		Compression:    types.CompressionNone,
		EncryptArchive: encrypt,
	})

	if partial != nil {
		partial(t, partialPath)
	} else {
		source := filepath.Join(dir, "staged")
		if err := os.MkdirAll(source, 0o755); err != nil {
			t.Fatalf("mkdir staged: %v", err)
		}
		if err := os.WriteFile(filepath.Join(source, "f.txt"), []byte("data"), 0o644); err != nil {
			t.Fatalf("seed staged: %v", err)
		}
		if err := createBackupArchiveFile(context.Background(), archiver, source, partialPath); err != nil {
			t.Fatalf("createBackupArchiveFile: %v", err)
		}
	}

	stats := &BackupStats{
		Compression:      types.CompressionNone,
		UncompressedSize: discardedArchiveUncompressedSize,
		// createBackupArchive sets this as soon as the partial is written.
		ArchivePath: archivePath,
	}
	return &discardedArchiveFixture{
		orch: &Orchestrator{logger: newTestLogger(), cfg: &config.Config{BackupPath: dir}},
		run:  &backupRunContext{ctx: ctx, stats: stats},
		workspace: &backupWorkspace{
			fs: osFS{},
		},
		artifacts: &backupArtifacts{
			archiver:     archiver,
			archivePath:  archivePath,
			partialPath:  partialPath,
			checksumPath: archivePath + ".sha256",
		},
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func writeNotATar(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("F2 fixture: these bytes are not a tar archive\n"), 0o640); err != nil {
		t.Fatalf("seed partial: %v", err)
	}
}

func writeOpaqueBytes(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("F2 fixture: opaque encrypted payload\n"), 0o640); err != nil {
		t.Fatalf("seed partial: %v", err)
	}
}

// writeUnreadableAsFile puts a directory at the partial path. It stats as a
// non-empty entry, so the size is recorded and the encrypted archiver's
// verification passes, but reading it for the checksum fails - on any user,
// root included. The file inside keeps the directory size non-zero on
// filesystems that report an empty directory as 0 bytes.
func writeUnreadableAsFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir partial: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "entry"), []byte("x"), 0o640); err != nil {
		t.Fatalf("seed partial dir: %v", err)
	}
}

func assertNoArchiveOnDisk(t *testing.T, a *backupArtifacts) {
	t.Helper()
	if _, err := os.Stat(a.partialPath); !os.IsNotExist(err) {
		t.Fatalf("partial must be discarded, stat err=%v", err)
	}
	if _, err := os.Stat(a.archivePath); !os.IsNotExist(err) {
		t.Fatalf("final archive must never exist on this path, stat err=%v", err)
	}
}

// assertDiscardedArchiveCleared pins the stats a discarded partial must leave
// behind: exactly what a compression failure leaves, because in both cases no
// archive exists. Fields that describe the run rather than the file (the staged
// payload size, the compression in use) stay as they were.
func assertDiscardedArchiveCleared(t *testing.T, s *BackupStats) {
	t.Helper()
	if s.ArchivePath != "" || s.ArchiveSize != 0 || s.CompressedSize != 0 || s.Checksum != "" {
		t.Fatalf("discarded archive still described: ArchivePath=%q ArchiveSize=%d CompressedSize=%d Checksum=%q",
			s.ArchivePath, s.ArchiveSize, s.CompressedSize, s.Checksum)
	}
	if s.CompressionRatio != 0 || s.CompressionRatioPercent != 0 || s.CompressionSavingsPercent != 0 {
		t.Fatalf("discarded archive ratios kept: ratio=%v percent=%v savings=%v",
			s.CompressionRatio, s.CompressionRatioPercent, s.CompressionSavingsPercent)
	}
	if s.UncompressedSize != discardedArchiveUncompressedSize || s.Compression != types.CompressionNone {
		t.Fatalf("run fields must survive: UncompressedSize=%d Compression=%q", s.UncompressedSize, s.Compression)
	}
}

// assertArchiveDescribed pins the stats when the final archive exists: its
// name, its real size and the ratios derived from it, and its real checksum.
func assertArchiveDescribed(t *testing.T, s *BackupStats, a *backupArtifacts) {
	t.Helper()
	data, err := os.ReadFile(a.archivePath)
	if err != nil {
		t.Fatalf("final archive must exist: %v", err)
	}
	sum := sha256.Sum256(data)
	want := int64(len(data))
	if s.ArchivePath != a.archivePath || s.ArchiveSize != want || s.CompressedSize != want {
		t.Fatalf("ArchivePath=%q ArchiveSize=%d CompressedSize=%d; want %q %d %d",
			s.ArchivePath, s.ArchiveSize, s.CompressedSize, a.archivePath, want, want)
	}
	if s.Checksum != hex.EncodeToString(sum[:]) {
		t.Fatalf("Checksum=%q; want the final archive's sha256", s.Checksum)
	}
	ratio := float64(want) / float64(discardedArchiveUncompressedSize)
	if s.CompressionRatio != ratio || s.CompressionRatioPercent != ratio*100 || s.CompressionSavingsPercent != (1-ratio)*100 {
		t.Fatalf("ratios=%v/%v/%v; want %v/%v/%v", s.CompressionRatio, s.CompressionRatioPercent,
			s.CompressionSavingsPercent, ratio, ratio*100, (1-ratio)*100)
	}
}

func requireBackupError(t *testing.T, err error, phase string, code types.ExitCode) {
	t.Helper()
	var be *BackupError
	if !errors.As(err, &be) {
		t.Fatalf("want *BackupError, got %T: %v", err, err)
	}
	if be.Phase != phase || be.Code != code {
		t.Fatalf("Phase=%q Code=%v; want %q %v (err: %v)", be.Phase, be.Code, phase, code, err)
	}
}

// F2: every failure that discards the partial clears what the stats said about
// it, so no channel reports the name and size of an archive that never existed.
func TestVerifyAndWriteBackupArtifacts_DiscardedPartialClearsArchiveStats(t *testing.T) {
	promoteErr := errors.New("F2 fixture: rename refused")
	cases := []struct {
		name    string
		ctx     context.Context
		encrypt bool
		partial func(*testing.T, string)
		fs      func(FS) FS
		phase   string
		code    types.ExitCode
		check   func(*testing.T, error)
	}{
		{
			name:    "verification fails",
			ctx:     context.Background(),
			partial: writeNotATar,
			phase:   "verification",
			code:    types.ExitVerificationError,
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "tar verification failed") {
					t.Fatalf("want the tar listing failure, got %v", err)
				}
			},
		},
		{
			name:  "cancelled during verification",
			ctx:   cancelledContext(),
			phase: "verification",
			code:  types.ExitVerificationError,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "tar verification failed") {
					t.Fatalf("want a cancelled tar listing, got %v", err)
				}
			},
		},
		{
			name:    "checksum fails",
			ctx:     context.Background(),
			encrypt: true,
			partial: writeUnreadableAsFile,
			phase:   "verification",
			code:    types.ExitVerificationError,
			check: func(t *testing.T, err error) {
				if !strings.Contains(err.Error(), "checksum generation failed") {
					t.Fatalf("want the checksum failure, got %v", err)
				}
			},
		},
		{
			name:    "cancelled during checksum",
			ctx:     cancelledContext(),
			encrypt: true,
			partial: writeOpaqueBytes,
			phase:   "verification",
			code:    types.ExitVerificationError,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "checksum generation failed") {
					t.Fatalf("want a cancelled checksum, got %v", err)
				}
			},
		},
		{
			name:  "promote fails",
			ctx:   context.Background(),
			fs:    func(inner FS) FS { return renameErrFS{FS: inner, err: promoteErr} },
			phase: "archive",
			code:  types.ExitArchiveError,
			check: func(t *testing.T, err error) {
				if !errors.Is(err, promoteErr) {
					t.Fatalf("want the promote failure, got %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiscardedArchiveFixture(t, tc.ctx, tc.encrypt, tc.partial)
			if tc.fs != nil {
				f.workspace.fs = tc.fs(f.workspace.fs)
			}

			err := f.orch.verifyAndWriteBackupArtifacts(f.run, f.workspace, f.artifacts)

			requireBackupError(t, err, tc.phase, tc.code)
			tc.check(t, err)
			assertNoArchiveOnDisk(t, f.artifacts)
			assertDiscardedArchiveCleared(t, f.run.stats)
		})
	}
}

// A failure after the promote leaves a real archive under the final name: the
// stats keep describing it.
func TestVerifyAndWriteBackupArtifacts_FailureAfterPromoteKeepsArchiveStats(t *testing.T) {
	f := newDiscardedArchiveFixture(t, context.Background(), false, nil)
	writeErr := errors.New("F2 fixture: checksum sidecar refused")
	f.workspace.fs = writeFileFailFS{FS: f.workspace.fs, failPath: f.artifacts.checksumPath, err: writeErr}

	err := f.orch.verifyAndWriteBackupArtifacts(f.run, f.workspace, f.artifacts)

	requireBackupError(t, err, "verification", types.ExitVerificationError)
	if !errors.Is(err, writeErr) {
		t.Fatalf("want the checksum sidecar failure, got %v", err)
	}
	assertArchiveDescribed(t, f.run.stats, f.artifacts)
}

func TestVerifyAndWriteBackupArtifacts_SuccessDescribesArchive(t *testing.T) {
	f := newDiscardedArchiveFixture(t, context.Background(), false, nil)

	if err := f.orch.verifyAndWriteBackupArtifacts(f.run, f.workspace, f.artifacts); err != nil {
		t.Fatalf("verifyAndWriteBackupArtifacts: %v", err)
	}

	assertArchiveDescribed(t, f.run.stats, f.artifacts)
	if want := f.artifacts.archivePath + ".manifest.json"; f.run.stats.ManifestPath != want {
		t.Fatalf("ManifestPath=%q; want %q", f.run.stats.ManifestPath, want)
	}
	if _, err := os.Stat(f.artifacts.partialPath); !os.IsNotExist(err) {
		t.Fatalf("partial must be gone after promote, stat err=%v", err)
	}
}
