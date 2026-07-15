package health

import (
	"os"
	"testing"
	"time"
)

// seedFreshHeartbeat records a transmitting heartbeat at ts (a healthy status).
func seedFreshHeartbeat(t *testing.T, base string, ts int64) {
	t.Helper()
	if err := RecordPing(base, "self", KindHeartbeat, ts, true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
}

// seedInfoFor records a DaemonInfo for base (no binary hash -- staleness is /proc-based now).
func seedInfoFor(t *testing.T, base, execPath string, pid int) {
	t.Helper()
	if err := WriteDaemonInfo(base, DaemonInfo{
		PID:      pid,
		ExecPath: execPath,
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

// TestCheckDaemonStateInfoSurfacesVersion: an identity record surfaces the running daemon's
// version/commit/start time for DISPLAY (HaveInfo), and -- with no ProcStale probe wired -- alignment
// stays UNKNOWN (the record no longer decides alignment).
func TestCheckDaemonStateInfoSurfacesVersion(t *testing.T) {
	base := t.TempDir()
	seedFreshHeartbeat(t, base, testNow.Unix())
	if err := WriteDaemonPID(base, 4321); err != nil {
		t.Fatalf("WriteDaemonPID: %v", err)
	}
	seedInfoFor(t, base, "/opt/proxsave/build/proxsave", 4321)

	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		SchedulerMode:     "daemon",
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(int) bool { return true },
		// No ProcStale: alignment is UNKNOWN.
	})

	if s.TxState() != TxTransmitting {
		t.Fatalf("TxState = %q, want %q", s.TxState(), TxTransmitting)
	}
	if !s.ProcessAlive {
		t.Fatalf("ProcessAlive should be true (pid=%d)", s.PID)
	}
	if !s.HaveInfo {
		t.Fatalf("HaveInfo should be true when a record exists")
	}
	if s.Version != "1.2.3" || s.Commit != "abcdef0" || s.StartTS != ts {
		t.Fatalf("identity fields not surfaced: v=%q c=%q ts=%d", s.Version, s.Commit, s.StartTS)
	}
	if s.AlignChecked {
		t.Fatalf("AlignChecked should be false (UNKNOWN) with no ProcStale probe")
	}
	if s.Aligned {
		t.Fatalf("Aligned should be false (UNKNOWN) with no ProcStale probe")
	}
	if s.StaleReason != "" {
		t.Fatalf("StaleReason should be empty when alignment is UNKNOWN, got %q", s.StaleReason)
	}
	if !s.HaveStatus {
		t.Fatalf("HaveStatus should be true")
	}
}

// TestCheckDaemonStateNoInfo: no info file -> HaveInfo false, Aligned false (UNKNOWN), but the
// diagnosis is still computed correctly from status + presence.
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
	if s.Aligned || s.AlignChecked {
		t.Fatalf("alignment should be UNKNOWN with no record and no ProcStale")
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

// TestCheckDaemonStateProcDead: a ProcAlive that returns false -> ProcessAlive false, even with a
// recorded pid.
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
	seedFreshHeartbeat(t, base, testNow.Unix())
	// Deliberately DO NOT call WriteDaemonPID: the .daemon.pid file is absent.
	seedInfoFor(t, base, "/opt/proxsave/build/proxsave", 4242) // info record carries pid 4242

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

// TestCheckDaemonStateProcStaleStale: a live pid + an injected ProcStale returning
// (stale=true, checked=true) reads BEHIND -> AlignChecked=true, Aligned=false, StaleReason set. No
// record is needed: /proc is the sole alignment signal.
func TestCheckDaemonStateProcStaleStale(t *testing.T) {
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
		ProcStale:         func(pid int) (bool, bool) { return true, true },
	})

	if s.HaveInfo {
		t.Fatalf("HaveInfo should stay false: alignment reflects /proc, not a record")
	}
	if !s.AlignChecked {
		t.Fatalf("AlignChecked should be true: the /proc probe returned a verdict")
	}
	if s.Aligned {
		t.Fatalf("Aligned should be false when ProcStale reports stale")
	}
	if s.StaleReason == "" {
		t.Fatalf("StaleReason should be set when the /proc probe reports stale")
	}
	// The behind render gate: AlignChecked && !Aligned suffices (no record required).
	if !(s.AlignChecked && !s.Aligned) {
		t.Fatalf("stale daemon must be behind-eligible via AlignChecked && !Aligned")
	}
}

// TestCheckDaemonStateProcStaleAligned: ProcStale returning (stale=false, checked=true) ->
// AlignChecked=true, Aligned=true, no StaleReason.
func TestCheckDaemonStateProcStaleAligned(t *testing.T) {
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
		ProcStale:         func(pid int) (bool, bool) { return false, true },
	})

	if !s.AlignChecked {
		t.Fatalf("AlignChecked should be true: the /proc probe returned a verdict")
	}
	if !s.Aligned {
		t.Fatalf("Aligned should be true when ProcStale reports not stale")
	}
	if s.StaleReason != "" {
		t.Fatalf("StaleReason should be empty when aligned, got %q", s.StaleReason)
	}
}

// TestCheckDaemonStateProcStaleUnknown: ProcStale returning checked=false leaves alignment UNKNOWN
// (AlignChecked stays false), exactly as if no probe were wired -- the daemon must NOT read as behind.
func TestCheckDaemonStateProcStaleUnknown(t *testing.T) {
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
		ProcStale:         func(pid int) (bool, bool) { return false, false },
	})

	if s.AlignChecked {
		t.Fatalf("AlignChecked should stay false when the /proc probe could not determine")
	}
	if s.Aligned {
		t.Fatalf("Aligned should stay false (UNKNOWN) when the probe could not determine")
	}
	if s.StaleReason != "" {
		t.Fatalf("StaleReason should be empty when alignment is UNKNOWN, got %q", s.StaleReason)
	}
}

// TestCheckDaemonStateProcStaleSkippedWhenNoPid: no pid to observe (pidfile + info absent) means the
// /proc probe has nothing to read and is skipped -> alignment stays UNKNOWN even with a probe set.
func TestCheckDaemonStateProcStaleSkippedWhenNoPid(t *testing.T) {
	base := t.TempDir()
	seedFreshHeartbeat(t, base, testNow.Unix())
	// No WriteDaemonPID and no WriteDaemonInfo: PID resolves to 0.

	called := false
	s := CheckDaemonState(DaemonStateInput{
		BaseDir:           base,
		HeartbeatInterval: 5 * time.Minute,
		Now:               testNow,
		Presence:          activePresence(),
		ProcAlive:         func(int) bool { return true },
		ProcStale:         func(pid int) (bool, bool) { called = true; return true, true },
	})

	if called {
		t.Fatalf("ProcStale must not be called when there is no pid to probe")
	}
	if s.AlignChecked {
		t.Fatalf("AlignChecked should stay false with no pid to probe")
	}
}

// TestCheckDaemonStateProcStaleSkippedWhenProcessDead: a pid that the liveness probe reports DEAD has
// no readable /proc/<pid>/exe, so the /proc probe is skipped -> alignment stays UNKNOWN.
func TestCheckDaemonStateProcStaleSkippedWhenProcessDead(t *testing.T) {
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
		ProcAlive:         func(int) bool { return false }, // process is dead
		ProcStale:         func(pid int) (bool, bool) { called = true; return true, true },
	})

	if called {
		t.Fatalf("ProcStale must not be called when the process is not alive")
	}
	if s.AlignChecked {
		t.Fatalf("AlignChecked should stay false when the process is dead")
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
