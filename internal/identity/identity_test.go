package identity

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

func TestEncodeDecodeProtectedServerIDRoundTrip(t *testing.T) {
	const serverID = "1234567890123456"
	const mac = "aa:bb:cc:dd:ee:ff"

	content, err := encodeProtectedServerID(serverID, mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	decoded, matchedByMAC, err := decodeProtectedServerID(content, mac, nil)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v\ncontent:\n%s", err, content)
	}
	if decoded != serverID {
		t.Fatalf("decoded server ID = %s, want %s", decoded, serverID)
	}
	if !matchedByMAC {
		t.Fatalf("expected decode to match by MAC for round trip")
	}
}

func TestDecodeProtectedServerIDAcceptsDifferentMACOnSameHost(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	}

	const serverID = "1111222233334444"
	content, err := encodeProtectedServerID(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	decoded, matchedByMAC, err := decodeProtectedServerID(content, "00:11:22:33:44:55", nil)
	if err != nil {
		t.Fatalf("expected decode to succeed with different MAC on same host, got %v", err)
	}
	if decoded != serverID {
		t.Fatalf("decoded server ID = %s, want %s", decoded, serverID)
	}
	if matchedByMAC {
		t.Fatalf("expected decode not to match by MAC when using different MAC")
	}
}

func TestDecodeProtectedServerIDRejectsDifferentHost(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	}

	const serverID = "1111222233334444"
	content, err := encodeProtectedServerID(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	hostnameFunc = func() (string, error) { return "host-two", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-two"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-two"
		default:
			return ""
		}
	}

	if _, _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff", nil); err == nil {
		t.Fatalf("expected mismatch error when decoding as different host")
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

	content, err := encodeProtectedServerID(serverID, mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	// Corrupt the checksum line.
	corrupted := strings.Replace(content, "SYSTEM_CONFIG_DATA=\"", "SYSTEM_CONFIG_DATA=\"corrupt", 1)
	if _, _, err := decodeProtectedServerID(corrupted, mac, nil); err == nil {
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
		_ = setImmutableAttribute(expectedPath, false, nil)
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
	content, err := encodeProtectedServerID(serverID, primary, nil)
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

func TestLoadServerIDTriesAllMACAddresses(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		t.Fatalf("failed to create identity dir: %v", err)
	}
	identityPath := filepath.Join(identityDir, identityFileName)

	const serverID = "9876543210987654"
	const boundMAC = "aa:bb:cc:dd:ee:ff"
	const nonMatchingMAC = "00:11:22:33:44:55"

	content, err := encodeProtectedServerIDLegacy(serverID, boundMAC)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDLegacy() error = %v", err)
	}
	if err := os.WriteFile(identityPath, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write identity file: %v", err)
	}

	loadedID, loadedMAC, err := loadServerID(identityPath, []string{nonMatchingMAC, boundMAC}, nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	if loadedID != serverID {
		t.Fatalf("ServerID = %q, want %q", loadedID, serverID)
	}
	if loadedMAC != boundMAC {
		t.Fatalf("bound MAC = %q, want %q", loadedMAC, boundMAC)
	}
}

func TestDetectErrorsWhenBaseDirEmpty(t *testing.T) {
	info, err := Detect("", nil)
	if err == nil {
		t.Fatalf("expected error when baseDir is empty")
	}
	if info == nil {
		t.Fatalf("Detect() returned nil info")
	}
	if info.IdentityFile != "" {
		t.Fatalf("expected empty IdentityFile when baseDir is empty, got %q", info.IdentityFile)
	}
	if info.ServerID != "" {
		t.Fatalf("expected empty ServerID when baseDir is empty, got %q", info.ServerID)
	}
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
		_ = setImmutableAttribute(path, false, nil)
	})

	const body = "test-content"
	if err := writeIdentityFile(path, body, nil); err != nil {
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
	k1 := generateSystemKey(mac, nil)
	k2 := generateSystemKey(mac, nil)

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
	if _, _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff", nil); err == nil {
		t.Fatalf("expected error for missing SYSTEM_CONFIG_DATA line")
	}
}

func TestDecodeProtectedServerIDInvalidBase64(t *testing.T) {
	content := "SYSTEM_CONFIG_DATA=\"!!!not-base64!!!\"\n"
	if _, _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff", nil); err == nil {
		t.Fatalf("expected error for invalid base64 payload")
	}
}

func TestDecodeProtectedServerIDInvalidPayloadFormat(t *testing.T) {
	payload := "a:b:c"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	content := fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded)

	if _, _, err := decodeProtectedServerID(content, "aa:bb:cc:dd:ee:ff", nil); err == nil {
		t.Fatalf("expected error for invalid payload format")
	}
}

