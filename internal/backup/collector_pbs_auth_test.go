package backup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tis24dev/proxmox-backup/internal/types"
)

func TestSafeCmdOutputWithPBSAuthSetsEnv(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }

	var capturedEnv []string
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		capturedEnv = append([]string(nil), extraEnv...)
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "pass"
	cfg.PBSFingerprint = "finger"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", output, "test", true); err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuth error: %v", err)
	}

	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected output file: %v", err)
	}

	expectedKeys := []string{"PBS_REPOSITORY=user@host", "PBS_PASSWORD=pass", "PBS_FINGERPRINT=finger"}
	for _, k := range expectedKeys {
		if !slices.Contains(capturedEnv, k) {
			t.Fatalf("expected env to contain %s, got %v", k, capturedEnv)
		}
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreBuildsRepo(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }

	var capturedEnv []string
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		capturedEnv = append([]string(nil), extraEnv...)
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host:oldds"
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "newds", true); err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuthForDatastore error: %v", err)
	}

	if !slices.Contains(capturedEnv, "PBS_REPOSITORY=user@host:newds") {
		t.Fatalf("expected repository to be replaced with datastore: %v", capturedEnv)
	}
	if !slices.Contains(capturedEnv, "PBS_PASSWORD=secret") {
		t.Fatalf("expected password in env: %v", capturedEnv)
	}
}
