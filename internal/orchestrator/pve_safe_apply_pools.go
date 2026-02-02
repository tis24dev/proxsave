package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type pvePoolSpec struct {
	ID       string
	Comment  string
	VMIDs    []string
	Storages []string
}

func readPVEPoolsFromExportUserCfg(exportRoot string) ([]pvePoolSpec, error) {
	userCfg := filepath.Join(exportRoot, "etc", "pve", "user.cfg")
	data, err := restoreFS.ReadFile(userCfg)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read exported user.cfg: %w", err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, nil
	}

	sections, err := parseProxmoxNotificationSections(raw)
	if err != nil {
		return nil, fmt.Errorf("parse user.cfg: %w", err)
	}

	var pools []pvePoolSpec
	for _, s := range sections {
		if !strings.EqualFold(strings.TrimSpace(s.Type), "pool") {
			continue
		}
		id := strings.TrimSpace(s.Name)
		if id == "" {
			continue
		}

		spec := pvePoolSpec{ID: id}
		for _, kv := range s.Entries {
			key := strings.ToLower(strings.TrimSpace(kv.Key))
			val := strings.TrimSpace(kv.Value)
			switch key {
			case "comment":
				spec.Comment = val
			case "vms":
				spec.VMIDs = splitProxmoxCSV(val)
			case "storage":
				spec.Storages = splitProxmoxCSV(val)
			}
		}

		spec.VMIDs = uniqueSortedStrings(spec.VMIDs)
		spec.Storages = uniqueSortedStrings(spec.Storages)
		pools = append(pools, spec)
	}

	sort.Slice(pools, func(i, j int) bool { return pools[i].ID < pools[j].ID })
	return pools, nil
}

func summarizePoolIDs(pools []pvePoolSpec, max int) string {
	if len(pools) == 0 || max <= 0 {
		return ""
	}
	var names []string
	for _, p := range pools {
		if strings.TrimSpace(p.ID) != "" {
			names = append(names, strings.TrimSpace(p.ID))
		}
	}
	names = uniqueSortedStrings(names)
	if len(names) == 0 {
		return ""
	}
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(names[:max], ", "), len(names)-max)
}

func anyPoolHasVMs(pools []pvePoolSpec) bool {
	for _, p := range pools {
		if len(p.VMIDs) > 0 {
			return true
		}
	}
	return false
}

func listPVEPoolIDs(ctx context.Context) (map[string]struct{}, error) {
	output, err := restoreCmd.Run(ctx, "pveum", "pool", "list")
	raw := strings.TrimSpace(string(output))
	if raw == "" {
		if err != nil {
			return nil, fmt.Errorf("pveum pool list failed: %w", err)
		}
		return nil, nil
	}

	out := make(map[string]struct{})
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "poolid") {
			continue
		}
		out[strings.TrimSpace(fields[0])] = struct{}{}
	}
	if err != nil {
		return out, fmt.Errorf("pveum pool list failed: %w", err)
	}
	return out, nil
}

func pvePoolAlreadyExists(existing map[string]struct{}, id string, addOutput []byte) bool {
	if existing != nil {
		if _, ok := existing[id]; ok {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(string(addOutput)))
	return strings.Contains(msg, "already exists") || strings.Contains(msg, "already exist")
}

func applyPVEPoolsDefinitions(ctx context.Context, logger *logging.Logger, pools []pvePoolSpec) (applied, failed int, err error) {
	if len(pools) == 0 {
		return 0, 0, nil
	}
	if _, lookErr := exec.LookPath("pveum"); lookErr != nil {
		return 0, 0, fmt.Errorf("pveum not found in PATH")
	}

	done := logging.DebugStart(logger, "pve pools apply (definitions)", "pools=%d", len(pools))
	defer func() { done(err) }()

	existingPools, listErr := listPVEPoolIDs(ctx)
	if listErr != nil {
		logger.Debug("Pools: unable to list existing pools: %v", listErr)
	}

	for _, pool := range pools {
		if err := ctx.Err(); err != nil {
			return applied, failed, err
		}
		id := strings.TrimSpace(pool.ID)
		if id == "" {
			continue
		}

		comment := strings.TrimSpace(pool.Comment)
		ok := false

		addArgs := []string{"pool", "add", id}
		if comment != "" {
			addArgs = append(addArgs, "--comment", comment)
		}
		addOut, addErr := restoreCmd.Run(ctx, "pveum", addArgs...)
		if addErr != nil {
			logger.Debug("Pools: add %s failed (may already exist): %v", id, addErr)
			if comment == "" && pvePoolAlreadyExists(existingPools, id, addOut) {
				ok = true
			}
		} else {
			ok = true
			if existingPools != nil {
				existingPools[id] = struct{}{}
			}
		}

		// Ensure comment is applied even if the pool already existed.
		if comment != "" {
			modArgs := []string{"pool", "modify", id, "--comment", comment}
			if _, modErr := restoreCmd.Run(ctx, "pveum", modArgs...); modErr != nil {
				logger.Warning("Pools: failed to set comment for %s: %v", id, modErr)
			} else {
				ok = true
			}
		}

		if ok {
			applied++
			logger.Info("Applied pool definition %s", id)
		} else {
			failed++
		}
	}

	if failed > 0 {
		return applied, failed, fmt.Errorf("applied=%d failed=%d", applied, failed)
	}
	return applied, failed, nil
}

func applyPVEPoolsMembership(ctx context.Context, logger *logging.Logger, pools []pvePoolSpec, allowMove bool) (applied, failed int, err error) {
	if len(pools) == 0 {
		return 0, 0, nil
	}
	if _, lookErr := exec.LookPath("pveum"); lookErr != nil {
		return 0, 0, fmt.Errorf("pveum not found in PATH")
	}

	done := logging.DebugStart(logger, "pve pools apply (membership)", "pools=%d allowMove=%v", len(pools), allowMove)
	defer func() { done(err) }()

	for _, pool := range pools {
		if err := ctx.Err(); err != nil {
			return applied, failed, err
		}
		id := strings.TrimSpace(pool.ID)
		if id == "" {
			continue
		}

		vmids := uniqueSortedStrings(pool.VMIDs)
		storages := uniqueSortedStrings(pool.Storages)
		if len(vmids) == 0 && len(storages) == 0 {
			continue
		}

		args := []string{"pool", "modify", id}
		if allowMove && len(vmids) > 0 {
			args = append(args, "--allow-move", "1")
		}
		if len(vmids) > 0 {
			args = append(args, "--vms", strings.Join(vmids, ","))
		}
		if len(storages) > 0 {
			args = append(args, "--storage", strings.Join(storages, ","))
		}

		if _, applyErr := restoreCmd.Run(ctx, "pveum", args...); applyErr != nil {
			logger.Warning("Pools: failed to apply membership for %s: %v", id, applyErr)
			failed++
			continue
		}

		applied++
		logger.Info("Applied pool membership %s", id)
	}

	if failed > 0 {
		return applied, failed, fmt.Errorf("applied=%d failed=%d", applied, failed)
	}
	return applied, failed, nil
}

func splitProxmoxCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', ' ', '\t', '\n', '\r':
			return true
		default:
			return false
		}
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func uniqueSortedStrings(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	var out []string
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
