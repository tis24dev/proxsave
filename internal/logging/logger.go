package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
	"github.com/tis24dev/proxsave/internal/types"
)

// osOpenFile and fileWrite are indirected so tests can simulate a blocking/dead
// log mount on the open and the write paths.
var (
	osOpenFile = os.OpenFile
	fileWrite  = func(w io.Writer, s string) (int, error) { return fmt.Fprint(w, s) }
)

// logWriteTimeoutCap bounds how long a single log-line write may block before the
// file sink is disabled. It caps the FS_IO_TIMEOUT-derived budget so that a mount
// dying mid-run stalls logging at most this long (once) before degrading to
// stdout-only, rather than for the full (generous) FS_IO_TIMEOUT.
const logWriteTimeoutCap = 5 * time.Second

// logSinkTimeoutStrikes is how many CONSECUTIVE write timeouts disable the file
// sink. One transient stall keeps the sink armed; a genuinely dead mount latches
// after this many strikes (bounded added latency ~= strikes * logWriteTimeoutCap
// once). A successful write resets the count.
const logSinkTimeoutStrikes = 3

// Logger handles application logging.
type Logger struct {
	mu         sync.Mutex
	level      types.LogLevel
	useColor   bool
	output     io.Writer
	timeFormat string
	logFile    *os.File // Log file (optional)
	// ioTimeout bounds the log-file open/write/close so a dead/stale LOG_PATH mount
	// cannot wedge the run in an uninterruptible syscall. Zero means unbounded
	// (legacy / FS_IO_TIMEOUT=0 opt-out). Set via SetIOTimeout by the run logger;
	// session loggers (local /tmp) leave it at 0.
	ioTimeout time.Duration
	// fileSinkDisabled is set after an open/write/close timeout so subsequent log
	// lines skip the (dead) file and go to stdout only. Guarded by mu.
	fileSinkDisabled bool
	// consecutiveLogTimeouts counts back-to-back write timeouts; the sink is disabled
	// only after logSinkTimeoutStrikes strikes so a single transient stall does not
	// permanently truncate the on-disk log. A successful write resets it. Guarded by mu.
	consecutiveLogTimeouts int
	warningCount           int64
	errorCount             int64
	// notifyCount tracks NOTIFY-ERR lines: notification/communication failures that
	// display as errors but are warning-weight for the run status (never escalate the
	// exit code / gauge to error). Kept separate from errorCount so a notify failure
	// never looks like a backup error.
	notifyCount int64
	// notifyErrorScope, when > 0, reclassifies every unlabeled error/critical logged
	// through this logger as a NOTIFY-ERR (display-error, warning-weight). It brackets
	// the notification dispatch, whose whole subsystem (notifiers, adapter, serverbot)
	// shares this one logger instance, so a channel outage never escalates the run.
	// Guarded by mu; nestable via Enter/ExitNotifyErrorScope.
	notifyErrorScope int
	issueLines       []string // Captured WARNING/ERROR/CRITICAL lines for end-of-run summary
	exitFunc         func(int)
	secrets          []secretForm // registered secret values scrubbed from every log line
}

// RegisterSecret records a secret value so it is masked out of every subsequent
// log line (stdout, log file, and the end-of-run issue summary), at any level.
// This is a defense-in-depth net on top of source-level redaction; empty/too
// short secrets are ignored, and both raw and URL-encoded forms are covered.
func (l *Logger) RegisterSecret(s string) {
	forms := secretReplaceForms([]string{s})
	if len(forms) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range forms {
		dup := false
		for _, existing := range l.secrets {
			if existing.form == f.form {
				dup = true
				break
			}
		}
		if !dup {
			l.secrets = append(l.secrets, f)
		}
	}
	sort.Slice(l.secrets, func(i, j int) bool {
		return len(l.secrets[i].form) > len(l.secrets[j].form)
	})
}

// New creates a new logger.
func New(level types.LogLevel, useColor bool) *Logger {
	return &Logger{
		level:      level,
		useColor:   useColor,
		output:     os.Stdout,
		timeFormat: "2006-01-02 15:04:05",
		exitFunc:   os.Exit,
	}
}

// SwapOutput replaces the console writer and returns the previous one, so
// full-screen UI sessions can silence the console for their lifetime (log
// files are unaffected) and restore it afterwards.
func (l *Logger) SwapOutput(w io.Writer) io.Writer {
	l.mu.Lock()
	defer l.mu.Unlock()
	prev := l.output
	if w == nil {
		w = os.Stdout
	}
	l.output = w
	return prev
}

// SetOutput sets the logger output writer.
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w == nil {
		l.output = os.Stdout
		return
	}
	l.output = w
}

