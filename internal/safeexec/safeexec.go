package safeexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

var ErrCommandNotAllowed = errors.New("command not allowed")

// CommandContext creates commands only for binaries that are intentionally
// allowed by the application. Keep exec.CommandContext calls in the switch so
// static analyzers can see literal command names.
func CommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if strings.TrimSpace(name) != name || name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("%w: %q", ErrCommandNotAllowed, name)
	}

	switch name {
	case "apt-cache":
		return exec.CommandContext(ctx, "apt-cache", args...), nil
	case "blkid":
		return exec.CommandContext(ctx, "blkid", args...), nil
	case "bridge":
		return exec.CommandContext(ctx, "bridge", args...), nil
	case "bzip2":
		return exec.CommandContext(ctx, "bzip2", args...), nil
	case "cat":
		return exec.CommandContext(ctx, "cat", args...), nil
	case "ceph":
		return exec.CommandContext(ctx, "ceph", args...), nil
	case "chattr":
		return exec.CommandContext(ctx, "chattr", args...), nil
	case "crontab":
		return exec.CommandContext(ctx, "crontab", args...), nil
	case "df":
		return exec.CommandContext(ctx, "df", args...), nil
	case "dmidecode":
		return exec.CommandContext(ctx, "dmidecode", args...), nil
	case "dpkg":
		return exec.CommandContext(ctx, "dpkg", args...), nil
	case "dpkg-query":
		return exec.CommandContext(ctx, "dpkg-query", args...), nil
	case "echo":
		return exec.CommandContext(ctx, "echo", args...), nil
	case "ethtool":
		return exec.CommandContext(ctx, "ethtool", args...), nil
	case "false":
		return exec.CommandContext(ctx, "false", args...), nil
	case "firewall-cmd":
		return exec.CommandContext(ctx, "firewall-cmd", args...), nil
	case "free":
		return exec.CommandContext(ctx, "free", args...), nil
	case "hostname":
		return exec.CommandContext(ctx, "hostname", args...), nil
	case "ifreload":
		return exec.CommandContext(ctx, "ifreload", args...), nil
	case "ifup":
		return exec.CommandContext(ctx, "ifup", args...), nil
	case "ip":
		return exec.CommandContext(ctx, "ip", args...), nil
	case "iptables":
		return exec.CommandContext(ctx, "iptables", args...), nil
	case "iptables-save":
		return exec.CommandContext(ctx, "iptables-save", args...), nil
	case "ip6tables":
		return exec.CommandContext(ctx, "ip6tables", args...), nil
	case "ip6tables-save":
		return exec.CommandContext(ctx, "ip6tables-save", args...), nil
	case "journalctl":
		return exec.CommandContext(ctx, "journalctl", args...), nil
	case "lsblk":
		return exec.CommandContext(ctx, "lsblk", args...), nil
	case "lspci":
		return exec.CommandContext(ctx, "lspci", args...), nil
	case "lscpu":
		return exec.CommandContext(ctx, "lscpu", args...), nil
	case "lsmod":
		return exec.CommandContext(ctx, "lsmod", args...), nil
	case "lsusb":
		return exec.CommandContext(ctx, "lsusb", args...), nil
	case "lvs":
		return exec.CommandContext(ctx, "lvs", args...), nil
	case "lzma":
		return exec.CommandContext(ctx, "lzma", args...), nil
	case "mailq":
		return exec.CommandContext(ctx, "mailq", args...), nil
	case "mount":
		return exec.CommandContext(ctx, "mount", args...), nil
	case "mountpoint":
		return exec.CommandContext(ctx, "mountpoint", args...), nil
	case "nft":
		return exec.CommandContext(ctx, "nft", args...), nil
	case "pbzip2":
		return exec.CommandContext(ctx, "pbzip2", args...), nil
	case "pgrep":
		return exec.CommandContext(ctx, "pgrep", args...), nil
	case "pigz":
		return exec.CommandContext(ctx, "pigz", args...), nil
	case "ping":
		return exec.CommandContext(ctx, "ping", args...), nil
	case "pvs":
		return exec.CommandContext(ctx, "pvs", args...), nil
	case "proxmox-backup-client":
		return exec.CommandContext(ctx, "proxmox-backup-client", args...), nil
	case "proxmox-backup-manager":
		return exec.CommandContext(ctx, "proxmox-backup-manager", args...), nil
	case "proxmox-mail-forward":
		return exec.CommandContext(ctx, "proxmox-mail-forward", args...), nil
	case "proxmox-tape":
		return exec.CommandContext(ctx, "proxmox-tape", args...), nil
	case "ps":
		return exec.CommandContext(ctx, "ps", args...), nil
	case "pvecm":
		return exec.CommandContext(ctx, "pvecm", args...), nil
	case "pve-firewall":
		return exec.CommandContext(ctx, "pve-firewall", args...), nil
	case "pvenode":
		return exec.CommandContext(ctx, "pvenode", args...), nil
	case "pvesh":
		return exec.CommandContext(ctx, "pvesh", args...), nil
	case "pvesm":
		return exec.CommandContext(ctx, "pvesm", args...), nil
	case "pveum":
		return exec.CommandContext(ctx, "pveum", args...), nil
	case "pveversion":
		return exec.CommandContext(ctx, "pveversion", args...), nil
	case "rclone":
		return exec.CommandContext(ctx, "rclone", args...), nil
	case "sendmail":
		return exec.CommandContext(ctx, "sendmail", args...), nil
	case "sensors":
		return exec.CommandContext(ctx, "sensors", args...), nil
	case "sh":
		return exec.CommandContext(ctx, "sh", args...), nil
	case "smartctl":
		return exec.CommandContext(ctx, "smartctl", args...), nil
	case "ss":
		return exec.CommandContext(ctx, "ss", args...), nil
	case "systemctl":
		return exec.CommandContext(ctx, "systemctl", args...), nil
	case "systemd-run":
		return exec.CommandContext(ctx, "systemd-run", args...), nil
	case "sysctl":
		return exec.CommandContext(ctx, "sysctl", args...), nil
	case "tail":
		return exec.CommandContext(ctx, "tail", args...), nil
	case "tar":
		return exec.CommandContext(ctx, "tar", args...), nil
	case "udevadm":
		return exec.CommandContext(ctx, "udevadm", args...), nil
	case "umount":
		return exec.CommandContext(ctx, "umount", args...), nil
	case "uname":
		return exec.CommandContext(ctx, "uname", args...), nil
	case "ufw":
		return exec.CommandContext(ctx, "ufw", args...), nil
	case "vgs":
		return exec.CommandContext(ctx, "vgs", args...), nil
	case "which":
		return exec.CommandContext(ctx, "which", args...), nil
	case "xz":
		return exec.CommandContext(ctx, "xz", args...), nil
	case "zfs":
		return exec.CommandContext(ctx, "zfs", args...), nil
	case "zpool":
		return exec.CommandContext(ctx, "zpool", args...), nil
	case "zstd":
		return exec.CommandContext(ctx, "zstd", args...), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrCommandNotAllowed, name)
	}
}

func CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

func TrustedCommandContext(ctx context.Context, execPath string, args ...string) (*exec.Cmd, error) {
	if err := ValidateTrustedExecutablePath(execPath); err != nil {
		return nil, err
	}
	// #nosec G204 -- execPath is absolute, regular, executable, and not world-writable.
	// nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
	return exec.CommandContext(ctx, execPath, args...), nil
}

func ValidateTrustedExecutablePath(execPath string) error {
	clean := strings.TrimSpace(execPath)
	if clean == "" {
		return fmt.Errorf("executable path is empty")
	}
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("executable path must be absolute: %s", execPath)
	}
	info, err := os.Stat(clean)
	if err != nil {
		return fmt.Errorf("stat executable path: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable path is not a regular file: %s", clean)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("executable path is not executable: %s", clean)
	}
	if info.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("executable path is world-writable: %s", clean)
	}
	return nil
}

func ValidateRcloneRemoteName(remote string) error {
	if remote == "" {
		return fmt.Errorf("rclone remote name is empty")
	}
	if strings.HasPrefix(remote, "-") {
		return fmt.Errorf("rclone remote name must not start with '-'")
	}
	if strings.ContainsAny(remote, `/\:`) {
		return fmt.Errorf("rclone remote name contains a path separator or colon")
	}
	for _, r := range remote {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("rclone remote name contains whitespace or control characters")
		}
	}
	return nil
}

func ValidateRemoteRelativePath(value, field string) error {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return nil
	}
	for _, r := range clean {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	normalized := path.Clean(strings.Trim(clean, "/"))
	if normalized == "." {
		return nil
	}
	if strings.HasPrefix(normalized, "../") || normalized == ".." {
		return fmt.Errorf("%s must not traverse outside the configured remote", field)
	}
	return nil
}

func ProcPath(pid int, leaf string) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("pid must be positive")
	}
	switch leaf {
	case "comm":
		return fmt.Sprintf("/proc/%d/comm", pid), nil
	case "status":
		return fmt.Sprintf("/proc/%d/status", pid), nil
	case "exe":
		return fmt.Sprintf("/proc/%d/exe", pid), nil
	default:
		return "", fmt.Errorf("unsupported proc leaf: %s", leaf)
	}
}
