package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// --decrypt read DEBUG_LEVEL only, so --log-level was ignored there alone (measured on
// pve-test: no DEBUG line with --log-level debug, 154 for --restore). It now follows
// the backup and restore rule: DEBUG_LEVEL as the base, --log-level overriding it.
func TestDecryptHonoursLogLevel(t *testing.T) {
	base := t.TempDir()
	cfgPath := filepath.Join(base, "configs", "backup.env")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	env := strings.Replace(config.DefaultEnvTemplate(), "DEBUG_LEVEL=", "DEBUG_LEVEL=standard #", 1)
	if err := os.WriteFile(cfgPath, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = stdinW.Close()
	prevStdin := os.Stdin
	os.Stdin = stdinR
	prevLogger := logging.GetDefaultLogger()
	t.Cleanup(func() { os.Stdin = prevStdin; _ = stdinR.Close(); logging.SetDefaultLogger(prevLogger) })

	prevCheck := decryptStdinCheck
	decryptStdinCheck = func() error { return nil }
	t.Cleanup(func() { decryptStdinCheck = prevCheck })

	bootstrap := logging.NewBootstrapLogger()
	bootstrap.SetConsoleQuiet(true)
	runErr := runDecryptWorkflowOnly(context.Background(), &cli.Args{ConfigPath: cfgPath, LogLevel: types.LogLevelDebug}, bootstrap, "test", true)
	t.Logf("decrypt returned %v", runErr)
	if got := logging.GetDefaultLogger().GetLevel(); got != types.LogLevelDebug {
		t.Fatalf("decrypt logger level = %v, want DEBUG from --log-level", got)
	}
}
