package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// encodeProtectedServerIDForTest builds a payload the way production does: one arm list
// derived from a fact snapshot, projected into a key field.
func encodeProtectedServerIDForTest(serverID, primaryMAC string, logger *logging.Logger) (string, error) {
	f := readHostFacts(logger)
	return encodeProtectedServerIDWithKeyField(serverID, identityKeyFieldFor(f, []string{primaryMAC}, primaryMAC), logger), nil
}

// decodeForTest decodes a payload against the CURRENT host facts and the given MAC, which
// is what almost every legacy pin in this file wants.
func decodeForTest(content, mac string) (identityDecodeResult, error) {
	macs := []string(nil)
	if mac != "" {
		macs = []string{mac}
	}
	return decodeProtectedServerID(content, macs, mac, readHostFacts(nil), nil)
}

func TestEncodeDecodeProtectedServerIDRoundTrip(t *testing.T) {
	const serverID = "1234567890123456"
	const mac = "aa:bb:cc:dd:ee:ff"

	content, err := encodeProtectedServerIDForTest(serverID, mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	res, err := decodeForTest(content, mac)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v\ncontent:\n%s", err, content)
	}
	if res.ServerID != serverID {
		t.Fatalf("decoded server ID = %s, want %s", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected round trip decode to be verified")
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	})

	const serverID = "1111222233334444"
	content, err := encodeProtectedServerIDForTest(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	res, err := decodeForTest(content, "00:11:22:33:44:55")
	if err != nil {
		t.Fatalf("expected decode to succeed with different MAC on same host, got %v", err)
	}
	if res.ServerID != serverID {
		t.Fatalf("decoded server ID = %s, want %s", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected the uuid arm to verify the payload on a different MAC")
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	})

	const serverID = "1111222233334444"
	content, err := encodeProtectedServerIDForTest(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	hostnameFunc = func() (string, error) { return "host-two", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-two"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-two"
		default:
			return ""
		}
	})

	if _, err := decodeForTest(content, "aa:bb:cc:dd:ee:ff"); err == nil {
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

	content, err := encodeProtectedServerIDForTest(serverID, mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	// Corrupt the checksum line.
	corrupted := strings.Replace(content, "SYSTEM_CONFIG_DATA=\"", "SYSTEM_CONFIG_DATA=\"corrupt", 1)
	if _, err := decodeForTest(corrupted, mac); err == nil {
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
		_ = setImmutableAttributeWithContext(context.Background(), expectedPath, false, nil)
	})
	if info.IdentityFile != expectedPath {
		t.Fatalf("IdentityFile = %q, want %q", info.IdentityFile, expectedPath)
	}
	if _, err := os.Stat(expectedPath); err != nil {
		t.Fatalf("expected identity file to exist at %q: %v", expectedPath, err)
	}
}

func TestSetImmutableAttributeWithContext_CanceledBeforeCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires linux")
	}

	path := filepath.Join(t.TempDir(), "identity")
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := setImmutableAttributeWithContext(ctx, path, false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
}

func TestDetectUsesExistingIdentityFile(t *testing.T) {
	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		t.Fatalf("failed to create identity dir: %v", err)
	}
	identityPath := filepath.Join(identityDir, identityFileName)

	_, macs := collectMACCandidates(nil)
	if len(macs) == 0 {
		t.Skip("no non-loopback MACs available on this system")
	}
	primary := macs[0]

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerIDForTest(serverID, primary, nil)
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

