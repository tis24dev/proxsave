package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

func TestFilterCronLines(t *testing.T) {
	// Define the user's specific lines that must be preserved
	userLine1 := "0 12 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh 192.168.1.5 htpc-1 /mnt/pve/nas"
	userLine2 := "0 2 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh pbs.miodominio.com pbs1-test /mnt/pve/nas"

	tests := []struct {
		name         string
		inputLines   []string
		correctPaths []string
		wantLines    []string
		wantHasEntry bool
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
			name: "Remove outdated binary reference",
			inputLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup", // Outdated path
				userLine1,
			},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				userLine1,
			},
			wantHasEntry: false,
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
			wantHasEntry: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLines, gotHasEntry := filterCronLines(tt.inputLines, tt.correctPaths)

			if gotHasEntry != tt.wantHasEntry {
				t.Errorf("hasCurrentEntry = %v, want %v", gotHasEntry, tt.wantHasEntry)
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
