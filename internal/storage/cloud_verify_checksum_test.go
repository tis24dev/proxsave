package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// writeLocalForVerify writes a local file and returns its path, size, and sha256.
func writeLocalForVerify(t *testing.T, content string) (path string, size int, hash string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "file.tar")
	writeTestFile(t, path, content)
	return path, len(content), sha256Hex(content)
}

// TestCloudVerifyUploadChecksum drives VerifyUpload through the primary (lsl) path
// with the checksum step enabled and a stubbed hashsum response.
func TestCloudVerifyUploadChecksum(t *testing.T) {
	const remoteFile = "remote:dir/file.tar"
	content := "primary-archive-bytes"
	wantHash := sha256Hex(content)

	tests := []struct {
		name           string
		verifyDownload bool
		hashsum        []queuedResponse // queued AFTER the lsl size check
		wantOK         bool
		wantErrSubstr  string
	}{
		{
			name:    "native sha256 match",
			hashsum: []queuedResponse{{name: "rclone", out: wantHash + "  file.tar\n"}},
			wantOK:  true,
		},
		{
			name:          "native sha256 mismatch fails",
			hashsum:       []queuedResponse{{name: "rclone", out: strings.Repeat("a", 64) + "  file.tar\n"}},
			wantOK:        false,
			wantErrSubstr: "checksum mismatch",
		},
		{
			name:    "backend without sha256 falls back to size-only",
			hashsum: []queuedResponse{{name: "rclone", out: "\n"}},
			wantOK:  true,
		},
		{
			name:    "hashsum unsupported error falls back",
			hashsum: []queuedResponse{{name: "rclone", out: "Error: hash type sha256 not supported", err: errors.New("exit 1")}},
			wantOK:  true,
		},
		{
			name:          "hashsum transport error is fatal",
			hashsum:       []queuedResponse{{name: "rclone", out: "connection reset", err: errors.New("exit 1")}},
			wantOK:        false,
			wantErrSubstr: "rclone hashsum failed",
		},
		{
			name:    "hash for a different file falls back",
			hashsum: []queuedResponse{{name: "rclone", out: wantHash + "  other.tar\n"}},
			wantOK:  true,
		},
		{
			name:    "malformed non-hex hash falls back",
			hashsum: []queuedResponse{{name: "rclone", out: "not-a-valid-hash  file.tar\n"}},
			wantOK:  true,
		},
		{
			name:           "download forced when no native hash",
			verifyDownload: true,
			hashsum: []queuedResponse{
				{name: "rclone", out: "\n"},                      // first: no native hash
				{name: "rclone", out: wantHash + "  file.tar\n"}, // second: --download succeeds
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localPath, size, _ := writeLocalForVerify(t, content)
			cfg := &config.Config{
				CloudEnabled:           true,
				CloudRemote:            "remote",
				CloudVerifyChecksum:    true,
				CloudVerifyDownload:    tt.verifyDownload,
				RcloneTimeoutOperation: 10,
			}
			cs := newCloudStorageForTest(cfg)

			queue := &commandQueue{t: t}
			// Size pre-check (primary/lsl) succeeds with the matching size.
			queue.queue = append(queue.queue, queuedResponse{
				name: "rclone",
				args: []string{"lsl", remoteFile},
				out:  itoa(size) + " 2025-01-01 00:00:00 file.tar\n",
			})
			queue.queue = append(queue.queue, tt.hashsum...)
			cs.execCommand = queue.exec

			ok, err := cs.VerifyUpload(context.Background(), localPath, remoteFile)
			if tt.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErrSubstr, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("VerifyUpload ok = %v, want %v (err=%v)", ok, tt.wantOK, err)
			}

			// The download case must issue a second hashsum carrying --download.
			if tt.verifyDownload {
				var sawDownload bool
				for _, call := range queue.calls {
					if len(call.args) > 0 && call.args[0] == "hashsum" {
						for _, a := range call.args {
							if a == "--download" {
								sawDownload = true
							}
						}
					}
				}
				if !sawDownload {
					t.Fatalf("expected a hashsum call with --download, calls=%v", queue.calls)
				}
			}
		})
	}
}

