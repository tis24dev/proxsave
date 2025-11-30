package notify

import (
	"fmt"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
)

// buildDiscordPayload builds a Discord-formatted webhook payload with embeds
func buildDiscordPayload(data *NotificationData, logger *logging.Logger) (map[string]interface{}, error) {
	logger.Debug("buildDiscordPayload() starting...")

	// Determine embed color based on status
	var color int
	switch data.Status {
	case StatusSuccess:
		color = 3066993 // Green
		logger.Debug("Status: success, color: %d (green)", color)
	case StatusWarning:
		color = 16753920 // Orange
		logger.Debug("Status: warning, color: %d (orange)", color)
	case StatusFailure:
		color = 15158332 // Red
		logger.Debug("Status: failure, color: %d (red)", color)
	default:
		color = 9807270 // Gray
		logger.Debug("Status: unknown, color: %d (gray)", color)
	}

	// Build title with emoji
	statusEmoji := GetStatusEmoji(data.Status)
	proxmoxType := strings.ToUpper(data.ProxmoxType.String())
	title := fmt.Sprintf("%s %s Backup Report", statusEmoji, proxmoxType)
	logger.Debug("Title: %s", title)

	// Build description
	description := fmt.Sprintf("Backup completed with status: **%s** on %s",
		data.Status.String(), data.Hostname)
	logger.Debug("Description: %s", description)

	// Build fields array
	fields := []map[string]interface{}{}

	// Status and basic info
	fields = append(fields,
		map[string]interface{}{
			"name":   "Hostname",
			"value":  data.Hostname,
			"inline": true,
		},
		map[string]interface{}{
			"name":   "Status",
			"value":  fmt.Sprintf("%s %s", statusEmoji, data.Status.String()),
			"inline": true,
		},
		map[string]interface{}{
			"name":   "Date",
			"value":  data.BackupDate.Format("2006-01-02 15:04:05"),
			"inline": true,
		},
	)

	// Backup metrics
	fields = append(fields,
		map[string]interface{}{
			"name":   "Duration",
			"value":  FormatDuration(data.BackupDuration),
			"inline": true,
		},
		map[string]interface{}{
			"name":   "Size",
			"value":  data.BackupSizeHR,
			"inline": true,
		},
		map[string]interface{}{
			"name":   "Compression",
			"value":  fmt.Sprintf("%s (%.2f%%)", data.CompressionType, data.CompressionRatio),
			"inline": true,
		},
	)

	// Storage status
	localStorage := fmt.Sprintf("%s %s", GetStorageEmoji(data.LocalStatus), data.LocalStatusSummary)
	fields = append(fields, map[string]interface{}{
		"name":   "Local Storage",
		"value":  localStorage,
		"inline": true,
	})

	if data.SecondaryEnabled {
		secondaryStorage := fmt.Sprintf("%s %s", GetStorageEmoji(data.SecondaryStatus), data.SecondaryStatusSummary)
		fields = append(fields, map[string]interface{}{
			"name":   "Secondary Storage",
			"value":  secondaryStorage,
			"inline": true,
		})
	}

	if data.CloudEnabled {
		cloudStorage := fmt.Sprintf("%s %s", GetStorageEmoji(data.CloudStatus), data.CloudStatusSummary)
		fields = append(fields, map[string]interface{}{
			"name":   "Cloud Storage",
			"value":  cloudStorage,
			"inline": true,
		})
	}

	// Issues summary
	issuesSummary := fmt.Sprintf("Errors: %d, Warnings: %d", data.ErrorCount, data.WarningCount)
	fields = append(fields, map[string]interface{}{
		"name":   "Issues",
		"value":  issuesSummary,
		"inline": false,
	})

	logger.Debug("Built %d fields for Discord embed", len(fields))

	// Build embed
	embed := map[string]interface{}{
		"title":       title,
		"description": description,
		"color":       color,
		"fields":      fields,
		"footer": map[string]interface{}{
			"text": fmt.Sprintf("Proxmox Backup Script v%s • Exit Code: %d", data.ScriptVersion, data.ExitCode),
		},
		"timestamp": data.BackupDate.Format("2006-01-02T15:04:05Z07:00"),
	}

	// Add log categories if present
	if len(data.LogCategories) > 0 {
		logger.Debug("Adding %d log categories to embed", len(data.LogCategories))
		categoriesText := "```\n"
		for i, cat := range data.LogCategories {
			if i >= 5 { // Limit to first 5 categories
				categoriesText += fmt.Sprintf("... and %d more\n", len(data.LogCategories)-5)
				break
			}
			categoriesText += fmt.Sprintf("[%s] %s (count: %d)\n", cat.Type, cat.Label, cat.Count)
		}
		categoriesText += "```"

		// Insert after issues field
		fields = append(fields, map[string]interface{}{
			"name":   "Top Issues",
			"value":  categoriesText,
			"inline": false,
		})
		embed["fields"] = fields
	}

	payload := map[string]interface{}{
		"embeds": []interface{}{embed},
	}

	logger.Debug("Discord payload built successfully with 1 embed and %d fields", len(fields))
	return payload, nil
}

