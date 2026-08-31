package main

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// probeVerdict carries the probe's pair of answers off the goroutine the test runs it
// on, so the test can put a deadline on a call whose whole point is that it must not
// hang. A direct call cannot be timed out from the test that made it.
type probeVerdict struct {
	references, readable bool
}

// A cron command that is a fifo is not exotic: an operator who redirects a job through
// a named pipe leaves one in command position, and open(2) on a writer-less fifo waits
// for a writer forever. The probe is reached from detectIndirectProxsaveCron, which
// runUpgrade, runInstall, upgradeFinalizePhase and runInstallTUI all wait on, so that
// wait is not a slow probe: it is an upgrade that never returns and never prints.
func TestTheWrapperProbeReturnsOnAFifoInsteadOfWaitingForAWriter(t *testing.T) {
	// The deadline must not be what saves this test. Push it far past the guard below so
	// the only thing that can make the probe answer in time is the O_NONBLOCK on the open.
	// Without this the two fifo tests pass on a tree with the flag removed.
	origTimeout := cronProbeTimeout
	t.Cleanup(func() { cronProbeTimeout = origTimeout })
	cronProbeTimeout = time.Minute

	fifo := filepath.Join(t.TempDir(), "proxsave-nas-guard")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan probeVerdict, 1)
	go func() {
		references, readable := scriptProxsaveProbe(newCronProbeDeadline(), fifo)
		done <- probeVerdict{references: references, readable: readable}
	}()

	select {
	case got := <-done:
		// A fifo carries no script. "readable" must stay false or rule 4, which fires
		// only when nothing could be read, would be skipped for it.
		if got.references || got.readable {
			t.Errorf("scriptProxsaveProbe(%s) = (%v, %v), want (false, false)", fifo, got.references, got.readable)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("scriptProxsaveProbe(%s) did not return within 5s: the open has no O_NONBLOCK, so a fifo on a cron line blocks the probe until a writer appears", fifo)
	}
}

