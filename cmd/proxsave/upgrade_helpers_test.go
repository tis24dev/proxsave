package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDownloadFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hello"))
		case "/fail":
			http.Error(w, "nope", http.StatusTeapot)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() { _ = root.Close() }()
	dest := filepath.Join(dir, "out.bin")

	if err := downloadFile(context.Background(), server.URL+"/ok", root, "out.bin", nil); err != nil {
		t.Fatalf("downloadFile(ok) error: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("downloaded content = %q, want %q", string(data), "hello")
	}

	if err := downloadFile(context.Background(), server.URL+"/fail", root, "fail.bin", nil); err == nil {
		t.Fatalf("expected downloadFile(fail) to return error")
	}
}

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() { _ = root.Close() }()
	filename := "test-archive.tar.gz"
	archivePath := filepath.Join(dir, filename)
	checksumPath := filepath.Join(dir, "SHA256SUMS")

	payload := []byte("archive-bytes")
	if err := os.WriteFile(archivePath, payload, 0o600); err != nil {
		t.Fatalf("WriteFile(archive): %v", err)
	}

	sum := sha256.Sum256(payload)
	sumHex := hex.EncodeToString(sum[:])
	if err := os.WriteFile(checksumPath, []byte(sumHex+"  "+filename+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(checksum): %v", err)
	}

	if err := verifyChecksum(root, "SHA256SUMS", filename, nil); err != nil {
		t.Fatalf("verifyChecksum() error: %v", err)
	}

	t.Run("missing entry", func(t *testing.T) {
		if err := verifyChecksum(root, "SHA256SUMS", "missing.tar.gz", nil); err == nil {
			t.Fatalf("expected error for missing checksum entry")
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		if err := os.WriteFile(checksumPath, []byte("deadbeef  "+filename+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(checksum mismatch): %v", err)
		}
		if err := verifyChecksum(root, "SHA256SUMS", filename, nil); err == nil {
			t.Fatalf("expected checksum mismatch error")
		}
	})

	t.Run("overlapping suffix entries", func(t *testing.T) {
		// A path-prefixed entry whose name merely ends with filename must be
		// ignored; only the exact-filename entry (with the correct hash) counts.
		wrong := "0000000000000000000000000000000000000000000000000000000000000000"
		content := wrong + "  artifacts/" + filename + "\n" + sumHex + "  " + filename + "\n"
		if err := os.WriteFile(checksumPath, []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(checksum overlap): %v", err)
		}
		if err := verifyChecksum(root, "SHA256SUMS", filename, nil); err != nil {
			t.Fatalf("expected exact filename match to win over a suffix match, got: %v", err)
		}
	})
}

func TestExtractBinaryFromTar(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() { _ = root.Close() }()
	archivePath := filepath.Join(dir, "bundle.tar.gz")
	destPath := filepath.Join(dir, "proxsave")

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	writeFile := func(name string, body []byte) {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(body)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}

	writeFile("other", []byte("x"))
	writeFile("proxsave", []byte("binary-bytes"))

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gzw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	if err := os.WriteFile(archivePath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile(archive): %v", err)
	}

	if err := extractBinaryFromTar(root, "bundle.tar.gz", "proxsave", "proxsave", nil); err != nil {
		t.Fatalf("extractBinaryFromTar() error: %v", err)
	}
	data, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("ReadFile(dest): %v", err)
	}
	if string(data) != "binary-bytes" {
		t.Fatalf("extracted content = %q, want %q", string(data), "binary-bytes")
	}

	if err := extractBinaryFromTar(root, "bundle.tar.gz", "missing", "missing", nil); err == nil {
		t.Fatalf("expected error when binary is missing from archive")
	}
}

func TestInstallBinary(t *testing.T) {
	dir := t.TempDir()
	srcRoot, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	defer func() { _ = srcRoot.Close() }()
	src := filepath.Join(dir, "src.bin")
	if err := os.WriteFile(src, []byte("bin"), 0o600); err != nil {
		t.Fatalf("WriteFile(src): %v", err)
	}

	dest := filepath.Join(dir, "nested", "proxsave")
	if err := installBinary(srcRoot, "src.bin", dest, nil); err != nil {
		t.Fatalf("installBinary() error: %v", err)
	}

	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile(dest): %v", err)
	}
	if string(data) != "bin" {
		t.Fatalf("installed content = %q, want %q", string(data), "bin")
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("Stat(dest): %v", err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("dest mode = %o, want %o", info.Mode().Perm(), 0o755)
		}
	}
}

// audited: 2026-06-09 — only linux/amd64 releases are published, so arm64 is no
// longer accepted (the old test treated arm64 as supported, documenting an arch the
// release pipeline never builds).
func TestDetectOSArch(t *testing.T) {
	osName, arch, err := detectOSArch()

	if runtime.GOOS != "linux" {
		if err == nil {
			t.Fatalf("expected error for unsupported OS %q, got os=%q arch=%q", runtime.GOOS, osName, arch)
		}
		return
	}

	if runtime.GOARCH != "amd64" {
		if err == nil {
			t.Fatalf("expected error for unsupported architecture %q, got os=%q arch=%q", runtime.GOARCH, osName, arch)
		}
		return
	}

	if err != nil {
		t.Fatalf("detectOSArch() error: %v", err)
	}
	if osName != "linux" {
		t.Fatalf("detectOSArch() os=%q, want %q", osName, "linux")
	}
	if arch != "amd64" {
		t.Fatalf("detectOSArch() arch=%q, want %q", arch, "amd64")
	}
}
