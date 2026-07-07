package logging

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
	"golang.org/x/term"
)

// bootstrapTimeFormat matches the main Logger's timeFormat (see logger.go) so
// bootstrap console lines carry an identically shaped timestamp.
const bootstrapTimeFormat = "2006-01-02 15:04:05"

// consoleUseColor decides whether the bootstrap console prefix carries ANSI
// color for the given target stream. The main Logger's color is config-driven
// (USE_COLOR/DISABLE_COLORS), but the bootstrap runs before config is loaded,
// so it falls back to the conventional rule: color only on a real terminal and
// only when NO_COLOR is unset.
func consoleUseColor(f *os.File) bool {
	if f == nil {
		return false
	}
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

type bootstrapEntry struct {
	level   types.LogLevel
	message string
	raw     bool
}

// BootstrapLogger accumulates logs generated before the main logger is initialized,
// so they can be flushed into the final log.
type BootstrapLogger struct {
	mu       sync.Mutex
	entries  []bootstrapEntry
	flushed  bool
	minLevel types.LogLevel
	mirror   *Logger

	// consoleQuiet suppresses direct console prints while a full-screen UI
	// session owns the terminal; mirror/record keep working.
	consoleQuiet bool
}

// NewBootstrapLogger creates a new bootstrap logger with INFO level by default.
func NewBootstrapLogger() *BootstrapLogger {
	return &BootstrapLogger{
		minLevel: types.LogLevelInfo,
	}
}

// SetLevel updates the minimum level used during Flush.
// SetConsoleQuiet suppresses direct console prints (mirror logging and the
// recorded summary are unaffected). Used while a full-screen UI session owns
// the terminal: raw writes would corrupt the alternate screen.
func (b *BootstrapLogger) SetConsoleQuiet(quiet bool) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.consoleQuiet = quiet
}

func (b *BootstrapLogger) consoleQuietEnabled() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.consoleQuiet
}

// EntryCount returns the number of recorded entries (a mark for
// ReplayConsoleSince).
func (b *BootstrapLogger) EntryCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// ReplayConsoleSince re-prints to stderr the warning/error entries recorded
// after mark. Used when console prints were quieted for a UI handoff that
// then failed before the UI could take over: the failure must not stay
// invisible.
func (b *BootstrapLogger) ReplayConsoleSince(mark int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	entries := append([]bootstrapEntry(nil), b.entries...)
	b.mu.Unlock()
	if mark < 0 {
		mark = 0
	}
	// LogLevel orders NONE(0) < CRITICAL < ERROR < WARNING < INFO < DEBUG:
	// replay warning and worse only.
	for i := mark; i < len(entries); i++ {
		e := entries[i]
		if !e.raw && e.level >= types.LogLevelCritical && e.level <= types.LogLevelWarning {
			fmt.Fprintln(os.Stderr, e.message)
		}
	}
}

func (b *BootstrapLogger) SetLevel(level types.LogLevel) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minLevel = level
}

