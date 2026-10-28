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

// "datastore update" refuses --backend and --backing-device (fixed once the datastore
// exists): the update of an S3 or removable datastore leaves them out and restores
// the other settings.
func TestPBSDatastoreUpdateLeavesOutTheCreateOnlySettings(t *testing.T) {
	got := withoutPBSDatastoreCreateOnlyFlags([]string{"--backend", "type=s3,client=e1,bucket=b", "--gc-schedule", "daily", "--backing-device=uuid-1", "--comment", "c"})
	if strings.Join(got, " ") != "--gc-schedule daily --comment c" {
		t.Fatalf("update flags = %q", got)
	}
}

// Clean mode moves a datastore whose path on this PBS differs from the backup's:
// remove, then create at the backup's path. With the backup's path on the root
// filesystem where its disk is not mounted, that create built an empty chunk store
// there and the move was reported applied; the guard now runs before the remove and
// the datastore stays where it is.
func TestApplyPBSDatastoreCfgViaAPI_DoesNotMoveWhereTheMountIsMissing(t *testing.T) {
	orig := pbsDatastoreMountMissingFn
	t.Cleanup(func() { pbsDatastoreMountMissingFn = orig })
	pbsDatastoreMountMissingFn = func(path string, _ *logging.Logger) bool { return path == "/mnt/datastore/ds1" }

	stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
	var logBuf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&logBuf)
	writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg", "datastore: ds1\n    path /mnt/datastore/ds1\n", 0o640)
	runner.outputs = map[string][]byte{pbsDatastoreBlockListCmd: []byte(`[{"name":"ds1","path":"/mnt/old/ds1"}]`)}

	_ = applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, true)
	for _, c := range runner.calls {
		if strings.Contains(c, "datastore remove") || strings.Contains(c, "datastore create") {
			t.Fatalf("datastore moved where its mount is missing: %v", runner.calls)
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
	if strings.Contains(logBuf.String(), "moved from") {
		t.Fatalf("the move was reported:\n%s", logBuf.String())
	}
}