func TestDetectWithContext_PropagatesCancellationDuringLegacyUpgrade(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "host-one", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return ""
		default:
			return ""
		}
	})

	baseDir := t.TempDir()
	identityDir := filepath.Join(baseDir, identityDirName)
	if err := os.MkdirAll(identityDir, 0o755); err != nil {
		t.Fatalf("failed to create identity dir: %v", err)
	}
	identityPath := filepath.Join(identityDir, identityFileName)

	t.Cleanup(func() {
		_ = setImmutableAttributeWithContext(context.Background(), identityPath, false, nil)
	})

	const serverID = "1234567890123456"
	_, macs := collectMACCandidates(nil)
	if len(macs) == 0 {
		t.Skip("no non-loopback MACs available on this system")
	}
	primaryMAC := macs[0]
	legacy, err := encodeProtectedServerIDLegacy(serverID, primaryMAC)
	if err != nil {
		t.Fatalf("encodeProtectedServerIDLegacy() error = %v", err)
	}
	if err := os.WriteFile(identityPath, []byte(legacy), 0o600); err != nil {
		t.Fatalf("failed to write legacy identity file: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := DetectWithContext(ctx, baseDir, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
	if info == nil {
		t.Fatal("expected info even on cancellation")
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

	res, err := loadServerID(identityPath, []string{nonMatchingMAC, boundMAC}, nonMatchingMAC, readHostFacts(nil), nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	if res.ServerID != serverID {
		t.Fatalf("ServerID = %q, want %q", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected the alternate MAC arm to verify the payload")
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

	got, state := readFirstLine(path, 5)
	if got != "FIRST" || state != systemFilePresent {
		t.Fatalf("readFirstLine() = (%q, %s), want (%q, present)", got, state, "FIRST")
	}

	gotMissing, missingState := readFirstLine(filepath.Join(dir, "missing.txt"), 10)
	if gotMissing != "" || missingState != systemFileAbsent {
		t.Fatalf("readFirstLine(missing) = (%q, %s), want (\"\", absent)", gotMissing, missingState)
	}
}

func TestWriteIdentityFileCreatesFileWith0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id.conf")

	t.Cleanup(func() {
		_ = setImmutableAttributeWithContext(context.Background(), path, false, nil)
	})

	const body = "test-content"
	if err := writeIdentityFileWithContext(context.Background(), path, body, nil); err != nil {
		t.Fatalf("writeIdentityFileWithContext() error = %v", err)
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

func TestWriteIdentityFileWithContext_RelocksOnCanceledContextBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id.conf")
	const initialContent = "initial"
	if err := os.WriteFile(path, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("seed identity file: %v", err)
	}

	origSetImmutable := writeIdentityFileWithContextSetImmutable
	origCreateTemp := identityCreateTempFunc
	origChmod := writeIdentityFileWithContextChmod
	t.Cleanup(func() {
		writeIdentityFileWithContextSetImmutable = origSetImmutable
		identityCreateTempFunc = origCreateTemp
		writeIdentityFileWithContextChmod = origChmod
	})

	ctx, cancel := context.WithCancel(context.Background())
	type immutableCall struct {
		ctx    context.Context
		enable bool
	}
	var calls []immutableCall
	writeIdentityFileWithContextSetImmutable = func(callCtx context.Context, path string, enable bool, logger *logging.Logger) error {
		calls = append(calls, immutableCall{ctx: callCtx, enable: enable})
		if !enable {
			cancel()
		}
		return nil
	}
	identityCreateTempFunc = func(dir, pattern string) (*os.File, error) {
		t.Fatal("writeIdentityFileWithContext should not write after context cancellation")
		return nil, nil
	}
	writeIdentityFileWithContextChmod = func(path string, mode os.FileMode) error {
		t.Fatal("writeIdentityFileWithContext should not chmod after context cancellation")
		return nil
	}

	err := writeIdentityFileWithContext(ctx, path, "updated", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v; want %v", err, context.Canceled)
	}
	if len(calls) != 2 {
		t.Fatalf("immutable call count = %d, want 2", len(calls))
	}
	if calls[0].ctx != ctx || calls[0].enable {
		t.Fatalf("first immutable call = %+v, want unlock with original ctx", calls[0])
	}
	if !calls[1].enable {
		t.Fatalf("second immutable call = %+v, want relock", calls[1])
	}
	if calls[1].ctx == ctx {
		t.Fatalf("expected relock to use non-cancelable context")
	}
	if calls[1].ctx.Err() != nil {
		t.Fatalf("expected relock context to be active, got %v", calls[1].ctx.Err())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	if string(data) != initialContent {
		t.Fatalf("file content = %q, want %q", string(data), initialContent)
	}
}

func TestWriteIdentityFileWithContext_RelocksOnWriteError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "id.conf")
	const initialContent = "initial"
	if err := os.WriteFile(path, []byte(initialContent), 0o600); err != nil {
		t.Fatalf("seed identity file: %v", err)
	}

	origSetImmutable := writeIdentityFileWithContextSetImmutable
	origCreateTemp := identityCreateTempFunc
	origChmod := writeIdentityFileWithContextChmod
	t.Cleanup(func() {
		writeIdentityFileWithContextSetImmutable = origSetImmutable
		identityCreateTempFunc = origCreateTemp
		writeIdentityFileWithContextChmod = origChmod
	})

	type immutableCall struct {
		ctx    context.Context
		enable bool
	}
	var calls []immutableCall
	writeIdentityFileWithContextSetImmutable = func(callCtx context.Context, path string, enable bool, logger *logging.Logger) error {
		calls = append(calls, immutableCall{ctx: callCtx, enable: enable})
		return nil
	}
	writeErr := errors.New("write failed")
	identityCreateTempFunc = func(dir, pattern string) (*os.File, error) {
		return nil, writeErr
	}
	writeIdentityFileWithContextChmod = func(path string, mode os.FileMode) error {
		t.Fatal("writeIdentityFileWithContext should not chmod after write failure")
		return nil
	}

	err := writeIdentityFileWithContext(context.Background(), path, "updated", nil)
	if !errors.Is(err, writeErr) {
		t.Fatalf("err=%v; want %v", err, writeErr)
	}
	if len(calls) != 2 {
		t.Fatalf("immutable call count = %d, want 2", len(calls))
	}
	if calls[0].enable {
		t.Fatalf("first immutable call = %+v, want unlock", calls[0])
	}
	if !calls[1].enable {
		t.Fatalf("second immutable call = %+v, want relock", calls[1])
	}
	if calls[1].ctx.Err() != nil {
		t.Fatalf("expected relock context to be active, got %v", calls[1].ctx.Err())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	if string(data) != initialContent {
		t.Fatalf("file content = %q, want %q", string(data), initialContent)
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

func TestCollectMACAddressesSortedAndUnique(t *testing.T) {
	_, macs := collectMACCandidates(nil)
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
	if _, err := decodeForTest(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for missing SYSTEM_CONFIG_DATA line")
	}
}

func TestDecodeProtectedServerIDInvalidBase64(t *testing.T) {
	content := "SYSTEM_CONFIG_DATA=\"!!!not-base64!!!\"\n"
	if _, err := decodeForTest(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for invalid base64 payload")
	}
}

func TestDecodeProtectedServerIDInvalidPayloadFormat(t *testing.T) {
	payload := "a:b:c"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	content := fmt.Sprintf("SYSTEM_CONFIG_DATA=\"%s\"\n", encoded)

	if _, err := decodeForTest(content, "aa:bb:cc:dd:ee:ff"); err == nil {
		t.Fatalf("expected error for invalid payload format")
	}
}

func TestDecodeProtectedServerIDInvalidServerIDFormat(t *testing.T) {
	const mac = "aa:bb:cc:dd:ee:ff"
	content, err := encodeProtectedServerIDForTest("AAAAAAAAAAAAAAAA", mac, nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if _, err := decodeForTest(content, mac); err == nil {
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerIDForTest(serverID, "", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	res, err := loadServerID(path, []string{}, "", readHostFacts(nil), nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	if res.ServerID != serverID {
		t.Fatalf("loadedID = %q, want %q", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected the uuid arm to verify the payload with no MACs")
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
	_, err = loadServerID(path, wrongMACs, wrongMACs[0], readHostFacts(nil), nil)
	if err == nil {
		t.Fatalf("expected error when all MACs fail")
	}
}

func encodeProtectedServerIDLegacy(serverID, primaryMAC string) (string, error) {
	machineID, _ := readMachineID(nil)
	hostnamePart := readHostnamePart(nil)
	macPart := strings.ReplaceAll(primaryMAC, ":", "")
	systemKey := computeSystemKey(machineID, hostnamePart, macPart)
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return ""
		default:
			return ""
		}
	})

	const serverID = "1111222233334444"
	const macPrimary = "aa:bb:cc:dd:ee:ff"
	const macAlt = "00:11:22:33:44:55"

	f := readHostFacts(nil)
	content := encodeProtectedServerIDWithKeyField(serverID, identityKeyFieldFor(f, []string{macPrimary, macAlt}, macPrimary), nil)

	res, err := decodeForTest(content, macAlt)
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v", err)
	}
	if res.ServerID != serverID {
		t.Fatalf("decoded server ID = %s, want %s", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected decode to be verified for the alternate MAC")
	}

	// An UNKNOWN MAC on the same machine-id now keeps the identity, through the
	// machine-id arm. That is the whole point of change 2: a DMI-less host must survive
	// an ordinary MAC change instead of being renamed permanently and silently. HEAD
	// asserted the opposite here, because HEAD had no machine-id arm to fall back on.
	changed, err := decodeForTest(content, "de:ad:be:ef:00:01")
	if err != nil {
		t.Fatalf("expected the machine-id arm to keep the identity across a MAC change, got %v", err)
	}
	if changed.ServerID != serverID {
		t.Fatalf("decoded server ID after MAC change = %s, want %s", changed.ServerID, serverID)
	}
	if !changed.Verified {
		t.Fatalf("expected the machine-id arm match to be verified")
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return ""
		default:
			return ""
		}
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	t.Cleanup(func() {
		_ = setImmutableAttributeWithContext(context.Background(), path, false, nil)
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

	f := readHostFacts(nil)
	macs := []string{macPrimary, macAlt}
	res, err := loadServerID(path, macs, macPrimary, f, nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	if err := maybeRewriteIdentityFile(context.Background(), path, res, macPrimary, macs, f, nil); err != nil {
		t.Fatalf("maybeRewriteIdentityFile() error = %v", err)
	}

	upgraded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read upgraded identity file: %v", err)
	}
	upgradedContent := string(upgraded)
	if !strings.Contains(upgradedContent, "# Format: proxsave-identity-v2") {
		t.Fatalf("expected upgraded identity file to contain v2 header")
	}

	keyField := extractIdentityKeyField(t, upgradedContent)
	if !strings.Contains(keyField, "=") {
		t.Fatalf("expected upgraded identity payload to contain key labels, got %q", keyField)
	}
	if !strings.Contains(keyField, "mac=") {
		t.Fatalf("expected key field to contain mac= entry, got %q", keyField)
	}
	if !strings.Contains(keyField, "mac_alt1=") {
		t.Fatalf("expected key field to contain mac_alt1= entry, got %q", keyField)
	}

	if decoded, err := decodeForTest(upgradedContent, macAlt); err != nil || !decoded.Verified {
		t.Fatalf("expected upgraded identity to decode via alternate MAC (err=%v verified=%v)", err, decoded.Verified)
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

// ============ MAC address function tests ============

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
		{0, 0},  // permanent - best
		{3, 1},  // set by userspace
		{2, 2},  // stolen
		{1, 3},  // random
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

// ============ Interface detection tests ============

func TestReadAddrAssignType(t *testing.T) {
	origRead := readFirstLineFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
	})

	// Test parsing valid values
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if strings.Contains(path, "addr_assign_type") {
			return "0"
		}
		return ""
	})
	if got := readAddrAssignType("eth0", nil); got != 0 {
		t.Errorf("readAddrAssignType() = %d, want 0", got)
	}

	// Test empty file
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		return ""
	})
	if got := readAddrAssignType("eth0", nil); got != -1 {
		t.Errorf("readAddrAssignType() = %d, want -1 for empty", got)
	}

	// Test invalid value
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		return "invalid"
	})
	if got := readAddrAssignType("eth0", nil); got != -1 {
		t.Errorf("readAddrAssignType() = %d, want -1 for invalid", got)
	}

	// Test with spaces
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		return "  3  "
	})
	if got := readAddrAssignType("eth0", nil); got != 3 {
		t.Errorf("readAddrAssignType() = %d, want 3", got)
	}
}

func TestIsBridgeInterfaceByName(t *testing.T) {
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
			got := isBridgeInterfaceByName(tt.name)
			if got != tt.want {
				t.Fatalf("isBridgeInterfaceByName(%q)=%v; want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsWirelessInterfaceByName(t *testing.T) {
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
			got := isWirelessInterfaceByName(tt.name)
			if got != tt.want {
				t.Fatalf("isWirelessInterfaceByName(%q)=%v; want %v", tt.name, got, tt.want)
			}
		})
	}
}

// ============ ID generation tests ============

func TestBuildSystemData(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
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
	})

	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	data := buildSystemData(macs, readHostFacts(nil), nil)

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
	readFirstLineFunc = stringReader(func(path string, limit int) string { return "" })

	data := buildSystemData(nil, readHostFacts(nil), nil)

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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "test-machine-id"
		default:
			return ""
		}
	})

	macs := []string{"aa:bb:cc:dd:ee:ff"}
	serverID, encoded, err := generateServerID(macs, macs[0], readHostFacts(nil), nil)
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-id-123"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-456"
		default:
			return ""
		}
	})

	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	keyField := identityKeyFieldFor(readHostFacts(nil), macs, "aa:bb:cc:dd:ee:ff")

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

