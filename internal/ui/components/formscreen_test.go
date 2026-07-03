package components

import (
	"errors"
	"strings"
	"testing"

	"charm.land/huh/v2"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

// These tests are the Phase-0 spike proving huh v2 forms can be embedded as
// screens inside the shell router (focus, completion, and abort semantics).

func TestFormScreenCompletes(t *testing.T) {
	var name string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Name").Value(&name),
	))
	f := NewFormScreen("Setup", form)
	resolved := false
	var gotErr error
	f.Bind(func(_ struct{}, err error) {
		resolved = true
		gotErr = err
	})
	f.Init()
	for _, r := range "pve1" {
		pump(t, f, shell.KeyMsg(string(r)))
	}
	pump(t, f, shell.KeyMsg("enter"))
	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if !resolved {
		t.Fatal("form did not resolve on completion")
	}
	// The engine reads values through the caller-owned bindings, written
	// by the loop strictly before the resolve: the form pointer itself is
	// never handed back.
	if name != "pve1" {
		t.Fatalf("bound value = %q, want pve1", name)
	}
}

func TestFormScreenAborts(t *testing.T) {
	var name string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Name").Value(&name),
	))
	f := NewFormScreen("Setup", form)
	var gotErr error
	resolved := false
	f.Bind(func(_ struct{}, err error) {
		resolved = true
		gotErr = err
	})
	f.Init()
	press(t, f, "esc")
	if !resolved {
		t.Fatal("form did not resolve on abort")
	}
	if !errors.Is(gotErr, shell.ErrAborted) {
		t.Fatalf("expected ErrAborted, got %v", gotErr)
	}
}

func TestFormScreenEscBackSentinel(t *testing.T) {
	back := errors.New("back to previous step")
	var name string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("Name").Value(&name),
	))
	f := NewFormScreen("Setup", form, WithFormBack(back))
	var gotErr error
	f.Bind(func(_ struct{}, err error) { gotErr = err })
	f.Init()
	press(t, f, "esc")
	if !errors.Is(gotErr, back) {
		t.Fatalf("expected back sentinel, got %v", gotErr)
	}
}

func TestFormScreenViewRenders(t *testing.T) {
	var enabled bool
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title("Enable encryption?").Value(&enabled),
	))
	f := NewFormScreen("Encryption", form)
	f.Init()
	view := f.View(80, 24)
	if !strings.Contains(view, "Encryption") || !strings.Contains(view, "encryption?") {
		t.Errorf("form view incomplete: %q", view)
	}
}
