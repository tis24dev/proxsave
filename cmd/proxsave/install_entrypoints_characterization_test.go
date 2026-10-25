package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// These tests pin cleanupGlobalProxmoxBackupEntrypoints as it behaves today: every
// line it logs, in order and at its level, the removed/kept counters, and which
// entries are still on disk afterwards. They are the safety net for moving its
// filesystem calls onto os.Root, so they assert outcomes, never the mechanism.

var entrypointLogLine = regexp.MustCompile(`^\[[^\]]*\]\s+(\S+)\s+(.*)$`)

// runEntrypointCleanup runs the cleanup with PATH and globalEntrypointDirs set as
// given and returns every logged line as "LEVEL message", with base replaced by
// "<base>" so the expectations do not depend on the temp directory.
func runEntrypointCleanup(t *testing.T, base, execPath, pathEnv string, globals []string) []string {
	t.Helper()
	prev := globalEntrypointDirs
	globalEntrypointDirs = globals
	t.Cleanup(func() { globalEntrypointDirs = prev })
	t.Setenv("PATH", pathEnv)

	boot := logging.NewBootstrapLogger()
	boot.SetConsoleQuiet(true)
	buf := &bytes.Buffer{}
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(buf)
	boot.SetMirrorLogger(mirror)

	cleanupGlobalProxmoxBackupEntrypoints(execPath, boot)

	var lines []string
	for raw := range strings.SplitSeq(strings.TrimRight(buf.String(), "\n"), "\n") {
		if raw == "" {
			continue
		}
		m := entrypointLogLine.FindStringSubmatch(raw)
		if m == nil {
			t.Fatalf("unexpected log line shape: %q", raw)
		}
		msg := m[2]
		if base != "" {
			msg = strings.ReplaceAll(msg, base, "<base>")
		}
		lines = append(lines, m[1]+" "+msg)
	}
	return lines
}

func assertEntrypointLines(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("logged lines differ\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// entrypointBase returns a fresh temp directory with symlinks resolved, so the
// paths the cleanup logs and compares are the same ones the test builds.
func entrypointBase(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	return base
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, target, err)
	}
}

// assertPresence checks, with Lstat, that every path in present is still there and
// every path in absent is gone.
func assertPresence(t *testing.T, present, absent []string) {
	t.Helper()
	for _, p := range present {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s must still exist: %v", p, err)
		}
	}
	for _, p := range absent {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s must be gone, lstat err=%v", p, err)
		}
	}
}

