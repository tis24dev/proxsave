package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type pveClusterMappingJSON struct {
	ID      string            `json:"id"`
	Comment string            `json:"comment,omitempty"`
	Map     []pveMappingEntry `json:"map,omitempty"`
}

// pveMappingEntry is one per-node mapping inside a cluster resource mapping.
// `pvesh get /cluster/mapping/<type> --output-format=json` emits each entry as a
// PVE property string ("node=pve01,path=0000:01:00.0,id=8086:1234"), which is the
// real on-disk backup format. We also accept an object ({"node":"pve01",...}) for
// robustness, since earlier code and fixtures assumed that (never-emitted) shape.
type pveMappingEntry struct {
	props map[string]string
}

func (e *pveMappingEntry) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		e.props = nil
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		e.props = parsePVEPropertyString(s)
		return nil
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return err
	}
	props := make(map[string]string, len(obj))
	for k, v := range obj {
		k = strings.TrimSpace(k)
		if k == "" || v == nil {
			continue
		}
		props[k] = strings.TrimSpace(fmt.Sprint(v))
	}
	e.props = props
	return nil
}

// parsePVEPropertyString splits a PVE property string "k=v,k=v" into its fields.
func parsePVEPropertyString(s string) map[string]string {
	out := make(map[string]string)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			continue
		}
		out[k] = strings.TrimSpace(v)
	}
	return out
}

type pveClusterMappingSpec struct {
	ID         string
	Comment    string
	MapEntries []string // canonical `k=v,k=v` entries (one per node mapping)
}

func maybeApplyPVEClusterResourceMappingsWithUI(ctx context.Context, ui RestoreWorkflowUI, logger *logging.Logger, exportRoot string) error {
	specsByType, total, err := loadPVEClusterResourceMappingsFromExport(exportRoot)
	if err != nil {
		return err
	}
	if total == 0 {
		return nil
	}

	var parts []string
	for _, typ := range []string{"pci", "usb", "dir"} {
		if n := len(specsByType[typ]); n > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", typ, n))
		}
	}
	summary := strings.Join(parts, ", ")
	if summary == "" {
		summary = fmt.Sprintf("total=%d", total)
	}

	message := fmt.Sprintf("Found %d resource mapping(s) (%s) in the backup.\n\nRecommended: apply mappings before VM/CT configs if your guests use mapping=<id> for PCI/USB passthrough.", total, summary)
	applyNow, err := ui.ConfirmAction(ctx, "Apply PVE resource mappings (pvesh)", message, "Apply now", "Skip apply", 0, false)
	if err != nil {
		return err
	}
	if !applyNow {
		logger.Info("Skipping resource mappings apply")
		return nil
	}

	applied := 0
	failed := 0
	for _, typ := range []string{"pci", "usb", "dir"} {
		specs := specsByType[typ]
		if len(specs) == 0 {
			continue
		}
		ok, bad := applyPVEClusterResourceMappings(ctx, logger, typ, specs)
		applied += ok
		failed += bad
	}

	if failed > 0 {
		return fmt.Errorf("applied=%d failed=%d", applied, failed)
	}
	logger.Info("Resource mappings apply completed: ok=%d failed=%d", applied, failed)
	return nil
}

func loadPVEClusterResourceMappingsFromExport(exportRoot string) (map[string][]pveClusterMappingSpec, int, error) {
	specsByType := make(map[string][]pveClusterMappingSpec, 3)
	total := 0

	for _, typ := range []string{"pci", "usb", "dir"} {
		specs, err := readPVEClusterResourceMappingsFromExport(exportRoot, typ)
		if err != nil {
			return nil, 0, err
		}
		if len(specs) > 0 {
			specsByType[typ] = specs
			total += len(specs)
		}
	}
	return specsByType, total, nil
}

func readPVEClusterResourceMappingsFromExport(exportRoot, mappingType string) ([]pveClusterMappingSpec, error) {
	mappingType = strings.TrimSpace(mappingType)
	if mappingType == "" {
		return nil, nil
	}

	path := filepath.Join(exportRoot, "var", "lib", "proxsave-info", "commands", "pve", fmt.Sprintf("mapping_%s.json", mappingType))
	data, err := restoreFS.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s mappings: %w", mappingType, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return nil, nil
	}

	var items []pveClusterMappingJSON
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		// Some environments wrap data in an object.
		var wrapper struct {
			Data []pveClusterMappingJSON `json:"data"`
		}
		if err2 := json.Unmarshal([]byte(raw), &wrapper); err2 != nil {
			return nil, fmt.Errorf("parse %s mappings JSON: %w", mappingType, err)
		}
		items = wrapper.Data
	}

	var specs []pveClusterMappingSpec
	for _, item := range items {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		spec := pveClusterMappingSpec{
			ID:      id,
			Comment: strings.TrimSpace(item.Comment),
		}

		for _, m := range item.Map {
			if rendered := renderMappingEntry(m.props); rendered != "" {
				spec.MapEntries = append(spec.MapEntries, rendered)
			}
		}

		spec.MapEntries = uniqueSortedStrings(spec.MapEntries)
		specs = append(specs, spec)
	}

	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs, nil
}

