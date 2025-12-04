package notify

import (
	"fmt"
	"html"
	"strings"
)

// BuildEmailSubject builds the email subject line matching Bash output
func BuildEmailSubject(data *NotificationData) string {
	statusEmoji := GetStatusEmoji(data.Status)

	proxmoxType := strings.ToUpper(data.ProxmoxType.String())
	timestamp := data.BackupDate.Format("2006-01-02 15:04")
	return fmt.Sprintf("%s %s Backup on %s - %s", statusEmoji, proxmoxType, data.Hostname, timestamp)
}

// BuildEmailPlainText builds a plain text email body
func BuildEmailPlainText(data *NotificationData) string {
	var body strings.Builder

	statusEmoji := GetStatusEmoji(data.Status)
	body.WriteString(fmt.Sprintf("%s %s BACKUP REPORT - %s\n",
		statusEmoji, strings.ToUpper(data.ProxmoxType.String()), strings.ToUpper(data.Status.String())))
	body.WriteString(fmt.Sprintf("Hostname: %s\n", data.Hostname))
	body.WriteString(fmt.Sprintf("Date: %s\n\n", data.BackupDate.Format("2006-01-02 15:04:05")))

	body.WriteString("BACKUP STATUS:\n")
	body.WriteString(fmt.Sprintf("  Local:     %s backups (%s free)\n", data.LocalStatusSummary, data.LocalFree))
	if data.SecondaryEnabled {
		body.WriteString(fmt.Sprintf("  Secondary: %s backups (%s free)\n", data.SecondaryStatusSummary, data.SecondaryFree))
	}
	if data.CloudEnabled {
		body.WriteString(fmt.Sprintf("  Cloud:     %s backups\n", data.CloudStatusSummary))
	}
	body.WriteString("\n")

	body.WriteString("BACKUP DETAILS:\n")
	body.WriteString(fmt.Sprintf("  Backup File: %s\n", data.BackupFile))
	body.WriteString(fmt.Sprintf("  Size: %s\n", data.BackupSizeHR))
	body.WriteString(fmt.Sprintf("  Included Files: %d\n", data.FilesIncluded))
	body.WriteString(fmt.Sprintf("  Missing Files: %d\n", data.FilesMissing))
	body.WriteString(fmt.Sprintf("  Duration: %s\n", FormatDuration(data.BackupDuration)))
	body.WriteString(fmt.Sprintf("  Compression: %s (level %d, ratio %.2f%%)\n",
		data.CompressionType, data.CompressionLevel, data.CompressionRatio))
	body.WriteString("\n")

	body.WriteString("ISSUES:\n")
	body.WriteString(fmt.Sprintf("  Errors: %d\n", data.ErrorCount))
	body.WriteString(fmt.Sprintf("  Warnings: %d\n", data.WarningCount))
	body.WriteString(fmt.Sprintf("  Total Issues: %d\n", data.TotalIssues))
	if data.LogFilePath != "" {
		body.WriteString(fmt.Sprintf("  Log: %s\n", data.LogFilePath))
	}
	body.WriteString("\n")

	if len(data.LogCategories) > 0 {
		body.WriteString("ISSUE DETAILS:\n")
		for _, cat := range data.LogCategories {
			body.WriteString(fmt.Sprintf("  - [%s] %s (count: %d)\n", cat.Type, cat.Label, cat.Count))
			if cat.Example != "" {
				body.WriteString(fmt.Sprintf("    Example: %s\n", cat.Example))
			}
		}
		body.WriteString("\n")
	}

	body.WriteString(fmt.Sprintf("Exit Code: %d\n", data.ExitCode))
	body.WriteString(fmt.Sprintf("Script Version: %s\n", data.ScriptVersion))

	return body.String()
}

