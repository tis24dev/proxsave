package checks

import (
	"bytes"
	"os"
	"strconv"
)

// procStartTimeFunc returns a process start-time used to tell a live lock owner
// apart from a reused pid. It is a seam: overridden in tests.
var procStartTimeFunc = defaultProcStartTime

// defaultProcStartTime returns the start-time of pid (field 22 of /proc/<pid>/stat,
// in clock ticks since boot) and ok=true, or (0, false) when it cannot be
// determined (non-Linux, /proc unreadable, or the process is gone).
func defaultProcStartTime(pid int) (uint64, bool) {
	content, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	return parseProcStat(content)
}

// parseProcStat extracts field 22 (starttime) from a /proc/<pid>/stat body. Field 2
// (comm) is parenthesized and may itself contain spaces and ')', so the fields
// after comm are located by splitting AFTER the LAST ')'.
func parseProcStat(content []byte) (uint64, bool) {
	end := bytes.LastIndexByte(content, ')')
	if end < 0 || end+1 >= len(content) {
		return 0, false
	}
	// Fields after comm are field 3 (state) onward. starttime is field 22 overall
	// -> index 19 (22 - 3) in this 0-based slice.
	rest := bytes.Fields(content[end+1:])
	const startTimeIndex = 19
	if len(rest) <= startTimeIndex {
		return 0, false
	}
	v, err := strconv.ParseUint(string(rest[startTimeIndex]), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
