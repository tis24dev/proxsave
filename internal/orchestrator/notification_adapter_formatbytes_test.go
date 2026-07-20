package orchestrator

import (
	"math"
	"testing"
)

// P-07: formatBytesHR indexed a 5-entry units slice with an exponent that reaches 5 for
// values >= 1 EiB (uint64 goes up to ~16 EiB), panicking with index-out-of-range. The
// panic loses the notification, exits non-zero, and skips the finalization phase.
func TestFormatBytesHR_LargeValuesDoNotPanic(t *testing.T) {
	if got := formatBytesHR(1 << 60); got != "1.00 EB" {
		t.Fatalf("1 EiB: got %q want %q", got, "1.00 EB")
	}
	if got := formatBytesHR(math.MaxUint64); got == "" {
		t.Fatal("MaxUint64 returned empty")
	} else if got[len(got)-2:] != "EB" {
		t.Fatalf("MaxUint64: got %q, want a value ending in EB", got)
	}
	// Common-range guards: the fix must not shift the existing units.
	if got := formatBytesHR(2048); got != "2.00 KB" {
		t.Fatalf("2 KiB: got %q want %q", got, "2.00 KB")
	}
	if got := formatBytesHR(1 << 50); got != "1.00 PB" {
		t.Fatalf("1 PiB: got %q want %q", got, "1.00 PB")
	}
}
