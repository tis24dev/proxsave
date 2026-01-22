package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

type commandCall struct {
	name string
	args []string
}

type queuedResponse struct {
	name string
	args []string
	out  string
	err  error
}

type commandQueue struct {
	t     *testing.T
	queue []queuedResponse
	calls []commandCall
}

func (q *commandQueue) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	q.calls = append(q.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if len(q.queue) == 0 {
		q.t.Fatalf("unexpected command: %s %v", name, args)
	}
	resp := q.queue[0]
	q.queue = q.queue[1:]

	if resp.name != "" && resp.name != name {
		q.t.Fatalf("expected command %s, got %s", resp.name, name)
	}
	if resp.args != nil {
		if len(resp.args) != len(args) {
			q.t.Fatalf("expected args %v, got %v", resp.args, args)
		}
		for i := range resp.args {
			if resp.args[i] != args[i] {
				q.t.Fatalf("expected args %v, got %v", resp.args, args)
			}
		}
	}
	return []byte(resp.out), resp.err
}

func newCloudStorageForTest(cfg *config.Config) *CloudStorage {
	cs, _ := NewCloudStorage(cfg, newTestLogger())
	return cs
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o640); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}

func TestCloudStorageDetectFilesystem_RcloneMissingReturnsRecoverableError(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		RcloneTimeoutConnection: 5,
	}
	cs := newCloudStorageForTest(cfg)
	cs.lookPath = func(name string) (string, error) {
		return "", errors.New("missing")
	}

	info, err := cs.DetectFilesystem(context.Background())
	if info != nil {
		t.Fatalf("DetectFilesystem info=%+v; want nil", info)
	}
	var se *StorageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StorageError, got %T: %v", err, err)
	}
	if se.Location != LocationCloud {
		t.Fatalf("Location=%v; want %v", se.Location, LocationCloud)
	}
	if se.IsCritical {
		t.Fatalf("IsCritical=true; want false")
	}
	if !se.Recoverable {
		t.Fatalf("Recoverable=false; want true")
	}
}

func TestRemoteCheckErrorUnwrap(t *testing.T) {
	base := errors.New("root")
	err := &remoteCheckError{msg: "wrapped", err: base}
	if !errors.Is(err, base) {
		t.Fatalf("expected errors.Is to match wrapped error")
	}

	var nilErr *remoteCheckError
	if nilErr.Unwrap() != nil {
		t.Fatalf("nil Unwrap() should return nil")
	}
}

func TestCloudStorageMarkCloudLogPathAvailableClearsMissing(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "remote",
	}
	cs := newCloudStorageForTest(cfg)

	cs.markCloudLogPathMissing("remote:logs", "not found")
	if !cs.isCloudLogPathUnavailable() {
		t.Fatalf("expected log path to be marked missing")
	}

	cs.markCloudLogPathAvailable()
	if cs.isCloudLogPathUnavailable() {
		t.Fatalf("expected log path missing flag to be cleared")
	}
}

func TestDefaultExecCommandReturnsErrorForMissingCommand(t *testing.T) {
	_, err := defaultExecCommand(context.Background(), "proxsave-command-does-not-exist")
	if err == nil {
		t.Fatalf("expected error for missing command")
	}
}

func TestCloudStorageVerifyAlternativeSucceeds(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "remote",
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"ls", "remote:dir"},
				out:  "123 file.tar\n",
			},
		},
	}
	cs.execCommand = queue.exec

	ok, err := cs.verifyAlternative(context.Background(), "remote:dir/file.tar", 123, "file.tar")
	if err != nil || !ok {
		t.Fatalf("verifyAlternative returned %v, %v; want true, nil", ok, err)
	}
}

