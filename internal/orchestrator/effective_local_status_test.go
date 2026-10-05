package orchestrator

import "testing"

// The Local status the notifications and the dashboard show: the one step [6] wrote,
// or, for a run that never reached it, the one the exit code gives.
func TestEffectiveLocalStatus(t *testing.T) {
	for _, tc := range []struct {
		local string
		exit  int
		want  string
	}{
		{"ok", 4, "ok"},
		{"warning", 0, "warning"},
		{" error ", 0, "error"},
		{"", 0, "ok"},
		{"", 1, "warning"},
		{"", 4, "error"},
		{"", 130, "error"},
	} {
		if got := EffectiveLocalStatus(&BackupStats{LocalStatus: tc.local, ExitCode: tc.exit}); got != tc.want {
			t.Errorf("LocalStatus=%q exit=%d: %q, want %q", tc.local, tc.exit, got, tc.want)
		}
	}
	if got := EffectiveLocalStatus(nil); got != "" {
		t.Errorf("nil stats: %q, want empty", got)
	}
}
