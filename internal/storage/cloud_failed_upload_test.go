package storage

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// rclone 1.63 and later upload to local, sftp, ftp and pcloud remotes as
// "<name>.<hash>.partial" and rename it at the end; a transfer stopped before the
// rename leaves the partial behind (measured with rclone 1.75.2 under SIGKILL). The
// cloud listing used to admit it as an owned, unverified backup: retention never
// deleted it and reported it as skipped on every run, and the stats counted it.
func TestCloudListSkipsRclonePartialUpload(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", BundleAssociatedFiles: true}
	cs := newCloudStorageForTest(cfg)
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if args[0] != "lsl" {
			t.Fatalf("unexpected rclone call: %v", args)
		}
		return []byte("  2912256 2026-10-27 15:48:43.000000000 pve1-backup-20261027-010000.tar.zst.bundle.tar.1fe602a5.partial\n" +
			" 20971520 2026-10-26 01:05:00.000000000 pve1-backup-20261026-010000.tar.zst.bundle.tar\n"), nil
	}

	backups, err := cs.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(backups) != 1 || backups[0].BackupFile != "pve1-backup-20261026-010000.tar.zst.bundle.tar" {
		var names []string
		for _, b := range backups {
			names = append(names, b.BackupFile)
		}
		t.Fatalf("listed %v, want only the completed bundle", names)
	}
}

// A done ctx stops rclone with SIGTERM: rclone 1.63 and later then remove the partial
// of the upload they did not finish. rclone handles the signal and exits 143 (measured
// with rclone 1.75.2), which the cause helpers would read as an uninformative "exit
// status N"; it is reported as the signal it got.
func TestDefaultRunCommandStopsWithSIGTERM(t *testing.T) {
	old := cloudExecWaitDelay
	cloudExecWaitDelay = 10 * time.Second
	t.Cleanup(func() { cloudExecWaitDelay = old })

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	// Like rclone: on SIGTERM it cleans up and exits 143.
	_, err := defaultRunCommand(ctx, "sh", "-c", "trap 'kill $! 2>/dev/null; exit 143' TERM; sleep 30 >/dev/null 2>&1 & wait")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the process did not stop on SIGTERM: %s", d)
	}
	if err == nil || err.Error() != "signal: terminated" {
		t.Fatalf("err = %v, want signal: terminated", err)
	}
	exitErr, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exitErr.ExitCode() != 143 {
		t.Fatalf("err does not wrap the exit status 143: %#v", err)
	}
	if got := rcloneCause("2026/10/27 15:52:54 NOTICE: Config file \"/root/.config/rclone/rclone.conf\" not found - using defaults", err); got != "signal: terminated" {
		t.Fatalf("cause = %q, want the stop, not rclone's NOTICE", got)
	}
}

// One that does not exit on SIGTERM is SIGKILLed once WaitDelay has passed.
func TestDefaultRunCommandKillsWhatIgnoresSIGTERM(t *testing.T) {
	old := cloudExecWaitDelay
	cloudExecWaitDelay = 200 * time.Millisecond
	t.Cleanup(func() { cloudExecWaitDelay = old })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := defaultRunCommand(ctx, "sh", "-c", "trap '' TERM; exec sleep 30")
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("WaitDelay did not bound the wait: %s", d)
	}
	if err == nil || err.Error() != "signal: killed" {
		t.Fatalf("err = %v, want signal: killed", err)
	}
}

// Exit 143 with the ctx still live is rclone's own status, not a stop: it stays as is.
func TestDefaultRunCommandKeepsExit143WithoutStop(t *testing.T) {
	_, err := defaultRunCommand(context.Background(), "sh", "-c", "exit 143")
	if err == nil || err.Error() != "exit status 143" {
		t.Fatalf("err = %v, want exit status 143", err)
	}
}

