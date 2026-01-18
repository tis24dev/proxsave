package orchestrator

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

var dnsLookupHostFunc = func(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

var dialContextFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

type networkHealthSeverity int

const (
	networkHealthOK networkHealthSeverity = iota
	networkHealthWarn
	networkHealthCritical
)

func (s networkHealthSeverity) String() string {
	switch s {
	case networkHealthOK:
		return "OK"
	case networkHealthWarn:
		return "WARN"
	case networkHealthCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

type networkHealthCheck struct {
	Name     string
	Severity networkHealthSeverity
	Message  string
}

type networkHealthReport struct {
	Severity    networkHealthSeverity
	Checks      []networkHealthCheck
	GeneratedAt time.Time
}

func (r *networkHealthReport) add(name string, severity networkHealthSeverity, message string) {
	r.Checks = append(r.Checks, networkHealthCheck{
		Name:     name,
		Severity: severity,
		Message:  message,
	})
	if severity > r.Severity {
		r.Severity = severity
	}
}

func (r networkHealthReport) Summary() string {
	return fmt.Sprintf("Network health: %s", r.Severity.String())
}

func (r networkHealthReport) Details() string {
	var b strings.Builder
	b.WriteString(r.Summary())
	b.WriteString("\n")
	for _, c := range r.Checks {
		b.WriteString(fmt.Sprintf("- [%s] %s: %s\n", c.Severity.String(), c.Name, c.Message))
	}
	return strings.TrimRight(b.String(), "\n")
}

type networkHealthOptions struct {
	SystemType         SystemType
	Logger             *logging.Logger
	CommandTimeout     time.Duration
	EnableGatewayPing  bool
	ForceSSHRouteCheck bool
	EnableDNSResolve   bool
	DNSResolveHost     string
	LocalPortChecks    []tcpPortCheck
}

func defaultNetworkHealthOptions() networkHealthOptions {
	return networkHealthOptions{
		SystemType:         SystemTypeUnknown,
		Logger:             nil,
		CommandTimeout:     3 * time.Second,
		EnableGatewayPing:  true,
		ForceSSHRouteCheck: false,
		EnableDNSResolve:   true,
	}
}

type tcpPortCheck struct {
	Name    string
	Address string
	Port    int
}

type ipRouteInfo struct {
	Dev string
	Src string
	Via string
}

type ipLinkInfo struct {
	State string
}

func runNetworkHealthChecks(ctx context.Context, opts networkHealthOptions) networkHealthReport {
	done := logging.DebugStart(opts.Logger, "network health checks", "systemType=%s timeout=%s", opts.SystemType, opts.CommandTimeout)
	defer done(nil)
	if opts.CommandTimeout <= 0 {
		opts.CommandTimeout = 3 * time.Second
	}
	report := networkHealthReport{
		Severity:    networkHealthOK,
		GeneratedAt: nowRestore(),
	}

	logging.DebugStep(opts.Logger, "network health checks", "SSH route check")
	sshIP := parseSSHClientIP()
	var sshRoute ipRouteInfo
	var sshRouteErr error
	if sshIP != "" {
		sshRoute, sshRouteErr = ipRouteGet(ctx, sshIP, opts.CommandTimeout)
		switch {
		case sshRouteErr != nil:
			report.add("SSH route", networkHealthCritical, fmt.Sprintf("ip route get %s failed: %v", sshIP, sshRouteErr))
		case sshRoute.Dev == "":
			report.add("SSH route", networkHealthCritical, fmt.Sprintf("ip route get %s returned no interface", sshIP))
		default:
			msg := fmt.Sprintf("client=%s dev=%s src=%s", sshIP, sshRoute.Dev, sshRoute.Src)
			if sshRoute.Via != "" {
				msg += " via=" + sshRoute.Via
			}
			report.add("SSH route", networkHealthOK, msg)
		}
	} else if opts.ForceSSHRouteCheck {
		report.add("SSH route", networkHealthWarn, "no SSH client detected (SSH_CONNECTION/SSH_CLIENT not set)")
	} else {
		report.add("SSH route", networkHealthOK, "not running under SSH")
	}

	logging.DebugStep(opts.Logger, "network health checks", "Default route check")
	defaultRoute, defaultRouteErr := ipDefaultRoute(ctx, opts.CommandTimeout)
	switch {
	case defaultRouteErr != nil:
		report.add("Default route", networkHealthWarn, fmt.Sprintf("ip route show default failed: %v", defaultRouteErr))
	case defaultRoute.Dev == "" && defaultRoute.Via == "":
		report.add("Default route", networkHealthWarn, "no default route found")
	default:
		msg := fmt.Sprintf("dev=%s", defaultRoute.Dev)
		if defaultRoute.Via != "" {
			msg += " via=" + defaultRoute.Via
		}
		report.add("Default route", networkHealthOK, msg)
	}

	validationDev := sshRoute.Dev
	if validationDev == "" {
		validationDev = defaultRoute.Dev
	}
	if strings.TrimSpace(validationDev) == "" {
		report.add("Interface", networkHealthWarn, "no interface to validate (no SSH route and no default route)")
	} else {
		logging.DebugStep(opts.Logger, "network health checks", "Validate link/address on %s", validationDev)
		linkInfo, linkErr := ipLinkShow(ctx, validationDev, opts.CommandTimeout)
		if linkErr != nil {
			report.add("Link", networkHealthWarn, fmt.Sprintf("%s: ip link show failed: %v", validationDev, linkErr))
		} else if linkInfo.State == "" {
			report.add("Link", networkHealthWarn, fmt.Sprintf("%s: link state unknown", validationDev))
		} else if strings.EqualFold(linkInfo.State, "UP") {
			report.add("Link", networkHealthOK, fmt.Sprintf("%s: state=%s", validationDev, linkInfo.State))
		} else {
			report.add("Link", networkHealthWarn, fmt.Sprintf("%s: state=%s", validationDev, linkInfo.State))
		}

		addrs, addrErr := ipGlobalAddresses(ctx, validationDev, opts.CommandTimeout)
		if addrErr != nil {
			report.add("Addresses", networkHealthWarn, fmt.Sprintf("%s: ip addr show failed: %v", validationDev, addrErr))
		} else if len(addrs) == 0 {
			report.add("Addresses", networkHealthWarn, fmt.Sprintf("%s: no global addresses detected", validationDev))
		} else {
			msg := fmt.Sprintf("%s: %s", validationDev, strings.Join(addrs, ", "))
			report.add("Addresses", networkHealthOK, msg)
		}

		gw := strings.TrimSpace(sshRoute.Via)
		if gw == "" {
			gw = strings.TrimSpace(defaultRoute.Via)
		}
		if opts.EnableGatewayPing && gw != "" {
			logging.DebugStep(opts.Logger, "network health checks", "Gateway ping check (%s)", gw)
			if !commandAvailable("ping") {
				report.add("Gateway", networkHealthWarn, fmt.Sprintf("ping not available (gateway=%s)", gw))
			} else if pingGateway(ctx, gw, opts.CommandTimeout) {
				report.add("Gateway", networkHealthOK, fmt.Sprintf("%s: ping ok", gw))
			} else {
				report.add("Gateway", networkHealthWarn, fmt.Sprintf("%s: ping failed (may be blocked)", gw))
			}
		}
	}

	if opts.EnableDNSResolve {
		logging.DebugStep(opts.Logger, "network health checks", "DNS config/resolve check")
		nameservers, err := readResolvConfNameservers()
		switch {
		case err != nil:
			report.add("DNS config", networkHealthWarn, fmt.Sprintf("read /etc/resolv.conf failed: %v", err))
		case len(nameservers) == 0:
			report.add("DNS config", networkHealthWarn, "no nameserver entries in /etc/resolv.conf")
		default:
			report.add("DNS config", networkHealthOK, fmt.Sprintf("nameservers: %s", strings.Join(nameservers, ", ")))
		}

		host := strings.TrimSpace(opts.DNSResolveHost)
		if host == "" {
			host = defaultDNSTestHost()
		}
		if host != "" {
			logging.DebugStep(opts.Logger, "network health checks", "Resolve %s", host)
			ctxTimeout, cancel := context.WithTimeout(ctx, opts.CommandTimeout)
			ips, err := dnsLookupHostFunc(ctxTimeout, host)
			cancel()
			if err != nil {
				report.add("DNS resolve", networkHealthWarn, fmt.Sprintf("resolve %s failed: %v", host, err))
			} else if len(ips) == 0 {
				report.add("DNS resolve", networkHealthWarn, fmt.Sprintf("resolve %s returned no addresses", host))
			} else {
				preview := ips
				if len(preview) > 3 {
					preview = preview[:3]
				}
				msg := fmt.Sprintf("%s -> %s", host, strings.Join(preview, ", "))
				if len(ips) > len(preview) {
					msg += fmt.Sprintf(" (+%d more)", len(ips)-len(preview))
				}
				report.add("DNS resolve", networkHealthOK, msg)
			}
		}
	}

	if len(opts.LocalPortChecks) > 0 {
		for _, check := range opts.LocalPortChecks {
			logging.DebugStep(opts.Logger, "network health checks", "Local port check: %s %s:%d", strings.TrimSpace(check.Name), strings.TrimSpace(check.Address), check.Port)
			name := strings.TrimSpace(check.Name)
			if name == "" {
				name = "Local port"
			}
			addr := strings.TrimSpace(check.Address)
			if addr == "" {
				addr = "127.0.0.1"
			}
			if check.Port <= 0 || check.Port > 65535 {
				report.add(name, networkHealthWarn, fmt.Sprintf("invalid port: %d", check.Port))
				continue
			}
			target := fmt.Sprintf("%s:%d", addr, check.Port)
			ctxTimeout, cancel := context.WithTimeout(ctx, opts.CommandTimeout)
			conn, err := dialContextFunc(ctxTimeout, "tcp", target)
			cancel()
			if err != nil {
				report.add(name, networkHealthWarn, fmt.Sprintf("%s: connect failed: %v", target, err))
				continue
			}
			_ = conn.Close()
			report.add(name, networkHealthOK, fmt.Sprintf("%s: reachable", target))
		}
	}

	if opts.SystemType == SystemTypePVE {
		logging.DebugStep(opts.Logger, "network health checks", "Cluster (corosync/quorum) check")
		runCorosyncClusterHealthChecks(ctx, opts.CommandTimeout, opts.Logger, &report)
	}

	logging.DebugStep(opts.Logger, "network health checks", "Done (severity=%s)", report.Severity.String())
	return report
}

func logNetworkHealthReport(logger *logging.Logger, report networkHealthReport) {
	if logger == nil {
		return
	}
	switch report.Severity {
	case networkHealthCritical, networkHealthWarn:
		logger.Warning("%s", report.Summary())
	default:
		logger.Info("%s", report.Summary())
	}
	logger.Debug("Network health details:\n%s", report.Details())
}

func defaultDNSTestHost() string {
	if v := strings.TrimSpace(os.Getenv("PROXSAVE_DNS_TEST_HOST")); v != "" {
		return v
	}
	return "proxmox.com"
}

func readResolvConfNameservers() ([]string, error) {
	data, err := restoreFS.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "nameserver") {
			out = append(out, fields[1])
		}
	}
	return out, nil
}

func ipRouteGet(ctx context.Context, dest string, timeout time.Duration) (ipRouteInfo, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "ip", "route", "get", dest)
	if err != nil {
		return ipRouteInfo{}, err
	}
	return parseIPRouteInfo(string(output)), nil
}

