package storage

import (
	"fmt"
	"sort"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// RetentionConfig defines the retention policy configuration
type RetentionConfig struct {
	// Policy type: "simple" (count-based) or "gfs" (time-distributed)
	Policy string

	// Simple retention: total number of backups to keep
	MaxBackups int

	// GFS retention: time-distributed, hierarchical (daily → weekly → monthly → yearly)
	// Daily: keep the last N backups (newest first)
	Daily   int // Keep last N backups as daily
	Weekly  int // Keep N weekly backups (one per week)
	Monthly int // Keep N monthly backups (one per month)
	Yearly  int // Keep N yearly backups (one per year, 0 = keep all)
}

// RetentionCategory represents the classification of a backup
type RetentionCategory string

const (
	CategoryDaily   RetentionCategory = "daily"
	CategoryWeekly  RetentionCategory = "weekly"
	CategoryMonthly RetentionCategory = "monthly"
	CategoryYearly  RetentionCategory = "yearly"
	CategoryDelete  RetentionCategory = "delete"
)

// NewRetentionConfigFromConfig creates a RetentionConfig from main Config
// Auto-detects whether to use simple or GFS policy based on configuration
func NewRetentionConfigFromConfig(cfg *config.Config, location BackupLocation) RetentionConfig {
	rc := RetentionConfig{
		Daily:   cfg.RetentionDaily,
		Weekly:  cfg.RetentionWeekly,
		Monthly: cfg.RetentionMonthly,
		Yearly:  cfg.RetentionYearly,
	}

	// Auto-detect policy: if any GFS parameter is set, use GFS
	if cfg.IsGFSRetentionEnabled() {
		rc.Policy = "gfs"
	} else {
		rc.Policy = "simple"
		// Use location-specific max backups for simple policy
		switch location {
		case LocationPrimary:
			rc.MaxBackups = cfg.LocalRetentionDays
		case LocationSecondary:
			rc.MaxBackups = cfg.SecondaryRetentionDays
		case LocationCloud:
			rc.MaxBackups = cfg.CloudRetentionDays
		default:
			rc.MaxBackups = 7 // fallback
		}
	}

	return rc
}

// ClassifyBackupsGFS classifies backups according to GFS (Grandfather-Father-Son) scheme
// Returns a map of backup -> category, allowing intelligent time-distributed retention
func ClassifyBackupsGFS(backups []*types.BackupMetadata, config RetentionConfig) map[*types.BackupMetadata]RetentionCategory {
	if len(backups) == 0 {
		return make(map[*types.BackupMetadata]RetentionCategory)
	}

	// Sort by timestamp descending (newest first)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	classification := make(map[*types.BackupMetadata]RetentionCategory)
	now := time.Now()
	currentYear, currentWeek := now.ISOWeek()
	currentYearInt := now.Year()
	currentMonth := int(now.Month())

	// 1. DAILY: Keep the last N backups (newest first)
	dailyLimit := config.Daily
	if dailyLimit < 0 {
		dailyLimit = 0
	}
	dailyCount := 0
	dailyCutIndex := len(backups)
	if dailyLimit > 0 {
		for i, b := range backups {
			if dailyCount >= dailyLimit {
				dailyCutIndex = i
				break
			}
			classification[b] = CategoryDaily
			dailyCount++
		}
	}
	if dailyCount < dailyLimit {
		dailyCutIndex = len(backups)
	}

	// 2. WEEKLY: Keep one backup per week (ISO week number)
	// Only consider backups older than the oldest daily and not already classified.
	// Only weeks strictly before the current ISO week are eligible.
	if config.Weekly > 0 {
		weeksSeen := make(map[string]bool)
		for i := dailyCutIndex; i < len(backups); i++ {
			b := backups[i]
			if classification[b] != "" {
				continue // Already classified
			}

			year, week := b.Timestamp.ISOWeek()
			// Skip backups from the current ISO week or later (should not happen for past backups)
			if year > currentYear || (year == currentYear && week >= currentWeek) {
				continue
			}

			weekKey := fmt.Sprintf("%d-W%02d", year, week)

			if !weeksSeen[weekKey] && len(weeksSeen) < config.Weekly {
				classification[b] = CategoryWeekly
				weeksSeen[weekKey] = true
			}
		}
	}

	// 3. MONTHLY: Keep one backup per month
	// Only consider backups older than the oldest daily and not already classified.
	// Only months strictly before the current month are eligible.
	if config.Monthly > 0 {
		monthsSeen := make(map[string]bool)
		for i := dailyCutIndex; i < len(backups); i++ {
			b := backups[i]
			if classification[b] != "" {
				continue
			}

			byear := b.Timestamp.Year()
			bmonth := int(b.Timestamp.Month())
			if byear > currentYearInt || (byear == currentYearInt && bmonth >= currentMonth) {
				continue
			}

			monthKey := b.Timestamp.Format("2006-01")

			if !monthsSeen[monthKey] && len(monthsSeen) < config.Monthly {
				classification[b] = CategoryMonthly
				monthsSeen[monthKey] = true
			}
		}
	}

	// 4. YEARLY: Keep one backup per year
	// If Yearly == 0, keep all yearly backups (infinite retention)
	if config.Yearly >= 0 {
		yearsSeen := make(map[string]bool)
		for i := dailyCutIndex; i < len(backups); i++ {
			b := backups[i]
			if classification[b] != "" {
				continue
			}

			byear := b.Timestamp.Year()
			// Only consider years strictly before the current year
			if byear >= currentYearInt {
				continue
			}

			yearKey := b.Timestamp.Format("2006")

			// Yearly == 0 means keep all yearly backups (no limit)
			keepThisYear := !yearsSeen[yearKey] && (config.Yearly == 0 || len(yearsSeen) < config.Yearly)
			if keepThisYear {
				classification[b] = CategoryYearly
				yearsSeen[yearKey] = true
			}
		}
	}

	// 5. Mark remaining backups for deletion
	for _, b := range backups {
		if classification[b] == "" {
			classification[b] = CategoryDelete
		}
	}

	return classification
}

// GetRetentionStats returns statistics about classification results
func GetRetentionStats(classification map[*types.BackupMetadata]RetentionCategory) map[RetentionCategory]int {
	stats := make(map[RetentionCategory]int)
	for _, cat := range classification {
		stats[cat]++
	}
	return stats
}