// ============ Helper function tests ============

func TestReadMachineID(t *testing.T) {
	origRead := readFirstLineFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
	})

	// Test primary path
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == machineIDPath {
			return "primary-machine-id"
		}
		return ""
	})
	if got, state := readMachineID(nil); got != "primary-machine-id" || state != systemFilePresent {
		t.Errorf("readMachineID() = (%q, %s), want (%q, present)", got, state, "primary-machine-id")
	}

	// Test fallback path
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == dbusMachineIDPath {
			return "fallback-machine-id"
		}
		return ""
	})
	if got, state := readMachineID(nil); got != "fallback-machine-id" || state != systemFilePresent {
		t.Errorf("readMachineID() fallback = (%q, %s), want (%q, present)", got, state, "fallback-machine-id")
	}

	// Test missing
	readFirstLineFunc = stringReader(func(path string, limit int) string { return "" })
	if got, state := readMachineID(nil); got != "" || state != systemFileAbsent {
		t.Errorf("readMachineID() missing = (%q, %s), want (empty, absent)", got, state)
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-id-123"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-456"
		default:
			return ""
		}
	})

	prefixes := identityKeyPrefixesFor(readHostFacts(nil), []string{"aa:bb:cc:dd:ee:ff"}, "aa:bb:cc:dd:ee:ff")

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