// SetIOTimeout bounds subsequent log-file open/write/close operations so a dead or
// stale LOG_PATH mount cannot wedge the logger in an uninterruptible syscall. A
// non-positive value restores unbounded behaviour (the FS_IO_TIMEOUT=0 opt-out).
// Callers pass fsIoTimeoutDuration(cfg).
func (l *Logger) SetIOTimeout(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ioTimeout = d
}

// SetLevel sets the logging level.
func (l *Logger) SetLevel(level types.LogLevel) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

// SetExitFunc allows customizing the exit function (useful for tests).
// If fn is nil, it restores os.Exit.
func (l *Logger) SetExitFunc(fn func(int)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if fn == nil {
		l.exitFunc = os.Exit
		return
	}
	l.exitFunc = fn
}

// OpenLogFile opens a log file and starts real-time writing.
func (l *Logger) OpenLogFile(logPath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// If a log file is already open, close it first.
	if l.logFile != nil {
		err := l.logFile.Close()
		l.logFile = nil
		if err != nil {
			return fmt.Errorf("failed to close existing log file: %w", err)
		}
	}

	file, err := l.openFileBounded(logPath)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	l.logFile = file
	l.fileSinkDisabled = false
	// A fresh mount must start with a clean strike count: OpenLogFile is the only
	// re-enable path, so any stale count from a prior disable would otherwise let
	// a single new timeout re-latch the sink and defeat the N-strike protection.
	l.consecutiveLogTimeouts = 0
	return nil
}

// openFileBounded performs the O_SYNC create+open, bounded by l.ioTimeout when set
// (0 = unbounded, legacy). On timeout safefs returns *TimeoutError and abandons the
// worker goroutine; the caller falls back to stdout-only. Caller must hold l.mu.
func (l *Logger) openFileBounded(logPath string) (*os.File, error) {
	open := func() (*os.File, error) {
		// O_SYNC forces immediate writes to disk (real-time, no buffering).
		return osOpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0600)
	}
	if l.ioTimeout <= 0 {
		return open()
	}
	return safefs.Run(context.Background(), "logopen", logPath, l.ioTimeout, open)
}

// writeFileLocked writes one preformatted line to the open log file, bounding the
// (O_SYNC) write so a dead/stale mount cannot wedge the logger while it holds l.mu.
// A single write timeout is a strike; only logSinkTimeoutStrikes CONSECUTIVE
// strikes permanently disable the sink (subsequent lines then skip the file). A
// non-timeout outcome resets the streak. Caller MUST hold l.mu and have checked
// logFile != nil && !fileSinkDisabled.
func (l *Logger) writeFileLocked(line string) {
	f := l.logFile // local: a disable nils l.logFile, the abandoned goroutine keeps f
	if l.ioTimeout <= 0 {
		_, _ = fileWrite(f, line) // opt-out / legacy: unbounded
		return
	}
	wt := l.ioTimeout
	if wt > logWriteTimeoutCap {
		wt = logWriteTimeoutCap
	}
	_, err := safefs.Run(context.Background(), "logwrite", f.Name(), wt, func() (int, error) {
		return fileWrite(f, line)
	})
	if err != nil && errors.Is(err, safefs.ErrTimeout) {
		l.consecutiveLogTimeouts++
		if l.consecutiveLogTimeouts >= logSinkTimeoutStrikes {
			l.disableFileSinkLocked(err)
		}
		return
	}
	// Any non-timeout outcome (a clean write or a non-timeout error) breaks the streak.
	l.consecutiveLogTimeouts = 0
}

// disableFileSinkLocked turns the file sink off after a timed-out write so every
// subsequent line skips the dead mount. The blocked write goroutine is abandoned
// and the *os.File is intentionally NOT closed here: a Close() on the dead mount
// would block too, and the abandoned goroutine still references it. We drop our
// reference (the fd is reclaimed at process exit). Caller MUST hold l.mu.
func (l *Logger) disableFileSinkLocked(cause error) {
	if l.fileSinkDisabled {
		return
	}
	timestamp := time.Now().Format(l.timeFormat)
	// Best-effort bounded marker so the shipped/attached on-disk log self-documents
	// the cut. Bounded via the same safefs path (min(ioTimeout, cap)) so it cannot
	// re-wedge; its failure is ignored. disableFileSinkLocked is only reached with
	// ioTimeout > 0 (the bounded write path), so mwt is always positive.
	if f := l.logFile; f != nil {
		mwt := l.ioTimeout
		if mwt > logWriteTimeoutCap {
			mwt = logWriteTimeoutCap
		}
		marker := fmt.Sprintf("[%s] %-8s log file sink disabled after I/O timeout; on-disk log truncated here\n", timestamp, types.LogLevelWarning.String())
		_, _ = safefs.Run(context.Background(), "logwrite-marker", f.Name(), mwt, func() (int, error) {
			return fileWrite(f, marker)
		})
	}
	l.fileSinkDisabled = true
	l.logFile = nil
	warn := fmt.Sprintf("log file sink disabled after I/O timeout (%v); continuing on stdout only", cause)
	// stdout only - never route this back through the (dead) file sink.
	_, _ = fmt.Fprintf(l.output, "[%s] %-8s %s\n", timestamp, types.LogLevelWarning.String(), warn)
	l.warningCount++
	l.issueLines = append(l.issueLines, fmt.Sprintf("[%s] %-8s %s", timestamp, types.LogLevelWarning.String(), warn))
}