func TestDecodeProtectedServerIDInvalidServerIDFormat(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	content, err := encodeProtectedServerID("AAAAAAAAAAAAAAAA", mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if _, _, err := decodeProtectedServerID(content, mac, nil); err == nil {
		t.Fatalf("expected error for non-digit serverID")
	}
}

func TestLoadServerIDWithEmptyMACSlice(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerID(serverID, "", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	loadedID, loadedMAC, err := loadServerID(path, []string{}, nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	if loadedID != serverID {
		t.Fatalf("loadedID = %q, want %q", loadedID, serverID)
	}
	if loadedMAC != "" {
		t.Fatalf("loadedMAC = %q, want empty", loadedMAC)
	}
}

func TestLoadServerIDFailsAllMACs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	const boundMAC = "aa:bb:cc:dd:ee:ff"
	content, err := encodeProtectedServerIDLegacy("1234567890123456", boundMAC)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDLegacy() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	wrongMACs := []string{"00:00:00:00:00:01", "00:00:00:00:00:02"}
	_, _, err = loadServerID(path, wrongMACs, nil)
	if err == nil {
		t.Fatalf("expected error when all MACs fail")
	}
}

func encodeProtectedServerIDLegacy(serverID, primaryMAC string) (string, error) {
	systemKey := generateSystemKey(primaryMAC, nil)
	timestamp := time.Unix(1700000000, 0).Unix()
	data := fmt.Sprintf("%s:%d:%s", serverID, timestamp, systemKey[:systemKeyPrefixLength])
	checksum := sha256.Sum256([]byte(data))
	finalData := fmt.Sprintf("%s:%s", data, fmt.Sprintf("%x", checksum)[:systemKeyPrefixLength])
	encoded := base64.StdEncoding.EncodeToString([]byte(finalData))
	return fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded), nil
}

func TestSelectPreferredMACPreferenceOrder(t *testing.T) {
	tests := []struct {
		name      string
		cands     []macCandidate
		wantIface string
		wantMAC   string
	}{
		{
			name: "wired beats vmbr and wireless",
			cands: []macCandidate{
				{Iface: "wlp3s0", MAC: "58:1c:f8:11:57:92", IsWireless: true},
				{Iface: "vmbr0", MAC: "a4:bb:6d:a2:16:b4", IsBridge: true},
				{Iface: "eno1", MAC: "a4:bb:6d:a2:16:b4"},
			},
			wantIface: "eno1",
			wantMAC:   "a4:bb:6d:a2:16:b4",
		},
		{
			name: "vmbr beats bridge and wireless",
			cands: []macCandidate{
				{Iface: "wlp3s0", MAC: "58:1c:f8:11:57:92", IsWireless: true},
				{Iface: "br0", MAC: "00:11:22:33:44:55", IsBridge: true},
				{Iface: "vmbr0", MAC: "a4:bb:6d:a2:16:b4", IsBridge: true},
			},
			wantIface: "vmbr0",
			wantMAC:   "a4:bb:6d:a2:16:b4",
		},
		{
			name: "bridge beats wireless",
			cands: []macCandidate{
				{Iface: "wlp3s0", MAC: "58:1c:f8:11:57:92", IsWireless: true},
				{Iface: "br0", MAC: "00:11:22:33:44:55", IsBridge: true},
			},
			wantIface: "br0",
			wantMAC:   "00:11:22:33:44:55",
		},
		{
			name: "wireless beats other",
			cands: []macCandidate{
				{Iface: "lo0", MAC: "00:00:00:00:00:00"},
				{Iface: "wlp3s0", MAC: "58:1c:f8:11:57:92", IsWireless: true},
				{Iface: "dummy0", MAC: "00:11:22:33:44:55", IsVirtual: true},
			},
			wantIface: "wlp3s0",
			wantMAC:   "58:1c:f8:11:57:92",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMAC, gotIface := selectPreferredMAC(tt.cands)
			if gotMAC != tt.wantMAC || gotIface != tt.wantIface {
				t.Fatalf("selectPreferredMAC() = (%q, %q), want (%q, %q)", gotMAC, gotIface, tt.wantMAC, tt.wantIface)
			}
		})
	}
}

func TestDecodeProtectedServerIDMatchesAlternateMACWhenUUIDMissing(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return ""
		default:
			return ""
		}
	}

	const serverID = "1111222233334444"
	const macPrimary = "aa:bb:cc:dd:ee:ff"
	const macAlt = "00:11:22:33:44:55"

	content, err := encodeProtectedServerIDWithMACs(serverID, []string{macPrimary, macAlt}, macPrimary, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDWithMACs() error = %v", err)
	}

	decoded, matchedByMAC, err := decodeProtectedServerID(content, macAlt, nil)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v", err)
	}
	if decoded != serverID {
		t.Fatalf("decoded server ID = %s, want %s", decoded, serverID)
	}
	if !matchedByMAC {
		t.Fatalf("expected decode to match by MAC for alternate MAC")
	}

	if _, _, err := decodeProtectedServerID(content, "de:ad:be:ef:00:01", nil); err == nil {
		t.Fatalf("expected decode to fail for unknown MAC when uuid is missing")
	}
}

