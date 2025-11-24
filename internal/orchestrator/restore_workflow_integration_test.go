package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/logging"
)

func TestStopPVEClusterServices_Success(t *testing.T) {
	orig := restoreCmd
	defer func() { restoreCmd = orig }()

	fake := &FakeCommandRunner{
		Outputs: map[string][]byte{
			"systemctl start pve-cluster": {},
			"systemctl start pvedaemon":   {},
			"systemctl start pveproxy":    {},
			"systemctl start pvestatd":    {},
		},
	}
	restoreCmd = fake

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	if err := startPVEClusterServices(context.Background(), logger); err != nil {
		t.Fatalf("startPVEClusterServices: %v", err)
	}
	if len(fake.Calls) != 4 {
		t.Fatalf("expected 4 systemctl calls, got %d", len(fake.Calls))
	}
}

func TestExtractPlainArchive_CorruptedTar(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.tar.gz")
	// Write invalid gzip content
	if err := os.WriteFile(archive, []byte("not a gzip"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	err := extractPlainArchive(context.Background(), archive, filepath.Join(dir, "dest"), logger)
	if err == nil {
		t.Fatalf("expected error for corrupted tar.gz")
	}
}

// Helper to create a reader for prompt-driven functions.
func fakeReader(inputs ...string) *bufio.Reader {
	return bufio.NewReader(bytes.NewBufferString(strings.Join(inputs, "\n") + "\n"))
}
