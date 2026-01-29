package backup

import (
	"encoding/json"
	"path/filepath"
	"time"
)

// ManifestFileStatus represents the status of a file in the backup manifest
type ManifestFileStatus string

const (
	StatusCollected ManifestFileStatus = "collected"
	StatusNotFound  ManifestFileStatus = "not_found"
	StatusFailed    ManifestFileStatus = "failed"
	StatusSkipped   ManifestFileStatus = "skipped"
	StatusDisabled  ManifestFileStatus = "disabled"
)

// ManifestEntry represents a single file entry in the manifest
type ManifestEntry struct {
	Status ManifestFileStatus `json:"status"`
	Size   int64              `json:"size,omitempty"`
	Error  string             `json:"error,omitempty"`
}

// BackupManifest contains metadata about all files in the backup
type BackupManifest struct {
	CreatedAt   time.Time                `json:"created_at"`
	Hostname    string                   `json:"hostname"`
	ProxmoxType string                   `json:"proxmox_type"`
	PBSConfigs  map[string]ManifestEntry `json:"pbs_configs,omitempty"`
	PVEConfigs  map[string]ManifestEntry `json:"pve_configs,omitempty"`
	SystemFiles map[string]ManifestEntry `json:"system_files,omitempty"`
	Stats       ManifestStats            `json:"stats"`
}

// ManifestStats contains summary statistics for the manifest
type ManifestStats struct {
	FilesProcessed int64 `json:"files_processed"`
	FilesFailed    int64 `json:"files_failed"`
	FilesNotFound  int64 `json:"files_not_found"`
	FilesSkipped   int64 `json:"files_skipped"`
	DirsCreated    int64 `json:"dirs_created"`
	BytesCollected int64 `json:"bytes_collected"`
}

// WriteManifest writes the backup manifest to the temp directory
func (c *Collector) WriteManifest(hostname string) error {
	manifest := BackupManifest{
		CreatedAt:   time.Now().UTC(),
		Hostname:    hostname,
		ProxmoxType: string(c.proxType),
		PBSConfigs:  c.pbsManifest,
		PVEConfigs:  c.pveManifest,
		SystemFiles: c.systemManifest,
		Stats: ManifestStats{
			FilesProcessed: c.stats.FilesProcessed,
			FilesFailed:    c.stats.FilesFailed,
			FilesNotFound:  c.stats.FilesNotFound,
			FilesSkipped:   c.stats.FilesSkipped,
			DirsCreated:    c.stats.DirsCreated,
			BytesCollected: c.stats.BytesCollected,
		},
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(c.tempDir, "manifest.json")
	return c.writeReportFile(manifestPath, data)
}
