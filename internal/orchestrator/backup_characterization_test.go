package orchestrator

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
	"github.com/tis24dev/proxsave/internal/types"
)

// Characterization lock for the backup path (archive, bundle, copy to the storage
// paths, notification, log management), written before the backup-blocks refactor so
// that refactor is judged against tests that predate it. Each case drives the real
// Orchestrator.RunGoBackup over the real StorageAdapter, with storage.Storage fakes that
// fail on command, and freezes everything the run produces in one golden file per case.
//
// These goldens record what the code does TODAY, defects included. A golden that has to
// change is a behaviour change and must be justified as one.
// Regenerate deliberately with: go test ./internal/orchestrator/ -run TestBackupCharacterization -update
var updateBackupGoldens = flag.Bool("update", false, "rewrite backup characterization golden files")

const (
	backupCharHost        = "charhost"
	backupCharRemote      = "charremote"
	backupCharChannelName = "Characterization"
)

// backupCharStart is the run's clock reading. UTC, so the timestamp in every file name
// and in the stats is the same on every machine.
var backupCharStart = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func assertBackupGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "backup_characterization", name)
	if *updateBackupGoldens {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", name, err)
	}
	if string(want) != string(got) {
		t.Fatalf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

// backupCharClock is the orchestrator's TimeProvider. It stands still except when a
// storage backend stores an archive, which moves it forward one hour: a timestamp the
// run takes after the copy to the storage paths therefore shows up in the stats, while
// one taken before it (EndTime, Duration) keeps reading the start.
type backupCharClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *backupCharClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *backupCharClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// backupCharRecorder is the single ordered log of every call the fakes receive, storage
// backends and notification channel alike, so the golden fixes their relative order.
type backupCharRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *backupCharRecorder) add(format string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf(format, args...))
}

func (r *backupCharRecorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

// backupCharBackend is a storage.Storage (and storage.RetentionReporter, like the real
// backends) whose DetectFilesystem, Store and ApplyRetention fail on command. A
// non-empty destDir makes Store copy the archive it is handed there, so the golden can
// list what reached each path. Name, Location, IsEnabled and IsCritical are not recorded:
// they are lookups the adapter repeats for logging, not operations.
type backupCharBackend struct {
	label         string
	name          string
	location      storage.BackupLocation
	critical      bool
	destDir       string
	failDetect    bool
	failStore     bool
	failRetention bool
	fsInfo        storage.FilesystemInfo
	stats         storage.StorageStats
	deleted       int
	summary       storage.RetentionSummary
	rec           *backupCharRecorder
	clock         *backupCharClock
}

func (b *backupCharBackend) failure(operation, path string) error {
	return &storage.StorageError{
		Location:    b.location,
		Operation:   operation,
		Path:        path,
		Err:         errors.New("injected failure"),
		IsCritical:  b.critical,
		Recoverable: !b.critical,
	}
}

func (b *backupCharBackend) Name() string                     { return b.name }
func (b *backupCharBackend) Location() storage.BackupLocation { return b.location }
func (b *backupCharBackend) IsEnabled() bool                  { return true }
func (b *backupCharBackend) IsCritical() bool                 { return b.critical }

func (b *backupCharBackend) DetectFilesystem(ctx context.Context) (*storage.FilesystemInfo, error) {
	b.rec.add("%s DetectFilesystem", b.label)
	if b.failDetect {
		return nil, b.failure("detect_filesystem", b.fsInfo.Path)
	}
	info := b.fsInfo
	return &info, nil
}

func (b *backupCharBackend) Store(ctx context.Context, backupFile string, metadata *types.BackupMetadata) error {
	b.rec.add("%s Store file=%s", b.label, filepath.Base(backupFile))
	if metadata != nil {
		b.rec.add("%s Store metadata %s", b.label, describeBackupCharMetadata(metadata))
	}
	b.clock.advance(time.Hour)
	if b.failStore {
		return b.failure("store", b.fsInfo.Path)
	}
	if b.destDir == "" {
		return nil
	}
	return copyBackupCharFile(backupFile, filepath.Join(b.destDir, filepath.Base(backupFile)))
}

func (b *backupCharBackend) List(ctx context.Context) ([]*types.BackupMetadata, error) {
	b.rec.add("%s List", b.label)
	return nil, nil
}

func (b *backupCharBackend) Delete(ctx context.Context, backupFile string) error {
	b.rec.add("%s Delete file=%s", b.label, filepath.Base(backupFile))
	return nil
}

func (b *backupCharBackend) ApplyRetention(ctx context.Context, cfg storage.RetentionConfig) (int, error) {
	b.rec.add("%s ApplyRetention policy=%s max=%d", b.label, cfg.Policy, cfg.MaxBackups)
	if b.failRetention {
		return 0, b.failure("retention", b.fsInfo.Path)
	}
	return b.deleted, nil
}

func (b *backupCharBackend) LastRetentionSummary() storage.RetentionSummary {
	b.rec.add("%s LastRetentionSummary", b.label)
	return b.summary
}

func (b *backupCharBackend) VerifyUpload(ctx context.Context, localFile, remoteFile string) (bool, error) {
	b.rec.add("%s VerifyUpload", b.label)
	return true, nil
}

func (b *backupCharBackend) GetStats(ctx context.Context) (*storage.StorageStats, error) {
	b.rec.add("%s GetStats", b.label)
	stats := b.stats
	return &stats, nil
}

func describeBackupCharMetadata(m *types.BackupMetadata) string {
	checksum := "empty"
	if m.Checksum != "" {
		checksum = "set"
	}
	return fmt.Sprintf("file=%s timestamp=%s size=%d checksum=%s type=%s compression=%s version=%q",
		filepath.Base(m.BackupFile), m.Timestamp.UTC().Format(time.RFC3339), m.Size, checksum,
		m.ProxmoxType, m.Compression, m.Version)
}

func copyBackupCharFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o640)
}

