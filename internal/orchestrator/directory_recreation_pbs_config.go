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

	backupPath, err := writePBSDatastoreCfgBackup(raw)
	if err != nil {
		return fmt.Errorf("write backup copy: %w", err)
	}

	mode := datastoreCfgMode(path)
	if err := writePBSDatastoreCfgAtomically(path, []byte(normalized), mode); err != nil {
		return fmt.Errorf("write normalized datastore.cfg: %w", err)
	}

	logger.Warning("PBS datastore.cfg: fixed %d malformed line(s) (properties must be indented); backup saved to %s", fixed, backupPath)
	return nil
}

func writePBSDatastoreCfgBackup(raw []byte) (backupPath string, err error) {
	backupDir, err := os.MkdirTemp("/tmp", "proxsave-")
	if err != nil {
		return "", err
	}
	removeBackupDir := true
	defer func() {
		if err != nil && removeBackupDir {
			_ = os.RemoveAll(backupDir)
		}
	}()

	prefix := fmt.Sprintf("datastore.cfg.pre-normalize.%s-", nowRestore().Format("20060102-150405"))
	backupFile, err := os.CreateTemp(backupDir, prefix)
	if err != nil {
		return "", err
	}
	backupPath = backupFile.Name()
	defer func() {
		if err != nil {
			_ = backupFile.Close()
			_ = os.Remove(backupPath)
		}
	}()

	if err = backupFile.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err = backupFile.Write(raw); err != nil {
		return "", err
	}
	if err = backupFile.Close(); err != nil {
		return "", err
	}
	removeBackupDir = false
	return backupPath, nil
}

func writePBSDatastoreCfgAtomically(path string, data []byte, mode os.FileMode) (err error) {
	tmpFile, err := os.CreateTemp(filepath.Dir(path), "datastore.cfg.proxsave-*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		if err != nil {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err = tmpFile.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmpFile.Write(data); err != nil {
		return err
	}
	if err = tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync datastore.cfg temp file: %w", err)
	}
	if err = tmpFile.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace datastore.cfg: %w", err)
	}
	if err = syncPBSDatastoreCfgDir(path); err != nil {
		return err
	}
	return nil
}

func syncPBSDatastoreCfgDir(path string) (err error) {
	dirFile, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open datastore.cfg directory: %w", err)
	}
	defer closeIntoErr(&err, dirFile, "close datastore.cfg directory")

	if err = dirFile.Sync(); err != nil {
		return fmt.Errorf("fsync datastore.cfg directory: %w", err)
	}
	return nil
}

func datastoreCfgMode(path string) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return os.FileMode(0o644)
}

// normalizePBSDatastoreCfgContent expects PBS datastore.cfg content, where the
// only supported top-level sections are datastore blocks. Once a datastore block
// is seen, subsequent non-comment lines are treated as datastore properties.
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
