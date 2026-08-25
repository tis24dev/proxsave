package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeListing(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func evidenceCollector(t *testing.T) *Collector {
	t.Helper()
	return &Collector{
		logger:  newTestLogger(),
		tempDir: t.TempDir(),
		config:  &CollectorConfig{BackupPBSNotifications: true, BackupPBSNotificationsPriv: true},
	}
}

// The test the design record named and nobody wrote. One readable-and-empty smtp listing
// used to license an affirmative claim about gotify and webhook, whose listings were never
// captured at all.
func TestWritePBSNotificationSummary_PrivWarnsWhenEvidenceIncomplete(t *testing.T) {
	c := evidenceCollector(t)
	c.pbsManifest = map[string]ManifestEntry{
		"notifications.cfg":      {Status: StatusCollected},
		"notifications-priv.cfg": {Status: StatusSkipped},
	}

	dir := t.TempDir()
	// smtp readable and empty. gotify, webhook and targets deliberately absent.
	writeListing(t, dir, "notification_endpoints_smtp.json", `{"data":[]}`)

	s := pbsNotificationsSummary{Enabled: true, Endpoints: map[string]pbsNotificationSnapshotSummary{}}
	s.ConfigFiles = &pbsNotificationsConfigFilesSummary{
		NotificationsCfg:     c.pbsManifest["notifications.cfg"],
		NotificationsPrivCfg: c.pbsManifest["notifications-priv.cfg"],
	}
	s.Targets = summarizePBSNotificationSnapshot(filepath.Join(dir, "notification_targets.json"))
	for _, typ := range pbsNotificationEndpointTypes {
		s.Endpoints[typ] = summarizePBSNotificationSnapshot(filepath.Join(dir, "notification_endpoints_"+typ+".json"))
	}

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 1 {
		t.Fatalf("incomplete evidence must warn, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
	if !strings.Contains(s.Warnings[0], "gotify or webhook") {
		t.Fatalf("the warning must name the kinds nothing accounts for, got: %s", s.Warnings[0])
	}
	// Load-bearing: asserting the ABSENCE of the affirmative clause makes this unfoolable
	// by rewording.
	for _, line := range append(append([]string{}, s.Warnings...), s.Notes...) {
		if strings.Contains(line, "so no secret was lost") {
			t.Fatalf("nothing may claim no secret was lost here, got: %s", line)
		}
	}
}

// The targets listing is the union of the four endpoint kinds and carries each entry's
// type, so one readable targets listing accounts for kinds whose own listing is missing.
// This is what makes PBS 3.2, which has no webhook list command at all, reach a complete
// answer without reading a version string.
func TestPBSSecretEvidence_TargetsUnionCoversUnreadKinds(t *testing.T) {
	c := evidenceCollector(t)
	dir := t.TempDir()
	writeListing(t, dir, "notification_targets.json",
		`{"data":[{"name":"mail-to-root","origin":"builtin","type":"sendmail"}]}`)

	s := summaryWith(StatusCollected, StatusSkipped)
	s.Enabled = true
	s.Targets = summarizePBSNotificationSnapshot(filepath.Join(dir, "notification_targets.json"))

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 0 {
		t.Fatalf("the targets union accounts for every kind, so nothing is unknown: %v", s.Warnings)
	}
	if len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "verified from the notification target listing") {
		t.Fatalf("the note must name the source of the claim, got: %v", s.Notes)
	}
}

// An unknown type in the targets listing may itself be secret-capable — webhook was added
// in PBS 3.3 — so it must veto the union rather than be silently excluded from a count we
// are about to call complete.
func TestPBSSecretEvidence_UnknownTargetTypeVetoesTheUnion(t *testing.T) {
	c := evidenceCollector(t)
	dir := t.TempDir()
	writeListing(t, dir, "notification_targets.json",
		`{"data":[{"name":"x","origin":"user-created","type":"ntfy"}]}`)

	s := summaryWith(StatusCollected, StatusSkipped)
	s.Enabled = true
	s.Targets = summarizePBSNotificationSnapshot(filepath.Join(dir, "notification_targets.json"))

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 1 {
		t.Fatalf("an unknown kind must not be counted as covered, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
}

// The union supplements the per-kind listings and must never lower an observation.
func TestPBSSecretEvidence_TargetsNeverLowerAnObservation(t *testing.T) {
	c := evidenceCollector(t)
	s := summaryWith(StatusCollected, StatusSkipped)
	s.Enabled = true
	s.Targets = pbsNotificationSnapshotSummary{Present: true, Total: 1, ByType: map[string]int{"sendmail": 1}}
	s.Endpoints = map[string]pbsNotificationSnapshotSummary{
		"smtp": {Present: true, Total: 2},
	}

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "2 secret-capable") {
		t.Fatalf("the per-kind observation must survive the union, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
}

// A zero-byte listing is a truncated or redirected-to-nothing file, never proof that PBS
// has no objects of that kind. proxmox-router prints "[]" for a genuinely empty list.
func TestSummarizePBSNotificationSnapshot_EmptyBodyIsUnusable(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name       string
		body       string
		wantUsable bool
	}{
		{"zero bytes", "", false},
		{"whitespace only", "   \n\t\n", false},
		{"empty json list", "[]", true},
		{"empty data envelope", `{"data":[]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tt.name, " ", "_")+".json")
			writeListing(t, dir, filepath.Base(path), tt.body)
			got := summarizePBSNotificationSnapshot(path)
			if got.usable() != tt.wantUsable {
				t.Fatalf("usable()=%v want %v (%+v)", got.usable(), tt.wantUsable, got)
			}
			if !tt.wantUsable && got.Error == "" {
				t.Fatal("an unusable listing must carry the reason")
			}
		})
	}
}

// An incomplete survey can only undercount, so the number must be presented as a floor.
func TestPBSNotificationPrivFindings_IncompleteSurveyReportsAFloor(t *testing.T) {
	c := evidenceCollector(t)
	s := summaryWith(StatusCollected, StatusSkipped)
	s.Enabled = true
	s.Endpoints = map[string]pbsNotificationSnapshotSummary{
		"smtp": {Present: true, Total: 2},
	}

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "at least 2") {
		t.Fatalf("an incomplete survey must say the count is a floor, got: %v", s.Warnings)
	}
}

// A dry run attempts no listing command, so every listing is absent by construction. A
// warning about secret loss there is exit-code-bearing noise about a run in which nothing
// was tried.
func TestPBSNotificationPrivFindings_DryRunSaysNothing(t *testing.T) {
	c := evidenceCollector(t)
	c.dryRun = true
	s := summaryWith(StatusCollected, StatusSkipped)
	s.Enabled = true

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 0 || len(s.Notes) != 0 {
		t.Fatalf("a dry run must say nothing, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
}

// An empty Status means the manifest brick never ran, not that collection was skipped.
// Falling through emits a finding whose premise is fabricated.
func TestPBSNotificationPrivFindings_EmptyStatusSaysNothing(t *testing.T) {
	c := evidenceCollector(t)
	s := summaryWith(StatusCollected, "")
	s.Enabled = true
	s.Endpoints = map[string]pbsNotificationSnapshotSummary{"smtp": {Present: true, Total: 0}}

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 0 || len(s.Notes) != 0 {
		t.Fatalf("a missing manifest entry is not a collection status, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
}

// The PBS 3.2 remark explains webhook and nothing else. Appending it to the other five
// listings attributed a webhook-specific cause to failures it cannot explain.
func TestPBSNotificationEvidenceNotes_PBS32RemarkOnlyOnWebhook(t *testing.T) {
	c := evidenceCollector(t)
	s := pbsNotificationsSummary{Enabled: true, Endpoints: map[string]pbsNotificationSnapshotSummary{}}

	c.appendPBSNotificationEvidenceNotes(&s)

	var withRemark []string
	for _, n := range s.Notes {
		if strings.Contains(n, "webhook list command") {
			withRemark = append(withRemark, n)
		}
	}
	if len(withRemark) != 1 {
		t.Fatalf("exactly one note may carry the PBS 3.2 remark, got %d: %v", len(withRemark), s.Notes)
	}
	if !strings.Contains(withRemark[0], "webhook endpoint listing") {
		t.Fatalf("the remark must sit on the webhook note, got: %s", withRemark[0])
	}
}

// Mirror of the origin invariant: no entry may fall out of the type accounting either.
func TestSummarizePBSNotificationSnapshot_TypeBucketsSumToTotal(t *testing.T) {
	dir := t.TempDir()
	writeListing(t, dir, "mixed.json",
		`{"data":[{"name":"a","type":"smtp"},{"name":"b"},{"name":"c","type":"ntfy"},"not-an-object",42]}`)

	got := summarizePBSNotificationSnapshot(filepath.Join(dir, "mixed.json"))

	sum := got.TypeMissing
	for _, n := range got.ByType {
		sum += n
	}
	if sum != got.Total {
		t.Fatalf("type buckets sum to %d but Total is %d (%+v)", sum, got.Total, got)
	}
}

// PBS writes a private section whenever a secret-capable endpoint is created, so endpoints
// without the file cannot happen on a healthy host: it means a wrong lookup path or real
// secret loss. The notifications.cfg branch already treats its equivalent as a Warning, and
// a Note only reaches Info level and never enters the run issue summary.
func TestPBSNotificationPrivFindings_NotFoundWithEndpointsWarns(t *testing.T) {
	c := evidenceCollector(t)
	s := summaryWith(StatusCollected, StatusNotFound)
	s.Enabled = true
	s.Endpoints = map[string]pbsNotificationSnapshotSummary{
		"smtp":    {Present: true, Total: 1},
		"gotify":  {Present: true, Total: 0},
		"webhook": {Present: true, Total: 0},
	}

	c.appendPBSNotificationPrivFindings(&s)

	if len(s.Warnings) != 1 {
		t.Fatalf("a missing priv file with live endpoints must warn, got warnings=%v notes=%v", s.Warnings, s.Notes)
	}
	if len(s.Notes) != 0 {
		t.Fatalf("the finding must not also be filed as a note, got: %v", s.Notes)
	}
}
