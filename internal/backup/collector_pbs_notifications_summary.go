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

type pbsNotificationSnapshotSummary struct {
	Present  bool     `json:"present"`
	Bytes    int64    `json:"bytes,omitempty"`
	Total    int      `json:"total,omitempty"`
	BuiltIn  int      `json:"built_in,omitempty"`
	Custom   int      `json:"custom,omitempty"`
	Names    []string `json:"names,omitempty"`
	Error    string   `json:"error,omitempty"`
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
	for _, typ := range []string{"smtp", "sendmail", "gotify", "webhook"} {
		summary.Endpoints[typ] = summarizePBSNotificationSnapshot(filepath.Join(commandsDir, fmt.Sprintf("notification_endpoints_%s.json", typ)))
	}

	if summary.ConfigFiles != nil {
		cfg := summary.ConfigFiles.NotificationsCfg
		priv := summary.ConfigFiles.NotificationsPrivCfg

		if cfg.Status != StatusCollected && cfg.Status != StatusDisabled {
			if summary.Targets.Total > 0 || sumEndpointTotals(summary.Endpoints) > 0 {
				summary.Warnings = append(summary.Warnings, "Notification objects detected in snapshots, but notifications.cfg was not collected (check BACKUP_PBS_NOTIFICATIONS and exclusions).")
			}
		}

		if priv.Status == StatusDisabled {
			summary.Notes = append(summary.Notes, "notifications-priv.cfg backup is disabled (BACKUP_PBS_NOTIFICATIONS_PRIV=false); endpoint credentials/secrets will not be included.")
		} else if priv.Status != StatusCollected {
			if summary.Targets.Custom > 0 || sumEndpointCustom(summary.Endpoints) > 0 {
				summary.Warnings = append(summary.Warnings, "Custom notification endpoints/targets detected, but notifications-priv.cfg was not collected; restore may require re-entering secrets/credentials.")
			}
		}
	}

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

func sumEndpointTotals(endpoints map[string]pbsNotificationSnapshotSummary) int {
	total := 0
	for _, s := range endpoints {
		total += s.Total
	}
	return total
}

func sumEndpointCustom(endpoints map[string]pbsNotificationSnapshotSummary) int {
	total := 0
	for _, s := range endpoints {
		total += s.Custom
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
			continue
		}

		name := firstString(entry, "name", "id", "target", "matcher")
		if name != "" {
			names = append(names, name)
		}

		origin := strings.ToLower(strings.TrimSpace(firstString(entry, "origin")))
		switch {
		case strings.Contains(origin, "built"):
			summary.BuiltIn++
		case strings.Contains(origin, "custom"):
			summary.Custom++
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
