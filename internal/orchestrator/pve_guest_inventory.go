package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type pveGuestResource struct {
	VMID   int    `json:"vmid"`
	Node   string `json:"node"`
	Kind   string `json:"type"`
	Status string `json:"status"`
	Name   string `json:"name"`
}

func loadPVEGuestInventory(ctx context.Context) (map[string]pveGuestResource, error) {
	const endpoint = "/cluster/resources"
	out, err := restoreCmd.Run(ctx, "pvesh", "get", endpoint, "--type", "vm", "--output-format=json")
	if err != nil {
		return nil, fmt.Errorf("pvesh get %s: %w", endpoint, err)
	}

	payload := bytes.TrimSpace(out)
	if len(payload) == 0 || payload[0] != '[' {
		return nil, fmt.Errorf("parse PVE guest inventory: expected a JSON array")
	}

	var resources []pveGuestResource
	if err := json.Unmarshal(payload, &resources); err != nil {
		return nil, fmt.Errorf("parse PVE guest inventory: %w", err)
	}

	inventory := make(map[string]pveGuestResource, len(resources))
	for index, resource := range resources {
		resource.Node = strings.TrimSpace(resource.Node)
		resource.Kind = strings.ToLower(strings.TrimSpace(resource.Kind))
		resource.Status = strings.ToLower(strings.TrimSpace(resource.Status))
		resource.Name = strings.TrimSpace(resource.Name)
		if resource.VMID <= 0 {
			return nil, fmt.Errorf("parse PVE guest inventory: item %d has an invalid vmid", index)
		}
		if resource.Node == "" {
			return nil, fmt.Errorf("parse PVE guest inventory: vmid %d has no node", resource.VMID)
		}
		if resource.Kind != "qemu" && resource.Kind != "lxc" {
			return nil, fmt.Errorf("parse PVE guest inventory: vmid %d has invalid type %q", resource.VMID, resource.Kind)
		}

		vmid := strconv.Itoa(resource.VMID)
		if _, exists := inventory[vmid]; exists {
			return nil, fmt.Errorf("parse PVE guest inventory: duplicate vmid %s", vmid)
		}
		inventory[vmid] = resource
	}
	return inventory, nil
}
