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

	"github.com/tis24dev/proxsave/internal/config"
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

// ============================================================
// runtime_helpers.go - additional tests
// ============================================================

func TestDetectBaseDir(t *testing.T) {
	baseDir, found := detectBaseDir()

	// Should return values (may be empty)
	t.Logf("detectBaseDir() returned: baseDir=%q, found=%v", baseDir, found)

	// If found is true, baseDir should not be empty
	if found && baseDir == "" {
		t.Error("if found is true, baseDir should not be empty")
	}
}

func TestResolveHostname(t *testing.T) {
	hostname := resolveHostname()

	if hostname == "" {
		t.Error("resolveHostname should not return empty string")
	}

	// Should be either a valid hostname or "unknown"
	if hostname != "unknown" {
		// Basic validation: no spaces, reasonable length
		if strings.Contains(hostname, " ") {
			t.Errorf("hostname should not contain spaces: %q", hostname)
		}
		if len(hostname) > 255 {
			t.Errorf("hostname too long: %d chars", len(hostname))
		}
	}
}

func TestLogServerIdentityValues(t *testing.T) {
	tests := []struct {
		name     string
		serverID string
		mac      string
	}{
		{"both values", "server-123", "00:11:22:33:44:55"},
		{"only server ID", "server-456", ""},
		{"only MAC", "", "AA:BB:CC:DD:EE:FF"},
		{"empty values", "", ""},
		{"whitespace", "  ", "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just ensure it doesn't panic
			logServerIdentityValues(tt.serverID, tt.mac)
		})
	}
}

func TestWarnExecPathMissing(t *testing.T) {
	// Just ensure it doesn't panic
	warnExecPathMissing()
}

func TestCheckInternetConnectivity(t *testing.T) {
	// This test may fail in isolated environments
	// We test it doesn't panic and handles timeout correctly

	t.Run("with timeout", func(t *testing.T) {
		err := checkInternetConnectivity(100 * time.Millisecond)
		// May succeed or fail depending on network
		t.Logf("checkInternetConnectivity result: %v", err)
	})

	t.Run("zero timeout", func(t *testing.T) {
		err := checkInternetConnectivity(0)
		// Should fail immediately with zero timeout
		if err == nil {
			t.Log("checkInternetConnectivity with zero timeout succeeded (unexpected but ok)")
		}
	})
}

// Note: detectFilesystemInfo requires storage.Storage mock, tested elsewhere

// ============================================================
// main.go tests
// ============================================================

func TestCheckGoRuntimeVersion(t *testing.T) {
	tests := []struct {
		name       string
		minVersion string
		wantErr    bool
	}{
		{
			name:       "very old version passes",
			minVersion: "1.0.0",
			wantErr:    false,
		},
		{
			name:       "future version fails",
			minVersion: "99.99.99",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkGoRuntimeVersion(tt.minVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkGoRuntimeVersion(%s) error = %v, wantErr %v", tt.minVersion, err, tt.wantErr)
			}
		})
	}
}

func TestFeaturesNeedNetwork(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.Config
		expectNeeds  bool
		expectReason string
	}{
		{
			name: "no network features",
			cfg: &config.Config{
				TelegramEnabled: false,
				EmailEnabled:    false,
				CloudEnabled:    false,
			},
			expectNeeds: false,
		},
		{
			name: "telegram enabled",
			cfg: &config.Config{
				TelegramEnabled: true,
				EmailEnabled:    false,
				CloudEnabled:    false,
			},
			expectNeeds:  true,
			expectReason: "Telegram",
		},
		{
			name: "email relay enabled",
			cfg: &config.Config{
				TelegramEnabled:     false,
				EmailEnabled:        true,
				EmailDeliveryMethod: "relay",
				CloudEnabled:        false,
			},
			expectNeeds:  true,
			expectReason: "Email",
		},
		{
			name: "cloud enabled",
			cfg: &config.Config{
				TelegramEnabled: false,
				EmailEnabled:    false,
				CloudEnabled:    true,
			},
			expectNeeds:  true,
			expectReason: "Cloud",
		},
		{
			name: "secondary rclone enabled",
			cfg: &config.Config{
				TelegramEnabled:  false,
				EmailEnabled:     false,
				CloudEnabled:     false,
				SecondaryEnabled: true,
				SecondaryPath:    "rclone:remote/path",
			},
			expectNeeds: false, // Secondary is not checked in featuresNeedNetwork
		},
		{
			name: "multiple features",
			cfg: &config.Config{
				TelegramEnabled:     true,
				EmailEnabled:        true,
				EmailDeliveryMethod: "relay",
				CloudEnabled:        true,
			},
			expectNeeds: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			needs, reasons := featuresNeedNetwork(tt.cfg)

			if needs != tt.expectNeeds {
				t.Errorf("featuresNeedNetwork() needs = %v, want %v", needs, tt.expectNeeds)
			}

			if tt.expectNeeds && tt.expectReason != "" {
				found := false
				for _, reason := range reasons {
					if strings.Contains(reason, tt.expectReason) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected reason containing %q in reasons: %v", tt.expectReason, reasons)
				}
			}
		})
	}
}

func TestDisableNetworkFeaturesForRun(t *testing.T) {
	cfg := &config.Config{
		TelegramEnabled:     true,
		EmailEnabled:        true,
		EmailDeliveryMethod: "relay",
		CloudEnabled:        true,
	}

	// Mock bootstrap logger (nil is ok for this test)
	disableNetworkFeaturesForRun(cfg, nil)

	if cfg.TelegramEnabled {
		t.Error("TelegramEnabled should be disabled")
	}
	if cfg.CloudEnabled {
		t.Error("CloudEnabled should be disabled")
	}

	// Email with relay should be disabled
	if cfg.EmailEnabled && cfg.EmailDeliveryMethod == "relay" {
		t.Error("Email relay should be disabled")
	}
}

func TestDisableNetworkFeaturesForRun_PreservesLocal(t *testing.T) {
	cfg := &config.Config{
		TelegramEnabled:     false,
		EmailEnabled:        true,
		EmailDeliveryMethod: "email-sendmail",
		SecondaryEnabled:    true,
		SecondaryPath:       "/local/path",
	}

	disableNetworkFeaturesForRun(cfg, nil)

	// Local features should remain enabled
	if !cfg.EmailEnabled {
		t.Error("Local email (sendmail) should remain enabled")
	}
	if !cfg.SecondaryEnabled {
		t.Error("Local secondary storage should remain enabled")
	}
}

func TestPrintFinalSummary(t *testing.T) {
	tests := []struct {
		name     string
		exitCode int
	}{
		{"success", 0},
		{"error", 1},
		{"config error", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just ensure it doesn't panic
			printFinalSummary(tt.exitCode)
		})
	}
}

// Helper

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}
