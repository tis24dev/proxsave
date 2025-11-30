package proxmoxbackup

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
)

var (
	//go:embed README.md
	embeddedReadme []byte

	//go:embed docs
	embeddedDocs embed.FS
)

// DocAsset represents an embedded documentation file that can be
// materialized during installation.
type DocAsset struct {
	Name string
	Data []byte
}

var installableDocs = func() []DocAsset {
	builtins := []DocAsset{
		{Name: "README.md", Data: embeddedReadme},
	}
	docAssets, err := collectEmbeddedDocs()
	if err != nil {
		panic(fmt.Errorf("failed to load embedded docs: %w", err))
	}
	return append(builtins, docAssets...)
}()

// InstallableDocs returns the list of documentation files embedded in the
// binary that should be written to the installation root.
func InstallableDocs() []DocAsset {
	out := make([]DocAsset, len(installableDocs))
	copy(out, installableDocs)
	return out
}

func collectEmbeddedDocs() ([]DocAsset, error) {
	assets := make([]DocAsset, 0, 4)
	err := fs.WalkDir(embeddedDocs, "docs", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		data, err := embeddedDocs.ReadFile(path)
		if err != nil {
			return err
		}
		assets = append(assets, DocAsset{
			Name: path,
			Data: data,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(assets, func(i, j int) bool {
		return assets[i].Name < assets[j].Name
	})
	return assets, nil
}

// Dummy symbol so that Go Report Card recognizes this package as valid.
const _goreportcardFix = true
