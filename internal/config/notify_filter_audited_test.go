package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNotifyOnIsReadFromTheConfig pins the read site. Nothing else in the tree
// cross-checks this string setting against the key name the template documents, so a
// typo in either one passes the whole suite: the loader would quietly leave NotifyOn at
// "always" forever and the only symptom is that the operator keeps getting the
// notifications they switched off.
func TestNotifyOnIsReadFromTheConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			// The key a config predating the feature does not carry at all. This is the
			// upgrade path for every existing install and it must not change behaviour.
			name:    "absent",
			content: "BACKUP_ENABLED=true\n",
			want:    NotifyOnAlways,
		},
		{
			// A bare assignment reaches getString as "" rather than as a missing key, so
			// the default argument never applies and the normalizer is what saves it.
			// Without the empty case NotifyOn would be "", which is not a policy at all.
			name:    "assigned empty",
			content: "NOTIFY_ON=\n",
			want:    NotifyOnAlways,
		},
		{name: "always", content: "NOTIFY_ON=always\n", want: NotifyOnAlways},
		{name: "warning", content: "NOTIFY_ON=warning\n", want: NotifyOnWarning},
		{name: "failure", content: "NOTIFY_ON=failure\n", want: NotifyOnFailure},
		{name: "uppercase", content: "NOTIFY_ON=WARNING\n", want: NotifyOnWarning},
		{name: "padded", content: "NOTIFY_ON=  failure  \n", want: NotifyOnFailure},
		{
			// The plural is the typo a reader of the docs makes, and the singular/plural
			// pair is the one place a silent fallback to "always" would look like the
			// feature not working at all.
			name:    "plural warnings",
			content: "NOTIFY_ON=warnings\n",
			want:    NotifyOnWarning,
		},
		{name: "error means failure", content: "NOTIFY_ON=error\n", want: NotifyOnFailure},
		{
			// An unrecognised value is NOT coerced here. It travels to the dispatcher
			// intact so the warning can quote what the operator actually wrote, which is
			// the same contract NormalizeEmailDeliveryMethod has with extensions.go.
			// Coercing it to "always" here would make the config silently self-correcting
			// and the typo undiscoverable.
			name:    "unrecognised is passed through",
			content: "NOTIFY_ON=only-when-broken\n",
			want:    "only-when-broken",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := loadEnvForTest(t, "backup.env", tc.content).NotifyOn; got != tc.want {
				t.Fatalf("NotifyOn = %q; want %q", got, tc.want)
			}
		})
	}
}

// The shipped template must leave a fresh install notifying on everything. The key is
// deliberately a COMMENTED example there and not an active assignment: an active one
// would be Absent in every config written before this release, and an absent template
// variable is a WARNING, which promotes an otherwise clean run to exit 1. That would turn
// the feature for quieting noisy notifications into a new source of noisy notifications
// for every operator who never set it.
func TestTheShippedTemplateLeavesNotifyOnAtTheDefault(t *testing.T) {
	if got := loadEnvForTest(t, "shipped.env", DefaultEnvTemplate()).NotifyOn; got != NotifyOnAlways {
		t.Fatalf("template NotifyOn = %q; want %q", got, NotifyOnAlways)
	}

	tmpl := DefaultEnvTemplate()
	for _, line := range strings.Split(tmpl, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "NOTIFY_ON=") {
			t.Fatalf("the template assigns NOTIFY_ON on an uncommented line (%q); it must stay a commented example", line)
		}
	}
	if !strings.Contains(tmpl, "# NOTIFY_ON=") {
		t.Fatal("the template must document NOTIFY_ON as a commented example, or the audit reports it unknown when an operator sets it")
	}
}

// NOTIFY_ON is on envOverrideKeys, so the shell can override the file like every other
// notification key -- which is also how the behavioural checks in the PR were run without
// editing a live backup.env. Membership in that list is load-bearing twice over: it is
// what loadEnvForTest neutralises, and a key missing from it leaks a developer's or a CI
// job's shell value into every config a comparison test builds, so the two sides agree
// because of the shell rather than because the code agrees.
func TestNotifyOnHonoursTheEnvironmentOverride(t *testing.T) {
	var listed bool
	for _, key := range envOverrideKeys {
		if key == "NOTIFY_ON" {
			listed = true
			break
		}
	}
	if !listed {
		t.Fatal("NOTIFY_ON must be in envOverrideKeys, or the shell cannot override the file and loadEnvForTest cannot neutralise it")
	}

	// Loaded directly rather than through loadEnvForTest, which blanks every override key
	// by design and so can never observe an override taking effect.
	path := filepath.Join(t.TempDir(), "backup.env")
	if err := os.WriteFile(path, []byte("NOTIFY_ON=always\n"), 0o644); err != nil {
		t.Fatalf("write backup.env: %v", err)
	}
	t.Setenv("NOTIFY_ON", "failure")
	cfg, err := LoadConfigWithBaseDir(path, "/custom/base")
	if err != nil {
		t.Fatalf("LoadConfigWithBaseDir: %v", err)
	}
	if cfg.NotifyOn != NotifyOnFailure {
		t.Fatalf("NotifyOn = %q; the environment must win over the file, want %q", cfg.NotifyOn, NotifyOnFailure)
	}
}

// IsValidNotifyOn answers exactly one question -- did NormalizeNotifyOn understand the
// value -- and the dispatcher's warning is gated on it. If it ever returned true for a
// passed-through value the warning would never fire and the typo would stay invisible.
func TestIsValidNotifyOnAcceptsOnlyTheCanonicalValues(t *testing.T) {
	for _, v := range []string{NotifyOnAlways, NotifyOnWarning, NotifyOnFailure} {
		if !IsValidNotifyOn(v) {
			t.Errorf("IsValidNotifyOn(%q) = false; want true", v)
		}
	}
	for _, v := range []string{"", " ", "ALWAYS", "warnings", "only-when-broken", "never"} {
		if IsValidNotifyOn(v) {
			t.Errorf("IsValidNotifyOn(%q) = true; only the canonical values are valid", v)
		}
	}

	// Everything NormalizeNotifyOn claims to understand must come back canonical, or the
	// dispatcher warns about a value the loader accepted.
	for _, v := range []string{"", "  ", "Always", "ALL", "any", "warn", "WARNINGS", "failed", "Fail", "errors"} {
		if got := NormalizeNotifyOn(v); !IsValidNotifyOn(got) {
			t.Errorf("NormalizeNotifyOn(%q) = %q, which IsValidNotifyOn rejects", v, got)
		}
	}
}
