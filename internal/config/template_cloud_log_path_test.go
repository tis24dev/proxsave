package config

import (
	"strings"
	"testing"
)

// The template defaulted CLOUD_LOG_PATH to "/proxsave/log" next to
// CLOUD_REMOTE_PATH=/proxsave/backup. The backup path drops its leading "/" (decided
// 2026-10-04, so sftp backups stay in the login home); the log path keeps it, so on an
// sftp or ftp remote the logs went to the server root, away from the backups, and a
// non-root login could not write there. The default is now relative.
func TestTemplateCloudLogPathIsRelative(t *testing.T) {
	for line := range strings.SplitSeq(DefaultEnvTemplate(), "\n") {
		if value, ok := strings.CutPrefix(line, "CLOUD_LOG_PATH="); ok {
			value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
			if value != "proxsave/log" {
				t.Fatalf("template CLOUD_LOG_PATH = %q, want proxsave/log", value)
			}
			return
		}
	}
	t.Fatal("no CLOUD_LOG_PATH line in the template")
}
