package storage

import (
	"github.com/tis24dev/proxsave/internal/logging"
)

// NormalizeGFSRetentionConfig applies the required adjustments to the GFS configuration
// before running retention. Currently:
//   - ensures the DAILY tier is at least 1 (minimum accepted value)
//     when the policy is gfs and RETENTION_DAILY is <= 0.
//   - emits a log line to document the adjustment.
func NormalizeGFSRetentionConfig(logger *logging.Logger, backendName string, cfg RetentionConfig) RetentionConfig {
	if cfg.Policy != "gfs" {
		return cfg
	}

	effective := cfg
	if effective.Daily <= 0 {
		if logger != nil {
			logger.Info("%s: RETENTION_DAILY is %d or not set, enforcing minimum of 1 daily backup", backendName, cfg.Daily)
		}
		effective.Daily = 1
	}

	return effective
}
