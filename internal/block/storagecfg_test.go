package block

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The .pw files PVE writes hold the 36-character token secret plus one newline (37 bytes).
const (
	testSecret     = "6f0c2d9e-41b7-4a8e-9c35-d27e80b1f4a6"
	testHostname   = "pve.lan.local"
	testServerFP   = "3e:41:9a:07:c2:5d:88:16:f0:2b:6c:d4:91:ae:57:03:bb:68:1f:e9:24:7a:c5:30:0d:96:4f:e2:b8:15:73:ca"
	testEncKeyFP   = "5a:73:a8:e0:95:24:2f:97:61:c8:0e:3b:d2:44:9f:17:a6:05:7e:c1:38:fb:52:9d:e7:20:84:6b:13:ca:0d:b5"
	testEncKeyBody = `{"kdf":null,"created":"2026-10-02T15:44:10+02:00","modified":"2026-10-02T15:44:10+02:00","data":"AAAA","fingerprint":"` + testEncKeyFP + `"}`
)

// newPVEConfigDir builds a PVE_CONFIG_PATH tree from testdata/pve/storage.cfg and the
// given files under priv/storage.
func newPVEConfigDir(t *testing.T, priv map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	cfg, err := os.ReadFile(filepath.Join("testdata", "pve", "storage.cfg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "storage.cfg"), cfg, 0o640); err != nil {
		t.Fatalf("write storage.cfg: %v", err)
	}
	privDir := filepath.Join(dir, "priv", "storage")
	if err := os.MkdirAll(privDir, 0o700); err != nil {
		t.Fatalf("mkdir priv/storage: %v", err)
	}
	for name, body := range priv {
		if err := os.WriteFile(filepath.Join(privDir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestParseStorageConfigReadsTheMeasuredFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "pve", "storage.cfg"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	sections := ParseStorageConfig(data)
	var got []string
	for _, s := range sections {
		got = append(got, s.Type+":"+s.ID)
	}
	want := "dir:local lvmthin:local-lvm pbs:proxsave-probe pbs:proxsave-probe-enc pbs:proxsave-r2-enc pbs:proxsave-off"
	if strings.Join(got, " ") != want {
		t.Fatalf("sections = %v, want %s", got, want)
	}
	probe := sections[2].Properties
	for key, value := range map[string]string{
		"datastore": "proxsave-probe", "server": "127.0.0.1", "content": "backup",
		"fingerprint": testServerFP, "namespace": "proxsave-probe", "username": "proxsave-probe@pbs!pve",
	} {
		if probe[key] != value {
			t.Errorf("proxsave-probe %s = %q, want %q", key, probe[key], value)
		}
	}
	if _, ok := probe["port"]; ok {
		t.Error("proxsave-probe has no port line, but a port was parsed")
	}
	if sections[3].Properties["port"] != "8007" || sections[3].Properties["encryption-key"] != testEncKeyFP {
		t.Errorf("proxsave-probe-enc port/encryption-key = %q/%q", sections[3].Properties["port"], sections[3].Properties["encryption-key"])
	}
	if sections[4].Properties["master-pubkey"] != "1" {
		t.Errorf("proxsave-r2-enc master-pubkey = %q, want 1", sections[4].Properties["master-pubkey"])
	}
	if value, ok := sections[5].Properties["disable"]; !ok || value != "" {
		t.Errorf("proxsave-off disable = %q (present %v), want a flag line", value, ok)
	}
}

func TestParseStorageConfigWithoutBlankLines(t *testing.T) {
	data := "# written by hand\n" +
		"dir: local\r\n\tpath /var/lib/vz\r\n" +
		"pbs: one\n\tserver 10.0.0.1\n  datastore ds1\n" +
		"pbs: two\n\tserver   10.0.0.2  \n\tdatastore ds2"
	sections := ParseStorageConfig([]byte(data))
	if len(sections) != 3 {
		t.Fatalf("got %d sections, want 3: %+v", len(sections), sections)
	}
	if sections[0].Properties["path"] != "/var/lib/vz" {
		t.Errorf("local path = %q", sections[0].Properties["path"])
	}
	if sections[1].Properties["server"] != "10.0.0.1" || sections[1].Properties["datastore"] != "ds1" {
		t.Errorf("one = %+v", sections[1].Properties)
	}
	if sections[2].Properties["server"] != "10.0.0.2" || sections[2].Properties["datastore"] != "ds2" {
		t.Errorf("two = %+v", sections[2].Properties)
	}
}

// After a blank line an indented line belongs to no section, as in PVE's own parser.
func TestParseStorageConfigIgnoresPropertiesAfterTheBlankLine(t *testing.T) {
	sections := ParseStorageConfig([]byte("pbs: one\n\tserver 10.0.0.1\n\n\tdatastore stray\n"))
	if len(sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(sections))
	}
	if _, ok := sections[0].Properties["datastore"]; ok {
		t.Fatal("a property after the blank line was attached to the section")
	}
}

func TestResolvePBSTargetPlain(t *testing.T) {
	dir := newPVEConfigDir(t, map[string]string{"proxsave-probe.pw": testSecret + "\n"})
	target, fact := ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true})
	if fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	want := PBSTarget{
		StorageID:   "proxsave-probe",
		Server:      "127.0.0.1",
		Datastore:   "proxsave-probe",
		Namespace:   "proxsave-probe",
		Username:    "proxsave-probe@pbs!pve",
		Repository:  "proxsave-probe@pbs!pve@127.0.0.1:proxsave-probe",
		Fingerprint: testServerFP,
		Password:    testSecret,
		BackupID:    "proxsave-pve.lan.local",
	}
	if target != want {
		t.Fatalf("target =\n%+v\nwant\n%+v", target, want)
	}
}

func TestResolvePBSTargetWithKeyAndPort(t *testing.T) {
	dir := newPVEConfigDir(t, map[string]string{
		"proxsave-probe-enc.pw":  testSecret + "\n",
		"proxsave-probe-enc.enc": testEncKeyBody,
	})
	target, fact := ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-probe-enc", Hostname: testHostname, IsPVEHost: true})
	if fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	if target.Repository != "proxsave-probe@pbs!pve@127.0.0.1:8007:proxsave-probe" {
		t.Errorf("Repository = %q", target.Repository)
	}
	if want := filepath.Join(dir, "priv", "storage", "proxsave-probe-enc.enc"); target.Keyfile != want {
		t.Errorf("Keyfile = %q, want %q", target.Keyfile, want)
	}
	if target.MasterPubkeyFile != "" {
		t.Errorf("MasterPubkeyFile = %q, want empty (no .master.pem)", target.MasterPubkeyFile)
	}
	if target.EncryptionKey != testEncKeyFP || target.Namespace != "proxsave-probe-enc" {
		t.Errorf("EncryptionKey/Namespace = %q/%q", target.EncryptionKey, target.Namespace)
	}
}

func TestResolvePBSTargetMasterPubkey(t *testing.T) {
	dir := newPVEConfigDir(t, map[string]string{
		"proxsave-r2-enc.pw":         testSecret + "\n",
		"proxsave-r2-enc.enc":        testEncKeyBody,
		"proxsave-r2-enc.master.pem": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n\n",
	})
	target, fact := ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-r2-enc", Hostname: testHostname, IsPVEHost: true})
	if fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	if want := filepath.Join(dir, "priv", "storage", "proxsave-r2-enc.master.pem"); target.MasterPubkeyFile != want {
		t.Errorf("MasterPubkeyFile = %q, want %q", target.MasterPubkeyFile, want)
	}

	// The client refuses a master key together with --crypt-mode none, so without the
	// keyfile the master key is not used.
	dir = newPVEConfigDir(t, map[string]string{
		"proxsave-r2-enc.pw":         testSecret + "\n",
		"proxsave-r2-enc.master.pem": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n\n",
	})
	target, fact = ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-r2-enc", Hostname: testHostname, IsPVEHost: true})
	if fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	if target.Keyfile != "" || target.MasterPubkeyFile != "" {
		t.Errorf("Keyfile/MasterPubkeyFile = %q/%q, want both empty", target.Keyfile, target.MasterPubkeyFile)
	}
}

// Only the final newline of the .pw file is removed: anything else is the secret.
func TestResolvePBSTargetPasswordKeepsAllButTheFinalNewline(t *testing.T) {
	dir := newPVEConfigDir(t, map[string]string{"proxsave-probe.pw": " " + testSecret + " \n"})
	target, fact := ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true})
	if fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	if target.Password != " "+testSecret+" " {
		t.Fatalf("Password = %q", target.Password)
	}
}

