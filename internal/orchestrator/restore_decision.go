package orchestrator

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// RestoreDecisionSource describes where trusted restore facts were derived from.
type RestoreDecisionSource string

const (
	RestoreDecisionSourceUnknown          RestoreDecisionSource = "unknown"
	RestoreDecisionSourceInternalMetadata RestoreDecisionSource = "internal_metadata"
	RestoreDecisionSourceCategories       RestoreDecisionSource = "categories"
)

// RestoreDecisionInfo contains the archive-derived facts used for restore decisions.
type RestoreDecisionInfo struct {
	BackupType     SystemType
	ClusterPayload bool
	BackupHostname string
	Source         RestoreDecisionSource
}

type restoreDecisionMetadata struct {
	BackupType    SystemType
	BackupTargets []string
	ClusterMode   string
	Hostname      string
}

type restoreArchiveInspection struct {
	AvailableCategories []Category
	Decision            *RestoreDecisionInfo
}

const (
	restoreDecisionMetadataPath     = "var/lib/proxsave-info/backup_metadata.txt"
	restoreDecisionMetadataMaxBytes = 8 * 1024
)

// AnalyzeRestoreArchive inspects the archive once and derives trusted restore facts
// from archive contents plus internal backup metadata when present.
func AnalyzeRestoreArchive(archivePath string, logger *logging.Logger) (categories []Category, decision *RestoreDecisionInfo, err error) {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	done := logging.DebugStart(logger, "analyze restore archive", "archive=%s", archivePath)
	defer func() { done(err) }()
	logger.Info("Analyzing backup contents...")

	inspection, err := inspectRestoreArchiveContents(archivePath, logger)
	if err != nil {
		return nil, nil, err
	}

	for _, cat := range inspection.AvailableCategories {
		logger.Debug("Category available: %s (%s)", cat.ID, cat.Name)
	}
	logger.Info("Detected %d available categories", len(inspection.AvailableCategories))
	if inspection.Decision != nil {
		logger.Debug(
			"Restore decision facts: backup_type=%s cluster_payload=%v hostname=%q source=%s",
			inspection.Decision.BackupType,
			inspection.Decision.ClusterPayload,
			inspection.Decision.BackupHostname,
			inspection.Decision.Source,
		)
	}

	return inspection.AvailableCategories, inspection.Decision, nil
}

func inspectRestoreArchiveContents(archivePath string, logger *logging.Logger) (inspection *restoreArchiveInspection, err error) {
	file, err := restoreFS.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	defer closeIntoErr(&err, file, "close archive")

	reader, err := createDecompressionReader(context.Background(), file, archivePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil && err == nil {
			inspection = nil
			err = fmt.Errorf("inspect archive: %w", closeErr)
		}
	}()

	tarReader := tar.NewReader(reader)
	archivePaths, metadata, metadataErr, collectErr := collectRestoreArchiveFacts(tarReader)
	if collectErr != nil {
		return nil, fmt.Errorf("inspect archive: %w", collectErr)
	}
	if metadataErr != nil {
		logger.Warning("Could not parse internal backup metadata: %v", metadataErr)
	}

	logger.Debug("Found %d entries in archive", len(archivePaths))
	availableCategories := AnalyzeArchivePaths(archivePaths, GetAllCategories())

	decision := buildRestoreDecisionInfo(metadata, availableCategories, logger)
	inspection = &restoreArchiveInspection{
		AvailableCategories: availableCategories,
		Decision:            decision,
	}
	return inspection, nil
}

func collectRestoreArchiveFacts(tarReader *tar.Reader) ([]string, *restoreDecisionMetadata, error, error) {
	var (
		archivePaths []string
		metadata     *restoreDecisionMetadata
		metadataErr  error
	)

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, nil, err
		}

		archivePaths = append(archivePaths, header.Name)
		if metadata != nil || header.FileInfo().IsDir() {
			continue
		}
		if !isRestoreDecisionMetadataEntry(header.Name) {
			continue
		}

		data, readErr := readRestoreDecisionMetadata(tarReader, header)
		if readErr != nil {
			metadataErr = readErr
			continue
		}
		parsed, parseErr := parseRestoreDecisionMetadata(data)
		if parseErr != nil {
			metadataErr = parseErr
			continue
		}
		metadata = parsed
	}

	return archivePaths, metadata, metadataErr, nil
}

