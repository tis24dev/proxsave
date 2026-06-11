package orchestrator

import (
	"errors"
	"testing"
)

// Regression for preflight-skip-rolls-back-valid-config (2026-06-09 audit):
// maybeInstallNetworkConfigFromStage routed every !preflight.Ok() straight to the
// automatic rollback. But Ok() is `!Skipped && ExitError==nil`, so when NO validator
// binary (ifup/ifreload) was available the preflight returned Skipped=true and Ok()
// was false, rolling back a perfectly valid restored network config. The fix keys
// the rollback off a genuine FAILURE, not a SKIP. Written after that change.
func TestNetworkPreflightWarrantsRollback(t *testing.T) {
	cases := []struct {
		name   string
		result networkPreflightResult
		want   bool
	}{
		{"ok (ran, no error)", networkPreflightResult{}, false},
		{"skipped (no validator)", networkPreflightResult{Skipped: true, SkipReason: "ifup/ifreload not found"}, false},
		{"genuine validation failure", networkPreflightResult{ExitError: errors.New("ifup -na failed")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := networkPreflightWarrantsRollback(c.result); got != c.want {
				t.Fatalf("networkPreflightWarrantsRollback(%+v) = %v, want %v", c.result, got, c.want)
			}
		})
	}
}
