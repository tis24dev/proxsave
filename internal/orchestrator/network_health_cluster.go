package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

func runCorosyncClusterHealthChecks(ctx context.Context, timeout time.Duration, logger *logging.Logger, report *networkHealthReport) {
	if report == nil {
		return
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}

	done := logging.DebugStart(logger, "cluster health checks", "timeout=%s", timeout)
	defer done(nil)

	logging.DebugStep(logger, "cluster health checks", "Check pmxcfs mount (/etc/pve)")
	mounted, mountKnown, mountMsg := mountpointCheck(ctx, "/etc/pve", timeout)
	switch {
	case mountKnown && mounted:
		report.add("PMXCFS", networkHealthOK, "/etc/pve mounted")
	case mountKnown && !mounted:
		msg := "/etc/pve not mounted (cluster checks may be limited)"
		if mountMsg != "" {
			msg += ": " + mountMsg
		}
		report.add("PMXCFS", networkHealthWarn, msg)
	default:
		report.add("PMXCFS", networkHealthOK, "mountpoint check not available")
	}

	logging.DebugStep(logger, "cluster health checks", "Detect corosync configuration")
	configPath, configured := detectCorosyncConfig()
	switch {
	case configured:
		report.add("Corosync config", networkHealthOK, fmt.Sprintf("found: %s", configPath))
	default:
		if mountKnown && !mounted {
			report.add("Corosync config", networkHealthWarn, "corosync.conf not found (and /etc/pve not mounted)")
		} else {
			report.add("Corosync config", networkHealthOK, "not configured (corosync.conf not found)")
			return
		}
	}

	logging.DebugStep(logger, "cluster health checks", "Check service state: pve-cluster")
	serviceState, serviceMsg, systemctlAvailable := systemctlServiceState(ctx, "pve-cluster", timeout)
	if !systemctlAvailable {
		report.add("pve-cluster service", networkHealthWarn, "systemctl not available; cannot check service state")
	} else if serviceMsg != "" {
		report.add("pve-cluster service", networkHealthWarn, serviceMsg)
	} else if strings.EqualFold(serviceState, "active") {
		report.add("pve-cluster service", networkHealthOK, "active")
	} else {
		report.add("pve-cluster service", networkHealthWarn, fmt.Sprintf("state=%s", serviceState))
	}

	logging.DebugStep(logger, "cluster health checks", "Check service state: corosync")
	corosyncState, corosyncMsg, systemctlAvailable := systemctlServiceState(ctx, "corosync", timeout)
	if !systemctlAvailable {
		report.add("corosync service", networkHealthWarn, "systemctl not available; cannot check service state")
	} else if corosyncMsg != "" {
		report.add("corosync service", networkHealthWarn, corosyncMsg)
	} else if strings.EqualFold(corosyncState, "active") {
		report.add("corosync service", networkHealthOK, "active")
	} else {
		report.add("corosync service", networkHealthWarn, fmt.Sprintf("state=%s", corosyncState))
	}

	logging.DebugStep(logger, "cluster health checks", "Check quorum: pvecm status")
	quorumInfo, pvecmAvailable, quorumMsg := pvecmQuorumStatus(ctx, timeout)
	if !pvecmAvailable {
		report.add("Cluster quorum", networkHealthWarn, "pvecm not available; cannot check quorum")
		return
	}
	if quorumMsg != "" {
		report.add("Cluster quorum", networkHealthWarn, quorumMsg)
		return
	}
	if quorumInfo.Quorate {
		report.add("Cluster quorum", networkHealthOK, quorumInfo.Summary())
	} else {
		report.add("Cluster quorum", networkHealthWarn, quorumInfo.Summary())
	}
}

func detectCorosyncConfig() (path string, ok bool) {
	candidates := []string{"/etc/pve/corosync.conf", "/etc/corosync/corosync.conf"}
	for _, candidate := range candidates {
		if _, err := restoreFS.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func mountpointCheck(ctx context.Context, path string, timeout time.Duration) (mounted bool, known bool, message string) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "mountpoint", "-q", path)
	_ = output
	if err == nil {
		return true, true, ""
	}
	if isExecNotFound(err) {
		return false, false, ""
	}
	if msg := strings.TrimSpace(string(output)); msg != "" {
		return false, true, msg
	}
	return false, true, ""
}

