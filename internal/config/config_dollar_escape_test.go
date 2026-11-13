package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A '$' could not be written in any backup.env value: quotes are stripped before the
// expansion, a backslash stays and the name after it is still looked up, and "$$" was
// consumed whole, so a webhook password "p$ssw0rd" reached the endpoint as "p". "$$" is
// now a literal '$', in every value.
func TestConfigDoubleDollarIsALiteralDollar(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), "backup.env")
	content := `WEBHOOK_ENABLED=true
WEBHOOK_ENDPOINTS=ops
WEBHOOK_OPS_URL=https://hooks.example.invalid/x?k=a$$b
WEBHOOK_OPS_AUTH_TYPE=basic
WEBHOOK_OPS_AUTH_USER=admin
WEBHOOK_OPS_AUTH_PASS='p$$ssw0rd'
WEBHOOK_OPS_AUTH_SECRET="ab$$$$cd"
WEBHOOK_OPS_AUTH_TOKEN=tok$$
WEBHOOK_OPS_HEADERS=X-Key:v$$al
`
	if err := os.WriteFile(envPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigWithBaseDir(envPath, t.TempDir())
	if err != nil {
		t.Fatalf("LoadConfigWithBaseDir: %v", err)
	}
	wh := cfg.BuildWebhookConfig()
	if len(wh.Endpoints) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(wh.Endpoints))
	}
	ep := wh.Endpoints[0]
	for _, tc := range []struct{ name, got, want string }{
		{"URL", ep.URL, "https://hooks.example.invalid/x?k=a$b"},
		{"AUTH_PASS", ep.Auth.Pass, "p$ssw0rd"},
		{"AUTH_SECRET", ep.Auth.Secret, "ab$$cd"},
		{"AUTH_TOKEN", ep.Auth.Token, "tok$"},
		{"HEADERS", ep.Headers["X-Key"], "v$al"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
