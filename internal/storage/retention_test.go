package storage

import (
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestNewRetentionConfigFromConfig tests the auto-detection of retention policy
func TestNewRetentionConfigFromConfig(t *testing.T) {
	tests := []struct {
		name           string
		cfg            *config.Config
		location       BackupLocation
		expectedPolicy string
		expectedMax    int
	}{
		{
			name: "GFS policy - daily set",
			cfg: &config.Config{
				RetentionPolicy:  "gfs",
				RetentionDaily:   7,
				RetentionWeekly:  0,
				RetentionMonthly: 0,
				RetentionYearly:  0,
			},
			location:       LocationPrimary,
			expectedPolicy: "gfs",
			expectedMax:    0,
		},
		{
			name: "GFS policy - weekly set",
			cfg: &config.Config{
				RetentionPolicy:  "gfs",
				RetentionDaily:   0,
				RetentionWeekly:  4,
				RetentionMonthly: 0,
				RetentionYearly:  0,
			},
			location:       LocationPrimary,
			expectedPolicy: "gfs",
			expectedMax:    0,
		},
		{
			name: "GFS policy - all parameters set",
			cfg: &config.Config{
				RetentionPolicy:  "gfs",
				RetentionDaily:   7,
				RetentionWeekly:  4,
				RetentionMonthly: 6,
				RetentionYearly:  3,
			},
			location:       LocationPrimary,
			expectedPolicy: "gfs",
			expectedMax:    0,
		},
		{
			name: "Simple policy - primary location",
			cfg: &config.Config{
				LocalRetentionDays: 14,
			},
			location:       LocationPrimary,
			expectedPolicy: "simple",
			expectedMax:    14,
		},
		{
			name: "Simple policy - secondary location",
			cfg: &config.Config{
				SecondaryRetentionDays: 30,
			},
			location:       LocationSecondary,
			expectedPolicy: "simple",
			expectedMax:    30,
		},
		{
			name: "Simple policy - cloud location",
			cfg: &config.Config{
				CloudRetentionDays: 90,
			},
			location:       LocationCloud,
			expectedPolicy: "simple",
			expectedMax:    90,
		},
		{
			name:           "Simple policy - default fallback",
			cfg:            &config.Config{},
			location:       BackupLocation("unknown"),
			expectedPolicy: "simple",
			expectedMax:    7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := NewRetentionConfigFromConfig(tt.cfg, tt.location)

			if rc.Policy != tt.expectedPolicy {
				t.Errorf("Policy = %v, want %v", rc.Policy, tt.expectedPolicy)
			}

			if rc.MaxBackups != tt.expectedMax {
				t.Errorf("MaxBackups = %v, want %v", rc.MaxBackups, tt.expectedMax)
			}

			// Verify GFS parameters are copied
			if tt.cfg.RetentionDaily != 0 || tt.cfg.RetentionWeekly != 0 || tt.cfg.RetentionMonthly != 0 || tt.cfg.RetentionYearly != 0 {
				if rc.Daily != tt.cfg.RetentionDaily {
					t.Errorf("Daily = %v, want %v", rc.Daily, tt.cfg.RetentionDaily)
				}
				if rc.Weekly != tt.cfg.RetentionWeekly {
					t.Errorf("Weekly = %v, want %v", rc.Weekly, tt.cfg.RetentionWeekly)
				}
				if rc.Monthly != tt.cfg.RetentionMonthly {
					t.Errorf("Monthly = %v, want %v", rc.Monthly, tt.cfg.RetentionMonthly)
				}
				if rc.Yearly != tt.cfg.RetentionYearly {
					t.Errorf("Yearly = %v, want %v", rc.Yearly, tt.cfg.RetentionYearly)
				}
			}
		})
	}
}

