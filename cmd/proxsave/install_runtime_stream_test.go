package main

import (
	"context"
	"errors"
	"testing"

	"github.com/tis24dev/proxsave/internal/ui/shell"
)

func TestInstallRuntimeFinalizationRetainsBackendFailure(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	f.cron = false
	f.installErr = errors.New("start failed after enable")
	ctx := t.Context()
	var buf shell.SyncBuffer
	session := shell.StartForTestWithOutput(ctx, shell.Config{AppName: "ProxSave", Subtitle: "Install Wizard"}, &buf)
	defer session.Close()
	done := make(chan error, 1)
	postRan := false
	var backendErr error
	go func() {
		_, backendErr = runInstallFinalizationTask(ctx, session, f.p, func(context.Context) (string, string) { postRan = true; return "ok", "permissions OK" })
		done <- backendErr
	}()
	// The real stream may expose a failed task until Continue; drive that same control.
	err := pumpEnter(t, session, done)
	if !errors.Is(err, f.installErr) || !errors.Is(backendErr, f.installErr) {
		t.Fatalf("backend failure swallowed: returned=%v backend=%v", err, backendErr)
	}
	if postRan {
		t.Fatal("success finalization ran after backend failure")
	}
}

func TestInstallRuntimeClosedSessionDoesNotStartFinalization(t *testing.T) {
	f := newInstallRuntimeFixture(t)
	session := shell.StartForTest(context.Background(), shell.Config{AppName: "ProxSave"})
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	_, backendErr := runInstallFinalizationTask(context.Background(), session, f.p, func(context.Context) (string, string) {
		t.Error("post-finalization ran on closed session")
		return "", ""
	})
	if !errors.Is(backendErr, shell.ErrClosed) {
		t.Fatalf("closed session backend=%v", backendErr)
	}
	if len(f.events) != 0 {
		t.Fatalf("closed session mutated scheduler: %v", f.events)
	}
}
