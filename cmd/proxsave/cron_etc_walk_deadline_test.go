package main

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// stallEtcWalk swaps BOTH filesystem halves of the /etc walk for ones that never answer on
// the paths stallOn picks, and shrinks cronProbeTimeout so a timeout costs the suite
// milliseconds. It returns the number of steps that actually entered a stalling path, which
// is what tells one deadline for the walk from one per habitat.
//
// A seam is not a convenience here, it is the only way in. The walk's blocking calls are a
// stat, a symlink resolution and a read, and none of the three can be made to block on a
// temp tree: the one object an unprivileged test can build that blocks on open(2) is a
// writer-less fifo, and strace of the real walk shows a fifo entry stat'ed exactly once as
// S_IFIFO and present in no openat at all, because the IsRegular gate runs first.
func stallEtcWalk(t *testing.T, stallOn func(string) bool) *int32 {
	t.Helper()
	var stalls int32
	release := make(chan struct{})
	// Registered BEFORE the seam swaps, so it runs after them: an abandoned step reads its
	// own local copy of the seam and never the package var, but releasing last keeps the
	// two orders from ever mattering.
	t.Cleanup(func() { close(release) })

	origStat, origTimeout := cronEntryStatFn, cronProbeTimeout
	t.Cleanup(func() { cronEntryStatFn, cronProbeTimeout = origStat, origTimeout })
	cronProbeTimeout = 100 * time.Millisecond
	cronEntryStatFn = func(path string) (os.FileInfo, error) {
		if !stallOn(path) {
			return origStat(path)
		}
		atomic.AddInt32(&stalls, 1)
		<-release
		return nil, os.ErrNotExist
	}
	return &stalls
}

// walkWithin runs the /etc walk on its own goroutine and reports how long it took, failing
// the test rather than hanging the suite when nothing bounds it. The guard is deliberately
// far above cronProbeTimeout: this is the difference between "bounded" and "not bounded",
// not a latency assertion.
func walkWithin(t *testing.T, mode systemCronScan, what string) ([]indirectCronRef, time.Duration) {
	t.Helper()
	type result struct {
		refs    []indirectCronRef
		elapsed time.Duration
	}
	done := make(chan result, 1)
	started := time.Now()
	go func() {
		refs := systemCronRefs(mode)
		done <- result{refs, time.Since(started)}
	}()
	select {
	case got := <-done:
		return got.refs, got.elapsed
	case <-time.After(5 * time.Second):
		t.Fatalf("systemCronRefs did not return within 5s %s: nothing bounds the wall clock the /etc walk may spend in the filesystem, so an entry symlinked into a dead mount hangs runUpgrade, runInstall, upgradeFinalizePhase and runInstallTUI before any of them prints anything", what)
		return nil, 0
	}
}