// TestCloudVerifyUploadChecksumFilenameWithSpaces covers a remote filename that
// contains spaces. rclone prints the hashsum path unquoted, so the parser must
// reconstruct the full path from the line rather than reading the last
// whitespace-delimited token. The mismatch case is the mutation-prover: with the
// old last-token parser the line would not match the basename and a real checksum
// mismatch would be silently downgraded to a size-only "OK".
func TestCloudVerifyUploadChecksumFilenameWithSpaces(t *testing.T) {
	const remoteFile = "remote:dir/file with spaces.tar"
	content := "spaced-archive-bytes"
	wantHash := sha256Hex(content)

	tests := []struct {
		name          string
		hashOut       string
		wantOK        bool
		wantErrSubstr string
	}{
		{
			name:    "matching hash for a spaced name verifies",
			hashOut: wantHash + "  file with spaces.tar\n",
			wantOK:  true,
		},
		{
			name:          "wrong hash for a spaced name is a mismatch, not a silent downgrade",
			hashOut:       strings.Repeat("a", 64) + "  file with spaces.tar\n",
			wantOK:        false,
			wantErrSubstr: "checksum mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localPath, size, _ := writeLocalForVerify(t, content)
			cfg := &config.Config{
				CloudEnabled:           true,
				CloudRemote:            "remote",
				CloudVerifyChecksum:    true,
				RcloneTimeoutOperation: 10,
			}
			cs := newCloudStorageForTest(cfg)

			queue := &commandQueue{t: t}
			// Size pre-check (lsl) succeeds: verifyPrimary only reads the size field.
			queue.queue = append(queue.queue, queuedResponse{
				name: "rclone",
				args: []string{"lsl", remoteFile},
				out:  itoa(size) + " 2025-01-01 00:00:00 file with spaces.tar\n",
			})
			queue.queue = append(queue.queue, queuedResponse{name: "rclone", out: tt.hashOut})
			cs.execCommand = queue.exec

			ok, err := cs.VerifyUpload(context.Background(), localPath, remoteFile)
			if tt.wantErrSubstr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSubstr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErrSubstr, err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("VerifyUpload ok = %v, want %v (err=%v)", ok, tt.wantOK, err)
			}
		})
	}
}

// When CLOUD_VERIFY_CHECKSUM is off, no hashsum call must be made (legacy
// size-only behavior). The commandQueue fails the test on any unexpected command,
// so queueing only the lsl response asserts hashsum is never called.
func TestCloudVerifyUploadChecksumDisabled(t *testing.T) {
	const remoteFile = "remote:dir/file.tar"
	localPath, size, _ := writeLocalForVerify(t, "data")
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudVerifyChecksum:    false,
		RcloneTimeoutOperation: 10,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"lsl", remoteFile}, out: itoa(size) + " 2025-01-01 00:00:00 file.tar\n"},
		},
	}
	cs.execCommand = queue.exec

	ok, err := cs.VerifyUpload(context.Background(), localPath, remoteFile)
	if err != nil || !ok {
		t.Fatalf("VerifyUpload = %v, %v; want true, nil", ok, err)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected exactly 1 rclone call (lsl only), got %d: %v", len(queue.calls), queue.calls)
	}
}

// A size mismatch must short-circuit before any hashsum call.
func TestCloudVerifyUploadChecksumSizeMismatchShortCircuits(t *testing.T) {
	const remoteFile = "remote:dir/file.tar"
	localPath, size, _ := writeLocalForVerify(t, "data")
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudVerifyChecksum:    true,
		RcloneTimeoutOperation: 10,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// Report a wrong size; no hashsum is queued, so a hashsum call would Fatalf.
			{name: "rclone", args: []string{"lsl", remoteFile}, out: itoa(size+1) + " 2025-01-01 00:00:00 file.tar\n"},
		},
	}
	cs.execCommand = queue.exec

	ok, err := cs.VerifyUpload(context.Background(), localPath, remoteFile)
	if err == nil || ok {
		t.Fatalf("VerifyUpload = %v, %v; want false, error", ok, err)
	}
	if !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The alternative method must also run the SHA256 comparison after its size match.
func TestCloudVerifyUploadChecksumAlternative(t *testing.T) {
	const remoteFile = "remote:file.tar"
	content := "alt-archive"
	localPath, size, wantHash := writeLocalForVerify(t, content)
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudVerifyChecksum:    true,
		RcloneVerifyMethod:     "alternative",
		RcloneTimeoutOperation: 10,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"ls", "remote:"}, out: itoa(size) + " file.tar\n"},
			{name: "rclone", out: wantHash + "  file.tar\n"},
		},
	}
	cs.execCommand = queue.exec

	ok, err := cs.VerifyUpload(context.Background(), localPath, remoteFile)
	if err != nil || !ok {
		t.Fatalf("VerifyUpload (alternative) = %v, %v; want true, nil", ok, err)
	}
}

