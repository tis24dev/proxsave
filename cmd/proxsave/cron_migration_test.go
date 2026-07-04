package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// audited: 2026-06-09 — filterCronLines now returns []string (all distinct removed
// schedules) instead of a single string, so multiple legacy entries with different
// schedules no longer collapse into one. Cases updated to the slice signature.
func TestFilterCronLines(t *testing.T) {
	// Define the user's specific lines that must be preserved
	userLine1 := "0 12 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh 192.168.1.5 htpc-1 /mnt/pve/nas"
	userLine2 := "0 2 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh pbs.miodominio.com pbs1-test /mnt/pve/nas"

	tests := []struct {
		name          string
		inputLines    []string
		correctPaths  []string
		wantLines     []string
		wantHasEntry  bool
		wantSchedules []string
	}{
		{
			name: "Preserve proxmox-backup-client lines",
			inputLines: []string{
				"# Existing comments",
				"0 1 * * * /some/other/job",
				userLine1,
				userLine2,
				"0 3 * * * /usr/local/bin/proxsave", // Correct entry
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"# Existing comments",
				"0 1 * * * /some/other/job",
				userLine1,
				userLine2,
				"0 3 * * * /usr/local/bin/proxsave",
			},
			wantHasEntry: true,
		},
		{
			name: "Migrate legacy proxmox-backup symlink, keep schedule",
			inputLines: []string{
				"0 5 * * * /usr/local/bin/proxmox-backup",
				userLine1,
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				userLine1,
			},
			wantHasEntry:  false,
			wantSchedules: []string{"0 5 * * *"},
		},
		{
			name: "Remove outdated binary reference",
			inputLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup", // Outdated path
				userLine1,
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				userLine1,
			},
			wantHasEntry:  false,
			wantSchedules: []string{"0 2 * * *"},
		},
		{
			name: "Preserve custom binary name proxmox-backup-new",
			inputLines: []string{
				"0 2 * * * /usr/local/bin/proxmox-backup-new",
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"0 2 * * * /usr/local/bin/proxmox-backup-new",
			},
			wantHasEntry: false,
		},
		{
			name: "Preserve proxmox-backup-dog",
			inputLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup-dog",
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup-dog",
			},
			wantHasEntry: false,
		},
		{
			name: "Preserve proxmox-backup-test",
			inputLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup-test --flag",
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup-test --flag",
			},
			wantHasEntry: false,
		},
		{
			name: "Mixed scenario",
			inputLines: []string{
				"# Header",
				userLine1,                           // Keep
				"0 4 * * * /usr/bin/proxsave",       // Wrong path
				userLine2,                           // Keep
				"0 5 * * * /usr/local/bin/proxsave", // Correct
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"# Header",
				userLine1,
				userLine2,
				"0 5 * * * /usr/local/bin/proxsave",
			},
			wantHasEntry:  true,
			wantSchedules: []string{"0 4 * * *"},
		},
		{
			// Regression: a different binary whose name merely shares the
			// "/usr/local/bin/proxsave" prefix must not be matched (was a false
			// positive when matching by substring instead of the command token).
			name: "Ignore a prefix-sharing binary (not the proxsave entry)",
			inputLines: []string{
				"0 2 * * * /usr/local/bin/proxsavex",
				userLine1,
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"0 2 * * * /usr/local/bin/proxsavex",
				userLine1,
			},
			wantHasEntry: false,
		},
		{
			// Regression: a job whose COMMAND is a different binary but which passes
			// the proxsave path only as an ARGUMENT (e.g. backing up the binary) must
			// NOT be removed — removal keys off the cron command token, not a path
			// appearing anywhere in the line.
			name: "Preserve a job referencing proxsave only as an argument",
			inputLines: []string{
				"0 4 * * * /usr/bin/cp /usr/local/bin/proxsave /backup/proxsave.bak",
				userLine1,
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				"0 4 * * * /usr/bin/cp /usr/local/bin/proxsave /backup/proxsave.bak",
				userLine1,
			},
			wantHasEntry: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLines, gotHasEntry, gotSchedules := filterCronLines(tt.inputLines, tt.correctPaths)

			if gotHasEntry != tt.wantHasEntry {
				t.Errorf("hasCurrentEntry = %v, want %v", gotHasEntry, tt.wantHasEntry)
			}

			if len(gotSchedules) != len(tt.wantSchedules) {
				t.Errorf("replacedSchedules = %q, want %q", gotSchedules, tt.wantSchedules)
			} else {
				for i := range gotSchedules {
					if gotSchedules[i] != tt.wantSchedules[i] {
						t.Errorf("replacedSchedules[%d] = %q, want %q", i, gotSchedules[i], tt.wantSchedules[i])
					}
				}
			}

			if len(gotLines) != len(tt.wantLines) {
				t.Fatalf("got %d lines, want %d lines\nGot:  %v\nWant: %v", len(gotLines), len(tt.wantLines), gotLines, tt.wantLines)
			}

			for i, line := range gotLines {
				if line != tt.wantLines[i] {
					t.Errorf("line[%d] = %q, want %q", i, line, tt.wantLines[i])
				}
			}
		})
	}
}