// TestClassifyBackupsGFS_EmptyList tests GFS classification with empty backup list
func TestClassifyBackupsGFS_EmptyList(t *testing.T) {
	config := RetentionConfig{
		Daily:   7,
		Weekly:  4,
		Monthly: 6,
		Yearly:  3,
	}

	result := ClassifyBackupsGFS([]*types.BackupMetadata{}, config)

	if len(result) != 0 {
		t.Errorf("Expected empty result for empty input, got %d entries", len(result))
	}
}

// TestClassifyBackupsGFS_DailyOnly tests keeping only daily backups
func TestClassifyBackupsGFS_DailyOnly(t *testing.T) {
	now := time.Now()
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-24 * time.Hour)},  // 1 day ago
		{Timestamp: now.Add(-48 * time.Hour)},  // 2 days ago
		{Timestamp: now.Add(-72 * time.Hour)},  // 3 days ago
		{Timestamp: now.Add(-96 * time.Hour)},  // 4 days ago
		{Timestamp: now.Add(-120 * time.Hour)}, // 5 days ago
		{Timestamp: now.Add(-144 * time.Hour)}, // 6 days ago
		{Timestamp: now.Add(-168 * time.Hour)}, // 7 days ago
		{Timestamp: now.Add(-192 * time.Hour)}, // 8 days ago
		{Timestamp: now.Add(-216 * time.Hour)}, // 9 days ago
	}

	config := RetentionConfig{
		Daily:   7,
		Weekly:  0,
		Monthly: 0,
		Yearly:  -1, // Disable yearly retention to keep this test focused on the daily tier.
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 7 {
		t.Errorf("Expected 7 daily backups, got %d", stats[CategoryDaily])
	}

	if stats[CategoryDelete] != 2 {
		t.Errorf("Expected 2 backups marked for deletion, got %d", stats[CategoryDelete])
	}

	// Verify the newest 7 are kept
	for i := 0; i < 7; i++ {
		if classification[backups[i]] != CategoryDaily {
			t.Errorf("Backup %d should be daily, got %v", i, classification[backups[i]])
		}
	}

	// Verify the oldest 2 are deleted
	for i := 7; i < 9; i++ {
		if classification[backups[i]] != CategoryDelete {
			t.Errorf("Backup %d should be deleted, got %v", i, classification[backups[i]])
		}
	}
}

// TestClassifyBackupsGFS_ZeroDaily tests when daily retention is 0
func TestClassifyBackupsGFS_ZeroDaily(t *testing.T) {
	now := time.Now()
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-24 * time.Hour)},
		{Timestamp: now.Add(-48 * time.Hour)},
	}

	config := RetentionConfig{
		Daily:   0,
		Weekly:  1,
		Monthly: 0,
		Yearly:  0,
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	// With Daily=0, backups should go to weekly/monthly/yearly or delete
	if stats[CategoryDaily] != 0 {
		t.Errorf("Expected 0 daily backups with Daily=0, got %d", stats[CategoryDaily])
	}
}