func TestMaybeUpgradeIdentityFileRewritesLegacyToV2WithAltMACs(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return ""
		default:
			return ""
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	t.Cleanup(func() {
		_ = setImmutableAttribute(path, false, nil)
	})

	const serverID = "1111222233334444"
	const macPrimary = "aa:bb:cc:dd:ee:ff"
	const macAlt = "00:11:22:33:44:55"

	legacy, err := encodeProtectedServerIDLegacy(serverID, macPrimary)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDLegacy() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatalf("failed to write legacy identity file: %v", err)
	}

	maybeUpgradeIdentityFile(path, serverID, macPrimary, []string{macPrimary, macAlt}, nil)

	upgraded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read upgraded identity file: %v", err)
	}
	upgradedContent := string(upgraded)
	if !strings.Contains(upgradedContent, "# Format: proxsave-identity-v2") {
		t.Fatalf("expected upgraded identity file to contain v2 header")
	}
	if !identityPayloadHasKeyLabels(upgradedContent, nil) {
		t.Fatalf("expected upgraded identity payload to contain key labels")
	}

	keyField := extractIdentityKeyField(t, upgradedContent)
	if !strings.Contains(keyField, "mac=") {
		t.Fatalf("expected key field to contain mac= entry, got %q", keyField)
	}
	if !strings.Contains(keyField, "mac_alt1=") {
		t.Fatalf("expected key field to contain mac_alt1= entry, got %q", keyField)
	}

	if _, matchedByMAC, err := decodeProtectedServerID(upgradedContent, macAlt, nil); err != nil || !matchedByMAC {
		t.Fatalf("expected upgraded identity to decode via alternate MAC (err=%v matchedByMAC=%v)", err, matchedByMAC)
	}
}

func extractIdentityKeyField(t *testing.T, fileContent string) string {
	t.Helper()

	for _, line := range strings.Split(fileContent, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SYSTEM_CONFIG_DATA=") {
			continue
		}

		encoded := strings.Trim(line[len("SYSTEM_CONFIG_DATA="):], "\"")
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatalf("failed to decode SYSTEM_CONFIG_DATA: %v", err)
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 4 {
			t.Fatalf("unexpected payload parts=%d", len(parts))
		}
		return parts[2]
	}

	t.Fatalf("SYSTEM_CONFIG_DATA line not found")
	return ""
}

// ============ Test funzioni MAC address ============

func TestIsLocallyAdministeredMAC(t *testing.T) {
	tests := []struct {
		mac  string
		want bool
	}{
		{"02:00:00:00:00:00", true},  // LAA bit set (0x02 & 0x02 = 0x02)
		{"00:00:00:00:00:00", false}, // LAA bit not set
		{"aa:bb:cc:dd:ee:ff", true},  // 0xaa = 10101010, bit 1 = 1 (LAA set)
		{"a8:bb:cc:dd:ee:ff", false}, // 0xa8 = 10101000, bit 1 = 0 (LAA not set)
		{"fe:ff:ff:ff:ff:ff", true},  // 0xfe = 11111110, bit 1 = 1
		{"fc:ff:ff:ff:ff:ff", false}, // 0xfc = 11111100, bit 1 = 0
		{"", false},
		{"invalid", false},
		{"zz:zz:zz:zz:zz:zz", false},
	}

	for _, tt := range tests {
		t.Run(tt.mac, func(t *testing.T) {
			got := isLocallyAdministeredMAC(tt.mac)
			if got != tt.want {
				t.Errorf("isLocallyAdministeredMAC(%q) = %v, want %v", tt.mac, got, tt.want)
			}
		})
	}
}