// buildSlackPayload builds a Slack-formatted webhook payload with blocks
func buildSlackPayload(data *NotificationData, logger *logging.Logger) (map[string]interface{}, error) {
	logger.Debug("buildSlackPayload() starting...")

	statusEmoji := GetStatusEmoji(data.Status)
	proxmoxType := strings.ToUpper(data.ProxmoxType.String())
	headerText := fmt.Sprintf("%s %s Backup Report", statusEmoji, proxmoxType)
	logger.Debug("Header text: %s", headerText)

	blocks := []interface{}{}

	// Header block
	blocks = append(blocks, map[string]interface{}{
		"type": "header",
		"text": map[string]interface{}{
			"type": "plain_text",
			"text": headerText,
		},
	})

	// Status section
	blocks = append(blocks, map[string]interface{}{
		"type": "section",
		"fields": []interface{}{
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Hostname:*\n%s", data.Hostname),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Status:*\n%s %s", statusEmoji, data.Status.String()),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Date:*\n%s", data.BackupDate.Format("2006-01-02 15:04:05")),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Duration:*\n%s", FormatDuration(data.BackupDuration)),
			},
		},
	})

	// Divider
	blocks = append(blocks, map[string]interface{}{
		"type": "divider",
	})

	// Backup details section
	blocks = append(blocks, map[string]interface{}{
		"type": "section",
		"fields": []interface{}{
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Size:*\n%s", data.BackupSizeHR),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Compression:*\n%s (%.2f%%)", data.CompressionType, data.CompressionRatio),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Files:*\n%d included", data.FilesIncluded),
			},
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("*Exit Code:*\n%d", data.ExitCode),
			},
		},
	})

	// Divider
	blocks = append(blocks, map[string]interface{}{
		"type": "divider",
	})

	// Storage section
	storageFields := []interface{}{
		map[string]interface{}{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Local Storage:*\n%s %s", GetStorageEmoji(data.LocalStatus), data.LocalStatusSummary),
		},
	}

	if data.SecondaryEnabled {
		storageFields = append(storageFields, map[string]interface{}{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Secondary Storage:*\n%s %s", GetStorageEmoji(data.SecondaryStatus), data.SecondaryStatusSummary),
		})
	}

	if data.CloudEnabled {
		storageFields = append(storageFields, map[string]interface{}{
			"type": "mrkdwn",
			"text": fmt.Sprintf("*Cloud Storage:*\n%s %s", GetStorageEmoji(data.CloudStatus), data.CloudStatusSummary),
		})
	}

	blocks = append(blocks, map[string]interface{}{
		"type":   "section",
		"fields": storageFields,
	})

	// Issues section
	if data.ErrorCount > 0 || data.WarningCount > 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "divider",
		})

		issuesText := fmt.Sprintf("*Errors:* %d | *Warnings:* %d", data.ErrorCount, data.WarningCount)

		if len(data.LogCategories) > 0 {
			issuesText += "\n\n*Top Issues:*\n"
			for i, cat := range data.LogCategories {
				if i >= 5 { // Limit to first 5
					issuesText += fmt.Sprintf("_... and %d more_\n", len(data.LogCategories)-5)
					break
				}
				issuesText += fmt.Sprintf("• [%s] %s (×%d)\n", cat.Type, cat.Label, cat.Count)
			}
		}

		blocks = append(blocks, map[string]interface{}{
			"type": "section",
			"text": map[string]interface{}{
				"type": "mrkdwn",
				"text": issuesText,
			},
		})
	}

	// Footer context
	blocks = append(blocks, map[string]interface{}{
		"type": "context",
		"elements": []interface{}{
			map[string]interface{}{
				"type": "mrkdwn",
				"text": fmt.Sprintf("Proxmox Backup Script v%s", data.ScriptVersion),
			},
		},
	})

	payload := map[string]interface{}{
		"blocks": blocks,
	}

	logger.Debug("Slack payload built successfully with %d blocks", len(blocks))
	return payload, nil
}