// TestClassifyBackupsGFS_WeeklyRetention tests weekly backup retention
func TestClassifyBackupsGFS_WeeklyRetention(t *testing.T) {
	// Create backups spanning multiple weeks
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC) // Sunday
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},  // Yesterday (this week - should be daily)
		{Timestamp: now.Add(-8 * 24 * time.Hour)},  // Last week
		{Timestamp: now.Add(-15 * 24 * time.Hour)}, // 2 weeks ago
		{Timestamp: now.Add(-22 * 24 * time.Hour)}, // 3 weeks ago
		{Timestamp: now.Add(-29 * 24 * time.Hour)}, // 4 weeks ago
		{Timestamp: now.Add(-36 * 24 * time.Hour)}, // 5 weeks ago
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  3,
		Monthly: 0,
		Yearly:  -1, // -1 to disable yearly retention
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 1 {
		t.Errorf("Expected 1 daily backup, got %d", stats[CategoryDaily])
	}

	if stats[CategoryWeekly] != 3 {
		t.Errorf("Expected 3 weekly backups, got %d", stats[CategoryWeekly])
	}

	// Total should be 6, 1 daily + 3 weekly + X delete
	totalKept := stats[CategoryDaily] + stats[CategoryWeekly]
	expectedDelete := len(backups) - totalKept
	if stats[CategoryDelete] != expectedDelete {
		t.Errorf("Expected %d backups marked for deletion, got %d", expectedDelete, stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_MonthlyRetention tests monthly backup retention
func TestClassifyBackupsGFS_MonthlyRetention(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},   // This month (daily)
		{Timestamp: now.Add(-32 * 24 * time.Hour)},  // November 2024
		{Timestamp: now.Add(-63 * 24 * time.Hour)},  // October 2024
		{Timestamp: now.Add(-94 * 24 * time.Hour)},  // September 2024
		{Timestamp: now.Add(-125 * 24 * time.Hour)}, // August 2024
		{Timestamp: now.Add(-156 * 24 * time.Hour)}, // July 2024
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  0,
		Monthly: 3,
		Yearly:  -1, // -1 to disable yearly retention
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 1 {
		t.Errorf("Expected 1 daily backup, got %d", stats[CategoryDaily])
	}

	if stats[CategoryMonthly] != 3 {
		t.Errorf("Expected 3 monthly backups, got %d", stats[CategoryMonthly])
	}

	// Total should be 6, 1 daily + 3 monthly + X delete
	totalKept := stats[CategoryDaily] + stats[CategoryMonthly]
	expectedDelete := len(backups) - totalKept
	if stats[CategoryDelete] != expectedDelete {
		t.Errorf("Expected %d backups marked for deletion, got %d", expectedDelete, stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_YearlyRetention tests yearly backup retention
func TestClassifyBackupsGFS_YearlyRetention(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},    // 2024 (daily)
		{Timestamp: now.Add(-366 * 24 * time.Hour)},  // 2023
		{Timestamp: now.Add(-731 * 24 * time.Hour)},  // 2022
		{Timestamp: now.Add(-1096 * 24 * time.Hour)}, // 2021
		{Timestamp: now.Add(-1461 * 24 * time.Hour)}, // 2020
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  0,
		Monthly: 0,
		Yearly:  2,
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 1 {
		t.Errorf("Expected 1 daily backup, got %d", stats[CategoryDaily])
	}

	if stats[CategoryYearly] != 2 {
		t.Errorf("Expected 2 yearly backups, got %d", stats[CategoryYearly])
	}

	if stats[CategoryDelete] != 2 {
		t.Errorf("Expected 2 backups marked for deletion, got %d", stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_YearlyUnlimited tests unlimited yearly retention (Yearly=0)
func TestClassifyBackupsGFS_YearlyUnlimited(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},    // 2024 (daily)
		{Timestamp: now.Add(-366 * 24 * time.Hour)},  // 2023
		{Timestamp: now.Add(-731 * 24 * time.Hour)},  // 2022
		{Timestamp: now.Add(-1096 * 24 * time.Hour)}, // 2021
		{Timestamp: now.Add(-1461 * 24 * time.Hour)}, // 2020
		{Timestamp: now.Add(-1826 * 24 * time.Hour)}, // 2019
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  0,
		Monthly: 0,
		Yearly:  0, // 0 means unlimited yearly retention
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 1 {
		t.Errorf("Expected 1 daily backup, got %d", stats[CategoryDaily])
	}

	// With Yearly=0, all past year backups should be kept
	if stats[CategoryYearly] != 5 {
		t.Errorf("Expected 5 yearly backups with unlimited retention, got %d", stats[CategoryYearly])
	}

	if stats[CategoryDelete] != 0 {
		t.Errorf("Expected 0 backups marked for deletion with unlimited yearly, got %d", stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_CompleteGFS tests full GFS hierarchy
func TestClassifyBackupsGFS_CompleteGFS(t *testing.T) {
	// Reference date: 2024-12-15 (Week 50, Sunday)
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)

	// Create a comprehensive set of backups
	backups := []*types.BackupMetadata{
		// Daily (last 7 days)
		{Timestamp: now.Add(-1 * 24 * time.Hour)}, // Day 1
		{Timestamp: now.Add(-2 * 24 * time.Hour)}, // Day 2
		{Timestamp: now.Add(-3 * 24 * time.Hour)}, // Day 3
		{Timestamp: now.Add(-4 * 24 * time.Hour)}, // Day 4
		{Timestamp: now.Add(-5 * 24 * time.Hour)}, // Day 5
		{Timestamp: now.Add(-6 * 24 * time.Hour)}, // Day 6
		{Timestamp: now.Add(-7 * 24 * time.Hour)}, // Day 7

		// Older backups for weekly/monthly/yearly
		{Timestamp: now.Add(-14 * 24 * time.Hour)}, // 2 weeks ago
		{Timestamp: now.Add(-21 * 24 * time.Hour)}, // 3 weeks ago
		{Timestamp: now.Add(-28 * 24 * time.Hour)}, // 4 weeks ago
		{Timestamp: now.Add(-35 * 24 * time.Hour)}, // 5 weeks ago

		// Monthly
		{Timestamp: now.Add(-65 * 24 * time.Hour)},  // ~2 months ago
		{Timestamp: now.Add(-95 * 24 * time.Hour)},  // ~3 months ago
		{Timestamp: now.Add(-125 * 24 * time.Hour)}, // ~4 months ago

		// Yearly
		{Timestamp: now.Add(-400 * 24 * time.Hour)}, // ~1.1 years ago
		{Timestamp: now.Add(-800 * 24 * time.Hour)}, // ~2.2 years ago
	}

	config := RetentionConfig{
		Daily:   7,
		Weekly:  4,
		Monthly: 3,
		Yearly:  2,
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 7 {
		t.Errorf("Expected 7 daily backups, got %d", stats[CategoryDaily])
	}

	if stats[CategoryWeekly] < 1 {
		t.Errorf("Expected at least 1 weekly backup, got %d", stats[CategoryWeekly])
	}

	if stats[CategoryMonthly] < 1 {
		t.Errorf("Expected at least 1 monthly backup, got %d", stats[CategoryMonthly])
	}

	if stats[CategoryYearly] < 1 {
		t.Errorf("Expected at least 1 yearly backup, got %d", stats[CategoryYearly])
	}

	// Verify all backups are classified
	totalClassified := stats[CategoryDaily] + stats[CategoryWeekly] + stats[CategoryMonthly] + stats[CategoryYearly] + stats[CategoryDelete]
	if totalClassified != len(backups) {
		t.Errorf("Expected %d total classifications, got %d", len(backups), totalClassified)
	}
}

// TestClassifyBackupsGFS_NegativeDaily tests handling of negative daily value
func TestClassifyBackupsGFS_NegativeDaily(t *testing.T) {
	now := time.Now()
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-24 * time.Hour)},
		{Timestamp: now.Add(-48 * time.Hour)},
	}

	config := RetentionConfig{
		Daily:   -5, // Negative should be treated as 0
		Weekly:  0,
		Monthly: 0,
		Yearly:  -1, // Disable yearly retention so older-year backups aren't implicitly kept.
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 0 {
		t.Errorf("Expected 0 daily backups with negative daily config, got %d", stats[CategoryDaily])
	}

	if stats[CategoryDelete] != 2 {
		t.Errorf("Expected all backups marked for deletion, got %d", stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_CurrentWeekExclusion tests that current week backups aren't classified as weekly
func TestClassifyBackupsGFS_CurrentWeekExclusion(t *testing.T) {
	// Set a specific date for reproducibility
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC) // Sunday, Week 50

	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},  // Same week (Saturday)
		{Timestamp: now.Add(-8 * 24 * time.Hour)},  // Previous week
		{Timestamp: now.Add(-15 * 24 * time.Hour)}, // 2 weeks ago
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  2,
		Monthly: 0,
		Yearly:  0,
	}

	classification := ClassifyBackupsGFS(backups, config)

	// The backup from yesterday should be daily, not weekly
	if classification[backups[0]] != CategoryDaily {
		t.Errorf("Expected first backup (same week) to be daily, got %v", classification[backups[0]])
	}

	stats := GetRetentionStats(classification)

	if stats[CategoryWeekly] != 2 {
		t.Errorf("Expected 2 weekly backups from previous weeks, got %d", stats[CategoryWeekly])
	}
}

// TestClassifyBackupsGFS_CurrentMonthExclusion tests that current month backups aren't classified as monthly
func TestClassifyBackupsGFS_CurrentMonthExclusion(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)

	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-5 * 24 * time.Hour)},  // Same month (Dec 2024)
		{Timestamp: now.Add(-35 * 24 * time.Hour)}, // Previous month (Nov 2024)
		{Timestamp: now.Add(-65 * 24 * time.Hour)}, // 2 months ago (Oct 2024)
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  0,
		Monthly: 2,
		Yearly:  0,
	}

	classification := ClassifyBackupsGFS(backups, config)

	// The backup from this month should be daily, not monthly
	if classification[backups[0]] != CategoryDaily {
		t.Errorf("Expected first backup (same month) to be daily, got %v", classification[backups[0]])
	}

	stats := GetRetentionStats(classification)

	if stats[CategoryMonthly] != 2 {
		t.Errorf("Expected 2 monthly backups from previous months, got %d", stats[CategoryMonthly])
	}
}

// TestClassifyBackupsGFS_CurrentYearExclusion tests that current year backups aren't classified as yearly
func TestClassifyBackupsGFS_CurrentYearExclusion(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)

	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-100 * 24 * time.Hour)}, // Same year (2024)
		{Timestamp: now.Add(-400 * 24 * time.Hour)}, // Previous year (2023)
		{Timestamp: now.Add(-800 * 24 * time.Hour)}, // 2 years ago (2022)
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  0,
		Monthly: 0,
		Yearly:  2,
	}

	classification := ClassifyBackupsGFS(backups, config)

	// The backup from this year should be daily, not yearly
	if classification[backups[0]] != CategoryDaily {
		t.Errorf("Expected first backup (same year) to be daily, got %v", classification[backups[0]])
	}

	stats := GetRetentionStats(classification)

	if stats[CategoryYearly] != 2 {
		t.Errorf("Expected 2 yearly backups from previous years, got %d", stats[CategoryYearly])
	}
}