func TestCloudStorageVerifyAlternativeRejectsSizeMismatch(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "remote",
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"ls", "remote:"},
				out:  "124 file.tar\n",
			},
		},
	}
	cs.execCommand = queue.exec

	ok, err := cs.verifyAlternative(context.Background(), "remote:file.tar", 123, "file.tar")
	if err == nil || ok {
		t.Fatalf("verifyAlternative returned %v, %v; want false, error", ok, err)
	}
	if !strings.Contains(err.Error(), "size mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCloudStorageUploadTasksParallelRunsAllTasks(t *testing.T) {
	tmpDir := t.TempDir()
	local1 := filepath.Join(tmpDir, "one.tar")
	local2 := filepath.Join(tmpDir, "two.tar")
	if err := os.WriteFile(local1, []byte("1"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(local2, []byte("2"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudParallelJobs:      2,
		RcloneRetries:          1,
		RcloneTimeoutOperation: 5,
	}
	cs := newCloudStorageForTest(cfg)

	var mu sync.Mutex
	var calls []commandCall
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "rclone" {
			t.Fatalf("unexpected command %q", name)
		}
		if len(args) == 0 || args[0] != "copyto" {
			t.Fatalf("unexpected rclone args %v", args)
		}
		mu.Lock()
		calls = append(calls, commandCall{name: name, args: append([]string(nil), args...)})
		mu.Unlock()
		return []byte("ok"), nil
	}

	tasks := []uploadTask{
		{local: local1, remote: "remote:one.tar", verify: false},
		{local: local2, remote: "remote:two.tar", verify: false},
	}

	failed, err := cs.uploadTasksParallel(context.Background(), tasks)
	if err != nil || failed {
		t.Fatalf("uploadTasksParallel returned %v, %v; want false, nil", failed, err)
	}

	mu.Lock()
	gotCalls := len(calls)
	mu.Unlock()
	if gotCalls != len(tasks) {
		t.Fatalf("calls=%d; want %d", gotCalls, len(tasks))
	}
}

func TestCloudStorageUploadWithRetryEventuallySucceeds(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		RcloneRetries:          3,
		RcloneTimeoutOperation: 5,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", err: errors.New("copy failed")},
			{name: "rclone", err: errors.New("copy failed again")},
			{name: "rclone", out: "ok"},
		},
	}
	cs.execCommand = queue.exec
	cs.sleep = func(time.Duration) {}

	if err := cs.uploadWithRetry(context.Background(), "/tmp/local.tar", "remote:local.tar"); err != nil {
		t.Fatalf("uploadWithRetry() error = %v", err)
	}
	if len(queue.calls) != 3 {
		t.Fatalf("expected 3 upload attempts, got %d", len(queue.calls))
	}
}

func TestCloudStorageListParsesBackups(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "remote",
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"lsl", "remote:"},
				out: strings.TrimSpace(`
99999 2024-11-12 12:00:00 host-backup-20241112.tar.zst
12000 2024-11-10 08:00:00 proxmox-backup-legacy.tar.gz
555 random line ignored
`),
			},
		},
	}
	cs.execCommand = queue.exec

	backups, err := cs.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("List() = %d backups, want 2", len(backups))
	}
	if backups[0].BackupFile != "host-backup-20241112.tar.zst" {
		t.Fatalf("expected newest backup first, got %s", backups[0].BackupFile)
	}
	if backups[1].BackupFile != "proxmox-backup-legacy.tar.gz" {
		t.Fatalf("expected legacy backup second, got %s", backups[1].BackupFile)
	}
}

func TestCloudStorageDeleteSkipsMissingBundleCandidates(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:          true,
		CloudRemote:           "remote",
		BundleAssociatedFiles: true,
	}
	cs := newCloudStorageForTest(cfg)
	listOutput := strings.TrimSpace(`
100 2025-01-01 01:00:00 backup/host-backup-20250101-010101.tar.xz
10 2025-01-01 01:00:00 backup/host-backup-20250101-010101.tar.xz.sha256
10 2025-01-01 01:00:00 backup/host-backup-20250101-010101.tar.xz.metadata
10 2025-01-01 01:00:00 backup/host-backup-20250101-010101.tar.xz.metadata.sha256
`)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"lsl", "remote:"}, out: listOutput},
			{name: "rclone", args: []string{"deletefile", "remote:backup/host-backup-20250101-010101.tar.xz"}},
			{name: "rclone", args: []string{"deletefile", "remote:backup/host-backup-20250101-010101.tar.xz.sha256"}},
			{name: "rclone", args: []string{"deletefile", "remote:backup/host-backup-20250101-010101.tar.xz.metadata"}},
			{name: "rclone", args: []string{"deletefile", "remote:backup/host-backup-20250101-010101.tar.xz.metadata.sha256"}},
		},
	}
	cs.execCommand = queue.exec

	backups, err := cs.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}

	if err := cs.Delete(context.Background(), backups[0].BackupFile); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(queue.calls) != 5 {
		t.Fatalf("expected 5 rclone calls (list + 4 deletes), got %d", len(queue.calls))
	}
}