func systemctlServiceState(ctx context.Context, service string, timeout time.Duration) (state string, message string, available bool) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "systemctl", "is-active", service)
	if err != nil && isExecNotFound(err) {
		return "", "", false
	}
	text := strings.TrimSpace(string(output))
	lower := strings.ToLower(text)
	switch lower {
	case "active", "inactive", "failed", "activating", "deactivating", "unknown", "not-found":
		return lower, "", true
	}
	if text == "" && err != nil {
		return "", fmt.Sprintf("systemctl is-active %s failed: %v", service, err), true
	}
	if text == "" {
		return "", "systemctl returned no output", true
	}
	return "", strings.TrimSpace(text), true
}

func pvecmQuorumStatus(ctx context.Context, timeout time.Duration) (info pvecmStatusInfo, available bool, message string) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "pvecm", "status")
	if err != nil && isExecNotFound(err) {
		return pvecmStatusInfo{}, false, ""
	}
	text := string(output)
	info = parsePvecmStatus(text)
	if info.QuorateKnown {
		return info, true, ""
	}

	clean := strings.TrimSpace(text)
	if clean == "" && err != nil {
		return pvecmStatusInfo{}, true, fmt.Sprintf("pvecm status failed: %v", err)
	}
	if clean == "" {
		return pvecmStatusInfo{}, true, "pvecm status returned no output"
	}
	first := clean
	if strings.Contains(first, "\n") {
		first = strings.SplitN(first, "\n", 2)[0]
	}
	return pvecmStatusInfo{}, true, fmt.Sprintf("could not determine quorum: %s", first)
}

type pvecmStatusInfo struct {
	QuorateKnown bool
	Quorate      bool
	Nodes        string
	Expected     string
	TotalVotes   string
	RingAddrs    []string
}

func (i pvecmStatusInfo) Summary() string {
	var parts []string
	if i.QuorateKnown {
		if i.Quorate {
			parts = append(parts, "quorate=yes")
		} else {
			parts = append(parts, "quorate=no")
		}
	}
	if i.Nodes != "" {
		parts = append(parts, "nodes="+i.Nodes)
	}
	if i.Expected != "" {
		parts = append(parts, "expectedVotes="+i.Expected)
	}
	if i.TotalVotes != "" {
		parts = append(parts, "totalVotes="+i.TotalVotes)
	}
	if len(i.RingAddrs) > 0 {
		addrs := i.RingAddrs
		if len(addrs) > 3 {
			addrs = addrs[:3]
		}
		parts = append(parts, "ringAddrs="+strings.Join(addrs, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " ")
}

func parsePvecmStatus(output string) pvecmStatusInfo {
	var info pvecmStatusInfo
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Quorate:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Quorate:"))
			info.QuorateKnown = true
			info.Quorate = strings.EqualFold(val, "Yes")
			continue
		}
		if strings.HasPrefix(line, "Nodes:") {
			info.Nodes = strings.TrimSpace(strings.TrimPrefix(line, "Nodes:"))
			continue
		}
		if strings.HasPrefix(line, "Expected votes:") {
			info.Expected = strings.TrimSpace(strings.TrimPrefix(line, "Expected votes:"))
			continue
		}
		if strings.HasPrefix(line, "Total votes:") {
			info.TotalVotes = strings.TrimSpace(strings.TrimPrefix(line, "Total votes:"))
			continue
		}
		if strings.HasPrefix(line, "Ring") && strings.Contains(line, "_addr:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				addr := strings.TrimSpace(parts[1])
				if addr != "" {
					info.RingAddrs = append(info.RingAddrs, addr)
				}
			}
		}
	}
	return info
}

func isExecNotFound(err error) bool {
	if err == nil {
		return false
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) && errors.Is(execErr.Err, exec.ErrNotFound) {
		return true
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && errors.Is(pathErr.Err, os.ErrNotExist) {
		return true
	}
	return false
}