func TestIdentityKeyPrefixesForCoversMACAndMachineIDArms(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == machineIDPath {
			return "machine-id-123"
		}
		return ""
	})

	f := readHostFacts(nil)
	// One MAC gives the mac= / mac_nohost= pair, and the machine-id gives one more arm.
	prefixes := identityKeyPrefixesFor(f, []string{"aa:bb:cc:dd:ee:ff"}, "aa:bb:cc:dd:ee:ff")
	if len(prefixes) != 3 {
		t.Errorf("expected 3 prefixes, got %d", len(prefixes))
	}

	// With no MAC at all the machine-id arm is the only one left, which is exactly what
	// keeps a DMI-less host from being renamed by a MAC change.
	noMAC := identityKeyPrefixesFor(f, nil, "")
	if len(noMAC) != 1 {
		t.Errorf("expected 1 prefix with no MAC, got %d", len(noMAC))
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
		{Iface: "", MAC: "aa:bb:cc:dd:ee:ff"},     // empty iface
		{Iface: "eth0", MAC: ""},                  // empty mac
		{Iface: "  ", MAC: "  "},                  // whitespace only
		{Iface: "eth1", MAC: "00:11:22:33:44:55"}, // valid
	}

	mac, iface := selectPreferredMAC(candidates)
	if mac != "00:11:22:33:44:55" || iface != "eth1" {
		t.Errorf("selectPreferredMAC should skip invalid entries, got (%q, %q)", mac, iface)
	}
}

