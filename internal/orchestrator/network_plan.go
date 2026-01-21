package orchestrator

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

type networkEndpoint struct {
	Interface string
	Addresses []string
	Gateway   string
}

func (e networkEndpoint) summary() string {
	iface := strings.TrimSpace(e.Interface)
	if iface == "" {
		iface = "n/a"
	}
	addrs := strings.Join(compactStrings(e.Addresses), ",")
	if strings.TrimSpace(addrs) == "" {
		addrs = "n/a"
	}
	gw := strings.TrimSpace(e.Gateway)
	if gw == "" {
		gw = "n/a"
	}
	return fmt.Sprintf("iface=%s ip=%s gw=%s", iface, addrs, gw)
}

func buildNetworkPlanReport(ctx context.Context, logger *logging.Logger, iface, source string, timeout time.Duration) (string, error) {
	if strings.TrimSpace(iface) == "" {
		return fmt.Sprintf("Network plan\n\n- Management interface: n/a\n- Detection source: %s\n", strings.TrimSpace(source)), nil
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	current, _ := currentNetworkEndpoint(ctx, iface, timeout)
	target, _ := targetNetworkEndpointFromConfig(logger, iface)

	var b strings.Builder
	b.WriteString("Network plan\n\n")
	b.WriteString(fmt.Sprintf("- Management interface: %s\n", strings.TrimSpace(iface)))
	if strings.TrimSpace(source) != "" {
		b.WriteString(fmt.Sprintf("- Detection source: %s\n", strings.TrimSpace(source)))
	}
	b.WriteString(fmt.Sprintf("- Current runtime: %s\n", current.summary()))
	b.WriteString(fmt.Sprintf("- Target config:  %s\n", target.summary()))
	return b.String(), nil
}

func currentNetworkEndpoint(ctx context.Context, iface string, timeout time.Duration) (networkEndpoint, error) {
	ep := networkEndpoint{Interface: strings.TrimSpace(iface)}
	if ep.Interface == "" {
		return ep, fmt.Errorf("empty interface")
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	addrs, err := ipGlobalAddresses(ctx, ep.Interface, timeout)
	if err != nil {
		return ep, err
	}
	ep.Addresses = addrs

	route, err := ipDefaultRoute(ctx, timeout)
	if err != nil {
		return ep, err
	}
	ep.Gateway = strings.TrimSpace(route.Via)
	return ep, nil
}

func targetNetworkEndpointFromConfig(logger *logging.Logger, iface string) (networkEndpoint, error) {
	ep := networkEndpoint{Interface: strings.TrimSpace(iface)}
	if ep.Interface == "" {
		return ep, fmt.Errorf("empty interface")
	}

	paths, err := collectIfupdownConfigPaths()
	if err != nil {
		return ep, err
	}
	for _, p := range paths {
		data, err := restoreFS.ReadFile(p)
		if err != nil {
			continue
		}
		addrs, gw, found := parseIfupdownStanzaForInterface(string(data), ep.Interface)
		if !found {
			continue
		}
		if len(addrs) > 0 {
			ep.Addresses = append(ep.Addresses, addrs...)
		}
		if strings.TrimSpace(gw) != "" && strings.TrimSpace(ep.Gateway) == "" {
			ep.Gateway = strings.TrimSpace(gw)
		}
	}
	ep.Addresses = uniqueStrings(ep.Addresses)
	sort.Strings(ep.Addresses)
	return ep, nil
}

func collectIfupdownConfigPaths() ([]string, error) {
	paths := []string{"/etc/network/interfaces"}
	entries, err := restoreFS.ReadDir("/etc/network/interfaces.d")
	if err == nil {
		for _, entry := range entries {
			if entry == nil || entry.IsDir() {
				continue
			}
			name := strings.TrimSpace(entry.Name())
			if name == "" {
				continue
			}
			paths = append(paths, filepath.Join("/etc/network/interfaces.d", name))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func parseIfupdownStanzaForInterface(config string, iface string) (addresses []string, gateway string, found bool) {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return nil, "", false
	}

	var currentIface string
	for _, raw := range strings.Split(config, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if fields := strings.Fields(line); len(fields) >= 4 && fields[0] == "iface" && fields[2] == "inet" {
			currentIface = fields[1]
			continue
		}
		if currentIface != iface {
			continue
		}

		if fields := strings.Fields(line); len(fields) >= 2 {
			switch fields[0] {
			case "address":
				addresses = append(addresses, fields[1])
				found = true
			case "gateway":
				if gateway == "" {
					gateway = fields[1]
				}
				found = true
			}
		}
	}
	return addresses, gateway, found
}

func compactStrings(values []string) []string {
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
