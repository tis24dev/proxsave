package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// 260-7: a mixed self config (full alive URL + backup ID only) must still
// resolve the backup check from its ID instead of returning an empty backup
// URL (which records no_url). Alive and backup resolve independently, like the
// updates field and addNotify.
func TestSelfURLs_ResolvesBackupFromIDWhenAliveIsFullURL(t *testing.T) {
	d := &daemon{cfg: &config.Config{
		HealthcheckPingEndpoint: "https://hc.example",
		HealthcheckAliveURL:     "https://hc.example/alive-full",
		HealthcheckBackupID:     "bkid",
	}}
	alive, backup, _ := d.selfURLs()
	if alive != "https://hc.example/alive-full" {
		t.Fatalf("alive = %q, want the full alive URL", alive)
	}
	if backup != "https://hc.example/bkid" {
		t.Fatalf("backup = %q, want it assembled from the backup ID", backup)
	}
}

// Lock the existing IDs-only behavior: both resolve from their IDs.
func TestSelfURLs_ResolvesBothFromIDs(t *testing.T) {
	d := &daemon{cfg: &config.Config{
		HealthcheckPingEndpoint: "https://hc.example",
		HealthcheckAliveID:      "aid",
		HealthcheckBackupID:     "bid",
	}}
	alive, backup, _ := d.selfURLs()
	if alive != "https://hc.example/aid" || backup != "https://hc.example/bid" {
		t.Fatalf("alive=%q backup=%q, want assembled from IDs", alive, backup)
	}
}
