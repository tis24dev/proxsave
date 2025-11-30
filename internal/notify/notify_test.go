package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/types"
)

func TestNotificationStatusString(t *testing.T) {
	cases := []struct {
		status   NotificationStatus
		expected string
	}{
		{StatusSuccess, "success"},
		{StatusWarning, "warning"},
		{StatusFailure, "failure"},
		{NotificationStatus(99), "unknown"},
	}

	for _, tc := range cases {
		if got := tc.status.String(); got != tc.expected {
			t.Fatalf("String() for %v = %s, want %s", tc.status, got, tc.expected)
		}
	}
}

func TestStatusFromExitCode(t *testing.T) {
	cases := []struct {
		code     int
		expected NotificationStatus
	}{
		{types.ExitSuccess.Int(), StatusSuccess},
		{types.ExitGenericError.Int(), StatusWarning},
		{types.ExitBackupError.Int(), StatusFailure},
		{123, StatusFailure},
	}

	for _, tc := range cases {
		if got := StatusFromExitCode(tc.code); got != tc.expected {
			t.Fatalf("StatusFromExitCode(%d) = %v, want %v", tc.code, got, tc.expected)
		}
	}
}

func TestStatusAndStorageEmoji(t *testing.T) {
	emojiCases := []struct {
		name     string
		fn       func() string
		expected string
	}{
		{"status-success", func() string { return GetStatusEmoji(StatusSuccess) }, "✅"},
		{"status-warning", func() string { return GetStatusEmoji(StatusWarning) }, "⚠️"},
		{"status-failure", func() string { return GetStatusEmoji(StatusFailure) }, "❌"},
		{"status-unknown", func() string { return GetStatusEmoji(NotificationStatus(42)) }, "❓"},
		{"storage-ok", func() string { return GetStorageEmoji("ok") }, "✅"},
		{"storage-warning", func() string { return GetStorageEmoji("warning") }, "⚠️"},
		{"storage-error", func() string { return GetStorageEmoji("error") }, "❌"},
		{"storage-disabled", func() string { return GetStorageEmoji("disabled") }, "➖"},
		{"storage-unknown", func() string { return GetStorageEmoji("foobar") }, "❓"},
	}

	for _, tc := range emojiCases {
		if got := tc.fn(); got != tc.expected {
			t.Fatalf("%s emoji = %s, want %s", tc.name, got, tc.expected)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		duration time.Duration
		expected string
	}{
		{500 * time.Millisecond, "< 1s"},
		{3 * time.Second, "3s"},
		{2 * time.Minute, "2m"},
		{2*time.Minute + 10*time.Second, "2m 10s"},
		{1*time.Hour + 5*time.Minute + 7*time.Second, "1h 5m 7s"},
	}

	for _, tc := range cases {
		if got := FormatDuration(tc.duration); got != tc.expected {
			t.Fatalf("FormatDuration(%v) = %s, want %s", tc.duration, got, tc.expected)
		}
	}
}

func TestTemplateHelpers(t *testing.T) {
	data := createTestNotificationData()

	// Subject should include emoji, proxmox type upper, hostname and timestamp.
	subject := BuildEmailSubject(data)
	if !strings.Contains(subject, "✅") || !strings.Contains(subject, strings.ToUpper(data.ProxmoxType.String())) || !strings.Contains(subject, data.Hostname) {
		t.Fatalf("BuildEmailSubject missing expected components: %s", subject)
	}

	plain := BuildEmailPlainText(data)
	expectedPlain := []string{
		"BACKUP REPORT",
		"BACKUP STATUS:",
		"BACKUP DETAILS:",
		"ISSUES:",
		data.Hostname,
		data.BackupFile,
		FormatDuration(data.BackupDuration),
	}
	for _, piece := range expectedPlain {
		if !strings.Contains(plain, piece) {
			t.Fatalf("BuildEmailPlainText missing %q\nBody:\n%s", piece, plain)
		}
	}

	html := BuildEmailHTML(data)
	expectedHTML := []string{
		"<!DOCTYPE html>",
		"<title>" + strings.ToUpper(data.ProxmoxType.String()) + " Backup Report</title>",
		data.Hostname,
		data.BackupFile,
		FormatDuration(data.BackupDuration),
	}
	for _, piece := range expectedHTML {
		if !strings.Contains(html, piece) {
			t.Fatalf("BuildEmailHTML missing %q", piece)
		}
	}

	// Trigger recommendations block with high usage
	data.LocalUsagePercent = 90.0
	htmlWithRecommendation := BuildEmailHTML(data)
	if !strings.Contains(htmlWithRecommendation, "System Recommendations") {
		t.Fatalf("Expected recommendations section when LocalUsagePercent is high")
	}
}

func TestValueHelpers(t *testing.T) {
	if got := valueOrNA(" "); got != "N/A" {
		t.Fatalf("valueOrNA blank = %s, want N/A", got)
	}
	if got := valueOrNA("abc"); got != "abc" {
		t.Fatalf("valueOrNA non-empty = %s, want original", got)
	}

	if escapeHTML(`<script>`) != "&lt;script&gt;" {
		t.Fatal("escapeHTML did not escape")
	}

	if color := getStatusColor(StatusWarning); color != "#FF9800" {
		t.Fatalf("getStatusColor warning = %s, want #FF9800", color)
	}
	if color := getStatusColor(NotificationStatus(10)); color != "#9E9E9E" {
		t.Fatalf("getStatusColor unknown = %s, want gray fallback", color)
	}

	if css := getEmbeddedCSS(); !strings.Contains(css, "body {") {
		t.Fatal("getEmbeddedCSS returned empty or unexpected CSS")
	}
}
