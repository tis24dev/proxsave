package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxmox-backup/internal/config"
)

// ============================================================
// config_helpers.go tests
// ============================================================

func TestSetEnvValue_ReplaceExisting(t *testing.T) {
	template := "FOO=old\nBAR=something\nBAZ=other"

	result := setEnvValue(template, "BAR", "new_value")

	assertContains(t, result, "BAR=new_value")
	assertContains(t, result, "FOO=old")
}

func TestSetEnvValue_PreserveComment(t *testing.T) {
	template := "FOO=old  # this is a comment"

	result := setEnvValue(template, "FOO", "new")

	assertContains(t, result, "FOO=new")
	assertContains(t, result, "# this is a comment")
}

func TestSetEnvValue_AddNew(t *testing.T) {
	template := "FOO=bar"

	result := setEnvValue(template, "NEW_KEY", "new_value")

	assertContains(t, result, "NEW_KEY=new_value")
	assertContains(t, result, "FOO=bar")
}

func TestSetEnvValue_PreserveIndentation(t *testing.T) {
	template := "    INDENTED=old"

	result := setEnvValue(template, "INDENTED", "new")

	assertContains(t, result, "    INDENTED=new")
}

func TestSetEnvValue_DoesNotTouchPrefixedKeys(t *testing.T) {
	template := "FOO=old\nFOO_BAR=keep"

	result := setEnvValue(template, "FOO", "new")

	assertContains(t, result, "FOO=new")
	assertContains(t, result, "FOO_BAR=keep")
}

func TestSetEnvValue_SupportsValuesWithEquals(t *testing.T) {
	template := "FOO=bar"

	result := setEnvValue(template, "FOO", "a=b=c")

	assertContains(t, result, "FOO=a=b=c")
}

func TestResolveInstallConfigPath(t *testing.T) {
	t.Run("absolute path", func(t *testing.T) {
		got, err := resolveInstallConfigPath("/etc/config.env")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "/etc/config.env" {
			t.Fatalf("expected absolute path untouched, got %q", got)
		}
	})

	t.Run("empty path", func(t *testing.T) {
		if _, err := resolveInstallConfigPath("  "); err == nil {
			t.Fatal("expected error for empty path")
		}
	})
}

type stubLogger struct {
	warns []string
}

func (s *stubLogger) Warning(format string, args ...interface{}) {
	s.warns = append(s.warns, fmt.Sprintf(format, args...))
}
func (s *stubLogger) Info(format string, args ...interface{}) {}

func TestEnsureConfigExists(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if err := ensureConfigExists("   ", &stubLogger{}); err == nil {
			t.Fatal("expected error for empty path")
		}
	})

	t.Run("existing file", func(t *testing.T) {
		tmp := filepath.Join(t.TempDir(), "config.env")
		if err := os.WriteFile(tmp, []byte("data"), 0o644); err != nil {
			t.Fatalf("failed to write tmp file: %v", err)
		}
		if err := ensureConfigExists(tmp, &stubLogger{}); err != nil {
			t.Fatalf("expected nil error for existing file: %v", err)
		}
	})

	t.Run("missing file warns", func(t *testing.T) {
		logger := &stubLogger{}
		tmp := filepath.Join(t.TempDir(), "missing.env")
		if err := ensureConfigExists(tmp, logger); err == nil {
			t.Fatal("expected error for missing file")
		}
		if len(logger.warns) == 0 {
			t.Fatal("expected warnings to be logged for missing file")
		}
	})
}

func TestSanitizeEnvValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"normal", "normal", "normal"},
		{"spaces", "  spaces  ", "spaces"},
		{"newline", "with\nnewline", "withnewline"},
		{"carriage", "with\rcarriage", "withcarriage"},
		{"null", "with\x00null", "withnull"},
		{"only control", "\n\r\x00", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := sanitizeEnvValue(tc.input)
			if result != tc.expected {
				t.Errorf("sanitizeEnvValue(%q) = %q, want %q", tc.input, result, tc.expected)
			}
		})
	}
}