func TestLoadServerIDFileNotFound(t *testing.T) {
	_, err := loadServerID("/nonexistent/path/identity.conf", []string{"aa:bb:cc:dd:ee:ff"}, "aa:bb:cc:dd:ee:ff", readHostFacts(nil), nil)
	if err == nil {
		t.Errorf("loadServerID should error for missing file")
	}
}

func TestKeyFieldHasLabelEdgeCases(t *testing.T) {
	if keyFieldHasLabel("", uuidKeyLabel) {
		t.Errorf("an empty key field carries no label")
	}
	// A v1 bare prefix carries no label at all, which is what makes the format upgrade
	// trigger fire on it and what stops it being excused by an unreadable product_uuid.
	if keyFieldHasLabel("abcdef12", uuidKeyLabel) || keyFieldHasLabel("abcdef12", machineIDKeyLabel) {
		t.Errorf("a v1 bare prefix must not report any label")
	}
	if !keyFieldHasLabel("mac=abc,uuid=def", uuidKeyLabel) {
		t.Errorf("expected uuid label to be found")
	}
	if keyFieldHasLabel("mac=abc,uuid=def", machineIDKeyLabel) {
		t.Errorf("expected machineid label to be absent")
	}
	if !keyFieldHasLabel("mac=abc, machineid=def ", machineIDKeyLabel) {
		t.Errorf("expected surrounding spaces to be trimmed")
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
	if got, state := readFirstLine(emptyPath, 100); got != "" || state != systemFileAbsent {
		t.Errorf("readFirstLine(empty) = (%q, %s), want (empty, absent)", got, state)
	}

	// Test file with only whitespace
	spacePath := filepath.Join(dir, "space.txt")
	if err := os.WriteFile(spacePath, []byte("   \n  \n"), 0o600); err != nil {
		t.Fatalf("failed to write space file: %v", err)
	}
	if got, state := readFirstLine(spacePath, 100); got != "" || state != systemFileAbsent {
		t.Errorf("readFirstLine(spaces) = (%q, %s), want (empty, absent)", got, state)
	}

	// Test limit of 0 (should return full line)
	fullPath := filepath.Join(dir, "full.txt")
	if err := os.WriteFile(fullPath, []byte("fullcontent"), 0o600); err != nil {
		t.Fatalf("failed to write full file: %v", err)
	}
	if got, state := readFirstLine(fullPath, 0); got != "fullcontent" || state != systemFilePresent {
		t.Errorf("readFirstLine(limit=0) = (%q, %s), want (%q, present)", got, state, "fullcontent")
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	})

	// Empty primary MAC but with alternate MACs
	macs := []string{"aa:bb:cc:dd:ee:ff", "00:11:22:33:44:55"}
	keyField := identityKeyFieldFor(readHostFacts(nil), macs, "")

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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	})

	// Same MAC twice in list
	macs := []string{"aa:bb:cc:dd:ee:ff", "aa:bb:cc:dd:ee:ff"}
	keyField := identityKeyFieldFor(readHostFacts(nil), macs, "aa:bb:cc:dd:ee:ff")

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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "test-machine-id"
		}
		return ""
	})

	// Empty MACs should still work
	serverID, encoded, err := generateServerID([]string{}, "", readHostFacts(nil), nil)
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
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		switch path {
		case "/etc/machine-id":
			return "machine-one"
		case "/sys/class/dmi/id/product_uuid":
			return "uuid-one"
		default:
			return ""
		}
	})

	const serverID = "1234567890123456"
	content, err := encodeProtectedServerIDForTest(serverID, "aa:bb:cc:dd:ee:ff", nil)
	if err != nil {
		t.Fatalf("encodeProtectedServerID() error = %v", err)
	}

	// Decode with empty MAC - should still work via the uuid arm
	res, err := decodeForTest(content, "")
	if err != nil {
		t.Fatalf("decodeProtectedServerID() error = %v", err)
	}
	if res.ServerID != serverID {
		t.Fatalf("decoded = %q, want %q", res.ServerID, serverID)
	}
	if !res.Verified {
		t.Fatalf("expected the uuid arm to verify the payload with no MAC")
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

func TestMaybeRewriteIdentityFileSkipsUnverifiedResult(t *testing.T) {
	// An unverified acceptance must never be laundered into a rewrite, so the path is
	// never even opened.
	res := identityDecodeResult{ServerID: "1234567890123456", KeyField: "abcdef12", Verified: false}
	if err := maybeRewriteIdentityFile(context.Background(), "/nonexistent/path/identity.conf", res, "aa:bb:cc:dd:ee:ff", nil, readHostFacts(nil), nil); err != nil {
		t.Fatalf("maybeRewriteIdentityFile() error = %v", err)
	}
}

func TestMaybeUpgradeIdentityFileAlreadyUpgraded(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	})

	dir := t.TempDir()
	path := filepath.Join(dir, "identity.conf")

	t.Cleanup(func() {
		_ = setImmutableAttributeWithContext(context.Background(), path, false, nil)
	})

	const serverID = "1234567890123456"
	macs := []string{"aa:bb:cc:dd:ee:ff"}

	// Create a v2 file (already has key labels AND the machine-id arm)
	f := readHostFacts(nil)
	v2Content := encodeProtectedServerIDWithKeyField(serverID, identityKeyFieldFor(f, macs, macs[0]), nil)
	if err := os.WriteFile(path, []byte(v2Content), 0o600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	original, _ := os.ReadFile(path)

	res, err := loadServerID(path, macs, macs[0], f, nil)
	if err != nil {
		t.Fatalf("loadServerID() error = %v", err)
	}
	// Both triggers are latched by the labels the rewrite itself writes, so this is a
	// no-op and the file must come out BYTE IDENTICAL.
	if err := maybeRewriteIdentityFile(context.Background(), path, res, macs[0], macs, f, nil); err != nil {
		t.Fatalf("maybeRewriteIdentityFile() error = %v", err)
	}

	after, _ := os.ReadFile(path)
	if !bytes.Equal(original, after) {
		t.Errorf("no-op rewrite changed the file")
	}
}

