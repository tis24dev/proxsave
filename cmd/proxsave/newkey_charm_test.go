package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/ui/shell"
	"github.com/tis24dev/proxsave/internal/uitest"
)

// End-to-end coverage of the Charm newkey flow through the
// newAgeSetupSession seam: the real engine collects a passphrase draft and
// writes the recipient file; a dying UI program maps to the interactive
// abort, not a hard failure.

type newkeyUIDriver struct {
	t       *testing.T
	buf     *shell.SyncBuffer
	pushes  chan string
	session *shell.Session
	cancel  context.CancelFunc
}

func installNewkeySessionSeam(t *testing.T) *newkeyUIDriver {
	t.Helper()
	d := &newkeyUIDriver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan string, 64)}
	orig := newAgeSetupSession
	newAgeSetupSession = func(ctx context.Context, cfg shell.Config) *shell.Session {
		progCtx, cancel := context.WithCancel(ctx)
		d.cancel = cancel
		d.session = shell.StartObservedForTest(progCtx, cfg, d.buf, func(title string) {
			d.pushes <- title
		})
		return d.session
	}
	t.Cleanup(func() {
		newAgeSetupSession = orig
		// Deterministically tear down the program this test created (if any): Close
		// quits it and BLOCKS until its event-loop/renderer goroutines exit, so a
		// leftover bubbletea program can never leak into and destabilize the next test.
		// This is the root fix for the flaky driver-test hangs (a stray program from an
		// earlier test intermittently stalled a later test's RunTask/waitScreen).
		if d.session != nil {
			_ = d.session.Close()
		}
		if d.cancel != nil {
			d.cancel()
		}
	})
	return d
}

func (d *newkeyUIDriver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(uitest.Deadline(60 * time.Second))
	for {
		select {
		case got := <-d.pushes:
			if got == title {
				return
			}
		case <-deadline:
			d.t.Fatalf("timed out waiting for screen %q; output tail:\n%s", title, tailStr(ansi.Strip(d.buf.String())))
		}
	}
}

func (d *newkeyUIDriver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

func (d *newkeyUIDriver) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.session.Send(shell.KeyMsg(string(r)))
	}
}

func tailStr(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[len(s)-2000:]
}

func TestRunNewKeyTUISavesRecipient(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "backup.env")
	driver := installNewkeySessionSeam(t)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runNewKeyTUI(context.Background(), configPath, baseDir, nil)
	}()

	const passphrase = "Str0ng-Passphrase!42"
	driver.waitScreen("AGE encryption setup")
	driver.keys("down enter") // generate from passphrase
	driver.waitScreen("Passphrase")
	driver.typeText(passphrase)
	driver.keys("enter")
	driver.waitScreen("Confirm passphrase")
	driver.typeText(passphrase)
	driver.keys("enter")
	driver.waitScreen("Add another recipient")
	driver.keys("enter") // Finish (default)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runNewKeyTUI error: %v", err)
		}
	case <-time.After(uitest.Deadline(60 * time.Second)):
		t.Fatal("newkey flow did not finish")
	}

	recipientPath := filepath.Join(baseDir, "identity", "age", "recipient.txt")
	data, err := os.ReadFile(recipientPath)
	if err != nil {
		t.Fatalf("recipient file not written: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(data)), "age1") {
		t.Fatalf("unexpected recipient content: %q", string(data))
	}
}

// TestRunNewKeyTUIProgramDeathMapsToAborted covers the ErrClosed branch: a
// UI program that terminates cleanly out from under the engine (interrupt)
// is reported as an interactive abort, not a hard failure.
func TestRunNewKeyTUIProgramDeathMapsToAborted(t *testing.T) {
	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "backup.env")
	driver := installNewkeySessionSeam(t)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runNewKeyTUI(context.Background(), configPath, baseDir, nil)
	}()

	driver.waitScreen("AGE encryption setup")
	driver.cancel() // kill the UI program out from under the engine

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected an error after UI death")
		}
		if !errors.Is(err, errInteractiveAborted) {
			t.Fatalf("expected errInteractiveAborted, got %v", err)
		}
	case <-time.After(uitest.Deadline(30 * time.Second)):
		t.Fatal("newkey flow did not return after UI death")
	}
}