// ============================================================
// runtime_helpers.go tests
// ============================================================

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{1099511627776, "1.0 TiB"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := formatBytes(tc.bytes)
			if result != tc.expected {
				t.Errorf("formatBytes(%d) = %q, want %q", tc.bytes, result, tc.expected)
			}
		})
	}
}

func TestFormatBytes_Negative(t *testing.T) {
	if got := formatBytes(-42); got != "-42 B" {
		t.Errorf("formatBytes(-42) = %q, want -42 B", got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "30.0s"},
		{59 * time.Second, "59.0s"},
		{60 * time.Second, "1.0m"},
		{90 * time.Second, "1.5m"},
		{60 * time.Minute, "1.0h"},
		{90 * time.Minute, "1.5h"},
	}

	for _, tc := range tests {
		t.Run(tc.expected, func(t *testing.T) {
			result := formatDuration(tc.duration)
			if result != tc.expected {
				t.Errorf("formatDuration(%v) = %q, want %q", tc.duration, result, tc.expected)
			}
		})
	}
}

func TestFormatDuration_SubSecond(t *testing.T) {
	if got := formatDuration(500 * time.Millisecond); got != "0.5s" {
		t.Errorf("formatDuration(500ms) = %q, want 0.5s", got)
	}
}

func TestFormatBackupNoun(t *testing.T) {
	tests := []struct {
		n        int
		expected string
	}{
		{0, "0 backups"},
		{1, "1 backup"},
		{2, "2 backups"},
		{100, "100 backups"},
	}

	for _, tc := range tests {
		result := formatBackupNoun(tc.n)
		if result != tc.expected {
			t.Errorf("formatBackupNoun(%d) = %q, want %q", tc.n, result, tc.expected)
		}
	}
}

func TestFormatBackupNoun_Negative(t *testing.T) {
	if got := formatBackupNoun(-3); got != "-3 backups" {
		t.Errorf("formatBackupNoun(-3) = %q, want -3 backups", got)
	}
}

func TestTruncateHash(t *testing.T) {
	tests := []struct {
		hash     string
		expected string
	}{
		{"short", "short"},
		{"exactly16chars!", "exactly16chars!"},
		{"abcdef1234567890extra", "abcdef1234567890"},
		{"", ""},
	}

	for _, tc := range tests {
		result := truncateHash(tc.hash)
		if result != tc.expected {
			t.Errorf("truncateHash(%q) = %q, want %q", tc.hash, result, tc.expected)
		}
	}
}

func TestIsLocalPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"absolute", "/absolute/path", true},
		{"root", "/", true},
		{"relative", "relative/path", false},
		{"empty", "", false},
		{"spaces", "   ", false},
		{"remote s3", "s3:bucket/path", false},
		{"rclone", "rclone:remote/path", false},
		{"sftp", "sftp:host/path", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isLocalPath(tc.path)
			if result != tc.expected {
				t.Errorf("isLocalPath(%q) = %v, want %v", tc.path, result, tc.expected)
			}
		})
	}
}

func TestAddPathExclusion(t *testing.T) {
	excludes := []string{"/existing"}

	result := addPathExclusion(excludes, "/new/path")

	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if result[0] != "/existing" {
		t.Error("existing exclusion removed")
	}
	if result[1] != "/new/path" {
		t.Errorf("path not added: %v", result)
	}
	if result[2] != "/new/path/**" {
		t.Errorf("glob pattern not added: %v", result)
	}
}

func TestAddPathExclusion_EmptyPath(t *testing.T) {
	excludes := []string{"/existing"}

	result := addPathExclusion(excludes, "")
	result = addPathExclusion(result, "   ")

	if len(result) != 1 {
		t.Errorf("empty paths should be ignored, got %d items", len(result))
	}
}

func TestAddPathExclusion_NormalizesPath(t *testing.T) {
	input := " /tmp/foo//../bar/ "
	expectedPath := filepath.Clean("/tmp/foo/../bar")

	result := addPathExclusion(nil, input)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[0] != expectedPath {
		t.Errorf("path not cleaned: got %q want %q", result[0], expectedPath)
	}
	if result[1] != filepath.ToSlash(filepath.Join(expectedPath, "**")) {
		t.Errorf("glob not normalized: %q", result[1])
	}
}

