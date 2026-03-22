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
