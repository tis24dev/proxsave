// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

func normalizePBSDatastoreCfg(path string, logger *logging.Logger) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read datastore.cfg: %w", err)
	}

	normalized, fixed := normalizePBSDatastoreCfgContent(string(raw))
	if fixed == 0 {
		logger.Debug("PBS datastore.cfg: formatting looks OK (no normalization needed)")
		return nil
	}

	if err := os.MkdirAll("/tmp/proxsave", 0o755); err != nil {
		return fmt.Errorf("ensure /tmp/proxsave exists: %w", err)
	}

	backupPath := filepath.Join("/tmp/proxsave", fmt.Sprintf("datastore.cfg.pre-normalize.%s", nowRestore().Format("20060102-150405")))
	if err := os.WriteFile(backupPath, raw, 0o600); err != nil {
		return fmt.Errorf("write backup copy: %w", err)
	}

	mode := datastoreCfgMode(path)
	tmpPath := fmt.Sprintf("%s.proxsave.tmp", path)
	if err := os.WriteFile(tmpPath, []byte(normalized), mode); err != nil {
		return fmt.Errorf("write normalized datastore.cfg: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace datastore.cfg: %w", err)
	}

	logger.Warning("PBS datastore.cfg: fixed %d malformed line(s) (properties must be indented); backup saved to %s", fixed, backupPath)
	return nil
}

func datastoreCfgMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return os.FileMode(0o644)
}

func normalizePBSDatastoreCfgContent(content string) (string, int) {
	lines := strings.Split(content, "\n")
	inDatastoreBlock := false
	fixed := 0

	for i, line := range lines {
		startsBlock, needsIndent := classifyPBSDatastoreCfgLine(line, inDatastoreBlock)
		if startsBlock {
			inDatastoreBlock = true
			continue
		}
		if needsIndent {
			lines[i] = "    " + line
			fixed++
		}
	}

	return strings.Join(lines, "\n"), fixed
}

func classifyPBSDatastoreCfgLine(line string, inDatastoreBlock bool) (bool, bool) {
	trimmed := strings.TrimSpace(line)
	if isIgnoredConfigLine(trimmed) {
		return false, false
	}
	if strings.HasPrefix(trimmed, "datastore:") {
		return true, false
	}
	if !inDatastoreBlock || hasConfigIndent(line) {
		return false, false
	}
	return false, true
}

func hasConfigIndent(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}
