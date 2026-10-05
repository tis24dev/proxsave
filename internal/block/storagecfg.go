package block

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/safefs"
)

// FactKind names one configuration fact that keeps the PBS storage from being
// initialized at startup.
type FactKind string

// The configuration facts checked by ProxSave itself, before any answer of the server.
const (
	FactNotPVEHost          FactKind = "not-pve-host"
	FactHostnameNotResolved FactKind = "hostname-not-resolved"
	FactStorageNotFound     FactKind = "storage-not-found"
	FactStorageWrongType    FactKind = "storage-wrong-type"
	FactStorageDisabled     FactKind = "storage-disabled"
	FactPasswordMissing     FactKind = "password-missing"
	FactKeyNeedsPassphrase  FactKind = "key-needs-passphrase"
	FactKeyMismatch         FactKind = "key-mismatch"
	FactClientNotFound      FactKind = "client-not-found"
)

// Fact is a configuration fact the caller prints, indented under "Path PBS: <storage>",
// before the outcome line. Nothing in this package prints it.
//
// Label and Text form the approved line "<Label>: <Text>". Err, when set, is the error
// behind the fact, for the caller's DEBUG lines; it never reaches the visible line.
type Fact struct {
	Kind  FactKind
	Label string
	Text  string
	Err   error
}

// Line returns the fact as the approved "<Label>: <Text>", without indentation.
func (f Fact) Line() string { return f.Label + ": " + f.Text }

// StorageSection is one stanza of storage.cfg: a header "<type>: <id>" followed by
// indented "<key> <value>" lines. A flag line such as "disable" has an empty value.
type StorageSection struct {
	Type       string
	ID         string
	Properties map[string]string
}

// ParseStorageConfig reads storage.cfg in the section format PVE writes. A blank line
// ends a section; a header line also ends the one before it, so a file without the final
// blank line, or without one between two sections, still parses. Comments and lines that
// fit neither form are skipped.
func ParseStorageConfig(data []byte) []StorageSection {
	var sections []StorageSection
	current := -1
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			current = -1
		case strings.HasPrefix(trimmed, "#"):
			continue
		case line[0] == ' ' || line[0] == '\t':
			if current < 0 {
				continue
			}
			key, value := trimmed, ""
			if i := strings.IndexAny(trimmed, " \t"); i >= 0 {
				key, value = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
			}
			sections[current].Properties[key] = value
		default:
			typ, id, ok := strings.Cut(trimmed, ":")
			typ, id = strings.TrimSpace(typ), strings.TrimSpace(id)
			if !ok || typ == "" || id == "" || strings.ContainsAny(typ, " \t") || strings.ContainsAny(id, " \t") {
				current = -1
				continue
			}
			sections = append(sections, StorageSection{Type: typ, ID: id, Properties: map[string]string{}})
			current = len(sections) - 1
		}
	}
	return sections
}

// PBSTarget is the PBS storage resolved from storage.cfg and the files PVE keeps for it.
// Credentials come from nowhere else.
type PBSTarget struct {
	StorageID  string
	Server     string
	Port       string // empty when the stanza has no port line (client default 8007)
	Datastore  string
	Namespace  string // empty = root namespace
	Username   string
	Repository string // <username>@<server>[:<port>]:<datastore>
	// Fingerprint is the server certificate fingerprint, empty when the stanza has none.
	Fingerprint string
	// Password is the secret of the .pw file. It goes to the client through the
	// environment only, never argv, and is registered with the logger as a secret.
	Password string
	// Keyfile is <id>.enc when PVE keeps one for the storage; empty means the upload is
	// made with --crypt-mode none.
	Keyfile string
	// MasterPubkeyFile is <id>.master.pem when it exists next to the keyfile.
	MasterPubkeyFile string
	// EncryptionKey is the "encryption-key" line of the stanza (the key's fingerprint),
	// compared with the keyfile by EvaluateKeyShow.
	EncryptionKey string
	BackupID      string // proxsave-<hostname>
}

// ResolveInput is what ResolvePBSTarget needs from the run.
type ResolveInput struct {
	PVEConfigPath string // PVE_CONFIG_PATH, /etc/pve by default
	StorageID     string // PBS_TARGET_STORAGE
	Hostname      string // FQDN as in bundle names; "unknown" when it could not be resolved
	IsPVEHost     bool
	Logger        *logging.Logger // receives the password as a secret; may be nil
}

// pbsStorageType is the storage.cfg type of a PBS storage.
const pbsStorageType = "pbs"

// unresolvedHostname is the fallback resolveHostname returns when neither "hostname -f"
// nor os.Hostname gives a name.
const unresolvedHostname = "unknown"

