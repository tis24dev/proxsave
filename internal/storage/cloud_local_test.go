package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// cloudForm is one way of writing CLOUD_REMOTE, with the rclone references the backend
// must build from it.
type cloudForm struct {
	name       string
	remote     string
	remotePath string
	root       string // what the accessibility check lists first
	base       string // the backup directory
	logDir     string // where CLOUD_LOG_PATH=/proxsave/log resolves
}

func (f cloudForm) local() bool { return !strings.Contains(f.remote, ":") }

// cloudFormsFor lists the ways of writing CLOUD_REMOTE. The local form (an absolute
// CLOUD_REMOTE without a colon) builds plain paths, which rclone's local backend takes
// as is, under a real temporary directory, because ProxSave creates it; the remote
// form is unchanged.
func cloudFormsFor(t *testing.T) []cloudForm {
	root := filepath.Join(t.TempDir(), "cloud")
	return []cloudForm{
		{name: "local", remote: root, root: root, base: root, logDir: root + "/proxsave/log"},
		{name: "local with CLOUD_REMOTE_PATH", remote: root, remotePath: "host1", root: root, base: root + "/host1", logDir: root + "/proxsave/log"},
		{name: "remote", remote: "remote:backups", root: "remote:", base: "remote:backups", logDir: "remote:/proxsave/log"},
		{name: "remote with CLOUD_REMOTE_PATH", remote: "remote:backups", remotePath: "host1", root: "remote:", base: "remote:backups/host1", logDir: "remote:/proxsave/log"},
	}
}

// argvRecorder records every rclone argv and answers each one with respond.
type argvRecorder struct {
	mu      sync.Mutex
	calls   [][]string
	respond func(args []string) ([]byte, error)
}

func (r *argvRecorder) exec(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	r.mu.Unlock()
	if r.respond == nil {
		return nil, nil
	}
	return r.respond(args)
}

func (r *argvRecorder) argv() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.calls))
	for _, call := range r.calls {
		out = append(out, strings.Join(call, " "))
	}
	return out
}

func requireArgv(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rclone argv =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func newFormCloud(t *testing.T, form cloudForm, mutate func(*config.Config), rec *argvRecorder) *CloudStorage {
	t.Helper()
	cfg := &config.Config{
		CloudEnabled:    true,
		CloudRemote:     form.remote,
		CloudRemotePath: form.remotePath,
		CloudLogPath:    "/proxsave/log",
		CloudBatchSize:  10,
	}
	if mutate != nil {
		mutate(cfg)
	}
	cs, err := NewCloudStorage(cfg, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage(%q, %q): %v", form.remote, form.remotePath, err)
	}
	if !cs.IsEnabled() {
		t.Fatalf("the backend for %q must be enabled", form.remote)
	}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	cs.waitForRetry = func(context.Context, time.Duration) error { return nil }
	cs.sleep = func(time.Duration) {}
	cs.execCommand = rec.exec
	return cs
}

func TestCloudLocalFormAccessibilityCheck(t *testing.T) {
	for _, form := range cloudFormsFor(t) {
		t.Run(form.name, func(t *testing.T) {
			rec := &argvRecorder{}
			cs := newFormCloud(t, form, nil, rec)
			info, err := cs.DetectFilesystem(context.Background())
			if err != nil {
				t.Fatalf("DetectFilesystem: %v", err)
			}
			// The local form creates the backup directory first, like the Secondary
			// (MkdirAll 0700, parents included, no rclone), then lists; the remote form
			// lists the root, then creates and lists the path under it.
			var want []string
			if form.local() {
				want = append(want, "lsf "+form.root+" --max-depth 1")
				if form.base != form.root {
					want = append(want, "lsf "+form.base+" --max-depth 1")
				}
				for _, dir := range []string{form.root, form.base} {
					if st, err := os.Stat(dir); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
						t.Fatalf("%s must be created 0700: %v %v", dir, st, err)
					}
				}
			} else {
				want = append(want, "lsf "+form.root+" --max-depth 1")
				if form.base != form.root {
					want = append(want, "mkdir "+form.base, "lsf "+form.base+" --max-depth 1")
				}
			}
			requireArgv(t, rec.argv(), want...)
			// The local form carries the type the filesystem detection finds (unknown
			// where the directory does not exist), never an rclone label; the remote
			// form keeps the rclone label (TestCloudLocalFormDetectsTheRealFilesystem).
			if form.local() {
				if info.Path != form.base || strings.HasPrefix(string(info.Type), "rclone-") {
					t.Fatalf("filesystem info = %+v, want path %s and a detected type", info, form.base)
				}
			} else if info.Path != form.base || info.MountPoint != form.base || info.Type != "rclone-remote" {
				t.Fatalf("filesystem info = %+v, want path and mount %s, type rclone-remote", info, form.base)
			}
		})
	}
}

