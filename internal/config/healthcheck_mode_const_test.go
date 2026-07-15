package config

import "testing"

// 259-21: the HealthcheckMode constants must equal the config semantics they
// replace. A drift here would silently change control flow in the daemon
// (centralized vs self ping resolution).
func TestHealthcheckModeConstants(t *testing.T) {
	if HealthcheckModeCentralized != "centralized" {
		t.Fatalf("HealthcheckModeCentralized = %q, want centralized", HealthcheckModeCentralized)
	}
	if HealthcheckModeSelf != "self" {
		t.Fatalf("HealthcheckModeSelf = %q, want self", HealthcheckModeSelf)
	}
}