// TestEntrypointCleanupMixedTreeCharacterization covers every per-entry branch in one
// scan: the current binary itself, a relative symlink and a symlink chain that both
// resolve to it, a real file, a dangling relative symlink, a symlink to another
// binary, a PATH entry that is a file, a directory that does not exist, PATH entries
// with whitespace, an empty entry, a duplicate spelled with a trailing slash, and a
// directory listed both in PATH and among the global ones.
func TestEntrypointCleanupMixedTreeCharacterization(t *testing.T) {
	base := entrypointBase(t)
	execDir := filepath.Join(base, "exec")
	dirA := filepath.Join(base, "a")
	dirB := filepath.Join(base, "b")
	dirD := filepath.Join(base, "d")
	dirG := filepath.Join(base, "g")
	for _, d := range []string{execDir, dirA, dirB, dirD, dirG} {
		mustMkdir(t, d)
	}
	execPath := filepath.Join(execDir, "proxsave")
	mustWriteFile(t, execPath, "current binary")
	other := filepath.Join(base, "other")
	mustWriteFile(t, other, "another binary")
	chain := filepath.Join(base, "chain")
	mustSymlink(t, execPath, chain)
	notADir := filepath.Join(base, "notadir")
	mustWriteFile(t, notADir, "a file listed in PATH")

	mustSymlink(t, filepath.Join("..", "exec", "proxsave"), filepath.Join(dirB, "proxsave"))
	mustWriteFile(t, filepath.Join(dirB, "proxmox-backup"), "#!/bin/sh\n# packaged\n")
	mustSymlink(t, chain, filepath.Join(dirA, "proxsave"))
	mustSymlink(t, "proxsave-old", filepath.Join(dirA, "proxmox-backup"))
	mustSymlink(t, other, filepath.Join(dirD, "proxsave"))
	mustSymlink(t, "/nonexistent-proxsave-target", filepath.Join(dirG, "proxmox-backup"))

	pathEnv := strings.Join([]string{" " + dirB + " ", execDir, "", dirB + "/", dirA, notADir, dirD}, string(os.PathListSeparator))
	globals := []string{dirA, dirG, filepath.Join(base, "missing")}

	got := runEntrypointCleanup(t, base, execPath, pathEnv, globals)

	assertEntrypointLines(t, got, []string{
		"INFO Scanning for existing proxsave/proxmox-backup commands on PATH before installation",
		"INFO Keeping proxsave/proxmox-backup symlink pointing to Go binary: <base>/b/proxsave -> <base>/exec/proxsave",
		"INFO Leaving <base>/b/proxmox-backup in place: not a symlink created by proxsave (a real/package-managed file is never removed)",
		"INFO Keeping proxsave/proxmox-backup entrypoint (current Go binary): <base>/exec/proxsave",
		"INFO Keeping proxsave/proxmox-backup symlink pointing to Go binary: <base>/a/proxsave -> <base>/exec/proxsave",
		"INFO Removed existing proxsave/proxmox-backup symlink: <base>/a/proxmox-backup",
		"INFO Removed existing proxsave/proxmox-backup symlink: <base>/d/proxsave",
		"INFO Removed existing proxsave/proxmox-backup symlink: <base>/g/proxmox-backup",
		"INFO Global proxsave/proxmox-backup entrypoint scan: removed=3 kept=4",
	})
	assertPresence(t,
		[]string{
			filepath.Join(dirB, "proxsave"), filepath.Join(dirB, "proxmox-backup"), execPath,
			filepath.Join(dirA, "proxsave"), other, chain, notADir,
		},
		[]string{
			filepath.Join(dirA, "proxmox-backup"), filepath.Join(dirD, "proxsave"), filepath.Join(dirG, "proxmox-backup"),
		},
	)
}

// TestEntrypointCleanupEmptyPathCharacterization: an empty PATH still scans the
// global directories, and a scan that finds nothing says so.
func TestEntrypointCleanupEmptyPathCharacterization(t *testing.T) {
	base := entrypointBase(t)
	execPath := filepath.Join(base, "proxsave-bin")
	mustWriteFile(t, execPath, "current binary")
	empty := filepath.Join(base, "empty")
	mustMkdir(t, empty)

	got := runEntrypointCleanup(t, base, execPath, "", []string{empty})

	assertEntrypointLines(t, got, []string{
		"INFO PATH is empty; scanning only the well-known global directories",
		"INFO No existing proxsave/proxmox-backup entrypoints found on PATH or in /usr/local/bin,/usr/bin",
	})
}

// TestEntrypointCleanupGlobalsOnlyCharacterization: with an empty PATH a stale
// symlink in a global directory is still removed.
func TestEntrypointCleanupGlobalsOnlyCharacterization(t *testing.T) {
	base := entrypointBase(t)
	execPath := filepath.Join(base, "proxsave-bin")
	mustWriteFile(t, execPath, "current binary")
	global := filepath.Join(base, "global")
	mustMkdir(t, global)
	stale := filepath.Join(global, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), stale)

	got := runEntrypointCleanup(t, base, execPath, "", []string{global})

	assertEntrypointLines(t, got, []string{
		"INFO PATH is empty; scanning only the well-known global directories",
		"INFO Removed existing proxsave/proxmox-backup symlink: <base>/global/proxsave",
		"INFO Global proxsave/proxmox-backup entrypoint scan: removed=1 kept=0",
	})
	assertPresence(t, nil, []string{stale})
}

// TestEntrypointCleanupRemoveFailureCharacterization: a symlink that cannot be
// removed is reported with the full path in both the message and the error, and is
// counted neither as removed nor as kept.
func TestEntrypointCleanupRemoveFailureCharacterization(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory write permission")
	}
	base := entrypointBase(t)
	execPath := filepath.Join(base, "proxsave-bin")
	mustWriteFile(t, execPath, "current binary")
	locked := filepath.Join(base, "locked")
	mustMkdir(t, locked)
	stale := filepath.Join(locked, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), stale)
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got := runEntrypointCleanup(t, base, execPath, locked, nil)

	assertEntrypointLines(t, got, []string{
		"INFO Scanning for existing proxsave/proxmox-backup commands on PATH before installation",
		"WARNING Failed to remove existing proxsave/proxmox-backup symlink <base>/locked/proxsave: remove <base>/locked/proxsave: permission denied",
		"INFO No existing proxsave/proxmox-backup entrypoints found on PATH or in /usr/local/bin,/usr/bin",
	})
	assertPresence(t, []string{stale}, nil)
}