// BuildEmailHTML builds an HTML email body matching Bash template exactly
func BuildEmailHTML(data *NotificationData) string {
	// Determine status color
	statusColor := getStatusColor(data.Status)
	statusText := strings.ToUpper(data.Status.String())
	proxmoxType := strings.ToUpper(data.ProxmoxType.String())

	// Determine backup paths sidebar color
	backupPathsColor := "#4CAF50" // Green by default
	if data.LocalStatus == "error" || data.SecondaryStatus == "error" || data.CloudStatus == "error" {
		backupPathsColor = "#F44336" // Red
	} else if data.LocalStatus == "warning" || data.SecondaryStatus == "warning" || data.CloudStatus == "warning" {
		backupPathsColor = "#FF9800" // Orange
	}

	// Determine error summary sidebar color
	errorSummaryColor := "#4CAF50" // Green by default
	if data.ErrorCount > 0 {
		errorSummaryColor = "#F44336" // Red
	} else if data.WarningCount > 0 {
		errorSummaryColor = "#FF9800" // Orange
	}

	// Build HTML
	var html strings.Builder

	// DOCTYPE and head
	html.WriteString("<!DOCTYPE html>\n")
	html.WriteString("<html>\n<head>\n")
	html.WriteString("    <meta charset=\"UTF-8\">\n")
	html.WriteString(fmt.Sprintf("    <title>%s Backup Report</title>\n", proxmoxType))
	html.WriteString("    <style>\n")
	html.WriteString(getEmbeddedCSS())
	html.WriteString("    </style>\n")
	html.WriteString("</head>\n<body>\n")

	// Container
	html.WriteString("    <div class=\"container\">\n")

	// Header
	html.WriteString(fmt.Sprintf("        <div class=\"header\" style=\"background-color: %s;\">\n", statusColor))
	html.WriteString(fmt.Sprintf("            <h1>%s Backup Report - %s</h1>\n", proxmoxType, statusText))
	html.WriteString(fmt.Sprintf("            <p>%s - %s</p>\n", data.Hostname, data.BackupDate.Format("2006-01-02 15:04:05")))
	html.WriteString("        </div>\n")

	// Content
	html.WriteString("        <div class=\"content\">\n")

	// Backup Status Section
	html.WriteString(fmt.Sprintf("            <div class=\"backup-status\" style=\"border-left-color: %s;\">\n", backupPathsColor))

	// Local Storage
	html.WriteString("                <div class=\"backup-location\">\n")
	html.WriteString("                    <h3>Local Storage</h3>\n")
	html.WriteString("                    <div class=\"count-block\">\n")
	html.WriteString(fmt.Sprintf("                        <span class=\"emoji\">%s</span> %s backups\n", GetStorageEmoji(data.LocalStatus), data.LocalStatusSummary))
	html.WriteString("                    </div>\n")
	if data.LocalFree != "" && data.LocalFree != "N/A" {
		barColor := "normal"
		if data.LocalUsagePercent > 85 {
			barColor = "critical"
		} else if data.LocalUsagePercent > 70 {
			barColor = "warning"
		}
		html.WriteString("                    <div class=\"storage-info\">\n")
		html.WriteString(fmt.Sprintf("                        <span>%s</span>\n", data.LocalUsed))
		html.WriteString("                        <div class=\"space-bar\">\n")
		html.WriteString(fmt.Sprintf("                            <div class=\"space-used %s\" style=\"width: %.1f%%;\"></div>\n", barColor, data.LocalUsagePercent))
		html.WriteString("                        </div>\n")
		html.WriteString(fmt.Sprintf("                        <span>%s free (%s used)</span>\n", data.LocalFree, data.LocalPercent))
		html.WriteString("                    </div>\n")
	}
	html.WriteString("                </div>\n")

	// Secondary Storage
	html.WriteString("                \n")
	html.WriteString("                <div class=\"backup-location\">\n")
	html.WriteString("                    <h3>Secondary Storage</h3>\n")
	html.WriteString("                    <div class=\"count-block\">\n")
	html.WriteString(fmt.Sprintf("                        <span class=\"emoji\">%s</span> %s backups\n", GetStorageEmoji(data.SecondaryStatus), data.SecondaryStatusSummary))
	html.WriteString("                    </div>\n")
	if data.SecondaryEnabled && data.SecondaryFree != "" && data.SecondaryFree != "N/A" {
		barColor := "normal"
		if data.SecondaryUsagePercent > 85 {
			barColor = "critical"
		} else if data.SecondaryUsagePercent > 70 {
			barColor = "warning"
		}
		html.WriteString("                    <div class=\"storage-info\">\n")
		html.WriteString(fmt.Sprintf("                        <span>%s</span>\n", data.SecondaryUsed))
		html.WriteString("                        <div class=\"space-bar\">\n")
		html.WriteString(fmt.Sprintf("                            <div class=\"space-used %s\" style=\"width: %.1f%%;\"></div>\n", barColor, data.SecondaryUsagePercent))
		html.WriteString("                        </div>\n")
		html.WriteString(fmt.Sprintf("                        <span>%s free (%s used)</span>\n", data.SecondaryFree, data.SecondaryPercent))
		html.WriteString("                    </div>\n")
	}
	html.WriteString("                </div>\n")

	// Cloud Storage
	html.WriteString("                \n")
	html.WriteString("                <div class=\"backup-location\">\n")
	html.WriteString("                    <h3>Cloud Storage</h3>\n")
	html.WriteString("                    <div class=\"count-block\">\n")
	html.WriteString(fmt.Sprintf("                        <span class=\"emoji\">%s</span> %s backups\n", GetStorageEmoji(data.CloudStatus), data.CloudStatusSummary))
	html.WriteString("                    </div>\n")
	html.WriteString("                </div>\n")

	html.WriteString("            </div>\n")

	// Backup Details Section
	html.WriteString("            \n")
	html.WriteString("            <div class=\"section\">\n")
	html.WriteString("                <h2>Backup Details</h2>\n")
	html.WriteString("                <table class=\"info-table\">\n")
	html.WriteString(buildInfoTableRow("Backup File", data.BackupFile))
	html.WriteString(buildInfoTableRow("File Size", data.BackupSizeHR))
	html.WriteString(buildInfoTableRow("Included Files", fmt.Sprintf("%d", data.FilesIncluded)))
	html.WriteString(buildInfoTableRow("Missing Files", fmt.Sprintf("%d", data.FilesMissing)))
	html.WriteString(buildInfoTableRow("Duration", FormatDuration(data.BackupDuration)))
	html.WriteString(buildInfoTableRow("Compression Ratio", fmt.Sprintf("%.2f%%", data.CompressionRatio)))
	html.WriteString(buildInfoTableRow("Compression Type", fmt.Sprintf("%s (level: %d)", data.CompressionType, data.CompressionLevel)))
	html.WriteString(buildInfoTableRow("Backup Mode", valueOrNA(data.CompressionMode)))
	html.WriteString(buildInfoTableRow("Server MAC Address", data.ServerMAC))
	html.WriteString(buildInfoTableRow("Server ID", data.ServerID))
	html.WriteString(buildInfoTableRow("Telegram Status", valueOrNA(data.TelegramStatus)))
	html.WriteString(buildInfoTableRow("Local Path", valueOrNA(data.LocalPath)))
	if data.SecondaryEnabled && data.SecondaryPath != "" {
		html.WriteString(buildInfoTableRow("Secondary Path", data.SecondaryPath))
	}
	if data.CloudEnabled && data.CloudPath != "" {
		html.WriteString(buildInfoTableRow("Cloud Storage", data.CloudPath))
	}
	html.WriteString("                </table>\n")
	html.WriteString("            </div>\n")

	// Error/Warning Section
	html.WriteString("            \n")
	html.WriteString("            <div class=\"section\">\n")
	html.WriteString("                <h2>Error and Warning Summary</h2>\n")
	html.WriteString(fmt.Sprintf("                <div style=\"padding:15px; background-color:#F5F5F5; border-radius:6px; margin-bottom:15px; border-left:4px solid %s;\">\n", errorSummaryColor))
	html.WriteString(fmt.Sprintf("                    <p><strong>Total Issues:</strong> %d</p>\n", data.TotalIssues))
	html.WriteString(fmt.Sprintf("                    <p><strong>Errors:</strong> %d</p>\n", data.ErrorCount))
	html.WriteString(fmt.Sprintf("                    <p><strong>Warnings:</strong> %d</p>\n", data.WarningCount))
	html.WriteString("                </div>\n")

	if len(data.LogCategories) > 0 {
		html.WriteString("                <table class=\"info-table\">\n")
		html.WriteString("                    <tr>\n")
		html.WriteString("                        <th style=\"text-align:left; padding:10px; background-color:#f2f2f2;\">Problem</th>\n")
		html.WriteString("                        <th style=\"text-align:left; padding:10px; background-color:#f2f2f2;\">Type</th>\n")
		html.WriteString("                        <th style=\"text-align:left; padding:10px; background-color:#f2f2f2;\">Count</th>\n")
		html.WriteString("                    </tr>\n")
		for _, cat := range data.LogCategories {
			html.WriteString("                    <tr>\n")
			html.WriteString(fmt.Sprintf("                        <td>%s</td>\n", escapeHTML(cat.Label)))
			html.WriteString(fmt.Sprintf("                        <td>%s</td>\n", escapeHTML(cat.Type)))
			html.WriteString(fmt.Sprintf("                        <td>%d</td>\n", cat.Count))
			html.WriteString("                    </tr>\n")
		}
		html.WriteString("                </table>\n")
	}

	// Show log file path after the table
	if data.LogFilePath != "" {
		html.WriteString(fmt.Sprintf("                <p style=\"font-size:13px; color:#666; margin-top:10px;\">Full log available at: %s</p>\n", escapeHTML(data.LogFilePath)))
	}
	html.WriteString("            </div>\n")

	// System Recommendations Section
	if data.LocalUsagePercent > 85 || (data.SecondaryEnabled && data.SecondaryUsagePercent > 85) {
		html.WriteString("            \n")
		html.WriteString("            <div class=\"section\">\n")
		html.WriteString("                <h2>System Recommendations</h2>\n")
		html.WriteString("                <div style=\"padding:15px; background-color:#FFF3E0; border-radius:6px; border-left:4px solid #FF9800;\">\n")
		if data.LocalUsagePercent > 85 {
			html.WriteString(fmt.Sprintf("                    <p>⚠️ <strong>Local storage is %.1f%% full.</strong> Consider cleaning old backups or expanding storage capacity.</p>\n", data.LocalUsagePercent))
		}
		if data.SecondaryEnabled && data.SecondaryUsagePercent > 85 {
			html.WriteString(fmt.Sprintf("                    <p>⚠️ <strong>Secondary storage is %.1f%% full.</strong> Consider cleaning old backups or expanding storage capacity.</p>\n", data.SecondaryUsagePercent))
		}
		html.WriteString("                </div>\n")
		html.WriteString("            </div>\n")
	}

	// Footer
	html.WriteString("        </div>\n")
	html.WriteString("        <div class=\"footer\">\n")
	html.WriteString("            <p>This is an automated message from the Proxmox Backup Script.</p>\n")
	html.WriteString(fmt.Sprintf("            <p>Generated on %s by backup script v%s</p>\n", data.BackupDate.Format("2006-01-02 15:04:05"), data.ScriptVersion))
	html.WriteString("        </div>\n")

	html.WriteString("    </div>\n")
	html.WriteString("</body>\n</html>")

	return html.String()
}

