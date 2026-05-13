// Package safeexec centralizes constrained process execution helpers.
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

// ErrCommandNotAllowed reports that a command name is outside the allowlist.
var ErrCommandNotAllowed = errors.New("command not allowed")

type commandFactory func(context.Context, ...string) *exec.Cmd

var allowedCommandFactories = map[string]commandFactory{
	"apt-cache": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "apt-cache", args...)
	},
	"blkid": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "blkid", args...)
	},
	"bridge": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bridge", args...)
	},
	"bzip2": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "bzip2", args...)
	},
	"cat": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "cat", args...)
	},
	"ceph": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ceph", args...)
	},
	"chattr": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "chattr", args...)
	},
	"crontab": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "crontab", args...)
	},
	"df": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "df", args...)
	},
	"dmidecode": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "dmidecode", args...)
	},
	"dpkg": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "dpkg", args...)
	},
	"dpkg-query": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "dpkg-query", args...)
	},
	"echo": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", args...)
	},
	"ethtool": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ethtool", args...)
	},
	"false": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false", args...)
	},
	"firewall-cmd": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "firewall-cmd", args...)
	},
	"free": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "free", args...)
	},
	"hostname": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "hostname", args...)
	},
	"ifreload": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ifreload", args...)
	},
	"ifup": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ifup", args...)
	},
	"ip": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ip", args...)
	},
	"iptables": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "iptables", args...)
	},
	"iptables-save": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "iptables-save", args...)
	},
	"ip6tables": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ip6tables", args...)
	},
	"ip6tables-save": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ip6tables-save", args...)
	},
	"journalctl": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "journalctl", args...)
	},
	"lsblk": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lsblk", args...)
	},
	"lspci": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lspci", args...)
	},
	"lscpu": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lscpu", args...)
	},
	"lsmod": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lsmod", args...)
	},
	"lsusb": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lsusb", args...)
	},
	"lvs": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lvs", args...)
	},
	"lzma": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "lzma", args...)
	},
	"mailq": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "mailq", args...)
	},
	"mount": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "mount", args...)
	},
	"mountpoint": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "mountpoint", args...)
	},
	"nft": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "nft", args...)
	},
	"pbzip2": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pbzip2", args...)
	},
	"pgrep": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pgrep", args...)
	},
	"pigz": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pigz", args...)
	},
	"ping": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ping", args...)
	},
	"pvs": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pvs", args...)
	},
	"proxmox-backup-client": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "proxmox-backup-client", args...)
	},
	"proxmox-backup-manager": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "proxmox-backup-manager", args...)
	},
	"proxmox-mail-forward": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "proxmox-mail-forward", args...)
	},
	"proxmox-tape": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "proxmox-tape", args...)
	},
	"ps": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ps", args...)
	},
	"pvecm": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pvecm", args...)
	},
	"pve-firewall": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pve-firewall", args...)
	},
	"pvenode": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pvenode", args...)
	},
	"pvesh": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pvesh", args...)
	},
	"pvesm": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pvesm", args...)
	},
	"pveum": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pveum", args...)
	},
	"pveversion": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "pveversion", args...)
	},
	"rclone": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "rclone", args...)
	},
	"sendmail": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sendmail", args...)
	},
	"sensors": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sensors", args...)
	},
	"sh": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", args...)
	},
	"smartctl": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "smartctl", args...)
	},
	"ss": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ss", args...)
	},
	"systemctl": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "systemctl", args...)
	},
	"systemd-run": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "systemd-run", args...)
	},
	"sysctl": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sysctl", args...)
	},
	"tail": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "tail", args...)
	},
	"tar": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "tar", args...)
	},
	"udevadm": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "udevadm", args...)
	},
	"umount": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "umount", args...)
	},
	"uname": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "uname", args...)
	},
	"ufw": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "ufw", args...)
	},
	"vgs": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "vgs", args...)
	},
	"which": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "which", args...)
	},
	"xz": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "xz", args...)
	},
	"zfs": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "zfs", args...)
	},
	"zpool": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "zpool", args...)
	},
	"zstd": func(ctx context.Context, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "zstd", args...)
	},
}

// CommandContext creates commands only for binaries that are intentionally
// allowed by the application. Keep exec.CommandContext calls in the factory map so
// static analyzers can see literal command names.
func CommandContext(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if strings.TrimSpace(name) != name || name == "" || strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("%w: %q", ErrCommandNotAllowed, name)
	}

	if factory, ok := allowedCommandFactories[name]; ok {
		return factory(ctx, args...), nil
	}
	return nil, fmt.Errorf("%w: %q", ErrCommandNotAllowed, name)
}

// CombinedOutput runs an allowed command and returns its combined stdout/stderr.
func CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.CombinedOutput()
}

// Output runs an allowed command and returns stdout.
func Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd, err := CommandContext(ctx, name, args...)
	if err != nil {
		return nil, err
	}
	return cmd.Output()
}

// TrustedCommandContext creates a command for a validated absolute executable path.
func TrustedCommandContext(ctx context.Context, execPath string, args ...string) (*exec.Cmd, error) {
	if err := ValidateTrustedExecutablePath(execPath); err != nil {
		return nil, err
	}
	// #nosec G204 -- execPath is absolute, regular, executable, and not world-writable.
	return exec.CommandContext(ctx, execPath, args...), nil // nosemgrep: go.lang.security.audit.dangerous-exec-command.dangerous-exec-command
}

// ValidateTrustedExecutablePath verifies an executable path is absolute, regular, executable, and not world-writable.
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

// ValidateRcloneRemoteName validates a rclone remote name before it is used in command arguments.
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

// ValidateRemoteRelativePath validates a remote-relative path segment for a named field.
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

// ProcPath returns a safe /proc path for a supported PID leaf.
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