// TestEntrypointCleanupSkipsRelativePathEntries: a relative PATH entry names a
// directory under wherever the installer was started, not a global entrypoint
// location, so it is skipped with a DEBUG line and nothing under it is touched,
// while the absolute entries of the same PATH are still cleaned.
func TestEntrypointCleanupSkipsRelativePathEntries(t *testing.T) {
	base := entrypointBase(t)
	execPath := filepath.Join(base, "proxsave-bin")
	mustWriteFile(t, execPath, "current binary")
	cwd := filepath.Join(base, "cwd")
	mustMkdir(t, filepath.Join(cwd, "bin"))
	staleDot := filepath.Join(cwd, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), staleDot)
	staleBin := filepath.Join(cwd, "bin", "proxmox-backup")
	mustSymlink(t, filepath.Join(base, "gone"), staleBin)
	abs := filepath.Join(base, "abs")
	mustMkdir(t, abs)
	staleAbs := filepath.Join(abs, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), staleAbs)
	t.Chdir(cwd)

	pathEnv := strings.Join([]string{".", " bin ", abs}, string(os.PathListSeparator))
	got := runEntrypointCleanup(t, base, execPath, pathEnv, nil)

	assertEntrypointLines(t, got, []string{
		"INFO Scanning for existing proxsave/proxmox-backup commands on PATH before installation",
		`DEBUG Skipping relative PATH entry ".": only absolute directories are scanned for proxsave/proxmox-backup entrypoints`,
		`DEBUG Skipping relative PATH entry "bin": only absolute directories are scanned for proxsave/proxmox-backup entrypoints`,
		"INFO Removed existing proxsave/proxmox-backup symlink: <base>/abs/proxsave",
		"INFO Global proxsave/proxmox-backup entrypoint scan: removed=1 kept=0",
	})
	assertPresence(t, []string{staleDot, staleBin}, []string{staleAbs})
}

// TestEntrypointCleanupEmptyPathEntryCharacterization: an empty PATH entry is skipped,
// it is NOT read as the working directory, so a stale symlink there stays.
func TestEntrypointCleanupEmptyPathEntryCharacterization(t *testing.T) {
	base := entrypointBase(t)
	execPath := filepath.Join(base, "proxsave-bin")
	mustWriteFile(t, execPath, "current binary")
	cwd := filepath.Join(base, "cwd")
	mustMkdir(t, cwd)
	stale := filepath.Join(cwd, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), stale)
	t.Chdir(cwd)

	got := runEntrypointCleanup(t, base, execPath, string(os.PathListSeparator), []string{""})

	assertEntrypointLines(t, got, []string{
		"INFO Scanning for existing proxsave/proxmox-backup commands on PATH before installation",
		"INFO No existing proxsave/proxmox-backup entrypoints found on PATH or in /usr/local/bin,/usr/bin",
	})
	assertPresence(t, []string{stale}, nil)
}

// TestEntrypointCleanupUnknownExecPathCharacterization: without a current binary the
// cleanup logs one warning and touches nothing.
func TestEntrypointCleanupUnknownExecPathCharacterization(t *testing.T) {
	base := entrypointBase(t)
	dir := filepath.Join(base, "bin")
	mustMkdir(t, dir)
	stale := filepath.Join(dir, "proxsave")
	mustSymlink(t, filepath.Join(base, "gone"), stale)

	got := runEntrypointCleanup(t, base, "  ", dir, []string{dir})

	assertEntrypointLines(t, got, []string{
		"WARNING current executable path is unknown; skipping proxsave/proxmox-backup entrypoint cleanup to avoid removing a working entrypoint that cannot be recreated",
	})
	assertPresence(t, []string{stale}, nil)
}
