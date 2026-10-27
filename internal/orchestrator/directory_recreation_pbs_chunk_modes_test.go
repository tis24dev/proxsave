package orchestrator

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A chunk store entry that cannot be stat'ed for a reason other than its absence
// leaves a DEBUG trace; a missing one (the .lock not yet created) is skipped
// silently. The stat failure is forced with a base directory that can be read but
// not searched, so it needs a user that permissions apply to.
func TestSetPBSChunkStoreModesTracesAStatFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission the stat failure relies on")
	}
	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, ".chunks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
	setPBSChunkStoreModes(base, logger)
	if !strings.Contains(buf.String(), "PBS datastore permissions: cannot stat "+filepath.Join(base, ".chunks")) {
		t.Fatalf("no DEBUG line for the .chunks stat failure:\n%s", buf.String())
	}

	buf.Reset()
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatal(err)
	}
	setPBSChunkStoreModes(base, logger)
	if strings.Contains(buf.String(), "cannot stat") {
		t.Fatalf("a missing .lock must be skipped silently:\n%s", buf.String())
	}
}
