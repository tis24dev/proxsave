package install

import (
	"testing"

	cronutil "github.com/tis24dev/proxsave/internal/cron"
)

// TestCronFieldDefault pins the TUI half of F10-02: the wizard "Run at" field is
// seeded from the stored SCHEDULER_TIME on an Edit, so a no-op edit keeps the
// operator's time instead of resetting it to 02:00. Empty or invalid stored times
// fall back to DefaultTime.
func TestCronFieldDefault(t *testing.T) {
	if got := cronFieldDefault(""); got != cronutil.DefaultTime {
		t.Fatalf("empty stored time = %q, want %q", got, cronutil.DefaultTime)
	}
	if got := cronFieldDefault("07:30"); got != "07:30" {
		t.Fatalf("stored time = %q, want %q", got, "07:30")
	}
	if got := cronFieldDefault(" 7:5 "); got != "07:05" {
		t.Fatalf("normalized stored time = %q, want %q", got, "07:05")
	}
	if got := cronFieldDefault("99:99"); got != cronutil.DefaultTime {
		t.Fatalf("invalid stored time = %q, want %q", got, cronutil.DefaultTime)
	}
}
