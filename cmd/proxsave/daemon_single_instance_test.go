package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/types"
)

// A second `proxsave --daemon` used to start with no probe at all: it overwrote
// .daemon.pid/.daemon_info.json with its own identity and, on exit, DELETED both,
// leaving the still-running unit daemon undiscoverable - standalone backup handoffs
// find no live daemon and outcomes go unpinged until a unit restart, and while both
// run the daily backup is double-scheduled. run() must therefore probe the recorded
// pid before publishing anything and refuse to start when a live proxsave --daemon
// already owns it, leaving the incumbent's files untouched.
func TestDaemonRefusesSecondInstanceAndLeavesTheIncumbentsFilesAlone(t *testing.T) {
	base := t.TempDir()
	const incumbent = 424242
	if err := health.WriteDaemonPID(base, incumbent); err != nil {
		t.Fatalf("seed incumbent pid: %v", err)
	}
	withProbe(t, func(pid int) bool { return pid == incumbent })

	d := &daemon{
		cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"},
		now: time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan int, 1)
	go func() { done <- d.run(ctx) }()
	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run() never returned")
	}

	if code != types.ExitBackupSkipped.Int() {
		t.Fatalf("second daemon exit = %d, want %d", code, types.ExitBackupSkipped.Int())
	}
	pid, err := health.ReadDaemonPID(base)
	if err != nil || pid != incumbent {
		t.Fatalf("the incumbent's pid file was touched: pid=%d err=%v, want %d intact", pid, err, incumbent)
	}
}

// A STALE pid file (recorded pid dead, or not a proxsave --daemon) must not block
// startup: kill -9 skips the removal defer and systemd's restart must still boot.
func TestDaemonStartsOverAStalePidFile(t *testing.T) {
	base := t.TempDir()
	if err := health.WriteDaemonPID(base, 424242); err != nil {
		t.Fatalf("seed stale pid: %v", err)
	}
	withProbe(t, func(pid int) bool { return false })

	d := &daemon{
		cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"},
		now: time.Now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan int, 1)
	go func() { done <- d.run(ctx) }()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("run() = %d over a stale pid file, want a normal start and clean stop", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run() never returned")
	}
}

func TestConcurrentDaemonCannotPassTheSingleInstanceGate(t *testing.T) {
	base := t.TempDir()
	first := &daemon{cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"}, now: time.Now}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstResult := make(chan int, 1)
	firstStopped := make(chan struct{})
	go func() {
		defer close(firstStopped)
		firstResult <- first.run(firstCtx)
	}()
	t.Cleanup(func() {
		cancelFirst()
		select {
		case <-firstStopped:
		case <-time.After(10 * time.Second):
			t.Error("first daemon did not stop")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		pid, err := health.ReadDaemonPID(base)
		if err == nil && pid == os.Getpid() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first daemon never published its pid: pid=%d err=%v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	second := &daemon{cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"}, now: time.Now}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	if code := second.run(secondCtx); code != types.ExitBackupSkipped.Int() {
		t.Fatalf("second daemon exit = %d, want %d", code, types.ExitBackupSkipped.Int())
	}
	pid, err := health.ReadDaemonPID(base)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("second daemon disturbed incumbent pid: pid=%d err=%v", pid, err)
	}

	cancelFirst()
	select {
	case code := <-firstResult:
		if code != types.ExitSuccess.Int() {
			t.Fatalf("first daemon exit = %d, want success", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first daemon did not return")
	}

	successor := &daemon{cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"}, now: time.Now}
	successorCtx, cancelSuccessor := context.WithCancel(context.Background())
	cancelSuccessor()
	if code := successor.run(successorCtx); code != types.ExitSuccess.Int() {
		t.Fatalf("successor daemon exit = %d after release, want success", code)
	}
}

func TestDaemonFailsClosedWhenOwnershipCannotBeEstablished(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base-file")
	if err := os.WriteFile(base, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create base file: %v", err)
	}
	d := &daemon{cfg: &config.Config{BaseDir: base, SchedulerTime: "03:00"}, now: time.Now}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if code := d.run(ctx); code != types.ExitGenericError.Int() {
		t.Fatalf("run() = %d when ownership is unknown, want %d", code, types.ExitGenericError.Int())
	}
}
