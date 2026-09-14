package notify

import (
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// NOTIFY_ON is a severity THRESHOLD, and the whole feature is one boolean that decides
// whether an operator hears about a run at all. The row that matters most is the one a
// reader guesses wrong: "warning" is not warnings-only, it is warnings AND failures. Get
// that backwards and the setting most people will reach for is the one that hides the
// failures they turned it on for. The matrix is written out in full, every policy against
// every outcome, because a switch that returns the right answer for two thirds of the
// cells is exactly what a hand-picked sample would pass.
func TestNotifyOnAllowsIsASeverityThreshold(t *testing.T) {
	cases := []struct {
		policy string
		status NotificationStatus
		want   bool
	}{
		// always: the default, and the behaviour of every install that predates the key.
		{config.NotifyOnAlways, StatusSuccess, true},
		{config.NotifyOnAlways, StatusWarning, true},
		{config.NotifyOnAlways, StatusFailure, true},

		// warning: and worse. A failure is worse than a warning, so it passes too.
		{config.NotifyOnWarning, StatusSuccess, false},
		{config.NotifyOnWarning, StatusWarning, true},
		{config.NotifyOnWarning, StatusFailure, true},

		// failure: the only value that is an exact match, because nothing is worse.
		{config.NotifyOnFailure, StatusSuccess, false},
		{config.NotifyOnFailure, StatusWarning, false},
		{config.NotifyOnFailure, StatusFailure, true},
	}

	for _, tc := range cases {
		if got := NotifyOnAllows(tc.policy, tc.status); got != tc.want {
			t.Errorf("NotifyOnAllows(%q, %s) = %v; want %v", tc.policy, tc.status, got, tc.want)
		}
	}
}

// Every path that cannot state a policy has to deliver, because silence is the outcome
// nobody can detect. The empty string is not hypothetical: a bare &config.Config{} literal
// carries it and never runs the parser, which is what dozens of in-tree tests and any
// future caller that builds a Config by hand will hand to this function. A mistyped value
// is the operator-facing half of the same rule -- config.NormalizeNotifyOn passes it
// through unchanged precisely so the dispatcher can name it in a warning, and until the
// operator fixes it the notifications keep arriving.
func TestAnUnusablePolicyDeliversEverything(t *testing.T) {
	for _, policy := range []string{"", "  ", "warnings-only", "WARNING", "never", "0", "true"} {
		for _, status := range []NotificationStatus{StatusSuccess, StatusWarning, StatusFailure} {
			if !NotifyOnAllows(policy, status) {
				t.Errorf("NotifyOnAllows(%q, %s) = false; an unusable policy must never silence a run", policy, status)
			}
		}
	}
}

// The canonical values this package branches on are config's, not copies of them. A
// second set of string literals here would keep passing while the two drifted apart, and
// the symptom of that drift is a config the loader accepts and the dispatcher ignores.
func TestTheThresholdBranchesOnTheCanonicalValues(t *testing.T) {
	for _, policy := range []string{config.NotifyOnWarning, config.NotifyOnFailure} {
		if NotifyOnAllows(policy, StatusSuccess) {
			t.Fatalf("NotifyOnAllows(%q, success) = true; %q must suppress a clean run", policy, policy)
		}
	}
	if !config.IsValidNotifyOn(config.NotifyOnAlways) {
		t.Fatal("config.NotifyOnAlways must be a valid NOTIFY_ON value")
	}
}
