package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Regression for create-failure-undifferentiated (2026-06-09 audit):
// applyPVEClusterResourceMapping proceeded to the merge/set path after ANY create
// failure, discarding the create error and assuming "already exists". If create
// failed for another reason and the subsequent get also failed/returned empty, it
// still ran `set` with only the backup entries - overwriting a live mapping or
// masking the real cause. The set now runs only when the existing mapping is
// actually read; otherwise the original create error is surfaced.

type scriptedPveshRunner struct {
	createErr error
	getOut    []byte
	getErr    error
	setErr    error
	calls     []string
}

func (r *scriptedPveshRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
	if name != "pvesh" || len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "create":
		return []byte("create stderr"), r.createErr
	case "get":
		return r.getOut, r.getErr
	case "set":
		return nil, r.setErr
	}
	return nil, nil
}

func (r *scriptedPveshRunner) sawSet() bool {
	for _, c := range r.calls {
		if strings.HasPrefix(c, "pvesh set ") {
			return true
		}
	}
	return false
}

func withScriptedPvesh(t *testing.T, r *scriptedPveshRunner) {
	t.Helper()
	orig := restoreCmd
	restoreCmd = r
	t.Cleanup(func() { restoreCmd = orig })
}

func pciSpec() pveClusterMappingSpec {
	return pveClusterMappingSpec{ID: "device1", MapEntries: []string{"node=pve01,path=0000:01:00.0"}}
}

func TestApplyPVEClusterResourceMapping_CreateOkReturnsWithoutSet(t *testing.T) {
	r := &scriptedPveshRunner{createErr: nil}
	withScriptedPvesh(t, r)

	if err := applyPVEClusterResourceMapping(context.Background(), newTestLogger(), "pci", pciSpec()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.sawSet() {
		t.Errorf("a successful create must not fall through to set; calls=%v", r.calls)
	}
}

func TestApplyPVEClusterResourceMapping_CreateFailsAndGetUnreadable_SurfacesErrorNoSet(t *testing.T) {
	r := &scriptedPveshRunner{
		createErr: errors.New("exit status 2"), // e.g. permission denied / bad value
		getErr:    errors.New("exit status 2"), // existing mapping cannot be read
	}
	withScriptedPvesh(t, r)

	err := applyPVEClusterResourceMapping(context.Background(), newTestLogger(), "pci", pciSpec())
	if err == nil {
		t.Fatalf("expected an error when create failed and the existing mapping is unreadable")
	}
	if r.sawSet() {
		t.Errorf("must NOT run set when the existing mapping could not be read (blind overwrite); calls=%v", r.calls)
	}
}

func TestApplyPVEClusterResourceMapping_CreateFailsButMappingExists_MergesViaSet(t *testing.T) {
	r := &scriptedPveshRunner{
		createErr: errors.New("exit status 2"),
		// live mapping has a DIFFERENT node entry that must be preserved (unioned).
		getOut: []byte(`[{"id":"device1","map":["node=pve02,path=0000:09:00.0"]}]`),
	}
	withScriptedPvesh(t, r)

	if err := applyPVEClusterResourceMapping(context.Background(), newTestLogger(), "pci", pciSpec()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.sawSet() {
		t.Fatalf("expected a set call to merge entries; calls=%v", r.calls)
	}
	var setCall string
	for _, c := range r.calls {
		if strings.HasPrefix(c, "pvesh set ") {
			setCall = c
		}
	}
	// The set must union the live and backup entries (not overwrite with backup only).
	for _, want := range []string{"node=pve01,path=0000:01:00.0", "node=pve02,path=0000:09:00.0"} {
		if !strings.Contains(setCall, want) {
			t.Errorf("set call missing %q (entries must be unioned); set=%q", want, setCall)
		}
	}
}