func TestCloudStorageApplyRetentionDeletesOldest(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:          true,
		CloudRemote:           "remote",
		CloudBatchSize:        1,
		CloudBatchPause:       0,
		BundleAssociatedFiles: false,
	}
	cs := newCloudStorageForTest(cfg)
	cs.sleep = func(time.Duration) {}

	listOutput := strings.TrimSpace(`
100 2024-11-12 10:00:00 gamma-backup-3.tar.zst
100 2024-11-11 10:00:00 beta-backup-2.tar.zst
100 2024-11-10 10:00:00 alpha-backup-1.tar.zst
`)
	recountOutput := strings.TrimSpace(`
100 2024-11-12 10:00:00 gamma-backup-3.tar.zst
100 2024-11-11 10:00:00 beta-backup-2.tar.zst
`)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"lsl", "remote:"}, out: listOutput},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst"}},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.sha256"}},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.metadata"}},
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup-1.tar.zst.metadata.sha256"}},
			{name: "rclone", args: []string{"lsl", "remote:"}, out: recountOutput},
		},
	}
	cs.execCommand = queue.exec

	retentionCfg := RetentionConfig{Policy: "simple", MaxBackups: 2}
	deleted, err := cs.ApplyRetention(context.Background(), retentionCfg)
	if err != nil {
		t.Fatalf("ApplyRetention() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("ApplyRetention() deleted = %d, want 1", deleted)
	}
}

func TestCloudStorageStoreUploadsWithRemotePrefix(t *testing.T) {
	tmpDir := t.TempDir()
	backupFile := filepath.Join(tmpDir, "pbs1-backup.tar.zst")
	writeTestFile(t, backupFile, "primary")
	writeTestFile(t, backupFile+".sha256", "sum")
	writeTestFile(t, backupFile+".metadata", "{}")
	writeTestFile(t, backupFile+".metadata.sha256", "meta-sum")

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		CloudRemotePath:        "tenants/a",
		BundleAssociatedFiles:  false,
		RcloneRetries:          1,
		RcloneTimeoutOperation: 10,
	}

	cs := newCloudStorageForTest(cfg)
	cs.sleep = func(time.Duration) {}
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile, "remote:tenants/a/pbs1-backup.tar.zst"}},
			{name: "rclone", args: []string{"lsl", "remote:tenants/a/pbs1-backup.tar.zst"}, out: "7 2025-11-13 10:00:00 pbs1-backup.tar.zst"},
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile + ".sha256", "remote:tenants/a/pbs1-backup.tar.zst.sha256"}},
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile + ".metadata", "remote:tenants/a/pbs1-backup.tar.zst.metadata"}},
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile + ".metadata.sha256", "remote:tenants/a/pbs1-backup.tar.zst.metadata.sha256"}},
			{name: "rclone", args: []string{"lsl", "remote:tenants/a"}, out: "7 2025-11-13 10:00:00 pbs1-backup.tar.zst"},
		},
	}
	cs.execCommand = queue.exec

	if err := cs.Store(context.Background(), backupFile, nil); err != nil {
		t.Fatalf("Store() error = %v", err)
	}
	if len(queue.calls) != 6 {
		t.Fatalf("expected 6 rclone calls, got %d", len(queue.calls))
	}
}

func TestCloudStorageStorePrimaryFailure(t *testing.T) {
	tmpDir := t.TempDir()
	backupFile := filepath.Join(tmpDir, "pbs1-backup.tar.zst")
	writeTestFile(t, backupFile, "primary")

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		BundleAssociatedFiles:  false,
		RcloneRetries:          1,
		RcloneTimeoutOperation: 5,
	}

	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile, "remote:pbs1-backup.tar.zst"}, err: errors.New("boom")},
		},
	}
	cs.execCommand = queue.exec

	err := cs.Store(context.Background(), backupFile, nil)
	if err == nil {
		t.Fatal("Store() expected error, got nil")
	}
	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T", err)
	}
	if storageErr.Operation != "upload" {
		t.Fatalf("StorageError.Operation = %s; want upload", storageErr.Operation)
	}
}

