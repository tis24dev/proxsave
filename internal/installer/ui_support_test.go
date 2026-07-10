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