// buildTeamsPayload builds a Microsoft Teams-formatted webhook payload with Adaptive Cards
func buildTeamsPayload(data *NotificationData, logger *logging.Logger) (map[string]interface{}, error) {
	logger.Debug("buildTeamsPayload() starting...")

	statusEmoji := GetStatusEmoji(data.Status)
	proxmoxType := strings.ToUpper(data.ProxmoxType.String())
	title := fmt.Sprintf("%s %s Backup Report", statusEmoji, proxmoxType)
	logger.Debug("Title: %s", title)

	// Determine theme color
	var themeColor string
	switch data.Status {
	case StatusSuccess:
		themeColor = "00FF00" // Green
	case StatusWarning:
		themeColor = "FFA500" // Orange
	case StatusFailure:
		themeColor = "FF0000" // Red
	default:
		themeColor = "808080" // Gray
	}
	logger.Debug("Theme color: #%s", themeColor)

	// Build facts for FactSet
	facts := []map[string]interface{}{
		{"title": "Hostname", "value": data.Hostname},
		{"title": "Status", "value": fmt.Sprintf("%s %s", statusEmoji, data.Status.String())},
		{"title": "Date", "value": data.BackupDate.Format("2006-01-02 15:04:05")},
		{"title": "Duration", "value": FormatDuration(data.BackupDuration)},
		{"title": "Size", "value": data.BackupSizeHR},
		{"title": "Compression", "value": fmt.Sprintf("%s (level %d, ratio %.2f%%)",
			data.CompressionType, data.CompressionLevel, data.CompressionRatio)},
		{"title": "Files Included", "value": fmt.Sprintf("%d", data.FilesIncluded)},
		{"title": "Local Storage", "value": fmt.Sprintf("%s %s", GetStorageEmoji(data.LocalStatus), data.LocalStatusSummary)},
	}

	if data.SecondaryEnabled {
		facts = append(facts, map[string]interface{}{
			"title": "Secondary Storage",
			"value": fmt.Sprintf("%s %s", GetStorageEmoji(data.SecondaryStatus), data.SecondaryStatusSummary),
		})
	}

	if data.CloudEnabled {
		facts = append(facts, map[string]interface{}{
			"title": "Cloud Storage",
			"value": fmt.Sprintf("%s %s", GetStorageEmoji(data.CloudStatus), data.CloudStatusSummary),
		})
	}

	facts = append(facts,
		map[string]interface{}{"title": "Errors", "value": fmt.Sprintf("%d", data.ErrorCount)},
		map[string]interface{}{"title": "Warnings", "value": fmt.Sprintf("%d", data.WarningCount)},
		map[string]interface{}{"title": "Exit Code", "value": fmt.Sprintf("%d", data.ExitCode)},
	)

	logger.Debug("Built %d facts for Teams FactSet", len(facts))

	// Build Adaptive Card body
	body := []interface{}{
		map[string]interface{}{
			"type":   "TextBlock",
			"text":   title,
			"weight": "bolder",
			"size":   "large",
		},
		map[string]interface{}{
			"type":  "TextBlock",
			"text":  fmt.Sprintf("Backup completed with status: **%s** on %s", data.Status.String(), data.Hostname),
			"wrap":  true,
			"style": "default",
		},
		map[string]interface{}{
			"type":  "FactSet",
			"facts": facts,
		},
	}

	// Add log categories if present
	if len(data.LogCategories) > 0 {
		logger.Debug("Adding %d log categories to Teams card", len(data.LogCategories))
		categoriesText := "**Top Issues:**\n\n"
		for i, cat := range data.LogCategories {
			if i >= 5 { // Limit to first 5
				categoriesText += fmt.Sprintf("_... and %d more_", len(data.LogCategories)-5)
				break
			}
			categoriesText += fmt.Sprintf("• [%s] %s (×%d)\n\n", cat.Type, cat.Label, cat.Count)
		}

		body = append(body, map[string]interface{}{
			"type": "TextBlock",
			"text": categoriesText,
			"wrap": true,
		})
	}

	// Build Adaptive Card
	adaptiveCard := map[string]interface{}{
		"type":    "AdaptiveCard",
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"version": "1.5",
		"body":    body,
	}

	// Wrap in Teams message format
	payload := map[string]interface{}{
		"type": "message",
		"attachments": []interface{}{
			map[string]interface{}{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"content":     adaptiveCard,
			},
		},
		"themeColor": themeColor,
	}

	logger.Debug("Teams payload built successfully with Adaptive Card v1.5 and %d facts", len(facts))
	return payload, nil
}