// CloseLogFile closes the log file (call after notifications).
func (l *Logger) CloseLogFile() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logFile == nil {
		return nil
	}

	f := l.logFile
	l.logFile = nil
	if l.ioTimeout <= 0 {
		return f.Close()
	}
	// Bound the flushing close so a dead mount cannot hang shutdown; on timeout the
	// close goroutine and fd are abandoned (l.logFile is already nil, no double close).
	_, err := safefs.Run(context.Background(), "logclose", f.Name(), l.ioTimeout, func() (struct{}, error) {
		return struct{}{}, f.Close()
	})
	return err
}

// GetLogFilePath returns the path of the currently open log file (or "" if none).
func (l *Logger) GetLogFilePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logFile == nil {
		return ""
	}
	return l.logFile.Name()
}

// UsesColor returns whether color output is enabled.
func (l *Logger) UsesColor() bool {
	return l.useColor
}

// GetLevel returns the current log level.
func (l *Logger) GetLevel() types.LogLevel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// levelColorCode returns the ANSI color code for a level using the standard
// console palette. It is the single source of truth for the level->color
// mapping shared by the main Logger and FormatConsoleLogLine.
func levelColorCode(level types.LogLevel) string {
	switch level {
	case types.LogLevelDebug:
		return "\033[36m" // Cyan
	case types.LogLevelInfo:
		return "\033[32m" // Green
	case types.LogLevelWarning:
		return "\033[33m" // Yellow
	case types.LogLevelError:
		return "\033[31m" // Red
	case types.LogLevelCritical:
		return "\033[1;31m" // Bold Red
	}
	return ""
}

// assembleConsoleLine is the single source of truth for the stdout console log
// format. Both the main Logger and FormatConsoleLogLine build their output from
// this one Sprintf so the two can never drift. colorCode/resetCode are empty
// when color is disabled.
func assembleConsoleLine(timestamp, colorCode, levelStr, resetCode, message string) string {
	return fmt.Sprintf("[%s] %s%-8s%s %s\n",
		timestamp,
		colorCode,
		levelStr,
		resetCode,
		message,
	)
}

// FormatConsoleLogLine returns the exact stdout console format used by the main
// Logger for a standard (unlabelled) level:
//
//	[<timestamp>] <color><LEVEL padded %-8s><reset> <message>\n
//
// The color is applied only when useColor is true, using the same palette as
// the main Logger (see levelColorCode). This shared formatter lets the
// BootstrapLogger print early semantic lines with the same prefix as the main
// Logger, so the console format stays consistent across the whole run.
func FormatConsoleLogLine(timestamp string, level types.LogLevel, message string, useColor bool) string {
	var colorCode, resetCode string
	if useColor {
		resetCode = "\033[0m"
		colorCode = levelColorCode(level)
	}
	return assembleConsoleLine(timestamp, colorCode, level.String(), resetCode, message)
}

// log is the internal method used to write logs.
func (l *Logger) log(level types.LogLevel, format string, args ...interface{}) {
	l.logWithLabel(level, "", "", format, args...)
}