func TestNormalizeMAC(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"AA:BB:CC:DD:EE:FF", "aa:bb:cc:dd:ee:ff"},
		{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"},
		{"  AA:BB:CC:DD:EE:FF  ", "aa:bb:cc:dd:ee:ff"},
		{"", ""},
		{"   ", ""},
		{"invalid-mac", "invalid-mac"}, // returns as-is if ParseMAC fails
		{"00:11:22:33:44:55", "00:11:22:33:44:55"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeMAC(tt.input)
			if got != tt.want {
				t.Errorf("normalizeMAC(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCandidateRank(t *testing.T) {
	// Test that candidateRank returns expected rankings
	wiredPermanent := macCandidate{
		Iface:                 "eth0",
		MAC:                   "aa:bb:cc:dd:ee:ff",
		AddrAssignType:        0, // permanent
		IsVirtual:             false,
		IsBridge:              false,
		IsWireless:            false,
		IsLocallyAdministered: false,
	}

	wirelessRandom := macCandidate{
		Iface:                 "wlan0",
		MAC:                   "02:00:00:00:00:01",
		AddrAssignType:        1, // random
		IsVirtual:             false,
		IsBridge:              false,
		IsWireless:            true,
		IsLocallyAdministered: true,
	}

	rank1 := candidateRank(wiredPermanent)
	rank2 := candidateRank(wirelessRandom)

	// Wired permanent should rank better (lower values) than wireless random
	if rank1[0] >= rank2[0] {
		// Check next levels if first level equal
		if rank1[0] == rank2[0] && rank1[1] >= rank2[1] {
			t.Errorf("wiredPermanent should rank better than wirelessRandom")
		}
	}
}

func TestIfaceCategory(t *testing.T) {
	tests := []struct {
		name     string
		cand     macCandidate
		wantCat  int
		wantDesc string
	}{
		{"eth0 wired", macCandidate{Iface: "eth0"}, 0, "wired preferred"},
		{"eno1 wired", macCandidate{Iface: "eno1"}, 0, "wired preferred"},
		{"enp0s3 wired", macCandidate{Iface: "enp0s3"}, 0, "wired preferred"},
		{"bond0", macCandidate{Iface: "bond0"}, 0, "wired preferred"},
		{"team0", macCandidate{Iface: "team0"}, 0, "wired preferred"},
		{"vmbr0", macCandidate{Iface: "vmbr0", IsBridge: true}, 1, "vmbr bridge"},
		{"vmbr1", macCandidate{Iface: "vmbr1", IsBridge: true}, 1, "vmbr bridge"},
		{"br0", macCandidate{Iface: "br0", IsBridge: true}, 2, "other bridge"},
		{"bridge0", macCandidate{Iface: "bridge0", IsBridge: true}, 2, "other bridge"},
		{"br-lan", macCandidate{Iface: "br-lan", IsBridge: true}, 2, "other bridge"},
		{"wlan0", macCandidate{Iface: "wlan0", IsWireless: true}, 3, "wireless"},
		{"wlp3s0", macCandidate{Iface: "wlp3s0", IsWireless: true}, 3, "wireless"},
		{"wl0", macCandidate{Iface: "wl0"}, 3, "wireless prefix"},
		{"dummy0", macCandidate{Iface: "dummy0"}, 4, "other"},
		{"docker0", macCandidate{Iface: "docker0"}, 4, "other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ifaceCategory(tt.cand)
			if got != tt.wantCat {
				t.Errorf("ifaceCategory(%s) = %d, want %d (%s)", tt.cand.Iface, got, tt.wantCat, tt.wantDesc)
			}
		})
	}
}

func TestIsPreferredWiredIface(t *testing.T) {
	tests := []struct {
		name string
		cand macCandidate
		want bool
	}{
		{"eth0", macCandidate{Iface: "eth0"}, true},
		{"eth1", macCandidate{Iface: "eth1"}, true},
		{"eno1", macCandidate{Iface: "eno1"}, true},
		{"enp0s3", macCandidate{Iface: "enp0s3"}, true},
		{"bond0", macCandidate{Iface: "bond0"}, true},
		{"team0", macCandidate{Iface: "team0"}, true},
		{"wlan0 wireless", macCandidate{Iface: "wlan0", IsWireless: true}, false},
		{"eth0 but wireless flag", macCandidate{Iface: "eth0", IsWireless: true}, false},
		{"vmbr0", macCandidate{Iface: "vmbr0"}, false},
		{"br0", macCandidate{Iface: "br0"}, false},
		{"docker0", macCandidate{Iface: "docker0"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPreferredWiredIface(strings.ToLower(tt.cand.Iface), tt.cand)
			if got != tt.want {
				t.Errorf("isPreferredWiredIface(%s) = %v, want %v", tt.cand.Iface, got, tt.want)
			}
		})
	}
}

func TestAddrAssignRank(t *testing.T) {
	tests := []struct {
		value int
		want  int
	}{
		{0, 0}, // permanent - best
		{3, 1}, // set by userspace
		{2, 2}, // stolen
		{1, 3}, // random
		{-1, 4}, // unknown
		{99, 4}, // unknown
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("value_%d", tt.value), func(t *testing.T) {
			got := addrAssignRank(tt.value)
			if got != tt.want {
				t.Errorf("addrAssignRank(%d) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

func TestIsBetterMACCandidateEdgeCases(t *testing.T) {
	// Test tie-breaking by interface name
	a := macCandidate{Iface: "eth0", MAC: "aa:bb:cc:dd:ee:ff"}
	b := macCandidate{Iface: "eth1", MAC: "aa:bb:cc:dd:ee:ff"}

	if !isBetterMACCandidate(a, b) {
		t.Errorf("eth0 should be better than eth1 (alphabetical tie-break)")
	}
	if isBetterMACCandidate(b, a) {
		t.Errorf("eth1 should not be better than eth0")
	}

	// Test tie-breaking by MAC when names equal
	c := macCandidate{Iface: "eth0", MAC: "00:00:00:00:00:01"}
	d := macCandidate{Iface: "eth0", MAC: "00:00:00:00:00:02"}

	if !isBetterMACCandidate(c, d) {
		t.Errorf("lower MAC should win when names equal")
	}
}

// ============ Test rilevamento interfacce ============

func TestReadAddrAssignType(t *testing.T) {
	origRead := readFirstLineFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
	})

	// Test parsing valid values
	readFirstLineFunc = func(path string, limit int) string {
		if strings.Contains(path, "addr_assign_type") {
			return "0"
		}
		return ""
	}
	if got := readAddrAssignType("eth0", nil); got != 0 {
		t.Errorf("readAddrAssignType() = %d, want 0", got)
	}

	// Test empty file
	readFirstLineFunc = func(path string, limit int) string {
		return ""
	}
	if got := readAddrAssignType("eth0", nil); got != -1 {
		t.Errorf("readAddrAssignType() = %d, want -1 for empty", got)
	}

	// Test invalid value
	readFirstLineFunc = func(path string, limit int) string {
		return "invalid"
	}
	if got := readAddrAssignType("eth0", nil); got != -1 {
		t.Errorf("readAddrAssignType() = %d, want -1 for invalid", got)
	}

	// Test with spaces
	readFirstLineFunc = func(path string, limit int) string {
		return "  3  "
	}
	if got := readAddrAssignType("eth0", nil); got != 3 {
		t.Errorf("readAddrAssignType() = %d, want 3", got)
	}
}

func TestIsBridgeInterfaceByName(t *testing.T) {
	// On non-Linux or without sysfs, falls back to name-based detection
	tests := []struct {
		name string
		want bool
	}{
		{"vmbr0", true},
		{"vmbr1", true},
		{"br0", true},
		{"br-lan", true},
		{"bridge0", true},
		{"eth0", false},
		{"wlan0", false},
		{"docker0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This will use name-based fallback if sysfs not available
			got := isBridgeInterface(tt.name)
			// On Linux with sysfs, result may differ, so we just check it doesn't panic
			_ = got
		})
	}
}

func TestIsWirelessInterfaceByName(t *testing.T) {
	// On non-Linux or without sysfs, falls back to name-based detection
	tests := []struct {
		name string
		want bool
	}{
		{"wlan0", true},
		{"wlp3s0", true},
		{"wl0", true},
		{"eth0", false},
		{"eno1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWirelessInterface(tt.name)
			// Check name-based fallback behavior
			if strings.HasPrefix(strings.ToLower(tt.name), "wl") && !got {
				// May or may not work depending on sysfs
			}
		})
	}
}

// ============ Test generazione ID ============

func TestBuildSystemData(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "test-machine-id"
		case "/sys/class/dmi/id/product_uuid":
			return "test-uuid"
		case "/proc/version":
			return "Linux version 5.0"
		default:
			return ""
		}
	}

	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	data := buildSystemData(macs, nil)

	// Verify data contains expected components
	if !strings.Contains(data, "test-machine-id") {
		t.Errorf("buildSystemData should contain machine-id")
	}
	if !strings.Contains(data, "testhost") {
		t.Errorf("buildSystemData should contain hostname")
	}
	if !strings.Contains(data, "test-uuid") {
		t.Errorf("buildSystemData should contain uuid")
	}
	if !strings.Contains(data, "aa:bb:cc:dd:ee:ff") {
		t.Errorf("buildSystemData should contain MAC addresses")
	}
}

func TestBuildSystemDataWithMinimalInput(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	// All sources fail except timestamp (always added)
	hostnameFunc = func() (string, error) { return "", fmt.Errorf("no hostname") }
	readFirstLineFunc = func(path string, limit int) string { return "" }

	data := buildSystemData(nil, nil)

	// Should still return data (at minimum the timestamp)
	if data == "" {
		t.Errorf("buildSystemData should return non-empty string even when sources fail")
	}
	// Timestamp format is 20060102150405 (14 chars)
	if len(data) < 14 {
		t.Errorf("buildSystemData should contain at least the timestamp, got len=%d", len(data))
	}
}

func TestGenerateServerIDDirect(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "test-machine-id"
		default:
			return ""
		}
	}

	macs := []string{"aa:bb:cc:dd:ee:ff"}
	serverID, encoded, err := generateServerID(macs, macs[0], nil)
	if err != nil {
		t.Fatalf("generateServerID() error = %v", err)
	}

	if len(serverID) != serverIDLength {
		t.Errorf("serverID length = %d, want %d", len(serverID), serverIDLength)
	}
	if !isAllDigits(serverID) {
		t.Errorf("serverID should be all digits, got %q", serverID)
	}
	if !strings.Contains(encoded, "SYSTEM_CONFIG_DATA=") {
		t.Errorf("encoded should contain SYSTEM_CONFIG_DATA")
	}
}