// TestClassifyBackupsGFS_OneBackupPerPeriod tests that only one backup per week/month/year is kept
func TestClassifyBackupsGFS_OneBackupPerPeriod(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)

	// Multiple backups in the same week
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-1 * 24 * time.Hour)},  // Daily
		{Timestamp: now.Add(-8 * 24 * time.Hour)},  // Last week - first
		{Timestamp: now.Add(-9 * 24 * time.Hour)},  // Last week - second (same week, should not be kept as weekly)
		{Timestamp: now.Add(-10 * 24 * time.Hour)}, // Last week - third
	}

	config := RetentionConfig{
		Daily:   1,
		Weekly:  1,
		Monthly: 0,
		Yearly:  -1, // Disable yearly retention
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 1 {
		t.Errorf("Expected 1 daily backup, got %d", stats[CategoryDaily])
	}

	// Only ONE backup from last week should be kept as weekly
	if stats[CategoryWeekly] != 1 {
		t.Errorf("Expected exactly 1 weekly backup, got %d", stats[CategoryWeekly])
	}

	// The other backups from last week should be deleted
	totalKept := stats[CategoryDaily] + stats[CategoryWeekly]
	expectedDelete := len(backups) - totalKept
	if stats[CategoryDelete] != expectedDelete {
		t.Errorf("Expected %d backups marked for deletion, got %d", expectedDelete, stats[CategoryDelete])
	}
}

