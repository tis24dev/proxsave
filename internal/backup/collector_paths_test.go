package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectorProxsavePathHelpers(t *testing.T) {
	collector := NewCollector(logging.New(types.LogLevelDebug, false), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)

	wantRoot := filepath.Join(collector.tempDir, "var/lib/proxsave-info")
	if got := collector.proxsaveInfoRoot(); got != wantRoot {
		t.Fatalf("proxsaveInfoRoot() = %q; want %q", got, wantRoot)
	}

	wantInfoDir := filepath.Join(wantRoot, "runtime", "pbs", "nested")
	if got := collector.proxsaveInfoDir("runtime", "pbs", "nested"); got != wantInfoDir {
		t.Fatalf("proxsaveInfoDir() = %q; want %q", got, wantInfoDir)
	}

	wantCommandsDir := filepath.Join(wantRoot, "commands", "pbs")
	if got := collector.proxsaveCommandsDir("pbs"); got != wantCommandsDir {
		t.Fatalf("proxsaveCommandsDir() = %q; want %q", got, wantCommandsDir)
	}

	wantRuntimeDir := filepath.Join(wantRoot, "runtime", "pve")
	if got := collector.proxsaveRuntimeDir("pve"); got != wantRuntimeDir {
		t.Fatalf("proxsaveRuntimeDir() = %q; want %q", got, wantRuntimeDir)
	}
}

func TestCollectorEnsureCommandsDir(t *testing.T) {
	t.Run("creates directory", func(t *testing.T) {
		collector := NewCollector(logging.New(types.LogLevelDebug, false), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)

		dir, err := collector.ensureCommandsDir("pbs")
		if err != nil {
			t.Fatalf("ensureCommandsDir() returned error: %v", err)
		}

		want := filepath.Join(collector.proxsaveInfoRoot(), "commands", "pbs")
		if dir != want {
			t.Fatalf("ensureCommandsDir() = %q; want %q", dir, want)
		}
		if info, statErr := collector.depStat(dir); statErr != nil || !info.IsDir() {
			t.Fatalf("expected commands dir %q to exist as directory, stat=%v info=%v", dir, statErr, info)
		}
	})

	t.Run("propagates ensure dir failure", func(t *testing.T) {
		collector := NewCollector(logging.New(types.LogLevelDebug, false), GetDefaultCollectorConfig(), t.TempDir(), types.ProxmoxUnknown, false)

		blockingPath := collector.proxsaveCommandsDir("pbs")
		if err := collector.ensureDir(filepath.Dir(blockingPath)); err != nil {
			t.Fatalf("ensureDir(parent) returned error: %v", err)
		}
		if err := os.WriteFile(blockingPath, []byte("blocked"), 0o644); err != nil {
			t.Fatalf("WriteFile(blockingPath) returned error: %v", err)
		}

		_, err := collector.ensureCommandsDir("pbs")
		if err == nil {
			t.Fatal("ensureCommandsDir() = nil error; want wrapped mkdir failure")
		}
		if !strings.Contains(err.Error(), "failed to create commands directory") {
			t.Fatalf("ensureCommandsDir() error = %q; want wrapped commands directory context", err.Error())
		}
	})
}
