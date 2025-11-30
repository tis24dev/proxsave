package logging

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/tis24dev/proxsave/internal/types"
)

type bootstrapEntry struct {
	level   types.LogLevel
	message string
	raw     bool
}

// BootstrapLogger accumula i log generati prima che il logger principale sia
// inizializzato, così da poterli riversare nel log finale.
type BootstrapLogger struct {
	mu       sync.Mutex
	entries  []bootstrapEntry
	flushed  bool
	minLevel types.LogLevel
	mirror   *Logger
}

// NewBootstrapLogger crea un nuovo bootstrap logger con livello INFO di default.
func NewBootstrapLogger() *BootstrapLogger {
	return &BootstrapLogger{
		minLevel: types.LogLevelInfo,
	}
}

// SetLevel aggiorna il livello minimo che verrà usato al flush.
func (b *BootstrapLogger) SetLevel(level types.LogLevel) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minLevel = level
}

// Println registra una riga “raw” (usata per banner/testo senza header).
func (b *BootstrapLogger) Println(message string) {
	fmt.Println(message)
	b.mirrorLog(types.LogLevelInfo, message)
	b.recordRaw(message)
}

// Debug registra un messaggio di debug senza stamparlo a console.
func (b *BootstrapLogger) Debug(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	b.mirrorLog(types.LogLevelDebug, msg)
	b.record(types.LogLevelDebug, msg)
}

// Printf registra una riga formattata come raw.
func (b *BootstrapLogger) Printf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	b.mirrorLog(types.LogLevelInfo, msg)
	b.recordRaw(msg)
}

// Info registra un messaggio informativo precoce.
func (b *BootstrapLogger) Info(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	b.mirrorLog(types.LogLevelInfo, msg)
	b.record(types.LogLevelInfo, msg)
}

// Warning registra un messaggio di warning precoce (stampato su stderr).
func (b *BootstrapLogger) Warning(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(os.Stderr, msg)
	msg = strings.TrimSuffix(msg, "\n")
	b.mirrorLog(types.LogLevelWarning, msg)
	b.record(types.LogLevelWarning, msg)
}

// Error registra un messaggio di errore precoce (stderr).
func (b *BootstrapLogger) Error(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(os.Stderr, msg)
	msg = strings.TrimSuffix(msg, "\n")
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

// Flush riversa le entry accumulate nel logger principale (solo la prima volta).
func (b *BootstrapLogger) Flush(logger *Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.flushed {
		return
	}
	for _, entry := range b.entries {
		if entry.raw {
			logger.Info("%s", entry.message)
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
