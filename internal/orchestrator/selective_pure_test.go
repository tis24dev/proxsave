package orchestrator

import "testing"

func TestAnalyzeArchivePaths(t *testing.T) {
	categories := []Category{
		{ID: "pve_cluster", Name: "Cluster", Paths: []string{"./etc/pve/"}},
		{ID: "network", Name: "Network", Paths: []string{"./etc/network/interfaces"}},
		{ID: "logs", Name: "Logs", Paths: []string{"./var/log/"}},
	}

	paths := []string{
		"./etc/pve/storage.cfg",
		"./etc/network/interfaces",
		"./random/file",
	}

	available := AnalyzeArchivePaths(paths, categories)

	if len(available) != 2 {
		t.Fatalf("expected 2 categories available, got %d", len(available))
	}

	if !hasCategoryID(available, "pve_cluster") {
		t.Fatalf("pve_cluster should be detected as available")
	}
	if !hasCategoryID(available, "network") {
		t.Fatalf("network should be detected as available")
	}
	if hasCategoryID(available, "logs") {
		t.Fatalf("logs should not be marked available")
	}
}

func TestPathMatchesPatternVariants(t *testing.T) {
	cases := []struct {
		path     string
		pattern  string
		expected bool
	}{
		{"etc/pve/storage.cfg", "./etc/pve/", true},
		{"./etc/network/interfaces", "./etc/network/interfaces", true},
		{"./etc/network/interfaces.d/foo", "./etc/network/interfaces", false},
		{"./var/log/syslog", "./etc/network/", false},
	}

	for _, tc := range cases {
		if got := pathMatchesPattern(tc.path, tc.pattern); got != tc.expected {
			t.Fatalf("pathMatchesPattern(%q,%q)=%v want %v", tc.path, tc.pattern, got, tc.expected)
		}
	}
}

func TestAnalyzeArchivePathsDetectsNetworkHostname(t *testing.T) {
	all := GetAllCategories()
	paths := []string{
		"./etc/hostname",
		"./var/lib/ignore/me",
	}

	available := AnalyzeArchivePaths(paths, all)
	if !hasCategoryID(available, "network") {
		t.Fatalf("network category should be available when hostname is present")
	}
}
