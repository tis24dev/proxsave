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