func TestCloudLocalFormWriteTest(t *testing.T) {
	for _, form := range cloudFormsFor(t) {
		t.Run(form.name, func(t *testing.T) {
			rec := &argvRecorder{}
			cs := newFormCloud(t, form, func(c *config.Config) { c.CloudWriteHealthCheck = true }, rec)
			if _, err := cs.DetectFilesystem(context.Background()); err != nil {
				t.Fatalf("DetectFilesystem: %v", err)
			}
			got := rec.argv()
			if form.local() {
				if st, err := os.Stat(form.base); err != nil || !st.IsDir() {
					t.Fatalf("the local directory %s must exist before the write test: %v", form.base, err)
				}
			}
			if len(got) != 2 {
				t.Fatalf("rclone argv = %v, want the touch and the deletefile", got)
			}
			prefix := form.base + "/.pbs-backup-healthcheck-"
			if !strings.HasPrefix(got[0], "touch "+prefix) || !strings.HasPrefix(got[1], "deletefile "+prefix) ||
				strings.TrimPrefix(got[0], "touch ") != strings.TrimPrefix(got[1], "deletefile ") {
				t.Fatalf("rclone argv = %v, want touch and deletefile of one file under %s", got, form.base)
			}
		})
	}
}

// The upload, its size verification (primary and alternative method) and the checksum.
func TestCloudLocalFormUploadAndVerification(t *testing.T) {
	dir := t.TempDir()
	name := "node-backup-20261003-100000.tar.zst"
	archive := filepath.Join(dir, name)
	writeTestFile(t, archive, "archive")
	sizeLine := []byte("        7 2026-10-03 10:00:00.000000000 " + name + "\n")

	for _, form := range cloudFormsFor(t) {
		t.Run(form.name, func(t *testing.T) {
			remoteFile := form.base + "/" + name
			rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
				switch {
				case args[0] == "lsl" && args[1] == remoteFile:
					return sizeLine, nil
				case args[0] == "ls":
					return []byte("        7 " + name + "\n"), nil
				}
				return nil, nil
			}}
			cs := newFormCloud(t, form, nil, rec)
			if err := cs.Store(context.Background(), archive, nil); err != nil {
				t.Fatalf("Store: %v", err)
			}
			requireArgv(t, rec.argv(),
				"copyto "+archive+" "+remoteFile,
				"lsl "+remoteFile,
				"lsl "+form.base+" --max-depth 1",
			)

			rec = &argvRecorder{respond: rec.respond}
			cs = newFormCloud(t, form, func(c *config.Config) {
				c.RcloneVerifyMethod = "alternative"
				c.CloudVerifyChecksum = true
			}, rec)
			if ok, err := cs.VerifyUpload(context.Background(), archive, remoteFile); err != nil || !ok {
				t.Fatalf("VerifyUpload = %v, %v", ok, err)
			}
			requireArgv(t, rec.argv(),
				"ls "+form.base,
				"hashsum sha256 "+remoteFile,
			)
		})
	}
}