func TestDropLegacyBashCronLines(t *testing.T) {
	tests := []struct {
		name      string
		baseDir   string
		input     []string
		wantLines []string
	}{
		{
			name:    "drops legacy bash script under known roots",
			baseDir: "/opt/proxsave",
			input: []string{
				"0 2 * * * /opt/proxsave/script/proxmox-backup.sh",
				"0 2 * * * /opt/proxmox-backup/script/proxmox-backup.sh",
			},
			wantLines: []string{},
		},
		{
			name:    "drops legacy bash script under custom baseDir",
			baseDir: "/custom/base",
			input: []string{
				"0 2 * * * /custom/base/script/proxmox-backup.sh",
			},
			wantLines: []string{},
		},
		{
			// Safety guard: Proxmox Backup Server components and unrelated jobs
			// must NEVER be removed. PBS binaries have no ".sh"; a path that only
			// appears as an argument is not the command; a same-named script under
			// an unknown path is not ours.
			name:    "preserves PBS components and unrelated jobs",
			baseDir: "/opt/proxsave",
			input: []string{
				"# backup jobs",
				"0 1 * * * /usr/bin/proxmox-backup-client backup root.pxar:/etc",
				"0 2 * * * /usr/sbin/proxmox-backup-proxy",
				"0 3 * * * /usr/local/bin/proxsave",
				"0 4 * * * /home/me/proxmox-backup.sh",
				"0 5 * * * /bin/echo /opt/proxsave/script/proxmox-backup.sh",
			},
			wantLines: []string{
				"# backup jobs",
				"0 1 * * * /usr/bin/proxmox-backup-client backup root.pxar:/etc",
				"0 2 * * * /usr/sbin/proxmox-backup-proxy",
				"0 3 * * * /usr/local/bin/proxsave",
				"0 4 * * * /home/me/proxmox-backup.sh",
				"0 5 * * * /bin/echo /opt/proxsave/script/proxmox-backup.sh",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dropLegacyBashCronLines(tt.input, tt.baseDir, logging.NewBootstrapLogger())

			if len(got) != len(tt.wantLines) {
				t.Fatalf("got %d lines, want %d lines\nGot:  %v\nWant: %v", len(got), len(tt.wantLines), got, tt.wantLines)
			}

			for i, line := range got {
				if line != tt.wantLines[i] {
					t.Errorf("line[%d] = %q, want %q", i, line, tt.wantLines[i])
				}
			}
		})
	}
}

// TestBuildReinstallCronLines covers the (re)install cron behaviour (CRON-MIXED-001
// / CRON-INSTALL-002): every proxsave-managed entry (legacy Bash, outdated binary,
// and the canonical path at its old schedule) is dropped and a single fresh entry
// is written at the chosen schedule, while unrelated operator lines and comments
// are preserved.
func TestBuildReinstallCronLines(t *testing.T) {
	correctPaths := []string{"/usr/local/bin/proxsave"}
	userLine := "0 12 * * * /mnt/pve/nas/scripts/backup_folders-nightly.sh arg"

	lines := []string{
		"# Header",
		userLine, // unrelated operator job: keep
		"0 1 * * * /opt/proxsave/script/proxmox-backup.sh", // legacy Bash: drop
		"0 4 * * * /usr/bin/proxsave",                      // outdated binary: drop
		"0 5 * * * /usr/local/bin/proxsave",                // canonical, old schedule: drop
	}

	got := buildReinstallCronLines(lines, "/opt/proxsave", correctPaths, "30 1 * * *", "/usr/local/bin/proxsave", nil)

	want := []string{
		"# Header",
		userLine,
		"30 1 * * * /usr/local/bin/proxsave --backup", // single fresh entry, --backup pins non-interactive behavior
	}

	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d\nGot:  %v\nWant: %v", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("line[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
