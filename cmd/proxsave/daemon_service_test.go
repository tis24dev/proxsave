// Package main contains the proxsave command entrypoint.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDaemonUnitToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"empty is valid", "", false},
		{"canonical path", "/usr/local/bin/proxsave", false},
		{"absolute config", "/opt/proxsave/configs/backup.env", false},
		{"space rejected", "/opt/my backups/backup.env", true},
		{"tab rejected", "/opt/x\tbackup.env", true},
		{"newline rejected (unit injection)", "/opt/x\nExecStartPre=/bin/rm", true},
		{"carriage return rejected", "/opt/x\rbackup.env", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDaemonUnitToken("config path", tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("token %q: expected error, got nil", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("token %q: unexpected error: %v", tc.token, err)
			}
		})
	}
}

// A whitespace config path must be rejected up front, before installDaemonService
// writes the unit or calls systemctl (so the error is the validation message, not
// a filesystem/permission error from the write).
func TestInstallDaemonServiceRejectsWhitespaceToken(t *testing.T) {
	err := installDaemonService(context.Background(), "/usr/local/bin/proxsave", "/opt/my backups/backup.env", nil)
	if err == nil {
		t.Fatal("expected rejection for a config path containing a space")
	}
	if !strings.Contains(err.Error(), "config path must not contain whitespace") {
		t.Fatalf("error=%q, want the validation message", err.Error())
	}
}

func TestBuildDaemonUnitWithConfig(t *testing.T) {
	u := buildDaemonUnit("/usr/local/bin/proxsave", "/opt/proxsave/configs/backup.env")
	for _, want := range []string{
		"ExecStart=/usr/local/bin/proxsave --daemon --config /opt/proxsave/configs/backup.env",
		"Type=simple",
		"Restart=always",
		"RestartSec=10",
		"WantedBy=multi-user.target",
		"After=network-online.target",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit missing %q:\n%s", want, u)
		}
	}
}

func TestBuildDaemonUnitFallbacks(t *testing.T) {
	// Empty exec token -> canonical path; empty config -> no --config.
	u := buildDaemonUnit("", "")
	if !strings.Contains(u, "ExecStart="+daemonExecPath+" --daemon\n") {
		t.Errorf("expected canonical ExecStart without --config:\n%s", u)
	}
	if strings.Contains(u, "--config") {
		t.Errorf("empty config should not emit --config:\n%s", u)
	}
}

func TestBuildDaemonUnitEscapesPercent(t *testing.T) {
	u := buildDaemonUnit("/usr/local/bin/proxsave", "/opt/proxsave/50%off/backup.env")
	// The '%' in the config path must reach ExecStart as the systemd literal '%%',
	// otherwise systemd expands it as a specifier and corrupts --config.
	want := "ExecStart=/usr/local/bin/proxsave --daemon --config /opt/proxsave/50%%off/backup.env"
	if !strings.Contains(u, want) {
		t.Fatalf("percent in config path must be escaped in ExecStart\nwant substring: %q\ngot:\n%s", want, u)
	}
}

// A failed rename must leave the previous unit intact, never truncated: the in-place
// os.WriteFile truncated the existing unit before it could fail.
func TestWriteUnitFileAtomic_FailureKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unit.service")
	const old = "OLD-UNIT"
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	orig := unitRenameFunc
	t.Cleanup(func() { unitRenameFunc = orig })
	unitRenameFunc = func(oldname, newname string) error { return errors.New("rename boom") }

	if err := writeUnitFileAtomic(path, []byte("NEW-UNIT-CONTENT"), 0o644); err == nil {
		t.Fatal("want error, got nil")
	}
	got, _ := os.ReadFile(path)
	if string(got) != old {
		t.Fatalf("previous unit modified: got %q want %q", string(got), old)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp: %s", e.Name())
		}
	}
}

func TestWriteUnitFileAtomic_WritesContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unit.service")
	if err := writeUnitFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "hello" {
		t.Fatalf("content = %q", string(got))
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("perm = %v", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("unexpected dir entries: %v", entries)
	}
}
