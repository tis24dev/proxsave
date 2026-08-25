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

	// Type breakdown, read from the per-entry "type" field. Invariant, asserted by
	// TestSummarizePBSNotificationSnapshot_TypeBucketsSumToTotal:
	//   sum(ByType) + TypeMissing == Total
	//
	// Only the targets listing is ever consulted through these. "notification target list"
	// is the union of the four endpoint kinds (proxmox-notify api/mod.rs:85-120) and stamps
	// every entry with its type, so ONE readable targets listing can account for endpoint
	// kinds whose own listing was never captured -- including on PBS 3.2 and older, where
	// `notification endpoint webhook list` does not exist and webhook endpoints cannot
	// exist either.
	//
	// TypeMissing exists so that an entry we could not type VETOES that use rather than
	// silently shrinking a count we are about to call complete.
	ByType      map[string]int `json:"by_type,omitempty"`
	TypeMissing int            `json:"type_missing,omitempty"`

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

	// A dry run executes no listing command, so every listing is absent BY CONSTRUCTION.
	// The status below is real, but any sentence about secret loss would rest on evidence
	// nobody attempted to gather -- and today that produces an exit-code-bearing warning
	// about a run in which nothing was attempted. appendPBSNotificationEvidenceNotes
	// already carries this guard; this closes the asymmetry.
	if c.dryRun {
		return
	}

	// An empty Status is not a status. It arises from a missing c.pbsManifest key, which
	// means the manifest brick did not run, NOT that collection was skipped. Falling
	// through would emit a finding whose premise ("was not collected (unknown status)") is
	// fabricated. Note this is NOT covered by a !summary.Enabled guard: the hazard occurs
	// when BACKUP_PBS_NOTIFICATIONS is TRUE.
	if strings.TrimSpace(string(priv.Status)) == "" {
		return
	}

	// What losing this file actually costs, per endpoint kind: the gotify token is a
	// required positional for the restore path, which skips the endpoint outright without
	// it (pbs_notifications_api_apply.go). smtp and webhook take their positional from the
	// public config, so they come back looking fine and fail when they first send.
	consequence := "Gotify endpoints would be dropped by the restore path because their token lives in that file; smtp and webhook endpoints would be recreated without their password or secrets and fail silently the first time they try to send."

	ev := pbsSecretCapableEndpointEvidence(*summary)

	if priv.Status == StatusNotFound {
		// PBS writes a private section whenever a secret-capable endpoint is created, even
		// when it carries no secret, so endpoints without the file means we looked in the
		// wrong place rather than that there was nothing to collect.
		// A Warning, not a Note, and for the same reason the notifications.cfg branch
		// above uses one: PBS writes a private section whenever a secret-capable endpoint
		// is created, so on a healthy host this state cannot happen. It means either a
		// wrong lookup path or real secret loss. A Note only reaches Info level and never
		// enters the run issue summary, so the operator can miss it entirely.
		if ev.Observed > 0 {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf(
				"%s secret-capable notification endpoint(s) are configured, but %s was not found. %s",
				ev.countPhrase(), path, consequence))
		}
		return
	}

	// StatusSkipped, StatusFailed, and any status added later.
	switch {
	case ev.Observed > 0:
		// A positive observation is sound whether or not the survey was complete, because
		// an incomplete survey can only undercount -- countPhrase says so out loud instead
		// of presenting a floor as a total.
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s) while %s secret-capable endpoint(s) are configured. %s",
			path, pbsNotificationStatusDetail(priv), ev.countPhrase(), consequence))

	case ev.Complete:
		// The ONLY shape of evidence that earns an affirmative sentence: every
		// secret-capable kind accounted for by a listing we actually read, and every one of
		// them empty. Naming the source makes the claim auditable from the JSON artifact.
		summary.Notes = append(summary.Notes, fmt.Sprintf(
			"%s was not collected (%s), but no smtp, gotify or webhook endpoint is configured (verified from %s), so no secret was lost.",
			path, pbsNotificationStatusDetail(priv), ev.sourcePhrase()))

	case len(ev.Verified) == 0:
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s) and no endpoint listing could be read, so whether any secret was lost is unknown. %s",
			path, pbsNotificationStatusDetail(priv), consequence))

	default:
		// WAS THE BUG. Partial evidence: state the partition, draw no conclusion, and carry
		// the same severity as the no-evidence arm above -- the question is equally
		// unanswered, and a single readable listing downgrading it to a Note is exactly the
		// defect. persisted() already states the mirror rule ("an origin we could not read
		// must not raise an alarm"); the missing half is that a listing we could not read
		// must not SILENCE one.
		summary.Warnings = append(summary.Warnings, fmt.Sprintf(
			"%s was not collected (%s). Endpoint evidence is incomplete: %s, so whether any secret was lost is unknown. %s",
			path, pbsNotificationStatusDetail(priv), ev.incompleteClause(), consequence))
	}
}