// backupCharChannel is the notification channel. At the moment it is called it keeps a
// deep copy of the stats it is handed, which is the direct record of what a notification
// sees.
type backupCharChannel struct {
	rec      *backupCharRecorder
	calls    int
	snapshot []byte
}

func (c *backupCharChannel) Name() string { return backupCharChannelName }

func (c *backupCharChannel) Notify(ctx context.Context, stats *BackupStats) error {
	c.calls++
	c.rec.add("notify %s", c.Name())
	data, err := json.MarshalIndent(stats, "", "  ")
	if err != nil {
		c.snapshot = []byte(fmt.Sprintf("marshal error: %v", err))
		return nil
	}
	c.snapshot = data
	return nil
}

type backupCharCase struct {
	name        string
	dryRun      bool
	noBundle    bool
	encrypt     bool
	compression types.CompressionType
	noTargets   bool
	failLocal   bool
	failSecond  bool
	failCloud   bool
	// fakeBinaries are shell scripts placed first in PATH under their name. The name must
	// be one safeexec allows, since the archiver runs every external tool through it.
	fakeBinaries map[string]string
}

type backupCharDirs struct {
	root         string
	backup       string
	log          string
	secondary    string
	secondaryLog string
	cloud        string
	cloudLog     string
	sysroot      string
	workspace    string
	registry     string
	bin          string
}

// backupCharRootLen is the length every case's root directory is padded to. Absolute
// paths end up inside files whose size the goldens fix (the archive manifest, copied
// as the legacy .metadata into the bundle) and inside the log-category labels, which
// are truncated at a fixed width. t.TempDir() varies in length from run to run and from
// machine to machine, so the root is padded to one length everywhere.
const backupCharRootLen = 240

