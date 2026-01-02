package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeriveBaseDirFromConfig(t *testing.T) {
	tests := []struct {
		name       string
		configPath string
		want       string
	}{
		{"typical", "/opt/proxsave/env/backup.env", "/opt/proxsave"},
		{"root file fallback", "/backup.env", "/opt/proxsave"},
		{"relative fallback", "backup.env", "/opt/proxsave"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveBaseDirFromConfig(tt.configPath); got != tt.want {
				t.Fatalf("deriveBaseDirFromConfig(%q) = %q, want %q", tt.configPath, got, tt.want)
			}
		})
	}
}

func TestCleanupTempConfig(t *testing.T) {
	t.Run("empty is noop", func(t *testing.T) {
		cleanupTempConfig("")
	})

	t.Run("removes file when present", func(t *testing.T) {
		dir := t.TempDir()
		tmp := filepath.Join(dir, "config.tmp")
		if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cleanupTempConfig(tmp)

		if _, err := os.Stat(tmp); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed, stat err=%v", tmp, err)
		}
	})

	t.Run("missing file is noop", func(t *testing.T) {
		dir := t.TempDir()
		tmp := filepath.Join(dir, "missing.tmp")
		cleanupTempConfig(tmp)
	})
}
