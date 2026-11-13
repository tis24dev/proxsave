package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The runner swallows every exit status, so a dry-run killed by a cancel or the 90 s
// bound, or one that stopped early (exit 16), returned no suggestions and no error: a
// clean "no unused components". Those now come back as errors; exit 1 stays the normal
// case the hints are parsed from.
func TestPostInstallAuditReportsAnIncompleteDryRun(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(cfgPath, []byte("BACKUP_CLUSTER_CONFIG=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRunner := postInstallAuditRunner
	t.Cleanup(func() { postInstallAuditRunner = oldRunner })

	for _, tc := range []struct {
		name    string
		code    int
		cancel  bool
		wantErr string
	}{
		{"exit 16", 16, false, "post-install audit dry-run failed: exit code 16"},
		{"cancelled", 1, true, "post-install audit dry-run failed: context canceled"},
		{"warnings (exit 1)", 1, false, ""},
	} {
		postInstallAuditRunner = func(ctx context.Context, execPath, configPath string) (string, int, error) {
			return "", tc.code, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		if tc.cancel {
			cancel()
		}
		_, err := CollectPostInstallDisableSuggestions(ctx, "/fake/proxsave", cfgPath)
		cancel()
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != tc.wantErr {
			t.Errorf("%s: error %q, want %q", tc.name, got, tc.wantErr)
		}
	}
}