// Retention: the listing, the manifest reads, the deletions, and the cloud log
// listing and cleanup; then the statistics.
func TestCloudLocalFormRetentionAndStatistics(t *testing.T) {
	newest := "node-backup-20241112-100000.tar.zst"
	oldest := "node-backup-20241110-100000.tar.zst"
	listing := strings.Join([]string{
		"100 2024-11-12 10:00:00 " + newest,
		"120 2024-11-12 10:00:00 " + newest + ".sha256",
		"100 2024-11-10 10:00:00 " + oldest,
		"120 2024-11-10 10:00:00 " + oldest + ".sha256",
	}, "\n")

	for _, form := range cloudFormsFor(t) {
		t.Run(form.name, func(t *testing.T) {
			rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
				switch args[0] {
				case "lsl":
					return []byte(listing), nil
				case "cat":
					return []byte(`{"hostname":"node"}`), nil
				case "lsf":
					return []byte("backup-node-20241110-100000.log\n"), nil
				}
				return nil, nil
			}}
			cs := newFormCloud(t, form, nil, rec)
			cs.hostname = "node"
			deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
			if err != nil || deleted != 1 {
				t.Fatalf("ApplyRetention = %d, %v; want 1 deleted", deleted, err)
			}
			// The manifest reads run concurrently: compared in sorted order.
			got := rec.argv()
			sort.Strings(got)
			want := []string{
				"cat " + form.base + "/" + newest + ".metadata",
				"cat " + form.base + "/" + oldest + ".metadata",
				"deletefile " + form.base + "/" + oldest,
				"deletefile " + form.base + "/" + oldest + ".sha256",
				"deletefile " + form.logDir + "/backup-node-20241110-100000.log",
				"lsf " + form.logDir + " --files-only",
				"lsl " + form.base + " --max-depth 1",
			}
			sort.Strings(want)
			requireArgv(t, got, want...)

			rec = &argvRecorder{respond: rec.respond}
			cs = newFormCloud(t, form, nil, rec)
			cs.hostname = "node"
			stats, err := cs.GetStats(context.Background())
			if err != nil || stats.TotalBackups != 2 {
				t.Fatalf("GetStats = %+v, %v; want 2 backups", stats, err)
			}
			// The statistics read the manifests as retention does, to count only this
			// host's backups.
			got = rec.argv()
			sort.Strings(got)
			want = []string{
				"cat " + form.base + "/" + newest + ".metadata",
				"cat " + form.base + "/" + oldest + ".metadata",
				"lsl " + form.base + " --max-depth 1",
			}
			sort.Strings(want)
			requireArgv(t, got, want...)
		})
	}
}

// The log upload of step [8] goes through UploadToRemotePath with the destination the
// orchestrator resolved; inside the backend, the cleanup resolves the same directory.
func TestCloudLocalFormLogUpload(t *testing.T) {
	src := filepath.Join(t.TempDir(), "backup-node-20261003-100000.log")
	writeTestFile(t, src, "logdata")
	for _, form := range cloudFormsFor(t) {
		t.Run(form.name, func(t *testing.T) {
			rec := &argvRecorder{}
			cs := newFormCloud(t, form, nil, rec)
			dest := cs.cloudLogPath(cs.config.CloudLogPath, filepath.Base(src))
			if want := form.logDir + "/" + filepath.Base(src); dest != want {
				t.Fatalf("cloudLogPath = %q, want %q", dest, want)
			}
			if got := cs.cloudLogBase(cs.config.CloudLogPath); got != form.logDir {
				t.Fatalf("cloudLogBase = %q, want %q", got, form.logDir)
			}
			rec.respond = func(args []string) ([]byte, error) {
				if args[0] == "lsl" {
					return []byte("        7 2026-10-03 10:00:00.000000000 " + filepath.Base(src) + "\n"), nil
				}
				return nil, nil
			}
			if err := cs.UploadToRemotePath(context.Background(), src, dest, true); err != nil {
				t.Fatalf("UploadToRemotePath: %v", err)
			}
			requireArgv(t, rec.argv(), "copyto "+src+" "+dest, "lsl "+dest)
		})
	}
}

// A CLOUD_LOG_PATH with a colon is a full rclone reference and stays as it is, in the
// local form too.
func TestCloudLocalFormLegacyLogPathStaysAsIs(t *testing.T) {
	rec := &argvRecorder{}
	cs := newFormCloud(t, cloudFormsFor(t)[0], func(c *config.Config) { c.CloudLogPath = "other:/logs" }, rec)
	if got := cs.cloudLogPath(cs.config.CloudLogPath, "x.log"); got != "other:/logs/x.log" {
		t.Fatalf("cloudLogPath = %q, want other:/logs/x.log", got)
	}
	if got := cs.cloudLogBase(cs.config.CloudLogPath); got != "other:/logs" {
		t.Fatalf("cloudLogBase = %q, want other:/logs", got)
	}
}

