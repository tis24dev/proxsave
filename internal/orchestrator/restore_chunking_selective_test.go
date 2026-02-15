package orchestrator

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestExtractArchiveNative_SelectiveRestoreReassemblesChunkedFiles(t *testing.T) {
	logger := logging.New(types.LogLevelError, false)

	origFS := restoreFS
	restoreFS = osFS{}
	t.Cleanup(func() { restoreFS = origFS })

	tmp := t.TempDir()
	archivePath := filepath.Join(tmp, "test.tar")

	data := []byte("hello world")
	sum := sha256.Sum256(data)
	meta := map[string]any{
		"version":            1,
		"size_bytes":         len(data),
		"chunk_size_bytes":   6,
		"chunk_count":        2,
		"sha256":             hex.EncodeToString(sum[:]),
		"mode":               0o640,
		"uid":                -1,
		"gid":                -1,
		"mod_time_unix_nano": time.Now().UnixNano(),
	}
	metaBytes, _ := json.Marshal(meta)

	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	tw := tar.NewWriter(f)
	addFile := func(name string, payload []byte) {
		h := &tar.Header{
			Name:     name,
			Typeflag: tar.TypeReg,
			Mode:     0o640,
			Size:     int64(len(payload)),
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("write header %s: %v", name, err)
		}
		if len(payload) > 0 {
			if _, err := tw.Write(payload); err != nil {
				t.Fatalf("write data %s: %v", name, err)
			}
		}
	}

	addFile("var/lib/pve-cluster/config.db-wal.chunked", metaBytes)
	addFile("chunked_files/var/lib/pve-cluster/config.db-wal.001.chunk", data[:6])
	addFile("chunked_files/var/lib/pve-cluster/config.db-wal.002.chunk", data[6:])
	addFile("etc/hosts", []byte("127.0.0.1 localhost\n"))

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close tar file: %v", err)
	}

	destRoot := filepath.Join(tmp, "out")
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		t.Fatalf("mkdir destRoot: %v", err)
	}

	cats := []Category{{
		ID:   "pve_cluster_dbwal",
		Name: "PVE Cluster DB WAL (test)",
		Paths: []string{
			"./var/lib/pve-cluster/config.db-wal",
		},
	}}

	if err := extractArchiveNative(context.Background(), archivePath, destRoot, logger, cats, RestoreModeCustom, nil, "", nil); err != nil {
		t.Fatalf("extractArchiveNative: %v", err)
	}

	originalPath := filepath.Join(destRoot, "var", "lib", "pve-cluster", "config.db-wal")
	got, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read reassembled file: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("reassembled content mismatch: got %q", string(got))
	}

	if _, err := os.Stat(originalPath + ".chunked"); !os.IsNotExist(err) {
		t.Fatalf("marker file should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "chunked_files")); !os.IsNotExist(err) {
		t.Fatalf("chunked_files dir should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(destRoot, "etc", "hosts")); !os.IsNotExist(err) {
		t.Fatalf("unrelated file should not be extracted in selective mode, stat err=%v", err)
	}
}
