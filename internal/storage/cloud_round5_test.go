package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// RCLONE_RETRIES = 1 and the operation timeout: no attempt line, and the fact gives
// the short cause "timed out after <N>s" instead of the internal error text, for the
// backup ("  Upload failed") and for the log upload the step [8] copy prints.
func TestCloudSingleAttemptTimeoutHasAShortCause(t *testing.T) {
	archive := writeArchiveFixture(t)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 1, RcloneTimeoutOperation: 1}
	blocking := func(ctx context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "copyto" {
			<-ctx.Done()
			return nil, errors.New("signal: killed")
		}
		return nil, nil
	}
	cs, buf := newCloudWithCapturedLog(t, cfg, blocking)
	err := cs.Store(context.Background(), archive, nil)
	if err == nil || AttemptsReported(err) {
		t.Fatalf("Store = %v, want a failure the fact line describes", err)
	}
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO       Upload failed: timed out after 1s"; got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(err.Error(), "upload failed: operation timeout (1s exceeded) after 1 attempts") {
		t.Fatalf("the error text must stay as it was: %v", err)
	}

	cs, _ = newCloudWithCapturedLog(t, cfg, blocking)
	err = cs.UploadToRemotePath(context.Background(), archive, "remote:/logs/x.log", true)
	if err == nil || AttemptsReported(err) || ErrorCause(err) != "timed out after 1s" {
		t.Fatalf("UploadToRemotePath = %v (cause %q), want the short cause timed out after 1s", err, ErrorCause(err))
	}
}

// The log upload whose verification failed after a good copy is a verification
// failure, so step [8] prints "  Verification failed: <error>".
func TestCloudLogUploadVerificationFailure(t *testing.T) {
	src := writeArchiveFixture(t)
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote:backup", RcloneRetries: 1}
	cs, _ := newCloudWithCapturedLog(t, cfg, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "lsl" {
			return []byte("        5 2026-10-03 10:00:00.000000000 x.log\n"), nil
		}
		return nil, nil
	})
	err := cs.UploadToRemotePath(context.Background(), src, "remote:/logs/x.log", true)
	if !VerificationFailed(err) || ErrorCause(err) != "size mismatch: local=7 remote=5" {
		t.Fatalf("UploadToRemotePath = %v (cause %q), want a verification failure", err, ErrorCause(err))
	}
	if VerificationFailed(errors.New("x")) {
		t.Fatalf("a plain error is not a verification failure")
	}
}

// A local CLOUD_REMOTE directory that cannot be created: "  Directory not created:
// <cause>" under the check, the system error without the path (ProxSave creates the
// directory itself, 0700, like the Secondary), not the bare cause.
func TestCloudLocalDirectoryNotCreatedFact(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	writeTestFile(t, blocker, "x")
	cfg := &config.Config{CloudEnabled: true, CloudRemote: filepath.Join(blocker, "cloud"), RcloneTimeoutConnection: 30}
	rec := &argvRecorder{}
	cs, buf := newCloudWithCapturedLog(t, cfg, rec.exec)
	if _, err := cs.DetectFilesystem(context.Background()); err == nil {
		t.Fatalf("DetectFilesystem must fail when the directory cannot be created")
	}
	want := "INFO     Checking cloud remote accessibility...\nINFO       Directory not created: not a directory"
	if got := strings.Join(visibleOf(buf.String()), "\n"); got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if got := rec.argv(); len(got) != 0 {
		t.Fatalf("rclone must not run when the directory is missing: %v", got)
	}
}