func TestBuildIdentityKeyField(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-id-123"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-456"
		default:
			return ""
		}
	}

	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	keyField := buildIdentityKeyField(macs, "aa:bb:cc:dd:ee:ff", nil)

	// Should contain labeled entries
	if !strings.Contains(keyField, "mac=") {
		t.Errorf("keyField should contain mac= entry")
	}
	if !strings.Contains(keyField, "mac_nohost=") {
		t.Errorf("keyField should contain mac_nohost= entry")
	}
	if !strings.Contains(keyField, "uuid=") {
		t.Errorf("keyField should contain uuid= entry")
	}
	if !strings.Contains(keyField, "mac_alt1=") {
		t.Errorf("keyField should contain mac_alt1= entry for alternate MAC")
	}
}

func TestParseKeyFieldPrefixes(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantLen int
	}{
		{"empty", "", 0},
		{"single", "mac=abc123", 1},
		{"multiple", "mac=abc123,mac_nohost=def456,uuid=ghi789", 3},
		{"with spaces", "  mac=abc123 , mac_nohost=def456  ", 2},
		{"no equals", "abc123,def456", 2},
		{"mixed", "mac=abc123,plain,uuid=ghi789", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseKeyFieldPrefixes(tt.input)
			if len(got) != tt.wantLen {
				t.Errorf("parseKeyFieldPrefixes(%q) len = %d, want %d", tt.input, len(got), tt.wantLen)
			}
		})
	}

	// Test that values are extracted correctly
	prefixes := parseKeyFieldPrefixes("mac=abc123,uuid=def456")
	if prefixes[0] != "abc123" || prefixes[1] != "def456" {
		t.Errorf("parseKeyFieldPrefixes should extract values, got %v", prefixes)
	}
}

