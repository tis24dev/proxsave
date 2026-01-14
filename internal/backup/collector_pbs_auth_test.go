package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/tis24dev/proxsave/internal/types"
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

func TestSafeCmdOutputWithPBSAuthForDatastoreSkipsWhenNoCredentials(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }

	called := false
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		called = true
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = ""
	cfg.PBSPassword = ""

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")

	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds", true); err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuthForDatastore error: %v", err)
	}

	if called {
		t.Fatalf("expected command to be skipped when no credentials are configured")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file when skipped, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreDefaultsUserWhenRepoEmpty(t *testing.T) {
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
	cfg.PBSRepository = ""
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")

	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds1", true); err != nil {
		t.Fatalf("safeCmdOutputWithPBSAuthForDatastore error: %v", err)
	}

	if _, err := os.Stat(output); err != nil {
		t.Fatalf("expected output file: %v", err)
	}

	if !slices.Contains(capturedEnv, "PBS_REPOSITORY=root@pam@localhost:ds1") {
		t.Fatalf("expected default repository env, got %v", capturedEnv)
	}
	if !slices.Contains(capturedEnv, "PBS_PASSWORD=secret") {
		t.Fatalf("expected password in env: %v", capturedEnv)
	}
}

func TestSafeCmdOutputWithPBSAuthReturnsErrorOnEmptyCommand(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "   ", filepath.Join(t.TempDir(), "out.txt"), "desc", false); err == nil {
		t.Fatalf("expected error for empty command")
	}
}

func TestSafeCmdOutputWithPBSAuthCriticalCommandNotAvailableIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	t.Cleanup(func() { execLookPath = origLookPath })
	execLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "missing-cmd arg", filepath.Join(t.TempDir(), "out.txt"), "desc", true); err == nil {
		t.Fatalf("expected error for critical missing command")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthDryRunSkipsExecution(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		t.Fatalf("runCommandWithEnv should not be called in dry-run")
		return nil, nil
	}

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, true)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", output, "desc", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file in dry-run, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthWriteFailureIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	outputDir := filepath.Join(t.TempDir(), "dir")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("mkdir outputDir: %v", err)
	}
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", outputDir, "desc", false); err == nil {
		t.Fatalf("expected write error when output path is a directory")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthHonorsContextCancellation(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := collector.safeCmdOutputWithPBSAuth(ctx, "echo hi", filepath.Join(t.TempDir(), "out.txt"), "desc", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthNonCriticalCommandNotAvailableIsSkipped(t *testing.T) {
	origLookPath := execLookPath
	t.Cleanup(func() { execLookPath = origLookPath })
	execLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "missing-cmd arg", output, "desc", false); err != nil {
		t.Fatalf("expected non-critical missing command to be skipped, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthNonCriticalCommandFailureIsSwallowed(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("nope"), os.ErrInvalid
	}

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", output, "desc", false); err != nil {
		t.Fatalf("expected non-critical failure to be swallowed, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthEnsureDirFailureReturnsError(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	output := filepath.Join(blocker, "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", output, "desc", true); err == nil {
		t.Fatalf("expected ensureDir error")
	}
}

func TestSafeCmdOutputWithPBSAuthCriticalFailureIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("nope"), os.ErrInvalid
	}

	cfg := GetDefaultCollectorConfig()
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuth(context.Background(), "echo hi", output, "desc", true); err == nil {
		t.Fatalf("expected critical error")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreAppendsDatastoreAndIncludesFingerprint(t *testing.T) {
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
	cfg.PBSPassword = "secret"
	cfg.PBSFingerprint = "finger"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds1", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !slices.Contains(capturedEnv, "PBS_REPOSITORY=user@host:ds1") {
		t.Fatalf("expected datastore appended, got %v", capturedEnv)
	}
	if !slices.Contains(capturedEnv, "PBS_FINGERPRINT=finger") {
		t.Fatalf("expected fingerprint env, got %v", capturedEnv)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreNonCriticalFailureReturnsNil(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("nope"), os.ErrInvalid
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds1", false); err != nil {
		t.Fatalf("expected non-critical failure to be swallowed, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file on non-critical failure, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreReturnsErrorOnEmptyCommand(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "   ", filepath.Join(t.TempDir(), "out.txt"), "desc", "ds", false); err == nil {
		t.Fatalf("expected error for empty command")
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreHonorsContextCancellation(t *testing.T) {
	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := collector.safeCmdOutputWithPBSAuthForDatastore(ctx, "echo hi", filepath.Join(t.TempDir(), "out.txt"), "desc", "ds", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreNonCriticalCommandNotAvailableIsSkipped(t *testing.T) {
	origLookPath := execLookPath
	t.Cleanup(func() { execLookPath = origLookPath })
	execLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "missing-cmd arg", output, "desc", "ds", false); err != nil {
		t.Fatalf("expected non-critical missing command to be skipped, got %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreCriticalCommandNotAvailableIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	t.Cleanup(func() { execLookPath = origLookPath })
	execLookPath = func(string) (string, error) { return "", os.ErrNotExist }

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "missing-cmd arg", output, "desc", "ds", true); err == nil {
		t.Fatalf("expected critical error for missing command")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreDryRunSkipsExecution(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		t.Fatalf("runCommandWithEnv should not be called in dry-run")
		return nil, nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, true)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("expected no output file in dry-run, stat err=%v", err)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreCriticalFailureIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("nope"), os.ErrInvalid
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	output := filepath.Join(t.TempDir(), "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds", true); err == nil {
		t.Fatalf("expected critical error")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreWriteFailureIncrementsFilesFailed(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"

	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)
	outputDir := filepath.Join(t.TempDir(), "dir")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("mkdir outputDir: %v", err)
	}

	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", outputDir, "desc", "ds", false); err == nil {
		t.Fatalf("expected write error")
	}
	if got := collector.GetStats().FilesFailed; got != 1 {
		t.Fatalf("expected FilesFailed=1, got %d", got)
	}
}

func TestSafeCmdOutputWithPBSAuthForDatastoreEnsureDirFailureReturnsError(t *testing.T) {
	origLookPath := execLookPath
	origRun := runCommandWithEnv
	t.Cleanup(func() {
		execLookPath = origLookPath
		runCommandWithEnv = origRun
	})

	execLookPath = func(string) (string, error) { return "/bin/echo", nil }
	runCommandWithEnv = func(ctx context.Context, extraEnv []string, name string, args ...string) ([]byte, error) {
		return []byte("ok"), nil
	}

	cfg := GetDefaultCollectorConfig()
	cfg.PBSRepository = "user@host"
	cfg.PBSPassword = "secret"
	collector := NewCollector(newTestLogger(), cfg, t.TempDir(), types.ProxmoxBS, false)

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not-a-dir"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	output := filepath.Join(blocker, "out.txt")
	if err := collector.safeCmdOutputWithPBSAuthForDatastore(context.Background(), "echo hi", output, "desc", "ds", false); err == nil {
		t.Fatalf("expected ensureDir error")
	}
}
