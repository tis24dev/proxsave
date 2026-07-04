package logging

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestLoggerSwapOutput(t *testing.T) {
	l := New(types.LogLevelInfo, false)
	var buf bytes.Buffer
	l.SetOutput(&buf)

	prev := l.SwapOutput(io.Discard)
	if prev != &buf {
		t.Fatalf("SwapOutput must return the previous writer")
	}
	l.Info("hidden line")
	if buf.Len() != 0 {
		t.Fatalf("console must be silent after swap, got %q", buf.String())
	}

	l.SetOutput(prev)
	l.Info("visible line")
	if buf.Len() == 0 {
		t.Fatal("console must work again after restore")
	}
	if got := l.SwapOutput(nil); got != &buf {
		t.Fatalf("nil swap must still return previous writer")
	}
}

func TestBootstrapConsoleQuiet(t *testing.T) {
	b := NewBootstrapLogger()

	readStdout := func(fn func()) string {
		t.Helper()
		orig := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		fn()
		_ = w.Close()
		os.Stdout = orig
		data, _ := io.ReadAll(r)
		return string(data)
	}

	if out := readStdout(func() { b.Info("before quiet") }); out == "" {
		t.Fatal("info must print by default")
	}

	b.SetConsoleQuiet(true)
	if out := readStdout(func() { b.Info("while quiet") }); out != "" {
		t.Fatalf("quiet mode must not print, got %q", out)
	}

	b.SetConsoleQuiet(false)
	if out := readStdout(func() { b.Info("after quiet") }); out == "" {
		t.Fatal("info must print again after quiet is lifted")
	}

	// The quiet lines are still recorded for the flush/log file.
	var found int
	b.mu.Lock()
	for _, e := range b.entries {
		if e.message == "while quiet" {
			found++
		}
	}
	b.mu.Unlock()
	if found != 1 {
		t.Fatalf("quiet line must still be recorded, found %d", found)
	}
}

func TestBootstrapReplayConsoleSince(t *testing.T) {
	b := NewBootstrapLogger()
	b.SetConsoleQuiet(true)
	b.Info("noise before mark")
	mark := b.EntryCount()
	b.Info("muted info")       // below warning: not replayed
	b.Warning("muted warning") // replayed
	b.Error("muted error")     // replayed

	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	b.ReplayConsoleSince(mark)
	_ = w.Close()
	os.Stderr = orig
	data, _ := io.ReadAll(r)
	out := string(data)

	if !strings.Contains(out, "muted warning") || !strings.Contains(out, "muted error") {
		t.Fatalf("replay must include warning and error: %q", out)
	}
	if strings.Contains(out, "muted info") || strings.Contains(out, "noise before mark") {
		t.Fatalf("replay must exclude info and pre-mark entries: %q", out)
	}
}
