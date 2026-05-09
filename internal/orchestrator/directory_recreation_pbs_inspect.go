// Package orchestrator coordinates backup, restore, decrypt, and notification workflows.
package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

var allowedPBSDatastoreScaffoldEntries = map[string]struct{}{
	".chunks": {},
	".index":  {},
	".lock":   {},
}

func validatePBSDatastoreReadOnly(datastorePath string) string {
	if datastorePath == "" {
		return "datastore path is empty"
	}
	if warn := validatePBSDatastoreRoot(datastorePath); warn != "" {
		return warn
	}
	if warn := validatePBSDatastoreSubdir(datastorePath, ".chunks"); warn != "" {
		return warn
	}
	if warn := validatePBSDatastoreSubdir(datastorePath, ".index"); warn != "" {
		return warn
	}
	return validatePBSDatastoreLock(datastorePath)
}

func validatePBSDatastoreRoot(datastorePath string) string {
	info, err := os.Stat(datastorePath)
	if err != nil {
		return fmt.Sprintf("datastore path %s cannot be stat'd: %v", datastorePath, err)
	}
	if !info.IsDir() {
		return fmt.Sprintf("datastore path %s is not a directory (type=%s)", datastorePath, info.Mode())
	}
	return ""
}

func validatePBSDatastoreSubdir(datastorePath, name string) string {
	info, err := os.Stat(filepath.Join(datastorePath, name))
	if err != nil {
		return fmt.Sprintf("datastore %s missing %s directory: %v", datastorePath, name, err)
	}
	if !info.IsDir() {
		return fmt.Sprintf("datastore %s %s is not a directory (type=%s)", datastorePath, name, info.Mode())
	}
	return ""
}

func validatePBSDatastoreLock(datastorePath string) string {
	info, err := os.Stat(filepath.Join(datastorePath, ".lock"))
	if err != nil {
		return fmt.Sprintf("datastore %s missing .lock file: %v", datastorePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Sprintf("datastore %s .lock is not a regular file (type=%s)", datastorePath, info.Mode())
	}
	return ""
}

func pbsDatastoreHasData(datastorePath string) (bool, error) {
	if strings.TrimSpace(datastorePath) == "" {
		return false, fmt.Errorf("path is empty")
	}
	exists, err := existingDirectoryOrNoData(datastorePath)
	if err != nil || !exists {
		return false, err
	}
	return anyPBSDatastoreDataDirHasEntries(datastorePath)
}

func anyPBSDatastoreDataDirHasEntries(datastorePath string) (bool, error) {
	for _, subdir := range pbsDatastoreSubdirs {
		has, err := dirHasAnyEntry(filepath.Join(datastorePath, subdir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || has {
			return has, err
		}
	}
	return false, nil
}

func pbsDatastoreHasUnexpectedEntries(datastorePath string) (bool, error) {
	if strings.TrimSpace(datastorePath) == "" {
		return false, nil
	}
	exists, err := existingDirectoryOrNoData(datastorePath)
	if err != nil || !exists {
		return false, err
	}
	return datastoreContainsUnexpectedEntries(datastorePath)
}

func existingDirectoryOrNoData(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

func datastoreContainsUnexpectedEntries(datastorePath string) (unexpected bool, err error) {
	f, err := os.Open(datastorePath)
	if err != nil {
		return false, err
	}
	defer closeIntoErr(&err, f, "close datastore directory")
	return readerContainsUnexpectedEntries(f)
}

func readerContainsUnexpectedEntries(f *os.File) (bool, error) {
	for {
		names, err := f.Readdirnames(64)
		if err != nil {
			return handleDatastoreReaddirError(err)
		}
		if hasUnexpectedDatastoreName(names) {
			return true, nil
		}
	}
}

func handleDatastoreReaddirError(err error) (bool, error) {
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	return false, err
}

func hasUnexpectedDatastoreName(names []string) bool {
	for _, name := range names {
		if _, ok := allowedPBSDatastoreScaffoldEntries[name]; !ok {
			return true
		}
	}
	return false
}

func dirHasAnyEntry(path string) (hasEntry bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer closeIntoErr(&err, f, "close directory")

	_, err = f.Readdirnames(1)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	return false, err
}
