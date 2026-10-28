package pbs

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tis24dev/proxsave/internal/safefs"
)

// Namespace represents a single PBS namespace.
type Namespace struct {
	Ns      string `json:"ns"`
	Path    string `json:"path"`
	Parent  string `json:"parent"`
	Comment string `json:"comment"`
	Ctime   int64  `json:"ctime"`
}

// DiscoverNamespacesFromFilesystem lists a datastore's namespaces from its directory.
func DiscoverNamespacesFromFilesystem(ctx context.Context, datastorePath string, ioTimeout time.Duration) ([]Namespace, error) {
	return discoverNamespacesFromFilesystem(ctx, datastorePath, ioTimeout)
}

// pbsMaxNamespaceDepth is how many levels PBS allows below the root namespace
// (MAX_NAMESPACE_DEPTH in pbs-api-types).
const pbsMaxNamespaceDepth = 7

func discoverNamespacesFromFilesystem(ctx context.Context, datastorePath string, ioTimeout time.Duration) ([]Namespace, error) {
	if datastorePath == "" {
		return nil, fmt.Errorf("datastore path is empty")
	}

	if _, err := safefs.ReadDir(ctx, datastorePath, ioTimeout); err != nil {
		return nil, fmt.Errorf("cannot read datastore path %s: %w", datastorePath, err)
	}

	namespaces := []Namespace{{
		Ns:      "",
		Path:    datastorePath,
		Comment: "root namespace",
	}}

	// PBS keeps a namespace at <ds>/ns/<name> and a child one at <ds>/ns/<name>/ns/<child>
	// (measured on pve-test, PBS 4.2), up to pbsMaxNamespaceDepth levels. A directory that
	// cannot be read is skipped like an absent one; a timeout ends the walk.
	var walk func(dir, parent string, depth int) error
	walk = func(dir, parent string, depth int) error {
		if depth > pbsMaxNamespaceDepth {
			return nil
		}
		nsDir := filepath.Join(dir, "ns")
		entries, err := safefs.ReadDir(ctx, nsDir, ioTimeout)
		if err != nil {
			if errors.Is(err, safefs.ErrTimeout) {
				return err
			}
			return nil
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			ns := entry.Name()
			if parent != "" {
				ns = parent + "/" + ns
			}
			path := filepath.Join(nsDir, entry.Name())
			namespaces = append(namespaces, Namespace{Ns: ns, Path: path, Parent: parent})
			if err := walk(path, ns, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(datastorePath, "", 1); err != nil {
		return nil, err
	}

	return namespaces, nil
}
