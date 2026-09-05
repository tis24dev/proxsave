package orchestrator

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func nothingSettableLogger(t *testing.T) (*logging.Logger, *bytes.Buffer) {
	t.Helper()
	logger := logging.New(types.LogLevelDebug, false)
	buf := &bytes.Buffer{}
	logger.SetOutput(buf)
	return logger, buf
}

// The set fallback strips --storage and --type, so a staged block that carries
// nothing else leaves it with an empty argument list. `pvesh set /storage/<id>`
// with no option fails on the live node, and the block was counted as an apply
// FAILURE for a definition that already matched everything this restore could
// change. Nothing is sent at all now.
func TestSetFallbackSendsNothingWhenNoKeyIsSettable(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	pvesh := newSchemaAwarePvesh("local")
	restoreCmd = pvesh
	logger, _ := nothingSettableLogger(t)

	changed, err := pveshSetStorageDroppingCreateOnly(context.Background(), logger, "local", nil)
	if err != nil {
		t.Fatalf("an empty set is nothing to do, not a failure: %v", err)
	}
	if changed {
		t.Fatal("changed=true without sending a single key")
	}
	if len(pvesh.calls) != 0 {
		t.Fatalf("pvesh was called with no settable key: %v", pvesh.calls)
	}
}

// Two shapes reach the caller with a successful fallback that sent nothing: the
// block with no settable key at all, and the block whose every key came back
// refused as create-only. Announcing either as "Updated existing storage
// definition" claims a write that never happened.
func TestApplyStorageCfgDoesNotClaimAnUpdateItNeverSent(t *testing.T) {
	tests := []struct {
		name     string
		cfg      string
		id       string
		wantSets int
	}{
		{
			name:     "no settable key",
			cfg:      "dir: local\n",
			id:       "local",
			wantSets: 0,
		},
		{
			name:     "every key refused as create-only",
			cfg:      "dir: onlypath\n\tpath /var/lib/vz\n",
			id:       "onlypath",
			wantSets: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			origFS, origCmd := restoreFS, restoreCmd
			t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
			fakeFS := NewFakeFS()
			t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
			restoreFS = fakeFS
			pvesh := newSchemaAwarePvesh(tc.id)
			restoreCmd = pvesh

			if err := fakeFS.AddFile("/stage/etc/pve/storage.cfg", []byte(tc.cfg)); err != nil {
				t.Fatal(err)
			}
			logger, buf := nothingSettableLogger(t)

			applied, failed, err := applyStorageCfg(context.Background(), "/stage/etc/pve/storage.cfg", logger)
			if err != nil {
				t.Fatalf("applyStorageCfg: %v", err)
			}
			if applied != 1 || failed != 0 {
				t.Fatalf("applied=%d failed=%d, want 1/0: an already-matching definition is not a failure", applied, failed)
			}

			sets := strings.Count(strings.Join(pvesh.calls, "\n"), "set /storage/"+tc.id)
			if sets != tc.wantSets {
				t.Fatalf("set calls = %d, want %d; calls:\n%s", sets, tc.wantSets, strings.Join(pvesh.calls, "\n"))
			}

			out := buf.String()
			if !strings.Contains(out, "Storage definition "+tc.id+" already matches every settable key") {
				t.Fatalf("the outcome does not say nothing was sent:\n%s", out)
			}
			if strings.Contains(out, "Updated existing storage definition "+tc.id) {
				t.Fatalf("an update was announced without a single key reaching the node:\n%s", out)
			}
		})
	}
}

// The retry bound used to read the SHRINKING args slice, so it ran out of
// attempts before converging as soon as more than half the keys were refused.
// `nfs: id / server / export / content` reaches that: 3 settable keys, 2 of them
// create-only, 2 attempts allowed against the 3 needed. The storage was reported
// as "the set fallback did not converge" - a message that also replaced pvesh's
// real error - on a definition the retry would have applied.
func TestSetFallbackConvergesWhenMostKeysAreRefused(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	runner := &createOnlyRunner{refuse: map[string]bool{"server": true, "export": true, "path": true}}
	restoreCmd = runner
	logger, _ := nothingSettableLogger(t)

	changed, err := pveshSetStorageDroppingCreateOnly(context.Background(), logger, "nas",
		[]string{"--server=10.0.0.1", "--export=/srv/backups", "--content=backup"})
	if err != nil {
		t.Fatalf("the retry gave up before converging: %v", err)
	}
	if !changed {
		t.Fatal("changed=false, but --content was accepted and sent")
	}
	if runner.calls != 3 {
		t.Fatalf("pvesh calls = %d, want 3 (two drops plus the accepted set)", runner.calls)
	}
	if last := runner.lastArgs; strings.Join(last, " ") != "set /storage/nas --content=backup" {
		t.Fatalf("the surviving set was never sent; last call: %v", last)
	}
}

type createOnlyRunner struct {
	refuse   map[string]bool
	calls    int
	lastArgs []string
}

func (r *createOnlyRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls++
	r.lastArgs = append([]string(nil), args...)
	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			continue
		}
		key := strings.TrimPrefix(strings.SplitN(a, "=", 2)[0], "--")
		if r.refuse[key] {
			return []byte("Unknown option: " + key + "\n400 unable to parse option"), errString("exit status 255")
		}
	}
	return nil, nil
}
