package orchestrator

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
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
	// guests maps vmid -> exists cluster-wide. guestNodes and guestKinds may
	// override the generated /cluster/resources record; running controls the
	// status/current response.
	guests     map[string]bool
	guestNodes map[string]string
	guestKinds map[string]string
	running    map[string]bool
	// failSet forces `set .../<vmid>/config` to fail regardless of args with a
	// 500: the API understood the payload and could not apply it. schemaRefuseSet
	// forces the OTHER shape, a 400 refusing the payload itself, which is the only
	// one the conf-file fallback answers. The two are separate because the fallback
	// now discriminates between them (pveshSchemaRefusal).
	failSet         map[string]bool
	schemaRefuseSet map[string]bool
	statusOutput    map[string][]byte
	statusError     map[string]error
	inventoryOutput []byte
	inventoryError  error
	calls           []string
}

func newSchemaAwarePvesh(existingStorages ...string) *schemaAwarePvesh {
	s := &schemaAwarePvesh{
		storages:     map[string]bool{},
		guests:       map[string]bool{},
		guestNodes:   map[string]string{},
		guestKinds:   map[string]string{},
		running:      map[string]bool{},
		statusOutput: map[string][]byte{},
		statusError:  map[string]error{},
	}
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
	if len(args) == 5 && args[0] == "get" && args[1] == "/cluster/resources" && args[2] == "--type" && args[3] == "vm" && args[4] == "--output-format=json" {
		if s.inventoryError != nil {
			return s.inventoryOutput, s.inventoryError
		}
		if s.inventoryOutput != nil {
			return s.inventoryOutput, nil
		}
		vmids := make([]string, 0, len(s.guests))
		for vmid, exists := range s.guests {
			if exists {
				vmids = append(vmids, vmid)
			}
		}
		sort.Strings(vmids)
		resources := make([]pveGuestResource, 0, len(vmids))
		for _, vmid := range vmids {
			numericVMID, err := strconv.Atoi(vmid)
			if err != nil {
				return nil, err
			}
			node := s.guestNodes[vmid]
			if node == "" {
				node = localNodeName()
			}
			kind := s.guestKinds[vmid]
			if kind == "" {
				kind = "qemu"
			}
			status := "stopped"
			if s.running[vmid] {
				status = "running"
			}
			resources = append(resources, pveGuestResource{
				VMID: numericVMID, Node: node, Kind: kind, Status: status, Name: "guest-" + vmid,
			})
		}
		return json.Marshal(resources)
	}
	if len(args) >= 2 && args[0] == "get" && strings.HasSuffix(args[1], "/status/current") {
		vmid := pveshFakePathVMID(strings.TrimSuffix(args[1], "/status/current"))
		if err, ok := s.statusError[vmid]; ok {
			return s.statusOutput[vmid], err
		}
		if out, ok := s.statusOutput[vmid]; ok {
			return out, nil
		}
		if !s.guests[vmid] {
			return []byte("Configuration file 'nodes/pve/lxc/" + vmid + ".conf' does not exist"), errString("exit status 2")
		}
		parts := strings.Split(strings.Trim(args[1], "/"), "/")
		node, kind := "pve", "qemu"
		if len(parts) >= 4 {
			node, kind = parts[1], parts[2]
		}
		status := "stopped"
		if s.running[vmid] {
			status = "running"
		}
		return []byte(`{"name":"guest-` + vmid + `","node":"` + node + `","status":"` + status + `","type":"` + kind + `","vmid":` + vmid + `}`), nil
	}
	if len(args) >= 2 && args[0] == "get" && strings.HasSuffix(args[1], "/config") && (strings.Contains(args[1], "/qemu/") || strings.Contains(args[1], "/lxc/")) {
		if !s.guests[pveshFakePathVMID(strings.TrimSuffix(args[1], "/config"))] {
			// Live shape (measured on PVE 9.1.9): the reason lands on the
			// command's OUTPUT, the Go error is a bare exit status. An earlier
			// fake put the reason inside the error and masked that
			// pveshGuestExists never matched it on a real node.
			return []byte("Configuration file 'nodes/pve/lxc/990.conf' does not exist"), errString("exit status 2")
		}
		return []byte("{}"), nil
	}
	if len(args) >= 2 && args[0] == "set" && args[1] == "/cluster/config" {
		return nil, errString("No 'set' handler defined for '/cluster/config'")
	}
	if len(args) >= 2 && args[0] == "set" && strings.HasSuffix(args[1], "/config") {
		vmid := pveshFakePathVMID(strings.TrimSuffix(args[1], "/config"))
		if s.failSet[vmid] {
			return nil, errString("500 unable to apply configuration")
		}
		if s.schemaRefuseSet[vmid] {
			// The live 400 shape for a key the update schema does not carry, the
			// same family as the --meta rejection below.
			return []byte("400 Parameter verification failed. balloon: property is not defined in schema and the schema does not allow additional properties"),
				errString("exit status 255")
		}
	}
	if len(args) >= 2 && args[0] == "set" && strings.Contains(args[1], "/qemu/") && strings.HasSuffix(args[1], "/config") {
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "--meta=") {
				return nil, errString("400 Parameter verification failed. meta: property is not defined in schema and the schema does not allow additional properties")
			}
		}
	}
	if len(args) >= 2 && args[0] == "set" && strings.Contains(args[1], "/lxc/") && strings.HasSuffix(args[1], "/config") {
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "--ostemplate=") {
				return nil, errString("400 Parameter verification failed. ostemplate: property is not defined in schema and the schema does not allow additional properties")
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
		// Live shape (measured on PVE 9.1.9): `path` is create-only for storage
		// too - "Unknown option: path" on the OUTPUT, bare exit status as the
		// error. Any --path in a set is refused.
		for _, a := range args[2:] {
			if strings.HasPrefix(a, "--path=") {
				return []byte("Unknown option: path\n400 unable to parse option"), errString("exit status 255")
			}
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

func pveshFakePathVMID(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}