// buildInfoTableRow builds a table row for the info table (Bash style)
func buildInfoTableRow(label, value string) string {
	return fmt.Sprintf("                    <tr>\n                        <td>%s</td>\n                        <td>%s</td>\n                    </tr>\n", escapeHTML(label), escapeHTML(value))
}

func valueOrNA(value string) string {
	if strings.TrimSpace(value) == "" {
		return "N/A"
	}
	return value
}

func escapeHTML(value string) string {
	return html.EscapeString(value)
}

// getStatusColor returns the color for a given status
func getStatusColor(status NotificationStatus) string {
	switch status {
	case StatusSuccess:
		return "#4CAF50" // Green
	case StatusWarning:
		return "#FF9800" // Orange
	case StatusFailure:
		return "#F44336" // Red
	default:
		return "#9E9E9E" // Gray
	}
}

// getEmbeddedCSS returns the embedded CSS for email HTML (Bash style)
func getEmbeddedCSS() string {
	return `        body {
            font-family: 'Segoe UI', Arial, sans-serif;
            margin: 0;
            padding: 0;
            color: #333;
            background-color: #f5f5f5;
        }
        .container {
            max-width: 800px;
            margin: 0 auto;
            background-color: #fff;
            border-radius: 8px;
            overflow: hidden;
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        .header {
            color: white;
            padding: 20px 30px;
        }
        .header h1 {
            margin: 0;
            font-weight: 500;
            font-size: 24px;
        }
        .header p {
            margin: 10px 0 0 0;
            opacity: 0.9;
            font-size: 16px;
        }
        .content {
            padding: 30px;
        }
        .section {
            margin-bottom: 30px;
        }
        .section:last-child {
            margin-bottom: 0;
        }
        .section h2 {
            font-size: 18px;
            font-weight: 500;
            margin-top: 0;
            margin-bottom: 15px;
            padding-bottom: 10px;
            border-bottom: 1px solid #eee;
            color: #444;
        }
        .info-table {
            width: 100%;
            border-collapse: collapse;
            margin-bottom: 10px;
        }
        .info-table td {
            padding: 10px;
            border-bottom: 1px solid #eee;
            vertical-align: top;
        }
        .info-table tr:last-child td {
            border-bottom: none;
        }
        .info-table td:first-child {
            font-weight: 500;
            width: 35%;
            color: #555;
        }
        .footer {
            background-color: #f8f8f8;
            padding: 15px 30px;
            text-align: center;
            font-size: 13px;
            color: #777;
            border-top: 1px solid #eee;
        }
        .backup-status {
            background-color: #f9f9f9;
            border-radius: 8px;
            padding: 20px;
            margin-bottom: 30px;
            border-left: 4px solid;
            box-shadow: 0 2px 5px rgba(0,0,0,0.05);
        }
        .backup-location {
            margin-bottom: 15px;
            padding-bottom: 15px;
            border-bottom: 1px solid #eee;
        }
        .backup-location:last-child {
            margin-bottom: 0;
            padding-bottom: 0;
            border-bottom: none;
        }
        .backup-location h3 {
            margin-top: 0;
            margin-bottom: 10px;
            font-size: 16px;
            font-weight: 500;
            color: #444;
        }
        .storage-info {
            display: flex;
            align-items: center;
            margin-top: 8px;
            font-size: 14px;
            color: #666;
        }
        .storage-info .space-bar {
            flex-grow: 1;
            height: 8px;
            margin: 0 10px;
            background-color: #eee;
            border-radius: 4px;
            overflow: hidden;
            position: relative;
        }
        .storage-info .space-used {
            position: absolute;
            height: 100%;
            background-color: #4CAF50;
            border-radius: 4px;
        }
        .storage-info .space-used.warning {
            background-color: #FF9800;
        }
        .storage-info .space-used.critical {
            background-color: #F44336;
        }
        .count-block {
            font-size: 16px;
            font-weight: 500;
            margin-bottom: 5px;
        }
        .count-block .emoji {
            font-size: 18px;
            margin-right: 5px;
        }
`
}
