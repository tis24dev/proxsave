// Package main contains the proxsave command entrypoint.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/cli"
)

func TestSetBackupEnvKeysReplacesAndAppends(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "backup.env")
	initial := "BACKUP_PATH=/data\n" +
		"SCHEDULER_MODE=cron           # cron | daemon\n" +
		"HEALTHCHECK_ENABLED=false\n"
	if err := os.WriteFile(cfgPath, []byte(initial), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := setBackupEnvKeys(cfgPath, map[string]string{
		"SCHEDULER_MODE": "daemon", // existing -> replaced
		"DAEMON_OPT_OUT": "true",   // missing  -> appended
	}); err != nil {
		t.Fatalf("setBackupEnvKeys: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "SCHEDULER_MODE=daemon") {
		t.Errorf("SCHEDULER_MODE not switched to daemon:\n%s", content)
	}
	if strings.Contains(content, "SCHEDULER_MODE=cron") {
		t.Errorf("old SCHEDULER_MODE=cron still present:\n%s", content)
	}
	// The inline comment must survive the replacement.
	if !strings.Contains(content, "# cron | daemon") {
		t.Errorf("inline comment lost:\n%s", content)
	}
	if !strings.Contains(content, "DAEMON_OPT_OUT=true") {
		t.Errorf("missing key not appended:\n%s", content)
	}
	// Untouched keys stay put.
	if !strings.Contains(content, "BACKUP_PATH=/data") || !strings.Contains(content, "HEALTHCHECK_ENABLED=false") {
		t.Errorf("unrelated keys disturbed:\n%s", content)
	}
}

func TestReadConfiguredSchedulerMode(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"daemon", write("d.env", "SCHEDULER_MODE=daemon\n"), "daemon"},
		{"cron", write("c.env", "SCHEDULER_MODE=cron\n"), "cron"},
		{"key absent", write("none.env", "BACKUP_PATH=/x\n"), "cron"},
		{"garbage value", write("g.env", "SCHEDULER_MODE=weird\n"), "cron"},
		{"missing file", filepath.Join(dir, "nope.env"), "cron"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readConfiguredSchedulerMode(tc.path); got != tc.want {
				t.Fatalf("readConfiguredSchedulerMode = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCronCorrectPaths(t *testing.T) {
	if got := cronCorrectPaths(daemonExecPath); len(got) != 1 || got[0] != daemonExecPath {
		t.Errorf("same-as-canonical -> %v, want [%s]", got, daemonExecPath)
	}
	got := cronCorrectPaths("/opt/proxsave/proxsave")
	if len(got) != 2 || got[0] != daemonExecPath || got[1] != "/opt/proxsave/proxsave" {
		t.Errorf("distinct exec -> %v, want [canonical, /opt/proxsave/proxsave]", got)
	}
	if got := cronCorrectPaths(""); len(got) != 1 || got[0] != daemonExecPath {
		t.Errorf("empty exec -> %v, want [%s]", got, daemonExecPath)
	}
}

func TestValidateDaemonCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		args    cli.Args
		wantErr bool
	}{
		{"daemon alone ok", cli.Args{Daemon: true}, false},
		{"daemon-setup alone ok", cli.Args{DaemonSetup: true}, false},
		{"daemon-remove alone ok", cli.Args{DaemonRemove: true}, false},
		{"none ok", cli.Args{}, false},
		{"two daemon flags rejected", cli.Args{Daemon: true, DaemonSetup: true}, true},
		{"setup+remove rejected", cli.Args{DaemonSetup: true, DaemonRemove: true}, true},
		{"daemon + install rejected", cli.Args{Daemon: true, Install: true}, true},
		{"daemon-setup + upgrade rejected", cli.Args{DaemonSetup: true, Upgrade: true}, true},
		{"daemon + backup rejected", cli.Args{Daemon: true, Backup: true}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msgs := validateDaemonCompatibility(&tc.args)
			if tc.wantErr && len(msgs) == 0 {
				t.Fatalf("expected an incompatibility message, got none")
			}
			if !tc.wantErr && len(msgs) != 0 {
				t.Fatalf("expected no message, got %v", msgs)
			}
		})
	}
}
