package logging

import (
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestNewLineWriter_SplitsAcrossWrites(t *testing.T) {
	var got []string
	w := NewLineWriter(func(line string) { got = append(got, line) })

	// First write ends on a partial "par" (no newline).
	if _, err := w.Write([]byte("[a] INFO x\npar")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"[a] INFO x"}) {
		t.Fatalf("after write 1 got %#v, want [\"[a] INFO x\"]", got)
	}

	// Second write completes the partial and adds another full line.
	if _, err := w.Write([]byte("tial\n[b] WARN y\n")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	want := []string{"[a] INFO x", "partial", "[b] WARN y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestNewLineWriter_StripsANSIAndKeepsPrefix(t *testing.T) {
	var got []string
	w := NewLineWriter(func(line string) { got = append(got, line) })
	if _, err := w.Write([]byte("\x1b[32m[c] INFO z\x1b[0m\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"[c] INFO z"}) {
		t.Fatalf("got %#v, want [\"[c] INFO z\"]", got)
	}
}

func TestNewLineWriter_PartialStaysBuffered(t *testing.T) {
	var got []string
	w := NewLineWriter(func(line string) { got = append(got, line) })

	if _, err := w.Write([]byte("no newline yet")); err != nil {
		t.Fatalf("write 1: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected nothing forwarded before newline, got %#v", got)
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"no newline yet"}) {
		t.Fatalf("got %#v, want [\"no newline yet\"]", got)
	}
}

func TestCaptureConsole_WiresAndRestores(t *testing.T) {
	// Keep the process-global default logger clean across the test.
	prev := GetDefaultLogger()
	t.Cleanup(func() { SetDefaultLogger(prev) })

	fresh := New(types.LogLevelInfo, false)
	fresh.SetOutput(io.Discard) // so post-restore lines go nowhere noisy
	SetDefaultLogger(fresh)

	var mu sync.Mutex
	var lines []string
	sink := NewLineWriter(func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})

	bootstrap := NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(false)

	restore := CaptureConsole(bootstrap, sink)

	Info("hello")           // default logger -> swapped to sink
	bootstrap.Info("world") // bootstrap -> mirror -> sink

	mu.Lock()
	captured := append([]string(nil), lines...)
	mu.Unlock()

	if len(captured) != 2 {
		t.Fatalf("expected 2 captured lines, got %#v", captured)
	}
	if !containsAll(captured[0], "INFO", "hello") {
		t.Fatalf("first captured line %q missing INFO/hello", captured[0])
	}
	if !containsAll(captured[1], "INFO", "world") {
		t.Fatalf("second captured line %q missing INFO/world", captured[1])
	}

	// Bootstrap must be console-quiet while captured.
	if !bootstrap.consoleQuietEnabled() {
		t.Fatalf("bootstrap should be console-quiet during capture")
	}

	restore()

	// After restore: default logger no longer feeds the sink.
	before := len(captured)
	Info("after-restore")
	mu.Lock()
	n := len(lines)
	mu.Unlock()
	if n != before {
		t.Fatalf("default logger still feeding sink after restore: %d != %d", n, before)
	}

	// After restore: bootstrap mirror detached and prior quiet state restored.
	bootstrap.mu.Lock()
	mirror := bootstrap.mirror
	bootstrap.mu.Unlock()
	if mirror != nil {
		t.Fatalf("bootstrap mirror should be nil after restore, got %v", mirror)
	}
	if bootstrap.consoleQuietEnabled() {
		t.Fatalf("bootstrap console-quiet should be restored to false")
	}
}

func TestCaptureConsole_FiltersDebugAtStandardLevel(t *testing.T) {
	// Standard run: bootstrap + default logger both at INFO. Debug lines must NOT
	// leak into the UI stream - the mirror is created at the bootstrap's level, not
	// hardcoded Debug (a debug run raises the level and would stream debug).
	prev := GetDefaultLogger()
	t.Cleanup(func() { SetDefaultLogger(prev) })
	fresh := New(types.LogLevelInfo, false)
	fresh.SetOutput(io.Discard)
	SetDefaultLogger(fresh)

	var mu sync.Mutex
	var lines []string
	sink := NewLineWriter(func(line string) {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
	})

	bootstrap := NewBootstrapLogger() // INFO by default
	bootstrap.SetConsoleQuiet(false)
	restore := CaptureConsole(bootstrap, sink)
	defer restore()

	bootstrap.Debug("boot-debug-hidden")
	bootstrap.Info("boot-info-shown")
	Debug("default-debug-hidden")
	Info("default-info-shown")

	mu.Lock()
	joined := strings.Join(lines, "\n")
	mu.Unlock()

	if strings.Contains(joined, "boot-debug-hidden") || strings.Contains(joined, "default-debug-hidden") {
		t.Fatalf("debug lines leaked into the standard-level stream:\n%s", joined)
	}
	if !strings.Contains(joined, "boot-info-shown") || !strings.Contains(joined, "default-info-shown") {
		t.Fatalf("info lines missing from the stream:\n%s", joined)
	}
}

func TestCaptureConsole_NilBootstrap(t *testing.T) {
	prev := GetDefaultLogger()
	t.Cleanup(func() { SetDefaultLogger(prev) })

	fresh := New(types.LogLevelInfo, false)
	fresh.SetOutput(io.Discard)
	SetDefaultLogger(fresh)

	var lines []string
	sink := NewLineWriter(func(line string) { lines = append(lines, line) })

	restore := CaptureConsole(nil, sink)
	Info("only-default")
	restore()

	if len(lines) != 1 || !containsAll(lines[0], "INFO", "only-default") {
		t.Fatalf("nil-bootstrap capture failed: %#v", lines)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