func (l *Logger) logWithLabel(level types.LogLevel, label string, colorOverride string, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level > l.level {
		return
	}

	// Inside a notification-dispatch scope, an unlabeled error/critical is a
	// notification/communication failure: emit it as a NOTIFY-ERR (display-error,
	// warning-weight) so a channel outage never escalates the run status. Explicitly
	// labeled lines (PHASE/STEP/SKIP, or an already-NOTIFY-ERR) are left untouched.
	if l.notifyErrorScope > 0 && label == "" &&
		(level == types.LogLevelError || level == types.LogLevelCritical) {
		label = NotifyErrorLabel
	}

	// Track warning/error counters for summary/exit coloring. A NOTIFY-ERR line
	// (notification/communication failure) is emitted at error level but counts into
	// notifyCount, not errorCount: it displays as an error yet is warning-weight for
	// the run status.
	switch {
	case label == NotifyErrorLabel:
		l.notifyCount++
	case level == types.LogLevelWarning:
		l.warningCount++
	case level == types.LogLevelError, level == types.LogLevelCritical:
		l.errorCount++
	}

	timestamp := time.Now().Format(l.timeFormat)
	levelStr := level.String()
	if label != "" {
		levelStr = label
	}
	message := fmt.Sprintf(format, args...)
	if len(l.secrets) > 0 {
		// Defense-in-depth: scrub any registered secret value (raw or
		// URL-encoded) from the line before it hits stdout, the log file, or
		// the issue summary (the log is shipped off-host to secondary/cloud).
		message = applySecretForms(message, l.secrets)
	}

	var colorCode string
	var resetCode string

	if l.useColor {
		resetCode = "\033[0m"
		if colorOverride != "" {
			colorCode = colorOverride
		} else {
			colorCode = levelColorCode(level)
		}
	}

	// Format for stdout (with colors if enabled). Built from the shared
	// assembler so this and FormatConsoleLogLine can never drift. levelStr may
	// be a label override (PHASE/STEP/SKIP) and colorCode a color override, so
	// this path passes the resolved parts rather than calling
	// FormatConsoleLogLine directly.
	outputStdout := assembleConsoleLine(timestamp, colorCode, levelStr, resetCode, message)

	// Format for file (without colors).
	outputFile := fmt.Sprintf("[%s] %-8s %s\n",
		timestamp,
		levelStr,
		message,
	)

	// Capture warnings/errors for final summary output (single-line).
	switch level {
	case types.LogLevelWarning, types.LogLevelError, types.LogLevelCritical:
		issue := fmt.Sprintf("[%s] %-8s %s", timestamp, levelStr, message)
		issue = strings.ReplaceAll(issue, "\r", " ")
		issue = strings.ReplaceAll(issue, "\n", " ")
		l.issueLines = append(l.issueLines, issue)
	}

	// Write to stdout with colors.
	_, _ = fmt.Fprint(l.output, outputStdout)

	// If a log file is open and not disabled, write there too (without colors),
	// bounded so a dead/stale mount cannot wedge logging while holding l.mu.
	if l.logFile != nil && !l.fileSinkDisabled {
		l.writeFileLocked(outputFile)
	}
}

// HasWarnings returns true if at least one warning was logged.
func (l *Logger) HasWarnings() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warningCount > 0
}

// HasErrors returns true if at least one error or critical message was logged.
func (l *Logger) HasErrors() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.errorCount > 0
}

// WarningCount returns the total number of WARNING log entries emitted.
func (l *Logger) WarningCount() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.warningCount
}

// ErrorCount returns the total number of ERROR/CRITICAL log entries emitted.
func (l *Logger) ErrorCount() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.errorCount
}

// NotifyCount returns the number of NOTIFY-ERR entries emitted (notification/
// communication failures shown as errors but treated as warning-weight).
func (l *Logger) NotifyCount() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.notifyCount
}

// IssueLines returns a copy of captured WARNING/ERROR/CRITICAL log lines in
// chronological order. Intended for end-of-run summaries.
func (l *Logger) IssueLines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.issueLines) == 0 {
		return nil
	}
	out := make([]string, len(l.issueLines))
	copy(out, l.issueLines)
	return out
}

// Debug writes a debug log.
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(types.LogLevelDebug, format, args...)
}

// Info writes an informational log
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(types.LogLevelInfo, format, args...)
}

// Phase writes an informational log with PHASE label
func (l *Logger) Phase(format string, args ...interface{}) {
	if l == nil {
		return
	}
	colorOverride := ""
	if l.useColor {
		colorOverride = "\033[34m"
	}
	l.logWithLabel(types.LogLevelInfo, "PHASE", colorOverride, format, args...)
}

// Step writes an informational log with STEP label (to highlight sequential activities)
func (l *Logger) Step(format string, args ...interface{}) {
	if l == nil {
		return
	}
	colorOverride := ""
	if l.useColor {
		colorOverride = "\033[34m"
	}
	l.logWithLabel(types.LogLevelInfo, "STEP", colorOverride, format, args...)
}

// Skip writes an informational log with SKIP label (for disabled/ignored elements)
func (l *Logger) Skip(format string, args ...interface{}) {
	if l == nil {
		return
	}
	colorOverride := ""
	if l.useColor {
		colorOverride = "\033[35m"
	}
	l.logWithLabel(types.LogLevelInfo, "SKIP", colorOverride, format, args...)
}

