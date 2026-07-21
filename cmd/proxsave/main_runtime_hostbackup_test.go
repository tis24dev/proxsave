package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// runRedetectWithLive drives redetectHostBackupEnvironment with a mirrored
// bootstrap logger, seeding the pre-config live-container type, and returns
// everything it printed plus the resulting envInfo. Seeding a non-Unknown live type
// is what exposes the retain-live-type leak: a prefix run must overwrite it.
func runRedetectWithLive(t *testing.T, prefix string, hostBackup bool, liveType types.ProxmoxType) (string, *environment.EnvironmentInfo) {
	t.Helper()
	boot := logging.NewBootstrapLogger()
	boot.SetConsoleQuiet(true)
	buf := &bytes.Buffer{}
	mirror := logging.New(types.LogLevelDebug, false)
	mirror.SetOutput(buf)
	boot.SetMirrorLogger(mirror)

	rt := &appRuntime{
		bootstrap: boot,
		cfg:       &config.Config{SystemRootPrefix: prefix, HostBackupMode: hostBackup},
		envInfo:   &environment.EnvironmentInfo{Type: liveType},
	}
	redetectHostBackupEnvironment(rt)
	return buf.String(), rt.envInfo
}

// runRedetect is the common case: the live type is unknown (no Proxmox seen in the
// container) and only the prefix detection matters.
func runRedetect(t *testing.T, prefix string, hostBackup bool) (string, *environment.EnvironmentInfo) {
	t.Helper()
	return runRedetectWithLive(t, prefix, hostBackup, types.ProxmoxUnknown)
}

func writeHBFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRedetectPlainPrefixNeutralFraming: an existing SYSTEM_ROOT_PREFIX user with
// no HOST_BACKUP_MODE gets a neutral banner, no host-backup framing, no warning,
// but still gets the corrected detection type (issue #255 backward compatibility).
func TestRedetectPlainPrefixNeutralFraming(t *testing.T) {
	root := t.TempDir()
	// PVE is detected from the version file; /etc/pve is intentionally NOT created,
	// so this also pins the warnHostBackupMountShape HostBackupMode guard: a plain
	// prefix with a missing /etc/pve must still produce no bind-mount warning.
	writeHBFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")

	out, info := runRedetect(t, root, false)
	if info.Type != types.ProxmoxVE {
		t.Fatalf("envInfo.Type = %v, want ProxmoxVE (detection must still be corrected)", info.Type)
	}
	if !strings.Contains(out, "(prefix ") {
		t.Fatalf("expected a neutral 'prefix' banner, got:\n%s", out)
	}
	if strings.Contains(out, "host-backup mode") {
		t.Fatalf("plain prefix must not be framed as host-backup mode, got:\n%s", out)
	}
	if strings.Contains(strings.ToUpper(out), "WARNING") {
		t.Fatalf("plain prefix must not warn, got:\n%s", out)
	}
}

// TestRedetectPBSHostBackupNoPveWarning: a PBS host in host-backup mode gets the
// host-backup banner but no /etc/pve mount-shape warning (PBS has no /etc/pve).
func TestRedetectPBSHostBackupNoPveWarning(t *testing.T) {
	root := t.TempDir()
	writeHBFile(t, filepath.Join(root, "etc/proxmox-backup/version"), "3.2.1\n")

	out, info := runRedetect(t, root, true)
	if info.Type != types.ProxmoxBS {
		t.Fatalf("envInfo.Type = %v, want ProxmoxBS", info.Type)
	}
	if !strings.Contains(out, "host-backup mode") {
		t.Fatalf("expected host-backup framing, got:\n%s", out)
	}
	if strings.Contains(out, "/etc/pve") {
		t.Fatalf("PBS host must not get an /etc/pve warning, got:\n%s", out)
	}
}

// TestRedetectVEHostBackupMissingPveWarnsOnce: a VE host in host-backup mode whose
// /etc/pve bind is missing gets exactly one mount-shape warning.
func TestRedetectVEHostBackupMissingPveWarnsOnce(t *testing.T) {
	root := t.TempDir()
	// PVE detected via the version file; the /etc/pve directory is intentionally absent.
	writeHBFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")

	out, info := runRedetect(t, root, true)
	if info.Type != types.ProxmoxVE {
		t.Fatalf("envInfo.Type = %v, want ProxmoxVE", info.Type)
	}
	if !strings.Contains(out, "host-backup mode") {
		t.Fatalf("expected host-backup framing, got:\n%s", out)
	}
	if !strings.Contains(out, "/etc/pve") || !strings.Contains(out, "bind mount") {
		t.Fatalf("expected the /etc/pve bind-mount warning, got:\n%s", out)
	}
	if got := strings.Count(out, "bind mount may be missing"); got != 1 {
		t.Fatalf("expected exactly one mount-shape warning, got %d:\n%s", got, out)
	}
}

