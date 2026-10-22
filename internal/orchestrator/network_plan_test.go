package orchestrator

import (
	"slices"
	"testing"
)

// The ifupdown config paths come back sorted: the files are read in that order, and the
// first gateway found wins.
func TestCollectIfupdownConfigPathsSorted(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	fake := NewFakeFS()
	restoreFS = fake
	for _, name := range []string{"zz-vlan", "10-bond"} {
		if err := fake.AddFile("/etc/network/interfaces.d/"+name, []byte("\n")); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	paths, err := collectIfupdownConfigPaths()
	if err != nil {
		t.Fatalf("collectIfupdownConfigPaths: %v", err)
	}
	want := []string{"/etc/network/interfaces", "/etc/network/interfaces.d/10-bond", "/etc/network/interfaces.d/zz-vlan"}
	if !slices.Equal(paths, want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
}

// The configured addresses of an interface come back sorted, whatever file they were
// found in.
func TestTargetNetworkEndpointFromConfigSortsAddresses(t *testing.T) {
	orig := restoreFS
	t.Cleanup(func() { restoreFS = orig })
	fake := NewFakeFS()
	restoreFS = fake
	if err := fake.AddFile("/etc/network/interfaces", []byte("iface vmbr0 inet static\n    address 192.168.1.20/24\n")); err != nil {
		t.Fatalf("add interfaces: %v", err)
	}
	if err := fake.AddFile("/etc/network/interfaces.d/extra", []byte("iface vmbr0 inet static\n    address 10.0.0.5/24\n")); err != nil {
		t.Fatalf("add interfaces.d/extra: %v", err)
	}

	ep, err := targetNetworkEndpointFromConfig("vmbr0")
	if err != nil {
		t.Fatalf("targetNetworkEndpointFromConfig: %v", err)
	}
	if want := []string{"10.0.0.5/24", "192.168.1.20/24"}; !slices.Equal(ep.Addresses, want) {
		t.Fatalf("addresses = %v, want %v", ep.Addresses, want)
	}
}