func newBackupCharDirs(t *testing.T) backupCharDirs {
	t.Helper()
	base := t.TempDir()
	if len(base) > backupCharRootLen-16 {
		t.Fatalf("temp dir %q is longer than the %d-byte characterization root allows", base, backupCharRootLen-16)
	}
	root := filepath.Join(base, strings.Repeat("p", backupCharRootLen-len(base)-1))
	d := backupCharDirs{
		root:         root,
		backup:       filepath.Join(root, "backup"),
		log:          filepath.Join(root, "log"),
		secondary:    filepath.Join(root, "secondary"),
		secondaryLog: filepath.Join(root, "secondary-log"),
		cloud:        filepath.Join(root, "cloud"),
		cloudLog:     filepath.Join(root, "cloud-log"),
		sysroot:      filepath.Join(root, "sysroot"),
		workspace:    filepath.Join(root, "workspace"),
		registry:     filepath.Join(root, "registry"),
		bin:          filepath.Join(root, "bin"),
	}
	for _, dir := range []string{d.backup, d.log, d.secondary, d.cloud, d.cloudLog, d.sysroot, d.registry, d.bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return d
}

type backupCharResult struct {
	stats    *BackupStats
	err      error
	stdout   string
	stderr   string
	logFile  string
	channel  *backupCharChannel
	recorder *backupCharRecorder
}

func runBackupCharacterization(t *testing.T, tc backupCharCase) string {
	t.Helper()

	// cleanupPreviousExecutionArtifacts sweeps profiles from the fixed /tmp/proxsave
	// directory, which no seam redirects. A leftover profile there would add lines to the
	// output (and be deleted), so refuse to run rather than produce a spurious diff.
	for _, pattern := range []string{"cpu-*.pprof", "heap-*.pprof"} {
		if matches, _ := filepath.Glob(filepath.Join("/tmp", "proxsave", pattern)); len(matches) > 0 {
			t.Fatalf("leftover profiles %v under /tmp/proxsave would change the run output; remove them first", matches)
		}
	}

	// The listings fix file modes, and every mode the run creates passes through the
	// process umask: pin it to the common 022 so they read the same on every machine.
	origUmask := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(origUmask) })

	dirs := newBackupCharDirs(t)
	origRoot := workspaceRoot
	workspaceRoot = dirs.workspace
	t.Cleanup(func() { workspaceRoot = origRoot })

	// The handoff file for the daemon is written only when this is set; the collection
	// runs `cat` on the empty system root and its error text lands in the output, so it
	// must not be translated.
	t.Setenv(health.EnvRunID, "")
	t.Setenv("LC_ALL", "C")
	if len(tc.fakeBinaries) > 0 {
		for name, script := range tc.fakeBinaries {
			if err := os.WriteFile(filepath.Join(dirs.bin, name), []byte(script), 0o755); err != nil {
				t.Fatalf("write fake %s: %v", name, err)
			}
		}
		t.Setenv("PATH", dirs.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}

	clock := &backupCharClock{now: backupCharStart}
	rec := &backupCharRecorder{}

	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(io.Discard)
	logPath := filepath.Join(dirs.log, fmt.Sprintf("backup-%s-%s.log", backupCharHost, backupCharStart.Format("20060102-150405")))
	if err := logger.OpenLogFile(logPath); err != nil {
		t.Fatalf("OpenLogFile: %v", err)
	}
	t.Cleanup(func() { _ = logger.CloseLogFile() })

	compression := tc.compression
	if compression == "" {
		compression = types.CompressionNone
	}

	cfg := &config.Config{
		BackupPath:             dirs.backup,
		LogPath:                dirs.log,
		BundleAssociatedFiles:  !tc.noBundle,
		EncryptArchive:         tc.encrypt,
		RetentionPolicy:        "simple",
		LocalRetentionDays:     7,
		SecondaryRetentionDays: 14,
		CloudRetentionDays:     30,
		SecondaryEnabled:       true,
		SecondaryPath:          dirs.secondary,
		SecondaryLogPath:       dirs.secondaryLog,
		CloudEnabled:           true,
		CloudRemote:            backupCharRemote,
		CloudLogPath:           dirs.cloudLog,
		FsIoTimeoutSeconds:     30,
		SystemRootPrefix:       dirs.sysroot,
	}
	if tc.encrypt {
		cfg.AgeRecipients = []string{testAgeRecipient}
	}

	registry, err := NewTempDirRegistry(logger, filepath.Join(dirs.registry, "temp-dirs.json"))
	if err != nil {
		t.Fatalf("NewTempDirRegistry: %v", err)
	}

	orch := New(logger, tc.dryRun)
	orch.clock = clock
	orch.SetBackupConfig(dirs.backup, dirs.log, compression, 0, 0, "standard", nil)
	orch.SetConfig(cfg)
	orch.SetTempDirRegistry(registry)
	// The cloud log upload goes to rclone in production; here it is a copy into the
	// cloud log directory, through the seam the orchestrator already has for it.
	orch.copyLogToCloudFn = func(ctx context.Context, sourcePath, destPath string) error {
		rec.add("cloud log upload dest=%s", destPath)
		target := strings.TrimPrefix(destPath, backupCharRemote+":")
		return copyBackupCharFile(sourcePath, target)
	}

	if !tc.noTargets {
		// Registered in the order cmd/proxsave/backup_storage.go registers them: local,
		// secondary, cloud. Filesystem info and initial stats are preloaded the way cmd
		// preloads them after its own detection.
		backends := []*backupCharBackend{
			{
				label: "local", name: "Local Storage", location: storage.LocationPrimary, critical: true,
				failStore: tc.failLocal,
				fsInfo:    storage.FilesystemInfo{Path: dirs.backup, Type: storage.FilesystemExt4, SupportsOwnership: true, MountPoint: "/"},
				stats: storage.StorageStats{TotalBackups: 8, TotalSize: 8 << 20,
					AvailableSpace: 100 << 30, UsedSpace: 50 << 30, TotalSpace: 150 << 30},
				deleted: 1,
				summary: storage.RetentionSummary{BackupsDeleted: 1, LogsDeleted: 1, ScopeValid: true, Owned: 7, PassCompleted: true},
			},
			{
				label: "secondary", name: "Secondary Storage", location: storage.LocationSecondary,
				destDir: dirs.secondary, failStore: tc.failSecond,
				fsInfo: storage.FilesystemInfo{Path: dirs.secondary, Type: storage.FilesystemNFS4, IsNetworkFS: true, MountPoint: dirs.secondary},
				stats: storage.StorageStats{TotalBackups: 5, TotalSize: 5 << 20,
					AvailableSpace: 200 << 30, UsedSpace: 300 << 30, TotalSpace: 500 << 30},
				summary: storage.RetentionSummary{ScopeValid: true, Owned: 3, PassCompleted: true},
			},
			{
				label: "cloud", name: "Cloud Storage (rclone)", location: storage.LocationCloud,
				destDir: dirs.cloud, failStore: tc.failCloud,
				fsInfo: storage.FilesystemInfo{Path: backupCharRemote + ":", Type: storage.FilesystemType("rclone-" + backupCharRemote),
					IsNetworkFS: true, MountPoint: backupCharRemote + ":", Device: "cloud"},
				stats:   storage.StorageStats{TotalBackups: 4, TotalSize: 4 << 20},
				deleted: 2,
				summary: storage.RetentionSummary{BackupsDeleted: 2, PassCompleted: true},
			},
		}
		for _, b := range backends {
			b.rec = rec
			b.clock = clock
			info := b.fsInfo
			initial := b.stats
			adapter := NewStorageAdapter(b, logger, cfg)
			adapter.SetFilesystemInfo(&info)
			adapter.SetInitialStats(&initial)
			orch.RegisterStorageTarget(adapter)
		}
	}

	channel := &backupCharChannel{rec: rec}
	orch.RegisterNotificationChannel(channel)

	res := backupCharResult{channel: channel, recorder: rec}
	res.stdout, res.stderr = captureBackupCharOutput(t, logger, func() {
		res.stats, res.err = orch.RunGoBackup(context.Background(),
			&environment.EnvironmentInfo{Type: types.ProxmoxUnknown, Version: "unknown"}, backupCharHost)
	})
	if data, err := os.ReadFile(logPath); err == nil {
		res.logFile = string(data)
	} else {
		res.logFile = fmt.Sprintf("(unreadable: %v)", err)
	}

	return renderBackupCharacterization(t, dirs, res)
}

