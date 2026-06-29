package orchestrator

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// verifyStagedArchiveIntegrity must thread FS_IO_TIMEOUT into the bounded checksum
// verify: a staged archive on a wedged mount (a FIFO with no writer) must time out,
// not hang. A regression that drops the timeout (reverts to the unbounded
// VerifyChecksum wrapper, or passes 0) would hang here and trip the guard.
func TestVerifyStagedArchiveIntegrityThreadsTimeout(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "archive.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unsupported: %v", err)
	}
	t.Cleanup(func() {
		if w, e := os.OpenFile(fifo, os.O_WRONLY, 0); e == nil {
			_ = w.Close() // release the abandoned blocked open
		}
	})

	cand := &backupCandidate{
		Integrity: &stagedIntegrityExpectation{
			Checksum: strings.Repeat("a", 64),
			Source:   "checksum file",
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := verifyStagedArchiveIntegrity(
			context.Background(),
			logging.New(types.LogLevelError, false),
			stagedFiles{ArchivePath: fifo},
			cand,
			time.Second,
		)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, safefs.ErrTimeout) {
			t.Fatalf("want safefs.ErrTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("verifyStagedArchiveIntegrity hung: FS_IO_TIMEOUT was not threaded into the bounded verify")
	}
}

// preparePlainBundleCommon must thread its timeout into the plain-archive hash
// (backup.GenerateChecksumBounded). The decrypt callback writes the plain archive,
// so we make it a FIFO: the bounded hash then wedges on the read and the threaded
// timeout must turn it into safefs.ErrTimeout, not a hang. A paired writer goroutine
// is released after the assertion so the abandoned worker EOFs cleanly (no leak).
func TestPreparePlainBundleCommon_ThreadsTimeoutIntoPlainChecksum(t *testing.T) {
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	workArchive := filepath.Join(dir, "backup.tar.xz.age")
	if err := os.WriteFile(workArchive, []byte("ciphertext"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	manifestPath := filepath.Join(dir, "backup.metadata")
	if err := os.WriteFile(manifestPath, []byte(`{"encryption_mode":"age"}`), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	checksumPath := filepath.Join(dir, "backup.sha256")
	if err := os.WriteFile(checksumPath, checksumLineForBytes(filepath.Base(workArchive), []byte("ciphertext")), 0o600); err != nil {
		t.Fatalf("write checksum: %v", err)
	}

	cand := &backupCandidate{
		Manifest:        &backup.Manifest{ArchivePath: workArchive, EncryptionMode: "age"},
		Source:          sourceRaw,
		RawArchivePath:  workArchive,
		RawMetadataPath: manifestPath,
		RawChecksumPath: checksumPath,
		DisplayBase:     "backup.tar.xz.age",
	}
	logger := logging.New(types.LogLevelError, false)
	logger.SetOutput(io.Discard)

	release := make(chan struct{})
	var wg sync.WaitGroup
	skipped := make(chan struct{}, 1)

	decrypt := func(ctx context.Context, encryptedPath, outputPath, displayName string) error {
		if err := syscall.Mkfifo(outputPath, 0o600); err != nil {
			skipped <- struct{}{}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			w, e := os.OpenFile(outputPath, os.O_WRONLY, 0)
			if e != nil {
				return
			}
			<-release
			_ = w.Close()
		}()
		return nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := preparePlainBundleCommon(context.Background(), cand, "1.0.0", logger, decrypt, time.Second)
		done <- err
	}()

	select {
	case err := <-done:
		select {
		case <-skipped:
			close(release)
			wg.Wait()
			t.Skip("mkfifo unsupported")
		default:
		}
		if !errors.Is(err, safefs.ErrTimeout) {
			close(release)
			wg.Wait()
			t.Fatalf("want safefs.ErrTimeout from the plain-archive hash, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("preparePlainBundleCommon hung: FS_IO_TIMEOUT was not threaded into GenerateChecksumBounded")
	}

	close(release)
	wg.Wait()
}
