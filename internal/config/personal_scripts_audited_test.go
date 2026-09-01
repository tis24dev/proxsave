package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// personalScriptWarning is the note the maintainer froze, byte for byte. It is deliberately
// alarming and it is not to be reworded here or in the template.
const personalScriptWarning = "# WARNING: a failure of these scripts will block the backup. Use with caution."

// TestPersonalScriptPathsAreReadFromTheConfig pins the read site. Without the explicit-value
// rows a typo in either key name passes the entire repo suite: nothing else in the tree
// cross-checks a string setting against the template it ships in.
func TestPersonalScriptPathsAreReadFromTheConfig(t *testing.T) {
	cases := []struct {
		name     string
		content  string
		wantPre  string
		wantPost string
	}{
		{
			name:     "explicit paths",
			content:  "PERSONAL_SCRIPT_PRE_RUN=/opt/ops/pre.sh\nPERSONAL_SCRIPT_POST_RUN=/opt/ops/post.sh\n",
			wantPre:  "/opt/ops/pre.sh",
			wantPost: "/opt/ops/post.sh",
		},
		{
			name:    "keys absent entirely",
			content: "BACKUP_PATH=/data\n",
		},
		{
			name:    "keys present but empty",
			content: "PERSONAL_SCRIPT_PRE_RUN=\nPERSONAL_SCRIPT_POST_RUN=\n",
		},
		{
			name:     "surrounding whitespace is trimmed",
			content:  "PERSONAL_SCRIPT_PRE_RUN=   /opt/ops/pre.sh   \nPERSONAL_SCRIPT_POST_RUN=\t/opt/ops/post.sh\t\n",
			wantPre:  "/opt/ops/pre.sh",
			wantPost: "/opt/ops/post.sh",
		},
		{
			// The shipped template must arm nothing. An example path left in it would start
			// a script on every fresh install, silently, on the operator's behalf.
			name:    "the shipped template",
			content: DefaultEnvTemplate(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadEnvForTest(t, "backup.env", tc.content)
			if cfg.PersonalScriptPreRun != tc.wantPre {
				t.Errorf("PersonalScriptPreRun = %q, want %q", cfg.PersonalScriptPreRun, tc.wantPre)
			}
			if cfg.PersonalScriptPostRun != tc.wantPost {
				t.Errorf("PersonalScriptPostRun = %q, want %q", cfg.PersonalScriptPostRun, tc.wantPost)
			}
		})
	}
}

// TestTemplateCarriesThePersonalScriptWarningInline pins WHERE the frozen note lives, not only
// that it exists. The upgrade merge collects a template's KEY=VALUE lines and skips every
// standalone comment (upgrade.go:258-261), so a note written on its own line would reach
// fresh installs only. Inline on both keys is what carries it to an upgraded backup.env.
func TestTemplateCarriesThePersonalScriptWarningInline(t *testing.T) {
	for _, key := range []string{"PERSONAL_SCRIPT_PRE_RUN=", "PERSONAL_SCRIPT_POST_RUN="} {
		var found bool
		for _, line := range strings.Split(DefaultEnvTemplate(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), key) {
				found = true
				if !strings.Contains(line, personalScriptWarning) {
					t.Errorf("template line %q does not carry the frozen warning inline", line)
				}
			}
		}
		if !found {
			t.Errorf("template has no %s line", key)
		}
	}
}

// TestUpgradeAddsThePersonalScriptKeysWithTheirWarning is the reason the note is inline: an
// existing install that predates the feature must gain both keys AND see the warning.
func TestUpgradeAddsThePersonalScriptKeysWithTheirWarning(t *testing.T) {
	withTemplate(t, DefaultEnvTemplate(), func() {
		configPath := filepath.Join(t.TempDir(), "backup.env")
		if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/legacy\n"), 0o600); err != nil {
			t.Fatalf("write legacy config: %v", err)
		}

		if _, err := UpgradeConfigFile(configPath); err != nil {
			t.Fatalf("UpgradeConfigFile: %v", err)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read upgraded config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "BACKUP_PATH=/legacy") {
			t.Fatalf("the upgrade dropped the operator's own value:\n%s", content)
		}
		for _, key := range []string{"PERSONAL_SCRIPT_PRE_RUN=", "PERSONAL_SCRIPT_POST_RUN="} {
			if !strings.Contains(content, key) {
				t.Errorf("upgraded config is missing %s", key)
			}
		}
		if got := strings.Count(content, personalScriptWarning); got != 2 {
			t.Errorf("the frozen warning reached the upgraded config %d times, want 2 (one per key)", got)
		}
	})
}