func TestCloudStorageStoreAssociatedFailure(t *testing.T) {
	tmpDir := t.TempDir()
	backupFile := filepath.Join(tmpDir, "pbs1-backup.tar.zst")
	writeTestFile(t, backupFile, "primary")
	writeTestFile(t, backupFile+".sha256", "sum")

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		BundleAssociatedFiles:  false,
		RcloneRetries:          1,
		RcloneTimeoutOperation: 5,
	}

	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile, "remote:pbs1-backup.tar.zst"}},
			{name: "rclone", args: []string{"lsl", "remote:pbs1-backup.tar.zst"}, out: "7 2025-11-13 10:00:00 pbs1-backup.tar.zst"},
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", backupFile + ".sha256", "remote:pbs1-backup.tar.zst.sha256"}, err: errors.New("assoc failed")},
		},
	}
	cs.execCommand = queue.exec

	err := cs.Store(context.Background(), backupFile, nil)
	if err == nil {
		t.Fatal("Store() expected error, got nil")
	}
	var storageErr *StorageError
	if !errors.As(err, &storageErr) {
		t.Fatalf("expected StorageError, got %T", err)
	}
	if storageErr.Operation != "upload_associated" {
		t.Fatalf("StorageError.Operation = %s; want upload_associated", storageErr.Operation)
	}
}

func TestCloudStorageUploadToRemotePath(t *testing.T) {
	tmpDir := t.TempDir()
	localFile := filepath.Join(tmpDir, "logfile.txt")
	writeTestFile(t, localFile, "log")

	cfg := &config.Config{
		CloudEnabled:           true,
		CloudRemote:            "remote",
		RcloneRetries:          1,
		RcloneTimeoutOperation: 5,
	}

	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"copyto", "--progress", "--stats", "10s", localFile, "other:logs/logfile.txt"}},
			{name: "rclone", args: []string{"lsl", "other:logs/logfile.txt"}, out: "3 2025-11-13 10:00:00 logfile.txt"},
		},
	}
	cs.execCommand = queue.exec

	if err := cs.UploadToRemotePath(context.Background(), localFile, "other:logs/logfile.txt", true); err != nil {
		t.Fatalf("UploadToRemotePath() error = %v", err)
	}
}

func TestCloudStorageSkipsCloudLogsWhenPathMissing(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:  true,
		CloudRemote:   "remote",
		CloudLogPath:  "remote:logs",
		RcloneRetries: 1,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"lsf", "remote:logs", "--files-only"},
				out:  "2025/11/16 22:11:47 ERROR : remote:logs: directory not found",
				err:  errors.New("exit status 3"),
			},
		},
	}
	cs.execCommand = queue.exec

	if got := cs.countLogFiles(context.Background()); got != -1 {
		t.Fatalf("countLogFiles() = %d; want -1", got)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected 1 rclone call, got %d", len(queue.calls))
	}

	if got := cs.countLogFiles(context.Background()); got != -1 {
		t.Fatalf("countLogFiles() second call = %d; want -1", got)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected no additional rclone calls, got %d", len(queue.calls))
	}

	if deleted := cs.deleteAssociatedLog(context.Background(), "host-backup-20250101-010101.tar.xz"); deleted {
		t.Fatal("deleteAssociatedLog() returned true; expected false when log path missing")
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected no rclone delete when log path missing, got %d calls", len(queue.calls))
	}
}

func TestCloudStorageCountLogFiles_NormalizesPathOnlyCloudLogPath(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:  true,
		CloudRemote:   "remote:base-prefix",
		CloudLogPath:  "/logs/",
		RcloneRetries: 1,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"lsf", "remote:/logs", "--files-only"},
				out:  "backup-host-20250101-010101.log\n",
			},
		},
	}
	cs.execCommand = queue.exec

	if got := cs.countLogFiles(context.Background()); got != 1 {
		t.Fatalf("countLogFiles() = %d; want 1", got)
	}
}

func TestCloudStorageSkipsCloudLogsWhenPathOnlyLogDirMissing(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:  true,
		CloudRemote:   "remote",
		CloudLogPath:  "/logs",
		RcloneRetries: 1,
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"lsf", "remote:/logs", "--files-only"},
				out:  "2025/11/16 22:11:47 ERROR : remote:/logs: directory not found",
				err:  errors.New("exit status 3"),
			},
		},
	}
	cs.execCommand = queue.exec

	if got := cs.countLogFiles(context.Background()); got != -1 {
		t.Fatalf("countLogFiles() = %d; want -1", got)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected 1 rclone call, got %d", len(queue.calls))
	}

	if got := cs.countLogFiles(context.Background()); got != -1 {
		t.Fatalf("countLogFiles() second call = %d; want -1", got)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected no additional rclone calls, got %d", len(queue.calls))
	}
}

