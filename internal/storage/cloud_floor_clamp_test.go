package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// boundManagementCtx floors a deadline-less ctx with managementTimeout and never
// re-floors a ctx that already carries a deadline.
func TestBoundManagementCtx(t *testing.T) {
	prev := cloudManagementTimeoutFloor
	cloudManagementTimeoutFloor = 123 * time.Millisecond
	t.Cleanup(func() { cloudManagementTimeoutFloor = prev })

	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneTimeoutOperation: 0})

	// deadline-less -> floored to the management floor (op=0)
	ctx, cancel := cs.boundManagementCtx(context.Background())
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a deadline-less ctx must be floored")
	}
	if d := time.Until(dl); d <= 0 || d > 250*time.Millisecond {
		t.Fatalf("floored deadline = %s; want ~123ms", d)
	}

	// already-bounded -> passthrough, not re-floored
	parent, pcancel := context.WithTimeout(context.Background(), time.Hour)
	defer pcancel()
	ctx2, cancel2 := cs.boundManagementCtx(parent)
	defer cancel2()
	if dl2, _ := ctx2.Deadline(); time.Until(dl2) < 30*time.Minute {
		t.Fatalf("a bounded ctx was wrongly re-floored: %s", time.Until(dl2))
	}

	// op>0 -> managementTimeout uses RcloneTimeoutOperation
	csOp := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneTimeoutOperation: 7})
	if got := csOp.managementTimeout(); got != 7*time.Second {
		t.Fatalf("managementTimeout(op=7) = %s; want 7s", got)
	}
}

// A wedged management op (List) on the deadline-less run ctx must be floored
// (bounded, not hang) and surface as a NON-CRITICAL StorageError.
func TestListFloorsWedgedDeadlessCtx(t *testing.T) {
	prev := cloudManagementTimeoutFloor
	cloudManagementTimeoutFloor = 100 * time.Millisecond
	t.Cleanup(func() { cloudManagementTimeoutFloor = prev })

	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneTimeoutOperation: 0})
	var sawDeadline atomic.Bool
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		_, ok := ctx.Deadline()
		sawDeadline.Store(ok)
		<-ctx.Done() // wedge until the floor cancels
		return nil, ctx.Err()
	}

	done := make(chan error, 1)
	go func() {
		_, err := cs.List(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		var se *StorageError
		if !errors.As(err, &se) || se.IsCritical {
			t.Fatalf("want a non-critical StorageError, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("List hung: the deadline-less ctx was not floored")
	}
	if !sawDeadline.Load() {
		t.Fatal("exec did not observe a floored deadline")
	}
}

// countLogFiles is a second floored management site: a wedge must be bounded.
func TestCountLogFilesFloorsWedgedDeadlessCtx(t *testing.T) {
	prev := cloudManagementTimeoutFloor
	cloudManagementTimeoutFloor = 100 * time.Millisecond
	t.Cleanup(func() { cloudManagementTimeoutFloor = prev })

	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", CloudLogPath: "logs", RcloneTimeoutOperation: 0})
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	done := make(chan int, 1)
	go func() { done <- cs.countLogFiles(context.Background()) }()
	select {
	case <-done: // returns (-1 best-effort) promptly; not hung
	case <-time.After(3 * time.Second):
		t.Fatal("countLogFiles hung: the deadline-less ctx was not floored")
	}
}

// CLAMP: with RCLONE_TIMEOUT_OPERATION=0 the upload must NOT instant-fail (the old
// WithTimeout(ctx,0) bug) and must reach rclone with an UNBOUNDED ctx (no deadline).
func TestRunUploadTaskOperationZeroIsUnbounded(t *testing.T) {
	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneRetries: 1, RcloneTimeoutOperation: 0})
	var called, sawDeadline atomic.Bool
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		called.Store(true)
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		return []byte(""), nil // copy succeeds
	}

	if err := cs.runUploadTask(context.Background(), uploadTask{local: "/tmp/x", remote: "remote:x", verify: false}); err != nil {
		t.Fatalf("op=0 upload must not instant-fail, got %v", err)
	}
	if !called.Load() {
		t.Fatal("op=0 collapsed to an already-expired ctx: rclone was never invoked")
	}
	if sawDeadline.Load() {
		t.Fatal("op=0 must leave the upload ctx unbounded (no deadline)")
	}
}