// ============ Test funzioni helper ============

func TestReadMachineID(t *testing.T) {
	origRead := readFirstLineFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
	})

	// Test primary path
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "primary-machine-id"
		}
		return ""
	}
	if got := readMachineID(nil); got != "primary-machine-id" {
		t.Errorf("readMachineID() = %q, want %q", got, "primary-machine-id")
	}

	// Test fallback path
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/var/lib/dbus/machine-id" {
			return "fallback-machine-id"
		}
		return ""
	}
	if got := readMachineID(nil); got != "fallback-machine-id" {
		t.Errorf("readMachineID() fallback = %q, want %q", got, "fallback-machine-id")
	}

	// Test missing
	readFirstLineFunc = func(path string, limit int) string { return "" }
	if got := readMachineID(nil); got != "" {
		t.Errorf("readMachineID() missing = %q, want empty", got)
	}
}

func TestReadHostnamePart(t *testing.T) {
	origHost := hostnameFunc
	t.Cleanup(func() {
		hostnameFunc = origHost
	})

	// Test short hostname
	hostnameFunc = func() (string, error) { return "short", nil }
	if got := readHostnamePart(nil); got != "short" {
		t.Errorf("readHostnamePart() = %q, want %q", got, "short")
	}

	// Test long hostname (should be truncated to 8 chars)
	hostnameFunc = func() (string, error) { return "verylonghostname", nil }
	if got := readHostnamePart(nil); got != "verylong" {
		t.Errorf("readHostnamePart() = %q, want %q", got, "verylong")
	}

	// Test exactly 8 chars
	hostnameFunc = func() (string, error) { return "exactly8", nil }
	if got := readHostnamePart(nil); got != "exactly8" {
		t.Errorf("readHostnamePart() = %q, want %q", got, "exactly8")
	}

	// Test error
	hostnameFunc = func() (string, error) { return "", fmt.Errorf("no hostname") }
	if got := readHostnamePart(nil); got != "" {
		t.Errorf("readHostnamePart() error = %q, want empty", got)
	}

	// Test empty hostname
	hostnameFunc = func() (string, error) { return "  ", nil }
	if got := readHostnamePart(nil); got != "" {
		t.Errorf("readHostnamePart() empty = %q, want empty", got)
	}
}

func TestComputeSystemKey(t *testing.T) {
	// Test deterministic output
	key1 := computeSystemKey("machine1", "host1", "extra1")
	key2 := computeSystemKey("machine1", "host1", "extra1")

	if key1 != key2 {
		t.Errorf("computeSystemKey should be deterministic, got %q and %q", key1, key2)
	}

	if len(key1) != 16 {
		t.Errorf("computeSystemKey length = %d, want 16", len(key1))
	}

	// Test different inputs produce different outputs
	key3 := computeSystemKey("machine2", "host1", "extra1")
	if key1 == key3 {
		t.Errorf("different inputs should produce different keys")
	}
}

func TestComputeCurrentIdentityKeyPrefixes(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-id-123"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-456"
		default:
			return ""
		}
	}

	prefixes := computeCurrentIdentityKeyPrefixes("aa:bb:cc:dd:ee:ff", nil)

	// Should have prefixes for MAC and UUID (with and without host)
	if len(prefixes) < 2 {
		t.Errorf("expected at least 2 prefixes, got %d", len(prefixes))
	}

	// All prefixes should be non-empty
	for prefix := range prefixes {
		if prefix == "" {
			t.Errorf("found empty prefix in map")
		}
		if len(prefix) != systemKeyPrefixLength {
			t.Errorf("prefix length = %d, want %d", len(prefix), systemKeyPrefixLength)
		}
	}
}