func TestCloudStorageRemoteHelpersAndBuildArgs(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:    true,
		CloudRemote:     "remote",
		CloudRemotePath: "tenant/a",
		RcloneFlags:     []string{"-v", "--fast-list"},
	}
	cs := newCloudStorageForTest(cfg)

	if got := cs.remoteLabel(); got != "remote:tenant/a" {
		t.Fatalf("remoteLabel() = %q; want %q", got, "remote:tenant/a")
	}
	if got := cs.remoteBase(); got != "remote:tenant/a" {
		t.Fatalf("remoteBase() = %q; want %q", got, "remote:tenant/a")
	}
	if got := cs.remoteRoot(); got != "remote:" {
		t.Fatalf("remoteRoot() = %q; want %q", got, "remote:")
	}

	if got := cs.remotePathFor("../escape/backup.tar.zst"); got != "remote:tenant/a/backup.tar.zst" {
		t.Fatalf("remotePathFor() sanitized path = %q; want %q", got, "remote:tenant/a/backup.tar.zst")
	}

	args := cs.buildRcloneArgs("lsf")
	if len(args) < 4 || args[0] != "rclone" || args[1] != "lsf" {
		t.Fatalf("buildRcloneArgs prefix = %#v; want [\"rclone\",\"lsf\",...]", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-v") || !strings.Contains(joined, "--fast-list") {
		t.Fatalf("buildRcloneArgs flags missing in %q", joined)
	}
}

func TestCloudStorageRemoteNormalizationVariants(t *testing.T) {
	tests := []struct {
		name            string
		cloudRemote     string
		cloudRemotePath string
		wantLabel       string
		wantRoot        string
		wantRemoteFile  string
	}{
		{
			name:            "remote name plus prefix",
			cloudRemote:     "remote",
			cloudRemotePath: "tenants/a",
			wantLabel:       "remote:tenants/a",
			wantRoot:        "remote:",
			wantRemoteFile:  "remote:tenants/a/backup.tar.zst",
		},
		{
			name:            "remote with base path no extra prefix",
			cloudRemote:     "remote:pbs-backups",
			cloudRemotePath: "",
			wantLabel:       "remote:pbs-backups",
			wantRoot:        "remote:",
			wantRemoteFile:  "remote:pbs-backups/backup.tar.zst",
		},
		{
			name:            "remote with base path and extra prefix",
			cloudRemote:     "remote:pbs-backups",
			cloudRemotePath: "server1",
			wantLabel:       "remote:pbs-backups/server1",
			wantRoot:        "remote:",
			wantRemoteFile:  "remote:pbs-backups/server1/backup.tar.zst",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				CloudEnabled:    true,
				CloudRemote:     tt.cloudRemote,
				CloudRemotePath: tt.cloudRemotePath,
			}
			cs := newCloudStorageForTest(cfg)

			if got := cs.remoteLabel(); got != tt.wantLabel {
				t.Fatalf("remoteLabel() = %q; want %q", got, tt.wantLabel)
			}
			if got := cs.remoteBase(); got != tt.wantLabel {
				t.Fatalf("remoteBase() = %q; want %q", got, tt.wantLabel)
			}
			if got := cs.remoteRoot(); got != tt.wantRoot {
				t.Fatalf("remoteRoot() = %q; want %q", got, tt.wantRoot)
			}
			if got := cs.remotePathFor("backup.tar.zst"); got != tt.wantRemoteFile {
				t.Fatalf("remotePathFor() = %q; want %q", got, tt.wantRemoteFile)
			}
		})
	}
}

func TestSplitRemoteRefAndBaseName(t *testing.T) {
	tests := []struct {
		ref          string
		wantRemote   string
		wantRel      string
		wantBaseName string
	}{
		{"remote:", "remote", "", ""},
		{"remote:path/to/file.tar.zst", "remote", "path/to/file.tar.zst", "file.tar.zst"},
		{"remote:/leading/slash.tar", "remote", "/leading/slash.tar", "slash.tar"},
		{"nocolon", "nocolon", "", ""},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.ref, func(t *testing.T) {
			gotRemote, gotRel := splitRemoteRef(tt.ref)
			if gotRemote != tt.wantRemote || gotRel != tt.wantRel {
				t.Fatalf("splitRemoteRef(%q) = (%q,%q); want (%q,%q)", tt.ref, gotRemote, gotRel, tt.wantRemote, tt.wantRel)
			}
			if gotBase := remoteBaseName(tt.ref); gotBase != tt.wantBaseName {
				t.Fatalf("remoteBaseName(%q) = %q; want %q", tt.ref, gotBase, tt.wantBaseName)
			}
		})
	}
}

