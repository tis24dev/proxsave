package pbs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// PBS keeps a namespace at <ds>/ns/<name> and a child one at <ds>/ns/<name>/ns/<child>
// (measured on pve-test, PBS 4.2: namespaces other, r2, r2/child and r2/child/deep gave
// ns/other, ns/r2, ns/r2/ns/child and ns/r2/ns/child/ns/deep). The walk looked for
// vm/ct/host/namespace directly under the datastore and found none of them.
func TestDiscoverNamespacesReadsThePBSNamespaceTree(t *testing.T) {
	ds := t.TempDir()
	for _, d := range []string{"ns/other", "ns/r2/ns/child/ns/deep", "vm/100", ".chunks/0000"} {
		if err := os.MkdirAll(filepath.Join(ds, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, err := discoverNamespacesFromFilesystem(context.Background(), ds, 0)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := []Namespace{
		{Ns: "", Path: ds, Comment: "root namespace"},
		{Ns: "other", Path: filepath.Join(ds, "ns/other")},
		{Ns: "r2", Path: filepath.Join(ds, "ns/r2")},
		{Ns: "r2/child", Parent: "r2", Path: filepath.Join(ds, "ns/r2/ns/child")},
		{Ns: "r2/child/deep", Parent: "r2/child", Path: filepath.Join(ds, "ns/r2/ns/child/ns/deep")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("namespaces:\n%+v\nwant\n%+v", got, want)
	}
}

// PBS allows 7 levels below the root namespace; the walk stops there.
func TestDiscoverNamespacesStopsAtPBSMaxDepth(t *testing.T) {
	ds := t.TempDir()
	parts := []string{}
	for i := range 9 {
		parts = append(parts, "ns", string(rune('a'+i)))
	}
	if err := os.MkdirAll(filepath.Join(append([]string{ds}, parts...)...), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := discoverNamespacesFromFilesystem(context.Background(), ds, 0)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(got) != 1+7 || got[len(got)-1].Ns != strings.Join([]string{"a", "b", "c", "d", "e", "f", "g"}, "/") {
		t.Fatalf("got %d namespaces, last %q; want the root and 7 levels", len(got), got[len(got)-1].Ns)
	}
}