func TestResolvePBSTargetRegistersThePasswordAsSecret(t *testing.T) {
	dir := newPVEConfigDir(t, map[string]string{"proxsave-probe.pw": testSecret + "\n"})
	var out bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&out)
	if _, fact := ResolvePBSTarget(ResolveInput{PVEConfigPath: dir, StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true, Logger: logger}); fact != nil {
		t.Fatalf("unexpected fact %q", fact.Line())
	}
	logger.Debug("client said: %s", testSecret)
	if strings.Contains(out.String(), testSecret) {
		t.Fatalf("the password reached the log:\n%s", out.String())
	}
}

func TestResolvePBSTargetFacts(t *testing.T) {
	full := map[string]string{"proxsave-probe.pw": testSecret + "\n", "proxsave-off.pw": testSecret + "\n"}
	for _, tc := range []struct {
		name     string
		priv     map[string]string
		noConfig bool
		in       ResolveInput
		kind     FactKind
		line     string // %s = PVE_CONFIG_PATH
	}{
		{name: "not a PVE host", priv: full, in: ResolveInput{StorageID: "proxsave-probe", Hostname: testHostname},
			kind: FactNotPVEHost, line: "Host: not a Proxmox VE host"},
		{name: "hostname unknown", priv: full, in: ResolveInput{StorageID: "proxsave-probe", Hostname: "unknown", IsPVEHost: true},
			kind: FactHostnameNotResolved, line: "Hostname: not resolved"},
		{name: "hostname empty", priv: full, in: ResolveInput{StorageID: "proxsave-probe", Hostname: " ", IsPVEHost: true},
			kind: FactHostnameNotResolved, line: "Hostname: not resolved"},
		{name: "storage.cfg missing", priv: full, noConfig: true, in: ResolveInput{StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true},
			kind: FactStorageNotFound, line: "%s/storage.cfg: proxsave-probe not found"},
		{name: "id not found", priv: full, in: ResolveInput{StorageID: "pbs-nope", Hostname: testHostname, IsPVEHost: true},
			kind: FactStorageNotFound, line: "%s/storage.cfg: pbs-nope not found"},
		{name: "wrong type", priv: full, in: ResolveInput{StorageID: "local", Hostname: testHostname, IsPVEHost: true},
			kind: FactStorageWrongType, line: "%s/storage.cfg: local is type dir, not pbs"},
		{name: "disabled", priv: full, in: ResolveInput{StorageID: "proxsave-off", Hostname: testHostname, IsPVEHost: true},
			kind: FactStorageDisabled, line: "%s/storage.cfg: proxsave-off is disabled"},
		{name: "password missing", priv: nil, in: ResolveInput{StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true},
			kind: FactPasswordMissing, line: "Password file: missing or empty"},
		{name: "password only newline", priv: map[string]string{"proxsave-probe.pw": "\n"}, in: ResolveInput{StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true},
			kind: FactPasswordMissing, line: "Password file: missing or empty"},
		{name: "password empty", priv: map[string]string{"proxsave-probe.pw": ""}, in: ResolveInput{StorageID: "proxsave-probe", Hostname: testHostname, IsPVEHost: true},
			kind: FactPasswordMissing, line: "Password file: missing or empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newPVEConfigDir(t, tc.priv)
			if tc.noConfig {
				if err := os.Remove(filepath.Join(dir, "storage.cfg")); err != nil {
					t.Fatalf("remove storage.cfg: %v", err)
				}
			}
			in := tc.in
			in.PVEConfigPath = dir
			target, fact := ResolvePBSTarget(in)
			if fact == nil {
				t.Fatalf("no fact, target %+v", target)
			}
			if fact.Kind != tc.kind {
				t.Errorf("Kind = %q, want %q", fact.Kind, tc.kind)
			}
			want := strings.Replace(tc.line, "%s", dir, 1)
			if fact.Line() != want {
				t.Errorf("Line() = %q, want %q", fact.Line(), want)
			}
			if target.Password != "" {
				t.Error("a target with a fact carries the password")
			}
		})
	}
}

