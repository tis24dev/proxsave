package health

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedBinary writes content to base/proxsave and returns its path (a real file so
// ComputeBinaryIdentity has something to hash).
func seedBinary(t *testing.T, base string, content []byte) string {
	t.Helper()
	path := filepath.Join(base, "proxsave")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("seed binary: %v", err)
	}
	return path
}

// seedFreshHeartbeat records a transmitting heartbeat at ts (a healthy status).
func seedFreshHeartbeat(t *testing.T, base string, ts int64) {
	t.Helper()
	if err := RecordPing(base, "self", KindHeartbeat, ts, true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
}

// seedInfoFor records a DaemonInfo whose Binary is the identity of execPath's CURRENT content.
func seedInfoFor(t *testing.T, base, execPath string, pid int) {
	t.Helper()
	id, err := ComputeBinaryIdentity(execPath)
	if err != nil {
		t.Fatalf("compute identity for info: %v", err)
	}
	if err := WriteDaemonInfo(base, DaemonInfo{
		PID:      pid,
		ExecPath: execPath,
		Binary:   id,
		Version:  "1.2.3",
		Commit:   "abcdef0",
		StartTS:  ts,
	}); err != nil {
		t.Fatalf("seed info: %v", err)
	}
}

// ts is a fixed start timestamp for the seeded info (deterministic).
const ts int64 = 1700000000

var testNow = time.Unix(1700000300, 0) // 5 minutes after ts: a fresh heartbeat recorded at testNow

func activePresence() DaemonPresence {
	return DaemonPresence{Probed: true, Installed: true, Active: true}
}

// TestCheckDaemonStateAligned (case a): fresh heartbeat + active presence + info matching the
// on-disk binary -> healthy diagnosis, process alive, aligned, HaveInfo.
func TestCheckDaemonStateAligned(t *testing.T) {
	base := t.TempDir()
	bin := seedBinary(t, base, []byte("proxsave-v1"))
	seedFreshHeartbeat(t, base, testNow.Unix())
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	seedInfoFor(t, base, bin, 4321)

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		SchedulerMode:     "daemon",
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(int) bool { return true },
	})

	if s.TxState() != TxTransmitting {
		t.Fatalf("TxState = %q, want %q", s.TxState(), TxTransmitting)
	}
	if !s.Diagnosis.DaemonUp {
		t.Fatalf("DaemonUp should be true")
	}
	if !s.ProcessAlive {
		t.Fatalf("ProcessAlive should be true (pid=%d)", s.PID)
	}
	if !s.HaveInfo {
		t.Fatalf("HaveInfo should be true")
	}
	if !s.Aligned {
		t.Fatalf("Aligned should be true; StaleReason=%q", s.StaleReason)
	}
	if s.StaleReason != "" {
		t.Fatalf("StaleReason should be empty when aligned, got %q", s.StaleReason)
	}
	if !s.HaveStatus {
		t.Fatalf("HaveStatus should be true")
	}
	if s.Version != "1.2.3" || s.Commit != "abcdef0" || s.StartTS != ts {
		t.Fatalf("identity fields not surfaced: v=%q c=%q ts=%d", s.Version, s.Commit, s.StartTS)
	}
}

// TestCheckDaemonStateStale (case b): the info records a DIFFERENT hash than the current on-disk
// file (simulating an in-place upgrade) -> Aligned false with StaleReason set.
func TestCheckDaemonStateStale(t *testing.T) {
	base := t.TempDir()
	bin := seedBinary(t, base, []byte("proxsave-v1"))
	seedFreshHeartbeat(t, base, testNow.Unix())
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	seedInfoFor(t, base, bin, 4321) // records identity of v1
	// The on-disk binary is replaced (upgrade) while the recorded identity still points at v1.
	if err := os.WriteFile(bin, []byte("proxsave-v2-different"), 0o600); err != nil {
		t.Fatalf("rewrite binary: %v", err)
	}

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(int) bool { return true },
	})

	if !s.HaveInfo {
		t.Fatalf("HaveInfo should be true")
	}
	if s.Aligned {
		t.Fatalf("Aligned should be false after the on-disk binary changed")
	}
	if s.StaleReason == "" {
		t.Fatalf("StaleReason should be set when not aligned")
	}
}

