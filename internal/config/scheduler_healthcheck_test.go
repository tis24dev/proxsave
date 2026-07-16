package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSchedulerHealthcheckDefaults(t *testing.T) {
	c := &Config{raw: map[string]string{}}
	c.parseSchedulerSettings()
	c.parseHealthcheckSettings()

	if c.SchedulerMode != "cron" {
		t.Errorf("SchedulerMode = %q, want cron", c.SchedulerMode)
	}
	if c.SchedulerTime != "02:00" {
		t.Errorf("SchedulerTime = %q, want 02:00", c.SchedulerTime)
	}
	if c.MaxRunDuration != 1*time.Hour {
		t.Errorf("MaxRunDuration = %s, want 1h", c.MaxRunDuration)
	}
	if c.DaemonOptOut {
		t.Errorf("DaemonOptOut = true, want false")
	}
	if c.HealthcheckEnabled {
		t.Errorf("HealthcheckEnabled = true, want false")
	}
	if c.HealthcheckMode != "centralized" {
		t.Errorf("HealthcheckMode = %q, want centralized", c.HealthcheckMode)
	}
	if c.HealthcheckHeartbeatInterval != 5*time.Minute {
		t.Errorf("HealthcheckHeartbeatInterval = %s, want 5m", c.HealthcheckHeartbeatInterval)
	}
	if !c.HealthcheckSendLog {
		t.Errorf("HealthcheckSendLog = false, want true (default on)")
	}
	if c.HealthcheckPingEndpoint != "https://hc-ping.com" {
		t.Errorf("HealthcheckPingEndpoint = %q, want https://hc-ping.com", c.HealthcheckPingEndpoint)
	}
}

func TestParseSchedulerHealthcheckValues(t *testing.T) {
	c := &Config{raw: map[string]string{
		"SCHEDULER_MODE":                 "daemon",
		"SCHEDULER_TIME":                 "03:30",
		"MAX_RUN_DURATION":               "2h",
		"DAEMON_OPT_OUT":                 "true",
		"HEALTHCHECK_ENABLED":            "true",
		"HEALTHCHECK_MODE":               "self",
		"HEALTHCHECK_HEARTBEAT_INTERVAL": "30s",
		"HEALTHCHECK_SEND_LOG":           "false",
		"HEALTHCHECK_ALIVE_URL":          "https://hc.example/ping/a",
		"HEALTHCHECK_BACKUP_URL":         "https://hc.example/ping/b",
		"HEALTHCHECK_PING_ENDPOINT":      "https://my.hc",
		"HEALTHCHECK_PING_KEY":           "pk",
		"HEALTHCHECK_ALIVE_ID":           "alive-slug",
		"HEALTHCHECK_BACKUP_ID":          "backup-slug",
	}}
	c.parseSchedulerSettings()
	c.parseHealthcheckSettings()

	if c.SchedulerMode != "daemon" {
		t.Errorf("SchedulerMode = %q, want daemon", c.SchedulerMode)
	}
	if c.SchedulerTime != "03:30" {
		t.Errorf("SchedulerTime = %q, want 03:30", c.SchedulerTime)
	}
	if c.MaxRunDuration != 2*time.Hour {
		t.Errorf("MaxRunDuration = %s, want 2h", c.MaxRunDuration)
	}
	if !c.DaemonOptOut {
		t.Errorf("DaemonOptOut = false, want true")
	}
	if !c.HealthcheckEnabled {
		t.Errorf("HealthcheckEnabled = false, want true")
	}
	if c.HealthcheckMode != "self" {
		t.Errorf("HealthcheckMode = %q, want self", c.HealthcheckMode)
	}
	if c.HealthcheckHeartbeatInterval != 30*time.Second {
		t.Errorf("HealthcheckHeartbeatInterval = %s, want 30s", c.HealthcheckHeartbeatInterval)
	}
	if c.HealthcheckSendLog {
		t.Errorf("HealthcheckSendLog = true, want false")
	}
	if c.HealthcheckAliveURL != "https://hc.example/ping/a" || c.HealthcheckBackupURL != "https://hc.example/ping/b" {
		t.Errorf("centralized cache urls not parsed: %q / %q", c.HealthcheckAliveURL, c.HealthcheckBackupURL)
	}
	if c.HealthcheckPingEndpoint != "https://my.hc" || c.HealthcheckPingKey != "pk" {
		t.Errorf("self-mode endpoint/key not parsed: %q / %q", c.HealthcheckPingEndpoint, c.HealthcheckPingKey)
	}
	if c.HealthcheckAliveID != "alive-slug" || c.HealthcheckBackupID != "backup-slug" {
		t.Errorf("self-mode check ids not parsed: %q / %q", c.HealthcheckAliveID, c.HealthcheckBackupID)
	}
}