// captureBackupCharOutput runs fn with os.Stdout, os.Stderr and the logger's console
// all redirected, so the blank lines RunGoBackup prints with fmt.Println stay in order
// with the logger's lines. No test in this package calls t.Parallel, so swapping the
// process-wide writers cannot catch another test's output.
func captureBackupCharOutput(t *testing.T, logger *logging.Logger, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var outBuf, errBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&outBuf, outR) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&errBuf, errR) }()

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	logger.SetOutput(outW)
	func() {
		defer func() {
			logger.SetOutput(io.Discard)
			os.Stdout, os.Stderr = origOut, origErr
			_ = outW.Close()
			_ = errW.Close()
		}()
		fn()
	}()
	wg.Wait()
	return outBuf.String(), errBuf.String()
}

var (
	backupCharTimestampRe = regexp.MustCompile(`(?m)^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\]`)
	backupCharDurationRe  = regexp.MustCompile(`duration=[0-9][0-9a-z\x{00B5}.]*`)
	backupCharSHA256Re    = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	backupCharWorkspaceRe = regexp.MustCompile(`(proxsave-` + backupCharHost + `-\d{8}-\d{6}-)\d+`)
	// The collection manifest stamps its own wall-clock time (RFC3339 with trailing zeros
	// trimmed), so its size moves by a few bytes from run to run.
	backupCharManifestSizeRe = regexp.MustCompile(`(manifest\.json \()\d+( bytes\))`)
	backupCharStatsSizeRe    = regexp.MustCompile(`(?m)^(\s*"(?:BytesCollected|UncompressedSize|CompressionRatio|CompressionRatioPercent)": )[^,\n]+`)
	// A log-category label is cut at a fixed width with "..." appended; when the cut falls
	// inside the root directory, what is left of it is a prefix the full-root replacement
	// cannot see.
	backupCharTruncatedPathRe = regexp.MustCompile(`/[^\s"]*\.\.\.`)
)