// levelValue returns the configured minimum level (lock-safe), so a consumer can
// create a mirror at the SAME verbosity the bootstrap console uses instead of
// hardcoding one. Defaults to INFO for a nil receiver.
func (b *BootstrapLogger) levelValue() types.LogLevel {
	if b == nil {
		return types.LogLevelInfo
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.minLevel
}

// Println records a raw line (used for banners/text without a header).
func (b *BootstrapLogger) Println(message string) {
	if b == nil {
		return
	}
	if !b.consoleQuietEnabled() {
		fmt.Println(message)
	}
	b.mirrorLog(types.LogLevelInfo, message)
	b.recordRaw(message)
}

// Debug records a debug message without printing it to the console.
func (b *BootstrapLogger) Debug(format string, args ...interface{}) {
	if b == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	b.mirrorLog(types.LogLevelDebug, msg)
	b.record(types.LogLevelDebug, msg)
}

// Printf records a formatted line as raw.
func (b *BootstrapLogger) Printf(format string, args ...interface{}) {
	if b == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if !b.consoleQuietEnabled() {
		fmt.Println(msg)
	}
	b.mirrorLog(types.LogLevelInfo, msg)
	b.recordRaw(msg)
}

// Info logs an early informational message.
func (b *BootstrapLogger) Info(format string, args ...interface{}) {
	if b == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if !b.consoleQuietEnabled() {
		// Print the same "[timestamp] LEVEL   message" prefix as the main
		// Logger; record() still keeps the RAW msg so Flush re-formats it once.
		now := time.Now().Format(bootstrapTimeFormat)
		line := FormatConsoleLogLine(now, types.LogLevelInfo, msg, consoleUseColor(os.Stdout))
		fmt.Fprint(os.Stdout, line)
	}
	b.mirrorLog(types.LogLevelInfo, msg)
	b.record(types.LogLevelInfo, msg)
}

// Warning records an early warning message (printed to stderr).
func (b *BootstrapLogger) Warning(format string, args ...interface{}) {
	if b == nil {
		return
	}
	msg := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")
	if !b.consoleQuietEnabled() {
		// Same prefixed console format as the main Logger, to stderr.
		now := time.Now().Format(bootstrapTimeFormat)
		line := FormatConsoleLogLine(now, types.LogLevelWarning, msg, consoleUseColor(os.Stderr))
		fmt.Fprint(os.Stderr, line)
	}
	b.mirrorLog(types.LogLevelWarning, msg)
	b.record(types.LogLevelWarning, msg)
}

// Error records an early error message (stderr).
func (b *BootstrapLogger) Error(format string, args ...interface{}) {
	if b == nil {
		return
	}
	msg := strings.TrimSuffix(fmt.Sprintf(format, args...), "\n")
	if !b.consoleQuietEnabled() {
		// Same prefixed console format as the main Logger, to stderr.
		now := time.Now().Format(bootstrapTimeFormat)
		line := FormatConsoleLogLine(now, types.LogLevelError, msg, consoleUseColor(os.Stderr))
		fmt.Fprint(os.Stderr, line)
	}
	b.mirrorLog(types.LogLevelError, msg)
	b.record(types.LogLevelError, msg)
}

func (b *BootstrapLogger) record(level types.LogLevel, message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, bootstrapEntry{
		level:   level,
		message: message,
	})
}

func (b *BootstrapLogger) recordRaw(message string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries = append(b.entries, bootstrapEntry{
		level:   types.LogLevelInfo,
		message: message,
		raw:     true,
	})
}

// Flush flushes accumulated entries into the main logger (only the first time).
func (b *BootstrapLogger) Flush(logger *Logger) {
	if b == nil || logger == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return
	}
	for _, entry := range b.entries {
		if entry.raw {
			logger.AppendRaw(entry.message)
			continue
		}
		if entry.level > b.minLevel {
			continue
		}
		switch entry.level {
		case types.LogLevelDebug:
			logger.Debug("%s", entry.message)
		case types.LogLevelInfo:
			logger.Info("%s", entry.message)
		case types.LogLevelWarning:
			logger.Warning("%s", entry.message)
		case types.LogLevelError:
			logger.Error("%s", entry.message)
		case types.LogLevelCritical:
			logger.Critical("%s", entry.message)
		default:
			logger.Info("%s", entry.message)
		}
	}
	b.flushed = true
	b.entries = nil
}

// SetMirrorLogger forwards every bootstrap message to the provided logger.
func (b *BootstrapLogger) SetMirrorLogger(logger *Logger) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.mirror = logger
	b.mu.Unlock()
}

func (b *BootstrapLogger) mirrorLog(level types.LogLevel, message string) {
	b.mu.Lock()
	mirror := b.mirror
	b.mu.Unlock()
	if mirror == nil {
		return
	}
	switch level {
	case types.LogLevelDebug:
		mirror.Debug("%s", message)
	case types.LogLevelInfo:
		mirror.Info("%s", message)
	case types.LogLevelWarning:
		mirror.Warning("%s", message)
	case types.LogLevelError:
		mirror.Error("%s", message)
	case types.LogLevelCritical:
		mirror.Critical("%s", message)
	default:
		mirror.Info("%s", message)
	}
}