// appendPBSNotificationEvidenceNotes records listings that could not be captured or parsed,
// so that a quiet run is never mistaken for a verified-clean one.
func (c *Collector) appendPBSNotificationEvidenceNotes(summary *pbsNotificationsSummary) {
	if c.dryRun || !summary.Enabled {
		return
	}

	listings := []struct {
		name      string
		isWebhook bool
		snap      pbsNotificationSnapshotSummary
	}{
		{name: "targets", snap: summary.Targets},
		{name: "matchers", snap: summary.Matchers},
	}
	for _, typ := range pbsNotificationEndpointTypes {
		listings = append(listings, struct {
			name      string
			isWebhook bool
			snap      pbsNotificationSnapshotSummary
		}{
			name:      fmt.Sprintf("%s endpoint", typ),
			isWebhook: typ == "webhook",
			snap:      summary.Endpoints[typ],
		})
	}

	for _, l := range listings {
		switch {
		case !l.snap.Present && l.snap.Error != "":
			// Was reported with the generic "was not captured" text because the !Present
			// case came first, so the read error never reached the operator and survived
			// only inside the JSON artifact. A specific failure reported as a generic one
			// is the same defect class in miniature.
			summary.Notes = append(summary.Notes, fmt.Sprintf(
				"Notification %s listing could not be read (%s), so its contents are unknown.", l.name, l.snap.Error))
		case !l.snap.Present:
			note := fmt.Sprintf("Notification %s listing was not captured, so its contents are unknown.", l.name)
			// The PBS 3.2 remark belongs to webhook and to nothing else. Appending it to
			// the targets, matchers, smtp, sendmail and gotify notes attributed a
			// webhook-specific cause to five listings it cannot explain.
			if l.isWebhook {
				note += " On PBS 3.2 and older this is expected: there is no webhook list command there."
			}
			summary.Notes = append(summary.Notes, note)
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

// pbsSecretEvidence answers "could a secret have been lost" as evidence rather than as a
// conclusion, so the consumer can say what it actually knows.
//
// Observed is exact when Complete is true and a FLOOR when it is false: every source that
// contributes is a listing that parsed, and a listing we could not read can only hide
// endpoints, never invent them.
type pbsSecretEvidence struct {
	Observed int
	Complete bool     // every secret-capable kind was accounted for by a listing we read
	Verified []string // secret-capable kinds a readable listing accounted for
	Unknown  []string // secret-capable kinds nothing accounted for

	fromTargets   bool
	fromEndpoints bool
}

// pbsSecretCapableEndpointEvidence reports how many smtp, gotify and webhook endpoints were
// observed and, crucially, whether that count covers all three kinds.
//
// The previous version set a single evidenceUsable flag from ANY readable listing, so one
// readable-and-empty smtp listing licensed an affirmative claim about gotify and webhook.
// A missing map key was worse: the `!ok` branch skipped the kind outright, making "webhook
// was never captured" indistinguishable from "webhook was read and empty".
//
// The targets listing SUPPLEMENTS the per-kind listings and never overrides them. Taking
// the maximum per kind means a targets listing that under-reports can only ever fail to ADD
// evidence, never SUBTRACT it. That is the one direction that matters here, and it is the
// direction the old flag failed in.
//
// PBS 3.2 and older have no `notification endpoint webhook list` command (upstream
// src/api2/config/notifications/webhook.rs first appears 2024-11-26), so Endpoints["webhook"]
// is permanently unusable there. The targets union covers webhook there BY CONSTRUCTION --
// webhook endpoints cannot exist on 3.2, so they cannot be missing from the union -- which
// is why this reaches Complete without reading a version string.
func pbsSecretCapableEndpointEvidence(summary pbsNotificationsSummary) pbsSecretEvidence {
	unionCovers := pbsTargetsCoverEndpointKinds(summary.Targets)

	ev := pbsSecretEvidence{Complete: true}
	for _, typ := range pbsSecretCapableEndpointTypes {
		observed, covered := 0, false

		if s, ok := summary.Endpoints[typ]; ok && s.usable() {
			// Total is only assigned after every error return, so an unusable listing can
			// never contribute a count.
			observed = s.Total
			covered = true
			ev.fromEndpoints = true
		}
		if unionCovers {
			if n := summary.Targets.ByType[typ]; n > observed {
				observed = n
			}
			covered = true
			ev.fromTargets = true
		}

		ev.Observed += observed
		if covered {
			ev.Verified = append(ev.Verified, typ)
			continue
		}
		ev.Complete = false
		ev.Unknown = append(ev.Unknown, typ)
	}
	return ev
}

// pbsTargetsCoverEndpointKinds reports whether the targets listing may stand in for the
// per-kind endpoint listings. Every veto below closes a way the union could quietly
// under-report a secret-capable endpoint, which is the only failure direction that matters:
// this predicate is what licenses an affirmative "no secret was lost".
func pbsTargetsCoverEndpointKinds(targets pbsNotificationSnapshotSummary) bool {
	if !targets.usable() {
		return false
	}
	if targets.TypeMissing > 0 {
		// An entry we could not type might be the very endpoint we are about to deny.
		return false
	}
	if targets.Total == 0 {
		// PBS injects its pristine built-ins into every listing from a compiled-in default
		// set even when notifications.cfg does not exist, so a valid but empty targets
		// listing cannot occur on a live host. Fall back rather than build a reassurance on
		// a file we have ourselves declared impossible.
		return false
	}
	for typ := range targets.ByType {
		if !containsPBSString(pbsNotificationEndpointTypes, typ) {
			// A kind we do not know about may be secret-capable -- webhook itself was added
			// in PBS 3.3. Excluding it from the count while calling that count complete is
			// precisely the failure the old evidenceUsable flag made.
			return false
		}
	}
	return true
}

// countPhrase keeps the number honest: an incomplete survey can only ever floor the count.
func (e pbsSecretEvidence) countPhrase() string {
	if e.Complete {
		return fmt.Sprintf("%d", e.Observed)
	}
	return fmt.Sprintf("at least %d", e.Observed)
}

// sourcePhrase names the listing the affirmative claim rests on, so the sentence can be
// audited against notifications_summary.json.
func (e pbsSecretEvidence) sourcePhrase() string {
	switch {
	case e.fromTargets && e.fromEndpoints:
		return "the notification target and endpoint listings"
	case e.fromTargets:
		return "the notification target listing"
	default:
		return "the smtp, gotify and webhook endpoint listings"
	}
}

// incompleteClause states the evidence and draws no conclusion. It deliberately makes NO
// claim about WHY a listing is missing: proxsave cannot tell "the command does not exist on
// this PBS version" from "the command failed" from "an exclude pattern swallowed the output"
// -- captureCommandOutput discards the whole commandRunResult -- and several distinct causes
// converge on the same observable.
func (e pbsSecretEvidence) incompleteClause() string {
	return fmt.Sprintf("%s shows none configured, and no readable listing accounts for %s",
		joinPBSAnd(e.Verified), joinPBSOr(e.Unknown))
}

func containsPBSString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func joinPBSAnd(items []string) string { return joinPBSList(items, " and ") }
func joinPBSOr(items []string) string  { return joinPBSList(items, " or ") }

func joinPBSList(items []string, last string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + last + items[len(items)-1]
	}
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
		// proxmox-router prints "[]" for an empty list, so a zero-byte or whitespace-only
		// body is a truncated, half-written or redirected-to-nothing file -- never proof
		// that PBS has no objects of this kind. Returning it with Error=="" made usable()
		// true with Total 0, i.e. a zero-byte file read as positive evidence of absence.
		summary.Error = "empty output"
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
			// from the accounting, in either dimension.
			summary.OriginUnrecognized++
			summary.TypeMissing++
			continue
		}

		name := firstString(entry, "name", "id", "target", "matcher")
		if name != "" {
			names = append(names, name)
		}

		// Exact match, lowercased, same discipline as the origin vocabulary below.
		if typ := strings.ToLower(strings.TrimSpace(firstString(entry, "type"))); typ != "" {
			if summary.ByType == nil {
				summary.ByType = make(map[string]int)
			}
			summary.ByType[typ]++
		} else {
			summary.TypeMissing++
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