// The local form is normalized with filepath.Clean, never refused for its shape.
func TestCloudLocalFormIsNormalized(t *testing.T) {
	for _, tc := range []struct{ remote, remotePath, root, dir string }{
		{"/mnt/cloud/", "", "/mnt/cloud", "/mnt/cloud"},
		{"/mnt//cloud", "host1", "/mnt/cloud", "/mnt/cloud/host1"},
		{"/mnt/../cloud", "", "/cloud", "/cloud"},
		{"  /mnt/cloud/./x/..  ", "/host1/", "/mnt/cloud", "/mnt/cloud/host1"},
	} {
		cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: tc.remote, CloudRemotePath: tc.remotePath}, newTestLogger(), "")
		if err != nil {
			t.Fatalf("NewCloudStorage(%q, %q): %v", tc.remote, tc.remotePath, err)
		}
		if cs.localRoot != tc.root || cs.localDir != tc.dir || cs.remoteLabel() != tc.dir {
			t.Fatalf("NewCloudStorage(%q, %q) root=%q dir=%q label=%q, want root %q dir %q",
				tc.remote, tc.remotePath, cs.localRoot, cs.localDir, cs.remoteLabel(), tc.root, tc.dir)
		}
	}
}

func TestCloudLocalFormValidation(t *testing.T) {
	for _, tc := range []struct {
		remote, remotePath, want string
	}{
		{"/mnt/cloud", "../escape", "CLOUD_REMOTE_PATH must not traverse outside the configured remote"},
		// A colon makes it the remote form, whose name holds no separator: refused as
		// before.
		{"/mnt/cloud:sub", "", "invalid CLOUD_REMOTE: rclone remote name contains a path separator or colon"},
	} {
		_, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: tc.remote, CloudRemotePath: tc.remotePath}, newTestLogger(), "")
		if err == nil || err.Error() != tc.want {
			t.Fatalf("NewCloudStorage(%q, %q) error = %v, want %q", tc.remote, tc.remotePath, err, tc.want)
		}
	}
	// What cannot be a local directory is still refused (unreachable through
	// LocalCloudRemote, which only hands over absolute paths).
	if err := validateLocalCloudRemote("-x"); err == nil || err.Error() != "local directory must not start with '-'" {
		t.Fatalf("a directory starting with '-' must be refused, got %v", err)
	}
	if err := validateLocalCloudRemote("mnt/cloud"); err == nil || err.Error() != "local directory must be an absolute path" {
		t.Fatalf("a relative directory must be refused, got %v", err)
	}
	if err := validateLocalCloudRemote("/mnt/cloud"); err != nil {
		t.Fatalf("validateLocalCloudRemote(/mnt/cloud) = %v", err)
	}
}

func TestLocalCloudRemoteHelpers(t *testing.T) {
	for _, tc := range []struct {
		remote, remotePath, dir string
		ok                      bool
	}{
		{"/mnt/cloud", "", "/mnt/cloud", true},
		{"  /mnt/cloud  ", "/host1/", "/mnt/cloud/host1", true},
		{"/mnt/cloud/backups", "server1", "/mnt/cloud/backups/server1", true},
		{"/mnt/cloud:sub", "", "", false},
		{"gdrive", "x", "", false},
		{"gdrive:pbs", "", "", false},
		{"", "", "", false},
	} {
		dir, ok := LocalCloudRemoteDir(tc.remote, tc.remotePath)
		if dir != tc.dir || ok != tc.ok {
			t.Fatalf("LocalCloudRemoteDir(%q, %q) = %q, %v; want %q, %v", tc.remote, tc.remotePath, dir, ok, tc.dir, tc.ok)
		}
	}
	for _, tc := range []struct {
		logPath, remote, dir string
		ok                   bool
	}{
		{"/proxsave/log", "/mnt/cloud", "/mnt/cloud/proxsave/log", true},
		{"proxsave/log/", "/mnt/cloud", "/mnt/cloud/proxsave/log", true},
		{"other:/logs", "/mnt/cloud", "", false},
		{"/proxsave/log", "gdrive", "", false},
		{"", "/mnt/cloud", "", false},
	} {
		dir, ok := LocalCloudLogDir(tc.logPath, tc.remote)
		if dir != tc.dir || ok != tc.ok {
			t.Fatalf("LocalCloudLogDir(%q, %q) = %q, %v; want %q, %v", tc.logPath, tc.remote, dir, ok, tc.dir, tc.ok)
		}
	}
	if got := remoteDirRef("/mnt/cloud/host1/a.tar"); got != "/mnt/cloud/host1" {
		t.Fatalf("remoteDirRef(local) = %q", got)
	}
	if got := remoteBaseName("/mnt/cloud/host1/a.tar"); got != "a.tar" {
		t.Fatalf("remoteBaseName(local) = %q", got)
	}
}

