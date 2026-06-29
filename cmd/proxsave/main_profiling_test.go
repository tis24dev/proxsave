package main

import (
	"os"
	"path/filepath"
	"runtime/pprof"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

func setProfileBaseDir(t *testing.T, v string) {
	t.Helper()
	orig := profileBaseDir
	profileBaseDir = v
	t.Cleanup(func() { profileBaseDir = orig })
}

func TestBuildProfileDir(t *testing.T) {
	base := t.TempDir()
	setProfileBaseDir(t, base)
	got := buildProfileDir()
	want := filepath.Join(base, "proxsave")
	if got != want {
		t.Fatalf("buildProfileDir = %q; want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Fatalf("profile dir not created: stat err = %v", err)
	}

	// MkdirAll failure (base is a regular file) -> "" so the caller skips profiling.
	fileBase := filepath.Join(t.TempDir(), "iamafile")
	if err := os.WriteFile(fileBase, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	setProfileBaseDir(t, fileBase)
	if got := buildProfileDir(); got != "" {
		t.Fatalf("buildProfileDir on a non-dir base = %q; want \"\"", got)
	}
}

// initializeRunProfiling must write BOTH profiles under <profileBaseDir>/proxsave
// and NOTHING under LOG_PATH (issue #242: LOG_PATH may be a dead/stale mount).
func TestInitializeRunProfiling_WritesOffLogPath(t *testing.T) {
	base := t.TempDir()
	logDir := t.TempDir()
	setProfileBaseDir(t, base)

	rt := &appRuntime{
		cfg:          &config.Config{ProfilingEnabled: true, LogPath: logDir},
		hostname:     "host",
		timestampStr: "ts",
	}
	initializeRunProfiling(rt)
	// Always stop the (process-global) profiler, even if an assertion fails.
	t.Cleanup(func() {
		if rt.cpuProfileFile != nil {
			pprof.StopCPUProfile()
			_ = rt.cpuProfileFile.Close()
		}
	})

	if rt.cpuProfileFile == nil {
		t.Fatal("profiling should be enabled (cpuProfileFile is nil)")
	}
	wantDir := filepath.Join(base, "proxsave")
	if cpus, _ := filepath.Glob(filepath.Join(wantDir, "cpu-*.pprof")); len(cpus) != 1 {
		t.Fatalf("want 1 cpu profile under %s, got %v", wantDir, cpus)
	}
	if dir := filepath.Dir(rt.heapProfilePath); dir != wantDir {
		t.Fatalf("heapProfilePath dir = %s; want %s", dir, wantDir)
	}
	if leaked, _ := filepath.Glob(filepath.Join(logDir, "*.pprof")); len(leaked) != 0 {
		t.Fatalf("no profile may be written under LOG_PATH, got %v", leaked)
	}
}

func TestInitializeRunProfiling_DisabledIsNoOp(t *testing.T) {
	rt := &appRuntime{
		cfg:          &config.Config{ProfilingEnabled: false, LogPath: t.TempDir()},
		hostname:     "host",
		timestampStr: "ts",
	}
	initializeRunProfiling(rt)
	if rt.cpuProfileFile != nil || rt.heapProfilePath != "" {
		t.Fatalf("disabled profiling must be a no-op; cpuProfileFile=%v heapProfilePath=%q", rt.cpuProfileFile, rt.heapProfilePath)
	}
}
