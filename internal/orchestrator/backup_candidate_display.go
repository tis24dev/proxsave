package orchestrator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/backup"
	"github.com/tis24dev/proxsave/internal/ui/components"
)

const (
	backupCandidateDisplayTimeLayout = "2006-01-02 15:04:05"
	unknownBackupDateText            = "unknown date"
	unknownBackupHostText            = "unknown host"
	unknownBackupTargetText          = "UNKNOWN"
)

type backupCandidateDisplay struct {
	Created     string
	Hostname    string
	Mode        string
	Tool        string
	Target      string
	Compression string
	Base        string
	Summary     string
}

func describeBackupCandidate(cand *backupCandidate) backupCandidateDisplay {
	display := backupCandidateDisplay{
		Created:     formatBackupCandidateCreated(cand),
		Hostname:    formatBackupCandidateHostname(cand),
		Mode:        formatBackupCandidateMode(cand),
		Tool:        formatBackupCandidateTool(cand),
		Target:      formatBackupCandidateTarget(candidateManifest(cand)),
		Compression: formatBackupCandidateCompression(cand),
		Base:        backupCandidateBaseName(cand),
	}
	// Sanitize manifest-derived fields here, the single choke point feeding
	// both the CLI table (printed raw via fmt.Print*) and the Summary. The
	// TUI double-sanitizes harmlessly via NewSelector. No-op on clean data,
	// so column-width math on these fields stays consistent.
	display.Created = components.SanitizeLine(display.Created)
	display.Hostname = components.SanitizeLine(display.Hostname)
	display.Mode = components.SanitizeLine(display.Mode)
	display.Tool = components.SanitizeLine(display.Tool)
	display.Target = components.SanitizeLine(display.Target)
	display.Compression = components.SanitizeLine(display.Compression)
	display.Base = components.SanitizeLine(display.Base)
	// Build Summary from the already-sanitized fields so it is clean too.
	display.Summary = formatBackupCandidateSummary(display)
	return display
}

func candidateManifest(cand *backupCandidate) *backup.Manifest {
	if cand == nil {
		return nil
	}
	return cand.Manifest
}

func formatBackupCandidateCreated(cand *backupCandidate) string {
	manifest := candidateManifest(cand)
	if manifest == nil || manifest.CreatedAt.IsZero() {
		return unknownBackupDateText
	}
	return manifest.CreatedAt.Format(backupCandidateDisplayTimeLayout)
}

func formatBackupCandidateHostname(cand *backupCandidate) string {
	manifest := candidateManifest(cand)
	if manifest == nil {
		return unknownBackupHostText
	}
	host := strings.TrimSpace(manifest.Hostname)
	if host == "" {
		return unknownBackupHostText
	}
	return host
}

func formatBackupCandidateMode(cand *backupCandidate) string {
	manifest := candidateManifest(cand)
	if manifest == nil {
		return "UNKNOWN"
	}
	mode := strings.ToUpper(statusFromManifest(manifest))
	if mode == "" {
		return "UNKNOWN"
	}
	return mode
}

func formatBackupCandidateTool(cand *backupCandidate) string {
	manifest := candidateManifest(cand)
	if manifest == nil {
		return "Tool unknown"
	}
	version := strings.TrimSpace(manifest.ScriptVersion)
	if version == "" {
		return "Tool unknown"
	}
	if !strings.HasPrefix(strings.ToLower(version), "v") {
		version = "v" + version
	}
	return "Tool " + version
}

func formatBackupCandidateTarget(manifest *backup.Manifest) string {
	if manifest == nil {
		return unknownBackupTargetText
	}

	targets := formatTargets(manifest)
	targets = strings.TrimSpace(targets)
	if targets == "" || targets == "unknown target" {
		targets = unknownBackupTargetText
	} else {
		targets = strings.ToUpper(targets)
	}

	version := normalizeProxmoxVersion(manifest.ProxmoxVersion)
	if version != "" {
		targets = fmt.Sprintf("%s %s", targets, version)
	}

	if cluster := formatClusterMode(manifest.ClusterMode); cluster != "" {
		targets = fmt.Sprintf("%s (%s)", targets, cluster)
	}

	return targets
}

func formatBackupCandidateCompression(cand *backupCandidate) string {
	manifest := candidateManifest(cand)
	if manifest == nil {
		return ""
	}
	compression := strings.TrimSpace(manifest.CompressionType)
	if compression == "" {
		return ""
	}
	return strings.ToUpper(compression)
}

func backupCandidateBaseName(cand *backupCandidate) string {
	if cand == nil {
		return ""
	}
	base := strings.TrimSpace(cand.DisplayBase)
	if base != "" {
		return base
	}

	switch {
	case strings.TrimSpace(cand.BundlePath) != "":
		return filepath.Base(strings.TrimSpace(cand.BundlePath))
	case strings.TrimSpace(cand.RawArchivePath) != "":
		return filepath.Base(strings.TrimSpace(cand.RawArchivePath))
	default:
		return ""
	}
}

func formatBackupCandidateSummary(display backupCandidateDisplay) string {
	parts := make([]string, 0, 2)

	if display.Hostname != "" && display.Hostname != unknownBackupHostText {
		parts = append(parts, display.Hostname)
	}

	switch {
	case display.Base != "" && display.Created != "" && display.Created != unknownBackupDateText:
		parts = append(parts, fmt.Sprintf("%s (%s)", display.Base, display.Created))
	case display.Base != "":
		parts = append(parts, display.Base)
	case display.Created != "" && display.Created != unknownBackupDateText:
		parts = append(parts, display.Created)
	}

	if len(parts) == 0 {
		if display.Hostname != "" {
			return display.Hostname
		}
		return display.Created
	}
	return strings.Join(parts, " • ")
}
