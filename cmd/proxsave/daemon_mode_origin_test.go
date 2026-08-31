// Package main contains the proxsave command entrypoint.
package main

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// The --upgrade retrofit decides on the PRESENCE of SCHEDULER_MODE in backup.env, not on its
// value: a host that has never recorded a scheduler engine is the one the daemon is installed
// on, and a host that records one is left alone whatever it says. The signal is
// UpgradeResult.MissingKeys, which names the keys THIS upgrade's template merge had to add
// (internal/config/upgrade.go:344-352 populates it on every return path, including the two
// where nothing changed, so a no-op merge still answers the question).
//
// It is a tri-state, not a bool. cfgUpgradeResult is nil whenever the config merge itself
// failed (cmd/proxsave/upgrade.go:278-283) while the retrofit still runs, and collapsing that
// onto either answer would decide the host's scheduler on evidence that does not exist.
func TestSchedulerModeOriginFromUpgrade(t *testing.T) {
	// Fail closed: the zero value must be the one that decides nothing.
	if schedulerModeOriginUnknown != 0 {
		t.Fatal("the unknown origin must be the zero value, so a forgotten assignment decides nothing")
	}

	for _, tc := range []struct {
		name   string
		result *config.UpgradeResult
		want   schedulerModeOrigin
	}{
		{"merge result unavailable", nil, schedulerModeOriginUnknown},
		{"merge added the key: the host never recorded an engine", &config.UpgradeResult{MissingKeys: []string{"HEALTHCHECK_ENABLED", "SCHEDULER_MODE"}}, schedulerModeOriginInjected},
		{"merge added other keys: the key was already there", &config.UpgradeResult{MissingKeys: []string{"HEALTHCHECK_ENABLED"}}, schedulerModeOriginConfigured},
		{"merge added nothing", &config.UpgradeResult{}, schedulerModeOriginConfigured},
		// The merge reports the key as the TEMPLATE spells it, but a mismatched case or a
		// stray space must not read as "already configured" and strand the host on cron.
		{"case and padding are not evidence of presence", &config.UpgradeResult{MissingKeys: []string{" scheduler_mode "}}, schedulerModeOriginInjected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := schedulerModeOriginFromUpgrade(tc.result); got != tc.want {
				t.Errorf("schedulerModeOriginFromUpgrade(%+v) = %v, want %v", tc.result, got, tc.want)
			}
		})
	}
}