// CLOUD_REMOTE as a local directory: the storage summary gets the directory's real
// filesystem, detected like the Primary's and the Secondary's (FilesystemDetector),
// and the detection writes nothing visible (the network ownership probe lines are
// DEBUG). A directory the detection cannot read leaves the type unknown.
func TestCloudLocalFormDetectsTheRealFilesystem(t *testing.T) {
	dir := t.TempDir()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: dir}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	cs.execCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cs.fsDetector.mountPointLookup = func(string) (string, error) { return dir, nil }
	cs.fsDetector.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
		return FilesystemNFS4, "nas:/export", nil
	}
	probed := false
	cs.fsDetector.ownershipSupportTest = func(context.Context, string) bool { probed = true; return true }

	info, err := cs.DetectFilesystem(context.Background())
	if err != nil {
		t.Fatalf("DetectFilesystem: %v", err)
	}
	if info.Type != FilesystemNFS4 || info.MountPoint != dir || !info.IsNetworkFS || !probed {
		t.Fatalf("filesystem info = %+v (probed=%v), want the detected nfs4 mount with the ownership probe run", info, probed)
	}
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO     Checking cloud remote accessibility...\nINFO       Accessible"; got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(stripTimes(buf.String()), "DEBUG    Network filesystem nfs4 supports Unix ownership\n") {
		t.Fatalf("the ownership probe result belongs at DEBUG:\n%s", buf.String())
	}

	if cs.DetectionFailure() != nil || cs.localFS != info {
		t.Fatalf("a detection that worked records its result and no failure: %v", cs.DetectionFailure())
	}

	// A filesystem the detection cannot read: the type is unknown, and the failure is
	// reported for the storage initialization (the Secondary's shape).
	cs, err = NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: filepath.Join(dir, "sub")}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	cs.execCommand = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	cs.fsDetector.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
		return FilesystemUnknown, "", errors.New("statfs failed")
	}
	if info, err := cs.DetectFilesystem(context.Background()); err != nil || info.Type != FilesystemUnknown || info.SupportsOwnership {
		t.Fatalf("DetectFilesystem of an unreadable filesystem = %+v, %v; want an unknown type without ownership", info, err)
	}
	if err := cs.DetectionFailure(); err == nil || !strings.Contains(err.Error(), "statfs failed") {
		t.Fatalf("DetectionFailure = %v, want the detection error", err)
	}
}

// A CLOUD_LOG_PATH that resolves outside the local CLOUD_REMOTE directory: the step [6]
// cloud log cleanup does not run for it, prints nothing visible and counts nothing.
func TestCloudLocalFormLogPathOutsideSkipsTheCleanup(t *testing.T) {
	newest := "node-backup-20241112-100000.tar.zst"
	oldest := "node-backup-20241110-100000.tar.zst"
	listing := strings.Join([]string{
		"100 2024-11-12 10:00:00 " + newest,
		"120 2024-11-12 10:00:00 " + newest + ".sha256",
		"100 2024-11-10 10:00:00 " + oldest,
		"120 2024-11-10 10:00:00 " + oldest + ".sha256",
	}, "\n")
	rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
		switch args[0] {
		case "lsl":
			return []byte(listing), nil
		case "cat":
			return []byte(`{"hostname":"node"}`), nil
		}
		return nil, nil
	}}
	logger, buf := newCapturedLogger()
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "/mnt/cloud/", CloudLogPath: "../logs", CloudBatchSize: 10}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.execCommand = rec.exec
	cs.sleep = func(time.Duration) {}
	cs.hostname = "node"
	if deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil || deleted != 1 {
		t.Fatalf("ApplyRetention = %d, %v; want 1 deleted", deleted, err)
	}
	for _, line := range rec.argv() {
		if strings.Contains(line, "logs") {
			t.Fatalf("the cleanup ran outside CLOUD_REMOTE: %q", line)
		}
	}
	// The scale of the pass is the only fact: the skipped cleanup adds none.
	if got, want := strings.Join(visibleOf(buf.String()), "\n"), "INFO       Backups: 2, limit: 1"; got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if s := cs.LastRetentionSummary(); s.LogsNotDeleted != 0 || s.LogsDeleted != 0 {
		t.Fatalf("summary = %+v, want no log counted", s)
	}
}

