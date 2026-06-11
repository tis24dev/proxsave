package backup

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regression for the 2026-06-09 audit finding verify-skips-pigz-bzip2-lzma:
// VerifyArchive routed pigz/bzip2/lzma into the "Unknown compression type" default
// branch and returned nil, so a corrupt archive of those (supported) types passed
// verification and only failed at restore. These tests assert a corrupt archive is
// now rejected. Written after the fix, hence the _audited suffix.

// writeCompressibleTree drops one sizeable, low-compressibility file so the
// compressed body spans well past any header, making mid-stream truncation a
// reliable way to corrupt the stream.
func writeAuditPayload(t *testing.T, dir string) {
	t.Helper()
	buf := make([]byte, 256*1024)
	for i := range buf {
		buf[i] = byte((i*31 + i/7) % 251)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), buf, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
}

func TestVerifyArchive_DetectsCorruption_PigzBzip2Lzma(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not available")
	}

	cases := []struct {
		name string
		comp types.CompressionType
		tool string // underlying compressor; if missing, skip (or create falls back)
		ext  string
	}{
		{"pigz", types.CompressionPigz, "pigz", ".tar.gz"},
		{"bzip2", types.CompressionBzip2, "bzip2", ".tar.bz2"},
		{"lzma", types.CompressionLZMA, "lzma", ".tar.lzma"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := exec.LookPath(tc.tool); err != nil {
				// Without the tool, CreateArchive falls back to gzip and this
				// would no longer exercise the target type.
				t.Skipf("%s not available", tc.tool)
			}

			src := t.TempDir()
			writeAuditPayload(t, src)
			archivePath := filepath.Join(t.TempDir(), "backup"+tc.ext)

			logger := logging.New(types.LogLevelInfo, false)
			logger.SetOutput(io.Discard)
			ar := NewArchiver(logger, &ArchiverConfig{
				Compression:        tc.comp,
				CompressionLevel:   6,
				CompressionThreads: 2,
				DryRun:             false,
			})

			ctx := context.Background()
			if err := ar.CreateArchive(ctx, src, archivePath); err != nil {
				t.Fatalf("CreateArchive(%s): %v", tc.name, err)
			}

			// A valid archive must verify clean (also proves the verify command
			// is compatible with the create command for this type).
			if err := ar.VerifyArchive(ctx, archivePath); err != nil {
				t.Fatalf("VerifyArchive on a good %s archive returned error: %v", tc.name, err)
			}

			// Corrupt the compressed body: truncate to 60% so the decompressor
			// hits an unexpected end mid-stream.
			info, err := os.Stat(archivePath)
			if err != nil {
				t.Fatalf("stat archive: %v", err)
			}
			if err := os.Truncate(archivePath, info.Size()*6/10); err != nil {
				t.Fatalf("truncate archive: %v", err)
			}

			// The regression: before routing these types to a real verifier,
			// VerifyArchive returned nil for a corrupt archive.
			if err := ar.VerifyArchive(ctx, archivePath); err == nil {
				t.Fatalf("VerifyArchive accepted a CORRUPT %s archive (integrity check was skipped)", tc.name)
			}
		})
	}
}
