package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/whatsnew"
)

// The Location comes from the configuration: the flag in LOG_PATH, bounded by FS_IO_TIMEOUT.
func TestWhatsnewLocationFromConfig(t *testing.T) {
	cfg := &config.Config{LogPath: "/srv/log", FsIoTimeoutSeconds: 12}
	got := whatsnewLocationFromConfig(cfg, "/opt/proxsave")
	want := whatsnew.Location{LogPath: "/srv/log", BaseDir: "/opt/proxsave", Timeout: 12 * time.Second}
	if got != want {
		t.Fatalf("whatsnewLocationFromConfig = %+v, want %+v", got, want)
	}
	cfg.FsIoTimeoutSeconds = 0 // FS_IO_TIMEOUT=0: unbounded
	if got := whatsnewLocationFromConfig(cfg, "/opt/proxsave"); got.Timeout != 0 {
		t.Fatalf("FS_IO_TIMEOUT=0 -> Timeout %s, want 0 (unbounded)", got.Timeout)
	}
}

// Without a readable configuration LOG_PATH is unknown and the dashboard path stays silent
// with the REAL Decide: no screen, no write.
func TestWhatsnewWithoutConfigIsSilent(t *testing.T) {
	stubWhatsnewSeams(t)
	stubWhatsnewLoadConfig(t, func(string, string) (*config.Config, error) {
		return nil, errors.New("configuration file not found")
	})
	base := t.TempDir()
	loc := whatsnewLocation("/nonexistent/backup.env", base)
	if loc.LogPath != "" || loc.BaseDir != base {
		t.Fatalf("whatsnewLocation without config = %+v, want no LogPath and BaseDir %q", loc, base)
	}
	saveCalls := 0
	whatsnewSaveSeen = func(whatsnew.Location, string) error { saveCalls++; return nil }

	if show, body := whatsnewResolve(loc, "0.40.0"); show || body != "" {
		t.Fatalf("whatsnewResolve without config = (%v, %q), want silent", show, body)
	}
	if saveCalls != 0 {
		t.Fatalf("whatsnewSaveSeen called %d times without config, want 0", saveCalls)
	}
}

// The dashboard reads the seen-flag where the configuration says (LOG_PATH, FS_IO_TIMEOUT).
func TestDashboardReadsSeenFlagFromConfiguredLogPath(t *testing.T) {
	installDashboardGates(t, true, true)
	driver := installDashboardSessionSeam(t)
	var gotConfigPath string
	stubWhatsnewLoadConfig(t, func(cp, bd string) (*config.Config, error) {
		gotConfigPath = cp
		return &config.Config{LogPath: "/srv/proxsave-log", FsIoTimeoutSeconds: 9}, nil
	})
	prevDecide := whatsnewDecide
	t.Cleanup(func() { whatsnewDecide = prevDecide })
	locs := make(chan whatsnew.Location, 1)
	whatsnewDecide = func(loc whatsnew.Location, current string) (bool, string, error) {
		select {
		case locs <- loc:
		default:
		}
		return false, "", nil
	}

	args := &cli.Args{ConfigPath: "/etc/proxsave/backup.env"}
	res := driver.spawn(args)
	driver.waitScreen("Dashboard")
	driver.keys("esc")
	select {
	case <-res:
	case <-time.After(60 * time.Second):
		t.Fatal("dashboard did not resolve")
	}

	select {
	case loc := <-locs:
		wantBase, _ := detectedBaseDirOrFallback()
		want := whatsnew.Location{LogPath: "/srv/proxsave-log", BaseDir: wantBase, Timeout: 9 * time.Second}
		if loc != want {
			t.Fatalf("dashboard seen-flag location = %+v, want %+v", loc, want)
		}
	default:
		t.Fatal("the dashboard never consulted the seen-flag")
	}
	if gotConfigPath != args.ConfigPath {
		t.Fatalf("dashboard read config %q, want %q", gotConfigPath, args.ConfigPath)
	}
}

// writeDaemonStateAndIdentityCopies lays out what the first daemon start after an upgrade leaves:
// .daemon.pid and .daemon_info.json in daemon_state/ and in identity/.
func writeDaemonStateAndIdentityCopies(t *testing.T, base string) {
	t.Helper()
	for _, p := range []string{
		health.DaemonPIDPath(base), health.LegacyDaemonPIDPath(base),
	} {
		writeTestFile(t, p, "4242\n")
	}
	for _, p := range []string{
		health.DaemonInfoPath(base), health.LegacyDaemonInfoPath(base),
	} {
		writeTestFile(t, p, `{"pid":4242}`)
	}
}

// The dashboard's what's-new check removes the identity/ copies once daemon_state/ has the files,
// silently.
func TestDashboardWhatsnewCheckRemovesIdentityDaemonCopies(t *testing.T) {
	stubWhatsnewSeams(t)
	whatsnewDecide = func(whatsnew.Location, string) (bool, string, error) { return false, "", nil }
	base := t.TempDir()
	writeDaemonStateAndIdentityCopies(t, base)

	maybeShowWhatsnew(context.Background(), nil, testWhatsnewLoc(base), "0.40.0")

	requireAbsent(t, health.LegacyDaemonPIDPath(base))
	requireAbsent(t, health.LegacyDaemonInfoPath(base))
	if _, err := os.Stat(health.DaemonPIDPath(base)); err != nil {
		t.Fatalf("daemon_state/.daemon.pid removed: %v", err)
	}
	if _, err := os.Stat(health.DaemonInfoPath(base)); err != nil {
		t.Fatalf("daemon_state/.daemon_info.json removed: %v", err)
	}
}

