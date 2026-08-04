package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bare-filename (or whitespace-only) config path has no real directory
// component, so WriteConfigFileAtomic must reject it instead of writing the
// config into the process working directory. Parity with the pre-migration
// wizard writer.
func TestWriteConfigFileAtomicRejectsDirlessPath(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configPath string
	}{
		{"bare filename", "backup.env"},
		{"dot", "."},
		{"whitespace only", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := WriteConfigFileAtomic(tc.configPath, tc.configPath+".tmp", "KEY=value\n")
			if err == nil {
				t.Fatalf("configPath %q: expected rejection, got nil", tc.configPath)
			}
			if !strings.Contains(err.Error(), "invalid configuration path") {
				t.Fatalf("configPath %q: error=%q, want the validation message", tc.configPath, err.Error())
			}
		})
	}
}

// A real (absolute) config path still writes atomically: the guard must not
// break the normal case.
func TestWriteConfigFileAtomicWritesAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	tmpPath := configPath + ".tmp"
	const content = "SCHEDULER_MODE=daemon\n"

	if err := WriteConfigFileAtomic(configPath, tmpPath, content); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read back config: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content=%q, want %q", string(got), content)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp file should be gone after rename, stat err=%v", err)
	}
}

// A FAILED write must not leave the temp entry behind either. The cleanup defer
// is registered BEFORE root.WriteFile precisely so this path is covered: with the
// defer registered after the write (as it was before the callers in cmd/ dropped
// their own outer cleanup defers), an ENOSPC/EIO/EDQUOT write returned with the
// defer never registered and orphaned the temp file. tmpPath is pre-created as an
// empty directory here, which is the portable way to make root.WriteFile fail.
func TestWriteConfigFileAtomicCleansUpAfterFailedWrite(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "backup.env")
	tmpPath := configPath + ".tmp"

	if err := os.Mkdir(tmpPath, 0o700); err != nil {
		t.Fatalf("seed temp path as a directory: %v", err)
	}

	if err := WriteConfigFileAtomic(configPath, tmpPath, "KEY=value\n"); err == nil {
		t.Fatal("writing over a directory must fail")
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Fatalf("temp entry must be removed after a failed write, stat err=%v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config must not exist after a failed write, stat err=%v", err)
	}
}

// The preserved set is the compile-time [build env identity], so the renderer
// is pure: one trailing slash per entry, no filesystem lookup. Table moved
// verbatim from the CLI-only copy this function replaced.
func TestFormatPreservedEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []string
		want    string
	}{
		{
			name:    "formats trimmed entries",
			entries: []string{" build ", "env", " identity"},
			want:    "build/ env/ identity/",
		},
		{
			name:    "returns none for nil input",
			entries: nil,
			want:    "(none)",
		},
		{
			name:    "returns none for blank input",
			entries: []string{"", " ", "\t"},
			want:    "(none)",
		},
		{
			name:    "normalizes trailing slashes",
			entries: []string{"env/", "build//", " identity/// ", "/"},
			want:    "env/ build/ identity/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatPreservedEntries(tt.entries); got != tt.want {
				t.Fatalf("FormatPreservedEntries(%v) = %q, want %q", tt.entries, got, tt.want)
			}
		})
	}
}
