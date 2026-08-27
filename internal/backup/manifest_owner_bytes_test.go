package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// TestOwnerFromManifestBytes pins the reader retention attributes through, now that
// one payload has to yield TWO facts about the machine that wrote an archive. Both
// must come from the SAME payload: a hostname read from one sidecar merged with a
// server identity read from another would describe a writer that never existed, and
// retention decides deletions on it.
//
// The KEY=VALUE rows are the load-bearing ones. The pre-Go pipeline had no notion of a
// server identity and never wrote such a key, so that form must never yield one. A
// parser taught to read a SERVER_ID line would be inventing a format no archive on disk
// carries, and it could only ever fire on a hand-edited file.
func TestOwnerFromManifestBytes(t *testing.T) {
	const serverID = "1234567890123456"

	tests := []struct {
		name         string
		data         string
		wantHostname string
		wantServerID string
	}{
		{
			name:         "the JSON manifest the Go pipeline now writes",
			data:         `{"hostname":"pve.home.arpa","server_id":"1234567890123456"}`,
			wantHostname: "pve.home.arpa",
			wantServerID: serverID,
		},
		{
			name:         "a JSON manifest written before the field existed",
			data:         `{"hostname":"pve.home.arpa","created_at":"2025-01-02T10:00:00Z"}`,
			wantHostname: "pve.home.arpa",
			wantServerID: "",
		},
		{
			// The pre-Go KEY=VALUE sidecar. It names its host and can never name an
			// identity, whatever it appears to contain.
			name:         "the KEY=VALUE sidecar the pre-Go pipeline wrote",
			data:         "COMPRESSION_TYPE=gzip\nHOSTNAME=hostB\nSERVER_ID=1234567890123456\n",
			wantHostname: "hostB",
			wantServerID: "",
		},
		{
			name:         "JSON wins whenever it parses, exactly as it always has",
			data:         `{"hostname":"from-json","script_version":"HOSTNAME=from-legacy"}`,
			wantHostname: "from-json",
			wantServerID: "",
		},
		{
			// An identity with no hostname beside it names nobody. The caller must
			// never act on it: an identity is not a name, and the ownership rule
			// refuses it at its first clause.
			name:         "JSON that names an identity but no host",
			data:         `{"server_id":"1234567890123456"}`,
			wantHostname: "",
			wantServerID: serverID,
		},
		{
			name:         "garbage bytes name nobody and nothing",
			data:         "\x00\x01not a manifest at all",
			wantHostname: "",
			wantServerID: "",
		},
		{
			name:         "empty input names nobody and nothing",
			data:         "",
			wantHostname: "",
			wantServerID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostname, id := OwnerFromManifestBytes([]byte(tt.data))
			if hostname != tt.wantHostname {
				t.Errorf("hostname = %q, want %q", hostname, tt.wantHostname)
			}
			if id != tt.wantServerID {
				t.Errorf("server id = %q, want %q", id, tt.wantServerID)
			}
			// The older helper is now a wrapper, so the two can never disagree about
			// who wrote an archive. Asserted on every row rather than once, because a
			// wrapper that quietly stopped delegating is the failure being excluded.
			if got := HostnameFromManifestBytes([]byte(tt.data)); got != tt.wantHostname {
				t.Errorf("HostnameFromManifestBytes = %q, want %q; the two readers must agree on every payload", got, tt.wantHostname)
			}
		})
	}
}

// TestManifestServerIDRoundTripsAndStaysAbsentWhenEmpty pins both halves of the
// storage decision.
//
// The round trip is what makes the field usable at all: the writer records it, the
// reader gets it back unchanged. The omitempty half is what makes the change safe for
// the installed base: a manifest with no identity must serialise WITHOUT the key, so a
// host that has no identity keeps producing byte-identical manifests and nothing
// downstream sees a format change where there is nothing to record. An older binary
// reading a new manifest ignores the unknown key and reverts to hostname-only
// ownership, which is what it already does.
func TestManifestServerIDRoundTripsAndStaysAbsentWhenEmpty(t *testing.T) {
	const serverID = "1234567890123456"
	dir := t.TempDir()
	archive := filepath.Join(dir, "pve.home.arpa-backup-20250102-100000.tar.zst")
	if err := os.WriteFile(archive, []byte("archive"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	logger := logging.New(types.LogLevelNone, false)

	manifestPath := archive + ".manifest.json"
	written := &Manifest{
		ArchivePath: archive,
		CreatedAt:   time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC),
		Hostname:    "pve.home.arpa",
		ServerID:    serverID,
	}
	if err := CreateManifest(context.Background(), logger, written, manifestPath); err != nil {
		t.Fatalf("CreateManifest: %v", err)
	}
	loaded, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.ServerID != serverID {
		t.Errorf("ServerID = %q, want %q. Retention reads this field off the archive to recognise its own work across a hostname it can no longer resolve", loaded.ServerID, serverID)
	}

	bare, err := json.Marshal(&Manifest{Hostname: "pve"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(bare), "server_id") {
		t.Errorf("a manifest with no server identity serialised the key anyway: %s. The omitempty tag is load bearing: every archive written before this change has no identity, and their manifests must stay byte identical", bare)
	}
}