func ipDefaultRoute(ctx context.Context, timeout time.Duration) (ipRouteInfo, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "ip", "route", "show", "default")
	if err != nil {
		return ipRouteInfo{}, err
	}
	text := strings.TrimSpace(string(output))
	if text == "" {
		return ipRouteInfo{}, nil
	}
	first := strings.SplitN(text, "\n", 2)[0]
	return parseIPRouteInfo(first), nil
}

func parseIPRouteInfo(output string) ipRouteInfo {
	fields := strings.Fields(output)
	info := ipRouteInfo{}
	for i := 0; i < len(fields)-1; i++ {
		switch fields[i] {
		case "dev":
			info.Dev = fields[i+1]
		case "src":
			info.Src = fields[i+1]
		case "via":
			info.Via = fields[i+1]
		}
	}
	return info
}

func ipLinkShow(ctx context.Context, iface string, timeout time.Duration) (ipLinkInfo, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "ip", "-o", "link", "show", "dev", iface)
	if err != nil {
		return ipLinkInfo{}, err
	}
	return parseIPLinkInfo(string(output)), nil
}

func parseIPLinkInfo(output string) ipLinkInfo {
	fields := strings.Fields(output)
	info := ipLinkInfo{}
	for i := 0; i < len(fields)-1; i++ {
		if fields[i] == "state" {
			info.State = fields[i+1]
			break
		}
	}
	return info
}

func ipGlobalAddresses(ctx context.Context, iface string, timeout time.Duration) ([]string, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	output, err := restoreCmd.Run(ctxTimeout, "ip", "-o", "addr", "show", "dev", iface, "scope", "global")
	if err != nil {
		return nil, err
	}

	var addrs []string
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		for i := 0; i < len(fields)-1; i++ {
			if fields[i] == "inet" || fields[i] == "inet6" {
				addrs = append(addrs, fields[i+1])
				break
			}
		}
	}
	return addrs, nil
}

func pingGateway(ctx context.Context, gw string, timeout time.Duration) bool {
	ctxTimeout, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-c", "1", "-W", "1", gw}
	if strings.Contains(gw, ":") {
		args = []string{"-6", "-c", "1", "-W", "1", gw}
	}
	_, err := restoreCmd.Run(ctxTimeout, "ping", args...)
	return err == nil
}