// The run's what's-new check removes them too, with a DEBUG line per file; under --dry-run it
// touches nothing.
func TestRunWhatsnewCheckRemovesIdentityDaemonCopies(t *testing.T) {
	t.Run("removes with DEBUG evidence", func(t *testing.T) {
		stubWhatsnewShouldWarn(t, func(whatsnew.Location, string) (bool, string, error) { return false, "", nil })
		base := t.TempDir()
		writeDaemonStateAndIdentityCopies(t, base)
		logger, buf := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.40.0", false)

		requireAbsent(t, health.LegacyDaemonPIDPath(base))
		requireAbsent(t, health.LegacyDaemonInfoPath(base))
		for _, want := range []string{
			"daemon state: removed .daemon.pid from identity/, daemon_state/ has it",
			"daemon state: removed .daemon_info.json from identity/, daemon_state/ has it",
		} {
			if !strings.Contains(buf.String(), want) {
				t.Fatalf("missing DEBUG line %q\n%s", want, buf.String())
			}
		}
		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d, want 0 (DEBUG only)", got)
		}
	})
	t.Run("dry-run keeps them and checks read-only", func(t *testing.T) {
		var gotLoc whatsnew.Location
		stubWhatsnewShouldWarn(t, func(loc whatsnew.Location, _ string) (bool, string, error) {
			gotLoc = loc
			return false, "", nil
		})
		base := t.TempDir()
		writeDaemonStateAndIdentityCopies(t, base)
		logger, _ := captureLogger(t)

		maybeWarnWhatsnew(logger, testWhatsnewLoc(base), "0.40.0", true)

		if _, err := os.Stat(health.LegacyDaemonPIDPath(base)); err != nil {
			t.Fatalf("dry-run removed identity/.daemon.pid: %v", err)
		}
		if !gotLoc.ReadOnly {
			t.Fatal("dry-run check did not run read-only")
		}
	})
}

// End to end on the run: an upgraded host's acknowledgement in identity/ moves to LOG_PATH and
// keeps the nudge quiet; under --dry-run nothing moves and the nudge stays quiet too.
func TestRunWhatsnewMovesTheSeenFlagFromIdentity(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		name := "run"
		if dryRun {
			name = "dry-run"
		}
		t.Run(name, func(t *testing.T) {
			stubWhatsnewSeams(t)
			base := t.TempDir()
			logPath := filepath.Join(t.TempDir(), "log")
			legacy := whatsnew.LegacyStatePath(base)
			writeTestFile(t, legacy, `{"last_seen_notes_version":"0.40.0"}`)
			logger, _ := captureLogger(t)

			maybeWarnWhatsnew(logger, whatsnew.Location{LogPath: logPath, BaseDir: base}, "0.40.0", dryRun)

			if got := logger.WarningCount(); got != 0 {
				t.Fatalf("WarningCount = %d, want 0: 0.40.0 was acknowledged before the upgrade", got)
			}
			_, legacyErr := os.Stat(legacy)
			_, movedErr := os.Stat(whatsnew.StatePath(logPath))
			if dryRun {
				if legacyErr != nil || !os.IsNotExist(movedErr) {
					t.Fatalf("dry-run moved the flag: legacy err=%v, LOG_PATH err=%v", legacyErr, movedErr)
				}
				return
			}
			if !os.IsNotExist(legacyErr) || movedErr != nil {
				t.Fatalf("flag not moved: legacy err=%v, LOG_PATH err=%v", legacyErr, movedErr)
			}
		})
	}
}

// A seen-flag read that outlives FS_IO_TIMEOUT (a dead LOG_PATH mount) keeps both callers
// silent and bounded: no Screen 0, no nudge, no write.
func TestWhatsnewHungLogPathIsSilent(t *testing.T) {
	logPath := t.TempDir()
	flag := whatsnew.StatePath(logPath)
	if err := syscall.Mkfifo(flag, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			f, err := os.OpenFile(flag, os.O_WRONLY|syscall.O_NONBLOCK, 0)
			if err != nil {
				return
			}
			_ = f.Close()
			time.Sleep(10 * time.Millisecond)
		}
	})
	loc := whatsnew.Location{LogPath: logPath, BaseDir: t.TempDir(), Timeout: 100 * time.Millisecond}

	t.Run("dashboard", func(t *testing.T) {
		stubWhatsnewSeams(t) // real Decide
		runCalls, saveCalls := 0, 0
		whatsnewRun = func(context.Context, *shell.Session, string) error { runCalls++; return nil }
		whatsnewSaveSeen = func(whatsnew.Location, string) error { saveCalls++; return nil }

		done := make(chan struct{})
		go func() {
			defer close(done)
			maybeShowWhatsnew(context.Background(), nil, loc, "0.40.0")
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("dashboard what's-new check hung on a dead LOG_PATH")
		}
		if runCalls != 0 || saveCalls != 0 {
			t.Fatalf("hung flag: whatsnewRun=%d whatsnewSaveSeen=%d, want 0 and 0", runCalls, saveCalls)
		}
	})
	t.Run("run", func(t *testing.T) {
		stubWhatsnewSeams(t) // real ShouldWarn
		logger, buf := captureLogger(t)
		maybeWarnWhatsnew(logger, loc, "0.40.0", false)
		if got := logger.WarningCount(); got != 0 {
			t.Fatalf("WarningCount = %d on a hung flag, want 0", got)
		}
		if !strings.Contains(buf.String(), "gate error") || !strings.Contains(buf.String(), "timeout") {
			t.Fatalf("missing the gate-error DEBUG line naming the timeout\n%s", buf.String())
		}
	})
}
