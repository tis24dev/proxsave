package main

import (
	"testing"
	"time"
)

// A script that fails is dropped by contract, and the wait for it ends when it exits:
// neither its exit status nor the timeout may keep the scheduler parked.
func TestPersonalScriptWaitEndsWhenAFailingScriptExits(t *testing.T) {
	origTimeout := personalScriptTimeout
	t.Cleanup(func() { personalScriptTimeout = origTimeout })
	personalScriptTimeout = 5 * time.Second

	script := writePersonalScript(t, t.TempDir(), "fails.sh", "exit 3")

	start := time.Now()
	if err := runPersonalScript(script, nil); err != nil {
		t.Fatalf("runPersonalScript() = %v, want nil: a script's own failure is not reported", err)
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("returned after %s: the wait did not end when the script exited", elapsed)
	}
}