func readRestoreDecisionMetadata(tarReader *tar.Reader, header *tar.Header) ([]byte, error) {
	if header == nil {
		return nil, fmt.Errorf("restore metadata entry is missing a tar header")
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
		return nil, fmt.Errorf("archive entry %s is not a regular file", header.Name)
	}

	limited := io.LimitReader(tarReader, restoreDecisionMetadataMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > restoreDecisionMetadataMaxBytes {
		size := header.Size
		if size <= 0 {
			size = int64(len(data))
		}
		return nil, fmt.Errorf("archive entry %s too large (%d bytes)", header.Name, size)
	}
	return data, nil
}

func isRestoreDecisionMetadataEntry(entryName string) bool {
	return normalizeRestoreEntryPath(entryName) == restoreDecisionMetadataPath
}

func normalizeRestoreEntryPath(entryName string) string {
	clean := strings.TrimSpace(strings.ReplaceAll(entryName, "\\", "/"))
	if clean == "" {
		return ""
	}

	clean = path.Clean(clean)
	clean = strings.TrimPrefix(clean, "./")
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." {
		return ""
	}
	return clean
}

func parseRestoreDecisionMetadata(data []byte) (*restoreDecisionMetadata, error) {
	meta := &restoreDecisionMetadata{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		switch key {
		case "BACKUP_TYPE":
			meta.BackupType = parseSystemTypeString(value)
		case "BACKUP_TARGETS":
			meta.BackupTargets = splitTargetsCSV(value)
		case "PVE_CLUSTER_MODE", "CLUSTER_MODE":
			meta.ClusterMode = value
		case "HOSTNAME":
			meta.Hostname = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return meta, nil
}

func buildRestoreDecisionInfo(metadata *restoreDecisionMetadata, categories []Category, logger *logging.Logger) *RestoreDecisionInfo {
	if logger == nil {
		logger = logging.GetDefaultLogger()
	}

	info := &RestoreDecisionInfo{
		BackupType:     SystemTypeUnknown,
		ClusterPayload: hasCategoryID(categories, "pve_cluster"),
		Source:         RestoreDecisionSourceUnknown,
	}

	if metadata != nil {
		info.BackupHostname = strings.TrimSpace(metadata.Hostname)
		if info.BackupType == SystemTypeUnknown && len(metadata.BackupTargets) > 0 {
			info.BackupType = parseSystemTargets(metadata.BackupTargets)
		}
	}

	categoryType := detectBackupTypeFromCategories(categories)
	switch {
	case info.BackupType != SystemTypeUnknown && categoryType == SystemTypeUnknown:
		info.Source = RestoreDecisionSourceInternalMetadata
	case metadata != nil && metadata.BackupType != SystemTypeUnknown && categoryType == SystemTypeUnknown:
		info.BackupType = metadata.BackupType
		info.Source = RestoreDecisionSourceInternalMetadata
	case metadata != nil && metadata.BackupType != SystemTypeUnknown && categoryType != SystemTypeUnknown && metadata.BackupType != categoryType:
		logger.Warning("Internal backup metadata and archive payload disagree on backup type; using archive-derived type %s", strings.ToUpper(string(categoryType)))
		info.BackupType = categoryType
		info.Source = RestoreDecisionSourceCategories
	case categoryType != SystemTypeUnknown:
		info.BackupType = categoryType
		info.Source = RestoreDecisionSourceCategories
	case metadata != nil && metadata.BackupType != SystemTypeUnknown:
		info.BackupType = metadata.BackupType
		info.Source = RestoreDecisionSourceInternalMetadata
	}

	if metadata != nil {
		metadataCluster := strings.EqualFold(strings.TrimSpace(metadata.ClusterMode), "cluster")
		switch {
		case metadataCluster && !info.ClusterPayload:
			logger.Warning("Internal backup metadata reports cluster mode, but no pve_cluster payload was found; guarded cluster restore remains disabled")
		case !metadataCluster && info.ClusterPayload:
			logger.Warning("Cluster payload detected in archive despite metadata reporting non-cluster backup; guarded cluster restore remains enabled")
		}
	}

	return info
}

func detectBackupTypeFromCategories(categories []Category) SystemType {
	var hasPVE, hasPBS bool
	for _, cat := range categories {
		switch cat.Type {
		case CategoryTypePVE:
			hasPVE = true
		case CategoryTypePBS:
			hasPBS = true
		}
	}

	switch {
	case hasPVE && hasPBS:
		return SystemTypeDual
	case hasPVE:
		return SystemTypePVE
	case hasPBS:
		return SystemTypePBS
	default:
		return SystemTypeUnknown
	}
}

func splitTargetsCSV(value string) []string {
	parts := strings.Split(value, ",")
	targets := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			targets = append(targets, part)
		}
	}
	return targets
}
