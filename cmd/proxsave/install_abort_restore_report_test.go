package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// An aborted install whose backup.env cannot be put back was reported as a clean abort
// (exit 0, the failure at DEBUG only), leaving backup.env as the install wrote it. It
// now shows an ERROR and exits as a configuration error.
func TestAbortedInstallReportsARestoreThatFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte("ENCRYPT_ARCHIVE=false\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	snap := takeInstallConfigSnapshot(path)

	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)
	var out strings.Builder
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(&out)
	bootstrap.SetMirrorLogger(mirror)

	if code := finishAbortedInstall(snap, bootstrap); code != types.ExitSuccess.Int() {
		t.Fatalf("restorable abort: exit %d, want %d; log:\n%s", code, types.ExitSuccess.Int(), out.String())
	}

	// backup.env replaced by a non-empty directory: the rename over it fails, as root too.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "held"), 0o700); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := finishAbortedInstall(snap, bootstrap); code != types.ExitConfigError.Int() {
		t.Fatalf("failed restore: exit %d, want %d; log:\n%s", code, types.ExitConfigError.Int(), out.String())
	}
	want := "ERROR"
	line := path + " not restored as it was before the install: "
	if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), line) {
		t.Fatalf("missing the ERROR line %q; log:\n%s", line, out.String())
	}
}
