package pbs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

var execCommand = exec.CommandContext

// Namespace represents a single PBS namespace.
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

// ListNamespaces tries the PBS CLI first and, if it fails,
// falls back to the filesystem to infer namespaces.
func ListNamespaces(ctx context.Context, datastoreName, datastorePath string, ioTimeout time.Duration) ([]Namespace, bool, error) {
	if namespaces, err := listNamespacesViaCLI(ctx, datastoreName); err == nil {
		return namespaces, false, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	namespaces, err := discoverNamespacesFromFilesystem(ctx, datastorePath, ioTimeout)
	if err != nil {
		return nil, false, err
	}

	return namespaces, true, nil
}

func listNamespacesViaCLI(ctx context.Context, datastore string) ([]Namespace, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := execCommand(
		ctx,
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

func discoverNamespacesFromFilesystem(ctx context.Context, datastorePath string, ioTimeout time.Duration) ([]Namespace, error) {
	if datastorePath == "" {
		return nil, fmt.Errorf("datastore path is empty")
	}

	entries, err := safefs.ReadDir(ctx, datastorePath, ioTimeout)
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
			if _, err := safefs.Stat(ctx, filepath.Join(subPath, chk), ioTimeout); err == nil {
				namespaces = append(namespaces, Namespace{
					Ns:   entry.Name(),
					Path: subPath,
				})
				break
			} else if errors.Is(err, safefs.ErrTimeout) {
				return nil, err
			}
		}
	}

	return namespaces, nil
}
