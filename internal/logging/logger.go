package logging

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// Logger handles application logging.
type Logger struct {
	mu           sync.Mutex
	level        types.LogLevel
	useColor     bool
	output       io.Writer
	timeFormat   string
	logFile      *os.File // Log file (optional)
	warningCount int64
	errorCount   int64
	exitFunc     func(int)
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
		l.logFile.Close()
	}

	// Create the log file (O_CREATE|O_WRONLY|O_APPEND).
	// O_SYNC forces immediate writes to disk (real-time, no buffering).
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	l.logFile = file
	return nil
}

// CloseLogFile closes the log file (call after notifications).
func (l *Logger) CloseLogFile() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.logFile == nil {
		return nil
	}

	err := l.logFile.Close()
	l.logFile = nil
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

	// Track warning/error counters for summary/exit coloring
	switch level {
	case types.LogLevelWarning:
		l.warningCount++
	case types.LogLevelError, types.LogLevelCritical:
		l.errorCount++
	}

	timestamp := time.Now().Format(l.timeFormat)
	levelStr := level.String()
	if label != "" {
		levelStr = label
	}
	message := fmt.Sprintf(format, args...)

	var colorCode string
	var resetCode string

	if l.useColor {
		resetCode = "\033[0m"
		if colorOverride != "" {
			colorCode = colorOverride
		} else {
			switch level {
			case types.LogLevelDebug:
				colorCode = "\033[36m" // Cyan
			case types.LogLevelInfo:
				colorCode = "\033[32m" // Green
			case types.LogLevelWarning:
				colorCode = "\033[33m" // Yellow
			case types.LogLevelError:
				colorCode = "\033[31m" // Red
			case types.LogLevelCritical:
				colorCode = "\033[1;31m" // Bold Red
			}
		}
	}

	// Format for stdout (with colors if enabled).
	outputStdout := fmt.Sprintf("[%s] %s%-8s%s %s\n",
		timestamp,
		colorCode,
		levelStr,
		resetCode,
		message,
	)

	// Format for file (without colors).
	outputFile := fmt.Sprintf("[%s] %-8s %s\n",
		timestamp,
		levelStr,
		message,
	)

	// Write to stdout with colors.
	fmt.Fprint(l.output, outputStdout)

	// If a log file is open, write there too (without colors).
	if l.logFile != nil {
		fmt.Fprint(l.logFile, outputFile)
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
	if l.logFile == nil {
		return
	}
	timestamp := time.Now().Format(l.timeFormat)
	output := fmt.Sprintf("[%s] %-8s %s\n",
		timestamp,
		types.LogLevelInfo.String(),
		message,
	)
	fmt.Fprint(l.logFile, output)
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