// TestHealthcheckNotifyConfigParse proves the 8 self-mode per-channel notify keys
// (HEALTHCHECK_NOTIFY_<CH>_URL / _ID for email/telegram/gotify/webhook) parse into their
// Config fields. It mirrors the other HEALTHCHECK_* tests, which seed the raw map and call
// parseHealthcheckSettings directly (getString reads c.raw, not the process environment).
func TestHealthcheckNotifyConfigParse(t *testing.T) {
	c := &Config{raw: map[string]string{
		"HEALTHCHECK_NOTIFY_EMAIL_URL":    "https://hc/ping/e",
		"HEALTHCHECK_NOTIFY_EMAIL_ID":     "eid",
		"HEALTHCHECK_NOTIFY_TELEGRAM_URL": "https://hc/ping/t",
		"HEALTHCHECK_NOTIFY_TELEGRAM_ID":  "tid",
		"HEALTHCHECK_NOTIFY_GOTIFY_URL":   "https://hc/ping/g",
		"HEALTHCHECK_NOTIFY_GOTIFY_ID":    "gid",
		"HEALTHCHECK_NOTIFY_WEBHOOK_URL":  "https://hc/ping/w",
		"HEALTHCHECK_NOTIFY_WEBHOOK_ID":   "wid",
	}}
	c.parseHealthcheckSettings()

	cases := []struct{ name, got, want string }{
		{"HealthcheckNotifyEmailURL", c.HealthcheckNotifyEmailURL, "https://hc/ping/e"},
		{"HealthcheckNotifyEmailID", c.HealthcheckNotifyEmailID, "eid"},
		{"HealthcheckNotifyTelegramURL", c.HealthcheckNotifyTelegramURL, "https://hc/ping/t"},
		{"HealthcheckNotifyTelegramID", c.HealthcheckNotifyTelegramID, "tid"},
		{"HealthcheckNotifyGotifyURL", c.HealthcheckNotifyGotifyURL, "https://hc/ping/g"},
		{"HealthcheckNotifyGotifyID", c.HealthcheckNotifyGotifyID, "gid"},
		{"HealthcheckNotifyWebhookURL", c.HealthcheckNotifyWebhookURL, "https://hc/ping/w"},
		{"HealthcheckNotifyWebhookID", c.HealthcheckNotifyWebhookID, "wid"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

func TestSchedulerHealthcheckNormalizeFallback(t *testing.T) {
	c := &Config{raw: map[string]string{
		"SCHEDULER_MODE":   "garbage",
		"HEALTHCHECK_MODE": "weird",
		"MAX_RUN_DURATION": "notaduration",
	}}
	c.parseSchedulerSettings()
	c.parseHealthcheckSettings()
	if c.SchedulerMode != "cron" {
		t.Errorf("garbage SCHEDULER_MODE should fall back to cron, got %q", c.SchedulerMode)
	}
	if c.HealthcheckMode != "centralized" {
		t.Errorf("garbage HEALTHCHECK_MODE should fall back to centralized, got %q", c.HealthcheckMode)
	}
	if c.MaxRunDuration != 1*time.Hour {
		t.Errorf("unparseable MAX_RUN_DURATION should fall back to 1h, got %s", c.MaxRunDuration)
	}
}

// TestRealTemplateContainsNewKeys proves the embedded backup.env carries the new
// keys, so --upgrade/--upgrade-config inserts them into existing installs (the
// merge walks DefaultEnvTemplate()).
func TestRealTemplateContainsNewKeys(t *testing.T) {
	tmpl := DefaultEnvTemplate()
	for _, key := range []string{
		"SCHEDULER_MODE=", "SCHEDULER_TIME=", "MAX_RUN_DURATION=", "DAEMON_OPT_OUT=",
		"HEALTHCHECK_ENABLED=", "HEALTHCHECK_MODE=", "HEALTHCHECK_HEARTBEAT_INTERVAL=",
		"HEALTHCHECK_SEND_LOG=", "HEALTHCHECK_ALIVE_URL=", "HEALTHCHECK_BACKUP_URL=",
		"HEALTHCHECK_PING_ENDPOINT=", "HEALTHCHECK_PING_KEY=", "HEALTHCHECK_ALIVE_ID=",
		"HEALTHCHECK_BACKUP_ID=",
		"HEALTHCHECK_UPDATES_URL=", "HEALTHCHECK_UPDATES_ID=", "HEALTHCHECK_UPDATE_INTERVAL=",
		"HEALTHCHECK_NOTIFY_EMAIL_URL=", "HEALTHCHECK_NOTIFY_EMAIL_ID=",
		"HEALTHCHECK_NOTIFY_TELEGRAM_URL=", "HEALTHCHECK_NOTIFY_TELEGRAM_ID=",
		"HEALTHCHECK_NOTIFY_GOTIFY_URL=", "HEALTHCHECK_NOTIFY_GOTIFY_ID=",
		"HEALTHCHECK_NOTIFY_WEBHOOK_URL=", "HEALTHCHECK_NOTIFY_WEBHOOK_ID=",
	} {
		if !strings.Contains(tmpl, key) {
			t.Errorf("embedded template is missing new key %q", key)
		}
	}
}

const schedMergeTemplate = `BACKUP_PATH=/default/backup
LOG_PATH=/default/log
SCHEDULER_MODE=cron
SCHEDULER_TIME=02:00
MAX_RUN_DURATION=6h
DAEMON_OPT_OUT=false
HEALTHCHECK_ENABLED=false
HEALTHCHECK_MODE=centralized
HEALTHCHECK_ALIVE_URL=
`

// TestUpgradePreservesDaemonOptOut is the retrofit-safety guarantee: a user who
// ran --daemon-remove (DAEMON_OPT_OUT=true) must keep that value across every
// future --upgrade, so the daemon is never silently reinstalled.
func TestUpgradePreservesDaemonOptOut(t *testing.T) {
	withTemplate(t, schedMergeTemplate, func() {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "backup.env")
		legacy := "BACKUP_PATH=/legacy\nDAEMON_OPT_OUT=true\n"
		if err := os.WriteFile(configPath, []byte(legacy), 0600); err != nil {
			t.Fatalf("seed config: %v", err)
		}

		result, err := UpgradeConfigFile(configPath)
		if err != nil {
			t.Fatalf("UpgradeConfigFile: %v", err)
		}
		if !result.Changed {
			t.Fatalf("expected Changed=true (new keys missing)")
		}
		// DAEMON_OPT_OUT was already present -> must NOT be counted as missing.
		for _, k := range result.MissingKeys {
			if k == "DAEMON_OPT_OUT" {
				t.Fatalf("DAEMON_OPT_OUT wrongly treated as missing (would reset to false)")
			}
		}
		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("read merged config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "DAEMON_OPT_OUT=true") {
			t.Fatalf("merge clobbered the user's DAEMON_OPT_OUT=true:\n%s", content)
		}
		for _, k := range []string{"SCHEDULER_MODE=", "SCHEDULER_TIME=", "HEALTHCHECK_ENABLED="} {
			if !strings.Contains(content, k) {
				t.Fatalf("merge did not add %q", k)
			}
		}
	})
}

// assertSafeDaemonDefaults asserts a merged/migrated config's RAW map carries the
// daemon/healthcheck block with the behaviour-preserving defaults. It reads the raw
// INJECTED values (not the parsed Config) on purpose: the parsers supply the SAME
// safe defaults for ABSENT keys, so a template that stopped injecting the block
// would sail through a parsed-value check -- the ok-presence check below catches
// that. The empty-string checks catch a stale/attacker ping URL/ID that a parsed
// check would miss (AliveURL/BackupURL are only read once healthchecks is enabled).
func assertSafeDaemonDefaults(t *testing.T, raw map[string]string) {
	t.Helper()
	want := []struct{ key, val string }{
		{"SCHEDULER_MODE", "cron"},       // upgraded host stays on cron
		{"HEALTHCHECK_ENABLED", "false"}, // no pinging until explicitly enabled
		{"DAEMON_OPT_OUT", "false"},      // a fresh upgrade is not a --daemon-remove tombstone
		{"HEALTHCHECK_MODE", "centralized"},
		{"HEALTHCHECK_PING_ENDPOINT", "https://hc-ping.com"},
		// Empty by default: a non-empty default would make a host ping a stale or
		// attacker-chosen URL the moment HEALTHCHECK_ENABLED is flipped on.
		{"HEALTHCHECK_ALIVE_URL", ""},
		{"HEALTHCHECK_BACKUP_URL", ""},
		{"HEALTHCHECK_PING_KEY", ""},
		{"HEALTHCHECK_ALIVE_ID", ""},
		{"HEALTHCHECK_BACKUP_ID", ""},
		// New self-mode ping URLs/IDs: all empty so no accidental self activation
		// (a non-empty default would ping a stale/attacker URL when self mode flips on).
		{"HEALTHCHECK_UPDATES_URL", ""},
		{"HEALTHCHECK_UPDATES_ID", ""},
		{"HEALTHCHECK_UPDATE_INTERVAL", "5m"},
		{"HEALTHCHECK_NOTIFY_EMAIL_URL", ""},
		{"HEALTHCHECK_NOTIFY_EMAIL_ID", ""},
		{"HEALTHCHECK_NOTIFY_TELEGRAM_URL", ""},
		{"HEALTHCHECK_NOTIFY_TELEGRAM_ID", ""},
		{"HEALTHCHECK_NOTIFY_GOTIFY_URL", ""},
		{"HEALTHCHECK_NOTIFY_GOTIFY_ID", ""},
		{"HEALTHCHECK_NOTIFY_WEBHOOK_URL", ""},
		{"HEALTHCHECK_NOTIFY_WEBHOOK_ID", ""},
	}
	for _, w := range want {
		got, ok := raw[w.key]
		if !ok {
			t.Errorf("%s absent: the daemon/healthcheck block was not injected", w.key)
			continue
		}
		if got != w.val {
			t.Errorf("%s = %q, want %q (a template-default change here flips behaviour on every upgraded install)", w.key, got, w.val)
		}
	}
}

// TestUpgradeRealTemplateKeepsExistingInstallSafe guards the config-MERGE step of
// --upgrade against a dangerous template-default typo. A pre-daemon config (no
// scheduler/healthcheck block), merged against the REAL embedded template (NO
// withTemplate override -> the production merge in computeConfigUpgrade), must gain
// the block with the SAFE defaults. TestRealTemplateContainsNewKeys only checks key
// PRESENCE, so a value typo (HEALTHCHECK_ENABLED=true, SCHEDULER_MODE=daemon, or a
// non-empty ping URL) would land verbatim in every merged config undetected. This
// asserts the raw INJECTED values and that they parse to safe semantics. (Scope is
// the merge; the separate, DAEMON_OPT_OUT-gated retrofit auto-migration that a full
// --upgrade may then run is not exercised here.)
func TestUpgradeRealTemplateKeepsExistingInstallSafe(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "backup.env")
	if err := os.WriteFile(configPath, []byte("BACKUP_PATH=/data/backup\nLOG_PATH=/data/log\n"), 0600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	result, err := UpgradeConfigFile(configPath)
	if err != nil {
		t.Fatalf("UpgradeConfigFile (real template): %v", err)
	}
	if !result.Changed {
		t.Fatalf("expected the daemon/healthcheck block to be added (Changed=true)")
	}

	raw, err := parseEnvFile(configPath)
	if err != nil {
		t.Fatalf("parse merged config: %v", err)
	}
	// The injected VALUES are the safe defaults (raw map, presence-aware).
	assertSafeDaemonDefaults(t, raw)

	// ...and they parse to safe SEMANTICS (parse-path fidelity).
	c := &Config{raw: raw}
	c.parseSchedulerSettings()
	c.parseHealthcheckSettings()
	if c.SchedulerMode != "cron" || c.HealthcheckEnabled || c.DaemonOptOut {
		t.Errorf("merged config parses to an unsafe daemon state: mode=%q enabled=%v optout=%v",
			c.SchedulerMode, c.HealthcheckEnabled, c.DaemonOptOut)
	}
	// The user's own value survives the real-template merge (not clobbered).
	if raw["BACKUP_PATH"] != "/data/backup" {
		t.Errorf("merge clobbered the user's BACKUP_PATH: %q", raw["BACKUP_PATH"])
	}
}
