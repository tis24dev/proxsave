package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An install from before issue 347 has no HEALTHCHECK_ACCOUNT_INITIAL_EMAIL: the upgrade merge adds it,
// empty, with its template comment, under HEALTHCHECK_MODE, and keeps every value the operator set.
func TestUpgradeAddsTheInitialEmailVariableEmpty(t *testing.T) {
	var kept []string
	for line := range strings.SplitSeq(DefaultEnvTemplate(), "\n") {
		if strings.HasPrefix(line, "HEALTHCHECK_ACCOUNT_INITIAL_EMAIL=") {
			continue
		}
		if strings.HasPrefix(line, "HEALTHCHECK_MODE=") {
			line = "HEALTHCHECK_MODE=self"
		}
		kept = append(kept, line)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.env")
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := UpgradeConfigFileWithBaseDir(path, dir); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	want := "HEALTHCHECK_ACCOUNT_INITIAL_EMAIL=   # centralized: email for your monitoring portal; applied once after you confirm the mail, then change it in the portal"
	if !strings.Contains(got, want) {
		t.Fatalf("variable not added as in the template:\n%s", got)
	}
	if !strings.Contains(got, "HEALTHCHECK_MODE=self") {
		t.Fatalf("an operator value was changed")
	}
	if i, j := strings.Index(got, "HEALTHCHECK_MODE=self"), strings.Index(got, want); j < i {
		t.Fatalf("added before HEALTHCHECK_MODE")
	}
}
