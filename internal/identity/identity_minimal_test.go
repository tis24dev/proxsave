package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
)

// ============================================================================
// Fixture
//
// One helper installs hostnameFunc, readFirstLineFunc and collectMACCandidatesFunc from a
// fakeHost, and restores them with t.Cleanup. One runner calls DetectWithContext against a
// caller supplied directory. Every pin below observes DetectWithContext END TO END except
// P20, P21 and P22, which say why they are unit pins. No test writes outside t.TempDir.
// ============================================================================

// sysFact is one stubbed host fact: a value plus the state the reader classified it as.
type sysFact struct {
	value string
	state systemFileState
}

func factPresent(value string) sysFact { return sysFact{value: value, state: systemFilePresent} }
func factAbsent() sysFact              { return sysFact{state: systemFileAbsent} }
func factDenied() sysFact              { return sysFact{state: systemFileDenied} }
func factUnreadable() sysFact          { return sysFact{state: systemFileUnreadable} }

type fakeHost struct {
	machineID sysFact
	dbus      sysFact
	uuid      sysFact
	hostname  string
	macs      []string
}

func (h fakeHost) install(t *testing.T) {
	t.Helper()
	origRead := readFirstLineFunc
	origHost := hostnameFunc
	origMACs := collectMACCandidatesFunc
	origImmutable := writeIdentityFileWithContextSetImmutable
	t.Cleanup(func() {
		readFirstLineFunc = origRead
		hostnameFunc = origHost
		collectMACCandidatesFunc = origMACs
		writeIdentityFileWithContextSetImmutable = origImmutable
	})

	serve := func(f sysFact) (string, systemFileState) {
		// The real reader never reports present with an empty value, so a fixture that
		// leaves a fact unset means "absent" rather than "present and empty".
		if f.state == systemFilePresent && strings.TrimSpace(f.value) == "" {
			return "", systemFileAbsent
		}
		return f.value, f.state
	}
	readFirstLineFunc = func(path string, limit int) (string, systemFileState) {
		switch path {
		case machineIDPath:
			return serve(h.machineID)
		case dbusMachineIDPath:
			return serve(h.dbus)
		case productUUIDPath:
			return serve(h.uuid)
		default:
			return "", systemFileAbsent
		}
	}
	hostnameFunc = func() (string, error) { return h.hostname, nil }
	collectMACCandidatesFunc = func(*logging.Logger) ([]macCandidate, []string) {
		cands := make([]macCandidate, 0, len(h.macs))
		macs := make([]string, 0, len(h.macs))
		for i, mac := range h.macs {
			cands = append(cands, macCandidate{Iface: fmt.Sprintf("eth%d", i), MAC: mac})
			macs = append(macs, mac)
		}
		sort.Strings(macs)
		return cands, macs
	}
	// chattr is not the mechanism under test, and leaving +i behind would defeat the
	// t.TempDir cleanup.
	writeIdentityFileWithContextSetImmutable = func(context.Context, string, bool, *logging.Logger) error { return nil }
}

// facts returns the hostFacts this fixture produces, for the pins that need to build a
// payload the way a given machine would have written it.
func (h fakeHost) facts(t *testing.T) hostFacts {
	t.Helper()
	h.install(t)
	return readHostFacts(nil)
}

func detectAs(t *testing.T, baseDir string, h fakeHost) (*Info, error) {
	t.Helper()
	h.install(t)
	return DetectWithContext(context.Background(), baseDir, nil)
}

func mustDetectAs(t *testing.T, baseDir string, h fakeHost) *Info {
	t.Helper()
	info, err := detectAs(t, baseDir, h)
	if err != nil {
		t.Fatalf("DetectWithContext() error = %v", err)
	}
	if info == nil {
		t.Fatal("DetectWithContext() returned nil info")
	}
	return info
}

func identityDirOf(baseDir string) string { return filepath.Join(baseDir, identityDirName) }
func identityPathOf(baseDir string) string {
	return filepath.Join(identityDirOf(baseDir), identityFileName)
}

func identityBytes(t *testing.T, baseDir string) []byte {
	t.Helper()
	data, err := os.ReadFile(identityPathOf(baseDir))
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	return data
}

func rejectedNames(t *testing.T, baseDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(identityDirOf(baseDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("read identity dir: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), identityRejectedSuffix) {
			out = append(out, entry.Name())
		}
	}
	sort.Strings(out)
	return out
}

func payloadParts(t *testing.T, content string) []string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SYSTEM_CONFIG_DATA=") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(strings.Trim(line[len("SYSTEM_CONFIG_DATA="):], "\""))
		if err != nil {
			t.Fatalf("decode SYSTEM_CONFIG_DATA: %v", err)
		}
		parts := strings.Split(string(raw), ":")
		if len(parts) != 4 {
			t.Fatalf("unexpected payload parts=%d", len(parts))
		}
		return parts
	}
	t.Fatal("SYSTEM_CONFIG_DATA line not found")
	return nil
}

func storedKeyField(t *testing.T, content string) string {
	t.Helper()
	return payloadParts(t, content)[2]
}

func storedServerID(t *testing.T, content string) string {
	t.Helper()
	return payloadParts(t, content)[0]
}

// keyFieldMinus builds the key field a given machine would write, optionally WITHOUT some
// labels, so a pin can seed a payload of an older shape (mac arms only, no machineid arm)
// without hand rolling the hashing.
func keyFieldMinus(f hostFacts, macs []string, primaryMAC string, drop ...string) string {
	dropped := make(map[string]bool, len(drop))
	for _, label := range drop {
		dropped[label] = true
	}
	entries := make([]string, 0, 8)
	for _, arm := range identityKeyArms(f, macs, primaryMAC) {
		if dropped[arm.Label] {
			continue
		}
		entries = append(entries, arm.Label+"="+arm.Prefix)
	}
	return strings.Join(entries, ",")
}

