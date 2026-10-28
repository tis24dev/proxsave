package orchestrator

import "testing"

// `pvesh get /cluster/mapping/pci/<id>` prints the mapping without its id (measured on
// PVE 9.1.9). The existing-mapping path read that as unreadable, so a mapping that
// already existed was never merged ("the existing mapping could not be read").
func TestParsePVEClusterMappingObjectWithoutID(t *testing.T) {
	measured := []byte(`{"description":"probe desc","digest":"e747a3a6031f1400fadfa792d11f85bbad63763d","live-migration-capable":1,"map":["node=pve,path=0000:00:00.0,id=8086:7190"],"mdev":1,"type":"pci"}`)
	spec, ok, err := parsePVEClusterMappingObject(measured, "probe8")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want the mapping read", ok, err)
	}
	if spec.ID != "probe8" || len(spec.MapEntries) != 1 || spec.Description != "probe desc" {
		t.Fatalf("spec = %+v", spec)
	}
}
