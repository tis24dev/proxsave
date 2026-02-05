package orchestrator

import (
	"os"
	"testing"
)

func TestExtractIPFromSnapshotPrefersIPv4WithCIDR(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := `
GeneratedAt: 2026-02-05T16:41:49Z
Label: before

=== LIVE NETWORK STATE ===

$ ip -br addr
vmbr0             UP             192.168.178.146/24 2a01:db8::1/64
lo                UNKNOWN        127.0.0.1/8 ::1/128

$ ip route show
default via 192.168.178.1 dev vmbr0
`
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	got := extractIPFromSnapshot("/snap.txt", "vmbr0")
	if got != "192.168.178.146/24" {
		t.Fatalf("got %q want %q", got, "192.168.178.146/24")
	}
}

func TestExtractIPFromSnapshotFallsBackToIPv6WithCIDR(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	snapshot := `
$ ip -br addr
vmbr0 UP 2a01:db8::2/64
`
	if err := fakeFS.WriteFile("/snap.txt", []byte(snapshot), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	got := extractIPFromSnapshot("/snap.txt", "vmbr0")
	if got != "2a01:db8::2/64" {
		t.Fatalf("got %q want %q", got, "2a01:db8::2/64")
	}
}

func TestExtractIPFromSnapshotReturnsUnknownWhenMissing(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	got := extractIPFromSnapshot("/does-not-exist.txt", "vmbr0")
	if got != "unknown" {
		t.Fatalf("got %q want %q", got, "unknown")
	}
}