func etcHabitat(t *testing.T, paths ...string) {
	t.Helper()
	orig := systemCronPaths
	t.Cleanup(func() { systemCronPaths = orig })
	systemCronPaths = paths
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

// A cron.d entry symlinked into a mount that stopped answering is stat'ed, resolved and
// read by systemCronFileRefs before any deadline in this file exists. That is the hole
// maxCronWrapperProbeBytes documented and this closes.
func TestTheEtcWalkGivesUpOnACronFileThatNeverAnswers(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	if err := os.Mkdir(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := writeFile(t, filepath.Join(cronD, "nasbackup"), "0 2 * * * root /usr/local/bin/nas-guard\n", 0o644)
	etcHabitat(t, cronD)
	stallEtcWalk(t, func(path string) bool { return path == dead })

	_, elapsed := walkWithin(t, scanAll, "on a cron.d entry that never answers")
	if elapsed > time.Second {
		t.Errorf("the /etc walk took %s, want at most 1s: the stat, resolve and read of a cron.d entry must answer to cronProbeTimeout (%s)", elapsed, cronProbeTimeout)
	}
}

// The run-parts habitats reach the same mount by a different route: the entry stat there
// FOLLOWS the symlink on purpose, so it is the first call of that walk to enter the mount.
func TestARunPartsEntryThatNeverAnswersItsStatDoesNotHangTheWalk(t *testing.T) {
	dir := t.TempDir()
	daily := filepath.Join(dir, "cron.daily")
	if err := os.Mkdir(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	dead := writeFile(t, filepath.Join(daily, "nasguard"), "#!/bin/sh\ntrue\n", 0o755)
	etcHabitat(t, daily)
	stallEtcWalk(t, func(path string) bool { return path == dead })

	_, elapsed := walkWithin(t, scanAll, "on a run-parts entry whose stat never answers")
	if elapsed > time.Second {
		t.Errorf("the /etc walk took %s, want at most 1s: the stat of a run-parts entry follows its symlink, so it must answer to cronProbeTimeout (%s)", elapsed, cronProbeTimeout)
	}
}

// One dead mount is one fact about the host, not one fact per habitat. Without a latch
// shared by the whole walk, /etc/cron.d holds as many entries as an operator puts there and
// each of them costs a full timeout, which is a count and not a bound.
func TestOneStalledEntryCostsTheWholeEtcWalkOneTimeoutNotOnePerHabitat(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	daily := filepath.Join(dir, "cron.daily")
	weekly := filepath.Join(dir, "cron.weekly")
	for _, d := range []string{cronD, daily, weekly} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The FILE habitat is first and is not decoration. /etc/crontab is walked by a call
	// site of its own, and handing that one site a fresh deadline instead of the walk's is
	// a one-line change that leaves every directory-only test green.
	crontab := filepath.Join(dir, "crontab")
	writeFile(t, crontab, "0 1 * * * root /usr/local/bin/nas-guard\n", 0o644)
	writeFile(t, filepath.Join(cronD, "aaa"), "0 2 * * * root /usr/local/bin/nas-guard\n", 0o644)
	writeFile(t, filepath.Join(cronD, "bbb"), "0 3 * * * root /usr/local/bin/nas-guard\n", 0o644)
	writeFile(t, filepath.Join(daily, "ccc"), "#!/bin/sh\ntrue\n", 0o755)
	writeFile(t, filepath.Join(weekly, "ddd"), "#!/bin/sh\ntrue\n", 0o755)
	etcHabitat(t, crontab, cronD, daily, weekly)
	stalls := stallEtcWalk(t, func(string) bool { return true })

	_, elapsed := walkWithin(t, scanAll, "with five stalled entries across four habitats")
	if got := atomic.LoadInt32(stalls); got != 1 {
		t.Errorf("the /etc walk stalled on %d entries, want exactly 1: the deadline must latch across every habitat of one walk, or one dead mount costs one timeout per entry", got)
	}
	if elapsed > 3*cronProbeTimeout {
		t.Errorf("the /etc walk took %s, want at most %s", elapsed, 3*cronProbeTimeout)
	}
}

// The walk's own filesystem and the CONTENT probes it launches are two different questions
// about two different sets of paths, so they must not share a latch. A wrapper on a dead
// mount named by a cron line says nothing about whether /etc still answers, and if it
// silenced the rest of the walk it would take a literal `proxsave --backup` line with it.
//
// This one passes before the bound exists: it is the regression it must not cause.
func TestAStalledScriptProbeStillLetsTheWalkReadTheHabitatsAfterIt(t *testing.T) {
	dir := t.TempDir()
	daily := filepath.Join(dir, "cron.daily")
	cronD := filepath.Join(dir, "cron.d")
	for _, d := range []string{daily, cronD} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(daily, "wrapper"), "#!/bin/sh\n/mnt/dead-nas/guard.sh\n", 0o755)
	writeFile(t, filepath.Join(cronD, "proxsave"), "0 2 * * * root /usr/local/bin/proxsave --backup\n", 0o644)
	etcHabitat(t, daily, cronD)
	// Only the CONTENT probe stalls. The walk's own steps answer normally.
	stallingScan(t, func(string) bool { return true })

	refs, _ := walkWithin(t, scanAll, "with a stalled content probe")
	found := false
	for _, ref := range refs {
		if ref.Command == "/usr/local/bin/proxsave" {
			found = true
		}
	}
	if !found {
		t.Errorf("systemCronRefs after a stalled script probe = %+v, want the direct /etc/cron.d/proxsave line among them: a command that stopped answering must not stop /etc being read", refs)
	}
}

// The worker a timeout walks away from is ABANDONED, not cancelled, so it may write long
// after the caller answered. This test makes it do exactly that and runs under -race.
//
// The stall is a SLEEP and not the release channel stallEtcWalk uses, and that is
// load-bearing: closing a channel in t.Cleanup after the caller has read its answer puts a
// happens-before edge between the worker's write and the caller's read, and the detector
// then correctly reports nothing even on code that races. Measured both ways.
func TestTheAbandonedEtcWalkStepNeverWritesWhatTheCallerRead(t *testing.T) {
	dir := t.TempDir()
	daily := filepath.Join(dir, "cron.daily")
	if err := os.Mkdir(daily, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := writeFile(t, filepath.Join(daily, "nasguard"), "#!/bin/sh\ntrue\n", 0o755)
	etcHabitat(t, daily)

	origStat, origTimeout := cronEntryStatFn, cronProbeTimeout
	answered := make(chan struct{})
	t.Cleanup(func() {
		<-answered // the worker must be collected before the seam is restored
		cronEntryStatFn, cronProbeTimeout = origStat, origTimeout
	})
	cronProbeTimeout = 50 * time.Millisecond
	cronEntryStatFn = func(path string) (os.FileInfo, error) {
		if path != entry {
			return origStat(path)
		}
		defer close(answered)
		time.Sleep(400 * time.Millisecond)
		return origStat(path)
	}

	_, elapsed := walkWithin(t, scanAll, "on an entry whose stat answers only after the deadline")
	// Well under the seam's 400ms sleep on purpose: the caller must have walked away
	// BEFORE the worker answered, or there is no abandoned writer for -race to judge.
	if elapsed > 200*time.Millisecond {
		t.Errorf("the /etc walk took %s, want at most 200ms: the caller waited for the abandoned step instead of giving up on it, so nothing here can prove what that step is allowed to write", elapsed)
	}
}

// fakeRegularInfo answers "an ordinary 40-byte file" for anything. It exists because the
// only object an unprivileged test can build that really blocks on open(2) is a writer-less
// fifo, and the walk's own IsRegular gate rejects a fifo on the stat before any open - so
// reaching the read means lying about the stat and nothing else.
type fakeRegularInfo struct{ name string }

func (f fakeRegularInfo) Name() string       { return f.name }
func (f fakeRegularInfo) Size() int64        { return 40 }
func (f fakeRegularInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeRegularInfo) ModTime() time.Time { return time.Time{} }
func (f fakeRegularInfo) IsDir() bool        { return false }
func (f fakeRegularInfo) Sys() any           { return nil }

// The stat is not the call a network mount really wedges: a stat can be answered out of an
// attribute cache while the open behind it still has to reach the server. This is the one
// test in the file whose hang is a REAL kernel wait rather than a stub, and it is what pins
// the read inside the same bounded step as the stat.
func TestTheEtcWalkGivesUpWhenTheREADOfACronFileStalls(t *testing.T) {
	dir := t.TempDir()
	cronD := filepath.Join(dir, "cron.d")
	if err := os.Mkdir(cronD, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(cronD, "nasbackup")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skipf("cannot create a fifo here: %v", err)
	}
	etcHabitat(t, cronD)
	// The abandoned step is parked in open(2) on a fifo nobody will ever write, a wait the
	// kernel does not end and t.TempDir's RemoveAll does not either, so without this the
	// test leaks a goroutine, an OS thread and the parent-directory fd for the life of the
	// test binary. Opening the write end releases it. Registered before the seam swap so it
	// runs after it.
	t.Cleanup(func() {
		if w, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = w.Close()
		}
	})

	origStat, origTimeout := cronEntryStatFn, cronProbeTimeout
	t.Cleanup(func() { cronEntryStatFn, cronProbeTimeout = origStat, origTimeout })
	cronProbeTimeout = 100 * time.Millisecond
	// The ONLY lie is the stat. Everything after it is the real code on a real object that
	// really blocks: open(2) on a fifo with no writer waits for one forever, and the /etc
	// read carries no O_NONBLOCK (measured with strace: the confined open of a cron.d entry
	// is O_RDONLY|O_NOFOLLOW|O_CLOEXEC, while the script probe's is O_RDONLY|O_NONBLOCK).
	cronEntryStatFn = func(path string) (os.FileInfo, error) {
		if path == fifo {
			return fakeRegularInfo{name: filepath.Base(path)}, nil
		}
		return origStat(path)
	}

	_, elapsed := walkWithin(t, scanAll, "when the READ of a cron.d entry blocks in the kernel")
	if elapsed > time.Second {
		t.Errorf("the /etc walk took %s, want at most 1s: only the stat is bounded, so a mount that answers a stat out of its attribute cache and never answers the open still hangs the upgrade", elapsed)
	}
}
