// daemon_state.go composes the existing health primitives into ONE reusable "daemon state" verdict:
// is the daemon installed / active / process-alive, what is its transmission diagnosis, and -- the
// new question -- is the running daemon ALIGNED with the binary now on disk, or is it behind (an
// in-place upgrade replaced the file without a restart). It reuses only existing calls (LoadStatus,
// Diagnose, RefineWithPresence, ReadDaemonPID, ReadDaemonInfo, ComputeBinaryIdentity); the two
// side-effecting seams -- systemd presence and process-liveness -- are INJECTED so this package
// never shells out to systemctl and stays a logging-free leaf. Later stages render this state into
// the dashboard and drive upgrade/install restart-verify; this file only computes it.

package health

import (
	"os"
	"syscall"
	"time"
)

// DaemonState is the composed verdict for the resident daemon. Aligned answers "is the running
// daemon's binary the same as the file on disk?" -- but a comparison is only MEANINGFUL when
// AlignChecked is true, i.e. a record was found AND its recorded binary hash was non-empty AND the
// on-disk binary re-hashed successfully. Alignment is UNKNOWN (AlignChecked=false, Aligned=false) when
// there is no record, when the record carries an empty binary hash (the daemon's startup hash failed),
// or when the on-disk binary could not be re-read at check time. Callers must gate any "behind"
// verdict on AlignChecked so an UNKNOWN alignment never renders as behind. StaleReason carries the
// human phrasing: a real mismatch when AlignChecked, or the read failure (diagnostic only) when the
// on-disk binary is unreadable.
type DaemonState struct {
	SchedulerMode string
	Probed        bool
	Installed     bool
	Active        bool
	ProcessAlive  bool
	PID           int
	Diagnosis     Diagnosis
	RawStatus     Status
	HaveStatus    bool
	HaveInfo      bool
	Aligned       bool
	AlignChecked  bool
	StaleReason   string
	Version       string
	Commit        string
	StartTS       int64
}

// TxState surfaces the underlying transmission state so callers can switch on it without reaching
// through Diagnosis.
func (s DaemonState) TxState() TxState { return s.Diagnosis.State }

// DaemonStateInput is the injected context for CheckDaemonState. Presence is the systemd-level
// existence probed OUTSIDE this package; ProcAlive is the process-liveness probe (nil falls back to
// the local pidAlive). Now + HeartbeatInterval feed Diagnose deterministically (this package never
// reads the clock).
type DaemonStateInput struct {
	BaseDir           string
	SchedulerMode     string
	HeartbeatInterval time.Duration
	Now               time.Time
	Presence          DaemonPresence
	ProcAlive         func(pid int) bool
}

// CheckDaemonState composes the daemon-state verdict from existing primitives only. The steps are
// load-bearing in order: (1) load the persisted status; (2) diagnose it and refine with systemd
// presence; (3) resolve the pid and probe liveness; (4) load the identity record and, if present with
// a recorded hash, re-hash the on-disk binary to decide alignment. Alignment is a real comparison only
// when AlignChecked is set; with no record, an empty recorded hash, or an unreadable on-disk binary it
// stays UNKNOWN (Aligned=false) and callers gate the "behind" verdict on AlignChecked.
func CheckDaemonState(in DaemonStateInput) DaemonState {
	s := DaemonState{SchedulerMode: in.SchedulerMode}

	// 1. Persisted transmission status. A missing/empty file yields the zero Status with a nil
	// error (LoadStatus is tolerant); HaveStatus is true only when the load succeeded AND the file
	// carried real content.
	st, err := LoadStatus(in.BaseDir)
	s.RawStatus = st
	s.HaveStatus = err == nil && statusHasContent(st)

	// 2. Diagnose + refine with authoritative systemd presence.
	base := Diagnose(st, in.HeartbeatInterval, in.Now)
	d := RefineWithPresence(base, in.Presence)
	s.Diagnosis = d
	s.Probed = in.Presence.Probed
	s.Installed = in.Presence.Installed
	s.Active = in.Presence.Active

	// 3. Recorded pid + liveness. The cmd layer injects a stricter probe (a /proc/cmdline gate);
	// the leaf default is a bare signal-0 liveness check. The identity record is read FIRST so the
	// EFFECTIVE pid resolves to the pidfile pid, else the recorded info.PID -- otherwise a live
	// daemon whose best-effort pidfile hiccuped (pid==0) while its info record carries a live pid
	// would probe pid 0 and read as dead.
	pid, _ := ReadDaemonPID(in.BaseDir)
	info, haveInfo, _ := ReadDaemonInfo(in.BaseDir)
	s.PID = pid
	if s.PID == 0 && haveInfo {
		s.PID = info.PID
	}
	probe := in.ProcAlive
	if probe == nil {
		probe = pidAlive
	}
	s.ProcessAlive = s.PID > 0 && probe(s.PID)

	// 4. Identity record + alignment. Alignment is a real comparison ONLY when AlignChecked is set:
	// a record exists, its recorded hash is non-empty, AND the on-disk binary re-hashed. Every other
	// path is UNKNOWN (AlignChecked=false, Aligned=false) and must NOT read as "behind":
	//   - no record                          -> UNKNOWN.
	//   - record with an empty recorded hash  -> UNKNOWN (the daemon's startup hash failed), no reason.
	//   - record but the on-disk binary errs  -> UNKNOWN, StaleReason keeps the read failure for
	//                                             diagnostics (the binary is gone/unreadable, not stale).
	//   - record + hash both present          -> AlignChecked, Aligned reflects the real comparison,
	//                                             StaleReason set only on a genuine mismatch.
	s.HaveInfo = haveInfo
	if haveInfo {
		s.Version = info.Version
		s.Commit = info.Commit
		s.StartTS = info.StartTS
		switch {
		case info.Binary.SHA256 == "":
			// Recorded identity unavailable: alignment is UNKNOWN, not behind.
		default:
			cur, cerr := ComputeBinaryIdentity(info.ExecPath)
			if cerr != nil {
				s.StaleReason = "cannot read on-disk binary: " + cerr.Error()
			} else {
				s.AlignChecked = true
				s.Aligned = cur.Aligned(info.Binary)
				if !s.Aligned {
					s.StaleReason = "on-disk binary differs from the running daemon's binary"
				}
			}
		}
	}

	return s
}

// statusHasContent reports whether a loaded Status carries any real record (mode alone is not
// content). It lets CheckDaemonState treat a fresh/empty file as HaveStatus=false, matching how a
// missing file reads.
func statusHasContent(st Status) bool {
	return len(st.Records) > 0 || st.Update != nil
}

// pidAlive is the leaf process-liveness probe: pid > 0 AND signal 0 succeeds (nil => alive; ESRCH =>
// gone; EPERM => alive but not ours -> not signallable). It mirrors probeProxsaveDaemonAlive in
// cmd/proxsave/backup_healthcheck.go WITHOUT the /proc/cmdline identity gate -- the cmd layer
// injects that stricter probe via DaemonStateInput.ProcAlive when it needs the safety gate.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