func TestNormalizeRemoteRelativePathAndObjectNotFound(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{".", ""},
		{" ././foo/bar ", "foo/bar"},
		{"/", ""},
		{"../up/../file.tar", "file.tar"},
	}
	for _, c := range cases {
		if got := normalizeRemoteRelativePath(c.in); got != c.want {
			t.Fatalf("normalizeRemoteRelativePath(%q) = %q; want %q", c.in, got, c.want)
		}
	}

	if !isRcloneObjectNotFound("2025/01/01 ERROR : object not found") {
		t.Fatal("isRcloneObjectNotFound should detect 'object not found'")
	}
	if !isRcloneObjectNotFound("file doesn't exist on remote") {
		t.Fatal("isRcloneObjectNotFound should detect \"doesn't exist\"")
	}
	if isRcloneObjectNotFound("permission denied") {
		t.Fatal("isRcloneObjectNotFound should be false for unrelated errors")
	}
	if isRcloneObjectNotFound("") {
		t.Fatal("isRcloneObjectNotFound should be false for empty string")
	}
}

func TestCloudStorageMetadataHelpers(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:    true,
		CloudRemote:     "remote",
		CloudRemotePath: "tenant/projects",
	}
	cs := newCloudStorageForTest(cfg)

	if got := cs.Name(); got != "Cloud Storage (rclone)" {
		t.Fatalf("Name() = %s", got)
	}
	if cs.Location() != LocationCloud {
		t.Fatalf("Location() = %v, want %v", cs.Location(), LocationCloud)
	}
	if !cs.IsEnabled() {
		t.Fatal("IsEnabled() returned false")
	}
	if cs.IsCritical() {
		t.Fatal("cloud storage should be non-critical")
	}
	if got := cs.remoteRoot(); got != "remote:" {
		t.Fatalf("remoteRoot() = %s", got)
	}
	if got := cs.remotePathFor("backups/full.tar.zst"); got != "remote:tenant/projects/backups/full.tar.zst" {
		t.Fatalf("remotePathFor() = %s", got)
	}

	cfg.CloudEnabled = false
	cfg.CloudRemote = ""
	if cs.IsEnabled() {
		t.Fatal("IsEnabled() should reflect config updates")
	}
}

func TestRemoteDirRef(t *testing.T) {
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"root only", "remote:", "remote:"},
		{"nested path", "remote:foo/bar/baz.tar", "remote:foo/bar"},
		{"single file", "remote:file.tar", "remote:"},
		{"no colon", "local", "local:"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := remoteDirRef(tt.ref); got != tt.want {
				t.Fatalf("remoteDirRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestCloudStorageApplyGFSRetentionDeletesMarkedBackups(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:          true,
		CloudRemote:           "remote",
		CloudBatchSize:        0,
		CloudBatchPause:       0,
		BundleAssociatedFiles: false,
	}
	cs := newCloudStorageForTest(cfg)
	cs.sleep = func(time.Duration) {}
	cs.setRemoteSnapshot(map[string]struct{}{
		"alpha-backup.tar.zst": {},
		"beta-backup.tar.zst":  {},
	})

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{name: "rclone", args: []string{"deletefile", "remote:alpha-backup.tar.zst"}},
			{name: "rclone", args: []string{"deletefile", "remote:beta-backup.tar.zst"}},
		},
	}
	cs.execCommand = queue.exec

	now := time.Now()
	backups := []*types.BackupMetadata{
		{BackupFile: "alpha-backup.tar.zst", Timestamp: now.Add(-48 * time.Hour)},
		{BackupFile: "beta-backup.tar.zst", Timestamp: now.Add(-72 * time.Hour)},
	}
	retentionCfg := RetentionConfig{Policy: "gfs", Daily: 0, Weekly: 0, Monthly: 0, Yearly: -1}

	deleted, err := cs.applyGFSRetention(context.Background(), backups, retentionCfg)
	if err != nil {
		t.Fatalf("applyGFSRetention() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("applyGFSRetention() deleted = %d, want 2", deleted)
	}
	if len(queue.calls) != 2 {
		t.Fatalf("expected 2 delete commands, got %d", len(queue.calls))
	}

	summary := cs.LastRetentionSummary()
	if summary.BackupsDeleted != 2 || summary.BackupsRemaining != 0 {
		t.Fatalf("unexpected retention summary: %+v", summary)
	}
}

