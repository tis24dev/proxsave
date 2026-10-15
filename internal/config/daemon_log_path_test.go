package config

import (
	"os"
	"path/filepath"
	"testing"
)

// DAEMON_LOG_PATH: absent or empty is the folder inside LOG_PATH; ${LOG_PATH} expands to
// the run logs' folder for whoever wants the two together (discussion #336).
func TestDaemonLogPath(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"absent", "", "/srv/logs/daemon"},
		{"empty", "DAEMON_LOG_PATH=\n", "/srv/logs/daemon"},
		{"template default", "DAEMON_LOG_PATH=${LOG_PATH}/daemon\n", "/srv/logs/daemon"},
		{"same folder as the run logs", "DAEMON_LOG_PATH=${LOG_PATH}\n", "/srv/logs"},
		{"elsewhere", "DAEMON_LOG_PATH=/var/log/proxsave-daemon\n", "/var/log/proxsave-daemon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "backup.env")
			content := "LOG_PATH=/srv/logs\n" + tc.line
			if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			setBaseDirEnv(t, "/opt/proxsave")
			cfg, err := LoadConfigWithBaseDir(configPath, "/opt/proxsave")
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.DaemonLogPath != tc.want {
				t.Fatalf("DaemonLogPath = %q, want %q", cfg.DaemonLogPath, tc.want)
			}
		})
	}
}

// ${LOG_PATH} in DAEMON_LOG_PATH is the folder the run logs really go to. A LOG_PATH
// left empty or deleted falls back to its default, and the template line must follow
// it there rather than expand to "/daemon".
func TestDaemonLogPathFollowsTheDefaultLogFolder(t *testing.T) {
	for name, logLine := range map[string]string{"LOG_PATH empty": "LOG_PATH=\n", "LOG_PATH absent": ""} {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "backup.env")
			if err := os.WriteFile(configPath, []byte(logLine+"DAEMON_LOG_PATH=${LOG_PATH}/daemon\n"), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := LoadConfigWithBaseDir(configPath, "/opt/proxsave")
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if want := filepath.Join(cfg.LogPath, "daemon"); cfg.DaemonLogPath != want {
				t.Fatalf("DaemonLogPath = %q, want %q next to LogPath %q", cfg.DaemonLogPath, want, cfg.LogPath)
			}
		})
	}
}