func applyPVEClusterResourceMappings(ctx context.Context, logger *logging.Logger, mappingType string, specs []pveClusterMappingSpec) (applied, failed int) {
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			logger.Warning("Resource mappings apply aborted: %v", err)
			return applied, failed
		}
		if err := applyPVEClusterResourceMapping(ctx, logger, mappingType, spec); err != nil {
			logger.Warning("Failed to apply %s mapping %s: %v", mappingType, spec.ID, err)
			failed++
		} else {
			logger.Info("Applied %s mapping %s", mappingType, spec.ID)
			applied++
		}
	}
	return applied, failed
}

func applyPVEClusterResourceMapping(ctx context.Context, logger *logging.Logger, mappingType string, spec pveClusterMappingSpec) error {
	mappingType = strings.TrimSpace(mappingType)
	id := strings.TrimSpace(spec.ID)
	if mappingType == "" || id == "" {
		return fmt.Errorf("invalid mapping (type=%q id=%q)", mappingType, id)
	}
	if len(spec.MapEntries) == 0 {
		return fmt.Errorf("mapping has no entries (type=%s id=%s)", mappingType, id)
	}

	createArgs := []string{"create", fmt.Sprintf("/cluster/mapping/%s", mappingType), "--id", id}
	if strings.TrimSpace(spec.Comment) != "" {
		createArgs = append(createArgs, "--comment", strings.TrimSpace(spec.Comment))
	}
	for _, entry := range spec.MapEntries {
		createArgs = append(createArgs, "--map", entry)
	}

	createErr := runPvesh(ctx, logger, createArgs)
	if createErr == nil {
		return nil
	}

	// Create failed. It commonly means the mapping already exists, in which case we
	// merge the live entries with the backup ones and update via set. Only do that if
	// we can actually READ the existing mapping: if the get fails or returns nothing
	// parseable, the create may have failed for another reason (invalid value,
	// permission denied, transient cluster lock) and a blind set could overwrite a
	// live mapping with only the backup entries or mask the real cause - so surface
	// the original create error instead.
	getArgs := []string{"get", fmt.Sprintf("/cluster/mapping/%s/%s", mappingType, id), "--output-format=json"}
	out, getErr := runPveshSensitive(ctx, logger, getArgs)
	var existing pveClusterMappingSpec
	ok := false
	if getErr == nil && len(out) > 0 {
		if parsed, parsedOK, parseErr := parsePVEClusterMappingObject(out); parseErr == nil && parsedOK {
			existing, ok = parsed, true
		}
	}
	if !ok {
		return fmt.Errorf("create %s mapping %q failed and the existing mapping could not be read (get error: %v): %w", mappingType, id, getErr, createErr)
	}

	mergedEntries := uniqueSortedStrings(append(existing.MapEntries, spec.MapEntries...))
	comment := strings.TrimSpace(spec.Comment)
	if comment == "" {
		comment = strings.TrimSpace(existing.Comment)
	}

	setArgs := []string{"set", fmt.Sprintf("/cluster/mapping/%s/%s", mappingType, id)}
	if comment != "" {
		setArgs = append(setArgs, "--comment", comment)
	}
	for _, entry := range mergedEntries {
		setArgs = append(setArgs, "--map", entry)
	}

	return runPvesh(ctx, logger, setArgs)
}

func parsePVEClusterMappingObject(data []byte) (pveClusterMappingSpec, bool, error) {
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return pveClusterMappingSpec{}, false, nil
	}

	var obj pveClusterMappingJSON
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		// Some endpoints return arrays even for a single mapping.
		var arr []pveClusterMappingJSON
		if err2 := json.Unmarshal([]byte(raw), &arr); err2 != nil || len(arr) == 0 {
			return pveClusterMappingSpec{}, false, err
		}
		obj = arr[0]
	}

	id := strings.TrimSpace(obj.ID)
	if id == "" {
		return pveClusterMappingSpec{}, false, nil
	}
	spec := pveClusterMappingSpec{
		ID:      id,
		Comment: strings.TrimSpace(obj.Comment),
	}
	for _, m := range obj.Map {
		if rendered := renderMappingEntry(m.props); rendered != "" {
			spec.MapEntries = append(spec.MapEntries, rendered)
		}
	}
	spec.MapEntries = uniqueSortedStrings(spec.MapEntries)
	return spec, true, nil
}

func renderMappingEntry(entry map[string]string) string {
	if len(entry) == 0 {
		return ""
	}

	// Prefer stable ordering: node, path, id, then the rest alphabetically.
	var keys []string
	for k := range entry {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.TrimSpace(entry[k]) == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return ""
	}

	priority := func(k string) int {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "node":
			return 0
		case "path":
			return 1
		case "id":
			return 2
		default:
			return 3
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		pi := priority(keys[i])
		pj := priority(keys[j])
		if pi != pj {
			return pi < pj
		}
		return strings.ToLower(keys[i]) < strings.ToLower(keys[j])
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(entry[k])
		if v == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
