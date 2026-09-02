package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// schemaAwarePvesh is a CommandRunner that answers the way the REAL pvesh answered on
// a live PVE 9.1.9 node (probed 2026-09-02; the strings below are verbatim from that
// session, recorded in diagnostics/design-staged-apply-pmxcfs-2026-09-02.md). The
// exact-key FakeCommandRunner can only echo whatever a test teaches it, which is how
// the staged-apply arms stayed green for months against endpoints that do not exist:
// this fake exists so every arm is exercised against the surface that actually
// rejected them, and every arm test from the fix series drives it instead of the
// exact-key fake.
//
// It deliberately stays permissive for the pvesh callers the fix does NOT touch
// (applyPVEClusterResourceMapping, applyPveshObject): anything it does not
// recognize succeeds, so their tests keep meaning what they meant.
type schemaAwarePvesh struct {
	// storages holds the ids that "exist"; create on an existing id fails the way
	// the live node failed on 'local', set on a missing id fails too.
	storages map[string]bool
	calls    []string
}

func newSchemaAwarePvesh(existingStorages ...string) *schemaAwarePvesh {
	s := &schemaAwarePvesh{storages: map[string]bool{}}
	for _, id := range existingStorages {
		s.storages[id] = true
	}
	return s
}

func (s *schemaAwarePvesh) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.calls = append(s.calls, commandKey(name, args))
	if name != "pvesh" {
		return nil, nil // which, etc.
	}
	if len(args) >= 2 && args[0] == "set" && args[1] == "/cluster/config" {
		return nil, errString("No 'set' handler defined for '/cluster/config'")
	}
	if len(args) >= 2 && args[0] == "set" && strings.Contains(args[1], "/qemu/") && strings.HasSuffix(args[1], "/config") {
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "--meta=") {
				return nil, errString("400 Parameter verification failed. meta: property is not defined in schema and the schema does not allow additional properties")
			}
		}
	}
	if len(args) >= 2 && args[0] == "create" && args[1] == "/storage" {
		id := pveshFakeArg(args, "--storage=")
		if s.storages[id] {
			return nil, errString("create storage failed: storage ID '" + id + "' already defined")
		}
		s.storages[id] = true
		return nil, nil
	}
	if len(args) >= 2 && args[0] == "set" && strings.HasPrefix(args[1], "/storage/") {
		id := strings.TrimPrefix(args[1], "/storage/")
		if !s.storages[id] {
			return nil, errString("no such storage '" + id + "'")
		}
		return nil, nil
	}
	return nil, nil
}

func pveshFakeArg(args []string, prefix string) string {
	for _, a := range args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix)
		}
	}
	return ""
}

type errString string

func (e errString) Error() string { return string(e) }

// The fake itself is pinned against the live session, so a future "fix" that
// loosens it cannot silently un-teach it the rejections.
func TestSchemaAwarePveshReproducesTheLiveRejections(t *testing.T) {
	f := newSchemaAwarePvesh("local")
	ctx := context.Background()

	_, err := f.Run(ctx, "pvesh", "set", "/cluster/config", "-conf", "/tmp/datacenter.cfg")
	if err == nil || !strings.Contains(err.Error(), "No 'set' handler defined for '/cluster/config'") {
		t.Fatalf("set /cluster/config: got %v", err)
	}

	_, err = f.Run(ctx, "pvesh", "set", "/nodes/pve/qemu/101/config", "--cores=2", "--meta=creation-qemu=10.1.2")
	if err == nil || !strings.Contains(err.Error(), "meta: property is not defined in schema") {
		t.Fatalf("qemu set with --meta: got %v", err)
	}

	_, err = f.Run(ctx, "pvesh", "create", "/storage", "--storage=local", "--type=dir", "--path=/var/lib/vz")
	if err == nil || !strings.Contains(err.Error(), "storage ID 'local' already defined") {
		t.Fatalf("duplicate storage create: got %v", err)
	}

	if _, err = f.Run(ctx, "pvesh", "set", "/storage/local", "--content=iso"); err != nil {
		t.Fatalf("set on existing storage must succeed (the live node has the handler): %v", err)
	}
	if _, err = f.Run(ctx, "pvesh", "create", "/storage", "--storage=nas", "--type=nfs"); err != nil {
		t.Fatalf("create of a new storage must succeed: %v", err)
	}
	if _, err = f.Run(ctx, "pvesh", "set", "/cluster/options", "--keyboard=it"); err != nil {
		t.Fatalf("set /cluster/options exists on the live node: %v", err)
	}
}
