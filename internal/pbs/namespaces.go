package pbs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var execCommand = exec.Command

// Namespace rappresenta un singolo namespace PBS.
type Namespace struct {
	Ns      string `json:"ns"`
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	Comment string `json:"comment"`
	Ctime   int64  `json:"ctime"`
}

type listNamespacesResponse struct {
	Data []Namespace `json:"data"`
}

// ListNamespaces prova prima a usare la CLI PBS e, se fallisce,
// effettua il fallback su filesystem per dedurre i namespace.
func ListNamespaces(datastoreName, datastorePath string) ([]Namespace, bool, error) {
	if namespaces, err := listNamespacesViaCLI(datastoreName); err == nil {
		return namespaces, false, nil
	}

	namespaces, err := discoverNamespacesFromFilesystem(datastorePath)
	if err != nil {
		return nil, false, err
	}

	return namespaces, true, nil
}

func listNamespacesViaCLI(datastore string) ([]Namespace, error) {
	cmd := execCommand(
		"proxmox-backup-manager",
		"datastore",
		"namespace",
		"list",
		datastore,
		"--output-format=json",
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("namespace list command failed: %w (stderr: %s)", err, stderr.String())
	}

	var parsed listNamespacesResponse
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return nil, fmt.Errorf("namespace list parsing failed: %w", err)
	}

	return parsed.Data, nil
}

func discoverNamespacesFromFilesystem(datastorePath string) ([]Namespace, error) {
	if datastorePath == "" {
		return nil, fmt.Errorf("datastore path is empty")
	}

	entries, err := os.ReadDir(datastorePath)
	if err != nil {
		return nil, fmt.Errorf("cannot read datastore path %s: %w", datastorePath, err)
	}

	var namespaces []Namespace
	namespaces = append(namespaces, Namespace{
		Ns:      "",
		Path:    datastorePath,
		Comment: "root namespace",
	})

	checkDirs := []string{"vm", "ct", "host", "namespace"}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		subPath := filepath.Join(datastorePath, entry.Name())
		for _, chk := range checkDirs {
			if _, err := os.Stat(filepath.Join(subPath, chk)); err == nil {
				namespaces = append(namespaces, Namespace{
					Ns:   entry.Name(),
					Path: subPath,
				})
				break
			}
		}
	}

	return namespaces, nil
}
