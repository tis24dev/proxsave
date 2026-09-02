package main

import (
	"context"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
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

	if code == 0 {
		t.Fatal("a second daemon instance exited 0: it ran instead of refusing")
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
