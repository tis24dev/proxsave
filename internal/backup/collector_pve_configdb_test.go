package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// config.db is the pmxcfs SQLite backing store, the artifact RECOVERY writes back
// verbatim - and it runs in WAL mode: on a live node the base file is stale up to
// the last checkpoint (measured on the test node: base 40KB hours old, -wal 4.1MB
// current) and a plain io.Copy can also tear a page mid-write. The capture must go
// through sqlite3's .backup (the online-backup API reads base+WAL consistently);
// the raw copy survives only as a loudly-announced fallback.

func configDBCollector(t *testing.T, deps CollectorDeps) (*Collector, string, *bytes.Buffer) {
	t.Helper()
	clusterDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(clusterDir, "config.db"), []byte("RAW-BASE-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	cfg := &CollectorConfig{BackupClusterConfig: true, BackupPVEACL: true, PVEClusterPath: clusterDir}
	return NewCollectorWithDeps(logger, cfg, t.TempDir(), types.ProxmoxVE, false, deps), clusterDir, buf
}

func TestConfigDBCaptureGoesThroughSQLiteBackup(t *testing.T) {
	var gotName string
	var gotArgs []string
	deps := CollectorDeps{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			gotName = name
			gotArgs = args
			// The real .backup writes the destination; the fake proves the
			// collector trusts THIS artifact and does not overwrite it raw.
			dest := strings.TrimSuffix(strings.TrimPrefix(args[len(args)-1], `.backup "`), `"`)
			if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(dest, []byte("SQLITE-SNAPSHOT"), 0o600)
		},
	}
	c, clusterDir, buf := configDBCollector(t, deps)

	if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
		t.Fatalf("collectPVEClusterSnapshot: %v", err)
	}
	if gotName != "sqlite3" {
		t.Fatalf("config.db was not captured via sqlite3 (ran %q %v):\n%s", gotName, gotArgs, buf.String())
	}
	src := filepath.Join(clusterDir, "config.db")
	wantArgs := fmt.Sprintf("-cmd|.timeout 5000|%s|.backup \"%s\"", src, c.targetPathFor(src))
	if strings.Join(gotArgs, "|") != wantArgs {
		t.Fatalf("sqlite3 argv drifted:\n got %q\nwant %q", strings.Join(gotArgs, "|"), wantArgs)
	}
	captured, err := os.ReadFile(c.targetPathFor(src))
	if err != nil {
		t.Fatal(err)
	}
	if string(captured) != "SQLITE-SNAPSHOT" {
		t.Fatalf("the snapshot artifact was overwritten by a raw copy: %q", captured)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("a clean snapshot must not warn:\n%s", buf.String())
	}
}

func TestConfigDBRawFallbackIsLoud(t *testing.T) {
	deps := CollectorDeps{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return []byte("Error: database is locked"), errors.New("exit status 1")
		},
	}
	c, clusterDir, buf := configDBCollector(t, deps)

	if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
		t.Fatalf("collectPVEClusterSnapshot: %v", err)
	}
	src := filepath.Join(clusterDir, "config.db")
	captured, err := os.ReadFile(c.targetPathFor(src))
	if err != nil {
		t.Fatalf("the raw fallback did not capture config.db at all: %v", err)
	}
	if string(captured) != "RAW-BASE-BYTES" {
		t.Fatalf("fallback content drifted: %q", captured)
	}
	out := buf.String()
	var warned bool
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "] WARNING") && strings.Contains(l, "raw copy") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("the raw fallback must WARN that the capture can be torn and misses WAL data:\n%s", out)
	}
}

func TestConfigDBWithoutSQLite3FallsBackWithoutRunning(t *testing.T) {
	ran := false
	deps := CollectorDeps{
		LookPath:   func(name string) (string, error) { return "", errors.New("not found") },
		RunCommand: func(ctx context.Context, name string, args ...string) ([]byte, error) { ran = true; return nil, nil },
	}
	c, clusterDir, buf := configDBCollector(t, deps)
	if err := c.collectPVEClusterSnapshot(context.Background(), false); err != nil {
		t.Fatalf("collectPVEClusterSnapshot: %v", err)
	}
	if ran {
		t.Fatal("sqlite3 was executed despite LookPath saying it is absent")
	}
	if captured, err := os.ReadFile(c.targetPathFor(filepath.Join(clusterDir, "config.db"))); err != nil || string(captured) != "RAW-BASE-BYTES" {
		t.Fatalf("raw fallback missing without sqlite3: %q err=%v\n%s", captured, err, buf.String())
	}
}
