package health

import (
	"os"
	"path/filepath"
	"reflect"
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
