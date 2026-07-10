// daemon_state.go composes the existing health primitives into ONE reusable "daemon state" verdict:
// is the daemon installed / active / process-alive, what is its transmission diagnosis, and -- the
// new question -- is the running daemon ALIGNED with the binary now on disk, or is it behind (an
// in-place upgrade replaced the file without a restart). It reuses only existing calls (LoadStatus,
// Diagnose, RefineWithPresence, ReadDaemonPID, ReadDaemonInfo); the side-effecting seams -- systemd
// presence, process-liveness, and the /proc staleness probe -- are INJECTED so this package never
// shells out to systemctl nor reads /proc itself and stays a logging-free leaf. Later stages render
// this state into the dashboard and drive upgrade/install restart-verify; this file only computes it.

package health

import (
	"os"
	"syscall"
	"time"
)

// DaemonState is the composed verdict for the resident daemon. Aligned answers "was the running
// daemon's binary replaced on disk?" -- but the answer is only MEANINGFUL when AlignChecked is true,
// i.e. the injected /proc staleness probe returned a definitive verdict for a live, pid-bearing
// process. Alignment is UNKNOWN (AlignChecked=false, Aligned=false) when the process is not alive,
// when no ProcStale probe is wired, or when the probe could not read /proc/<pid>/exe. Callers must
// gate any "behind" verdict on AlignChecked so an UNKNOWN alignment never renders as behind.
// StaleReason carries the human phrasing when a genuine "behind" is detected. Version/Commit/StartTS
// come from the identity record (HaveInfo) and are for DISPLAY and the restart-verify freshness gate
// only -- the record no longer participates in the alignment decision.
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
// reads the clock). ProcStale is the RECORD-INDEPENDENT, HASH-FREE staleness probe (via
// /proc/<pid>/exe, implemented in the cmd layer so this leaf never reads /proc itself): it is the
// SOLE source of the alignment verdict, returning (stale, checked) where checked=false means "could
// not determine" (alignment stays UNKNOWN). A nil ProcStale leaves alignment UNKNOWN.
type DaemonStateInput struct {
	BaseDir           string
	SchedulerMode     string
	HeartbeatInterval time.Duration
	Now               time.Time
	Presence          DaemonPresence
	ProcAlive         func(pid int) bool
	ProcStale         func(pid int) (stale bool, checked bool)
}

// CheckDaemonState composes the daemon-state verdict from existing primitives only. The steps are
// load-bearing in order: (1) load the persisted status; (2) diagnose it and refine with systemd
// presence; (3) resolve the pid and probe liveness; (4) load the identity record for the daemon's
// version/commit/start time (DISPLAY + freshness only); (5) decide alignment SOLELY from the injected
// ProcStale probe (/proc/<pid>/exe). Alignment is known only when AlignChecked is set (the probe
// returned a verdict for a live process); otherwise it stays UNKNOWN (Aligned=false) and callers gate
// the "behind" verdict on AlignChecked.
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

	// 4. Identity record: surface the running daemon's version/commit/start time for DISPLAY and the
	// restart-verify freshness gate. HaveInfo now means only "a record exists"; the record no longer
	// participates in the alignment decision (that is step 5, /proc alone).
	s.HaveInfo = haveInfo
	if haveInfo {
		s.Version = info.Version
		s.Commit = info.Commit
		s.StartTS = info.StartTS
	}

	// 5. Alignment via the record-independent /proc probe ONLY. Linux blocks overwriting a running
	// executable (ETXTBSY), so an in-place upgrade/rebuild unlinks the old file and /proc/<pid>/exe
	// gains a " (deleted)" suffix -- a complete, HASH-FREE "behind" signal. The probe returns
	// (stale, checked); checked=false leaves alignment UNKNOWN (AlignChecked stays false), so a
	// "behind" verdict never renders on an undecidable state. It runs only for a live, pid-bearing
	// process -- a dead process has no /proc/<pid>/exe to read.
	if s.ProcessAlive && s.PID > 0 && in.ProcStale != nil {
		stale, checked := in.ProcStale(s.PID)
		if checked {
			s.AlignChecked = true
			s.Aligned = !stale
			if stale {
				s.StaleReason = "running binary was replaced on disk"
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
