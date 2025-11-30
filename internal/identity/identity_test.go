package identity

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestEncodeDecodeProtectedServerIDRoundTrip(t *testing.T) {
	const serverID = "1234567890123456"
	const mac = "aa:bb:cc:dd:ee:ff"

	content, err := encodeProtectedServerID(serverID, mac)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	decoded, err := decodeProtectedServerID(content, mac)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v\ncontent:\n%s", err, content)
	}
	if decoded != serverID {
		t.Fatalf("decoded server ID = %s, want %s", decoded, serverID)
	}
}

func TestDecodeProtectedServerIDRejectsDifferentHost(t *testing.T) {
	const serverID = "1111222233334444"
	content, err := encodeProtectedServerID(serverID, "aa:bb:cc:dd:ee:ff")
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	if _, err := decodeProtectedServerID(content, "00:11:22:33:44:55"); err == nil {
		t.Fatalf("expected mismatch error when decoding with different MAC")
	}
}

func TestNormalizeServerIDPaddingAndTruncation(t *testing.T) {
	hash := []byte("hashseed")

	if got := normalizeServerID("123", hash); got != "0000000000000123" {
		t.Fatalf("normalizeServerID padding = %s", got)
	}
	if got := normalizeServerID("12345678901234567890", hash); got != "1234567890123456" {
		t.Fatalf("normalizeServerID truncation = %s", got)
	}
	if got := normalizeServerID("", hash); got == "" {
		t.Fatalf("normalizeServerID fallback should not be empty")
	}
}

func TestSanitizeDigitsAndAllDigits(t *testing.T) {
	if got := sanitizeDigits("ab12cd34"); got != "1234" {
		t.Fatalf("sanitizeDigits = %s", got)
	}
	if !isAllDigits("1234567890123456") {
		t.Fatalf("isAllDigits returned false for numeric string")
	}
	if isAllDigits("12ab") {
		t.Fatalf("isAllDigits unexpectedly true for non-numeric string")
	}
}

func TestDecodeProtectedServerIDDetectsCorruptedData(t *testing.T) {
	const serverID = "5555666677778888"
	const mac = "aa:aa:aa:aa:aa:aa"

	content, err := encodeProtectedServerID(serverID, mac)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	// Corrupt the checksum line.
	corrupted := strings.Replace(content, "SYSTEM_CONFIG_DATA=\"", "SYSTEM_CONFIG_DATA=\"corrupt", 1)
	if _, err := decodeProtectedServerID(corrupted, mac); err == nil {
		t.Fatalf("expected checksum mismatch error after corrupting content")
	}
}

func TestDetectCreatesIdentityFileInBaseDir(t *testing.T) {
	baseDir := t.TempDir()

	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	info, err := Detect(baseDir, logger)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	if info == nil {
		t.Fatalf("Detect() returned nil info")
	}
	if info.ServerID == "" {
		t.Fatalf("expected non-empty ServerID")
	}
	if len(info.ServerID) != serverIDLength {
		t.Fatalf("expected ServerID length %d, got %d", serverIDLength, len(info.ServerID))
	}
	if !isAllDigits(info.ServerID) {
		t.Fatalf("expected ServerID to contain only digits, got %q", info.ServerID)
	}

	expectedPath := filepath.Join(baseDir, identityDirName, identityFileName)
	t.Cleanup(func() {
		_ = setImmutableAttribute(expectedPath, false)
	})
	if info.IdentityFile != expectedPath {
		t.Fatalf("IdentityFile = %q, want %q", info.IdentityFile, expectedPath)
	}
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected identity file to exist at %q: %v", expectedPath, err)
	}
}

func TestDetectUsesExistingIdentityFile(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		t.Fatalf("failed to create identity dir: %v", err)
	}
	identityPath := filepath.Join(identityDir, identityFileName)

	macs := collectMACAddresses()
	if len(macs) == 0 {
		t.Skip("no non-loopback MACs available on this system")
	}
	primary := macs[0]

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerID(serverID, primary)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if err := os.WriteFile(identityPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write identity file: %v", err)
	}

	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	info, err := Detect(baseDir, logger)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info == nil {
		t.Fatalf("Detect() returned nil info")
	}
	if info.ServerID != serverID {
		t.Fatalf("ServerID = %q, want %q", info.ServerID, serverID)
	}
	if info.IdentityFile != identityPath {
		t.Fatalf("IdentityFile = %q, want %q", info.IdentityFile, identityPath)
	}
}