// The end-to-end shape of the same defect, through the detector the upgrade calls.
func TestAProxsaveNamedFifoOnACronLineIsReportedInsteadOfHangingTheUpgrade(t *testing.T) {
	// The deadline must not be what saves this test. Push it far past the guard below so
	// the only thing that can make the probe answer in time is the O_NONBLOCK on the open.
	// Without this the two fifo tests pass on a tree with the flag removed.
	origTimeout := cronProbeTimeout
	t.Cleanup(func() { cronProbeTimeout = origTimeout })
	cronProbeTimeout = time.Minute

	fifo := filepath.Join(t.TempDir(), "proxsave-nas-guard")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	line := "0 2 * * * " + fifo

	done := make(chan []indirectCronRef, 1)
	go func() { done <- indirectProxsaveCronRefs([]string{line}, cronProbeReadScripts) }()

	select {
	case refs := <-done:
		want := fmt.Sprintf("command %q is named after proxsave and could not be read", "proxsave-nas-guard")
		if len(refs) != 1 || refs[0].Reason != want {
			t.Errorf("indirectProxsaveCronRefs(%q) = %+v, want exactly one finding with reason %q", line, refs, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("indirectProxsaveCronRefs did not return within 5s for %q: the probe's open has no O_NONBLOCK, so an operator fifo hangs the detector runUpgrade, runInstall and runInstallTUI all wait on", line)
	}
}

// stallingScan replaces the filesystem half of the probe with one that never answers for
// the paths the caller nominates, and counts how many times it was entered. Released
// through a channel rather than left blocked, so the package's goroutines do not
// accumulate across the run.
func stallingScan(t *testing.T, stallOn func(string) bool) *int32 {
	t.Helper()
	var calls int32
	release := make(chan struct{})
	// Registered BEFORE the seam swap, so it runs after it: the abandoned scan reads its
	// own local copy of the seam and never the package var, but releasing last keeps the
	// two orders from ever mattering.
	t.Cleanup(func() { close(release) })

	origScan, origTimeout := cronProbeScanFn, cronProbeTimeout
	t.Cleanup(func() { cronProbeScanFn, cronProbeTimeout = origScan, origTimeout })
	cronProbeTimeout = 100 * time.Millisecond
	cronProbeScanFn = func(path string) (bool, bool) {
		atomic.AddInt32(&calls, 1)
		if !stallOn(path) {
			return false, true
		}
		<-release
		return false, false
	}
	return &calls
}

// O_NONBLOCK answers for a fifo or a device node. It cannot answer for a file on a mount
// that stopped replying: that file is a regular file to the VFS, and two syscalls run
// before the flagged open anyway. Something has to bound the wall clock.
func TestTheWrapperProbeGivesUpOnAPathThatNeverAnswers(t *testing.T) {
	stallingScan(t, func(string) bool { return true })

	done := make(chan probeVerdict, 1)
	started := time.Now()
	go func() {
		references, readable := scriptProxsaveProbe(newCronProbeDeadline(), "/mnt/dead-nas/proxsave-wrapper.sh")
		done <- probeVerdict{references: references, readable: readable}
	}()

	select {
	case got := <-done:
		if got.references || got.readable {
			t.Errorf("scriptProxsaveProbe on a path that never answers = (%v, %v), want (false, false): a probe that ran out of time knows nothing, and saying it read the file is evidence of absence from a file nobody read", got.references, got.readable)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("the probe took %s to give up, want at most 1s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("scriptProxsaveProbe did not return within 5s on a path that never answers: nothing bounds the wall clock the content probe may spend in the filesystem, so a dead mount hangs runUpgrade and runInstall")
	}
}

// The deadline has to latch, or it is not a bound. maxCronWrapperProbesPerLine allows
// eight opens on ONE line, and a crontab has many lines.
func TestAStalledCommandCostsOneTimeoutForTheWholeCrontabNotOnePerProbe(t *testing.T) {
	calls := stallingScan(t, func(string) bool { return true })
	line := "0 2 * * * /usr/bin/flock -n /var/lock/nas /opt/wrap/nas-guard.sh"

	done := make(chan []indirectCronRef, 1)
	started := time.Now()
	go func() { done <- indirectProxsaveCronRefs([]string{line}, cronProbeReadScripts) }()

	select {
	case refs := <-done:
		if got := atomic.LoadInt32(calls); got != 1 {
			t.Errorf("the content probe entered the filesystem %d time(s), want 1: once one probe has run out of time every later probe of the crontab must be skipped without a syscall", got)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Errorf("the line cost %s, want at most 1s", elapsed)
		}
		if len(refs) != 0 {
			t.Errorf("indirectProxsaveCronRefs(%q) = %+v, want no findings: nothing on this line is named after proxsave", line, refs)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("indirectProxsaveCronRefs did not return within 5s for a stalled runner line")
	}
}

// What the latch ANSWERS matters as much as that it latches. Every line after a stall is
// unread, and unread is exactly the state rule 4 exists for: a command named after
// proxsave that nobody could read is reported, because the name is the last thing
// standing between the operator and #298. Answering "readable" instead would clear those
// lines on the strength of a file nobody opened.
func TestAfterAStallEveryLaterProxsaveNamedCommandIsStillReported(t *testing.T) {
	stallingScan(t, func(path string) bool { return path == "/mnt/dead-nas/wrapper.sh" })
	lines := []string{
		"0 2 * * * /mnt/dead-nas/wrapper.sh",
		"0 3 * * * /opt/ops/proxsave-nas-guard",
	}

	done := make(chan []indirectCronRef, 1)
	go func() { done <- indirectProxsaveCronRefs(lines, cronProbeReadScripts) }()

	select {
	case refs := <-done:
		want := fmt.Sprintf("command %q is named after proxsave and could not be read", "proxsave-nas-guard")
		if len(refs) != 1 || refs[0].Reason != want {
			t.Errorf("indirectProxsaveCronRefs after a stall = %+v, want exactly one finding with reason %q", refs, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("indirectProxsaveCronRefs did not return within 5s after a stalled line")
	}
}
