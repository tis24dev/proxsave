package main

import (
	"fmt"
	"os"
	"path/filepath"

	rootdocs "github.com/tis24dev/proxsave"
	"github.com/tis24dev/proxsave/internal/logging"
)

// installSupportDocs writes embedded documentation files (README, mapping, etc.)
// into the selected base directory so every installation ships with the same
// docs that were present at build time.
func installSupportDocs(baseDir string, bootstrap *logging.BootstrapLogger) error {
	docs := rootdocs.InstallableDocs()
	if len(docs) == 0 {
		return nil
	}

	for _, doc := range docs {
		target := filepath.Join(baseDir, doc.Name)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("ensure directory for %s: %w", target, err)
		}
		if err := os.WriteFile(target, doc.Data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", target, err)
		}
		// Silent success - only errors are shown
		// if bootstrap != nil {
		// 	bootstrap.Info("✓ Installed %s", target)
		// }
	}

	return nil
}