// op>0: the upload ctx IS bounded by RcloneTimeoutOperation (the guard's positive arm).
func TestRunUploadTaskOperationPositiveBoundsUpload(t *testing.T) {
	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneRetries: 1, RcloneTimeoutOperation: 30})
	var sawDeadline atomic.Bool
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if _, ok := ctx.Deadline(); ok {
			sawDeadline.Store(true)
		}
		return []byte(""), nil
	}
	if err := cs.runUploadTask(context.Background(), uploadTask{local: "/tmp/x", remote: "remote:x", verify: false}); err != nil {
		t.Fatalf("runUploadTask: %v", err)
	}
	if !sawDeadline.Load() {
		t.Fatal("op>0 must bound the upload ctx with a deadline")
	}
}

// CLAMP at the Store boundary: Store builds its own uploadCtx before delegating, so
// it needs its own guard. With op=0 the rclone copyto must be REACHED (not
// instant-failed by WithTimeout(ctx,0)) and on an UNBOUNDED ctx. We assert only the
// clamp property; Store may still error later on verify (not relevant here).
func TestStoreOperationZeroReachesUploadUnbounded(t *testing.T) {
	tmp := t.TempDir()
	backupFile := filepath.Join(tmp, "pbs1-backup.tar.zst")
	if err := os.WriteFile(backupFile, []byte("primary"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs := newCloudStorageForTest(&config.Config{CloudEnabled: true, CloudRemote: "remote", RcloneRetries: 1, RcloneTimeoutOperation: 0})
	var copyCalled, copySawDeadline atomic.Bool
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "copyto" {
			copyCalled.Store(true)
			if _, ok := ctx.Deadline(); ok {
				copySawDeadline.Store(true)
			}
		}
		return []byte(""), nil
	}

	_ = cs.Store(context.Background(), backupFile, nil)
	if !copyCalled.Load() {
		t.Fatal("op=0 Store instant-failed: rclone copyto was never invoked (WithTimeout(ctx,0) bug)")
	}
	if copySawDeadline.Load() {
		t.Fatal("op=0 Store must leave the upload ctx unbounded (no deadline)")
	}
}

// A wedged deletefile must be floored (deleteBackupInternal's insertion point). The
// snapshot lsl fails (snapshot not ready) so the deletefile is attempted; it then
// wedges and must be cut by the floor instead of hanging the retention path.
func TestDeleteBackupInternalFloorsWedgedDeletefile(t *testing.T) {
	prev := cloudManagementTimeoutFloor
	cloudManagementTimeoutFloor = 100 * time.Millisecond
	t.Cleanup(func() { cloudManagementTimeoutFloor = prev })

	cs := newCloudStorageForTest(&config.Config{CloudRemote: "remote", RcloneTimeoutOperation: 0})
	var sawDeadline atomic.Bool
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if len(args) > 0 && (args[0] == "deletefile" || args[0] == "delete") {
			_, ok := ctx.Deadline()
			sawDeadline.Store(ok)
			<-ctx.Done() // wedge until the floor cancels
			return nil, ctx.Err()
		}
		// snapshot lsl/lsf: fail fast so the snapshot stays empty and deletefile is attempted
		return nil, errors.New("no listing")
	}

	done := make(chan struct{}, 1)
	go func() {
		_, _ = cs.deleteBackupInternal(context.Background(), "pbs1-backup.tar.zst")
		done <- struct{}{}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("deleteBackupInternal hung: the wedged deletefile was not floored")
	}
	if !sawDeadline.Load() {
		t.Fatal("deletefile did not observe a floored deadline")
	}
}