func TestAddPathExclusion_DuplicateAddsAgain(t *testing.T) {
	excludes := []string{"/dup", "/dup/**"}

	result := addPathExclusion(excludes, "/dup")

	if count := strings.Count(strings.Join(result, ","), "/dup"); count < 2 {
		t.Errorf("expected duplicate /dup entries, got %v", result)
	}
	if strings.Count(strings.Join(result, ","), "/dup/**") < 2 {
		t.Errorf("expected duplicate glob entries, got %v", result)
	}
}

// ============================================================
// prompts.go tests
// ============================================================

func TestMapPromptInputError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected error
	}{
		{"nil error", nil, nil},
		{"EOF", io.EOF, errPromptInputClosed},
		{"closed file", errors.New("use of closed file"), errPromptInputClosed},
		{"bad fd", errors.New("bad file descriptor"), errPromptInputClosed},
		{"other error", errors.New("something else"), errors.New("something else")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := mapPromptInputError(tc.err)

			if tc.expected == nil {
				if result != nil {
					t.Errorf("expected nil, got %v", result)
				}
				return
			}

			if errors.Is(tc.expected, errPromptInputClosed) {
				if !errors.Is(result, errPromptInputClosed) {
					t.Errorf("expected errPromptInputClosed, got %v", result)
				}
				return
			}

			if result == nil || result.Error() != tc.expected.Error() {
				t.Errorf("expected %v, got %v", tc.expected, result)
			}
		})
	}
}

// ============================================================
// runtime_helpers.go - validateFutureFeatures tests
// ============================================================

func TestValidateFutureFeatures_SecondaryWithoutPath(t *testing.T) {
	cfg := &config.Config{SecondaryEnabled: true}

	if err := validateFutureFeatures(cfg); err == nil {
		t.Error("expected error for secondary enabled without path")
	}
}

func TestValidateFutureFeatures_TelegramWithoutToken(t *testing.T) {
	cfg := &config.Config{
		TelegramEnabled:  true,
		TelegramBotType:  "personal",
		TelegramBotToken: "",
		TelegramChatID:   "123",
	}

	if err := validateFutureFeatures(cfg); err == nil {
		t.Error("expected error for telegram without token")
	}
}

func TestValidateFutureFeatures_TelegramWithoutChatID(t *testing.T) {
	cfg := &config.Config{
		TelegramEnabled:  true,
		TelegramBotType:  "personal",
		TelegramBotToken: "token",
		TelegramChatID:   "",
	}

	if err := validateFutureFeatures(cfg); err == nil {
		t.Error("expected error for telegram without chat ID")
	}
}

func TestValidateFutureFeatures_MetricsWithoutPath(t *testing.T) {
	cfg := &config.Config{
		MetricsEnabled: true,
	}

	if err := validateFutureFeatures(cfg); err == nil {
		t.Error("expected error for metrics enabled without path")
	}
}

func TestValidateFutureFeatures_CloudDisabledWhenNoRemote(t *testing.T) {
	cfg := &config.Config{
		CloudEnabled: true,
		CloudRemote:  "",
	}

	if err := validateFutureFeatures(cfg); err != nil {
		t.Fatalf("should not error, just disable: %v", err)
	}
	if cfg.CloudEnabled {
		t.Error("cloud should be disabled when remote is empty")
	}
	if cfg.CloudRemote != "" || cfg.CloudLogPath != "" {
		t.Errorf("cloud fields should be cleared; remote=%q logPath=%q", cfg.CloudRemote, cfg.CloudLogPath)
	}
}

func TestValidateFutureFeatures_ValidConfig(t *testing.T) {
	cfg := &config.Config{
		SecondaryEnabled: true,
		SecondaryPath:    "/backup/secondary",
		CloudEnabled:     true,
		CloudRemote:      "s3:mybucket",
		MetricsEnabled:   true,
		MetricsPath:      "/var/metrics",
	}

	if err := validateFutureFeatures(cfg); err != nil {
		t.Errorf("valid config should not error: %v", err)
	}
}

// Helper

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}