// ResolvePBSTarget runs the configuration checks of the PBS storage in their order: host,
// hostname, storage.cfg stanza, type, disable flag, password file; then it finds the
// keyfile and the master public key. It stops at the first fact. The checks that need the
// client (the key's passphrase and fingerprint, the server) are not made here.
func ResolvePBSTarget(in ResolveInput) (PBSTarget, *Fact) {
	if !in.IsPVEHost {
		return PBSTarget{}, &Fact{Kind: FactNotPVEHost, Label: "Host", Text: "not a Proxmox VE host"}
	}
	hostname := strings.TrimSpace(in.Hostname)
	if hostname == "" || hostname == unresolvedHostname {
		return PBSTarget{}, &Fact{Kind: FactHostnameNotResolved, Label: "Hostname", Text: "not resolved"}
	}

	cfgPath := filepath.Join(in.PVEConfigPath, "storage.cfg")
	data, err := safefs.ReadFileUnderRoot(cfgPath)
	if err != nil {
		return PBSTarget{}, &Fact{Kind: FactStorageNotFound, Label: cfgPath, Text: in.StorageID + " not found", Err: err}
	}
	section, found := findStorageSection(ParseStorageConfig(data), in.StorageID)
	if !found {
		return PBSTarget{}, &Fact{Kind: FactStorageNotFound, Label: cfgPath, Text: in.StorageID + " not found"}
	}
	if section.Type != pbsStorageType {
		return PBSTarget{}, &Fact{Kind: FactStorageWrongType, Label: cfgPath,
			Text: fmt.Sprintf("%s is type %s, not pbs", in.StorageID, section.Type)}
	}
	if value, ok := section.Properties["disable"]; ok && value != "0" {
		return PBSTarget{}, &Fact{Kind: FactStorageDisabled, Label: cfgPath, Text: in.StorageID + " is disabled"}
	}

	props := section.Properties
	target := PBSTarget{
		StorageID:     in.StorageID,
		Server:        props["server"],
		Port:          props["port"],
		Datastore:     props["datastore"],
		Namespace:     props["namespace"],
		Username:      props["username"],
		Fingerprint:   props["fingerprint"],
		EncryptionKey: props["encryption-key"],
		BackupID:      "proxsave-" + hostname,
	}
	target.Repository = BuildRepository(target.Username, target.Server, target.Port, target.Datastore)

	privDir := filepath.Join(in.PVEConfigPath, "priv", "storage")
	password, err := readPasswordFile(filepath.Join(privDir, in.StorageID+".pw"))
	if err != nil {
		return PBSTarget{}, &Fact{Kind: FactPasswordMissing, Label: "Password file", Text: "missing or empty", Err: err}
	}
	if in.Logger != nil {
		in.Logger.RegisterSecret(password)
	}
	target.Password = password

	// A keyfile that exists but cannot be examined is still passed: the client then
	// fails on it, instead of the backup silently going out unencrypted.
	keyfile := filepath.Join(privDir, in.StorageID+".enc")
	if _, err := os.Stat(keyfile); err == nil || !errors.Is(err, fs.ErrNotExist) {
		target.Keyfile = keyfile
		master := filepath.Join(privDir, in.StorageID+".master.pem")
		if _, err := os.Stat(master); err == nil {
			target.MasterPubkeyFile = master
		}
	}
	return target, nil
}

// findStorageSection returns the first stanza with the given ID.
func findStorageSection(sections []StorageSection, id string) (StorageSection, bool) {
	for _, s := range sections {
		if s.ID == id {
			return s, true
		}
	}
	return StorageSection{}, false
}

// readPasswordFile reads a PVE .pw file and removes only the final newline PVE writes.
// The read error is wrapped with the full path, since the confined read names the
// file alone.
func readPasswordFile(path string) (string, error) {
	data, err := safefs.ReadFileUnderRoot(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	password := strings.TrimSuffix(string(data), "\n")
	if password == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return password, nil
}

// BuildRepository returns the client repository <username>@<server>[:<port>]:<datastore>.
// An IPv6 server is put in square brackets when it has none; the port is written only
// when the stanza has one.
func BuildRepository(username, server, port, datastore string) string {
	host := server
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return username + "@" + host + ":" + datastore
}

// keyShowOutput is the part of "proxmox-backup-client key show <keyfile>
// --output-format json" the key check reads.
type keyShowOutput struct {
	KDF         string `json:"kdf"`
	Fingerprint string `json:"fingerprint"`
}

// EvaluateKeyShow checks the output of "key show <keyfile> --output-format json": a key
// with a passphrase (kdf other than "none") cannot be used with stdin closed, and a key
// whose fingerprint differs from the stanza's encryption-key is not the storage's key. An
// empty encryptionKey skips the comparison. The error reports output that is not the
// expected JSON.
func EvaluateKeyShow(output []byte, encryptionKey string) (*Fact, error) {
	var shown keyShowOutput
	if err := json.Unmarshal(output, &shown); err != nil {
		return nil, fmt.Errorf("parse key show output: %w", err)
	}
	if shown.KDF != "none" {
		return &Fact{Kind: FactKeyNeedsPassphrase, Label: "Encryption key", Text: "needs a passphrase",
			Err: fmt.Errorf("key show: kdf=%q", shown.KDF)}, nil
	}
	want := strings.ToLower(strings.TrimSpace(encryptionKey))
	if want != "" && strings.ToLower(strings.TrimSpace(shown.Fingerprint)) != want {
		return &Fact{Kind: FactKeyMismatch, Label: "Encryption key", Text: "does not match storage",
			Err: fmt.Errorf("key show: fingerprint=%s, storage encryption-key=%s", shown.Fingerprint, encryptionKey)}, nil
	}
	return nil, nil
}