// TestCheckDaemonStateNoInfo (case c): no info file -> HaveInfo false, Aligned false (UNKNOWN), but
// the diagnosis is still computed correctly from status + presence.
func TestCheckDaemonStateNoInfo(t *testing.T) {
	base := t.TempDir()
	seedFreshHeartbeat(t, base, testNow.Unix())
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(int) bool { return true },
	})

	if s.HaveInfo {
		t.Fatalf("HaveInfo should be false with no info file")
	}
	if s.Aligned {
		t.Fatalf("Aligned should be false (UNKNOWN) with no info record")
	}
	if s.StaleReason != "" {
		t.Fatalf("StaleReason should be empty when alignment is UNKNOWN, got %q", s.StaleReason)
	}
	if s.TxState() != TxTransmitting {
		t.Fatalf("diagnosis should still be correct: TxState=%q, want %q", s.TxState(), TxTransmitting)
	}
	if !s.ProcessAlive {
		t.Fatalf("ProcessAlive should still reflect the probe")
	}
}

// TestCheckDaemonStateProcDead (case d): a ProcAlive that returns false -> ProcessAlive false, even
// with a recorded pid.
func TestCheckDaemonStateProcDead(t *testing.T) {
	base := t.TempDir()
	seedFreshHeartbeat(t, base, testNow.Unix())
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}

	called := false
	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(pid int) bool { called = true; return false },
	})

	if !called {
		t.Fatalf("injected ProcAlive should have been called for pid>0")
	}
	if s.ProcessAlive {
		t.Fatalf("ProcessAlive should be false when the probe returns false")
	}
	if s.PID != 4321 {
		t.Fatalf("PID = %d, want 4321", s.PID)
	}
}

// TestCheckDaemonStatePidFromInfoWhenPidfileAbsent (regression): the pidfile is ABSENT (best-effort
// WriteDaemonPID hiccuped at startup) but a DaemonInfo record IS present carrying a LIVE pid. The
// effective pid must fall back to info.PID and be PROBED -> a live daemon must NOT read as dead.
func TestCheckDaemonStatePidFromInfoWhenPidfileAbsent(t *testing.T) {
	base := t.TempDir()
	bin := seedBinary(t, base, []byte("proxsave-v1"))
	seedFreshHeartbeat(t, base, testNow.Unix())
	// Deliberately DO NOT call WriteDaemonPID: the .daemon.pid file is absent.
	seedInfoFor(t, base, bin, 4242) // info record carries pid 4242

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(pid int) bool { return pid == 4242 },
	})

	if s.PID != 4242 {
		t.Fatalf("PID should fall back to info.PID (4242) when pidfile absent, got %d", s.PID)
	}
	if !s.ProcessAlive {
		t.Fatalf("ProcessAlive should be true: the effective pid (info.PID=%d) is probed alive", s.PID)
	}
}

// TestCheckDaemonStatePidfileAndInfoAbsent (paired regression): pidfile absent AND info absent -> no
// pid to probe, so PID==0 and ProcessAlive==false (the probe short-circuits on pid 0).
func TestCheckDaemonStatePidfileAndInfoAbsent(t *testing.T) {
	base := t.TempDir()
	seedFreshHeartbeat(t, base, testNow.Unix())
	// No WriteDaemonPID and no WriteDaemonInfo: both records are absent.

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(pid int) bool { return pid == 4242 },
	})

	if s.PID != 0 {
		t.Fatalf("PID should be 0 when both pidfile and info are absent, got %d", s.PID)
	}
	if s.ProcessAlive {
		t.Fatalf("ProcessAlive should be false when there is no pid to probe")
	}
}

// TestPidAliveSelf: the leaf pidAlive default reports the test process (our own pid) alive, and a
// non-positive pid dead.
func TestPidAliveSelf(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatalf("pidAlive(self) should be true")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Fatalf("pidAlive(<=0) should be false")
	}
}