func TestBuildIdentityKeyFieldEmptyMACs(t *testing.T) {
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
	})

	hostnameFunc = func() (string, error) { return "testhost", nil }
	readFirstLineFunc = stringReader(func(path string, limit int) string {
		if path == "/etc/machine-id" {
			return "machine-id-123"
		}
		return ""
	})

	// Empty everything
	keyField := identityKeyFieldFor(readHostFacts(nil), nil, "")
	// Should not be empty (at minimum uuid entries if uuid available)
	// Even with empty input, the function should not panic
	_ = keyField
}

// A rename failure during the identity write must leave the original .server_identity
// intact (atomicity); the in-place os.WriteFile truncated it before it could fail.
func TestWriteIdentityFile_AtomicRenameFailureKeepsOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".server_identity")
	const original = "ORIGINAL-IDENTITY\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	origSI := writeIdentityFileWithContextSetImmutable
	origRename := writeIdentityFileWithContextRename
	t.Cleanup(func() {
		writeIdentityFileWithContextSetImmutable = origSI
		writeIdentityFileWithContextRename = origRename
	})
	writeIdentityFileWithContextSetImmutable = func(context.Context, string, bool, *logging.Logger) error { return nil }
	writeIdentityFileWithContextRename = func(oldname, newname string) error { return errors.New("rename boom") }

	if err := writeIdentityFileWithContext(context.Background(), path, "NEW-IDENTITY", nil); err == nil {
		t.Fatal("want error from failed atomic write, got nil")
	}
	got, _ := os.ReadFile(path)
	if string(got) != original {
		t.Fatalf("original modified: got %q want %q", string(got), original)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("leftover temp: %s", e.Name())
		}
	}
}

