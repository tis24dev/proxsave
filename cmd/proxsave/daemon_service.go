// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safeexec"
)

const (
	daemonUnitName = "proxsave-daemon.service"
	// daemonExecPath is the canonical entrypoint symlink the unit invokes (same
	// path used for the crontab line); resolved by ensureGoSymlink at install.
	daemonExecPath = "/usr/local/bin/proxsave"
)

// daemonUnitPath is the systemd unit path. A var (not const) so tests can point it
// at a temp dir.
var daemonUnitPath = "/etc/systemd/system/proxsave-daemon.service"

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

// validateDaemonUnitToken rejects a token that would be word-split (or, for a
// newline, inject unit directives) once embedded unquoted in ExecStart. An empty
// token is valid: empty configPath means "no --config" and empty execToken falls
// back to the canonical path. Only whitespace and control characters are hazards
// here (systemd word-splits on ASCII whitespace); anything else is left as-is.
func validateDaemonUnitToken(label, token string) error {
	if token == "" {
		return nil
	}
	for _, r := range token {
		if r == ' ' || unicode.IsControl(r) {
			return fmt.Errorf("%s must not contain whitespace or control characters: %q", label, token)
		}
	}
	return nil
}

// installDaemonService writes the unit, reloads systemd, and enables+starts it.
// execToken is the entrypoint the unit runs (canonical symlink by default);
// configPath is pinned when non-empty.
func installDaemonService(ctx context.Context, execToken, configPath string, bootstrap *logging.BootstrapLogger) error {
	// Reject a whitespace/control-char token BEFORE writing the unit, so a bad
	// --config path surfaces a clear error instead of a daemon that boots with the
	// wrong config or crash-loops on a word-split ExecStart.
	if err := validateDaemonUnitToken("executable path", strings.TrimSpace(execToken)); err != nil {
		return err
	}
	if err := validateDaemonUnitToken("config path", strings.TrimSpace(configPath)); err != nil {
		return err
	}
	unit := buildDaemonUnit(execToken, configPath)
	// The unit path is a fixed constant and the content is our own template, so a
	// plain write is safe here (no user-controlled path -> no G304/G306 class). The
	// write is atomic (temp+rename+fsync) so a crash mid-write never leaves a
	// truncated unit at this boot-critical path.
	if err := writeUnitFileAtomic(daemonUnitPath, []byte(unit), 0o644); err != nil {
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

// unitRenameFunc is the rename step of writeUnitFileAtomic, isolated as a var so
// tests can force a post-write failure and assert the previous unit is preserved.
var unitRenameFunc = os.Rename

// writeUnitFileAtomic writes data to path atomically and durably: it writes a temp
// sibling, fsyncs it, chmods it, closes it, renames it over path, then fsyncs the
// parent dir so the rename survives a crash. A failed write leaves the previous unit
// (or none) in place, never a truncated unit.
func writeUnitFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := unitRenameFunc(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	fsyncDir(dir)
	return nil
}

// fsyncDir best-effort flushes a directory entry so a rename survives a crash.
func fsyncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
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

// daemonPresenceProbe reports the daemon's systemd-level existence so the healthcheck
// checks can tell "not installed" / "not running" / "running but not reporting" apart
// from a heartbeat-derived guess. It is a seam so tests can drive the states without a
// real systemctl. When systemctl is unavailable (no active-state word) the probe returns
// Probed=false and the checks fall back to a heartbeat-only diagnosis.
var daemonPresenceProbe = daemonPresence

func daemonPresence(ctx context.Context) health.DaemonPresence {
	if ctx == nil {
		ctx = context.Background()
	}
	active := daemonUnitActiveState(ctx)
	if active == "" {
		return health.DaemonPresence{Probed: false}
	}
	return health.DaemonPresence{
		Probed:    true,
		Installed: daemonUnitInstalled(),
		Active:    strings.EqualFold(active, "active"),
	}
}

// daemonUnitActiveState returns the systemctl "is-active" word for the unit
// (e.g. "active", "inactive", "failed"), best-effort: "" when systemctl is
// unavailable. is-active exits non-zero when not active, so the exit code is
// ignored and only the printed state word is used.
func daemonUnitActiveState(ctx context.Context) string {
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd, err := safeexec.CommandContext(callCtx, "systemctl", "is-active", daemonUnitName)
	if err != nil {
		return ""
	}
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// restartDaemonService restarts the resident daemon unit (best used to load a rebuilt
// binary: systemd keeps the old process until an explicit restart). Idempotent-ish:
// systemctl restart starts the unit if it was stopped.
func restartDaemonService(ctx context.Context) error {
	return runSystemctl(ctx, "restart", daemonUnitName)
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
