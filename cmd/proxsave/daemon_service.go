// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

const (
	daemonUnitName = "proxsave-daemon.service"
	daemonUnitPath = "/etc/systemd/system/proxsave-daemon.service"
	// daemonExecPath is the canonical entrypoint symlink the unit invokes (same
	// path used for the crontab line); resolved by ensureGoSymlink at install.
	daemonExecPath = "/usr/local/bin/proxsave"
)

// buildDaemonUnit renders the systemd unit. systemd is only the keep-alive
// supervisor (Restart=always); the daemon schedules internally. A non-empty
// configPath is pinned with --config so the unit uses the same backup.env the
// install/upgrade wrote.
func buildDaemonUnit(execToken, configPath string) string {
	exec := strings.TrimSpace(execToken)
	if exec == "" {
		exec = daemonExecPath
	}
	cmd := exec + " --daemon"
	if p := strings.TrimSpace(configPath); p != "" {
		cmd += " --config " + p
	}
	return fmt.Sprintf(`[Unit]
Description=ProxSave backup daemon
Documentation=https://github.com/tis24dev/proxsave
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
`, cmd)
}

// installDaemonService writes the unit, reloads systemd, and enables+starts it.
// execToken is the entrypoint the unit runs (canonical symlink by default);
// configPath is pinned when non-empty.
func installDaemonService(ctx context.Context, execToken, configPath string, bootstrap *logging.BootstrapLogger) error {
	unit := buildDaemonUnit(execToken, configPath)
	// The unit path is a fixed constant and the content is our own template, so a
	// plain write is safe here (no user-controlled path -> no G304/G306 class).
	if err := os.WriteFile(daemonUnitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", daemonUnitPath, err)
	}
	logging.DebugStepBootstrap(bootstrap, "daemon", "wrote unit %s", daemonUnitPath)
	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl(ctx, "enable", "--now", daemonUnitName); err != nil {
		return err
	}
	return nil
}

// removeDaemonService disables+stops the unit and deletes it, then reloads
// systemd. Missing unit / already-stopped is not an error (idempotent teardown).
func removeDaemonService(ctx context.Context, bootstrap *logging.BootstrapLogger) error {
	// Best-effort disable+stop; ignore failure so a partial/never-installed unit
	// still gets cleaned up below.
	if err := runSystemctl(ctx, "disable", "--now", daemonUnitName); err != nil {
		logging.DebugStepBootstrap(bootstrap, "daemon", "disable %s: %v (continuing)", daemonUnitName, err)
	}
	if err := os.Remove(daemonUnitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", daemonUnitPath, err)
	}
	if err := runSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	logging.DebugStepBootstrap(bootstrap, "daemon", "removed unit %s", daemonUnitName)
	return nil
}

// daemonUnitInstalled reports whether the unit FILE exists on disk (installed,
// whether active or merely enabled/stopped). A cheap stat, no systemctl call.
func daemonUnitInstalled() bool {
	_, err := os.Stat(daemonUnitPath)
	return err == nil
}

// runSystemctl runs one systemctl invocation through the safeexec allowlist with
// a bounded timeout, surfacing stderr on failure.
func runSystemctl(ctx context.Context, args ...string) error {
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd, err := safeexec.CommandContext(callCtx, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