// TestRedetectHostBackupWithoutPrefixWarns: HOST_BACKUP_MODE with no prefix warns
// that it has no effect.
func TestRedetectHostBackupWithoutPrefixWarns(t *testing.T) {
	out, _ := runRedetect(t, "", true)
	if !strings.Contains(out, "SYSTEM_ROOT_PREFIX is empty") {
		t.Fatalf("expected an empty-prefix warning, got:\n%s", out)
	}
}

// TestRedetectFailedPrefixNeverRetainsLivePVE is the greptile-P1 regression: a live
// PVE system whose prefix has no Proxmox markers must NOT keep the live PVE type
// (which would ship a hollow archive labeled valid PVE). It must fail closed to
// Unknown, and since the live system is a Proxmox this is a mis-mount, so it warns
// even without HOST_BACKUP_MODE.
func TestRedetectFailedPrefixNeverRetainsLivePVE(t *testing.T) {
	root := t.TempDir() // empty: no Proxmox markers under the prefix

	out, info := runRedetectWithLive(t, root, false, types.ProxmoxVE)
	if info.Type != types.ProxmoxUnknown {
		t.Fatalf("envInfo.Type = %v, want ProxmoxUnknown (must not retain the live PVE type)", info.Type)
	}
	if !strings.Contains(strings.ToUpper(out), "WARNING") || !strings.Contains(out, "detected on the live system") {
		t.Fatalf("expected a mis-mount warning without HOST_BACKUP_MODE, got:\n%s", out)
	}
}

// TestRedetectFailedPrefixHostBackupFailsClosedAndWarns: host-backup mode with a
// prefix that has no host mounted fails closed to Unknown and warns, with no PVE
// banner.
func TestRedetectFailedPrefixHostBackupFailsClosedAndWarns(t *testing.T) {
	root := t.TempDir()

	out, info := runRedetectWithLive(t, root, true, types.ProxmoxVE)
	if info.Type != types.ProxmoxUnknown {
		t.Fatalf("envInfo.Type = %v, want ProxmoxUnknown", info.Type)
	}
	if !strings.Contains(out, "host filesystem may not be mounted") {
		t.Fatalf("expected the not-mounted warning, got:\n%s", out)
	}
	if strings.Contains(out, "✓ Proxmox Type") {
		t.Fatalf("failed detection must not print a type banner, got:\n%s", out)
	}
}

// TestRedetectFailedPrefixPlainStaysQuiet is the no-spam contract: a non-Proxmox
// live system (plain chroot/snapshot/CI) with an empty prefix and no
// HOST_BACKUP_MODE fails closed to Unknown quietly, emitting neither warning
// message (asserted on the message fragments rather than the "WARNING" level tag,
// since the interpolated prefix path echoes the test name).
func TestRedetectFailedPrefixPlainStaysQuiet(t *testing.T) {
	root := t.TempDir()

	out, info := runRedetectWithLive(t, root, false, types.ProxmoxUnknown)
	if info.Type != types.ProxmoxUnknown {
		t.Fatalf("envInfo.Type = %v, want ProxmoxUnknown", info.Type)
	}
	if strings.Contains(out, "may not be mounted") || strings.Contains(out, "detected on the live system") {
		t.Fatalf("a plain prefix on a non-Proxmox host must not warn, got:\n%s", out)
	}
}

// TestRedetectFailedPrefixNeverRetainsLivePBS mirrors the PVE regression for PBS.
func TestRedetectFailedPrefixNeverRetainsLivePBS(t *testing.T) {
	root := t.TempDir()

	out, info := runRedetectWithLive(t, root, false, types.ProxmoxBS)
	if info.Type != types.ProxmoxUnknown {
		t.Fatalf("envInfo.Type = %v, want ProxmoxUnknown (must not retain the live PBS type)", info.Type)
	}
	if !strings.Contains(strings.ToUpper(out), "WARNING") || !strings.Contains(out, "detected on the live system") {
		t.Fatalf("expected a mis-mount warning, got:\n%s", out)
	}
}