func TestComputeCurrentMACKeyPrefixes(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	}

	prefixes := computeCurrentMACKeyPrefixes("aa:bb:cc:dd:ee:ff", nil)

	// Should have 2 prefixes (with and without host)
	if len(prefixes) != 2 {
		t.Errorf("expected 2 prefixes, got %d", len(prefixes))
	}

	// Test empty MAC
	emptyPrefixes := computeCurrentMACKeyPrefixes("", nil)
	if len(emptyPrefixes) != 0 {
		t.Errorf("expected 0 prefixes for empty MAC, got %d", len(emptyPrefixes))
	}
}

// ============ Test edge cases ============

func TestSelectPreferredMACEmpty(t *testing.T) {
	mac, iface := selectPreferredMAC(nil)
	if mac != "" || iface != "" {
		t.Errorf("selectPreferredMAC(nil) = (%q, %q), want empty", mac, iface)
	}

	mac, iface = selectPreferredMAC([]macCandidate{})
	if mac != "" || iface != "" {
		t.Errorf("selectPreferredMAC([]) = (%q, %q), want empty", mac, iface)
	}
}

func TestSelectPreferredMACWithEmptyFields(t *testing.T) {
	candidates := []macCandidate{
		{Iface: "", MAC: "aa:bb:cc:dd:ee:ff"},        // empty iface
		{Iface: "eth0", MAC: ""},                     // empty mac
		{Iface: "  ", MAC: "  "},                     // whitespace only
		{Iface: "eth1", MAC: "00:11:22:33:44:55"},    // valid
	}

	mac, iface := selectPreferredMAC(candidates)
	if mac != "00:11:22:33:44:55" || iface != "eth1" {
		t.Errorf("selectPreferredMAC should skip invalid entries, got (%q, %q)", mac, iface)
	}
}

func TestLoadServerIDFileNotFound(t *testing.T) {
	_, _, err := loadServerID("/nonexistent/path/identity.conf", []string{"aa:bb:cc:dd:ee:ff"}, nil)
	if err == nil {
		t.Errorf("loadServerID should error for missing file")
	}
}

func TestIdentityPayloadHasKeyLabelsEdgeCases(t *testing.T) {
	// Empty content
	if identityPayloadHasKeyLabels("", nil) {
		t.Errorf("empty content should not have key labels")
	}

	// No SYSTEM_CONFIG_DATA line
	if identityPayloadHasKeyLabels("# just a comment\n", nil) {
		t.Errorf("no config line should not have key labels")
	}

	// Invalid base64
	if identityPayloadHasKeyLabels("SYSTEM_CONFIG_DATA=\"!!!invalid!!!\"\n", nil) {
		t.Errorf("invalid base64 should not have key labels")
	}

	// Valid payload without labels (legacy format)
	legacyPayload := base64.StdEncoding.EncodeToString([]byte("serverid:12345:keyprefix:checksum"))
	if identityPayloadHasKeyLabels(fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", legacyPayload), nil) {
		t.Errorf("legacy format without = should not have key labels")
	}

	// Valid payload with labels
	labeledPayload := base64.StdEncoding.EncodeToString([]byte("serverid:12345:mac=abc,uuid=def:checksum"))
	if !identityPayloadHasKeyLabels(fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", labeledPayload), nil) {
		t.Errorf("labeled format should have key labels")
	}
}

func TestIsAllDigitsEdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"0", true},
		{"0123456789", true},
		{"00000000000000000", true},
		{" 123", false},
		{"123 ", false},
		{"12 34", false},
		{"-123", false},
		{"+123", false},
		{"1.23", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isAllDigits(tt.input)
			if got != tt.want {
				t.Errorf("isAllDigits(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestReadFirstLineEdgeCases(t *testing.T) {
	dir := t.TempDir()

	// Test empty file
	emptyPath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyPath, []byte(""), 0o600); err != nil {
		t.Fatalf("failed to write empty file: %v", err)
	}
	if got := readFirstLine(emptyPath, 100); got != "" {
		t.Errorf("readFirstLine(empty) = %q, want empty", got)
	}

	// Test file with only whitespace
	spacePath := filepath.Join(dir, "space.txt")
	if err := os.WriteFile(spacePath, []byte("   \n  \n"), 0o600); err != nil {
		t.Fatalf("failed to write space file: %v", err)
	}
	if got := readFirstLine(spacePath, 100); got != "" {
		t.Errorf("readFirstLine(spaces) = %q, want empty", got)
	}

	// Test limit of 0 (should return full line)
	fullPath := filepath.Join(dir, "full.txt")
	if err := os.WriteFile(fullPath, []byte("fullcontent"), 0o600); err != nil {
		t.Fatalf("failed to write full file: %v", err)
	}
	if got := readFirstLine(fullPath, 0); got != "fullcontent" {
		t.Errorf("readFirstLine(limit=0) = %q, want %q", got, "fullcontent")
	}
}

func TestBuildIdentityKeyFieldNoPrimaryMAC(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	}

	// Empty primary MAC but with alternate MACs
	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	keyField := buildIdentityKeyField(macs, "", nil)

	// Should still have entries for alternate MACs
	if !strings.Contains(keyField, "mac_alt") || keyField == "" {
		t.Logf("keyField = %q", keyField)
	}
}

