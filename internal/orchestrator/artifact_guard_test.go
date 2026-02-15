package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain guards against tests accidentally creating artifacts in the package
// directory (e.g. due to naive fake binaries interpreting flags as paths).
func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: failed to get wd:", err)
		os.Exit(1)
	}

	artifact := filepath.Join(wd, "--progress")

	// Clean up a stale artifact from a previous run (best-effort).
	_ = os.Remove(artifact)

	code := m.Run()

	if _, err := os.Stat(artifact); err == nil {
		fmt.Fprintln(os.Stderr, "ERROR: test left artifact:", artifact)
		_ = os.Remove(artifact)
		code = 1
	}

	os.Exit(code)
}
