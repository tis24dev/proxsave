package orchestrator

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A removable datastore (PBS 3.3+) has backing-device and a relative path, which PBS
// mounts at /mnt/datastore/<name> (measured on pve-test, PBS 4.2, backing-device
// written before path). Recreation refused it ("unsafe datastore path ... is not
// absolute") and the restore ended with warnings. It is left to PBS now: DEBUG when
// its device is connected, a WARNING when it is not.
func TestRecreationLeavesRemovableDatastoresToPBS(t *testing.T) {
	origFS, origCmd, origPath := restoreFS, restoreCmd, datastoreCfgPath
	t.Cleanup(func() { restoreFS, restoreCmd, datastoreCfgPath = origFS, origCmd, origPath })
	restoreFS = osFS{}
	cfg := filepath.Join(t.TempDir(), "datastore.cfg")
	const uuid = "f4214a7e-7156-4144-a005-c2ca81684cb7"
	if err := os.WriteFile(cfg, []byte("datastore: usb9\n\tbacking-device "+uuid+"\n\tpath usb9\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	datastoreCfgPath = cfg

	for _, connected := range []bool{false, true} {
		runner := &FakeCommandRunner{Outputs: map[string][]byte{}, Errors: map[string]error{}}
		if connected {
			runner.Outputs["blkid -U "+uuid] = []byte("/dev/sdb1\n")
		} else {
			runner.Errors["blkid -U "+uuid] = errors.New("exit status 2")
		}
		restoreCmd = runner
		var buf bytes.Buffer
		logger := logging.New(types.LogLevelInfo, false)
		logger.SetOutput(&buf)
		if err := RecreateDatastoreDirectories(logger); err != nil {
			t.Fatalf("connected=%v: RecreateDatastoreDirectories: %v", connected, err)
		}
		out := buf.String()
		if strings.Contains(out, "Failed to create datastore structure") {
			t.Fatalf("connected=%v: the removable datastore was refused:\n%s", connected, out)
		}
		warned := strings.Contains(out, "Datastore usb9: removable, device not connected")
		if warned == connected {
			t.Fatalf("connected=%v: warning shown=%v:\n%s", connected, warned, out)
		}
	}
}
