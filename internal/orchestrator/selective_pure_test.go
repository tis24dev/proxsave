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
