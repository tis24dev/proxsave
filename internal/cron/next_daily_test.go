package cron

import (
	"testing"
	"time"
)

func TestNextDaily(t *testing.T) {
	loc := time.UTC
	tests := []struct {
		name string
		now  time.Time
		hhmm string
		want time.Time
	}{
		{
			name: "later today",
			now:  time.Date(2026, 7, 4, 1, 0, 0, 0, loc),
			hhmm: "02:00",
			want: time.Date(2026, 7, 4, 2, 0, 0, 0, loc),
		},
		{
			name: "already passed -> tomorrow",
			now:  time.Date(2026, 7, 4, 3, 0, 0, 0, loc),
			hhmm: "02:00",
			want: time.Date(2026, 7, 5, 2, 0, 0, 0, loc),
		},
		{
			name: "exactly now -> tomorrow (strictly after)",
			now:  time.Date(2026, 7, 4, 2, 0, 0, 0, loc),
			hhmm: "02:00",
			want: time.Date(2026, 7, 5, 2, 0, 0, 0, loc),
		},
		{
			name: "month rollover",
			now:  time.Date(2026, 7, 31, 23, 30, 0, 0, loc),
			hhmm: "01:15",
			want: time.Date(2026, 8, 1, 1, 15, 0, 0, loc),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextDaily(tc.now, tc.hhmm)
			if err != nil {
				t.Fatalf("NextDaily: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("NextDaily(%s, %q) = %s, want %s", tc.now, tc.hhmm, got, tc.want)
			}
		})
	}
}

func TestNextDailyInvalid(t *testing.T) {
	for _, bad := range []string{"25:00", "02:99", "abc", "", "2"} {
		if _, err := NextDaily(time.Now(), bad); err == nil {
			t.Errorf("NextDaily(%q) expected error, got nil", bad)
		}
	}
}