// maskBackupChar replaces only what changes from one run to the next: the root
// directory (whole, or cut short in a log-category label), log timestamps, durations,
// SHA-256 sums, the random suffix of the workspace and the size of the collection
// manifest.
func maskBackupChar(dirs backupCharDirs, s string) string {
	s = strings.ReplaceAll(s, dirs.root, "<ROOT>")
	s = backupCharTruncatedPathRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(dirs.root, strings.TrimSuffix(m, "...")) {
			return "<ROOT>..."
		}
		return m
	})
	s = backupCharTimestampRe.ReplaceAllString(s, "[<TIME>]")
	s = backupCharDurationRe.ReplaceAllString(s, "duration=<DURATION>")
	s = backupCharSHA256Re.ReplaceAllString(s, "<SHA256>")
	s = backupCharWorkspaceRe.ReplaceAllString(s, "${1}<RANDOM>")
	s = backupCharManifestSizeRe.ReplaceAllString(s, "${1}<SIZE>${2}")
	return s
}

// maskBackupCharStats masks, on top of maskBackupChar, the BackupStats fields derived
// from the collected byte count, which includes the collection manifest.
func maskBackupCharStats(dirs backupCharDirs, s string) string {
	return backupCharStatsSizeRe.ReplaceAllString(maskBackupChar(dirs, s), `${1}"<MASKED>"`)
}

func renderBackupCharacterization(t *testing.T, dirs backupCharDirs, res backupCharResult) string {
	t.Helper()
	var b strings.Builder
	section := func(title string) { fmt.Fprintf(&b, "\n===== %s =====\n", title) }

	section("result")
	exitCode := 0
	if res.err == nil {
		fmt.Fprintf(&b, "error: <nil>\n")
		if res.stats != nil {
			exitCode = res.stats.ExitCode
		}
	} else {
		var be *BackupError
		if errors.As(res.err, &be) {
			fmt.Fprintf(&b, "error: BackupError phase=%s code=%d\n", be.Phase, be.Code.Int())
			exitCode = be.Code.Int()
		} else {
			fmt.Fprintf(&b, "error: %T (not a BackupError)\n", res.err)
			exitCode = types.ExitBackupError.Int()
		}
		fmt.Fprintf(&b, "message: %s\n", maskBackupChar(dirs, res.err.Error()))
	}
	fmt.Fprintf(&b, "exit code (as cmd/proxsave computes it): %d\n", exitCode)

	section("calls to the fakes, in order")
	for _, ev := range res.recorder.list() {
		fmt.Fprintf(&b, "%s\n", maskBackupChar(dirs, ev))
	}

	section("files per location after the run")
	for _, loc := range []struct{ label, dir, ref string }{
		{"backup path (local)", dirs.backup, ""},
		{"secondary path", dirs.secondary, dirs.backup},
		{"cloud path", dirs.cloud, dirs.backup},
		{"log path", dirs.log, ""},
		{"secondary log path", dirs.secondaryLog, dirs.log},
		{"cloud log path", dirs.cloudLog, dirs.log},
		{"workspace root", dirs.workspace, ""},
	} {
		fmt.Fprintf(&b, "[%s]\n", loc.label)
		for _, line := range listBackupCharDir(loc.dir, loc.ref) {
			fmt.Fprintf(&b, "  %s\n", maskBackupChar(dirs, line))
		}
	}

	section("bundle members")
	for _, line := range listBackupCharBundles(dirs.backup) {
		fmt.Fprintf(&b, "%s\n", maskBackupChar(dirs, line))
	}

	section("stdout (console)")
	b.WriteString(maskBackupChar(dirs, res.stdout))

	section("stderr")
	b.WriteString(maskBackupChar(dirs, res.stderr))

	section("run log file")
	b.WriteString(maskBackupChar(dirs, res.logFile))

	section("BackupStats returned by RunGoBackup")
	if res.stats == nil {
		b.WriteString("<nil>\n")
	} else {
		data, err := json.MarshalIndent(res.stats, "", "  ")
		if err != nil {
			t.Fatalf("marshal stats: %v", err)
		}
		b.WriteString(maskBackupCharStats(dirs, string(data)))
		b.WriteString("\n")
	}

	section("BackupStats as seen by the notification channel")
	if res.channel.calls == 0 {
		b.WriteString("(channel not called)\n")
	} else {
		fmt.Fprintf(&b, "calls: %d\n", res.channel.calls)
		b.WriteString(maskBackupCharStats(dirs, string(res.channel.snapshot)))
		b.WriteString("\n")
	}
	return b.String()
}