func TestCloudStorageGetStatsSummarizesList(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "remote",
	}
	cs := newCloudStorageForTest(cfg)
	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			{
				name: "rclone",
				args: []string{"lsl", "remote:"},
				out: strings.TrimSpace(`
10 2025-06-01 10:00:00 host-backup-20250601.tar.zst
5 2025-05-30 08:00:00 host-backup-20250530.tar.zst
`),
			},
		},
	}
	cs.execCommand = queue.exec

	stats, err := cs.GetStats(context.Background())
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if stats.TotalBackups != 2 {
		t.Fatalf("TotalBackups = %d, want 2", stats.TotalBackups)
	}
	if stats.TotalSize != 15 {
		t.Fatalf("TotalSize = %d, want 15", stats.TotalSize)
	}
	if stats.FilesystemType != FilesystemType("rclone-remote") {
		t.Fatalf("FilesystemType = %s", stats.FilesystemType)
	}
	layout := "2006-01-02 15:04:05"
	newest, _ := time.Parse(layout, "2025-06-01 10:00:00")
	oldest, _ := time.Parse(layout, "2025-05-30 08:00:00")
	if stats.NewestBackup == nil || !stats.NewestBackup.Equal(newest) {
		t.Fatalf("NewestBackup = %v, want %v", stats.NewestBackup, newest)
	}
	if stats.OldestBackup == nil || !stats.OldestBackup.Equal(oldest) {
		t.Fatalf("OldestBackup = %v, want %v", stats.OldestBackup, oldest)
	}
	if len(queue.calls) != 1 {
		t.Fatalf("expected a single lsl call, got %d", len(queue.calls))
	}
}

// Test automatic fallback to write test when list check fails with 403
func TestCloudStorageCheckWithListPermissionDeniedFallbackToWrite(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "r2",
		CloudWriteHealthCheck:   false, // Auto mode
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// List check fails with 403 Forbidden
			{name: "rclone", args: []string{"lsf", "r2:", "--max-depth", "1"},
				err: errors.New("exit status 3"), out: "403 Forbidden"},
			// Write test succeeds
			{name: "rclone"}, // touch
			{name: "rclone"}, // deletefile
		},
	}
	cs.execCommand = queue.exec

	err := cs.checkRemoteAccessible(context.Background())
	if err != nil {
		t.Fatalf("checkRemoteAccessible() should succeed via fallback, got: %v", err)
	}

	if len(queue.calls) != 3 {
		t.Fatalf("expected 3 rclone calls (lsf + touch + deletefile), got %d", len(queue.calls))
	}
}

// Test NO fallback when timeout error occurs
func TestCloudStorageCheckWithTimeoutNoFallback(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		CloudWriteHealthCheck:   false,
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// List check times out (not a permission error)
			{name: "rclone", err: errors.New("context deadline exceeded"), out: "timeout"},
			// Retry attempt 2 list check
			{name: "rclone", err: errors.New("context deadline exceeded"), out: "timeout"},
			// Retry attempt 3 list check
			{name: "rclone", err: errors.New("context deadline exceeded"), out: "timeout"},
		},
	}
	cs.execCommand = queue.exec
	cs.sleep = func(time.Duration) {} // Disable sleep for fast tests

	err := cs.checkRemoteAccessible(context.Background())
	if err == nil {
		t.Fatal("checkRemoteAccessible() should fail with timeout, got nil")
	}

	// Should try 3 times (with retries), no fallback to write test
	if len(queue.calls) != 3 {
		t.Fatalf("expected 3 rclone calls (lsf per attempt), got %d", len(queue.calls))
	}
}

// Test NO fallback when network error occurs
func TestCloudStorageCheckWithNetworkErrorNoFallback(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		CloudWriteHealthCheck:   false,
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// List check fails with network error - attempt 1
			{name: "rclone", err: errors.New("exit 1"), out: "dial tcp: connection refused"},
			// Retry attempt 2
			{name: "rclone", err: errors.New("exit 1"), out: "dial tcp: connection refused"},
			// Retry attempt 3
			{name: "rclone", err: errors.New("exit 1"), out: "dial tcp: connection refused"},
		},
	}
	cs.execCommand = queue.exec
	cs.sleep = func(time.Duration) {} // Disable sleep for fast tests

	err := cs.checkRemoteAccessible(context.Background())
	if err == nil {
		t.Fatal("checkRemoteAccessible() should fail with network error, got nil")
	}

	// Should try 3 times (with retries), no fallback to write test
	if len(queue.calls) != 3 {
		t.Fatalf("expected 3 rclone calls (lsf per attempt), got %d", len(queue.calls))
	}
}

