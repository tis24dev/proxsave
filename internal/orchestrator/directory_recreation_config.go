// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

type pveStorageEntry struct {
	Name string
	Type string
	Path string
}

type pbsDatastoreEntry struct {
	Name string
	Path string
}

func loadPVEStorageEntries(path string, logger *logging.Logger) (entries []pveStorageEntry, err error) {
	if exists, err := configFileExists(path, "storage.cfg", "storage directory recreation", logger); err != nil || !exists {
		return nil, err
	}

	logger.Info("Parsing storage.cfg to recreate storage directories...")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open storage.cfg: %w", err)
	}
	defer closeIntoErr(&err, file, "close storage.cfg")

	entries, err = parsePVEStorageEntries(file)
	if err != nil {
		return nil, fmt.Errorf("read storage.cfg: %w", err)
	}
	return entries, nil
}

func loadPBSDatastoreEntries(path string, logger *logging.Logger) (entries []pbsDatastoreEntry, err error) {
	if exists, err := configFileExists(path, "datastore.cfg", "datastore directory recreation", logger); err != nil || !exists {
		return nil, err
	}

	if err := normalizePBSDatastoreCfg(path, logger); err != nil {
		logger.Warning("PBS datastore.cfg normalization failed: %v", err)
	}

	logger.Info("Parsing datastore.cfg to recreate datastore directories...")
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open datastore.cfg: %w", err)
	}
	defer closeIntoErr(&err, file, "close datastore.cfg")

	entries, err = parsePBSDatastoreEntries(file)
	if err != nil {
		return nil, fmt.Errorf("read datastore.cfg: %w", err)
	}
	return entries, nil
}

func configFileExists(path, label, skipReason string, logger *logging.Logger) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			logger.Debug("No %s found, skipping %s", label, skipReason)
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", label, err)
	}
	return true, nil
}

func parsePVEStorageEntries(reader io.Reader) ([]pveStorageEntry, error) {
	scanner := bufio.NewScanner(reader)
	var entries []pveStorageEntry
	var current pveStorageEntry

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if isIgnoredConfigLine(line) {
			continue
		}
		if entry, ok := parsePVEStorageHeader(line); ok {
			current = entry
			continue
		}
		if path, ok := parseConfigPath(line); ok && current.Name != "" {
			current.Path = path
			entries = append(entries, current)
			current = pveStorageEntry{}
		}
	}

	return entries, scanner.Err()
}

func parsePBSDatastoreEntries(reader io.Reader) ([]pbsDatastoreEntry, error) {
	scanner := bufio.NewScanner(reader)
	var entries []pbsDatastoreEntry
	var current pbsDatastoreEntry

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if isIgnoredConfigLine(line) {
			continue
		}
		if entry, ok := parsePBSDatastoreHeader(line); ok {
			current = entry
			continue
		}
		if path, ok := parseConfigPath(line); ok && current.Name != "" {
			current.Path = path
			entries = append(entries, current)
			current = pbsDatastoreEntry{}
		}
	}

	return entries, scanner.Err()
}

func isIgnoredConfigLine(line string) bool {
	return line == "" || strings.HasPrefix(line, "#")
}

func parsePVEStorageHeader(line string) (pveStorageEntry, bool) {
	if !strings.Contains(line, ":") || strings.Contains(line, "=") {
		return pveStorageEntry{}, false
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return pveStorageEntry{}, false
	}
	return pveStorageEntry{
		Type: strings.TrimSuffix(parts[0], ":"),
		Name: strings.TrimSuffix(parts[1], ":"),
	}, true
}

func parsePBSDatastoreHeader(line string) (pbsDatastoreEntry, bool) {
	if !strings.HasPrefix(line, "datastore:") {
		return pbsDatastoreEntry{}, false
	}
	parts := strings.Fields(line)
	if len(parts) < 2 {
		return pbsDatastoreEntry{}, false
	}
	return pbsDatastoreEntry{Name: strings.TrimSuffix(parts[1], ":")}, true
}

func parseConfigPath(line string) (string, bool) {
	const pathPrefix = "path "
	if !strings.HasPrefix(line, pathPrefix) {
		return "", false
	}
	return strings.TrimSpace(line[len(pathPrefix):]), true
}
