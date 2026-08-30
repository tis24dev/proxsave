// Package main contains the proxsave command entrypoint.
package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/config"
)

// schedulerModeOrigin answers one question about an --upgrade in progress: did backup.env
// already carry SCHEDULER_MODE before this upgrade's template merge ran?
//
// That, and not the key's VALUE, is what the daemon retrofit decides on. A host that has
// recorded a scheduler engine has made a choice, and cron means cron; a host that has never
// recorded one has made no choice at all, and it is the only host the daemon is installed on.
// The value cannot carry that distinction because an absent key resolves to the template
// default, which is "cron" (internal/config/config.go:841), so "chose cron" and "never chose"
// read identically once the merge has run.
type schedulerModeOrigin int

const (
	// schedulerModeOriginUnknown means the merge result is not available, so nothing about
	// the key's provenance is established. It is the ZERO VALUE deliberately: a caller that
	// forgets to set this decides nothing rather than deciding wrongly.
	schedulerModeOriginUnknown schedulerModeOrigin = iota
	// schedulerModeOriginConfigured means the key was already in the file.
	schedulerModeOriginConfigured
	// schedulerModeOriginInjected means this upgrade's merge added it.
	schedulerModeOriginInjected
)

// schedulerModeOriginFromUpgrade reads that provenance off the config merge's own report.
//
// MissingKeys names the template keys the merge had to ADD, which is exactly "was absent
// before". It is populated on every return path of computeConfigUpgrade, including the two
// that change nothing (internal/config/upgrade.go:344-352), so a merge that rewrote nothing
// still answers the question.
//
// A present-but-empty "SCHEDULER_MODE=" counts as PRESENT, and that is the merge's rule
// rather than one made here: parseEnvValues yields an entry with an empty value, and the
// missing-key walk skips any key that has one (upgrade.go:302-304), so an empty assignment is
// never reported. The operator wrote the line; the engine then resolves to the default.
func schedulerModeOriginFromUpgrade(result *config.UpgradeResult) schedulerModeOrigin {
	if result == nil {
		return schedulerModeOriginUnknown
	}
	for _, key := range result.MissingKeys {
		if strings.EqualFold(strings.TrimSpace(key), "SCHEDULER_MODE") {
			return schedulerModeOriginInjected
		}
	}
	return schedulerModeOriginConfigured
}
