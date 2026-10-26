package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/ui/theme"
)

const (
	// daemonLogPrefix names the logs the resident daemon and its admin commands write
	// into DAEMON_LOG_PATH: daemon-<host>-<YYYYMMDD-HHMMSS>.log (runLogLocation).
	daemonLogPrefix = "daemon"
	// daemonLogsKept is how many daemon logs of this host a daemon start keeps: the one
	// it just opened and the two before it.
	daemonLogsKept = 3
	// daemonLogTimestampLayout is the timestamp in a daemon log name, the same a run
	// puts in its own log name.
	daemonLogTimestampLayout = "20060102-150405"
)

// daemonLogKey splits a daemon log name, daemon-<host>-<YYYYMMDD-HHMMSS>.log, into the
// host and the timestamp it carries.
func daemonLogKey(name string) (host, ts string, ok bool) {
	prefix, suffix := daemonLogPrefix+"-", ".log"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return "", "", false
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
	tsLen := len(daemonLogTimestampLayout)
	if len(rest) < tsLen+2 || rest[len(rest)-tsLen-1] != '-' {
		return "", "", false
	}
	ts = rest[len(rest)-tsLen:]
	if _, err := time.Parse(daemonLogTimestampLayout, ts); err != nil {
		return "", "", false
	}
	return rest[:len(rest)-tsLen-1], ts, true
}

// rotateDaemonLogs keeps daemonLogsKept daemon logs of this host in the folder of
// current, the log this daemon start opened: current itself and the newest others, and
// deletes the older ones. A new log is born at every start of the process (reboot,
// upgrade, restart) and nothing else ever removes one (discussion #336). Only the logs
// carrying current's host are looked at, so a folder shared by several hosts keeps
// each host's own three. current counts toward the three wherever its name sorts, so a
// clock set back cannot leave a fourth. No log a process still holds open is deleted,
// such as the one of the --daemon-setup that started this daemon; when the open logs
// cannot be read, none is. The evidence is DEBUG; a log it cannot delete gets its fact
// line and the outcome, INFO because nothing is asked of the operator.
func rotateDaemonLogs(ctx context.Context, logger *logging.Logger, current string, timeout time.Duration) {
	const op = "daemon log rotation"
	if current == "" {
		logging.DebugStep(logger, op, "skipped reason=no_log_file")
		return
	}
	dir, currentName := filepath.Dir(current), filepath.Base(current)
	host, _, ok := daemonLogKey(currentName)
	if !ok {
		logging.DebugStep(logger, op, "skipped reason=unexpected_name name=%s", currentName)
		return
	}
	logging.DebugStep(logger, op, "path=%s pattern=%s-*.log keep=%d", dir, daemonLogPrefix, daemonLogsKept)

	names, err := safefs.Run(ctx, "daemon-log-list", dir, timeout, func() ([]string, error) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				names = append(names, entry.Name())
			}
		}
		return names, nil
	})
	if err != nil {
		logging.DebugStep(logger, op, "failed stage=list path=%s error=%v", dir, err)
		logger.Info("  Logs not listed: %s", safefs.SystemErrorText(err))
		logger.Info("%s Daemon logs: rotation not applied", theme.SymbolWarning)
		return
	}

	var others []string
	listed := 0
	for _, name := range names {
		if h, _, ok := daemonLogKey(name); ok && strings.EqualFold(h, host) {
			listed++
			if name != currentName {
				others = append(others, name)
			}
		}
	}
	// Same host and a fixed-width timestamp: the name order is the time order.
	slices.Sort(others)
	slices.Reverse(others)
	var expired []string
	if len(others) > daemonLogsKept-1 {
		expired = others[daemonLogsKept-1:]
	}
	logging.DebugStep(logger, op, "listed=%d delete=%d", listed, len(expired))
	if len(expired) == 0 {
		return
	}

	open, err := safefs.HeldOpenIn(ctx, dir, timeout)
	if err != nil {
		logging.DebugStep(logger, op, "skipped stage=open_check path=%s error=%v", dir, err)
		return
	}

	failed := 0
	for _, name := range expired {
		if open[name] {
			logging.DebugStep(logger, op, "kept %s reason=held_open", name)
			continue
		}
		err := safefs.Remove(ctx, filepath.Join(dir, name), timeout)
		switch {
		case err == nil:
			logging.DebugStep(logger, op, "deleted %s", name)
		case errors.Is(err, fs.ErrNotExist):
			logging.DebugStep(logger, op, "%s already removed", name)
		default:
			failed++
			logging.DebugStep(logger, op, "failed %s error=%v", name, err)
			logger.Info("  Log not deleted: %s: %s", name, safefs.SystemErrorText(err))
		}
	}
	if failed > 0 {
		logger.Info("%s Daemon logs: rotation not applied", theme.SymbolWarning)
	}
}