// TestClassifyBackupsGFS_UnsortedInput tests that function handles unsorted input correctly
func TestClassifyBackupsGFS_UnsortedInput(t *testing.T) {
	now := time.Date(2024, 12, 15, 12, 0, 0, 0, time.UTC)

	// Create backups in random order
	backups := []*types.BackupMetadata{
		{Timestamp: now.Add(-120 * time.Hour)}, // 5 days ago
		{Timestamp: now.Add(-24 * time.Hour)},  // 1 day ago
		{Timestamp: now.Add(-96 * time.Hour)},  // 4 days ago
		{Timestamp: now.Add(-48 * time.Hour)},  // 2 days ago
		{Timestamp: now.Add(-72 * time.Hour)},  // 3 days ago
	}

	config := RetentionConfig{
		Daily:   3,
		Weekly:  0,
		Monthly: 0,
		Yearly:  -1, // Disable yearly retention
	}

	classification := ClassifyBackupsGFS(backups, config)

	stats := GetRetentionStats(classification)

	// Should keep exactly 3 backups regardless of input order
	totalKept := stats[CategoryDaily] + stats[CategoryWeekly] + stats[CategoryMonthly] + stats[CategoryYearly]
	if totalKept != 3 {
		t.Errorf("Expected 3 total backups kept, got %d (daily:%d, weekly:%d, monthly:%d, yearly:%d)",
			totalKept, stats[CategoryDaily], stats[CategoryWeekly], stats[CategoryMonthly], stats[CategoryYearly])
	}

	if stats[CategoryDelete] != 2 {
		t.Errorf("Expected 2 backups marked for deletion, got %d", stats[CategoryDelete])
	}

	// Verify the most recent backup is kept
	if classification[backups[1]] == CategoryDelete {
		t.Errorf("Expected most recent backup to be kept, got deleted")
	}
}

