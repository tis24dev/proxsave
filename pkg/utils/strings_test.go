package utils

import "testing"

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{"bytes", 512, "512 B"},
		{"kilobytes", 2048, "2.0 KB"},
		{"megabytes", 5242880, "5.0 MB"},
		{"gigabytes", 1234567890, "1.1 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatBytes(tt.input)
			if result != tt.expected {
				t.Errorf("FormatBytes(%d) = %s; want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"true", "true", true},
		{"1", "1", true},
		{"yes", "yes", true},
		{"on", "on", true},
		{"enabled", "enabled", true},
		{"TRUE", "TRUE", true},
		{"false", "false", false},
		{"0", "0", false},
		{"no", "no", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ParseBool(tt.input)
			if result != tt.expected {
				t.Errorf("ParseBool(%q) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestTrimQuotes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"double quotes", `"hello"`, "hello"},
		{"single quotes", `'world'`, "world"},
		{"no quotes", "test", "test"},
		{"mixed quotes", `"test'`, `"test'`},
		{"empty", "", ""},
		{"with spaces", `  "value"  `, "value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TrimQuotes(tt.input)
			if result != tt.expected {
				t.Errorf("TrimQuotes(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSplitKeyValue(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		expectedKey   string
		expectedValue string
		expectedOK    bool
	}{
		{"valid", "KEY=value", "KEY", "value", true},
		{"with quotes", `KEY="value"`, "KEY", "value", true},
		{"with spaces", "  KEY  =  value  ", "KEY", "value", true},
		{"no equals", "INVALID", "", "", false},
		{"multiple equals", "KEY=value=123", "KEY", "value=123", true},
		{"empty value", "KEY=", "KEY", "", true},
		{"inline comment", "KEY=value # comment", "KEY", "value", true},
		{"quoted hash", `KEY="value # keep"`, "KEY", "value # keep", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, value, ok := SplitKeyValue(tt.input)
			if ok != tt.expectedOK {
				t.Errorf("SplitKeyValue(%q) ok = %v; want %v", tt.input, ok, tt.expectedOK)
			}
			if ok {
				if key != tt.expectedKey {
					t.Errorf("SplitKeyValue(%q) key = %q; want %q", tt.input, key, tt.expectedKey)
				}
				if value != tt.expectedValue {
					t.Errorf("SplitKeyValue(%q) value = %q; want %q", tt.input, value, tt.expectedValue)
				}
			}
		})
	}
}

func TestIsComment(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{"comment", "# This is a comment", true},
		{"comment with spaces", "  # Comment", true},
		{"empty line", "", true},
		{"spaces only", "   ", true},
		{"not comment", "KEY=value", false},
		{"hash in middle", "KEY=#value", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsComment(tt.input)
			if result != tt.expected {
				t.Errorf("IsComment(%q) = %v; want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateRandomString(t *testing.T) {
	s := GenerateRandomString(16)
	if len(s) != 16 {
		t.Fatalf("GenerateRandomString length = %d; want 16", len(s))
	}
	if s == "" {
		t.Fatal("GenerateRandomString returned empty string")
	}
}