// seedIdentityFile drops a payload with the given server ID and key field at the canonical
// path, which is how every "a foreign machine wrote this" pin starts.
func seedIdentityFile(t *testing.T, baseDir, serverID, keyField string) []byte {
	t.Helper()
	dir := identityDirOf(baseDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	content := encodeProtectedServerIDWithKeyField(serverID, keyField, nil)
	if err := os.WriteFile(identityPathOf(baseDir), []byte(content), 0o600); err != nil {
		t.Fatalf("seed identity file: %v", err)
	}
	return []byte(content)
}

// v1KeyField is the legacy shape: ONE bare prefix, no labels, keyed on
// computeSystemKey(machineID, hostnamePart, macPart). product_uuid is not one of its
// inputs, which is why an unreadable product_uuid can never excuse its mismatch.
func v1KeyField(machineID, hostnamePart, mac string) string {
	macPart := strings.ReplaceAll(normalizeMAC(mac), ":", "")
	return computeSystemKey(machineID, hostnamePart, macPart)[:systemKeyPrefixLength]
}

// seedCorruptIdentityFile drops a payload whose checksum does not verify, so the LOAD
// fails and the run reaches the replace path. That is the only way to exercise the replace
// path's own guards on a host that already has a file.
func seedCorruptIdentityFile(t *testing.T, baseDir string) []byte {
	t.Helper()
	dir := identityDirOf(baseDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	raw := "1234567890123456:1700000000:mac=deadbeef:00000000"
	content := "# ProxSave Backup System Configuration\nSYSTEM_CONFIG_DATA=\"" +
		base64.StdEncoding.EncodeToString([]byte(raw)) + "\"\n"
	if err := os.WriteFile(identityPathOf(baseDir), []byte(content), 0o600); err != nil {
		t.Fatalf("seed corrupt identity file: %v", err)
	}
	return []byte(content)
}

// failTempFor makes identityCreateTempFunc fail for exactly one class of target: the
// canonical identity file (whose temp pattern does NOT contain ".rejected-") or the
// quarantine copy (whose pattern does).
func failTempFor(t *testing.T, quarantine bool, err error) {
	t.Helper()
	orig := identityCreateTempFunc
	t.Cleanup(func() { identityCreateTempFunc = orig })
	identityCreateTempFunc = func(dir, pattern string) (*os.File, error) {
		if strings.Contains(pattern, identityRejectedSuffix) == quarantine {
			return nil, err
		}
		return orig(dir, pattern)
	}
}

var (
	hostAlpha = fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	hostBravo = fakeHost{
		machineID: factPresent("2222222222222222222222222222222b"),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
)

// ============================================================================
// I1: an existing payload is never destroyed before its replacement is in place
// ============================================================================

// P1 pins that a replacement preserves the payload it replaces, byte for byte.
func TestP1_ReplacementPreservesTheExistingPayload(t *testing.T) {
	baseDir := t.TempDir()

	first := mustDetectAs(t, baseDir, hostAlpha)
	original := identityBytes(t, baseDir)

	second := mustDetectAs(t, baseDir, hostBravo)
	if second.ServerID == first.ServerID {
		t.Fatalf("expected a new server ID on a foreign host, got %q twice", second.ServerID)
	}

	names := rejectedNames(t, baseDir)
	if len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
	preserved, err := os.ReadFile(filepath.Join(identityDirOf(baseDir), names[0]))
	if err != nil {
		t.Fatalf("read preserved copy: %v", err)
	}
	if !bytes.Equal(preserved, original) {
		t.Fatalf("preserved copy is not byte identical to the payload it replaced")
	}
}

// P2 pins that preservation is a COPY: a replacement write that fails must leave the
// original payload at the canonical path, still loadable by its own machine.
func TestP2_FailedReplacementLeavesTheCanonicalPayloadInPlace(t *testing.T) {
	baseDir := t.TempDir()

	first := mustDetectAs(t, baseDir, hostAlpha)
	original := identityBytes(t, baseDir)

	func() {
		failTempFor(t, false, errors.New("canonical write boom"))
		if _, err := detectAs(t, baseDir, hostBravo); err != nil {
			t.Fatalf("DetectWithContext() error = %v, want nil after a swallowed write failure", err)
		}
	}()

	if got := identityBytes(t, baseDir); !bytes.Equal(got, original) {
		t.Fatalf("canonical payload changed after a failed replacement write")
	}

	third := mustDetectAs(t, baseDir, hostAlpha)
	if third.ServerID != first.ServerID {
		t.Fatalf("server ID after a failed replacement = %q, want %q", third.ServerID, first.ServerID)
	}
}

// P3 pins that nothing DISCARDS the preserved copy when the replacement really landed and
// only the hardening after it failed.
func TestP3_PreservedCopySurvivesAWriteThatLandedThenFailed(t *testing.T) {
	baseDir := t.TempDir()

	mustDetectAs(t, baseDir, hostAlpha)
	original := identityBytes(t, baseDir)

	origChmod := writeIdentityFileWithContextChmod
	t.Cleanup(func() { writeIdentityFileWithContextChmod = origChmod })
	writeIdentityFileWithContextChmod = func(string, os.FileMode) error { return errors.New("chmod boom") }

	info, err := detectAs(t, baseDir, hostBravo)
	if err != nil {
		t.Fatalf("DetectWithContext() error = %v, want nil after a post-rename hardening failure", err)
	}

	names := rejectedNames(t, baseDir)
	if len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
	preserved, readErr := os.ReadFile(filepath.Join(identityDirOf(baseDir), names[0]))
	if readErr != nil {
		t.Fatalf("read preserved copy: %v", readErr)
	}
	if !bytes.Equal(preserved, original) {
		t.Fatalf("preserved copy was discarded or altered after a landed write")
	}
	if got := storedServerID(t, string(identityBytes(t, baseDir))); got != info.ServerID {
		t.Fatalf("canonical server ID = %q, want the new one %q", got, info.ServerID)
	}
}

// P4 pins that preservation is IDEMPOTENT and content addressed, which is why there is no
// pruner and no discard step to be wrong about.
func TestP4_RepeatedFailedReplacementsLeaveExactlyOnePreservedCopy(t *testing.T) {
	baseDir := t.TempDir()

	mustDetectAs(t, baseDir, hostAlpha)
	original := identityBytes(t, baseDir)

	failTempFor(t, false, errors.New("canonical write boom"))
	for i := 0; i < 6; i++ {
		if _, err := detectAs(t, baseDir, hostBravo); err != nil {
			t.Fatalf("run %d: DetectWithContext() error = %v", i, err)
		}
	}

	names := rejectedNames(t, baseDir)
	if len(names) != 1 {
		t.Fatalf("preserved copies after six failed replacements = %v, want exactly one", names)
	}
	sum := sha256.Sum256(original)
	want := identityFileName + identityRejectedSuffix + fmt.Sprintf("%x", sum)[:quarantineNameHexLen]
	if names[0] != want {
		t.Fatalf("preserved copy name = %q, want %q", names[0], want)
	}
	preserved, err := os.ReadFile(filepath.Join(identityDirOf(baseDir), names[0]))
	if err != nil {
		t.Fatalf("read preserved copy: %v", err)
	}
	if !bytes.Equal(preserved, original) {
		t.Fatalf("preserved copy changed across repeated preservation")
	}
}

// ============================================================================
// I2: a host with no usable DMI keeps its identity across a MAC change
// ============================================================================

// P5 is change 2 in one assertion.
func TestP5_DMILessHostKeepsItsIdentityAcrossAMACChange(t *testing.T) {
	baseDir := t.TempDir()
	host := fakeHost{
		machineID: factPresent("3333333333333333333333333333333c"),
		uuid:      factAbsent(),
		hostname:  "ct-one",
		macs:      []string{"aa:aa:aa:aa:aa:01"},
	}

	first := mustDetectAs(t, baseDir, host)
	before := identityBytes(t, baseDir)

	changed := host
	changed.macs = []string{"aa:aa:aa:aa:aa:02"}
	second := mustDetectAs(t, baseDir, changed)

	if second.ServerID != first.ServerID {
		t.Fatalf("server ID after a MAC change = %q, want %q", second.ServerID, first.ServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("unexpected preserved copies %v: the identity should not have been replaced", names)
	}
	if !bytes.Equal(identityBytes(t, baseDir), before) {
		t.Fatalf("identity file was rewritten by a run that had nothing to add")
	}
}

// P6 is the hypothesis itself: the machine-id arm is emitted ALONGSIDE the uuid arms on a
// DMI host, not instead of them, so that host also survives losing its DMI and its MAC.
func TestP6_MachineIDArmIsEmittedOnADMIHostToo(t *testing.T) {
	baseDir := t.TempDir()
	host := fakeHost{
		machineID: factPresent("4444444444444444444444444444444d"),
		uuid:      factPresent("11111111-2222-3333-4444-555555555555"),
		hostname:  "node-dmi",
		macs:      []string{"aa:aa:aa:aa:aa:03"},
	}

	first := mustDetectAs(t, baseDir, host)
	keyField := storedKeyField(t, string(identityBytes(t, baseDir)))
	for _, label := range []string{uuidKeyLabel + "=", uuidNoHostKeyLabel + "=", machineIDKeyLabel + "="} {
		if !strings.Contains(keyField, label) {
			t.Fatalf("key field %q is missing %q", keyField, label)
		}
	}
	before := identityBytes(t, baseDir)

	degraded := host
	degraded.uuid = factAbsent()
	degraded.macs = []string{"aa:aa:aa:aa:aa:04"}
	second := mustDetectAs(t, baseDir, degraded)

	if second.ServerID != first.ServerID {
		t.Fatalf("server ID after losing DMI and changing MAC = %q, want %q", second.ServerID, first.ServerID)
	}
	if !bytes.Equal(identityBytes(t, baseDir), before) {
		t.Fatalf("identity file changed on a run that matched through the machine-id arm")
	}
}

// ============================================================================
// I3: a transient read failure never causes a rejection or a regeneration
// ============================================================================

// P7 pins that one machine-id blip costs ZERO identity changes, where it used to cost two.
func TestP7_AMachineIDBlipCostsNothing(t *testing.T) {
	baseDir := t.TempDir()

	first := mustDetectAs(t, baseDir, hostAlpha)
	before := identityBytes(t, baseDir)

	blind := hostAlpha
	blind.machineID = factUnreadable()
	during := mustDetectAs(t, baseDir, blind)
	if during.ServerID != first.ServerID {
		t.Fatalf("server ID during the blip = %q, want %q", during.ServerID, first.ServerID)
	}
	if !bytes.Equal(identityBytes(t, baseDir), before) {
		t.Fatalf("a blind run rewrote the identity file")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("a blind run replaced the identity file (preserved %v)", names)
	}

	after := mustDetectAs(t, baseDir, hostAlpha)
	if after.ServerID != first.ServerID {
		t.Fatalf("server ID after the blip = %q, want %q", after.ServerID, first.ServerID)
	}
}

// P7b pins the OTHER half of the same rule: a run that cannot read machine-id may not
// REPLACE a payload either, even one it could not decode. Only a run that reaches the
// replace path exercises that guard, and only an undecodable payload gets it there.
func TestP7b_ABlindRunNeverReplacesAPayloadItCannotDecode(t *testing.T) {
	baseDir := t.TempDir()
	seeded := seedCorruptIdentityFile(t, baseDir)

	blind := hostAlpha
	blind.machineID = factUnreadable()
	info := mustDetectAs(t, baseDir, blind)

	if !bytes.Equal(identityBytes(t, baseDir), seeded) {
		t.Fatalf("a blind run replaced a payload it had no standing to judge")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("a blind run preserved and replaced the payload (preserved %v)", names)
	}
	if info.ServerID == "" {
		t.Fatal("expected a working in-memory server ID even when nothing is persisted")
	}
}

// P8 pins the last unfixed half of change 3: the dbus copy is a fallback for an ABSENT
// primary, never a substitute for one we could not read. Substituting it rekeys every
// prefix on both sides and costs two on-disk identity changes for one blip.
func TestP8_DbusCopyNeverMasksAnUnreadablePrimary(t *testing.T) {
	baseDir := t.TempDir()
	host := fakeHost{
		machineID: factPresent("aaaabbbbccccddddeeeeffff00001111"),
		dbus:      factPresent("99998888777766665555444433332222"),
		uuid:      factPresent("99999999-8888-7777-6666-555555555555"),
		hostname:  "node-dbus",
		macs:      []string{"aa:aa:aa:aa:aa:05"},
	}

	first := mustDetectAs(t, baseDir, host)
	before := identityBytes(t, baseDir)

	blip := host
	blip.machineID = factUnreadable()
	during := mustDetectAs(t, baseDir, blip)
	if during.ServerID != first.ServerID {
		t.Fatalf("server ID during the primary blip = %q, want %q", during.ServerID, first.ServerID)
	}
	if !bytes.Equal(identityBytes(t, baseDir), before) {
		t.Fatalf("the identity file changed during a primary machine-id blip")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("the identity was replaced during a primary machine-id blip (preserved %v)", names)
	}

	after := mustDetectAs(t, baseDir, host)
	if after.ServerID != first.ServerID {
		t.Fatalf("server ID after the primary blip = %q, want %q", after.ServerID, first.ServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("the healthy run rejected the file the blip left behind (preserved %v)", names)
	}
}

// P9 pins that an unreadable product_uuid DOES excuse the payload it could actually be the
// binding of: a labelled payload carrying uuid arms and no machineid arm.
func TestP9_ProductUUIDBlipExcusesAPayloadCarryingUUIDArms(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("5555555555555555555555555555555e"),
		uuid:      factPresent("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		hostname:  "node-legacy",
		macs:      []string{"aa:aa:aa:aa:aa:06"},
	}
	const serverID = "7777666655554444"
	wf := writer.facts(t)
	seedIdentityFile(t, baseDir, serverID, keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel))

	reader := writer
	reader.uuid = factUnreadable()
	reader.macs = []string{"aa:aa:aa:aa:aa:07"}

	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID != serverID {
		t.Fatalf("server ID = %q, want the stored %q", info.ServerID, serverID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("the payload was replaced during a product_uuid blip (preserved %v)", names)
	}
}

// ============================================================================
// I4: a machine never adopts a payload written by a different machine
// ============================================================================

// P10 is proven adoption one in its UNLABELLED v1 shape. A v1 payload is a mac-arm-only
// payload with the label stripped: product_uuid is not one of its inputs, so an unreadable
// or denied product_uuid can never explain its mismatch, on ANY host class.
func TestP10_ForeignV1PayloadIsNotExcusedByAProductUUIDBlip(t *testing.T) {
	const foreignServerID = "7777666655554444"

	rows := []struct {
		name string
		uuid sysFact
	}{
		{"dmi host, product_uuid unreadable", factUnreadable()},
		{"unprivileged container, product_uuid denied", factDenied()},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			baseDir := t.TempDir()
			// Machine A wrote this file. Machine B is about to be offered it.
			seeded := seedIdentityFile(t, baseDir, foreignServerID,
				v1KeyField("1111111111111111111111111111111a", "node-a", "aa:aa:aa:aa:aa:aa"))

			reader := fakeHost{
				machineID: factPresent("2222222222222222222222222222222b"),
				uuid:      row.uuid,
				hostname:  "node-b",
				macs:      []string{"bb:bb:bb:bb:bb:bb"},
			}
			info := mustDetectAs(t, baseDir, reader)
			if info.ServerID == foreignServerID {
				t.Fatalf("adopted the foreign v1 server ID %q", foreignServerID)
			}
			names := rejectedNames(t, baseDir)
			if len(names) != 1 {
				t.Fatalf("preserved copies = %v, want exactly one", names)
			}
			preserved, err := os.ReadFile(filepath.Join(identityDirOf(baseDir), names[0]))
			if err != nil {
				t.Fatalf("read preserved copy: %v", err)
			}
			if !bytes.Equal(preserved, seeded) {
				t.Fatalf("the foreign v1 payload was not preserved byte for byte")
			}
		})
	}
}

// P11 is proven adoption one in its LABELLED shape: MAC arms alone were never keyed on
// product_uuid either.
func TestP11_ForeignMACOnlyPayloadIsNotExcusedByAProductUUIDBlip(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factAbsent(),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seedIdentityFile(t, baseDir, foreignServerID, keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel))

	reader := fakeHost{
		machineID: factPresent("2222222222222222222222222222222b"),
		uuid:      factUnreadable(),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID == foreignServerID {
		t.Fatalf("adopted the foreign MAC-only server ID %q", foreignServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
}

// P12 is proven adoption two: inside an unprivileged container product_uuid is DENIED, not
// missing, and a denial is a durable fact, so the blindness excuse cannot fire there at all.
func TestP12_ForeignUUIDPayloadIsNotAdoptedInsideAContainer(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factPresent("aaaaaaaa-1111-2222-3333-444444444444"),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seedIdentityFile(t, baseDir, foreignServerID, keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel))

	reader := fakeHost{
		machineID: factPresent("2222222222222222222222222222222b"),
		uuid:      factDenied(),
		hostname:  "ct-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID == foreignServerID {
		t.Fatalf("adopted the foreign server ID %q inside a user namespace", foreignServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
}

// P13 pins the machineid conjunct: positive proof beats an excuse about a different fact.
// A payload carrying a machineid arm this run CAN recompute, that did not match, was
// written under a different machine-id, and a product_uuid blip excuses nothing.
func TestP13_ForeignNewFormatPayloadIsNotExcusedByAProductUUIDBlip(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factPresent("aaaaaaaa-1111-2222-3333-444444444444"),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seeded := seedIdentityFile(t, baseDir, foreignServerID, identityKeyFieldFor(wf, writer.macs, writer.macs[0]))
	if !strings.Contains(storedKeyField(t, string(seeded)), machineIDKeyLabel+"=") {
		t.Fatal("fixture error: the seeded payload must carry a machineid arm")
	}

	reader := fakeHost{
		machineID: factPresent("2222222222222222222222222222222b"),
		uuid:      factUnreadable(),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID == foreignServerID {
		t.Fatalf("adopted the foreign server ID %q", foreignServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
}

// P13b is P13's other half. P13 pins the reader whose machine-id is PRESENT; this pins the
// reader whose machine-id is ABSENT. Absent is a durable fact about this host, not
// blindness, so a host that has no machine-id at all meeting a payload that carries a
// machineid arm has positive proof the payload was written elsewhere. Rule two of
// uuidBlindnessExplains must therefore turn on "not unreadable", not on "present":
// gating it on systemFilePresent hands the whole excuse back to exactly this reader.
func TestP13b_ForeignPayloadIsNotAdoptedByAHostWithNoMachineID(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factPresent("aaaaaaaa-1111-2222-3333-444444444444"),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seeded := seedIdentityFile(t, baseDir, foreignServerID, identityKeyFieldFor(wf, writer.macs, writer.macs[0]))
	stored := storedKeyField(t, string(seeded))
	if !strings.Contains(stored, machineIDKeyLabel+"=") || !strings.Contains(stored, uuidKeyLabel+"=") {
		t.Fatal("fixture error: the seeded payload must carry both a machineid arm and a uuid arm")
	}

	reader := fakeHost{
		machineID: factAbsent(),
		uuid:      factUnreadable(),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID == foreignServerID {
		t.Fatalf("adopted the foreign server ID %q: this host has no machine-id, so a payload carrying a machineid arm cannot be its own", foreignServerID)
	}
	if names := rejectedNames(t, baseDir); len(names) != 1 {
		t.Fatalf("preserved copies = %v, want exactly one", names)
	}
}

// ============================================================================
// I5: a machine never loses an identity it should have kept
// ============================================================================

// P14 covers two clauses of I5 with two different carriers, so a mutation of either one is
// visible. Row one has no machine-id at all, so only the "_nohost" twins can save it from
// a hostname change. Row two has no DMI, so only the hostname free machine-id arm can save
// it from a hostname change PLUS a MAC change.
func TestP14_HostnameAndMACChangesKeepTheIdentity(t *testing.T) {
	t.Run("hostname change alone, carried by the nohost twins", func(t *testing.T) {
		baseDir := t.TempDir()
		host := fakeHost{
			machineID: factAbsent(),
			uuid:      factPresent("cccccccc-1111-2222-3333-444444444444"),
			hostname:  "before",
			macs:      []string{"aa:aa:aa:aa:aa:08"},
		}
		first := mustDetectAs(t, baseDir, host)

		renamed := host
		renamed.hostname = "after"
		second := mustDetectAs(t, baseDir, renamed)
		if second.ServerID != first.ServerID {
			t.Fatalf("server ID after a hostname change = %q, want %q", second.ServerID, first.ServerID)
		}
		if names := rejectedNames(t, baseDir); len(names) != 0 {
			t.Fatalf("a hostname change replaced the identity (preserved %v)", names)
		}
	})

	t.Run("hostname and MAC change, carried by the machine-id arm", func(t *testing.T) {
		baseDir := t.TempDir()
		host := fakeHost{
			machineID: factPresent("6666666666666666666666666666666f"),
			uuid:      factAbsent(),
			hostname:  "before",
			macs:      []string{"aa:aa:aa:aa:aa:09"},
		}
		first := mustDetectAs(t, baseDir, host)

		moved := host
		moved.hostname = "after"
		moved.macs = []string{"aa:aa:aa:aa:aa:0a"}
		second := mustDetectAs(t, baseDir, moved)
		if second.ServerID != first.ServerID {
			t.Fatalf("server ID after a hostname and MAC change = %q, want %q", second.ServerID, first.ServerID)
		}
		if names := rejectedNames(t, baseDir); len(names) != 0 {
			t.Fatalf("a hostname and MAC change replaced the identity (preserved %v)", names)
		}
	})
}

// P15 pins the rewrite: a v1 payload of THIS host loads, is upgraded in place without
// changing the server ID, gains the machine-id arm, and is then latched. The second row
// pins that the upgrade is ADDITIVE: a stored uuid arm survives a run that could not read
// product_uuid.
func TestP15_LegacyPayloadIsUpgradedInPlaceAndLatched(t *testing.T) {
	t.Run("v1 payload of this host", func(t *testing.T) {
		baseDir := t.TempDir()
		host := fakeHost{
			machineID: factPresent("7777777777777777777777777777777a"),
			uuid:      factAbsent(),
			hostname:  "node-v1",
			macs:      []string{"aa:aa:aa:aa:aa:0b"},
		}
		const serverID = "1234567890123456"
		bare := v1KeyField("7777777777777777777777777777777a", "node-v1", host.macs[0])
		seedIdentityFile(t, baseDir, serverID, bare)

		info := mustDetectAs(t, baseDir, host)
		if info.ServerID != serverID {
			t.Fatalf("server ID = %q, want the stored %q", info.ServerID, serverID)
		}
		if names := rejectedNames(t, baseDir); len(names) != 0 {
			t.Fatalf("the v1 payload was replaced instead of upgraded (preserved %v)", names)
		}

		upgraded := identityBytes(t, baseDir)
		keyField := storedKeyField(t, string(upgraded))
		if !strings.Contains(keyField, "=") {
			t.Fatalf("key field %q was not upgraded to the labelled format", keyField)
		}
		if !keyFieldHasLabel(keyField, machineIDKeyLabel) {
			t.Fatalf("key field %q did not gain the machine-id arm", keyField)
		}
		found := false
		for _, entry := range splitKeyFieldEntries(keyField) {
			if entry.Prefix == bare && strings.HasPrefix(entry.Label, "mac") {
				found = true
			}
		}
		if !found {
			t.Fatalf("key field %q dropped the prefix that matched (%q)", keyField, bare)
		}

		mustDetectAs(t, baseDir, host)
		if !bytes.Equal(identityBytes(t, baseDir), upgraded) {
			t.Fatalf("the rewrite is not latched: a second run rewrote the file again")
		}
	})

	t.Run("v1 payload on a host with no machine-id", func(t *testing.T) {
		// With no machine-id there is no machine-id arm to add, so the FORMAT trigger is
		// the only thing that can upgrade this payload. Without it the host keeps a
		// single bare prefix forever and loses its alternate MAC and uuid arms.
		baseDir := t.TempDir()
		host := fakeHost{
			machineID: factAbsent(),
			uuid:      factPresent("eeeeeeee-1111-2222-3333-444444444444"),
			hostname:  "novmid",
			macs:      []string{"aa:aa:aa:aa:aa:0d"},
		}
		const serverID = "3344556677889900"
		seedIdentityFile(t, baseDir, serverID, v1KeyField("", "novmid", host.macs[0]))

		info := mustDetectAs(t, baseDir, host)
		if info.ServerID != serverID {
			t.Fatalf("server ID = %q, want the stored %q", info.ServerID, serverID)
		}
		upgraded := identityBytes(t, baseDir)
		keyField := storedKeyField(t, string(upgraded))
		if !strings.Contains(keyField, "=") {
			t.Fatalf("key field %q was not upgraded to the labelled format", keyField)
		}
		for _, label := range []string{uuidKeyLabel, uuidNoHostKeyLabel} {
			if !keyFieldHasLabel(keyField, label) {
				t.Fatalf("upgraded key field %q is missing the %s arm", keyField, label)
			}
		}
		// The MAC family must survive with BOTH its hostname bearing and its hostname
		// free entry; the label carries an index that depends on primary MAC selection,
		// so the pin is on the family and not on one name.
		macHost, macNoHost := false, false
		for _, entry := range splitKeyFieldEntries(keyField) {
			if !strings.HasPrefix(entry.Label, "mac") {
				continue
			}
			if strings.HasSuffix(entry.Label, "_nohost") {
				macNoHost = true
			} else {
				macHost = true
			}
		}
		if !macHost || !macNoHost {
			t.Fatalf("upgraded key field %q lost part of the MAC family", keyField)
		}
		if keyFieldHasLabel(keyField, machineIDKeyLabel) {
			t.Fatalf("key field %q gained a machine-id arm on a host with no machine-id", keyField)
		}

		mustDetectAs(t, baseDir, host)
		if !bytes.Equal(identityBytes(t, baseDir), upgraded) {
			t.Fatalf("the format upgrade is not latched: a second run rewrote the file again")
		}
	})

	t.Run("adding the arm keeps a stored uuid arm the run cannot recompute", func(t *testing.T) {
		baseDir := t.TempDir()
		writer := fakeHost{
			machineID: factPresent("8888888888888888888888888888888b"),
			uuid:      factPresent("dddddddd-1111-2222-3333-444444444444"),
			hostname:  "node-uuid",
			macs:      []string{"aa:aa:aa:aa:aa:0c"},
		}
		const serverID = "2233445566778899"
		wf := writer.facts(t)
		storedKF := keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel)
		seedIdentityFile(t, baseDir, serverID, storedKF)

		// Same host, same MAC, but product_uuid is unreadable this run: the MAC arm
		// verifies the load, so the machine-id arm is added, and the union must keep the
		// uuid arms this run could not derive.
		blind := writer
		blind.uuid = factUnreadable()
		info := mustDetectAs(t, baseDir, blind)
		if info.ServerID != serverID {
			t.Fatalf("server ID = %q, want %q", info.ServerID, serverID)
		}
		keyField := storedKeyField(t, string(identityBytes(t, baseDir)))
		if !keyFieldHasLabel(keyField, machineIDKeyLabel) {
			t.Fatalf("key field %q did not gain the machine-id arm", keyField)
		}
		for _, label := range []string{uuidKeyLabel, uuidNoHostKeyLabel} {
			if !keyFieldHasLabel(keyField, label) {
				t.Fatalf("key field %q dropped the stored %s arm", keyField, label)
			}
		}
	})
}

// ============================================================================
// I6: the write side and the read side agree, within a run and across runs
// ============================================================================

func agreementShapes() []struct {
	name string
	host fakeHost
} {
	return []struct {
		name string
		host fakeHost
	}{
		{"dmi and machine-id", fakeHost{
			machineID: factPresent("aaaa1111aaaa1111aaaa1111aaaa1111"),
			uuid:      factPresent("11111111-1111-1111-1111-111111111111"),
			hostname:  "shape-1",
			macs:      []string{"aa:aa:aa:aa:0a:01"},
		}},
		{"machine-id, no dmi", fakeHost{
			machineID: factPresent("bbbb2222bbbb2222bbbb2222bbbb2222"),
			uuid:      factAbsent(),
			hostname:  "shape-2",
			macs:      []string{"aa:aa:aa:aa:0a:02"},
		}},
		{"no machine-id, dmi and macs", fakeHost{
			machineID: factAbsent(),
			uuid:      factPresent("33333333-3333-3333-3333-333333333333"),
			hostname:  "shape-3",
			macs:      []string{"aa:aa:aa:aa:0a:03"},
		}},
		{"no machine-id, macs only", fakeHost{
			machineID: factAbsent(),
			uuid:      factAbsent(),
			hostname:  "shape-4",
			macs:      []string{"aa:aa:aa:aa:0a:04"},
		}},
		{"no machine-id, no dmi, no macs", fakeHost{
			machineID: factAbsent(),
			uuid:      factAbsent(),
			hostname:  "shape-5",
		}},
		{"dmi, machine-id and two macs", fakeHost{
			machineID: factPresent("dddd4444dddd4444dddd4444dddd4444"),
			uuid:      factPresent("44444444-4444-4444-4444-444444444444"),
			hostname:  "shape-7",
			macs:      []string{"aa:aa:aa:aa:0a:07", "aa:aa:aa:aa:0a:08"},
		}},
		{"machine-id, no macs at all", fakeHost{
			machineID: factPresent("cccc3333cccc3333cccc3333cccc3333"),
			uuid:      factAbsent(),
			hostname:  "shape-6",
		}},
	}
}

// P16 pins that a payload this machine wrote loads on its NEXT run under the same
// conditions, for every host class, and that a run with MACs always reports a primary MAC.
func TestP16_WriteSideAndReadSideAgreeAcrossRuns(t *testing.T) {
	for _, shape := range agreementShapes() {
		t.Run(shape.name, func(t *testing.T) {
			baseDir := t.TempDir()
			first := mustDetectAs(t, baseDir, shape.host)
			if first.ServerID == "" {
				t.Fatal("first run produced no server ID")
			}
			before := identityBytes(t, baseDir)

			second := mustDetectAs(t, baseDir, shape.host)
			if second.ServerID != first.ServerID {
				t.Fatalf("server ID = %q on the second run, want %q", second.ServerID, first.ServerID)
			}
			if names := rejectedNames(t, baseDir); len(names) != 0 {
				t.Fatalf("the second run replaced its own file (preserved %v)", names)
			}
			if !bytes.Equal(identityBytes(t, baseDir), before) {
				t.Fatalf("the second run rewrote its own file")
			}
			if len(shape.host.macs) > 0 && strings.TrimSpace(second.PrimaryMAC) == "" {
				t.Fatalf("Info.PrimaryMAC is empty on a host with MACs")
			}
		})
	}
}

// ============================================================================
// I7: the failure direction is safe
// ============================================================================

// P17 pins that a replacement is refused when the payload cannot be preserved, because a
// payload we cannot preserve is one that replacing would destroy.
func TestP17_ReplacementIsRefusedWhenPreservationFails(t *testing.T) {
	baseDir := t.TempDir()
	mustDetectAs(t, baseDir, hostAlpha)
	original := identityBytes(t, baseDir)

	failTempFor(t, true, errors.New("quarantine write boom"))
	info, err := detectAs(t, baseDir, hostBravo)
	if err != nil {
		t.Fatalf("DetectWithContext() error = %v, want nil", err)
	}
	if !bytes.Equal(identityBytes(t, baseDir), original) {
		t.Fatalf("the canonical payload was replaced even though preservation failed")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("unexpected preserved copies %v", names)
	}
	if storedServerID(t, string(original)) == info.ServerID {
		t.Fatalf("the new server ID reached disk after a refused replacement")
	}
}

// P17b pins the other half of I1's "never destroy": a preserved name that already holds
// DIFFERENT bytes is an error, never an overwrite. The name is 64 bits of the payload's
// sha256, so two distinct payloads colliding is not reachable in practice; the branch
// exists for a preserved copy that something else truncated or rewrote, and without this
// pin the guard can be turned into a silent overwrite with the suite still green.
// Composed with P17, which pins that a preservation failure then refuses the replacement,
// the pair covers the whole invariant.
func TestP17b_APreservedNameHoldingDifferentBytesIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("the payload this run is preserving")

	name, err := quarantineIdentityPayload(dir, payload, nil)
	if err != nil {
		t.Fatalf("first preservation failed: %v", err)
	}
	preserved := filepath.Join(dir, name)

	// Re-preserving the same bytes is idempotent: the content addressed name means the
	// same payload always lands on the same file.
	if again, err := quarantineIdentityPayload(dir, payload, nil); err != nil || again != name {
		t.Fatalf("re-preserving the same payload = (%q, %v), want (%q, nil)", again, err, name)
	}

	// Something else rewrote the preserved copy. The next preservation of the ORIGINAL
	// payload must refuse rather than overwrite what is now a different file.
	if err := os.WriteFile(preserved, []byte("different bytes, same name"), 0o600); err != nil {
		t.Fatalf("could not rewrite the preserved copy: %v", err)
	}
	if _, err := quarantineIdentityPayload(dir, payload, nil); err == nil {
		t.Fatal("preserving over a name that holds different bytes returned no error: a preserved payload was destroyed")
	}
	got, err := os.ReadFile(preserved)
	if err != nil {
		t.Fatalf("reading the preserved copy back: %v", err)
	}
	if string(got) != "different bytes, same name" {
		t.Fatalf("the preserved copy was overwritten: %q", got)
	}
}

// P18 pins that a payload this run cannot READ is never replaced, in the two shapes that
// produce a non-NotExist error from the confined read.
func TestP18_ReplacementIsRefusedWhenTheExistingPayloadCannotBeRead(t *testing.T) {
	t.Run("symlink pointing outside the identity directory", func(t *testing.T) {
		baseDir := t.TempDir()
		outside := filepath.Join(baseDir, "elsewhere")
		if err := os.WriteFile(outside, []byte("OUTSIDE\n"), 0o600); err != nil {
			t.Fatalf("seed outside file: %v", err)
		}
		if err := os.MkdirAll(identityDirOf(baseDir), 0o750); err != nil {
			t.Fatalf("mkdir identity dir: %v", err)
		}
		if err := os.Symlink(outside, identityPathOf(baseDir)); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		if _, err := detectAs(t, baseDir, hostAlpha); err != nil {
			t.Fatalf("DetectWithContext() error = %v, want nil", err)
		}
		target, err := os.Readlink(identityPathOf(baseDir))
		if err != nil {
			t.Fatalf("the identity path is no longer a symlink: %v", err)
		}
		if target != outside {
			t.Fatalf("symlink target = %q, want %q", target, outside)
		}
		if names := rejectedNames(t, baseDir); len(names) != 0 {
			t.Fatalf("unexpected preserved copies %v", names)
		}
	})

	t.Run("identity path is a directory", func(t *testing.T) {
		baseDir := t.TempDir()
		if err := os.MkdirAll(identityPathOf(baseDir), 0o750); err != nil {
			t.Fatalf("mkdir identity path: %v", err)
		}

		if _, err := detectAs(t, baseDir, hostAlpha); err != nil {
			t.Fatalf("DetectWithContext() error = %v, want nil", err)
		}
		st, err := os.Lstat(identityPathOf(baseDir))
		if err != nil {
			t.Fatalf("lstat identity path: %v", err)
		}
		if !st.IsDir() {
			t.Fatalf("the identity path is no longer a directory (mode=%s)", st.Mode())
		}
		if names := rejectedNames(t, baseDir); len(names) != 0 {
			t.Fatalf("unexpected preserved copies %v", names)
		}
	})
}

// P19 pins that a blind run never LAUNDERS an unverified acceptance into a binding.
func TestP19_ABlindRunNeverLaundersAnUnverifiedAcceptance(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factAbsent(),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seeded := seedIdentityFile(t, baseDir, foreignServerID, keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel))

	blind := fakeHost{
		machineID: factUnreadable(),
		uuid:      factAbsent(),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, blind)
	if info.ServerID != foreignServerID {
		t.Fatalf("a blind run must keep the stored server ID, got %q", info.ServerID)
	}
	after := identityBytes(t, baseDir)
	if !bytes.Equal(after, seeded) {
		t.Fatalf("a blind run rewrote the payload it could not judge")
	}
	if keyFieldHasLabel(storedKeyField(t, string(after)), machineIDKeyLabel) {
		t.Fatalf("a blind run bound a foreign payload to this host")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("a blind run replaced the file (preserved %v)", names)
	}
}

// P19b pins the Verified guard on its own. Here the run DOES hold a machine-id, so it
// would happily add its own machine-id arm to a payload it only accepted because it could
// not read product_uuid. That rewrite would make the NEXT run accept the same foreign
// payload as verified, which is how an excuse becomes a permanent adoption.
func TestP19b_AnUnverifiedAcceptanceIsNeverBoundToThisHost(t *testing.T) {
	baseDir := t.TempDir()
	writer := fakeHost{
		machineID: factPresent("1111111111111111111111111111111a"),
		uuid:      factPresent("aaaaaaaa-1111-2222-3333-444444444444"),
		hostname:  "node-a",
		macs:      []string{"aa:aa:aa:aa:aa:aa"},
	}
	const foreignServerID = "7777666655554444"
	wf := writer.facts(t)
	seeded := seedIdentityFile(t, baseDir, foreignServerID, keyFieldMinus(wf, writer.macs, writer.macs[0], machineIDKeyLabel))

	reader := fakeHost{
		machineID: factPresent("2222222222222222222222222222222b"),
		uuid:      factUnreadable(),
		hostname:  "node-b",
		macs:      []string{"bb:bb:bb:bb:bb:bb"},
	}
	info := mustDetectAs(t, baseDir, reader)
	if info.ServerID != foreignServerID {
		t.Fatalf("a real product_uuid blip must keep the stored server ID, got %q", info.ServerID)
	}
	after := identityBytes(t, baseDir)
	if !bytes.Equal(after, seeded) {
		t.Fatalf("an unverified acceptance was rewritten")
	}
	if keyFieldHasLabel(storedKeyField(t, string(after)), machineIDKeyLabel) {
		t.Fatalf("an unverified acceptance was bound to this host with its machine-id arm")
	}
	if names := rejectedNames(t, baseDir); len(names) != 0 {
		t.Fatalf("unexpected preserved copies %v", names)
	}
}

// ============================================================================
// Unit pins. These three sit BELOW the readFirstLineFunc seam, or below
// DetectWithContext, and exist precisely because that seam bypasses the real
// reader (P20 and P22) or because the property is invisible from outside (P21).
// ============================================================================

// P20 pins the error classification and the two mappings that replace the whole
// /proc/self/uid_map discriminator.
func TestP20_ErrorClassification(t *testing.T) {
	rows := []struct {
		name string
		err  error
		want systemFileState
	}{
		{"not exist", &os.PathError{Op: "open", Err: syscall.ENOENT}, systemFileAbsent},
		{"fs.ErrNotExist", fs.ErrNotExist, systemFileAbsent},
		{"eacces", &os.PathError{Op: "open", Err: syscall.EACCES}, systemFileDenied},
		{"eperm", &os.PathError{Op: "open", Err: syscall.EPERM}, systemFileDenied},
		{"eio", &os.PathError{Op: "read", Err: syscall.EIO}, systemFileUnreadable},
		{"eisdir", &os.PathError{Op: "read", Err: syscall.EISDIR}, systemFileUnreadable},
	}
	for _, row := range rows {
		if got := classifyReadError(row.err); got != row.want {
			t.Errorf("classifyReadError(%s) = %s, want %s", row.name, got, row.want)
		}
	}

	orig := readFirstLineFunc
	t.Cleanup(func() { readFirstLineFunc = orig })
	readFirstLineFunc = func(string, int) (string, systemFileState) { return "", systemFileDenied }

	// A denied machine-id is BLINDNESS: keying prefixes on the empty string would reject
	// a good payload and mint a replacement.
	if _, state := readMachineID(nil); state != systemFileUnreadable {
		t.Errorf("readMachineID() on a denial = %s, want unreadable", state)
	}
	// A denied product_uuid is a DURABLE host fact: the DMI table is not visible to this
	// process and will not be next run either, so the excuse must not stay open forever.
	if _, state := readProductUUID(nil); state != systemFileAbsent {
		t.Errorf("readProductUUID() on a denial = %s, want absent", state)
	}
}

// P21 pins ARM SYMMETRY: the two projections of identityKeyArms cannot drift. It is nearly
// a tautology under the single builder, which is the point: it goes red the moment someone
// reintroduces a second computation.
func TestP21_KeyFieldAndPrefixSetAreTwoProjectionsOfOneArmList(t *testing.T) {
	for _, shape := range agreementShapes() {
		t.Run(shape.name, func(t *testing.T) {
			f := shape.host.facts(t)
			primary := ""
			if len(shape.host.macs) > 0 {
				primary = shape.host.macs[0]
			}
			fromField := make(map[string]bool)
			for _, prefix := range parseKeyFieldPrefixes(identityKeyFieldFor(f, shape.host.macs, primary)) {
				if prefix != "" {
					fromField[prefix] = true
				}
			}
			fromSet := identityKeyPrefixesFor(f, shape.host.macs, primary)
			if len(fromField) != len(fromSet) {
				t.Fatalf("prefix count: key field has %d, match set has %d", len(fromField), len(fromSet))
			}
			for prefix := range fromSet {
				if !fromField[prefix] {
					t.Fatalf("prefix %q is matched but never written", prefix)
				}
			}
		})
	}
}

// P22 pins the readMachineID state matrix. The fifth row is the one the dbus substitution
// got wrong, and it is what P8 observes end to end.
func TestP22_ReadMachineIDStateMatrix(t *testing.T) {
	rows := []struct {
		name      string
		etc       sysFact
		dbus      sysFact
		wantValue string
		wantState systemFileState
		wantDbus  bool
	}{
		{"etc present", factPresent("etc-value"), factPresent("dbus-value"), "etc-value", systemFilePresent, false},
		{"etc absent, dbus present", factAbsent(), factPresent("dbus-value"), "dbus-value", systemFilePresent, true},
		{"etc absent, dbus absent", factAbsent(), factAbsent(), "", systemFileAbsent, true},
		{"etc absent, dbus unreadable", factAbsent(), factUnreadable(), "", systemFileUnreadable, true},
		{"etc unreadable, dbus present", factUnreadable(), factPresent("dbus-value"), "", systemFileUnreadable, false},
		{"etc denied, dbus present", factDenied(), factPresent("dbus-value"), "", systemFileUnreadable, false},
	}

	orig := readFirstLineFunc
	t.Cleanup(func() { readFirstLineFunc = orig })

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			consultedDbus := false
			readFirstLineFunc = func(path string, limit int) (string, systemFileState) {
				switch path {
				case machineIDPath:
					return row.etc.value, row.etc.state
				case dbusMachineIDPath:
					consultedDbus = true
					return row.dbus.value, row.dbus.state
				default:
					return "", systemFileAbsent
				}
			}
			value, state := readMachineID(nil)
			if value != row.wantValue || state != row.wantState {
				t.Fatalf("readMachineID() = (%q, %s), want (%q, %s)", value, state, row.wantValue, row.wantState)
			}
			if consultedDbus != row.wantDbus {
				t.Fatalf("consulted the dbus copy = %v, want %v", consultedDbus, row.wantDbus)
			}
		})
	}
}