// Test backward compatibility: CLOUD_WRITE_HEALTHCHECK=true skips list check
func TestCloudStorageCheckWriteHealthCheckTrueSkipsList(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		CloudWriteHealthCheck:   true, // Force write test mode
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// Should skip list check entirely, go straight to write test
			{name: "rclone"}, // touch
			{name: "rclone"}, // deletefile
		},
	}
	cs.execCommand = queue.exec

	err := cs.checkRemoteAccessible(context.Background())
	if err != nil {
		t.Fatalf("checkRemoteAccessible() error = %v", err)
	}

	// Should only have 2 calls (touch + deletefile), no lsf
	if len(queue.calls) != 2 {
		t.Fatalf("expected 2 rclone calls (touch + deletefile, no lsf), got %d", len(queue.calls))
	}

	// Verify no call is lsf
	for _, call := range queue.calls {
		if strings.Contains(strings.Join(call.args, " "), "lsf") {
			t.Fatal("expected to skip lsf when CLOUD_WRITE_HEALTHCHECK=true")
		}
	}
}

// Test both list and write fail
func TestCloudStorageCheckBothListAndWriteFail(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		CloudWriteHealthCheck:   false,
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// List check fails with 401 - triggers fallback
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
			// Fallback write test also fails - triggers retry
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
			// Attempt 2: List check fails again
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
			// Fallback write test fails again
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
			// Attempt 3: List check fails again
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
			// Fallback write test fails again
			{name: "rclone", err: errors.New("exit 3"), out: "401 Unauthorized"},
		},
	}
	cs.execCommand = queue.exec
	cs.sleep = func(time.Duration) {} // Disable sleep for fast tests

	err := cs.checkRemoteAccessible(context.Background())
	if err == nil {
		t.Fatal("checkRemoteAccessible() should fail when both methods fail, got nil")
	}

	// Should try 3 times with fallback each time: 3x(lsf + touch) = 6 calls
	if len(queue.calls) != 6 {
		t.Fatalf("expected 6 rclone calls (lsf + touch per attempt), got %d", len(queue.calls))
	}
}

// Test write succeeds but cleanup fails (should still succeed overall)
func TestCloudStorageCheckWriteSucceedsButCleanupFails(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "remote",
		CloudWriteHealthCheck:   false,
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// List check fails
			{name: "rclone", args: []string{"lsf", "remote:", "--max-depth", "1"},
				err: errors.New("exit 3"), out: "403 Forbidden"},
			// Write test succeeds
			{name: "rclone"}, // touch succeeds
			// Cleanup fails (no delete permission)
			{name: "rclone", err: errors.New("exit 1"), out: "permission denied"},
		},
	}
	cs.execCommand = queue.exec

	err := cs.checkRemoteAccessible(context.Background())
	if err != nil {
		t.Fatalf("checkRemoteAccessible() should succeed even if cleanup fails, got: %v", err)
	}

	if len(queue.calls) != 3 {
		t.Fatalf("expected 3 rclone calls, got %d", len(queue.calls))
	}
}

// Test fallback with CLOUD_REMOTE_PATH configured
func TestCloudStorageCheckFallbackWithRemotePath(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled:            true,
		CloudRemote:             "s3",
		CloudRemotePath:         "backups",
		CloudWriteHealthCheck:   false,
		RcloneTimeoutConnection: 30,
	}
	cs := newCloudStorageForTest(cfg)

	queue := &commandQueue{
		t: t,
		queue: []queuedResponse{
			// Root check succeeds
			{name: "rclone", args: []string{"lsf", "s3:", "--max-depth", "1"}},
			// Path mkdir succeeds
			{name: "rclone", args: []string{"mkdir", "s3:backups"}},
			// Path list check fails with 403
			{name: "rclone", args: []string{"lsf", "s3:backups", "--max-depth", "1"},
				err: errors.New("exit 3"), out: "403 Forbidden"},
			// Write test succeeds
			{name: "rclone"}, // touch
			{name: "rclone"}, // deletefile
		},
	}
	cs.execCommand = queue.exec

	err := cs.checkRemoteAccessible(context.Background())
	if err != nil {
		t.Fatalf("checkRemoteAccessible() should succeed via fallback, got: %v", err)
	}

	// Should have: lsf (root) + mkdir + lsf (path, fails) + touch + deletefile
	if len(queue.calls) != 5 {
		t.Fatalf("expected 5 rclone calls, got %d", len(queue.calls))
	}
}
