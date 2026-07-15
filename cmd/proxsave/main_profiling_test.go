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

// TestCreateProfileFile_RefusesSymlinkAndExisting pins F02-06 layer 2: pprof
// files must never follow a symlink or truncate a pre-existing file (CWE-59).
func TestCreateProfileFile_RefusesSymlinkAndExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-planted symlink to a "root" file: must NOT be followed/truncated.
	target := filepath.Join(dir, "victim")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "cpu-host-ts.pprof")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := createProfileFile(link); err == nil {
		_ = f.Close()
		t.Fatal("createProfileFile followed a symlink; want error")
	}
	if b, _ := os.ReadFile(target); string(b) != "secret" {
		t.Fatalf("target was truncated/modified: %q", b)
	}

	// Pre-existing regular file: O_EXCL must refuse.
	reg := filepath.Join(dir, "heap-host-ts.pprof")
	if err := os.WriteFile(reg, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := createProfileFile(reg); err == nil {
		_ = f.Close()
		t.Fatal("createProfileFile opened a pre-existing file; want error")
	}

	// Fresh path: succeeds, 0600.
	fresh := filepath.Join(dir, "cpu-host-ts2.pprof")
	f, err := createProfileFile(fresh)
	if err != nil {
		t.Fatalf("fresh create failed: %v", err)
	}
	_ = f.Close()
	fi, err := os.Lstat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v; want 0600", fi.Mode().Perm())
	}
}

// TestBuildProfileDir_RejectsSymlink pins F02-06 layer 1: a symlinked profile
// dir (attacker interposition on a first run) is refused.
func TestBuildProfileDir_RejectsSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "realdir")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "proxsave")); err != nil {
		t.Fatal(err)
	}
	setProfileBaseDir(t, base)
	if got := buildProfileDir(); got != "" {
		t.Fatalf("buildProfileDir on symlinked dir = %q; want \"\"", got)
	}
}

// TestBuildProfileDir_RejectsOtherWritable rejects a group/other-writable dir
// where a local user could plant symlink files.
func TestBuildProfileDir_RejectsOtherWritable(t *testing.T) {
	base := t.TempDir()
	setProfileBaseDir(t, base)
	dir := filepath.Join(base, "proxsave")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil { // defeat umask
		t.Fatal(err)
	}
	if got := buildProfileDir(); got != "" {
		t.Fatalf("buildProfileDir on 0777 dir = %q; want \"\"", got)
	}
}

// TestBuildProfileDir_RejectsForeignOwner rejects a dir owned by another uid.
func TestBuildProfileDir_RejectsForeignOwner(t *testing.T) {
	base := t.TempDir()
	setProfileBaseDir(t, base)
	orig := profileEUID
	profileEUID = func() int { return orig() + 12345 }
	t.Cleanup(func() { profileEUID = orig })
	if got := buildProfileDir(); got != "" {
		t.Fatalf("buildProfileDir with owner mismatch = %q; want \"\"", got)
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