func failedUploadFixture(t *testing.T) (string, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	backupFile := filepath.Join(tmp, "pve1-backup-20261027-010000.tar.zst")
	writeTestFile(t, backupFile, "archive")
	writeTestFile(t, backupFile+".bundle.tar", "bundle-bytes")
	return backupFile, &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		BundleAssociatedFiles:   true,
		RcloneRetries:           1,
		RcloneTimeoutOperation:  1,
		RcloneTimeoutConnection: 7,
	}
}

// rclone before 1.63 (the Debian package on PVE 8 and 9) writes local, sftp, ftp and
// pcloud uploads in place: a copy stopped midway leaves a truncated bundle at its final
// name (measured: 2977792 of 20971520 bytes, under SIGTERM and SIGKILL alike). The
// listing counts every bundle as verified, so retention kept it and deleted a good
// backup in its place. Store now deletes it, on a bound of its own.
func TestStoreDeletesWhatAFailedCopyLeft(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		backupFile, cfg := failedUploadFixture(t)
		cs := newCloudStorageForTest(cfg)
		var deleted []string
		cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			switch args[0] {
			case "copyto":
				<-ctx.Done()
				return nil, errors.New("signal: terminated")
			case "deletefile":
				if ctx.Err() != nil {
					t.Errorf("the cleanup ran on a done ctx: %v", ctx.Err())
				}
				if dl, ok := ctx.Deadline(); !ok || time.Until(dl) != 7*time.Second {
					t.Errorf("cleanup deadline = %v (set %v), want RCLONE_TIMEOUT_CONNECTION (7s)", time.Until(dl), ok)
				}
				deleted = append(deleted, args[len(args)-1])
				return nil, nil
			}
			t.Fatalf("unexpected rclone call: %v", args)
			return nil, nil
		}

		if err := cs.Store(context.Background(), backupFile, nil); err == nil {
			t.Fatal("Store must fail: the copy did not complete")
		}
		if len(deleted) != 1 || deleted[0] != "remote:pve1-backup-20261027-010000.tar.zst.bundle.tar" {
			t.Fatalf("deleted %v, want the bundle at its final name", deleted)
		}
	})
}

// The run cancelled during the copy (Ctrl+C, systemctl stop) is the other way a copy
// stops midway: the cleanup still runs, on its own bound.
func TestStoreDeletesWhatACancelledCopyLeft(t *testing.T) {
	backupFile, cfg := failedUploadFixture(t)
	cs := newCloudStorageForTest(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var deleted []string
	cs.execCommand = func(callCtx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "copyto":
			cancel()
			return nil, errors.New("signal: terminated")
		case "deletefile":
			if callCtx.Err() != nil {
				t.Errorf("the cleanup ran on a done ctx: %v", callCtx.Err())
			}
			deleted = append(deleted, args[len(args)-1])
			return nil, nil
		}
		t.Fatalf("unexpected rclone call: %v", args)
		return nil, nil
	}

	if err := cs.Store(ctx, backupFile, nil); err == nil {
		t.Fatal("Store must fail: the copy did not complete")
	}
	if len(deleted) != 1 {
		t.Fatalf("deleted %v, want the bundle at its final name", deleted)
	}
}

// A copy that went through and failed only its verification keeps its object: it may
// be good, and nothing says it is truncated.
func TestStoreKeepsTheObjectWhenOnlyVerificationFailed(t *testing.T) {
	backupFile, cfg := failedUploadFixture(t)
	cs := newCloudStorageForTest(cfg)
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "copyto":
			return nil, nil
		case "lsl":
			return []byte("3 2026-10-27 01:05:00 pve1-backup-20261027-010000.tar.zst.bundle.tar\n"), nil
		case "deletefile":
			t.Errorf("deleted %v after a failed verification", args)
			return nil, nil
		}
		t.Fatalf("unexpected rclone call: %v", args)
		return nil, nil
	}

	err := cs.Store(context.Background(), backupFile, nil)
	if !VerificationFailed(err) {
		t.Fatalf("err = %v, want a verification failure", err)
	}
}
