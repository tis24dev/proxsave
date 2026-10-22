package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// pbsChunkStoreExists is the refusal proxmox-backup-manager 4.2.8 printed on VM 201
// (2026-10-18) for "datastore create ds1 /srv/ds1" when /srv/ds1 already held the
// chunk store of a datastore, here at the path p.
func pbsChunkStoreExists(p string) string {
	return `exit status 255: Error: task failed (status unable to create chunk store 'ds1' subdir "` + p + `/.chunks" - unable to create directory "` + p + `/.chunks" - EEXIST: File exists)`
}

// A create PBS refuses because the path already holds a chunk store is retried
// with --reuse-datastore true, which PBS 4.2.8 needs to configure a datastore over
// its existing data: for a datastore this PBS does not list, for the backup's path
// of a move, and for the rollback of a move to the path on this PBS.
func TestApplyPBSDatastoreCfgViaAPI_ReusesAnExistingChunkStore(t *testing.T) {
	const (
		head1 = "INFO     Restoring PBS datastore configuration..."
		count = "INFO       Datastores to restore: 1"
		names = "INFO       Names: ds1"
		ds1   = "datastore: ds1\n    path /new\n    comment c1\n"
	)
	cases := []struct {
		name       string
		strict     bool
		rows       string
		errs       map[string]error
		wantLines  []string
		wantCalls  []string
		notApplied bool
	}{
		{
			name: "create-over-existing-data",
			rows: `[]`,
			errs: map[string]error{"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New(pbsChunkStoreExists("/new"))},
			wantLines: []string{head1, count, names,
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1 --reuse-datastore true",
			},
		},
		{
			name: "reuse-refused-keeps-the-create-refusal",
			rows: `[]`,
			errs: map[string]error{
				"proxmox-backup-manager datastore create ds1 /new --comment c1":                        errors.New(pbsChunkStoreExists("/new")),
				"proxmox-backup-manager datastore create ds1 /new --comment c1 --reuse-datastore true": errors.New("exit status 255: Error: refused"),
				"proxmox-backup-manager datastore update ds1 --comment c1":                             errors.New("exit status 255: Error: no such datastore 'ds1'"),
			},
			wantLines: []string{head1, count, names,
				`INFO     Datastore ds1 was rejected by PBS: "task failed (status unable to create chunk store 'ds1' subdir "/new/.chunks" - unable to create directory "/new/.chunks" - EEXIST: File exists)"`,
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1 --reuse-datastore true",
				"proxmox-backup-manager datastore update ds1 --comment c1",
			},
			notApplied: true,
		},
		{
			name:   "move-onto-existing-data",
			strict: true,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs:   map[string]error{"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New(pbsChunkStoreExists("/new"))},
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 moved from /old to /new, the data in /old is not moved",
				"INFO     ✓ PBS datastore ds1: applied",
				"INFO     ✓ PBS datastore configuration: applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore remove ds1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1 --reuse-datastore true",
			},
		},
		{
			name:   "move-rollback-over-the-kept-data",
			strict: true,
			rows:   `[{"name":"ds1","path":"/old"}]`,
			errs: map[string]error{
				"proxmox-backup-manager datastore create ds1 /new --comment c1": errors.New("exit status 255: Error: refused"),
				"proxmox-backup-manager datastore create ds1 /old --comment c1": errors.New(pbsChunkStoreExists("/old")),
			},
			wantLines: []string{head1, count, names,
				"INFO     Datastore ds1 is at /old on this PBS and at /new in the backup",
				"WARNING  ⚠ PBS datastore ds1: not applied",
				"WARNING  ⚠ PBS datastore configuration: not applied",
			},
			wantCalls: []string{pbsDatastoreBlockListCmd,
				"proxmox-backup-manager datastore remove ds1",
				"proxmox-backup-manager datastore create ds1 /new --comment c1",
				"proxmox-backup-manager datastore create ds1 /old --comment c1",
				"proxmox-backup-manager datastore create ds1 /old --comment c1 --reuse-datastore true",
			},
			notApplied: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stageRoot, fs, runner := setupPBSAPIApplyTestDeps(t)
			logger := logging.New(types.LogLevelDebug, false)
			var logBuf bytes.Buffer
			logger.SetOutput(&logBuf)
			writeStageFile(t, fs, stageRoot, "etc/proxmox-backup/datastore.cfg", ds1, 0o640)
			runner.outputs = map[string][]byte{pbsDatastoreBlockListCmd: []byte(tc.rows)}
			runner.errs = tc.errs

			err := applyPBSDatastoreCfgViaAPI(context.Background(), logger, stageRoot, tc.strict)
			t.Logf("full log:\n%s", logBuf.String())

			if got := pbsVisibleLines(logBuf.String()); strings.Join(got, "\n") != strings.Join(tc.wantLines, "\n") {
				t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(tc.wantLines, "\n"))
			}
			if strings.Join(runner.calls, "\n") != strings.Join(tc.wantCalls, "\n") {
				t.Fatalf("calls\n--- got ---\n%s\n--- want ---\n%s", strings.Join(runner.calls, "\n"), strings.Join(tc.wantCalls, "\n"))
			}
			var incomplete *pbsDatastoreRestoreIncompleteError
			if got := errors.As(err, &incomplete); got != tc.notApplied {
				t.Fatalf("err = %v; incomplete %v, want %v", err, got, tc.notApplied)
			}
		})
	}
}

