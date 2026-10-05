package security

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// With the PBS storage on, proxmox-backup-client is an optional dependency, like rclone
// for the cloud; with it off the client is not listed.
func TestBuildDependencyListPBSStorage(t *testing.T) {
	for _, tc := range []struct {
		enabled bool
		want    bool
	}{{true, true}, {false, false}} {
		checker := &Checker{
			logger:   newSecurityTestLogger(),
			cfg:      &config.Config{PBSTargetEnabled: tc.enabled, PBSTargetStorage: "pbs-main"},
			result:   &Result{},
			lookPath: stubLookPath(map[string]bool{}),
		}
		var found *dependencyEntry
		deps := checker.buildDependencyList()
		for i := range deps {
			if deps[i].Name == "proxmox-backup-client" {
				found = &deps[i]
			}
		}
		if (found != nil) != tc.want {
			t.Fatalf("PBS_TARGET_ENABLED=%v: proxmox-backup-client listed=%v, want %v", tc.enabled, found != nil, tc.want)
		}
		if found != nil && (found.Required || found.Reason != "PBS storage uploads enabled") {
			t.Fatalf("dependency = %+v, want optional with reason %q", *found, "PBS storage uploads enabled")
		}
	}
}