func TestLocalCloudLogOutside(t *testing.T) {
	for _, tc := range []struct {
		logPath, remote, root string
		outside               bool
	}{
		{"/proxsave/log", "/mnt/cloud", "/mnt/cloud", false},
		{"/", "/mnt/cloud", "/mnt/cloud", false}, // the CLOUD_REMOTE directory itself
		{"../logs", "/mnt/cloud/", "/mnt/cloud", true},
		{"/../../etc", "/mnt/cloud", "/mnt/cloud", true},
		{"/x/../../cloud2", "/mnt/cloud", "/mnt/cloud", true},
		{"../logs", "/", "/", false},
		{"other:/logs", "/mnt/cloud", "", false}, // a full rclone reference: not checked
		{"../logs", "gdrive", "", false},         // remote form: not checked
	} {
		root, outside := LocalCloudLogOutside(tc.logPath, tc.remote)
		if root != tc.root || outside != tc.outside {
			t.Fatalf("LocalCloudLogOutside(%q, %q) = %q, %v; want %q, %v", tc.logPath, tc.remote, root, outside, tc.root, tc.outside)
		}
	}
}

// rcloneLocalCopy answers like rclone's local backend: copyto writes the destination
// 0644 (whatever the source mode), lsl reports the size of the local file.
func rcloneLocalCopy(failSuffix string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch args[0] {
		case "copyto":
			src, dst := args[len(args)-2], args[len(args)-1]
			if failSuffix != "" && strings.HasSuffix(src, failSuffix) {
				return []byte("2026/10/03 10:00:00 Failed to copyto: permission denied\n"), errors.New("exit status 1")
			}
			data, err := os.ReadFile(src)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				return nil, err
			}
			return nil, os.Chmod(dst, 0o644)
		case "lsl":
			if st, err := os.Stat(args[1]); err == nil && !st.IsDir() {
				return []byte(fmt.Sprintf("%9d 2026-10-03 10:00:00.000000000 %s\n", st.Size(), filepath.Base(args[1]))), nil
			}
		}
		return nil, nil
	}
}

func newLocalFormStore(t *testing.T, failSuffix string, supportsOwnership bool) (*CloudStorage, *bytes.Buffer, string, string) {
	t.Helper()
	srcDir := t.TempDir()
	archive := filepath.Join(srcDir, "node-backup-20261003-100000.tar.zst")
	for _, f := range []string{archive, archive + ".sha256", archive + ".metadata"} {
		if err := os.WriteFile(f, []byte("archive"), 0o600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	root := filepath.Join(t.TempDir(), "cloud")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	logger, buf := newCapturedLogger()
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: root, RcloneRetries: 1}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.execCommand = rcloneLocalCopy(failSuffix)
	cs.localFS = &FilesystemInfo{Path: root, Type: FilesystemExt4, SupportsOwnership: supportsOwnership}
	return cs, buf, archive, root
}

// The local form sets owner and mode on what rclone copied, exactly like the
// Secondary (0600 root:root by default): rclone writes 0644.
func TestCloudLocalFormSetsSecondaryPermissions(t *testing.T) {
	cs, buf, archive, root := newLocalFormStore(t, "", true)
	if err := cs.Store(context.Background(), archive, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	for _, name := range []string{filepath.Base(archive), filepath.Base(archive) + ".sha256", filepath.Base(archive) + ".metadata"} {
		st, err := os.Stat(filepath.Join(root, name))
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v (%v), want 0600 like the Secondary", name, st.Mode().Perm(), err)
		}
	}
	out := stripTimes(buf.String())
	if os.Geteuid() == 0 {
		if got := visibleOf(buf.String()); len(got) != 0 {
			t.Fatalf("a clean store prints no fact, got:\n%s", strings.Join(got, "\n"))
		}
		return
	}
	// Unprivileged: chown to root fails, like on the Secondary, one fact per file.
	if !strings.Contains(out, "INFO       Owner failed: "+filepath.Base(archive)+": operation not permitted\n") {
		t.Fatalf("missing the Owner failed fact:\n%s", out)
	}
	if issues := cs.LastStoreIssues(); len(issues) != 1 || issues[0] != StoreIssuePermissionsNotSet {
		t.Fatalf("LastStoreIssues = %v, want permissions not set", issues)
	}
}

// A sidecar that did not go up: its fact first, then the permissions of what was
// copied, and the issues in that order for the one outcome.
func TestCloudLocalFormSidecarFailureThenPermissions(t *testing.T) {
	cs, buf, archive, root := newLocalFormStore(t, ".sha256", true)
	err := cs.Store(context.Background(), archive, nil)
	var se *StorageError
	if !errors.As(err, &se) || !se.PrimarySaved {
		t.Fatalf("Store = %v, want the primary saved and a sidecar failure", err)
	}
	if st, err := os.Stat(filepath.Join(root, filepath.Base(archive))); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the archive must get 0600 even with a sidecar failure: %v %v", st, err)
	}
	got := visibleOf(buf.String())
	if len(got) == 0 || got[0] != "INFO       Sidecar failed: "+filepath.Base(archive)+".sha256: Failed to copyto: permission denied" {
		t.Fatalf("the sidecar fact must come first:\n%s", strings.Join(got, "\n"))
	}
	issues := cs.LastStoreIssues()
	if len(issues) == 0 || issues[0] != StoreIssueSidecarNotSaved {
		t.Fatalf("LastStoreIssues = %v, want the sidecar first", issues)
	}
	if os.Geteuid() != 0 && (len(issues) != 2 || issues[1] != StoreIssuePermissionsNotSet || len(got) < 2 || !strings.HasPrefix(got[1], "INFO       Owner failed: ")) {
		t.Fatalf("unprivileged: want the Owner failed facts after the sidecar and both issues, got %v:\n%s", issues, strings.Join(got, "\n"))
	}
}

