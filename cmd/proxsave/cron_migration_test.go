package main

import (
	"testing"
)

func TestFilterCronLines(t *testing.T) {
	// Define the user's specific lines that must be preserved
	userLine1 := "0 12 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh 192.168.1.5 htpc-1 /mnt/pve/nas"
	userLine2 := "0 2 * * * /mnt/pve/nas/scripts/proxmox/proxmox-backup-client/backup_folders-nightly.sh pbs.miodominio.com pbs1-test /mnt/pve/nas"

	tests := []struct {
		name         string
		inputLines   []string
		legacyPaths  []string
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
			legacyPaths:  []string{"/opt/proxsave/script/proxmox-backup.sh"},
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
			name: "Remove legacy bash script",
			inputLines: []string{
				"0 2 * * * /opt/proxsave/script/proxmox-backup.sh",
				userLine1,
			},
			legacyPaths:  []string{"/opt/proxsave/script/proxmox-backup.sh"},
			correctPaths: []string{"/usr/local/bin/proxsave"},
			wantLines: []string{
				userLine1,
			},
			wantHasEntry: false,
		},
		{
			name: "Remove outdated binary reference",
			inputLines: []string{
				"0 2 * * * /usr/bin/proxmox-backup", // Outdated path
				userLine1,
			},
			legacyPaths:  []string{},
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
			legacyPaths:  []string{},
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
			legacyPaths:  []string{},
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
			legacyPaths:  []string{},
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
				"0 2 * * * /opt/proxsave/script/proxmox-backup.sh", // Legacy
				userLine1,                           // Keep
				"0 4 * * * /usr/bin/proxsave",       // Wrong path
				userLine2,                           // Keep
				"0 5 * * * /usr/local/bin/proxsave", // Correct
			},
			legacyPaths:  []string{"/opt/proxsave/script/proxmox-backup.sh"},
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
			gotLines, gotHasEntry := filterCronLines(tt.inputLines, tt.legacyPaths, tt.correctPaths)

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
