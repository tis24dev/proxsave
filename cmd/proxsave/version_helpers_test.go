package main

import "testing"

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{"equal", "0.11.2", "0.11.2", 0},
		{"patch older", "0.11.2", "0.11.3", -1},
		{"patch newer", "0.11.10", "0.11.2", 1},
		{"minor newer", "0.10.9", "0.11.0", -1},
		{"major newer", "1.0.0", "0.99.0", 1},
		{"ignore suffix", "1.2.3-rc1", "1.2.3", 0},
		{"different length", "1.2", "1.2.1", -1},
		{"empty current", "", "1", -1},
		{"empty latest", "1", "", 1},
		{"trim spaces", " 1.2.3 ", "1.2.4", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compareVersions(tt.current, tt.latest); got != tt.want {
				t.Fatalf("compareVersions(%q,%q) = %d, want %d", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestIsNewerVersion(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{"same", "0.1.0", "0.1.0", false},
		{"patch newer", "0.1.0", "0.1.1", true},
		{"minor newer", "0.1.9", "0.2.0", true},
		{"major newer", "1.9.9", "2.0.0", true},
		{"strip leading v", "v1.2.3", "1.2.4", true},
		{"ignore prerelease", "1.2.3-rc1", "1.2.3", false},
		{"missing patch treated as 0", "1.2", "1.2.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNewerVersion(tt.current, tt.latest); got != tt.want {
				t.Fatalf("isNewerVersion(%q,%q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}
