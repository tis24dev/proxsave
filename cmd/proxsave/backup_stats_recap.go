package main

import (
	"fmt"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// The backup-statistics recap, single-sourced for BOTH front-ends.
//
// It was written twice — a debug-only log block and a themed graphical block — and the
// two had drifted: only the graphical one reported FilesMissing (the field the
// notifications send, so an email could name missing files the same run's log never
// did), and the bundle lines came out in opposite orders. Building the ROWS here and
// leaving each front-end to present them is what stops that: adding a row reaches both,
// and neither can quietly stop showing one.
//
// Presentation stays with the renderer. The graphical block themes the rows; the log
// block writes them through the logger. Neither decides WHAT the recap says.

// backupStatLine is one recap row. Warn marks a row reporting something the operator
// should look at (missing or failed files) — the graphical block renders those in the
// warning colour. The log block ignores it: a log line carries its level, not a colour,
// and the whole block already sits at one level.
type backupStatLine struct {
	Text string
	Warn bool
}

// backupStatsRecap builds the recap rows.
//
// compact keeps only what answers "did it work, and where is the archive": the file
// counts, the archive size, how long it took, and the path. It exists because the full
// block is DEBUG-only while logBackupStatistics runs on every backup including the
// unattended ones, which left a cron run at INFO with no statistics at all and no
// graphical recap to stand in for them.
func backupStatsRecap(st *orchestrator.BackupStats, compact bool) []backupStatLine {
	if st == nil {
		return nil
	}

	// Files first, and always: the collected/missing/failed triple is the one row that
	// says whether the backup is complete. "missing" is st.FilesMissing, the SAME field
	// the notifications report — a mail saying "5 missing" that the log never mentions
	// cannot be reconciled by whoever is investigating.
	files := fmt.Sprintf("Files: %d collected - %d missing", st.FilesCollected, st.FilesMissing)
	if st.FilesFailed > 0 {
		files += fmt.Sprintf(" (%d failed)", st.FilesFailed)
	}
	lines := []backupStatLine{{Text: files, Warn: st.FilesMissing > 0 || st.FilesFailed > 0}}

	if !compact {
		lines = append(lines,
			backupStatLine{Text: fmt.Sprintf("Directories created: %d", st.DirsCreated)},
			backupStatLine{Text: "Data collected: " + formatBytes(st.BytesCollected)},
		)
	}
	lines = append(lines, backupStatLine{Text: "Archive size: " + formatBytes(st.ArchiveSize)})
	if !compact {
		lines = append(lines,
			backupStatLine{Text: "Compression ratio: " + compressionRatioText(st)},
			backupStatLine{Text: fmt.Sprintf("Compression used: %s (level %d, mode %s)", st.Compression, st.CompressionLevel, st.CompressionMode)},
		)
		if st.RequestedCompression != st.Compression {
			lines = append(lines, backupStatLine{Text: fmt.Sprintf("Requested compression: %s", st.RequestedCompression)})
		}
	}
	lines = append(lines, backupStatLine{Text: "Duration: " + formatDuration(st.Duration)})

	return append(lines, backupArtifactLines(st, compact)...)
}

// backupArtifactLines names where the run's output landed. The bundle case reports the
// PATH first and its contents second, matching the plain case where "Archive path"
// precedes the manifest and checksum that describe it — the graphical block used to
// invert the pair while its own comment claimed to mirror the log.
//
// compact keeps only the path itself: the manifest and checksum are derivable from it
// and belong to the full block.
func backupArtifactLines(st *orchestrator.BackupStats, compact bool) []backupStatLine {
	if st.BundleCreated {
		lines := []backupStatLine{{Text: "Bundle path: " + st.ArchivePath}}
		if compact {
			return lines
		}
		return append(lines, backupStatLine{Text: "Bundle contents: archive + checksum + metadata"})
	}

	lines := []backupStatLine{{Text: "Archive path: " + st.ArchivePath}}
	if compact {
		return lines
	}
	if st.ManifestPath != "" {
		lines = append(lines, backupStatLine{Text: "Manifest path: " + st.ManifestPath})
	}
	if st.Checksum != "" {
		lines = append(lines, backupStatLine{Text: "Archive checksum (SHA256): " + st.Checksum})
	}
	return lines
}