// A full Store() run with the default CLOUD_VERIFY_CHECKSUM=true must issue a
// hashsum call for the PRIMARY archive (verify:true), and must NOT verify the
// sidecars when CLOUD_PARALLEL_VERIFICATION is off.
func TestCloudStorageStoreRunsChecksumOnPrimary(t *testing.T) {
	tmpDir := t.TempDir()
	backupFile := filepath.Join(tmpDir, "pbs1-backup.tar.zst")
	const primary = "primary"
	writeTestFile(t, backupFile, primary)
	writeTestFile(t, backupFile+".sha256", "sum")
	writeTestFile(t, backupFile+".metadata", "{}")
	writeTestFile(t, backupFile+".metadata.sha256", "meta-sum")
	primaryHash := sha256Hex(primary)

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudRemotePath:        "tenants/a",
		BundleAssociatedFiles:  false,
		CloudVerifyChecksum:    true,
		RcloneRetries:          1,
		RcloneTimeoutOperation: 10,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"copyto", backupFile, "remote:tenants/a/pbs1-backup.tar.zst"}},
			{name: "rclone", args: []string{"lsl", "remote:tenants/a/pbs1-backup.tar.zst"}, out: "7 2025-11-13 10:00:00 pbs1-backup.tar.zst"},
			{name: "rclone", args: []string{"hashsum", "sha256", "remote:tenants/a/pbs1-backup.tar.zst"}, out: primaryHash + "  pbs1-backup.tar.zst\n"},
			{name: "rclone", args: []string{"copyto", backupFile + ".sha256", "remote:tenants/a/pbs1-backup.tar.zst.sha256"}},
			{name: "rclone", args: []string{"copyto", backupFile + ".metadata", "remote:tenants/a/pbs1-backup.tar.zst.metadata"}},
			{name: "rclone", args: []string{"copyto", backupFile + ".metadata.sha256", "remote:tenants/a/pbs1-backup.tar.zst.metadata.sha256"}},
			{name: "rclone", args: []string{"lsl", "remote:tenants/a"}, out: "7 2025-11-13 10:00:00 pbs1-backup.tar.zst"},
		},
	}
	cs.execCommand = queue.exec

	if err := cs.Store(context.Background(), backupFile, nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	var hashsumCalls int
	for _, call := range queue.calls {
		if len(call.args) > 0 && call.args[0] == "hashsum" {
			hashsumCalls++
		}
	}
	if hashsumCalls != 1 {
		t.Fatalf("expected exactly 1 hashsum call (primary only), got %d: %v", hashsumCalls, queue.calls)
	}
}

// Regression lock-in: on a backend without a native SHA256, a successful upload
// must NOT emit a WARNING (which the orchestrator's log parser would count and
// turn into a non-zero exit code). The message belongs at Debug level.
func TestCloudVerifyNoNativeHashEmitsNoWarning(t *testing.T) {
	const remoteFile = "remote:dir/file.tar"
	localPath, size, _ := writeLocalForVerify(t, "data")
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudVerifyChecksum:    true,
		RcloneTimeoutOperation: 10,
	}
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	cs, err := NewCloudStorage(cfg, logger)
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"lsl", remoteFile}, out: itoa(size) + " 2025-01-01 00:00:00 file.tar\n"},
			{name: "rclone", out: "\n"}, // no native hash
		},
	}
	cs.execCommand = queue.exec

	ok, vErr := cs.VerifyUpload(context.Background(), localPath, remoteFile)
	if vErr != nil || !ok {
		t.Fatalf("VerifyUpload = %v, %v; want true, nil", ok, vErr)
	}
	if out := buf.String(); strings.Contains(out, "WARNING") || strings.Contains(out, "ERROR") {
		t.Fatalf("no-native-hash fallback must not log WARNING/ERROR; got:\n%s", out)
	}
}

func TestValidateRcloneArgsAcceptsHashsum(t *testing.T) {
	if err := validateRcloneArgs([]string{"hashsum", "sha256", "remote:file.tar"}); err != nil {
		t.Fatalf("hashsum should be allowed: %v", err)
	}
	if err := validateRcloneArgs([]string{"hashsum", "sha256", ""}); err == nil {
		t.Fatalf("empty operand should still be rejected")
	}
}

// itoa avoids pulling strconv into the test for a single small int conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