// The rollback of a move cancelled after its remove reuses the data kept at the
// path on this PBS, still without the cancellation, then the block stops.
func TestApplyPBSDatastoreCfgViaAPI_CancelledMoveRollbackReusesTheKeptData(t *testing.T) {
	runner := &pbsCtxRunner{
		cancelOn: "proxmox-backup-manager datastore create ds1 /mnt/ds1",
		list:     `[{"name":"ds1","path":"/mnt/a"}]`,
		refused:  map[string]error{"proxmox-backup-manager datastore create ds1 /mnt/a": errors.New(pbsChunkStoreExists("/mnt/a"))},
	}
	log, err := runPBSDatastoreCancelStep(t, PBSRestoreBehaviorClean, "datastore: ds1\n\tpath /mnt/ds1\n", runner)
	t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
	if !restoreAbortOrInput(err) {
		t.Fatalf("step error = %v; want an abort", err)
	}
	want := []string{
		"INFO     Restoring PBS datastore configuration...",
		"INFO       Datastores to restore: 1",
		"INFO       Names: ds1",
	}
	if got := pbsVisibleLines(log); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("visible lines\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	calls := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore remove ds1",
		"proxmox-backup-manager datastore create ds1 /mnt/ds1",
		"proxmox-backup-manager datastore create ds1 /mnt/a",
		"proxmox-backup-manager datastore create ds1 /mnt/a --reuse-datastore true",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(calls, "\n") {
		t.Fatalf("calls\n--- got ---\n%s\n--- want ---\n%s", strings.Join(runner.calls, "\n"), strings.Join(calls, "\n"))
	}
}

// A create refused for an existing chunk store just as the operator cancels is not
// retried: the block stops with the cancellation.
func TestApplyPBSDatastoreCfgViaAPI_NoReuseRetryAfterCancellation(t *testing.T) {
	runner := &pbsCtxRunner{
		cancelOn:  "proxmox-backup-manager datastore create ds1 /mnt/ds1",
		cancelErr: errors.New(pbsChunkStoreExists("/mnt/ds1")),
		list:      `[]`,
	}
	log, err := runPBSDatastoreCancelStep(t, PBSRestoreBehaviorClean, "datastore: ds1\n\tpath /mnt/ds1\n", runner)
	t.Logf("full log:\n%s", strings.Join(pbsBlockLogLines(log), "\n"))
	if !restoreAbortOrInput(err) {
		t.Fatalf("step error = %v; want an abort", err)
	}
	calls := []string{
		"proxmox-backup-manager datastore list --output-format=json",
		"proxmox-backup-manager datastore create ds1 /mnt/ds1",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(calls, "\n") {
		t.Fatalf("calls\n--- got ---\n%s\n--- want ---\n%s", strings.Join(runner.calls, "\n"), strings.Join(calls, "\n"))
	}
}
