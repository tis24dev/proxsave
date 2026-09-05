package health

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func daemonRuntimeFixture() DaemonRuntimeState {
	return DaemonRuntimeState{
		SchemaVersion: DaemonRuntimeSchemaVersion,
		PID:           4321,
		StartTS:       1_700_000_000,
		ConfigPath:    "/opt/proxsave/configs/backup.env",
		DaemonUID:     0,
		PersonalScripts: DaemonRuntimeScripts{
			Pre: DaemonRuntimeScript{
				Path:   "/home/operator/pre.sh",
				State:  "ready-with-warning",
				Reason: "/home/operator is owned by uid 1000",
				Components: []DaemonRuntimePathComponent{
					{Path: "/home/operator/pre.sh", UID: 0, Mode: 0o755},
				},
			},
			Post: DaemonRuntimeScript{State: "not-configured"},
		},
	}
}

func TestDaemonRuntimeRoundTripIsAtomicAndPrivate(t *testing.T) {
	base := t.TempDir()
	want := daemonRuntimeFixture()
	if err := WriteDaemonRuntime(base, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, found, err := ReadDaemonRuntime(base)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("read = (%+v, %v, %v), want (%+v, true, nil)", got, found, err, want)
	}
	info, err := os.Stat(DaemonRuntimePath(base))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(DaemonRuntimePath(base) + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary file survived: %v", err)
	}
}

func TestWriteDaemonRuntimeRestrictsPreexistingTemporaryFile(t *testing.T) {
	base := t.TempDir()
	tmpPath := DaemonRuntimePath(base) + ".tmp"
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteDaemonRuntime(base, daemonRuntimeFixture()); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(DaemonRuntimePath(base))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestWriteDaemonRuntimeDoesNotFollowLegacyTemporarySymlink(t *testing.T) {
	base := t.TempDir()
	want := daemonRuntimeFixture()
	victimDir := t.TempDir()
	victimPath := filepath.Join(victimDir, "victim")
	const victimContents = "must remain unchanged"
	if err := os.WriteFile(victimPath, []byte(victimContents), 0o600); err != nil {
		t.Fatal(err)
	}

	tmpPath := DaemonRuntimePath(base) + ".tmp"
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimPath, tmpPath); err != nil {
		t.Fatal(err)
	}

	if err := WriteDaemonRuntime(base, want); err != nil {
		t.Fatalf("write through protected temporary path: %v", err)
	}
	gotVictim, err := os.ReadFile(victimPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotVictim) != victimContents {
		t.Fatalf("external symlink target changed: got %q, want %q", gotVictim, victimContents)
	}
	info, err := os.Lstat(DaemonRuntimePath(base))
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("runtime mode = %s, want regular file", info.Mode())
	}
	got, found, err := ReadDaemonRuntime(base)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("read = (%+v, %v, %v), want (%+v, true, nil)", got, found, err, want)
	}
	if _, err := os.Lstat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("legacy temporary path survived: %v", err)
	}
}

func TestWriteDaemonRuntimeConcurrentWritesRemainComplete(t *testing.T) {
	base := t.TempDir()
	first := daemonRuntimeFixture()
	second := daemonRuntimeFixture()
	second.PID = 9876
	second.ConfigPath = "/opt/proxsave/configs/alternate.env"

	states := []DaemonRuntimeState{first, second}
	start := make(chan struct{})
	errs := make(chan error, len(states))
	var writers sync.WaitGroup
	for _, state := range states {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			errs <- WriteDaemonRuntime(base, state)
		}()
	}
	close(start)
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}

	got, found, err := ReadDaemonRuntime(base)
	if err != nil || !found {
		t.Fatalf("read = (%+v, %v, %v), want a complete runtime state", got, found, err)
	}
	if !reflect.DeepEqual(got, first) && !reflect.DeepEqual(got, second) {
		t.Fatalf("read mixed or partial state: %+v", got)
	}
	entries, err := os.ReadDir(filepath.Dir(DaemonRuntimePath(base)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("temporary file survived concurrent writes: %s", entry.Name())
		}
	}
}

func TestWriteDaemonRuntimeCleansTemporaryFileAfterRenameFailure(t *testing.T) {
	base := t.TempDir()
	path := DaemonRuntimePath(base)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteDaemonRuntime(base, daemonRuntimeFixture()); err == nil {
		t.Fatal("write succeeded despite non-empty directory at runtime path")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("temporary file survived failed rename: %s", entry.Name())
		}
	}
}

func TestReadDaemonRuntimeMissingEmptyAndMalformed(t *testing.T) {
	base := t.TempDir()
	zero := DaemonRuntimeState{}
	got, found, err := ReadDaemonRuntime(base)
	if err != nil || found || !reflect.DeepEqual(got, zero) {
		t.Fatalf("missing = (%+v, %v, %v)", got, found, err)
	}

	path := DaemonRuntimePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	got, found, err = ReadDaemonRuntime(base)
	if err != nil || found || !reflect.DeepEqual(got, zero) {
		t.Fatalf("empty = (%+v, %v, %v)", got, found, err)
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, found, err = ReadDaemonRuntime(base)
	if err == nil || found || !reflect.DeepEqual(got, zero) {
		t.Fatalf("malformed = (%+v, %v, %v), want zero/false/error", got, found, err)
	}
}

func TestReadDaemonRuntimeReportsNonFile(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(DaemonRuntimePath(base), 0o750); err != nil {
		t.Fatal(err)
	}

	got, found, err := ReadDaemonRuntime(base)
	if err == nil || found || !reflect.DeepEqual(got, DaemonRuntimeState{}) {
		t.Fatalf("read directory = (%+v, %v, %v), want zero/false/error", got, found, err)
	}
}

func TestRemoveDaemonRuntimeIsIdempotent(t *testing.T) {
	base := t.TempDir()
	if err := WriteDaemonRuntime(base, daemonRuntimeFixture()); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDaemonRuntime(base); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDaemonRuntime(base); err != nil {
		t.Fatalf("second removal: %v", err)
	}
}

func TestRemoveDaemonRuntimeReportsNonEmptyDirectory(t *testing.T) {
	base := t.TempDir()
	path := DaemonRuntimePath(base)
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := RemoveDaemonRuntime(base); err == nil {
		t.Fatal("remove succeeded for non-empty runtime directory")
	}
}