// TestGetRetentionStats tests the statistics function
func TestGetRetentionStats(t *testing.T) {
	backups := []*types.BackupMetadata{
		{Timestamp: time.Now()},
		{Timestamp: time.Now()},
		{Timestamp: time.Now()},
		{Timestamp: time.Now()},
		{Timestamp: time.Now()},
	}

	classification := map[*types.BackupMetadata]RetentionCategory{
		backups[0]: CategoryDaily,
		backups[1]: CategoryDaily,
		backups[2]: CategoryWeekly,
		backups[3]: CategoryMonthly,
		backups[4]: CategoryDelete,
	}

	stats := GetRetentionStats(classification)

	if stats[CategoryDaily] != 2 {
		t.Errorf("Expected 2 daily, got %d", stats[CategoryDaily])
	}
	if stats[CategoryWeekly] != 1 {
		t.Errorf("Expected 1 weekly, got %d", stats[CategoryWeekly])
	}
	if stats[CategoryMonthly] != 1 {
		t.Errorf("Expected 1 monthly, got %d", stats[CategoryMonthly])
	}
	if stats[CategoryYearly] != 0 {
		t.Errorf("Expected 0 yearly, got %d", stats[CategoryYearly])
	}
	if stats[CategoryDelete] != 1 {
		t.Errorf("Expected 1 delete, got %d", stats[CategoryDelete])
	}
}

// TestGetRetentionStats_EmptyClassification tests statistics with empty classification
func TestGetRetentionStats_EmptyClassification(t *testing.T) {
	classification := make(map[*types.BackupMetadata]RetentionCategory)

	stats := GetRetentionStats(classification)

	if len(stats) != 0 {
		t.Errorf("Expected empty stats, got %d entries", len(stats))
	}
}