func TestDetectFallbackToTmpWhenBaseDirEmpty(t *testing.T) {
	info, err := Detect("", nil)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if info == nil {
		t.Fatalf("Detect() returned nil info")
	}
	if info.IdentityFile != fallbackIdentityPath {
		t.Fatalf("IdentityFile = %q, want %q", info.IdentityFile, fallbackIdentityPath)
	}
	if info.ServerID == "" {
		t.Fatalf("expected non-empty ServerID")
	}
	if len(info.ServerID) != serverIDLength {
		t.Fatalf("expected ServerID length %d, got %d", serverIDLength, len(info.ServerID))
	}
	if !isAllDigits(info.ServerID) {
		t.Fatalf("expected numeric ServerID, got %q", info.ServerID)
	}
	if _, err := os.Stat(fallbackIdentityPath); err != nil {
		t.Fatalf("expected fallback identity file at %q: %v", fallbackIdentityPath, err)
	}
	t.Cleanup(func() {
		_ = setImmutableAttribute(fallbackIdentityPath, false)
	})
}

func TestReadFirstLineTruncatesAndTrims(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "  FIRST-LINE-TOO-LONG  \nsecond line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	got := readFirstLine(path, 5)
	if got != "FIRST" {
		t.Fatalf("readFirstLine() = %q, want %q", got, "FIRST")
	}

	gotMissing := readFirstLine(filepath.Join(dir, "missing.txt"), 10)
	if gotMissing != "" {
		t.Fatalf("expected empty string for missing file, got %q", gotMissing)
	}
}

func TestWriteIdentityFileCreatesFileWith0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id.conf")

	t.Cleanup(func() {
		_ = setImmutableAttribute(path, false)
	})

	const body = "test-content"
	if err := writeIdentityFile(path, body); err != nil {
		t.Fatalf("writeIdentityFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}
	if string(data) != body {
		t.Fatalf("file content = %q, want %q", string(data), body)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file permissions = %v, want 0600", info.Mode().Perm())
	}
}

func TestHexToDecimalValidAndInvalid(t *testing.T) {
	if got := hexToDecimal("ff"); got != "255" {
		t.Fatalf("hexToDecimal(\"ff\") = %q, want %q", got, "255")
	}
	if got := hexToDecimal("ZZ"); got != "" {
		t.Fatalf("hexToDecimal(\"ZZ\") = %q, want empty string", got)
	}
}

func TestFallbackServerIDFormat(t *testing.T) {
	id := fallbackServerID([]byte("seed"))
	if len(id) != serverIDLength {
		t.Fatalf("fallbackServerID length = %d, want %d", len(id), serverIDLength)
	}
	if !isAllDigits(id) {
		t.Fatalf("fallbackServerID should be all digits, got %q", id)
	}
}

func TestGenerateSystemKeyStableAndLength(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	k1 := generateSystemKey(mac)
	k2 := generateSystemKey(mac)

	if len(k1) != 16 {
		t.Fatalf("generateSystemKey length = %d, want 16", len(k1))
	}
	if k1 != k2 {
		t.Fatalf("generateSystemKey should be stable, got %q and %q", k1, k2)
	}
}

func TestCollectMACAddressesSortedAndUnique(t *testing.T) {
	macs := collectMACAddresses()
	for i := 0; i < len(macs); i++ {
		if macs[i] == "" {
			t.Fatalf("unexpected empty MAC at index %d", i)
		}
		if macs[i] != strings.ToLower(macs[i]) {
			t.Fatalf("MAC %q is not lowercase", macs[i])
		}
		if i > 0 {
			if macs[i] < macs[i-1] {
				t.Fatalf("MAC addresses not sorted: %q before %q", macs[i], macs[i-1])
			}
			if macs[i] == macs[i-1] {
				t.Fatalf("duplicate MAC address %q at indices %d and %d", macs[i], i-1, i)
			}
		}
	}
}

func TestDecodeProtectedServerIDMissingConfigLine(t *testing.T) {
	content := "# no SYSTEM_CONFIG_DATA here\n"
	if _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for missing SYSTEM_CONFIG_DATA line")
	}
}

func TestDecodeProtectedServerIDInvalidBase64(t *testing.T) {
	content := "SYSTEM_CONFIG_DATA=\"!!!not-base64!!!\"\n"
	if _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for invalid base64 payload")
	}
}

func TestDecodeProtectedServerIDInvalidPayloadFormat(t *testing.T) {
	payload := "a:b:c"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	content := fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded)

	if _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for invalid payload format")
	}
}

func TestDecodeProtectedServerIDInvalidServerIDFormat(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	content, err := encodeProtectedServerID("AAAAAAAAAAAAAAAA", mac)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if _, err := decodeProtectedServerID(content, mac); err == nil {
		t.Fatalf("expected error for non-digit serverID")
	}
}
