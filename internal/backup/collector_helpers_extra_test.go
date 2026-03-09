package backup

import (
	"strings"
	"testing"
)

func TestSummarizeCommandOutputText(t *testing.T) {
	if got := summarizeCommandOutputText(""); got != "(no stdout/stderr)" {
		t.Fatalf("expected empty output placeholder, got %s", got)
	}

	multi := "line1\nline2"
	if got := summarizeCommandOutputText(multi); got != "line1 | line2" {
		t.Fatalf("expected newline replacement, got %s", got)
	}

	long := make([]rune, 2050)
	for i := range long {
		long[i] = 'a'
	}
	s := summarizeCommandOutputText(string(long))
	runes := []rune(s)
	if len(runes) != 2049 || runes[len(runes)-1] != '…' {
		t.Fatalf("expected truncated output ending with ellipsis, got len=%d last=%q", len(runes), runes[len(runes)-1])
	}
}

func TestSanitizeFilenameExtra(t *testing.T) {
	cases := map[string]string{
		"abc/def":      "abc_def",
		"my@host":      "my_host",
		"..\\etc":      "__etc",
		"":             "entry",
		"normal":       "normal",
		"odd:name.ext": "odd_name.ext",
	}
	for in, expected := range cases {
		if got := sanitizeFilename(in); got != expected {
			t.Fatalf("sanitizeFilename(%s)=%s want %s", in, got, expected)
		}
	}
}

func TestCollectorPathKey(t *testing.T) {
	if got := collectorPathKey("store1"); got != "store1" {
		t.Fatalf("collectorPathKey(store1)=%q want %q", got, "store1")
	}

	unsafe := "../evil"
	got := collectorPathKey(unsafe)
	if got == unsafe {
		t.Fatalf("collectorPathKey(%q) should not keep unsafe value", unsafe)
	}
	if got == sanitizeFilename(unsafe) {
		t.Fatalf("collectorPathKey(%q) should add a disambiguating suffix", unsafe)
	}
	if !strings.HasPrefix(got, "__evil") {
		t.Fatalf("collectorPathKey(%q)=%q should start with sanitized prefix", unsafe, got)
	}

	if a, b := collectorPathKey("a/b"), collectorPathKey("a_b"); a == b {
		t.Fatalf("collectorPathKey should avoid collisions: %q == %q", a, b)
	}
}
