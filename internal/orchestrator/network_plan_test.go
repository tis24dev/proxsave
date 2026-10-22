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
