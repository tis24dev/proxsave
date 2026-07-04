package agesetup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/charmbracelet/x/ansi"

	"github.com/tis24dev/proxsave/internal/orchestrator"
	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// Strong enough for ValidatePassphraseStrength (length + character classes).
const testPassphrase = "Str0ng-Passphrase!42"

type driver struct {
	t       *testing.T
	buf     *shell.SyncBuffer
	pushes  chan string
	session *shell.Session
}

func newDriver(t *testing.T) (*driver, *UI) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, buf: &shell.SyncBuffer{}, pushes: make(chan string, 64)}
	d.session = shell.StartObservedForTest(ctx, shell.Config{
		AppName:  "ProxSave",
		Subtitle: "AGE Encryption Setup",
	}, d.buf, func(title string) { d.pushes <- title })
	t.Cleanup(func() {
		_ = d.session.Close()
		cancel()
	})
	return d, New(d.session)
}

func (d *driver) waitScreen(title string) {
	d.t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-d.pushes:
			if got == title {
				return
			}
		case <-deadline:
			d.t.Fatalf("timed out waiting for screen %q; output tail:\n%s", title, tail(ansi.Strip(d.buf.String())))
		}
	}
}

func (d *driver) waitOutput(text string) {
	d.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if strings.Contains(ansi.Strip(d.buf.String()), text) {
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %q; output tail:\n%s", text, tail(ansi.Strip(d.buf.String())))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (d *driver) keys(script string) {
	d.t.Helper()
	for _, msg := range shell.Keys(script) {
		d.session.Send(msg)
	}
}

func (d *driver) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.session.Send(shell.KeyMsg(string(r)))
	}
}

func tail(s string) string {
	if len(s) <= 2000 {
		return s
	}
	return s[len(s)-2000:]
}

type draftResult struct {
	draft *orchestrator.AgeRecipientDraft
	err   error
}

func collect(ui *UI, ctx context.Context) <-chan draftResult {
	ch := make(chan draftResult, 1)
	go func() {
		draft, err := ui.CollectRecipientDraft(ctx, "/tmp/recipient.txt")
		ch <- draftResult{draft, err}
	}()
	return ch
}