// listBackupCharDir lists every entry under dir with its mode and size. The size of a
// run log is masked: the log holds durations, whose length varies. When ref is set, a
// file is also compared byte for byte with the file of the same name in ref.
func listBackupCharDir(dir, ref string) []string {
	entries := []string{}
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if info.IsDir() {
			entries = append(entries, fmt.Sprintf("%s/ mode=%04o", rel, info.Mode().Perm()))
			return nil
		}
		size := fmt.Sprintf("%d", info.Size())
		if strings.HasSuffix(rel, ".log") {
			size = "<SIZE>"
		}
		line := fmt.Sprintf("%s mode=%04o size=%s", rel, info.Mode().Perm(), size)
		if ref != "" {
			line += " " + compareBackupCharFiles(path, filepath.Join(ref, rel))
		}
		entries = append(entries, line)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return []string{"(does not exist)"}
		}
		return []string{fmt.Sprintf("(walk error: %v)", err)}
	}
	if len(entries) == 0 {
		return []string{"(empty)"}
	}
	sort.Strings(entries)
	return entries
}

func compareBackupCharFiles(path, refPath string) string {
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(read error: %v)", err)
	}
	want, err := os.ReadFile(refPath)
	if err != nil {
		return "(no file of that name at the reference location)"
	}
	if bytes.Equal(got, want) {
		return "(same bytes as the reference location)"
	}
	return "(DIFFERENT bytes from the reference location)"
}

func listBackupCharBundles(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.bundle.tar"))
	if len(matches) == 0 {
		return []string{"(no bundle)"}
	}
	sort.Strings(matches)
	lines := []string{}
	for _, bundle := range matches {
		lines = append(lines, fmt.Sprintf("%s:", filepath.Base(bundle)))
		f, err := os.Open(bundle)
		if err != nil {
			lines = append(lines, fmt.Sprintf("  (open error: %v)", err))
			continue
		}
		tr := tar.NewReader(f)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				lines = append(lines, fmt.Sprintf("  (read error: %v)", err))
				break
			}
			lines = append(lines, fmt.Sprintf("  %s size=%d mode=%o type=%c", hdr.Name, hdr.Size, hdr.Mode, hdr.Typeflag))
		}
		_ = f.Close()
	}
	return lines
}

// backupCharFakeXZFailing closes its stderr first: the archiver reads a compressor's
// stderr on a goroutine of its own, and an EOF that arrives only at exit can race the
// pipe close in Wait into a stray DEBUG line. It then drains the tar stream, so the
// writer never sees a broken pipe and the failure is the compressor's exit status alone.
var backupCharFakeXZFailing = "#!/bin/sh\nexec 2>&-\ncat >/dev/null\nexit 1\n"

var backupCharFakeTarFailing = "#!/bin/sh\necho 'tar: injected verification failure' >&2\nexit 2\n"

func TestBackupCharacterization(t *testing.T) {
	cases := []backupCharCase{
		{name: "all_ok"},
		{name: "secondary_failed", failSecond: true},
		{name: "cloud_failed", failCloud: true},
		{name: "local_failed", failLocal: true},
		{name: "dry_run", dryRun: true},
		{name: "bundle_disabled", noBundle: true},
		{name: "no_targets", noTargets: true},
		{name: "age_encryption", encrypt: true},
		{name: "verification_failed", fakeBinaries: map[string]string{"tar": backupCharFakeTarFailing}},
		{name: "compression_failed", compression: types.CompressionXZ, fakeBinaries: map[string]string{"xz": backupCharFakeXZFailing}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runBackupCharacterization(t, tc)
			assertBackupGolden(t, tc.name+".golden", []byte(got))
		})
	}
}
