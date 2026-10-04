package notify

import (
	"strings"
	"testing"
)

// The System Recommendations section lists, in this order, Local, Secondary and PBS
// above 85%; PBS only when it is on.
func TestEmailHTMLRecommendationsIncludePBS(t *testing.T) {
	line := func(name string, percent string) string {
		return "<p>⚠️ <strong>" + name + " storage is " + percent + " full.</strong> Consider cleaning old backups or expanding storage capacity.</p>"
	}
	data := &NotificationData{
		LocalUsagePercent:     90.1,
		SecondaryEnabled:      true,
		SecondaryUsagePercent: 91.2,
		PBSEnabled:            true,
		PBSUsagePercent:       92.3,
	}
	html := BuildEmailHTML(data)
	local, secondary, pbs := strings.Index(html, line("Local", "90.1%")), strings.Index(html, line("Secondary", "91.2%")), strings.Index(html, line("PBS", "92.3%"))
	if local < 0 || secondary < local || pbs < secondary {
		t.Fatalf("recommendation lines at %d/%d/%d, want Local, Secondary, PBS in this order:\n%s", local, secondary, pbs, html)
	}

	only := &NotificationData{PBSEnabled: true, PBSUsagePercent: 85.1}
	if html := BuildEmailHTML(only); !strings.Contains(html, "<h2>System Recommendations</h2>") || !strings.Contains(html, line("PBS", "85.1%")) {
		t.Fatalf("PBS alone above 85%% opens the section:\n%s", html)
	}
	for _, d := range []*NotificationData{
		{PBSEnabled: true, PBSUsagePercent: 85},
		{PBSEnabled: false, PBSUsagePercent: 99},
	} {
		if html := BuildEmailHTML(d); strings.Contains(html, "System Recommendations") {
			t.Fatalf("enabled=%v percent=%v: no section expected:\n%s", d.PBSEnabled, d.PBSUsagePercent, html)
		}
	}
}
