package backup

import "testing"

func TestShouldRunHostCommandsUnderPrefix(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		want   bool
	}{
		{"real root empty", "", true},
		{"real root slash", "/", true},
		{"host prefix", "/host", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Collector{config: &CollectorConfig{SystemRootPrefix: tc.prefix}}
			if got := c.shouldRunHostCommands(); got != tc.want {
				t.Fatalf("shouldRunHostCommands(%q) = %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

// TestShouldRunKernelSharedCommands: ZFS is global kernel state, so it runs on a
// real root always, and under a prefix only when HOST_BACKUP_MODE is set. The
// namespace-scoped gate (shouldRunHostCommands) stays off under any prefix.
func TestShouldRunKernelSharedCommands(t *testing.T) {
	cases := []struct {
		name       string
		prefix     string
		hostBackup bool
		want       bool
	}{
		{"real root", "", false, true},
		{"prefix without flag", "/host", false, false},
		{"prefix with flag", "/host", true, true},
		{"flag without prefix", "", true, true}, // real root already returns true
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Collector{config: &CollectorConfig{SystemRootPrefix: tc.prefix, HostBackupMode: tc.hostBackup}}
			if got := c.shouldRunKernelSharedCommands(); got != tc.want {
				t.Fatalf("shouldRunKernelSharedCommands = %v, want %v", got, tc.want)
			}
			// The namespace-scoped gate must never be flipped on by the flag.
			if tc.prefix != "" && tc.prefix != "/" && c.shouldRunHostCommands() {
				t.Fatal("shouldRunHostCommands must stay false under a prefix")
			}
		})
	}
}
