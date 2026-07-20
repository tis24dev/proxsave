package backup

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The stderr-scan goroutine started by attachStderrLogger blocks forever on
// cmd.StderrPipe() if the command is never started. Every early return that
// precedes cmd.Start() must therefore also precede the attach, so no such return
// can leak the goroutine. This mirrors pipeTarThroughCommand, where the attach is
// the last step before the tar goroutine and cmd.Start().
func TestArchiveDoesNotAttachStderrOnOutputOpenFailure(t *testing.T) {
	orig := attachStderrLoggerFn
	t.Cleanup(func() { attachStderrLoggerFn = orig })
	attaches := 0
	attachStderrLoggerFn = func(a *Archiver, cmd *exec.Cmd, algo string) error {
		attaches++
		return orig(a, cmd, algo)
	}

	// A real cmd that is never started on the early-return paths below, so the test
	// does not depend on xz/zstd being installed.
	trueCmd := func(ctx context.Context, _ string, _ ...string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "true"), nil
	}
	logger := logging.New(types.LogLevelError, false)

	// Arm 1: output-file open fails before any goroutine is needed. A path under a
	// non-existent directory makes createBackupOutputFile's os.OpenRoot(dir) fail.
	attaches = 0
	a := NewArchiver(logger, &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 3,
	})
	a.deps.CommandContext = trueCmd
	bad := filepath.Join(t.TempDir(), "no-such-dir", "out.tar.xz")

	if err := a.createXZArchive(context.Background(), t.TempDir(), bad); err == nil {
		t.Fatal("createXZArchive must fail when the output file cannot be created")
	}
	if err := a.createZstdArchive(context.Background(), t.TempDir(), bad); err == nil {
		t.Fatal("createZstdArchive must fail when the output file cannot be created")
	}
	if attaches != 0 {
		t.Fatalf("attachStderrLogger must not run on the output-open-failure path, ran %d times", attaches)
	}

	// Arm 2: encryption setup fails after the output file opens but before the
	// stderr reader should attach. Encryption is enabled with no AGE recipients, so
	// wrapEncryptionWriter errors while createBackupOutputFile still succeeds (the
	// output directory exists). With the attach placed after wrapEncryptionWriter
	// (matching pipeTarThroughCommand) this early return cannot leak the goroutine.
	attaches = 0
	enc := NewArchiver(logger, &ArchiverConfig{
		Compression:      types.CompressionXZ,
		CompressionLevel: 3,
		EncryptArchive:   true,
	})
	enc.deps.CommandContext = trueCmd
	out := filepath.Join(t.TempDir(), "out.tar.xz")

	if err := enc.createXZArchive(context.Background(), t.TempDir(), out); err == nil {
		t.Fatal("createXZArchive must fail when encryption cannot be initialized")
	}
	if attaches != 0 {
		t.Fatalf("attachStderrLogger must not run on the encryption-setup-failure path, ran %d times", attaches)
	}
}
