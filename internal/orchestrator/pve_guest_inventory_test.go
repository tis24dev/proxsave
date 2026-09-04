package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const pveGuestInventoryCommand = "pvesh get /cluster/resources --type vm --output-format=json"

func guestInventoryJSON(t *testing.T, resources ...pveGuestResource) []byte {
	t.Helper()
	if resources == nil {
		resources = []pveGuestResource{}
	}
	out, err := json.Marshal(resources)
	if err != nil {
		t.Fatalf("marshal guest inventory: %v", err)
	}
	return out
}

func TestLoadPVEGuestInventoryParsesCompleteResources(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })
	restoreCmd = &FakeCommandRunner{Outputs: map[string][]byte{
		pveGuestInventoryCommand: []byte(`[
			{"vmid":100,"node":"pve-a","type":"qemu","status":"running","name":"vm100"},
			{"vmid":101,"node":"pve-b","type":"lxc","status":"stopped","name":"ct101"}
		]`),
	}}

	got, err := loadPVEGuestInventory(context.Background())
	if err != nil {
		t.Fatalf("loadPVEGuestInventory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("inventory size=%d, want 2", len(got))
	}
	if guest := got["100"]; guest.Node != "pve-a" || guest.Kind != "qemu" || guest.Status != "running" {
		t.Fatalf("guest 100=%+v", guest)
	}
	if guest := got["101"]; guest.Node != "pve-b" || guest.Kind != "lxc" || guest.Status != "stopped" {
		t.Fatalf("guest 101=%+v", guest)
	}
}

func TestLoadPVEGuestInventoryRejectsUnsafeResponses(t *testing.T) {
	tests := []struct {
		name   string
		output string
		marker string
	}{
		{name: "malformed JSON", output: `{"vmid":`, marker: "parse"},
		{name: "non-array JSON", output: `{"vmid":100}`, marker: "array"},
		{name: "null JSON", output: `null`, marker: "array"},
		{name: "missing node", output: `[{"vmid":100,"type":"qemu"}]`, marker: "node"},
		{name: "missing kind", output: `[{"vmid":100,"node":"pve-a"}]`, marker: "type"},
		{name: "invalid kind", output: `[{"vmid":100,"node":"pve-a","type":"storage"}]`, marker: "type"},
		{name: "duplicate VMID", output: `[{"vmid":100,"node":"pve-a","type":"qemu"},{"vmid":100,"node":"pve-b","type":"qemu"}]`, marker: "duplicate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origCmd := restoreCmd
			t.Cleanup(func() { restoreCmd = origCmd })
			restoreCmd = &FakeCommandRunner{Outputs: map[string][]byte{
				pveGuestInventoryCommand: []byte(tt.output),
			}}

			_, err := loadPVEGuestInventory(context.Background())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.marker) {
				t.Fatalf("error=%v, want marker %q", err, tt.marker)
			}
		})
	}
}
