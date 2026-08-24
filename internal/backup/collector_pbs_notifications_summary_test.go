package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSnapshot drops a listing fixture on disk so the parser is exercised end to end.
// Hand-built pbsNotificationSnapshotSummary structs are deliberately avoided: the bug this
// file exists to prevent lived in the parser, and a struct literal cannot reach it.
func writeSnapshot(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "listing.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// The three origin values below are the complete vocabulary proxmox-notify can emit
// (proxmox.git, proxmox-notify/src/lib.rs:284-342). "custom" is not one of them.
func TestSummarizePBSNotificationSnapshot_OriginVocabulary(t *testing.T) {
	tests := []struct {
		name string
		body string
		want pbsNotificationSnapshotSummary
	}{
		{
			name: "stock host: built-ins only, inside the data envelope",
			body: `{"data":[{"name":"mail-to-root","origin":"builtin","type":"sendmail"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 1, BuiltIn: 1},
		},
		{
			name: "user-created target must not land in built-in",
			body: `{"data":[{"name":"mail-to-root","origin":"builtin","type":"sendmail"},
			                {"name":"my-gotify","origin":"user-created","type":"gotify"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 2, BuiltIn: 1, UserCreated: 1},
		},
		{
			name: "modified built-in is user state, not a pristine default",
			body: `{"data":[{"name":"default-matcher","origin":"modified-builtin"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 1, ModifiedBuiltin: 1},
		},
		{
			name: "bare list without the envelope",
			body: `[{"name":"my-smtp","origin":"user-created","type":"smtp"}]`,
			want: pbsNotificationSnapshotSummary{Total: 1, UserCreated: 1},
		},
		{
			name: "origin absent (older PBS) is counted, never guessed",
			body: `{"data":[{"name":"legacy-target"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 1, OriginMissing: 1},
		},
		{
			name: "origin we do not know is counted, never guessed",
			body: `{"data":[{"name":"future","origin":"something-new"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 1, OriginUnrecognized: 1},
		},
		{
			name: "the removed substring rule would have folded these together",
			body: `{"data":[{"name":"a","origin":"builtin"},{"name":"b","origin":"modified-builtin"}]}`,
			want: pbsNotificationSnapshotSummary{Total: 2, BuiltIn: 1, ModifiedBuiltin: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizePBSNotificationSnapshot(writeSnapshot(t, tt.body))

			if got.Error != "" {
				t.Fatalf("unexpected parse error: %s", got.Error)
			}
			if got.Total != tt.want.Total ||
				got.BuiltIn != tt.want.BuiltIn ||
				got.ModifiedBuiltin != tt.want.ModifiedBuiltin ||
				got.UserCreated != tt.want.UserCreated ||
				got.OriginMissing != tt.want.OriginMissing ||
				got.OriginUnrecognized != tt.want.OriginUnrecognized {
				t.Fatalf("buckets mismatch\n got: total=%d builtin=%d modified=%d user=%d missing=%d unknown=%d\nwant: total=%d builtin=%d modified=%d user=%d missing=%d unknown=%d",
					got.Total, got.BuiltIn, got.ModifiedBuiltin, got.UserCreated, got.OriginMissing, got.OriginUnrecognized,
					tt.want.Total, tt.want.BuiltIn, tt.want.ModifiedBuiltin, tt.want.UserCreated, tt.want.OriginMissing, tt.want.OriginUnrecognized)
			}
		})
	}
}

// No object may fall out of the accounting: a listing whose buckets do not add up to Total
// would let a real object hide behind a zero.
func TestSummarizePBSNotificationSnapshot_BucketsSumToTotal(t *testing.T) {
	bodies := []string{
		`{"data":[{"name":"a","origin":"builtin"},{"name":"b","origin":"user-created"},{"name":"c"},"not-an-object",42]}`,
		`[{"origin":"modified-builtin"},{"origin":"UNKNOWN"},{"name":"x","origin":"USER-CREATED"}]`,
	}

	for _, body := range bodies {
		got := summarizePBSNotificationSnapshot(writeSnapshot(t, body))
		sum := got.BuiltIn + got.ModifiedBuiltin + got.UserCreated + got.OriginMissing + got.OriginUnrecognized
		if sum != got.Total {
			t.Fatalf("buckets sum to %d but Total is %d for %s", sum, got.Total, body)
		}
	}
}

func TestSummarizePBSNotificationSnapshot_Unusable(t *testing.T) {
	missing := summarizePBSNotificationSnapshot(filepath.Join(t.TempDir(), "absent.json"))
	if missing.Present || missing.usable() {
		t.Fatalf("a listing that was never captured must not read as usable: %+v", missing)
	}

	broken := summarizePBSNotificationSnapshot(writeSnapshot(t, `{"data":`))
	if !broken.Present || broken.Error == "" || broken.usable() {
		t.Fatalf("an unparseable listing must be Present but unusable: %+v", broken)
	}
}

func newNotificationTestCollector() *Collector {
	return &Collector{config: &CollectorConfig{BackupPBSNotifications: true, BackupPBSNotificationsPriv: true}}
}

func summaryWith(cfg, priv ManifestFileStatus) pbsNotificationsSummary {
	return pbsNotificationsSummary{
		Enabled:     true,
		PrivEnabled: true,
		ConfigFiles: &pbsNotificationsConfigFilesSummary{
			NotificationsCfg:     ManifestEntry{Status: cfg},
			NotificationsPrivCfg: ManifestEntry{Status: priv},
		},
		Endpoints: map[string]pbsNotificationSnapshotSummary{},
	}
}

func joined(lines []string) string { return strings.Join(lines, " | ") }

// The status gate must be immune to what the listings happen to contain. These are the
// scenarios the previous count-based gate got wrong in both directions.
func TestPBSNotificationCfgFindings_GateOnStatusNotCounts(t *testing.T) {
	c := newNotificationTestCollector()

	t.Run("stock host: built-ins listed, file absent, no warning", func(t *testing.T) {
		s := summaryWith(StatusNotFound, StatusNotFound)
		s.Targets = pbsNotificationSnapshotSummary{Present: true, Total: 1, BuiltIn: 1}
		s.Matchers = pbsNotificationSnapshotSummary{Present: true, Total: 1, BuiltIn: 1}

		c.appendPBSNotificationCfgFindings(&s)

		if len(s.Warnings) != 0 {
			t.Fatalf("built-ins alone must not warn, got: %s", joined(s.Warnings))
		}
		if len(s.Notes) != 1 {
			t.Fatalf("expected one explanatory note, got: %s", joined(s.Notes))
		}
	})

	t.Run("excluded file with user-created target still warns", func(t *testing.T) {
		s := summaryWith(StatusSkipped, StatusCollected)
		s.Targets = pbsNotificationSnapshotSummary{Present: true, Total: 2, BuiltIn: 1, UserCreated: 1}

		c.appendPBSNotificationCfgFindings(&s)

		if len(s.Warnings) != 1 {
			t.Fatalf("a skipped notifications.cfg must warn, got: %s", joined(s.Warnings))
		}
	})

	t.Run("excluded file warns even with no listing at all", func(t *testing.T) {
		s := summaryWith(StatusSkipped, StatusCollected)

		c.appendPBSNotificationCfgFindings(&s)

		if len(s.Warnings) != 1 {
			t.Fatalf("an empty listing must not silence the status gate, got: %s", joined(s.Warnings))
		}
	})

	t.Run("failed collection reports the underlying error", func(t *testing.T) {
		s := summaryWith(StatusFailed, StatusCollected)
		s.ConfigFiles.NotificationsCfg.Error = "permission denied"

		c.appendPBSNotificationCfgFindings(&s)

		if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "permission denied") {
			t.Fatalf("the cause must reach the operator, got: %s", joined(s.Warnings))
		}
	})

	t.Run("collected and disabled stay silent", func(t *testing.T) {
		for _, status := range []ManifestFileStatus{StatusCollected, StatusDisabled} {
			s := summaryWith(status, StatusCollected)
			s.Targets = pbsNotificationSnapshotSummary{Present: true, Total: 5, UserCreated: 5}

			c.appendPBSNotificationCfgFindings(&s)

			if len(s.Warnings) != 0 || len(s.Notes) != 0 {
				t.Fatalf("status %s must produce nothing, got warnings=%s notes=%s", status, joined(s.Warnings), joined(s.Notes))
			}
		}
	})

	t.Run("absent file plus live user state means we looked in the wrong place", func(t *testing.T) {
		s := summaryWith(StatusNotFound, StatusCollected)
		s.Targets = pbsNotificationSnapshotSummary{Present: true, Total: 2, BuiltIn: 1, UserCreated: 1}

		c.appendPBSNotificationCfgFindings(&s)

		if len(s.Warnings) != 1 {
			t.Fatalf("expected the lookup-path tripwire, got: %s", joined(s.Warnings))
		}
	})
}

func TestPBSNotificationPrivFindings(t *testing.T) {
	c := newNotificationTestCollector()

	secretCapable := func(total int) map[string]pbsNotificationSnapshotSummary {
		return map[string]pbsNotificationSnapshotSummary{
			"smtp":     {Present: true, Total: total},
			"sendmail": {Present: true, Total: 1},
			"gotify":   {Present: true},
			"webhook":  {Present: true},
		}
	}

	t.Run("excluded priv with secret-capable endpoints warns", func(t *testing.T) {
		s := summaryWith(StatusCollected, StatusSkipped)
		s.Endpoints = secretCapable(1)

		c.appendPBSNotificationPrivFindings(&s)

		if len(s.Warnings) != 1 {
			t.Fatalf("expected one warning, got: %s", joined(s.Warnings))
		}
	})

	t.Run("excluded priv with only sendmail is a note, not a warning", func(t *testing.T) {
		s := summaryWith(StatusCollected, StatusSkipped)
		s.Endpoints = secretCapable(0)

		c.appendPBSNotificationPrivFindings(&s)

		if len(s.Warnings) != 0 {
			t.Fatalf("sendmail holds no secret, so nothing was lost: %s", joined(s.Warnings))
		}
		if len(s.Notes) != 1 {
			t.Fatalf("expected the reassuring note, got: %s", joined(s.Notes))
		}
	})

	t.Run("excluded priv with unreadable listings warns rather than reassures", func(t *testing.T) {
		s := summaryWith(StatusCollected, StatusFailed)
		s.Endpoints = map[string]pbsNotificationSnapshotSummary{
			"smtp":    {Present: false},
			"gotify":  {Present: false},
			"webhook": {Present: false},
		}

		c.appendPBSNotificationPrivFindings(&s)

		if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "unknown") {
			t.Fatalf("with no evidence the answer is unknown, not fine: %s", joined(s.Warnings))
		}
	})

	t.Run("collected priv stays silent", func(t *testing.T) {
		s := summaryWith(StatusCollected, StatusCollected)
		s.Endpoints = secretCapable(3)

		c.appendPBSNotificationPrivFindings(&s)

		if len(s.Warnings) != 0 || len(s.Notes) != 0 {
			t.Fatalf("nothing to say, got warnings=%s notes=%s", joined(s.Warnings), joined(s.Notes))
		}
	})

	t.Run("disabled priv names the flag the operator actually set", func(t *testing.T) {
		parentOff := &Collector{config: &CollectorConfig{BackupPBSNotifications: false}}
		s := summaryWith(StatusDisabled, StatusDisabled)

		parentOff.appendPBSNotificationPrivFindings(&s)

		if len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "BACKUP_PBS_NOTIFICATIONS=") {
			t.Fatalf("with the parent switch off the child flag is the wrong hint: %s", joined(s.Notes))
		}

		s = summaryWith(StatusCollected, StatusDisabled)
		c.appendPBSNotificationPrivFindings(&s)

		if len(s.Notes) != 1 || !strings.Contains(s.Notes[0], "BACKUP_PBS_NOTIFICATIONS_PRIV=") {
			t.Fatalf("expected the child flag here: %s", joined(s.Notes))
		}
	})
}

// "notification target list" is the union of the endpoint listings, so adding the two
// inflates every operator-facing count.
func TestPBSPersistedObjectCount_DoesNotDoubleCountTargets(t *testing.T) {
	s := pbsNotificationsSummary{
		Targets:  pbsNotificationSnapshotSummary{Present: true, Total: 2, BuiltIn: 1, UserCreated: 1},
		Matchers: pbsNotificationSnapshotSummary{Present: true, Total: 1, BuiltIn: 1},
		Endpoints: map[string]pbsNotificationSnapshotSummary{
			"smtp":     {Present: true, Total: 1, UserCreated: 1},
			"sendmail": {Present: true, Total: 1, BuiltIn: 1},
		},
	}

	if got := pbsPersistedObjectCount(s); got != 1 {
		t.Fatalf("the smtp endpoint is the same object as the target; want 1, got %d", got)
	}

	// Without a usable targets listing the endpoint listings are the only evidence left.
	s.Targets = pbsNotificationSnapshotSummary{Present: false}
	if got := pbsPersistedObjectCount(s); got != 1 {
		t.Fatalf("want 1 from the endpoint fallback, got %d", got)
	}

	s.Matchers = pbsNotificationSnapshotSummary{Present: true, Total: 1, ModifiedBuiltin: 1}
	if got := pbsPersistedObjectCount(s); got != 2 {
		t.Fatalf("a modified built-in matcher is user state; want 2, got %d", got)
	}
}
