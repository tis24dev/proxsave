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
