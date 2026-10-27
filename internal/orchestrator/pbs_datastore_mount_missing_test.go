package orchestrator

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The package tests use datastore paths that sit on the build host's root filesystem;
// the guard is exercised on its own below.
func init() {
	pbsDatastoreMountMissingFn = func(string, *logging.Logger) bool { return false }
}

// A datastore this PBS does not list, whose path is on the root filesystem where its
// disk or pool should be mounted, is not created there: PBS would build an empty
// chunk store and the block would call it applied.
func TestApplyPBSDatastoreCfgViaAPI_DoesNotCreateWhereTheMountIsMissing(t *testing.T) {
	orig := pbsDatastoreMountMissingFn
	t.Cleanup(func() { pbsDatastoreMountMissingFn = orig })
	pbsDatastoreMountMissingFn = func(path string, _ *logging.Logger) bool { return path == "/mnt/datastore/ds1" }

	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg", "datastore: ds1\n    path /mnt/datastore/ds1\n", 0o640)
	runner.outputs = map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[]`)}

	_ = applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, false)
	for _, c := range runner.calls {
		if strings.Contains(c, "datastore create") {
			t.Fatalf("datastore created where its mount is missing: %v", runner.calls)
		}
	}
	for _, want := range []string{
		"INFO     Datastore ds1 is at /mnt/datastore/ds1, on the root filesystem: its disk is not mounted",
		"WARNING  ⚠ PBS datastore ds1: not applied",
	} {
		if !strings.Contains(logBuf.String(), want) {
			t.Fatalf("log lacks %q:\n%s", want, logBuf.String())
		}
	}
}
