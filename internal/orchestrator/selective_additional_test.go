package orchestrator

import (
	"archive/tar"
	"bytes"
	"os"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestCollectArchivePaths(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	entries := []struct {
		name string
		data []byte
	}{
		{name: "one.txt", data: []byte("one")},
		{name: "dir/two.txt", data: []byte("two")},
	}

	for _, entry := range entries {
		hdr := &tar.Header{
			Name:     entry.name,
			Typeflag: tar.TypeReg,
			Mode:     0o600,
			Size:     int64(len(entry.data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", entry.name, err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatalf("Write(%q): %v", entry.name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close tar writer: %v", err)
	}

	tr := tar.NewReader(bytes.NewReader(buf.Bytes()))
	got := collectArchivePaths(tr)

	if len(got) != len(entries) {
		t.Fatalf("collectArchivePaths() len=%d; want %d (paths=%v)", len(got), len(entries), got)
	}
	for i, entry := range entries {
		if got[i] != entry.name {
			t.Fatalf("collectArchivePaths()[%d]=%q; want %q", i, got[i], entry.name)
		}
	}
}

func TestConfirmRestoreOperation(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)
	old := os.Stdin
	defer func() { os.Stdin = old }()

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"restore", "RESTORE\n", true},
		{"cancel", "cancel\n", false},
		{"zero", "0\n", false},
		{"invalid then restore", "nope\nRESTORE\n", true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("os.Pipe: %v", err)
			}
			if _, err := w.WriteString(tt.input); err != nil {
				_ = r.Close()
				_ = w.Close()
				t.Fatalf("WriteString: %v", err)
			}
			_ = w.Close()
			os.Stdin = r
			defer r.Close()

			got, err := ConfirmRestoreOperation(logger)
			if err != nil {
				t.Fatalf("ConfirmRestoreOperation returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ConfirmRestoreOperation() = %v; want %v", got, tt.want)
			}
		})
	}
}
