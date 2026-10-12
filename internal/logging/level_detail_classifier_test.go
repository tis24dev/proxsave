package logging

import "testing"

func TestRestatementKindStructuredErrorDetail(t *testing.T) {
	for _, level := range []string{"INFO", "DEBUG"} {
		if kind, word := restatementKind(level, "    Error: %s"); kind != "" || word != "" {
			t.Errorf("%s structured detail classified as (%q, %q)", level, kind, word)
		}
	}
}

func TestRestatementKindRetainsSeverityViolations(t *testing.T) {
	cases := []struct{ level, literal, word string }{
		{"INFO", "Error: %s", "ERROR"},
		{"DEBUG", "    ERROR: %s", "ERROR"},
		{"INFO", "    error: %s", "ERROR"},
		{"DEBUG", "  Error: %s", "ERROR"},
		{"INFO", "\tError: %s", "ERROR"},
		{"DEBUG", "    Error: %v", "ERROR"},
		{"INFO", "    Error: %s extra", "ERROR"},
		{"DEBUG", "    Error: permission denied", "ERROR"},
		{"ERROR", "    Error: %s", "ERROR"},
		{"WARNING", "    Error: %s", "ERROR"},
		{"CRITICAL", "    Error: %s", "ERROR"},
		{"DEBUG", "    Warning: %s", "WARNING"},
		{"INFO", "    INFO: %s", "INFO"},
		{"DEBUG", "    DEBUG: %s", "DEBUG"},
		{"INFO", "detail [ERROR] %s", "ERROR"},
		{"DEBUG", "    Error: %s [WARNING]", "ERROR"},
	}
	for _, tc := range cases {
		t.Run(tc.level+"/"+tc.literal, func(t *testing.T) {
			if kind, word := restatementKind(tc.level, tc.literal); kind != "word" || word != tc.word {
				t.Errorf("got (%q, %q), want (word, %s)", kind, word, tc.word)
			}
		})
	}
}
