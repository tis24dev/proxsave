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

// runRedetect drives redetectHostBackupEnvironment with a mirrored bootstrap
// logger and returns everything it printed plus the resulting envInfo.
func runRedetect(t *testing.T, prefix string, hostBackup bool) (string, *environment.EnvironmentInfo) {
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
		envInfo:   &environment.EnvironmentInfo{Type: types.ProxmoxUnknown},
	}
	redetectHostBackupEnvironment(rt)
	return buf.String(), rt.envInfo
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
	writeHBFile(t, filepath.Join(root, "etc/pve-manager/version"), "8.2.2\n")
	if err := os.MkdirAll(filepath.Join(root, "etc/pve"), 0o755); err != nil {
		t.Fatal(err)
	}

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
