package logging

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

// Logger gestisce il logging dell'applicazione
type Logger struct {
	mu           sync.Mutex
	level        types.LogLevel
	useColor     bool
	output       io.Writer
	timeFormat   string
	logFile      *os.File // File di log (opzionale)
	warningCount int64
	errorCount   int64
	exitFunc     func(int)
}

// New crea un nuovo logger
func New(level types.LogLevel, useColor bool) *Logger {
	return &Logger{
		level:      level,
		useColor:   useColor,
		output:     os.Stdout,
		timeFormat: "2006-01-02 15:04:05",
		exitFunc:   os.Exit,
	}
}

// SetOutput imposta l'output writer del logger
func (l *Logger) SetOutput(w io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w == nil {
		l.output = os.Stdout
		return
	}
	l.output = w
}

// SetLevel imposta il livello di logging
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

// OpenLogFile apre un file di log e inizia la scrittura real-time
func (l *Logger) OpenLogFile(logPath string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Se c'è già un file aperto, chiudilo prima
	if l.logFile != nil {
		l.logFile.Close()
	}

	// Crea il file di log (O_CREATE|O_WRONLY|O_APPEND)
	// O_SYNC forza la scrittura immediata su disco (real-time, nessun buffer)
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_SYNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open log file %s: %w", logPath, err)
	}

	l.logFile = file
	return nil
}

// CloseLogFile chiude il file di log (da chiamare dopo le notifiche)
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

// GetLogFilePath restituisce il path del file di log attualmente aperto (o "" se nessuno)
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

// GetLevel restituisce il livello corrente
func (l *Logger) GetLevel() types.LogLevel {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// log è il metodo interno per scrivere i log
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

	// Formato per stdout (con colori se abilitati)
	outputStdout := fmt.Sprintf("[%s] %s%-8s%s %s\n",
		timestamp,
		colorCode,
		levelStr,
		resetCode,
		message,
	)

	// Formato per file (senza colori)
	outputFile := fmt.Sprintf("[%s] %-8s %s\n",
		timestamp,
		levelStr,
		message,
	)

	// Scrivi su stdout con colori
	fmt.Fprint(l.output, outputStdout)

	// Se c'è un file di log, scrivi anche lì (senza colori)
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

// Debug scrive un log di debug
func (l *Logger) Debug(format string, args ...interface{}) {
	l.log(types.LogLevelDebug, format, args...)
}

// Info scrive un log informativo
func (l *Logger) Info(format string, args ...interface{}) {
	l.log(types.LogLevelInfo, format, args...)
}

// Phase scrive un log informativo con etichetta PHASE
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

// Step scrive un log informativo con etichetta STEP (per evidenziare attività sequenziali)
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

// Skip scrive un log informativo con etichetta SKIP (per elementi disabilitati/ignorati)
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

// Warning scrive un log di warning
func (l *Logger) Warning(format string, args ...interface{}) {
	l.log(types.LogLevelWarning, format, args...)
}

// Error scrive un log di errore
func (l *Logger) Error(format string, args ...interface{}) {
	l.log(types.LogLevelError, format, args...)
}

// Critical scrive un log critico
func (l *Logger) Critical(format string, args ...interface{}) {
	l.log(types.LogLevelCritical, format, args...)
}

// Fatal scrive un log critico ed esce con il codice specificato
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

// SetDefaultLogger imposta il logger di default
func SetDefaultLogger(logger *Logger) {
	defaultLogger = logger
}

// GetDefaultLogger restituisce il logger di default
func GetDefaultLogger() *Logger {
	return defaultLogger
}

// Package-level convenience functions

// Debug scrive un log di debug usando il logger di default
func Debug(format string, args ...interface{}) {
	defaultLogger.Debug(format, args...)
}

// Info scrive un log informativo usando il logger di default
func Info(format string, args ...interface{}) {
	defaultLogger.Info(format, args...)
}

// Step scrive un log STEP usando il logger di default
func Step(format string, args ...interface{}) {
	defaultLogger.Step(format, args...)
}

// Skip scrive un log SKIP usando il logger di default
func Skip(format string, args ...interface{}) {
	defaultLogger.Skip(format, args...)
}

// Warning scrive un log di warning usando il logger di default
func Warning(format string, args ...interface{}) {
	defaultLogger.Warning(format, args...)
}

// Error scrive un log di errore usando il logger di default
func Error(format string, args ...interface{}) {
	defaultLogger.Error(format, args...)
}

// Critical scrive un log critico usando il logger di default
func Critical(format string, args ...interface{}) {
	defaultLogger.Critical(format, args...)
}

// Fatal scrive un log critico ed esce con il codice specificato
func Fatal(exitCode types.ExitCode, format string, args ...interface{}) {
	defaultLogger.Fatal(exitCode, format, args...)
}