func TestCollectRecipientDraftExistingKey(t *testing.T) {
	d, ui := newDriver(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	recipient := identity.Recipient().String()

	ch := collect(ui, context.Background())
	d.waitScreen("AGE encryption setup")
	d.keys("enter") // first option: existing public key
	d.waitScreen("AGE recipient")
	// Invalid value first: validation must block inline.
	d.typeText("not-a-key")
	d.keys("enter")
	d.waitOutput("✗")
	for range "not-a-key" {
		d.keys("backspace")
	}
	d.typeText(recipient)
	d.keys("enter")

	res := <-ch
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.draft.Kind != orchestrator.AgeRecipientInputExisting || res.draft.PublicKey != recipient {
		t.Fatalf("unexpected draft: %+v", res.draft)
	}
}

func TestCollectRecipientDraftPassphrase(t *testing.T) {
	d, ui := newDriver(t)

	ch := collect(ui, context.Background())
	d.waitScreen("AGE encryption setup")
	d.keys("down enter") // passphrase option
	d.waitScreen("Passphrase")
	d.typeText(testPassphrase)
	d.keys("enter")
	d.waitScreen("Confirm passphrase")
	// Mismatch first: inline error, prompt stays.
	d.typeText("wrong")
	d.keys("enter")
	d.waitOutput("do not match")
	for range "wrong" {
		d.keys("backspace")
	}
	d.typeText(testPassphrase)
	d.keys("enter")

	res := <-ch
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.draft.Kind != orchestrator.AgeRecipientInputPassphrase || res.draft.Passphrase != testPassphrase {
		t.Fatalf("unexpected draft: %+v", res.draft)
	}
}

func TestCollectRecipientDraftPrivateKey(t *testing.T) {
	d, ui := newDriver(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	ch := collect(ui, context.Background())
	d.waitScreen("AGE encryption setup")
	d.keys("down down enter") // private key option
	d.waitScreen("AGE private key")
	d.typeText(identity.String())
	d.keys("enter")

	res := <-ch
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.draft.Kind != orchestrator.AgeRecipientInputPrivateKey || res.draft.PrivateKey != identity.String() {
		t.Fatalf("unexpected draft: %+v", res.draft)
	}
}

func TestCollectRecipientDraftEscAtSelectorAborts(t *testing.T) {
	d, ui := newDriver(t)
	ch := collect(ui, context.Background())
	d.waitScreen("AGE encryption setup")
	d.keys("esc")
	res := <-ch
	if !errors.Is(res.err, orchestrator.ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected setup aborted, got %v", res.err)
	}
}

func TestCollectRecipientDraftEscAtInputGoesBack(t *testing.T) {
	d, ui := newDriver(t)
	ch := collect(ui, context.Background())
	d.waitScreen("AGE encryption setup")
	d.keys("enter")
	d.waitScreen("AGE recipient")
	d.keys("esc") // back to the method selector, not an abort
	d.waitScreen("AGE encryption setup")
	d.keys("esc")
	res := <-ch
	if !errors.Is(res.err, orchestrator.ErrAgeRecipientSetupAborted) {
		t.Fatalf("expected setup aborted after back+esc, got %v", res.err)
	}
}

func TestCollectRecipientDraftCtxCancelIsNotAbortSentinel(t *testing.T) {
	d, ui := newDriver(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch := collect(ui, ctx)
	d.waitScreen("AGE encryption setup")
	cancel()
	res := <-ch
	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", res.err)
	}
	if errors.Is(res.err, orchestrator.ErrAgeRecipientSetupAborted) {
		t.Fatal("ctx cancellation must not be converted into the abort sentinel")
	}
}

func TestConfirmOverwriteExistingRecipient(t *testing.T) {
	d, ui := newDriver(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			ok, err := ui.ConfirmOverwriteExistingRecipient(context.Background(), "/tmp/recipient.txt")
			resCh <- result{ok, err}
		}()
	}

	// Bare Enter must pick the safe Cancel default; y must be ignored
	// (destructive prompt).
	ask()
	d.waitScreen("Existing AGE recipient")
	d.waitOutput("/tmp/recipient.txt")
	d.keys("y")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("bare enter must cancel, got %+v", res)
	}

	// Deliberate navigation overwrites.
	ask()
	d.waitScreen("Existing AGE recipient")
	d.keys("left enter")
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("deliberate overwrite failed: %+v", res)
	}

	// Ctrl+C behaves like Cancel (tview parity).
	ask()
	d.waitScreen("Existing AGE recipient")
	d.keys("ctrl+c")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("ctrl+c must behave like cancel, got %+v", res)
	}
}

func TestConfirmAddAnotherRecipient(t *testing.T) {
	d, ui := newDriver(t)

	type result struct {
		ok  bool
		err error
	}
	resCh := make(chan result, 1)
	ask := func() {
		go func() {
			ok, err := ui.ConfirmAddAnotherRecipient(context.Background(), 2)
			resCh <- result{ok, err}
		}()
	}

	// Default is Finish.
	ask()
	d.waitScreen("Add another recipient")
	d.waitOutput("Recipient(s) added: 2")
	d.keys("enter")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("bare enter must finish, got %+v", res)
	}

	// Deliberate choice adds another.
	ask()
	d.waitScreen("Add another recipient")
	d.keys("left enter")
	if res := <-resCh; res.err != nil || !res.ok {
		t.Fatalf("add-another failed: %+v", res)
	}

	// Ctrl+C behaves like Finish.
	ask()
	d.waitScreen("Add another recipient")
	d.keys("ctrl+c")
	if res := <-resCh; res.err != nil || res.ok {
		t.Fatalf("ctrl+c must behave like finish, got %+v", res)
	}
}
