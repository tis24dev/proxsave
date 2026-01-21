package orchestrator

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
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
	origFS := restoreFS
	restoreFS = osFS{}
	defer func() { restoreFS = origFS }()

	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	dir := t.TempDir()
	archive := filepath.Join(dir, "bad.tar.gz")
	// Write invalid gzip content
	if err := os.WriteFile(archive, []byte("not a gzip"), 0o640); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	err := extractPlainArchive(context.Background(), archive, filepath.Join(dir, "dest"), logger, nil)
	if err == nil {
		t.Fatalf("expected error for corrupted tar.gz")
	}
}

func TestRunSafeClusterApply_PveshNotFound(t *testing.T) {
	logger := logging.New(logging.GetDefaultLogger().GetLevel(), false)
	reader := bufio.NewReader(strings.NewReader("0\n"))

	// Force PATH empty so LookPath fails
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", "")
	defer os.Setenv("PATH", origPath)

	if err := runSafeClusterApply(context.Background(), reader, t.TempDir(), logger); err != nil {
		t.Fatalf("expected nil when pvesh missing, got %v", err)
	}
}

func TestDetectConfiguredZFSPools_Empty(t *testing.T) {
	orig := restoreFS
	defer func() { restoreFS = orig }()
	restoreFS = NewFakeFS()
	if pools := detectConfiguredZFSPools(); len(pools) != 0 {
		t.Fatalf("expected no pools, got %v", pools)
	}
}