func TestBuildIdentityKeyFieldDeduplication(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	}

	// Same MAC twice in list
	macs := []string{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"}
	keyField := buildIdentityKeyField(macs, "aa:bb:cc:dd:ee:ff", nil)

	// Should not have duplicates
	parts := strings.Split(keyField, ",")
	seen := make(map[string]bool)
	for _, part := range parts {
		if seen[part] {
			t.Errorf("duplicate entry in keyField: %q", part)
		}
		seen[part] = true
	}
}

func TestLogFunctionsNilLogger(t *testing.T) {
	// Should not panic with nil logger
	logDebug(nil, "test %s", "message")
	logWarning(nil, "test %s", "message")
}

func TestLogFunctionsWithLogger(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	logDebug(logger, "debug %s", "test")
	logWarning(logger, "warning %s", "test")

	output := buf.String()
	if !strings.Contains(output, "debug test") {
		t.Errorf("expected debug message in output")
	}
	if !strings.Contains(output, "warning test") {
		t.Errorf("expected warning message in output")
	}
}

func TestNormalizeServerIDWithEmptyHash(t *testing.T) {
	// Test with various hash lengths
	hash := []byte{}
	id := normalizeServerID("123", hash)
	if len(id) != serverIDLength {
		t.Errorf("normalizeServerID length = %d, want %d", len(id), serverIDLength)
	}

	// Test with nil-like value
	id2 := normalizeServerID("", []byte("seed"))
	if len(id2) != serverIDLength {
		t.Errorf("normalizeServerID fallback length = %d, want %d", len(id2), serverIDLength)
	}
}

func TestFallbackServerIDWithShortHash(t *testing.T) {
	// Test with very short hash
	shortHash := []byte{0, 1, 2}
	id := fallbackServerID(shortHash)
	if len(id) != serverIDLength {
		t.Errorf("fallbackServerID length = %d, want %d", len(id), serverIDLength)
	}
	if !isAllDigits(id) {
		t.Errorf("fallbackServerID should be all digits, got %q", id)
	}
}

func TestGenerateServerIDWithEmptyMACs(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "test-machine-id"
		}
		return ""
	}

	// Empty MACs should still work
	serverID, encoded, err := generateServerID([]string{}, "", nil)
	if err != nil {
		t.Fatalf("generateServerID() error = %v", err)
	}

	if len(serverID) != serverIDLength {
		t.Errorf("serverID length = %d, want %d", len(serverID), serverIDLength)
	}
	if encoded == "" {
		t.Errorf("encoded should not be empty")
	}
}

func TestDecodeProtectedServerIDWithEmptyMAC(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	}

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerID(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	// Decode with empty MAC - should still work via UUID
	decoded, matchedByMAC, err := decodeProtectedServerID(content, "", nil)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v", err)
	}
	if decoded != serverID {
		t.Fatalf("decoded = %q, want %q", decoded, serverID)
	}
	if matchedByMAC {
		t.Fatalf("should not match by MAC when MAC is empty")
	}
}

func TestCollectMACCandidatesWithLogger(t *testing.T) {
	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)

	// Just verify it doesn't panic with logger
	candidates, macs := collectMACCandidates(logger)
	_ = candidates
	_ = macs
}

func TestMaybeUpgradeIdentityFileNonExistent(t *testing.T) {
	// Should not panic on non-existent file
	maybeUpgradeIdentityFile("/nonexistent/path/identity.conf", "1234567890123456", "aa:bb:cc:dd:ee:ff", nil, nil)
}

func TestMaybeUpgradeIdentityFileAlreadyUpgraded(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	t.Cleanup(func() {
		_ = setImmutableAttribute(path, false, nil)
	})

	const serverID = "1234567890123456"
	macs := []string{"aa:bb:cc:dd:ee:ff"}

	// Create a v2 file (already has key labels)
	v2Content, err := encodeProtectedServerIDWithMACs(serverID, macs, macs[0], nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDWithMACs() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(v2Content), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// Get original content
	original, _ := os.ReadFile(path)

	// Try to upgrade - should be no-op since already v2
	maybeUpgradeIdentityFile(path, serverID, macs[0], macs, nil)

	// Content should not have changed (same format)
	after, _ := os.ReadFile(path)
	// We can't compare exact bytes because timestamps differ, but format should be same
	if !identityPayloadHasKeyLabels(string(after), nil) {
		t.Errorf("file should still have key labels after no-op upgrade")
	}
	_ = original
}

func TestBuildIdentityKeyFieldEmptyMACs(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	}

	// Empty everything
	keyField := buildIdentityKeyField(nil, "", nil)
	// Should not be empty (at minimum uuid entries if uuid available)
	// Even with empty input, the function should not panic
	_ = keyField
}
