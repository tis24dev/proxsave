package storage

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The Primary, the Secondary and the local cloud form build their detectors with the
// dry run, so a dry run never runs the network ownership write-probe.
func TestDryRunReachesEveryDetector(t *testing.T) {
	cfg := &config.Config{DryRun: true, BackupPath: t.TempDir(), SecondaryPath: t.TempDir(), CloudEnabled: true, CloudRemote: t.TempDir()}
	local, err := NewLocalStorage(cfg, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	secondary, err := NewSecondaryStorage(cfg, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	cloud, err := NewCloudStorage(cfg, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	for name, d := range map[string]*FilesystemDetector{"primary": local.fsDetector, "secondary": secondary.fsDetector, "cloud": cloud.fsDetector} {
		if d == nil || !d.dryRun {
			t.Fatalf("%s detector must be built with the dry run", name)
		}
	}
	cfg.DryRun = false
	local, _ = NewLocalStorage(cfg, newTestLogger(), "")
	if local.fsDetector.dryRun {
		t.Fatalf("a real run keeps the probe")
	}
}

// Dry run on a network filesystem: the ownership probe does not run (the existing
// DEBUG line says so) and nothing is written in the directory, a real temp dir.
func TestDryRunSkipsTheOwnershipProbe(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		dir := t.TempDir()
		logger := logging.New(types.LogLevelDebug, false)
		buf := &bytes.Buffer{}
		logger.SetOutput(buf)
		s, err := NewSecondaryStorage(&config.Config{DryRun: dryRun, SecondaryEnabled: true, SecondaryPath: dir}, logger, "")
		if err != nil {
			t.Fatalf("NewSecondaryStorage: %v", err)
		}
		s.fsDetector.mountPointLookup = func(string) (string, error) { return dir, nil }
		s.fsDetector.filesystemTypeLookup = func(context.Context, string) (FilesystemType, string, error) {
			return FilesystemNFS4, "nas:/export", nil
		}
		probes := 0
		s.fsDetector.ownershipSupportTest = func(ctx context.Context, path string) bool {
			probes++
			return s.fsDetector.testOwnershipSupport(ctx, path)
		}
		if _, err := s.DetectFilesystem(context.Background()); err != nil {
			t.Fatalf("DetectFilesystem(dryRun=%v): %v", dryRun, err)
		}
		if dryRun {
			if probes != 0 || !strings.Contains(buf.String(), "DRY RUN: skipping network-FS ownership write-probe") {
				t.Fatalf("dry run: probes=%d, want 0 and the DEBUG line:\n%s", probes, buf.String())
			}
		} else if probes != 1 {
			t.Fatalf("real run: probes=%d, want 1", probes)
		}
		if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
			t.Fatalf("dryRun=%v: the directory must be left empty, got %v (%v)", dryRun, entries, err)
		}
	}
}

// Dry run, missing destination: the Secondary and the local cloud form do not create
// it and stop with a DirectoryMissingError; an existing one goes on unchanged. The
// Primary keeps creating BACKUP_PATH.
func TestDryRunCreatesNoDestinationDirectory(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "secondary", "sub")
	s, err := NewSecondaryStorage(&config.Config{DryRun: true, SecondaryEnabled: true, SecondaryPath: missing}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewSecondaryStorage: %v", err)
	}
	_, err = s.DetectFilesystem(context.Background())
	var missingErr *DirectoryMissingError
	if !errors.As(err, &missingErr) || missingErr.Path != missing {
		t.Fatalf("Secondary DetectFilesystem = %v, want a DirectoryMissingError for %s", err, missing)
	}
	if _, err := os.Stat(filepath.Join(base, "secondary")); !os.IsNotExist(err) {
		t.Fatalf("a dry run must not create %s or its parents (stat err=%v)", missing, err)
	}

	cloudRoot := filepath.Join(base, "cloud")
	logger, buf := newCapturedLogger()
	cs, err := NewCloudStorage(&config.Config{DryRun: true, CloudEnabled: true, CloudRemote: cloudRoot, CloudRemotePath: "host1"}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	rec := &argvRecorder{}
	cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
	cs.execCommand = rec.exec
	if _, err := cs.DetectFilesystem(context.Background()); !errors.As(err, &missingErr) {
		t.Fatalf("cloud DetectFilesystem = %v, want a DirectoryMissingError", err)
	}
	want := "INFO     Checking cloud remote accessibility...\nINFO       Directory: missing, not created in dry run"
	if got := strings.Join(visibleOf(buf.String()), "\n"); got != want {
		t.Fatalf("visible lines =\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(cloudRoot); !os.IsNotExist(err) || len(rec.argv()) != 0 {
		t.Fatalf("a dry run must create nothing and run no rclone: stat err=%v argv=%v", err, rec.argv())
	}

	// An existing directory: nothing changes in a dry run.
	existing := t.TempDir()
	s, _ = NewSecondaryStorage(&config.Config{DryRun: true, SecondaryEnabled: true, SecondaryPath: existing}, newTestLogger(), "")
	if _, err := s.DetectFilesystem(context.Background()); errors.As(err, &missingErr) {
		t.Fatalf("an existing directory must not be reported missing: %v", err)
	}

	primary := filepath.Join(base, "primary", "backups")
	local, err := NewLocalStorage(&config.Config{DryRun: true, BackupPath: primary}, newTestLogger(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if _, err := local.DetectFilesystem(context.Background()); err != nil {
		t.Fatalf("Primary DetectFilesystem: %v", err)
	}
	if st, err := os.Stat(primary); err != nil || !st.IsDir() {
		t.Fatalf("the Primary keeps creating BACKUP_PATH in a dry run: %v", err)
	}
}

// Dry run, cloud with a real remote: read-only. Only lsf runs (no mkdir, no write
// test, even with CLOUD_WRITE_HEALTHCHECK=true); a missing backup directory is
// "  Directory: missing, not created in dry run"; a listing that is not permitted
// leaves the remote not checked.
func TestDryRunCloudRemoteIsReadOnly(t *testing.T) {
	missingDir := []byte("2026/10/03 10:00:00 ERROR : backups/host1: error listing: directory not found\n")
	denied := []byte("2026/10/03 10:00:00 ERROR : : error listing: 403 Forbidden: access denied\n")
	for _, tc := range []struct {
		name       string
		writeCheck bool
		respond    func(args []string) ([]byte, error)
		wantErr    bool
		wantArgv   []string
		wantFact   string
		notChecked bool
	}{
		{"existing directory", false, nil, false,
			[]string{"lsf remote: --max-depth 1", "lsf remote:backups/host1 --max-depth 1"}, "INFO       Accessible", false},
		{"write health check ignored", true, nil, false,
			[]string{"lsf remote: --max-depth 1", "lsf remote:backups/host1 --max-depth 1"}, "INFO       Accessible", false},
		{"missing directory", false, func(args []string) ([]byte, error) {
			if args[0] == "lsf" && args[1] == "remote:backups/host1" {
				return missingDir, errors.New("exit status 3")
			}
			return nil, nil
		}, true, nil, "INFO       Directory: missing, not created in dry run", false},
		{"listing not permitted", true, func(args []string) ([]byte, error) {
			if args[0] == "lsf" {
				return denied, errors.New("exit status 1")
			}
			return nil, nil
		}, false, []string{"lsf remote: --max-depth 1"}, "INFO       Listing not permitted, write test skipped in dry run", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &argvRecorder{respond: tc.respond}
			logger, buf := newCapturedLogger()
			cs, err := NewCloudStorage(&config.Config{DryRun: true, CloudEnabled: true, CloudRemote: "remote:backups", CloudRemotePath: "host1",
				CloudWriteHealthCheck: tc.writeCheck, RcloneTimeoutConnection: 30}, logger, "")
			if err != nil {
				t.Fatalf("NewCloudStorage: %v", err)
			}
			cs.lookPath = func(string) (string, error) { return "/usr/bin/rclone", nil }
			cs.execCommand = rec.exec
			cs.waitForRetry = func(context.Context, time.Duration) error { return nil }
			_, err = cs.DetectFilesystem(context.Background())
			var missingErr *DirectoryMissingError
			if tc.wantErr != (err != nil) || (tc.wantErr && !errors.As(err, &missingErr)) {
				t.Fatalf("DetectFilesystem = %v, wantErr=%v (a DirectoryMissingError)", err, tc.wantErr)
			}
			for _, line := range rec.argv() {
				if !strings.HasPrefix(line, "lsf ") {
					t.Fatalf("a dry run runs only lsf on the remote, got %q (all: %v)", line, rec.argv())
				}
			}
			if tc.wantArgv != nil {
				requireArgv(t, rec.argv(), tc.wantArgv...)
			}
			got := visibleOf(buf.String())
			if len(got) != 2 || got[1] != tc.wantFact {
				t.Fatalf("visible lines =\n%s\nwant the check line and %q", strings.Join(got, "\n"), tc.wantFact)
			}
			if cs.NotCheckedInDryRun() != tc.notChecked {
				t.Fatalf("NotCheckedInDryRun = %v, want %v", cs.NotCheckedInDryRun(), tc.notChecked)
			}
		})
	}
}
