package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type networkInventory struct {
	GeneratedAt string                    `json:"generated_at"`
	Hostname    string                    `json:"hostname"`
	Interfaces  []networkInterfaceProfile `json:"interfaces"`
}

type networkInterfaceProfile struct {
	Name          string            `json:"name"`
	MAC           string            `json:"mac,omitempty"`
	PermanentMAC  string            `json:"permanent_mac,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	PCIPath       string            `json:"pci_path,omitempty"`
	IfIndex       int               `json:"ifindex,omitempty"`
	OperState     string            `json:"oper_state,omitempty"`
	SpeedMbps     int               `json:"speed_mbps,omitempty"`
	IsVirtual     bool              `json:"is_virtual,omitempty"`
	UdevProps     map[string]string `json:"udev_properties,omitempty"`
	SystemNetPath string            `json:"system_net_path,omitempty"`
}

func (c *Collector) collectNetworkInventory(ctx context.Context, commandsDir, infoDir string) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	sysNet := c.systemPath("/sys/class/net")
	entries, err := os.ReadDir(sysNet)
	if err != nil {
		c.logger.Debug("Network inventory skipped: unable to read %s: %v", sysNet, err)
		return nil
	}

	inv := networkInventory{
		GeneratedAt: time.Now().Format(time.RFC3339),
	}
	if host, err := os.Hostname(); err == nil {
		inv.Hostname = host
	}

	for _, entry := range entries {
		if entry == nil {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if name == "" {
			continue
		}

		netPath := filepath.Join(sysNet, name)
		profile := networkInterfaceProfile{
			Name:          name,
			MAC:           readTrimmedLine(filepath.Join(netPath, "address"), 64),
			IfIndex:       readIntLine(filepath.Join(netPath, "ifindex")),
			OperState:     readTrimmedLine(filepath.Join(netPath, "operstate"), 32),
			SpeedMbps:     readIntLine(filepath.Join(netPath, "speed")),
			SystemNetPath: netPath,
		}
		if profile.IfIndex <= 0 {
			profile.IfIndex = 0
		}
		if profile.SpeedMbps <= 0 {
			profile.SpeedMbps = 0
		}

		if link, err := os.Readlink(netPath); err == nil && strings.Contains(link, "/virtual/") {
			profile.IsVirtual = true
		}
		if devPath, err := filepath.EvalSymlinks(filepath.Join(netPath, "device")); err == nil {
			profile.PCIPath = devPath
		}
		if driverPath, err := filepath.EvalSymlinks(filepath.Join(netPath, "device/driver")); err == nil {
			profile.Driver = filepath.Base(driverPath)
		}

		if c.shouldRunHostCommands() {
			if props, err := c.readUdevProperties(ctx, netPath); err == nil && len(props) > 0 {
				profile.UdevProps = props
			}
			if permMAC, err := c.readPermanentMAC(ctx, name); err == nil && permMAC != "" {
				profile.PermanentMAC = permMAC
			}
			if profile.Driver == "" {
				if drv, err := c.readDriverFromEthtool(ctx, name); err == nil && drv != "" {
					profile.Driver = drv
				}
			}
		}

		inv.Interfaces = append(inv.Interfaces, profile)
	}

	sort.Slice(inv.Interfaces, func(i, j int) bool {
		return inv.Interfaces[i].Name < inv.Interfaces[j].Name
	})

	data, err := json.MarshalIndent(inv, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal network inventory: %w", err)
	}

	primary := filepath.Join(commandsDir, "network_inventory.json")
	if err := c.writeReportFile(primary, data); err != nil {
		return err
	}
	if infoDir != "" {
		mirror := filepath.Join(infoDir, "network_inventory.json")
		if err := c.writeReportFile(mirror, data); err != nil {
			return err
		}
	}
	return nil
}

func (c *Collector) shouldRunHostCommands() bool {
	root := strings.TrimSpace(c.config.SystemRootPrefix)
	return root == "" || root == string(filepath.Separator)
}

func (c *Collector) readUdevProperties(ctx context.Context, netPath string) (map[string]string, error) {
	if _, err := c.depLookPath("udevadm"); err != nil {
		return nil, err
	}
	output, err := c.depRunCommand(ctx, "udevadm", "info", "-q", "property", "-p", netPath)
	if err != nil {
		return nil, err
	}
	props := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if key != "" {
			props[key] = val
		}
	}
	return props, nil
}

func (c *Collector) readPermanentMAC(ctx context.Context, iface string) (string, error) {
	if _, err := c.depLookPath("ethtool"); err != nil {
		return "", err
	}
	output, err := c.depRunCommand(ctx, "ethtool", "-P", iface)
	if err != nil {
		return "", err
	}
	return parseEthtoolPermanentMAC(string(output)), nil
}

func (c *Collector) readDriverFromEthtool(ctx context.Context, iface string) (string, error) {
	if _, err := c.depLookPath("ethtool"); err != nil {
		return "", err
	}
	output, err := c.depRunCommand(ctx, "ethtool", "-i", iface)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "driver:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "driver:")), nil
		}
	}
	return "", nil
}

func parseEthtoolPermanentMAC(output string) string {
	const prefix = "permanent address:"
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, prefix) {
			return strings.ToLower(strings.TrimSpace(line[len(prefix):]))
		}
	}
	return ""
}

func readTrimmedLine(path string, max int) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if max > 0 && len(line) > max {
		return line[:max]
	}
	return line
}

func readIntLine(path string) int {
	raw := readTrimmedLine(path, 32)
	if raw == "" {
		return 0
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return v
}
