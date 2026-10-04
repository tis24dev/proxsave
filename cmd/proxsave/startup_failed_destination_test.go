package main

import (
	"bytes"
	"testing"

	"github.com/tis24dev/proxsave/internal/checks"
	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// A destination on in backup.env that fails at startup is recorded as failed and left
// out of the disk-space check, as before the record existed: with a threshold no disk
// meets, the check warns about the Secondary and the Cloud only while they are in the
// run.
func TestStartupFailedDestinationLeavesTheDiskSpaceCheck(t *testing.T) {
	run := func(fail bool) (string, *config.Config) {
		var out bytes.Buffer
		logger := logging.New(types.LogLevelDebug, false)
		logger.SetOutput(&out)
		cfg := &config.Config{SecondaryEnabled: true, CloudEnabled: true}
		checker := checks.NewChecker(logger, &checks.CheckerConfig{
			BackupPath:         t.TempDir(),
			SecondaryEnabled:   true,
			SecondaryPath:      t.TempDir(),
			CloudEnabled:       true,
			CloudPath:          t.TempDir(),
			MinDiskSecondaryGB: 1 << 40,
			MinDiskCloudGB:     1 << 40,
		})
		if fail {
			disableSecondaryForRun(cfg, checker)
			disableCloudForRun(cfg, checker)
		}
		checker.CheckDiskSpace()
		return out.String(), cfg
	}

	out, _ := run(false)
	for _, label := range []string{"Secondary disk space:", "Cloud disk space:"} {
		if !bytes.Contains([]byte(out), []byte(label)) {
			t.Fatalf("control: %q missing with the destination in the run:\n%s", label, out)
		}
	}

	out, cfg := run(true)
	for _, label := range []string{"Secondary", "Cloud"} {
		if bytes.Contains([]byte(out), []byte(label+" disk space:")) || bytes.Contains([]byte(out), []byte("Checking disk space on "+label)) {
			t.Fatalf("%s failed at startup was checked for disk space:\n%s", label, out)
		}
	}
	if cfg.SecondaryEnabled || cfg.CloudEnabled || !cfg.SecondaryStartupFailed || !cfg.CloudStartupFailed {
		t.Fatalf("enabled %v/%v startupFailed %v/%v, want off and recorded", cfg.SecondaryEnabled, cfg.CloudEnabled, cfg.SecondaryStartupFailed, cfg.CloudStartupFailed)
	}
}
