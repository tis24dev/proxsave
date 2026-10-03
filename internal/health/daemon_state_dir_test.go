package health

import (
	"os"
	"path/filepath"
	"testing"
)

// Every daemon state file lives in BASE_DIR/daemon_state; only the two copies written for the
// previous release's upgrade verification live in identity/.
func TestDaemonStatePathsLiveInDaemonState(t *testing.T) {
	base := "/opt/proxsave"
	stateDir := filepath.Join(base, "daemon_state")
	for name, got := range map[string]string{
		".daemon.pid":                 DaemonPIDPath(base),
		".daemon_info.json":           DaemonInfoPath(base),
		".daemon_runtime.json":        DaemonRuntimePath(base),
		".daemon_abandoned.json":      AbandonPath(base),
		".healthcheck_status.json":    StatusPath(base),
		".notify_results.json":        NotifyResultsPath(base),
		".manual_backup_outcome.json": ManualOutcomePath(base),
	} {
		if want := filepath.Join(stateDir, name); got != want {
			t.Fatalf("%s path = %q, want %q", name, got, want)
		}
	}
	if got, want := LegacyDaemonPIDPath(base), filepath.Join(base, "identity", ".daemon.pid"); got != want {
		t.Fatalf("LegacyDaemonPIDPath = %q, want %q", got, want)
	}
	if got, want := LegacyDaemonInfoPath(base), filepath.Join(base, "identity", ".daemon_info.json"); got != want {
		t.Fatalf("LegacyDaemonInfoPath = %q, want %q", got, want)
	}
}

// Whichever writer creates daemon_state/ first, it is created root-only (0700).
func TestDaemonStateWritersCreateTheDirRootOnly(t *testing.T) {
	writers := map[string]func(base string) error{
		"WriteDaemonPID":     func(base string) error { return WriteDaemonPID(base, 42) },
		"WriteDaemonInfo":    func(base string) error { return WriteDaemonInfo(base, DaemonInfo{PID: 42}) },
		"WriteDaemonRuntime": func(base string) error { return WriteDaemonRuntime(base, DaemonRuntimeState{PID: 42}) },
		"WriteAbandon":       func(base string) error { return WriteAbandon(base, AbandonRecord{PID: 42}) },
		"RecordPing": func(base string) error {
			return RecordPing(base, "centralized", KindHeartbeat, 1, true, nil)
		},
		"WriteNotifyResults": func(base string) error { return WriteNotifyResults(base, "rid", 1, map[string]string{"Email": "ok"}) },
		"WriteManualOutcome": func(base string) error { return WriteManualOutcome(base, "rid", 1, 0) },
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			if err := write(base); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			info, err := os.Stat(DaemonStateDir(base))
			if err != nil {
				t.Fatalf("stat daemon_state: %v", err)
			}
			if got := info.Mode().Perm(); got != 0o700 {
				t.Fatalf("%s created daemon_state %o, want 0700", name, got)
			}
			if _, err := os.Stat(LegacyIdentityDir(base)); !os.IsNotExist(err) {
				t.Fatalf("%s created identity/ (stat err = %v)", name, err)
			}
		})
	}
}

// The identity/ copies are written for an older reader only: this release reads daemon_state/
// and never falls back to identity/.
func TestLegacyCopiesAreNeverReadBack(t *testing.T) {
	base := t.TempDir()
	if err := WriteLegacyDaemonPID(base, 4242); err != nil {
		t.Fatalf("WriteLegacyDaemonPID: %v", err)
	}
	if err := WriteLegacyDaemonInfo(base, DaemonInfo{PID: 4242, StartTS: 7}); err != nil {
		t.Fatalf("WriteLegacyDaemonInfo: %v", err)
	}
	if data, err := os.ReadFile(LegacyDaemonPIDPath(base)); err != nil || string(data) != "4242\n" {
		t.Fatalf("identity/.daemon.pid = (%q, %v), want 4242", data, err)
	}
	if pid, err := ReadDaemonPID(base); err != nil || pid != 0 {
		t.Fatalf("ReadDaemonPID = (%d, %v), want (0, nil): no read fallback to identity/", pid, err)
	}
	if _, ok, err := ReadDaemonInfo(base); err != nil || ok {
		t.Fatalf("ReadDaemonInfo = (ok=%v, %v), want no record: no read fallback to identity/", ok, err)
	}
}