func TestBuildRepository(t *testing.T) {
	for _, tc := range []struct {
		server, port, want string
	}{
		{"127.0.0.1", "", "u@pbs!t@127.0.0.1:ds"},
		{"127.0.0.1", "8007", "u@pbs!t@127.0.0.1:8007:ds"},
		{"pbs.example.lan", "", "u@pbs!t@pbs.example.lan:ds"},
		{"::1", "", "u@pbs!t@[::1]:ds"},
		{"::1", "8007", "u@pbs!t@[::1]:8007:ds"},
		{"[fd00::5]", "", "u@pbs!t@[fd00::5]:ds"},
	} {
		if got := BuildRepository("u@pbs!t", tc.server, tc.port, "ds"); got != tc.want {
			t.Errorf("BuildRepository(%q, %q) = %q, want %q", tc.server, tc.port, got, tc.want)
		}
	}
}

// keyShowJSON is the output measured on pve-test for a key without passphrase (the
// path field carries the quotes, as the client prints it).
func keyShowJSON(kdf, fingerprint string) []byte {
	return []byte(`{"fingerprint":"` + fingerprint + `","kdf":"` + kdf + `","path":"\"/etc/pve/priv/storage/proxsave-probe-enc.enc\""}`)
}

func TestEvaluateKeyShow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		output  []byte
		storage string
		want    string // "" = no fact
	}{
		{"matching key", keyShowJSON("none", testEncKeyFP), testEncKeyFP, ""},
		{"matching key, other case", keyShowJSON("none", strings.ToUpper(testEncKeyFP)), testEncKeyFP, ""},
		{"storage without encryption-key", keyShowJSON("none", testEncKeyFP), "", ""},
		{"other key", keyShowJSON("none", strings.Replace(testEncKeyFP, "5a:73", "87:18", 1)), testEncKeyFP, "Encryption key: does not match storage"},
		{"passphrase", keyShowJSON("scrypt", testEncKeyFP), testEncKeyFP, "Encryption key: needs a passphrase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fact, err := EvaluateKeyShow(tc.output, tc.storage)
			if err != nil {
				t.Fatalf("EvaluateKeyShow: %v", err)
			}
			switch {
			case tc.want == "" && fact != nil:
				t.Fatalf("unexpected fact %q", fact.Line())
			case tc.want != "" && fact == nil:
				t.Fatalf("no fact, want %q", tc.want)
			case tc.want != "" && fact.Line() != tc.want:
				t.Fatalf("Line() = %q, want %q", fact.Line(), tc.want)
			}
		})
	}
	if _, err := EvaluateKeyShow([]byte("Error: no such command 'info'"), testEncKeyFP); err == nil {
		t.Fatal("output that is not JSON was accepted")
	}
}
