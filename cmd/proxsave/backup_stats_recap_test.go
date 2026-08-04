package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/orchestrator"
)

func recapText(lines []backupStatLine) string {
	parts := make([]string, 0, len(lines))
	for _, l := range lines {
		parts = append(parts, l.Text)
	}
	return strings.Join(parts, "\n")
}

func fullStats() *orchestrator.BackupStats {
	return &orchestrator.BackupStats{
		FilesCollected:       40,
		FilesMissing:         5,
		FilesFailed:          3,
		DirsCreated:          7,
		BytesCollected:       8192,
		ArchiveSize:          4096,
		Compression:          "zstd",
		CompressionLevel:     3,
		CompressionMode:      "standard",
		RequestedCompression: "zstd",
		Duration:             90 * time.Second,
		ArchivePath:          "/var/backup/proxsave.tar.zst",
		ManifestPath:         "/var/backup/proxsave.manifest",
		Checksum:             "abc123",
	}
}

// TestBackupStatsRecapReportsMissingFiles is the regression proper: FilesMissing is the
// field the notifications send, and only the graphical block reported it. An email
// saying "5 missing" that the same run's log never mentions cannot be reconciled by
// whoever is investigating, and the log is what survives.
func TestBackupStatsRecapReportsMissingFiles(t *testing.T) {
	for _, compact := range []bool{false, true} {
		out := recapText(backupStatsRecap(fullStats(), compact))
		if !strings.Contains(out, "Files: 40 collected - 5 missing") {
			t.Errorf("compact=%v: the missing count must be reported:\n%s", compact, out)
		}
		if !strings.Contains(out, "(3 failed)") {
			t.Errorf("compact=%v: the failed count must be reported:\n%s", compact, out)
		}
	}
}

// TestBackupStatsRecapMarksTheFilesRowForAttention: the row carries its own severity so
// a renderer does not have to re-derive it from the numbers and get it wrong. Zero
// missing and zero failed is an ordinary row.
func TestBackupStatsRecapMarksTheFilesRowForAttention(t *testing.T) {
	cases := []struct {
		name     string
		missing  int
		failed   int
		wantWarn bool
	}{
		{"complete", 0, 0, false},
		{"files missing", 5, 0, true},
		{"files failed", 0, 3, true},
		{"both", 5, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := fullStats()
			st.FilesMissing, st.FilesFailed = tc.missing, tc.failed
			lines := backupStatsRecap(st, false)
			if len(lines) == 0 {
				t.Fatal("the recap must not be empty")
			}
			if !strings.HasPrefix(lines[0].Text, "Files:") {
				t.Fatalf("the files row must come first, got %q", lines[0].Text)
			}
			if lines[0].Warn != tc.wantWarn {
				t.Fatalf("Warn = %v, want %v for %q", lines[0].Warn, tc.wantWarn, lines[0].Text)
			}
		})
	}
}

// TestBackupStatsRecapPutsTheBundlePathBeforeItsContents: the two front-ends emitted
// this pair in opposite orders, and the graphical one claimed in a comment to mirror
// the log while inverting it. Path first matches the plain case, where "Archive path"
// precedes the manifest and checksum that describe it.
func TestBackupStatsRecapPutsTheBundlePathBeforeItsContents(t *testing.T) {
	st := fullStats()
	st.BundleCreated = true
	out := recapText(backupStatsRecap(st, false))

	path, contents := strings.Index(out, "Bundle path:"), strings.Index(out, "Bundle contents:")
	if path < 0 || contents < 0 {
		t.Fatalf("both bundle lines must be present:\n%s", out)
	}
	if path > contents {
		t.Fatalf("the bundle path must precede its contents (path at %d, contents at %d):\n%s", path, contents, out)
	}
	// A bundle run names no bare archive: the bundle IS the artifact.
	if strings.Contains(out, "Archive path:") {
		t.Fatalf("a bundle run must not also report an archive path:\n%s", out)
	}
}

// TestBackupStatsRecapCompactAnswersTheCronQuestions: the compact rows exist for the
// unattended run, and are worth nothing if they omit what a post-mortem starts from —
// whether the backup is complete, how big it is, how long it took, where it landed.
func TestBackupStatsRecapCompactAnswersTheCronQuestions(t *testing.T) {
	out := recapText(backupStatsRecap(fullStats(), true))
	for _, want := range []string{"Files: 40 collected", "Archive size:", "Duration:", "Archive path: /var/backup/proxsave.tar.zst"} {
		if !strings.Contains(out, want) {
			t.Errorf("the compact recap must answer %q:\n%s", want, out)
		}
	}
	// And it must stay compact, or it is just the full block under another name.
	for _, unwanted := range []string{"Directories created", "Data collected", "Compression", "Manifest path", "checksum"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the compact recap must drop %q:\n%s", unwanted, out)
		}
	}
}

// TestBackupStatsRecapFullKeepsEveryDetail guards the other direction: the compact
// split must not quietly cost the full block a row.
func TestBackupStatsRecapFullKeepsEveryDetail(t *testing.T) {
	st := fullStats()
	st.RequestedCompression = "xz" // differs from Compression, so the extra row appears
	out := recapText(backupStatsRecap(st, false))
	for _, want := range []string{
		"Files: 40 collected - 5 missing (3 failed)",
		"Directories created: 7",
		"Data collected: 8.0 KiB",
		"Archive size: 4.0 KiB",
		"Compression ratio: 50.0%",
		"Compression used: zstd (level 3, mode standard)",
		"Requested compression: xz",
		"Duration:",
		"Archive path: /var/backup/proxsave.tar.zst",
		"Manifest path: /var/backup/proxsave.manifest",
		"Archive checksum (SHA256): abc123",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the full recap must contain %q:\n%s", want, out)
		}
	}
}

// TestBackupStatsRecapToleratesNilStats: the graphical recap is also built for runs that
// died before any stats existed, and the builder is reached through a nil-able pointer.
func TestBackupStatsRecapToleratesNilStats(t *testing.T) {
	if lines := backupStatsRecap(nil, false); lines != nil {
		t.Fatalf("nil stats must yield no rows, got %v", lines)
	}
}