// On a filesystem without ownership the Secondary sets nothing, and neither does the
// local form; the remote form never does.
func TestCloudLocalFormNoOwnershipSetsNothing(t *testing.T) {
	cs, buf, archive, root := newLocalFormStore(t, "", false)
	if err := cs.Store(context.Background(), archive, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if st, err := os.Stat(filepath.Join(root, filepath.Base(archive))); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v (%v), want rclone's 0644 left alone", st.Mode().Perm(), err)
	}
	if got := visibleOf(buf.String()); len(got) != 0 || len(cs.LastStoreIssues()) != 0 {
		t.Fatalf("no ownership: nothing to print or record, got %v %v", got, cs.LastStoreIssues())
	}

	rec := &argvRecorder{respond: func(args []string) ([]byte, error) {
		if args[0] == "lsl" {
			return []byte("        7 2026-10-03 10:00:00.000000000 " + filepath.Base(archive) + "\n"), nil
		}
		return nil, nil
	}}
	remote := newFormCloud(t, cloudFormsFor(t)[2], nil, rec)
	remote.localFS = &FilesystemInfo{SupportsOwnership: true}
	if err := remote.Store(context.Background(), archive, nil); err != nil {
		t.Fatalf("remote Store: %v", err)
	}
	if issues := remote.LastStoreIssues(); len(issues) != 0 {
		t.Fatalf("the remote form sets no permissions: %v", issues)
	}
}

// SET_BACKUP_PERMISSIONS with BACKUP_USER/BACKUP_GROUP: 0640 and that owner, the
// Secondary's rule (here the test's own user, so the chown succeeds unprivileged).
func TestCloudLocalFormSharedPermissions(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skipf("current user: %v", err)
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skipf("current group: %v", err)
	}
	cs, buf, archive, root := newLocalFormStore(t, "", true)
	cs.config.SetBackupPermissions, cs.config.BackupUser, cs.config.BackupGroup = true, u.Username, g.Name
	if err := cs.Store(context.Background(), archive, nil); err != nil {
		t.Fatalf("Store: %v", err)
	}
	st, err := os.Stat(filepath.Join(root, filepath.Base(archive)))
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v (%v), want 0640", st.Mode().Perm(), err)
	}
	if got := visibleOf(buf.String()); len(got) != 0 || len(cs.LastStoreIssues()) != 0 {
		t.Fatalf("a clean store prints and records nothing, got %v %v", got, cs.LastStoreIssues())
	}
}
