package orchestrator

import (
	"archive/tar"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Bug 1 (Batch-B follow-up): a .tar.xz backup whose resolv_conf.txt is NOT the
// last entry must be read successfully. Before the fix, readTarEntry stopped
// reading at the found entry, the piped xz died on SIGPIPE, and its close error
// was returned in place of the valid resolv.conf bytes, so the caller silently
// discarded the backup's DNS and wrote the static gateway fallback instead.
func TestReadTarEntry_XZ_EntryNotLast_ReturnsContent(t *testing.T) {
	if _, err := exec.LookPath("xz"); err != nil {
		t.Skip("xz not installed")
	}
	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	dir := t.TempDir()
	plain := filepath.Join(dir, "b.tar")
	f, err := os.Create(plain)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	want := []byte("nameserver 10.1.3.1\nnameserver 1.1.1.1\n")
	write := func(name string, content []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	write("var/lib/proxsave-info/commands/system/resolv_conf.txt", want)
	pad := []byte(strings.Repeat("x", 65536))
	for i := 0; i < 200; i++ {
		write("padding/f"+strings.Repeat("0", i%5)+string(rune('a'+i%26))+".dat", pad)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	xzPath := filepath.Join(dir, "backup.tar.xz")
	out, err := os.Create(xzPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("xz", "-z", "-c", plain)
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		t.Fatalf("xz: %v", err)
	}
	_ = out.Close()

	got, err := readTarEntry(context.Background(), xzPath, "var/lib/proxsave-info/commands/system/resolv_conf.txt", maxResolvConfSize)
	if err != nil {
		t.Fatalf("readTarEntry must succeed on .tar.xz, got err=%v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got %q want %q", got, want)
	}
}