// Warning writes a warning log.
func (l *Logger) Warning(format string, args ...interface{}) {
	l.log(types.LogLevelWarning, format, args...)
}

// Error writes an error log.
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(types.LogLevelError, format, args...)
}

// NotifyErrorLabel is the level token written for a NOTIFY-ERR line: a
// notification/communication failure that must display as an error but stay
// warning-weight for the run exit code / status gauge.
const NotifyErrorLabel = "NOTIFY-ERR"

// NotifyError writes a notification/communication failure. It renders like an error
// (red, captured in the issue summary) but is tallied into notifyCount rather than
// errorCount, so it displays as ERROR yet never escalates the run status to error.
// The run-side count logic buckets the NOTIFY-ERR token as warning-weight.
func (l *Logger) NotifyError(format string, args ...interface{}) {
	if l == nil {
		return
	}
	l.logWithLabel(types.LogLevelError, NotifyErrorLabel, "", format, args...)
}

// NormalizeNotifyErrorToken rewrites the NOTIFY-ERR level token in a captured issue
// line to ERROR, so recap/footer renderers present notify failures as errors.
func NormalizeNotifyErrorToken(line string) string {
	return strings.Replace(line, NotifyErrorLabel, "ERROR", 1)
}

// EnterNotifyErrorScope brackets a region (the notification dispatch) in which every
// unlabeled error/critical logged through this logger is a notification/communication
// failure: it displays as an error but is warning-weight for the run status. Balanced
// by ExitNotifyErrorScope and nestable. Because the whole notification subsystem shares
// this one logger instance, wrapping the (sequential) dispatch reclassifies every
// channel failure in one place, with no per-call-site edits.
func (l *Logger) EnterNotifyErrorScope() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.notifyErrorScope++
	l.mu.Unlock()
}

// ExitNotifyErrorScope ends one EnterNotifyErrorScope level.
func (l *Logger) ExitNotifyErrorScope() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.notifyErrorScope > 0 {
		l.notifyErrorScope--
	}
	l.mu.Unlock()
}

// Critical writes a critical log.
func (l *Logger) Critical(format string, args ...interface{}) {
	l.log(types.LogLevelCritical, format, args...)
}

// Fatal writes a critical log and exits with the specified code
func (l *Logger) Fatal(exitCode types.ExitCode, format string, args ...interface{}) {
	l.Critical(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exitFunc == nil {
		l.exitFunc = os.Exit
	}
	l.exitFunc(exitCode.Int())
}

// AppendRaw writes a raw log line directly to the log file (if any)
// without emitting it to stdout. It is primarily used by the bootstrap
// logger to persist early banner/output without duplicating it on console.
func (l *Logger) AppendRaw(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logFile == nil || l.fileSinkDisabled {
		return
	}
	timestamp := time.Now().Format(l.timeFormat)
	output := fmt.Sprintf("[%s] %-8s %s\n",
		timestamp,
		types.LogLevelInfo.String(),
		message,
	)
	l.writeFileLocked(output)
}

// Package-level default logger
var defaultLogger *Logger

func init() {
	defaultLogger = New(types.LogLevelInfo, true)
}

// SetDefaultLogger sets the default logger.
func SetDefaultLogger(logger *Logger) {
	defaultLogger = logger
}

// GetDefaultLogger returns the default logger.
func GetDefaultLogger() *Logger {
	return defaultLogger
}

// Package-level convenience functions

// Debug writes a debug log using the default logger.
func Debug(format string, args ...interface{}) {
	defaultLogger.Debug(format, args...)
}

// Info writes an informational log using the default logger
func Info(format string, args ...interface{}) {
	defaultLogger.Info(format, args...)
}

// Step writes a STEP log using the default logger.
func Step(format string, args ...interface{}) {
	defaultLogger.Step(format, args...)
}

// Skip writes a SKIP log using the default logger.
func Skip(format string, args ...interface{}) {
	defaultLogger.Skip(format, args...)
}

// Warning writes a warning log using the default logger.
func Warning(format string, args ...interface{}) {
	defaultLogger.Warning(format, args...)
}

// Error writes an error log using the default logger.
func Error(format string, args ...interface{}) {
	defaultLogger.Error(format, args...)
}

// Critical writes a critical log using the default logger.
func Critical(format string, args ...interface{}) {
	defaultLogger.Critical(format, args...)
}

// Fatal writes a critical log and exits with the specified code
func Fatal(exitCode types.ExitCode, format string, args ...interface{}) {
	defaultLogger.Fatal(exitCode, format, args...)
}
