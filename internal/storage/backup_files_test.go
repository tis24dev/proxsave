package storage

import (
	"reflect"
	"testing"
)

func TestBundlePathForNormalizesRepeatedBundleSuffixes(t *testing.T) {
	got := bundlePathFor("backup.tar.zst.bundle.tar.bundle.tar")
	want := "backup.tar.zst.bundle.tar"
	if got != want {
		t.Fatalf("bundlePathFor() = %q, want %q", got, want)
	}
}

func TestBuildBackupCandidatePathsNormalizesBundleInput(t *testing.T) {
	tests := []struct {
		name          string
		base          string
		includeBundle bool
		want          []string
	}{
		{
			name:          "bundle included",
			base:          "backup.tar.zst.bundle.tar.bundle.tar",
			includeBundle: true,
			want: []string{
				"backup.tar.zst.bundle.tar",
				"backup.tar.zst",
				"backup.tar.zst.sha256",
				"backup.tar.zst.manifest.json",
				"backup.tar.zst.metadata",
				"backup.tar.zst.metadata.sha256",
			},
		},
		{
			name:          "legacy only",
			base:          "backup.tar.zst.bundle.tar",
			includeBundle: false,
			want: []string{
				"backup.tar.zst",
				"backup.tar.zst.sha256",
				"backup.tar.zst.manifest.json",
				"backup.tar.zst.metadata",
				"backup.tar.zst.metadata.sha256",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildBackupCandidatePaths(tt.base, tt.includeBundle)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildBackupCandidatePaths() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The bundle build stages `<archive>.bundle.tar.tmp-<rand>` IN BACKUP_PATH
// (fs.CreateTemp with pattern "<base>.tmp-*", orchestrator.go), with NO leading dot.
// A crash during bundling leaves that file behind, it matches the backup glob, and a
// prefix-only check let every List count it as a backup forever: phantom counts, a
// missing-.metadata WARNING each pass (pinning exit 1), and nothing ever deleting it.
// The temp marker must therefore match anywhere in the name.
func TestBackupTempArtifactMatchesBundleTempMidName(t *testing.T) {
	cases := map[string]bool{
		"host-backup-20250101-120000.tar.zst.bundle.tar.tmp-12345": true,
		".tmp-host-backup-20250101-120000.tar.zst":                 true,
		"host-backup-20250101-120000.tar.zst.partial":              true,
		"host-backup-20250101-120000.tar.zst":                      false,
		"host-backup-20250101-120000.tar.zst.bundle.tar":           false,
	}
	for path, want := range cases {
		if got := isBackupTempArtifact("/backups/" + path); got != want {
			t.Errorf("isBackupTempArtifact(%q) = %v, want %v", path, got, want)
		}
	}
}
