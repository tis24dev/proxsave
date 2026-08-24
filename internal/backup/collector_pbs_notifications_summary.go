package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// proxmox-notify stamps every notification config section with exactly one of these three
// kebab-case values while parsing (proxmox.git, proxmox-notify/src/lib.rs:284-342,
// #[serde(rename_all = "kebab-case")] enum Origin { UserCreated, Builtin, ModifiedBuiltin }).
// There is no "custom" origin and there never was.
const (
	pbsOriginBuiltin         = "builtin"
	pbsOriginModifiedBuiltin = "modified-builtin"
	pbsOriginUserCreated     = "user-created"
)

// Endpoint kinds that can own a section in notifications-priv.cfg. Creating one always
// writes that section, even with no password or secret in it (proxmox-notify
// api/smtp.rs:60-65, api/gotify.rs:54, api/webhook.rs:102). sendmail has no private
// config at all, so a missing priv file costs it nothing.
var pbsSecretCapableEndpointTypes = []string{"smtp", "gotify", "webhook"}

// All endpoint kinds proxsave snapshots, in the order they are collected.
var pbsNotificationEndpointTypes = []string{"smtp", "sendmail", "gotify", "webhook"}

type pbsNotificationSnapshotSummary struct {
	Present bool  `json:"present"`
	Bytes   int64 `json:"bytes,omitempty"`
	Total   int   `json:"total,omitempty"`

	// Origin breakdown. Invariant, asserted by TestSummarizePBSNotificationSnapshot_BucketsSumToTotal:
	//   BuiltIn + ModifiedBuiltin + UserCreated + OriginMissing + OriginUnrecognized == Total
	BuiltIn            int `json:"built_in,omitempty"`
	ModifiedBuiltin    int `json:"modified_builtin,omitempty"`
	UserCreated        int `json:"user_created,omitempty"`
	OriginMissing      int `json:"origin_missing,omitempty"`
	OriginUnrecognized int `json:"origin_unrecognized,omitempty"`

	Names []string `json:"names,omitempty"`
	Error string   `json:"error,omitempty"`
}

// persisted counts objects positively identified as living in notifications.cfg.
//
// PBS injects its pristine built-ins into every listing from a compiled-in default set even
// when the file does not exist (proxmox-notify lib.rs:315-326), so a non-zero Total proves
// nothing. Only user-created and modified-builtin entries prove there is state on disk.
//
// Unknown and absent origins are excluded on purpose: this number may only ever raise an
// alarm, and an origin we could not read must not raise one.
func (s pbsNotificationSnapshotSummary) persisted() int {
	return s.UserCreated + s.ModifiedBuiltin
}

// usable reports whether this listing actually tells us anything. captureCommandOutput
// writes no file when a non-critical command fails, so Present=false means "we did not get
// the data", never "PBS has none".
func (s pbsNotificationSnapshotSummary) usable() bool {
	return s.Present && s.Error == ""
}

type pbsNotificationsConfigFilesSummary struct {
	NotificationsCfg     ManifestEntry `json:"notifications_cfg"`
	NotificationsPrivCfg ManifestEntry `json:"notifications_priv_cfg"`
}

