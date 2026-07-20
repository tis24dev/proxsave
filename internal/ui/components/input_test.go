package components

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func bindInput(i *Input) *struct {
	resolved bool
	value    string
	err      error
} {
	cap := &struct {
		resolved bool
		value    string
		err      error
	}{}
	i.Bind(func(v string, err error) {
		cap.resolved = true
		cap.value = v
		cap.err = err
	})
	return cap
}

func typeString(t *testing.T, i *Input, s string) {
	t.Helper()
	for _, r := range s {
		press(t, i, string(r))
	}
}

func TestInputTypeAndSubmit(t *testing.T) {
	i := NewInput("Destination", "Enter the destination directory")
	i.Init()
	cap := bindInput(i)
	typeString(t, i, "/tmp/out")
	press(t, i, "enter")
	if !cap.resolved || cap.err != nil || cap.value != "/tmp/out" {
		t.Fatalf("expected /tmp/out, got %+v", cap)
	}
}

func TestInputInitialValue(t *testing.T) {
	i := NewInput("Destination", "Directory", WithInitialValue("/var/backup"))
	i.Init()
	cap := bindInput(i)
	press(t, i, "enter")
	if cap.value != "/var/backup" {
		t.Fatalf("expected prefilled value, got %q", cap.value)
	}
}

func TestInputEscAborts(t *testing.T) {
	i := NewInput("Destination", "Directory")
	i.Init()
	cap := bindInput(i)
	press(t, i, "esc")
	if !cap.resolved || !errors.Is(cap.err, shell.ErrAborted) {
		t.Fatalf("expected ErrAborted, got %+v", cap)
	}
}

func TestInputValidationBlocksSubmit(t *testing.T) {
	i := NewInput("Destination", "Directory", WithValidate(func(v string) error {
		if !strings.HasPrefix(v, "/") {
			return fmt.Errorf("path must be absolute")
		}
		return nil
	}))
	i.Init()
	cap := bindInput(i)
	typeString(t, i, "relative")
	press(t, i, "enter")
	if cap.resolved {
		t.Fatal("invalid value must not resolve")
	}
	if !strings.Contains(i.View(80, 20), "path must be absolute") {
		t.Error("validation error must be shown inline")
	}
	// Fix the value: clears on next submit attempt.
	for range "relative" {
		press(t, i, "backspace")
	}
	typeString(t, i, "/ok")
	press(t, i, "enter")
	if !cap.resolved || cap.value != "/ok" {
		t.Fatalf("expected /ok after fixing, got %+v", cap)
	}
}

func TestInputErrorTextPreseeded(t *testing.T) {
	i := NewInput("Passphrase", "Enter passphrase", WithErrorText("previous attempt failed"))
	i.Init()
	if !strings.Contains(i.View(80, 20), "previous attempt failed") {
		t.Error("pre-seeded error text must be shown")
	}
}

func TestInputEscBackSentinel(t *testing.T) {
	back := errors.New("back to selection")
	i := NewInput("Destination", "Directory", WithInputBack(back))
	i.Init()
	cap := bindInput(i)
	press(t, i, "esc")
	if !cap.resolved || !errors.Is(cap.err, back) {
		t.Fatalf("expected back sentinel, got %+v", cap)
	}
}

func TestInputSecretMasksValue(t *testing.T) {
	i := NewInput("Passphrase", "Enter passphrase", WithSecret())
	i.Init()
	typeString(t, i, "hunter2")
	view := i.View(80, 20)
	if strings.Contains(view, "hunter2") {
		t.Error("secret input must not render the plaintext value")
	}
	cap := bindInput(i)
	press(t, i, "enter")
	if cap.value != "hunter2" {
		t.Fatalf("secret input must still resolve the real value, got %q", cap.value)
	}
}