// Same atomicity guarantee for the notify secret (both funnel through the one helper).
func TestPersistNotifySecret_AtomicRenameFailureKeepsOriginal(t *testing.T) {
	baseDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(baseDir, identityDirName), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secretPath := NotifySecretPath(baseDir)
	const original = "old-secret\n"
	if err := os.WriteFile(secretPath, []byte(original), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	origSI := writeIdentityFileWithContextSetImmutable
	origRename := writeIdentityFileWithContextRename
	t.Cleanup(func() {
		writeIdentityFileWithContextSetImmutable = origSI
		writeIdentityFileWithContextRename = origRename
	})
	writeIdentityFileWithContextSetImmutable = func(context.Context, string, bool, *logging.Logger) error { return nil }
	writeIdentityFileWithContextRename = func(oldname, newname string) error { return errors.New("rename boom") }

	if err := PersistNotifySecret(context.Background(), baseDir, "new-secret", nil); err == nil {
		t.Fatal("want error, got nil")
	}
	got, _ := os.ReadFile(secretPath)
	if string(got) != original {
		t.Fatalf("original secret modified: got %q want %q", string(got), original)
	}
}

// stringReader adapts a plain string stub to the classified readFirstLineFunc seam, so a
// test that only cares about VALUES does not have to spell out a state. An empty value
// becomes systemFileAbsent, which is what the real reader reports for a missing or empty
// file. Tests that care about the difference between absent and unreadable install their
// own stub instead.
func stringReader(fn func(path string, limit int) string) func(string, int) (string, systemFileState) {
	return func(path string, limit int) (string, systemFileState) {
		value := strings.TrimSpace(fn(path, limit))
		if value == "" {
			return "", systemFileAbsent
		}
		return value, systemFilePresent
	}
}