type pbsNotificationsSummary struct {
	GeneratedAt time.Time `json:"generated_at"`
	Enabled     bool      `json:"enabled"`
	PrivEnabled bool      `json:"priv_enabled"`

	ConfigFiles *pbsNotificationsConfigFilesSummary `json:"config_files,omitempty"`

	Targets   pbsNotificationSnapshotSummary            `json:"targets"`
	Matchers  pbsNotificationSnapshotSummary            `json:"matchers"`
	Endpoints map[string]pbsNotificationSnapshotSummary `json:"endpoints"`

	Notes    []string `json:"notes,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (c *Collector) writePBSNotificationSummary(commandsDir string) {
	if c == nil {
		return
	}

	summary := pbsNotificationsSummary{
		GeneratedAt: time.Now().UTC(),
		Enabled:     c.config != nil && c.config.BackupPBSNotifications,
		PrivEnabled: c.config != nil && c.config.BackupPBSNotifications && c.config.BackupPBSNotificationsPriv,
		Endpoints:   make(map[string]pbsNotificationSnapshotSummary),
	}

	if c.pbsManifest != nil {
		summary.ConfigFiles = &pbsNotificationsConfigFilesSummary{
			NotificationsCfg:     c.pbsManifest["notifications.cfg"],
			NotificationsPrivCfg: c.pbsManifest["notifications-priv.cfg"],
		}
	}

	summary.Targets = summarizePBSNotificationSnapshot(filepath.Join(commandsDir, "notification_targets.json"))
	summary.Matchers = summarizePBSNotificationSnapshot(filepath.Join(commandsDir, "notification_matchers.json"))
	for _, typ := range pbsNotificationEndpointTypes {
		summary.Endpoints[typ] = summarizePBSNotificationSnapshot(filepath.Join(commandsDir, fmt.Sprintf("notification_endpoints_%s.json", typ)))
	}

	if summary.ConfigFiles != nil {
		c.appendPBSNotificationCfgFindings(&summary)
		c.appendPBSNotificationPrivFindings(&summary)
	}
	c.appendPBSNotificationEvidenceNotes(&summary)

	// Surface important mismatches in the console log too.
	if c.logger != nil {
		c.logger.Info("PBS notifications snapshot summary: targets=%d matchers=%d endpoints=%d",
			summary.Targets.Total,
			summary.Matchers.Total,
			sumEndpointTotals(summary.Endpoints),
		)
		for _, note := range summary.Notes {
			c.logger.Info("PBS notifications: %s", note)
		}
		for _, warning := range summary.Warnings {
			c.logger.Warning("PBS notifications: %s", warning)
		}
	}

	out, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		c.logger.Debug("PBS notifications summary skipped: marshal error: %v", err)
		return
	}

	if err := c.writeReportFile(filepath.Join(commandsDir, "notifications_summary.json"), out); err != nil {
		c.logger.Debug("PBS notifications summary write failed: %v", err)
	}
}

// appendPBSNotificationCfgFindings decides restorability from the collection status of
// notifications.cfg alone.
//
// Restore rebuilds targets and matchers from that file only (internal/orchestrator/
// pbs_notifications_api_apply.go:61-66); the JSON snapshots are staged but never read back.
// Meanwhile "notification target list" returns persisted state merged with synthetic
// defaults, so object counts cannot answer "is there anything to lose". Status decides
// whether a finding fires; the counters may only ever add one.
func (c *Collector) appendPBSNotificationCfgFindings(summary *pbsNotificationsSummary) {
	cfg := summary.ConfigFiles.NotificationsCfg
	path := filepath.Join(c.pbsConfigPath(), "notifications.cfg")

	switch cfg.Status {
	case StatusCollected, StatusDisabled:
		return

	case StatusNotFound:
		summary.Notes = append(summary.Notes, fmt.Sprintf(
			"No notification config to back up: %s does not exist. PBS serves its built-in targets and matchers from a compiled-in default set, so this is the normal state until someone creates or edits one.",
			path))

		// A file that is genuinely absent cannot hold user state. If the listings say
		// otherwise, we stat'ed the wrong place (SYSTEM_ROOT_PREFIX, PBS_CONFIG_PATH).
		if n := pbsPersistedObjectCount(*summary); n > 0 {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf(
				"%d user-created or user-modified notification object(s) are live on this host, but %s was not found. The configured lookup path is probably wrong, and those objects would not be restorable from this backup.",
				n, path))
		}

	default:
		// StatusSkipped, StatusFailed, and any status added later. An exclude pattern that
		// swallows this file is logged at Info only (collector_pbs.go:38), so this is the
		// sole signal above Info level. Never gate it on object counts.
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s). Notification targets and matchers are restored from that file alone, so they would not survive a restore from this backup. Check exclusion patterns and BACKUP_PBS_NOTIFICATIONS.",
			path, pbsNotificationStatusDetail(cfg)))
	}
}

// appendPBSNotificationPrivFindings does the same for notifications-priv.cfg, using
// endpoint kind rather than origin as its evidence: the built-in default set holds one
// sendmail target and one matcher, so every smtp, gotify or webhook endpoint is user
// state by construction.
func (c *Collector) appendPBSNotificationPrivFindings(summary *pbsNotificationsSummary) {
	priv := summary.ConfigFiles.NotificationsPrivCfg
	path := filepath.Join(c.pbsConfigPath(), "notifications-priv.cfg")

	if priv.Status == StatusCollected {
		return
	}

	if priv.Status == StatusDisabled {
		summary.Notes = append(summary.Notes, fmt.Sprintf(
			"%s backup is disabled (%s=false); endpoint credentials and secrets will not be included.",
			path, c.pbsNotificationsPrivDisableHint()))
		return
	}

	// What losing this file actually costs, per endpoint kind: the gotify token is a
	// required positional for the restore path, which skips the endpoint outright without
	// it (pbs_notifications_api_apply.go:180-196). smtp and webhook take their positional
	// from the public config, so they come back looking fine and fail when they first send.
	consequence := "Gotify endpoints would be dropped by the restore path because their token lives in that file; smtp and webhook endpoints would be recreated without their password or secrets and fail silently the first time they try to send."

	observed, evidenceUsable := pbsSecretCapableEndpointEvidence(*summary)

	if priv.Status == StatusNotFound {
		// PBS writes a private section whenever a secret-capable endpoint is created, even
		// when it carries no secret, so endpoints without the file means we looked in the
		// wrong place rather than that there was nothing to collect.
		if observed > 0 {
			summary.Notes = append(summary.Notes, fmt.Sprintf(
				"%d secret-capable notification endpoint(s) are configured, but %s was not found. %s",
				observed, path, consequence))
		}
		return
	}

	// StatusSkipped, StatusFailed, and any status added later.
	switch {
	case observed > 0:
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s) while %d secret-capable endpoint(s) are configured. %s",
			path, pbsNotificationStatusDetail(priv), observed, consequence))
	case !evidenceUsable:
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s) and no endpoint listing could be read, so whether any secret was lost is unknown. %s",
			path, pbsNotificationStatusDetail(priv), consequence))
	default:
		summary.Notes = append(summary.Notes, fmt.Sprintf(
			"%s was not collected (%s), but no smtp, gotify or webhook endpoint is configured, so no secret was lost.",
			path, pbsNotificationStatusDetail(priv)))
	}
}

// appendPBSNotificationEvidenceNotes records listings that could not be captured or parsed,
// so that a quiet run is never mistaken for a verified-clean one.
func (c *Collector) appendPBSNotificationEvidenceNotes(summary *pbsNotificationsSummary) {
	if c.dryRun || !summary.Enabled {
		return
	}

	listings := []struct {
		name string
		snap pbsNotificationSnapshotSummary
	}{
		{"targets", summary.Targets},
		{"matchers", summary.Matchers},
	}
	for _, typ := range pbsNotificationEndpointTypes {
		listings = append(listings, struct {
			name string
			snap pbsNotificationSnapshotSummary
		}{fmt.Sprintf("%s endpoint", typ), summary.Endpoints[typ]})
	}

	for _, l := range listings {
		switch {
		case !l.snap.Present:
			summary.Notes = append(summary.Notes, fmt.Sprintf(
				"Notification %s listing was not captured, so its contents are unknown. On PBS 3.2 and older this is expected for webhook, which has no list command there.", l.name))
		case l.snap.Error != "":
			summary.Notes = append(summary.Notes, fmt.Sprintf(
				"Notification %s listing could not be read (%s), so its contents are unknown.", l.name, l.snap.Error))
		}
	}
}

// pbsPersistedObjectCount counts objects that prove notifications.cfg has content.
//
// "notification target list" is the union of the four endpoint listings (proxmox-notify
// api/mod.rs:85-120 builds it by iterating gotify, sendmail, smtp and webhook), so the two
// must never be summed. Prefer the targets listing and fall back to the endpoint listings
// only when it is unusable. Matchers are a separate namespace and are added.
func pbsPersistedObjectCount(summary pbsNotificationsSummary) int {
	total := summary.Matchers.persisted()

	if summary.Targets.usable() {
		return total + summary.Targets.persisted()
	}
	for _, s := range summary.Endpoints {
		total += s.persisted()
	}
	return total
}

// pbsSecretCapableEndpointEvidence reports how many smtp, gotify and webhook endpoints were
// observed, and whether any of those listings could actually be read.
//
// evidenceUsable stays true as long as one of them was readable. PBS 3.2 and older have no
// "notification endpoint webhook list" command, so requiring all three would leave this
// permanently false there and warn on every run.
func pbsSecretCapableEndpointEvidence(summary pbsNotificationsSummary) (observed int, evidenceUsable bool) {
	for _, typ := range pbsSecretCapableEndpointTypes {
		s, ok := summary.Endpoints[typ]
		if !ok {
			continue
		}
		if s.usable() {
			evidenceUsable = true
		}
		observed += s.Total
	}
	return observed, evidenceUsable
}

// pbsNotificationsPrivDisableHint mirrors collectPBSManifestNotificationsPriv: when the
// parent switch is off, naming the child flag would send the operator to the wrong setting.
func (c *Collector) pbsNotificationsPrivDisableHint() string {
	if c.config != nil && !c.config.BackupPBSNotifications {
		return "BACKUP_PBS_NOTIFICATIONS"
	}
	return "BACKUP_PBS_NOTIFICATIONS_PRIV"
}

func pbsNotificationStatusDetail(entry ManifestEntry) string {
	status := strings.TrimSpace(string(entry.Status))
	if status == "" {
		status = "unknown status"
	} else {
		status = "status: " + status
	}
	if err := strings.TrimSpace(entry.Error); err != "" {
		return fmt.Sprintf("%s: %s", status, err)
	}
	return status
}

func sumEndpointTotals(endpoints map[string]pbsNotificationSnapshotSummary) int {
	total := 0
	for _, s := range endpoints {
		total += s.Total
	}
	return total
}

func summarizePBSNotificationSnapshot(path string) pbsNotificationSnapshotSummary {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return pbsNotificationSnapshotSummary{Present: false}
		}
		return pbsNotificationSnapshotSummary{
			Present: false,
			Error:   err.Error(),
		}
	}

	summary := pbsNotificationSnapshotSummary{
		Present: true,
		Bytes:   int64(len(raw)),
	}

	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return summary
	}

	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		summary.Error = fmt.Sprintf("invalid json: %v", err)
		return summary
	}

	// Unwrap proxmox-backup-manager JSON envelope (common shape: {"data":[...], ...}).
	if m, ok := payload.(map[string]any); ok {
		if data, ok := m["data"]; ok {
			payload = data
		}
	}

	items, ok := payload.([]any)
	if !ok {
		summary.Error = "unexpected json shape (expected list)"
		return summary
	}

	summary.Total = len(items)

	names := make([]string, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			// Keep the buckets summing to Total: a shape we cannot read must not vanish
			// from the accounting.
			summary.OriginUnrecognized++
			continue
		}

		name := firstString(entry, "name", "id", "target", "matcher")
		if name != "" {
			names = append(names, name)
		}

		// Exact match rather than substring: "modified-builtin" contains "builtin", so any
		// Contains rule silently folds user-modified defaults into the pristine bucket.
		switch strings.ToLower(strings.TrimSpace(firstString(entry, "origin"))) {
		case pbsOriginBuiltin:
			summary.BuiltIn++
		case pbsOriginModifiedBuiltin:
			summary.ModifiedBuiltin++
		case pbsOriginUserCreated:
			summary.UserCreated++
		case "":
			summary.OriginMissing++
		default:
			summary.OriginUnrecognized++
		}
	}

	sort.Strings(names)
	if len(names) > 100 {
		names = names[:100]
	}
	if len(names) > 0 {
		summary.Names = names
	}

	return summary
}

func firstString(entry map[string]any, keys ...string) string {
	for _, key := range keys {
		v, ok := entry[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}