// buildGenericPayload builds a generic JSON webhook payload
func buildGenericPayload(data *NotificationData, logger *logging.Logger) (map[string]interface{}, error) {
	logger.Debug("buildGenericPayload() starting...")

	payload := map[string]interface{}{
		// Status information
		"status":         data.Status.String(),
		"status_message": data.StatusMessage,
		"status_emoji":   GetStatusEmoji(data.Status),
		"exit_code":      data.ExitCode,

		// System information
		"hostname":       data.Hostname,
		"proxmox_type":   data.ProxmoxType.String(),
		"server_id":      data.ServerID,
		"server_mac":     data.ServerMAC,
		"script_version": data.ScriptVersion,

		// Timestamp
		"timestamp":     data.BackupDate.Unix(),
		"timestamp_iso": data.BackupDate.Format("2006-01-02T15:04:05Z07:00"),

		// Backup metrics
		"backup": map[string]interface{}{
			"file_name":        data.BackupFileName,
			"size_bytes":       data.BackupSize,
			"size_human":       data.BackupSizeHR,
			"duration_seconds": data.BackupDuration.Seconds(),
			"duration_human":   FormatDuration(data.BackupDuration),
			"files_included":   data.FilesIncluded,
			"files_missing":    data.FilesMissing,
		},

		// Compression details
		"compression": map[string]interface{}{
			"type":  data.CompressionType,
			"level": data.CompressionLevel,
			"mode":  data.CompressionMode,
			"ratio": data.CompressionRatio,
		},

		// Storage status
		"storage": map[string]interface{}{
			"local": map[string]interface{}{
				"status":         data.LocalStatus,
				"status_summary": data.LocalStatusSummary,
				"emoji":          GetStorageEmoji(data.LocalStatus),
				"count":          data.LocalCount,
				"free":           data.LocalFree,
				"used":           data.LocalUsed,
				"percent":        data.LocalPercent,
				"percent_num":    data.LocalUsagePercent,
			},
		},

		// Issues summary
		"issues": map[string]interface{}{
			"errors":   data.ErrorCount,
			"warnings": data.WarningCount,
			"total":    data.TotalIssues,
		},
	}

	storage, ok := payload["storage"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("internal error: storage payload malformed")
	}

	// Add secondary storage if enabled
	if data.SecondaryEnabled {
		storage["secondary"] = map[string]interface{}{
			"status":         data.SecondaryStatus,
			"status_summary": data.SecondaryStatusSummary,
			"emoji":          GetStorageEmoji(data.SecondaryStatus),
			"count":          data.SecondaryCount,
			"free":           data.SecondaryFree,
			"used":           data.SecondaryUsed,
			"percent":        data.SecondaryPercent,
			"percent_num":    data.SecondaryUsagePercent,
		}
		logger.Debug("Secondary storage added to generic payload")
	}

	// Add cloud storage if enabled
	if data.CloudEnabled {
		storage["cloud"] = map[string]interface{}{
			"status":         data.CloudStatus,
			"status_summary": data.CloudStatusSummary,
			"emoji":          GetStorageEmoji(data.CloudStatus),
			"count":          data.CloudCount,
		}
		logger.Debug("Cloud storage added to generic payload")
	}

	// Add log categories if present
	if len(data.LogCategories) > 0 {
		categories := make([]map[string]interface{}, 0, len(data.LogCategories))
		for _, cat := range data.LogCategories {
			categories = append(categories, map[string]interface{}{
				"type":    cat.Type,
				"label":   cat.Label,
				"count":   cat.Count,
				"example": cat.Example,
			})
		}
		payload["log_categories"] = categories
		logger.Debug("Added %d log categories to generic payload", len(categories))
	}

	logger.Debug("Generic payload built successfully with %d top-level keys", len(payload))
	return payload, nil
}
